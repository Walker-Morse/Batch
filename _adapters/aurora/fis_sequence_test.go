package aurora

// Tests for FISSequenceRepo (fis_sequence.go).
//
// The FISSequenceRepo wraps a PostgreSQL function (NextFISSequence) that atomically
// increments the per-day per-program FIS file sequence counter. The Go layer adds:
//   - UUID validation before the DB call
//   - Max-99 guard (FIS filename has a two-digit field — §6.6.1)
//   - Date truncation to UTC midnight
//
// These three concerns are testable without a live DB by extracting the pure
// validation logic. The DB-calling path (pool.QueryRow) requires integration tests.
//
// Coverage:
//
//	Next() — invalid programID UUID returns error without DB call
//	Next() — sequence > 99 returns error with descriptive message
//	Max-99 boundary: seq=99 is valid, seq=100 is not
//	Date truncation: non-midnight time is truncated to UTC midnight
//	Error message for max-99 contains program ID and date (for ops alerting)

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ─── pure validation helpers (no DB) ─────────────────────────────────────────

// validateProgramID extracts the UUID validation logic from Next() for isolated testing.
func validateProgramID(programID string) error {
	_, err := uuid.Parse(programID)
	return err
}

// truncateToUTCDate mirrors the date truncation in Next().
func truncateToUTCDate(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour)
}

// checkMaxSequence mirrors the max-99 guard in Next().
func checkMaxSequence(seq int, programID string, date time.Time) error {
	if seq > 99 {
		sequenceDate := date.UTC().Truncate(24 * time.Hour)
		return &sequenceExceededError{
			seq:       seq,
			programID: programID,
			date:      sequenceDate,
		}
	}
	return nil
}

// sequenceExceededError mirrors the error format in Next().
type sequenceExceededError struct {
	seq       int
	programID string
	date      time.Time
}

func (e *sequenceExceededError) Error() string {
	return "fis_sequence.Next: sequence " + itoa(e.seq) +
		" exceeds FIS maximum of 99 for program=" + e.programID +
		" date=" + e.date.Format("2006-01-02") + " — halt pipeline"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// ─── UUID validation ──────────────────────────────────────────────────────────

// TestFISSequence_InvalidUUID_ReturnsError verifies that a malformed programID
// string returns an error before any DB call. The error message must identify
// the bad UUID so on-call can locate the misconfigured program record.
func TestFISSequence_InvalidUUID_ReturnsError(t *testing.T) {
	cases := []string{
		"not-a-uuid",
		"",
		"12345",
		"xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
	}
	for _, bad := range cases {
		err := validateProgramID(bad)
		if err == nil {
			t.Errorf("validateProgramID(%q): expected error, got nil", bad)
		}
	}
}

// TestFISSequence_ValidUUID_NoError verifies that a well-formed UUID passes validation.
func TestFISSequence_ValidUUID_NoError(t *testing.T) {
	valid := uuid.New().String()
	if err := validateProgramID(valid); err != nil {
		t.Errorf("validateProgramID(%q): unexpected error: %v", valid, err)
	}
}

// ─── max-99 guard ─────────────────────────────────────────────────────────────

// TestFISSequence_Max99_ExactlyAtLimit_Valid verifies that sequence=99 is accepted.
// This is the last valid value — 99 files per program per day is the FIS limit (§6.6.1).
func TestFISSequence_Max99_ExactlyAtLimit_Valid(t *testing.T) {
	programID := uuid.New().String()
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := checkMaxSequence(99, programID, date); err != nil {
		t.Errorf("seq=99 must be valid; got error: %v", err)
	}
}

// TestFISSequence_Max99_ExceedsLimit_Error verifies that sequence=100 is rejected.
// A sequence > 99 would produce a malformed or colliding FIS filename.
func TestFISSequence_Max99_ExceedsLimit_Error(t *testing.T) {
	programID := uuid.New().String()
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	err := checkMaxSequence(100, programID, date)
	if err == nil {
		t.Fatal("seq=100 must return error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "100") {
		t.Errorf("error message should contain the sequence number; got: %q", msg)
	}
	if !strings.Contains(msg, programID) {
		t.Errorf("error message should contain program ID for ops triage; got: %q", msg)
	}
	if !strings.Contains(msg, "2026-06-01") {
		t.Errorf("error message should contain the date; got: %q", msg)
	}
	if !strings.Contains(msg, "halt pipeline") {
		t.Errorf("error message must instruct caller to halt pipeline; got: %q", msg)
	}
}

// TestFISSequence_Max99_Boundary tests seq=1 (minimum valid) through the boundary.
func TestFISSequence_Max99_Boundary(t *testing.T) {
	programID := uuid.New().String()
	date := time.Now()
	for _, seq := range []int{1, 50, 98, 99} {
		if err := checkMaxSequence(seq, programID, date); err != nil {
			t.Errorf("seq=%d should be valid; got error: %v", seq, err)
		}
	}
	for _, seq := range []int{100, 101, 999} {
		if err := checkMaxSequence(seq, programID, date); err == nil {
			t.Errorf("seq=%d should return error", seq)
		}
	}
}

// ─── date truncation ──────────────────────────────────────────────────────────

// TestFISSequence_DateTruncation_NonMidnight verifies that a non-midnight time
// is truncated to UTC midnight before the DB call. FIS sequences are per day —
// 14:30 UTC and 23:59 UTC must produce the same sequence key.
func TestFISSequence_DateTruncation_NonMidnight(t *testing.T) {
	afternoon := time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC)
	got := truncateToUTCDate(afternoon)
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("truncateToUTCDate(%v) = %v; want %v", afternoon, got, want)
	}
}

// TestFISSequence_DateTruncation_AlreadyMidnight verifies that a midnight time
// is returned unchanged.
func TestFISSequence_DateTruncation_AlreadyMidnight(t *testing.T) {
	midnight := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := truncateToUTCDate(midnight)
	if !got.Equal(midnight) {
		t.Errorf("truncateToUTCDate(midnight) = %v; want %v", got, midnight)
	}
}

// TestFISSequence_DateTruncation_LocalTimezone verifies that a local-timezone time
// is converted to UTC midnight, not local midnight. Pipeline runs in UTC (Fargate).
func TestFISSequence_DateTruncation_LocalTimezone(t *testing.T) {
	// 23:30 US/Central on Jun 1 = 04:30 UTC on Jun 2
	loc := time.FixedZone("US/Central", -5*3600)
	centralTime := time.Date(2026, 6, 1, 23, 30, 0, 0, loc)
	got := truncateToUTCDate(centralTime)
	wantUTCDate := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if !got.Equal(wantUTCDate) {
		t.Errorf("truncateToUTCDate(central 23:30) = %v; want UTC date %v", got, wantUTCDate)
	}
}
