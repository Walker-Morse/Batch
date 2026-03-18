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
	"context"
	"fmt"
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
	APIKey string // from Secrets Manager — never from env vars
}

// ScreenBatch submits up to 500 members to OFAC-API.com.
// Returns results indexed by case ID (client_member_id).
func (s *Screener) ScreenBatch(ctx context.Context, cases []ScreeningCase) (map[string]*ScreeningResult, error) {
	if len(cases) > BatchSize {
		return nil, fmt.Errorf("batch size %d exceeds maximum %d", len(cases), BatchSize)
	}
	// TODO: HTTP POST to OFACAPIEndpoint with sources, minScore, cases
	// TODO: parse response, map results by case.id
	return nil, fmt.Errorf("not implemented")
}
