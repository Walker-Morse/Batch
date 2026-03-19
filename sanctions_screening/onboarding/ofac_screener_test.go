package onboarding_test

// Tests for ScreenBatch — OFAC-API.com pre-pipeline batch screening.
//
// All tests use httptest.NewServer to avoid real network calls.
// The fake server validates request shape and returns controlled responses.
//
// Coverage:
//   - Happy path: clear member → IsHit=false, MatchCount=0
//   - Hit detected: matchCount > 0 → IsHit=true, Score populated
//   - Mixed batch: some clear, some hit — results keyed by case ID
//   - Empty batch: returns empty map, no HTTP call
//   - Batch exceeds 500: error before HTTP call
//   - HTTP error (connection refused): error returned
//   - Non-200 status: error returned with status code
//   - Malformed JSON response: error returned
//   - Request carries correct sources and minScore
//   - Context cancellation propagates

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/walker-morse/batch/sanctions_screening/onboarding"
)

// ─── fake server helpers ──────────────────────────────────────────────────────

// ofacFakeResponse mirrors the OFAC-API.com v4 response shape used by ScreenBatch.
type ofacFakeResponse struct {
	Results []ofacFakeResult `json:"results"`
}

type ofacFakeResult struct {
	ID      string          `json:"id"`
	Matches []ofacFakeMatch `json:"matches"`
}

type ofacFakeMatch struct {
	Score int `json:"score"`
}

// ofacFakeRequest mirrors the request body sent by ScreenBatch.
type ofacFakeRequest struct {
	APIKey   string   `json:"apiKey"`
	MinScore int      `json:"minScore"`
	Sources  []string `json:"sources"`
	Cases    []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		DOB  string `json:"dob"`
		Type string `json:"type"`
	} `json:"cases"`
}

// newFakeOFACServer returns an httptest.Server that responds with the given results
// and captures the last parsed request body for assertion.
func newFakeOFACServer(t *testing.T, statusCode int, resp ofacFakeResponse) (*httptest.Server, *ofacFakeRequest) {
	t.Helper()
	captured := &ofacFakeRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(captured); err != nil {
			t.Errorf("fake server: decode request: %v", err)
		}
		w.WriteHeader(statusCode)
		if statusCode == http.StatusOK {
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			_, _ = w.Write([]byte("server error"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

func newScreener(t *testing.T, srv *httptest.Server) *onboarding.Screener {
	t.Helper()
	return onboarding.NewScreenerWithEndpoint("test-api-key", srv.URL, srv.Client())
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestScreenBatch_ClearMember(t *testing.T) {
	srv, _ := newFakeOFACServer(t, http.StatusOK, ofacFakeResponse{
		Results: []ofacFakeResult{
			{ID: "MBR-001", Matches: nil},
		},
	})
	s := newScreener(t, srv)

	results, err := s.ScreenBatch(context.Background(), []onboarding.ScreeningCase{
		{ID: "MBR-001", Name: "Jane Smith", DOB: "1985-04-12", Type: "person"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r, ok := results["MBR-001"]
	if !ok {
		t.Fatal("expected result for MBR-001")
	}
	if r.IsHit {
		t.Error("IsHit = true; want false for clear member")
	}
	if r.MatchCount != 0 {
		t.Errorf("MatchCount = %d; want 0", r.MatchCount)
	}
}

func TestScreenBatch_HitDetected(t *testing.T) {
	srv, _ := newFakeOFACServer(t, http.StatusOK, ofacFakeResponse{
		Results: []ofacFakeResult{
			{ID: "MBR-999", Matches: []ofacFakeMatch{{Score: 97}}},
		},
	})
	s := newScreener(t, srv)

	results, err := s.ScreenBatch(context.Background(), []onboarding.ScreeningCase{
		{ID: "MBR-999", Name: "Blocked Person", DOB: "1970-01-01", Type: "person"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r, ok := results["MBR-999"]
	if !ok {
		t.Fatal("expected result for MBR-999")
	}
	if !r.IsHit {
		t.Error("IsHit = false; want true for OFAC hit")
	}
	if r.MatchCount != 1 {
		t.Errorf("MatchCount = %d; want 1", r.MatchCount)
	}
	if r.Score != 97 {
		t.Errorf("Score = %d; want 97", r.Score)
	}
}

func TestScreenBatch_MixedBatch(t *testing.T) {
	srv, _ := newFakeOFACServer(t, http.StatusOK, ofacFakeResponse{
		Results: []ofacFakeResult{
			{ID: "MBR-001", Matches: nil},
			{ID: "MBR-002", Matches: []ofacFakeMatch{{Score: 99}}},
			{ID: "MBR-003", Matches: nil},
		},
	})
	s := newScreener(t, srv)

	cases := []onboarding.ScreeningCase{
		{ID: "MBR-001", Name: "Clear One"},
		{ID: "MBR-002", Name: "Hit Person"},
		{ID: "MBR-003", Name: "Clear Three"},
	}
	results, err := s.ScreenBatch(context.Background(), cases)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d; want 3", len(results))
	}
	if results["MBR-001"].IsHit {
		t.Error("MBR-001: IsHit = true; want false")
	}
	if !results["MBR-002"].IsHit {
		t.Error("MBR-002: IsHit = false; want true")
	}
	if results["MBR-003"].IsHit {
		t.Error("MBR-003: IsHit = true; want false")
	}
}

func TestScreenBatch_EmptyBatch_NoHTTPCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	s := newScreener(t, srv)

	results, err := s.ScreenBatch(context.Background(), nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("len(results) = %d; want 0", len(results))
	}
	if called {
		t.Error("HTTP call made for empty batch; want no call")
	}
}

func TestScreenBatch_ExceedsMaxBatchSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	s := newScreener(t, srv)

	cases := make([]onboarding.ScreeningCase, onboarding.BatchSize+1)
	for i := range cases {
		cases[i] = onboarding.ScreeningCase{ID: "MBR", Name: "test"}
	}

	_, err := s.ScreenBatch(context.Background(), cases)

	if err == nil {
		t.Fatal("expected error for oversized batch; got nil")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Errorf("error = %q; want 'exceeds maximum'", err)
	}
}

func TestScreenBatch_Non200Status(t *testing.T) {
	srv, _ := newFakeOFACServer(t, http.StatusUnauthorized, ofacFakeResponse{})
	s := newScreener(t, srv)

	_, err := s.ScreenBatch(context.Background(), []onboarding.ScreeningCase{
		{ID: "MBR-001", Name: "Test"},
	})

	if err == nil {
		t.Fatal("expected error for non-200 status; got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error = %q; want status 401 in message", err)
	}
}

func TestScreenBatch_MalformedJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json {{{"))
	}))
	defer srv.Close()
	s := newScreener(t, srv)

	_, err := s.ScreenBatch(context.Background(), []onboarding.ScreeningCase{
		{ID: "MBR-001", Name: "Test"},
	})

	if err == nil {
		t.Fatal("expected error for malformed JSON; got nil")
	}
}

func TestScreenBatch_RequestCarriesCorrectSources(t *testing.T) {
	srv, captured := newFakeOFACServer(t, http.StatusOK, ofacFakeResponse{
		Results: []ofacFakeResult{{ID: "MBR-001"}},
	})
	s := newScreener(t, srv)

	_, _ = s.ScreenBatch(context.Background(), []onboarding.ScreeningCase{
		{ID: "MBR-001", Name: "Test"},
	})

	// Verify all four MVB-required sources are sent
	wantSources := map[string]bool{"SDN": true, "NONSDN": true, "FINCEN": true, "UN": true}
	for _, src := range captured.Sources {
		delete(wantSources, src)
	}
	if len(wantSources) > 0 {
		t.Errorf("missing required sources: %v", wantSources)
	}
	if captured.MinScore != onboarding.MinScore {
		t.Errorf("minScore = %d; want %d", captured.MinScore, onboarding.MinScore)
	}
}

func TestScreenBatch_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang — context should cancel before response
		<-r.Context().Done()
	}))
	defer srv.Close()
	s := newScreener(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := s.ScreenBatch(ctx, []onboarding.ScreeningCase{
		{ID: "MBR-001", Name: "Test"},
	})

	if err == nil {
		t.Fatal("expected error for cancelled context; got nil")
	}
}
