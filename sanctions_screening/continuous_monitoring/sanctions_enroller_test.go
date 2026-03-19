package continuous_monitoring_test

// Tests for Enroll — sanctions.io Continuous Monitoring enrollment.
//
// All tests use httptest.NewServer to avoid real network calls.
//
// Coverage:
//   - Happy path: 200 OK with active status → monitorID returned
//   - Happy path: 201 Created (some implementations return 201)
//   - Non-200/201 status: error returned
//   - Malformed JSON response: error returned
//   - Response status not "active": error returned
//   - Authorization header sent with Bearer token
//   - Subject fields correctly serialised (id, first_name, last_name, dob, type)
//   - webhook_url included in request body
//   - Context cancellation propagates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cm "github.com/walker-morse/batch/sanctions_screening/continuous_monitoring"
)

// ─── fake server helpers ──────────────────────────────────────────────────────

type fakeEnrollRequest struct {
	Subject struct {
		ID        string `json:"id"`
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		DOB       string `json:"dob"`
		Type      string `json:"type"`
	} `json:"subject"`
	WebhookURL string `json:"webhook_url"`
}

type fakeEnrollResponse struct {
	MonitorID string `json:"monitor_id"`
	Status    string `json:"status"`
}

func newFakeEnrollServer(t *testing.T, statusCode int, resp fakeEnrollResponse) (*httptest.Server, *fakeEnrollRequest, *http.Header) {
	t.Helper()
	captured := &fakeEnrollRequest{}
	capturedHeaders := &http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*capturedHeaders = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Errorf("fake server: decode request: %v", err)
		}
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK || statusCode == http.StatusCreated {
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			_, _ = w.Write([]byte("unauthorized"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, captured, capturedHeaders
}

func newEnroller(t *testing.T, srv *httptest.Server) *cm.Enroller {
	t.Helper()
	return cm.NewEnrollerWithEndpoint("test-api-key", "https://webhook.oneFintech.test/sanctions", srv.URL, srv.Client())
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestEnroll_HappyPath_200(t *testing.T) {
	srv, _, _ := newFakeEnrollServer(t, http.StatusOK, fakeEnrollResponse{
		MonitorID: "mon_abc123",
		Status:    "active",
	})
	e := newEnroller(t, srv)

	monitorID, err := e.Enroll(context.Background(), "MBR-001", "Jane", "Smith", "1985-04-12")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if monitorID != "mon_abc123" {
		t.Errorf("monitorID = %q; want mon_abc123", monitorID)
	}
}

func TestEnroll_HappyPath_201Created(t *testing.T) {
	srv, _, _ := newFakeEnrollServer(t, http.StatusCreated, fakeEnrollResponse{
		MonitorID: "mon_xyz789",
		Status:    "active",
	})
	e := newEnroller(t, srv)

	monitorID, err := e.Enroll(context.Background(), "MBR-002", "John", "Doe", "1990-11-30")

	if err != nil {
		t.Fatalf("unexpected error on 201 Created: %v", err)
	}
	if monitorID != "mon_xyz789" {
		t.Errorf("monitorID = %q; want mon_xyz789", monitorID)
	}
}

func TestEnroll_Non200Status(t *testing.T) {
	srv, _, _ := newFakeEnrollServer(t, http.StatusUnauthorized, fakeEnrollResponse{})
	e := newEnroller(t, srv)

	_, err := e.Enroll(context.Background(), "MBR-001", "Jane", "Smith", "1985-04-12")

	if err == nil {
		t.Fatal("expected error for 401; got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q; want 401 in message", err)
	}
}

func TestEnroll_MalformedJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	e := newEnroller(t, srv)

	_, err := e.Enroll(context.Background(), "MBR-001", "Jane", "Smith", "1985-04-12")

	if err == nil {
		t.Fatal("expected error for malformed JSON; got nil")
	}
}

func TestEnroll_InactiveStatus(t *testing.T) {
	srv, _, _ := newFakeEnrollServer(t, http.StatusOK, fakeEnrollResponse{
		MonitorID: "mon_pending",
		Status:    "pending", // not "active"
	})
	e := newEnroller(t, srv)

	_, err := e.Enroll(context.Background(), "MBR-001", "Jane", "Smith", "1985-04-12")

	if err == nil {
		t.Fatal("expected error for non-active status; got nil")
	}
	if !strings.Contains(err.Error(), "pending") {
		t.Errorf("error = %q; want 'pending' in message", err)
	}
}

func TestEnroll_AuthorizationHeaderSent(t *testing.T) {
	srv, _, capturedHeaders := newFakeEnrollServer(t, http.StatusOK, fakeEnrollResponse{
		MonitorID: "mon_x",
		Status:    "active",
	})
	e := newEnroller(t, srv)

	_, _ = e.Enroll(context.Background(), "MBR-001", "Jane", "Smith", "1985-04-12")

	auth := capturedHeaders.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("Authorization = %q; want 'Bearer <token>'", auth)
	}
	if !strings.Contains(auth, "test-api-key") {
		t.Errorf("Authorization = %q; want API key in token", auth)
	}
}

func TestEnroll_SubjectFieldsCorrect(t *testing.T) {
	srv, captured, _ := newFakeEnrollServer(t, http.StatusOK, fakeEnrollResponse{
		MonitorID: "mon_x",
		Status:    "active",
	})
	e := newEnroller(t, srv)

	_, _ = e.Enroll(context.Background(), "MBR-007", "Alice", "Walker", "1992-03-15")

	if captured.Subject.ID != "MBR-007" {
		t.Errorf("subject.id = %q; want MBR-007", captured.Subject.ID)
	}
	if captured.Subject.FirstName != "Alice" {
		t.Errorf("subject.first_name = %q; want Alice", captured.Subject.FirstName)
	}
	if captured.Subject.LastName != "Walker" {
		t.Errorf("subject.last_name = %q; want Walker", captured.Subject.LastName)
	}
	if captured.Subject.DOB != "1992-03-15" {
		t.Errorf("subject.dob = %q; want 1992-03-15", captured.Subject.DOB)
	}
	if captured.Subject.Type != "individual" {
		t.Errorf("subject.type = %q; want individual", captured.Subject.Type)
	}
}

func TestEnroll_WebhookURLInRequest(t *testing.T) {
	srv, captured, _ := newFakeEnrollServer(t, http.StatusOK, fakeEnrollResponse{
		MonitorID: "mon_x",
		Status:    "active",
	})
	e := newEnroller(t, srv)

	_, _ = e.Enroll(context.Background(), "MBR-001", "Jane", "Smith", "1985-04-12")

	if captured.WebhookURL != "https://webhook.oneFintech.test/sanctions" {
		t.Errorf("webhook_url = %q; want https://webhook.oneFintech.test/sanctions", captured.WebhookURL)
	}
}

func TestEnroll_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	e := newEnroller(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.Enroll(ctx, "MBR-001", "Jane", "Smith", "1985-04-12")

	if err == nil {
		t.Fatal("expected error for cancelled context; got nil")
	}
}
