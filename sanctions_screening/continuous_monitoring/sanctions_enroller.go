// Package continuous_monitoring implements the sanctions.io enrollment at Stage 7.
//
// Called when FIS return file confirms RT30 Completed (card physically issued).
// After enrollment, sanctions.io monitors the member against its global watchlist
// (updated every 60 minutes) and pushes webhook alerts on list updates.
//
// This satisfies MVB § 10.3 "rescreen whenever lists are updated" precisely —
// no scheduled batch job, no cadence approximation, no exposure windows.
package continuous_monitoring

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	sanctionsIOEnrollEndpoint = "https://api.sanctions.io/v1/monitor"
)

// Enroller enrolls confirmed members in sanctions.io Continuous Monitoring.
type Enroller struct {
	APIKey     string // from Secrets Manager
	WebhookURL string // One Fintech-hosted webhook endpoint registered with sanctions.io
	HTTPClient *http.Client
	// endpoint is overridable for tests
	endpoint string
}

// NewEnroller returns a production Enroller targeting sanctions.io.
func NewEnroller(apiKey, webhookURL string) *Enroller {
	return &Enroller{
		APIKey:     apiKey,
		WebhookURL: webhookURL,
		HTTPClient: &http.Client{},
		endpoint:   sanctionsIOEnrollEndpoint,
	}
}

// NewEnrollerWithEndpoint returns an Enroller pointing at a custom endpoint.
// Used in tests to target an httptest.Server.
func NewEnrollerWithEndpoint(apiKey, webhookURL, endpoint string, client *http.Client) *Enroller {
	return &Enroller{
		APIKey:     apiKey,
		WebhookURL: webhookURL,
		HTTPClient: client,
		endpoint:   endpoint,
	}
}

// ─── wire types (sanctions.io v1 schema) ─────────────────────────────────────

type enrollRequest struct {
	Subject    enrollSubject `json:"subject"`
	WebhookURL string        `json:"webhook_url"`
}

type enrollSubject struct {
	ID        string `json:"id"`   // client_member_id — for webhook alert correlation
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	DOB       string `json:"dob,omitempty"` // YYYY-MM-DD
	Type      string `json:"type"`          // always "individual"
}

type enrollResponse struct {
	MonitorID string `json:"monitor_id"`
	Status    string `json:"status"` // "active" on success
}

// ─── Enroll ──────────────────────────────────────────────────────────────────

// Enroll registers a confirmed member with sanctions.io after RT30 completion.
// Called once per member per Stage 7 RT30 COMPLETED confirmation.
// Returns the sanctions.io monitor_id for audit logging.
//
// A non-nil error must be logged as a compliance gap — the member is active in
// One Fintech but not enrolled in continuous monitoring. Ops must retry via
// the replay-cli before the next business day.
func (e *Enroller) Enroll(ctx context.Context, clientMemberID, firstName, lastName, dob string) (monitorID string, err error) {
	payload := enrollRequest{
		Subject: enrollSubject{
			ID:        clientMemberID,
			FirstName: firstName,
			LastName:  lastName,
			DOB:       dob,
			Type:      "individual",
		},
		WebhookURL: e.WebhookURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("sanctions_enroller: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("sanctions_enroller: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("sanctions_enroller: http POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("sanctions_enroller: unexpected status %d: %s", resp.StatusCode, snippet)
	}

	var enrollResp enrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&enrollResp); err != nil {
		return "", fmt.Errorf("sanctions_enroller: decode response: %w", err)
	}
	if enrollResp.Status != "active" {
		return "", fmt.Errorf("sanctions_enroller: unexpected status in response %q; want active", enrollResp.Status)
	}

	return enrollResp.MonitorID, nil
}
