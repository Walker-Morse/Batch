// Package onboarding implements the OFAC-API.com pre-pipeline batch screening.
//
// Triggered by the pre-pipeline screening Lambda on S3 file arrival.
// MUST complete before Stage 1 of the ingest-task begins.
// The ingest-task receives only the cleared member set.
//
// API: POST https://api.ofac-api.com/v4/screen
// Batch size: up to 500 cases per request
// Sources: ["SDN", "NONSDN", "FINCEN", "UN"]
// minScore: 95 (US Treasury fuzzy matching guideline default)
// case.id: set to client_member_id for direct SRG310 row mapping
//
// All screening results (including negatives) written to ofac_screening_results
// for audit retention before any pipeline state is touched.
// Any matchCount > 0: write to dead_letter_store with reason_code = OFAC_HIT.
package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	OFACAPIEndpoint = "https://api.ofac-api.com/v4/screen"
	BatchSize       = 500 // max cases per request per OFAC-API.com docs
	MinScore        = 95  // US Treasury fuzzy matching guideline
)

// RequiredSources are the four lists mandated by MVB Program Standards § 10.3.1.
var RequiredSources = []string{"SDN", "NONSDN", "FINCEN", "UN"}

// ScreeningCase is one member submission to the OFAC-API.com cases[] array.
type ScreeningCase struct {
	ID   string // client_member_id — maps results back to SRG310 rows
	Name string
	DOB  string // YYYY-MM-DD
	Type string // always "person"
}

// ScreeningResult is the parsed response for one member.
type ScreeningResult struct {
	CaseID     string // client_member_id
	MatchCount int
	IsHit      bool
	Score      int
}

// Screener submits member batches to OFAC-API.com.
type Screener struct {
	APIKey     string // from Secrets Manager — never from env vars
	HTTPClient *http.Client
	// endpoint is the API URL; overridable in tests via NewScreenerWithEndpoint.
	endpoint string
}

// NewScreener returns a production Screener using the default OFAC-API endpoint.
func NewScreener(apiKey string) *Screener {
	return &Screener{
		APIKey:     apiKey,
		HTTPClient: &http.Client{},
		endpoint:   OFACAPIEndpoint,
	}
}

// NewScreenerWithEndpoint returns a Screener pointing at a custom endpoint.
// Used in tests to target an httptest.Server.
func NewScreenerWithEndpoint(apiKey, endpoint string, client *http.Client) *Screener {
	return &Screener{APIKey: apiKey, HTTPClient: client, endpoint: endpoint}
}

// ─── wire types (OFAC-API.com v4 schema) ─────────────────────────────────────

type ofacRequest struct {
	APIKey   string      `json:"apiKey"`
	MinScore int         `json:"minScore"`
	Sources  []string    `json:"sources"`
	Cases    []ofacCase  `json:"cases"`
}

type ofacCase struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	DOB  string `json:"dob,omitempty"`
	Type string `json:"type"`
}

type ofacResponse struct {
	Results []ofacResult `json:"results"`
}

type ofacResult struct {
	ID         string      `json:"id"`
	Matches    []ofacMatch `json:"matches"`
}

type ofacMatch struct {
	Score int `json:"score"`
}

// ─── ScreenBatch ─────────────────────────────────────────────────────────────

// ScreenBatch submits up to 500 members to OFAC-API.com.
// Returns results indexed by case ID (client_member_id).
// A non-nil error means the HTTP round-trip or response parsing failed —
// the caller must treat the entire batch as unscreened and halt the pipeline.
func (s *Screener) ScreenBatch(ctx context.Context, cases []ScreeningCase) (map[string]*ScreeningResult, error) {
	if len(cases) == 0 {
		return map[string]*ScreeningResult{}, nil
	}
	if len(cases) > BatchSize {
		return nil, fmt.Errorf("ofac_screener: batch size %d exceeds maximum %d", len(cases), BatchSize)
	}

	// Build request payload
	req := ofacRequest{
		APIKey:   s.APIKey,
		MinScore: MinScore,
		Sources:  RequiredSources,
		Cases:    make([]ofacCase, len(cases)),
	}
	for i, c := range cases {
		req.Cases[i] = ofacCase{
			ID:   c.ID,
			Name: c.Name,
			DOB:  c.DOB,
			Type: "person",
		}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ofac_screener: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ofac_screener: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ofac_screener: http POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("ofac_screener: unexpected status %d: %s", resp.StatusCode, snippet)
	}

	var ofacResp ofacResponse
	if err := json.NewDecoder(resp.Body).Decode(&ofacResp); err != nil {
		return nil, fmt.Errorf("ofac_screener: decode response: %w", err)
	}

	// Build result map indexed by case ID
	results := make(map[string]*ScreeningResult, len(ofacResp.Results))
	for _, r := range ofacResp.Results {
		sr := &ScreeningResult{
			CaseID:     r.ID,
			MatchCount: len(r.Matches),
			IsHit:      len(r.Matches) > 0,
		}
		// Report the highest match score (first match is highest per OFAC-API.com docs)
		if len(r.Matches) > 0 {
			sr.Score = r.Matches[0].Score
		}
		results[r.ID] = sr
	}

	return results, nil
}
