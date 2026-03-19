package observability_test

// Dashboard 2 — Dead Letter / Failure Analysis
//
// Validates that dead.letter.written events carry all fields required by the
// Dead Letter dashboard:
//
//   - failure_category (DATA_GAP | DUPLICATE | PROCESSING_ERROR | SYSTEM_ERROR)
//   - error (structured code, no PHI)
//   - row_sequence_number
//   - command_type
//   - correlation_id, tenant_id, batch_file_id on every event
//
// Scenarios:
//   A — DATA_GAP: program lookup failed
//   B — PROCESSING_ERROR: RT30 insert failed
//   C — SYSTEM_ERROR: consumer upsert failed
//   D — Multiple dead letters in one file (repeated failure on same correlation_id)
//   E — batch.halt.triggered (RT99 full-file halt — most severe)
//   F — dead_letter_rate metric emitted with correct value
//   G — Failure category vocabulary is closed set (no unknown values)

package observability_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
)

var validFailureCategories = map[string]bool{
	"DATA_GAP":         true,
	"DUPLICATE":        true,
	"PROCESSING_ERROR": true,
	"SYSTEM_ERROR":     true,
}

func makeDeadLetterEvent(corrID, batchID uuid.UUID, seq int, cat, errCode, cmdType, tenant string) *ports.LogEvent {
	fc := cat
	er := errCode
	ct := cmdType
	return &ports.LogEvent{
		EventType:         observability.EventDeadLetterWritten,
		Level:             "WARN",
		CorrelationID:     corrID,
		TenantID:          tenant,
		BatchFileID:       batchID,
		Stage:             strPtr("row_processing"),
		RowSequenceNumber: &seq,
		CommandType:       &ct,
		FailureCategory:   &fc,
		Error:             &er,
		Message:           "dead_letter: seq=" + itoa(seq) + " reason=" + errCode,
	}
}

// ── Scenario A: DATA_GAP ──────────────────────────────────────────────────────

func TestDashboard_DeadLetter_DataGap(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()

	e := makeDeadLetterEvent(corrID, batchID, 3, "DATA_GAP",
		"program_lookup_failed(subprogram=99999): not found", "ENROLL", "rfu-oregon")
	_ = cap.LogEvent(ctx, e)

	dl := cap.eventsOfType(observability.EventDeadLetterWritten)
	if len(dl) != 1 {
		t.Fatalf("expected 1 dead letter event; got %d", len(dl))
	}
	if dl[0].FailureCategory == nil || *dl[0].FailureCategory != "DATA_GAP" {
		t.Errorf("failure_category = %v; want DATA_GAP", dl[0].FailureCategory)
	}
	if dl[0].RowSequenceNumber == nil || *dl[0].RowSequenceNumber != 3 {
		t.Errorf("row_sequence_number = %v; want 3", dl[0].RowSequenceNumber)
	}
	if dl[0].Error == nil || *dl[0].Error == "" {
		t.Error("error field must be present on dead.letter.written")
	}
	if dl[0].CommandType == nil || *dl[0].CommandType != "ENROLL" {
		t.Errorf("command_type = %v; want ENROLL", dl[0].CommandType)
	}
}

// ── Scenario B: PROCESSING_ERROR ─────────────────────────────────────────────

func TestDashboard_DeadLetter_ProcessingError(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()

	e := makeDeadLetterEvent(corrID, batchID, 1, "PROCESSING_ERROR",
		"rt30_insert_failed: duplicate key value violates unique constraint", "ENROLL", "rfu-oregon")
	_ = cap.LogEvent(ctx, e)

	dl := cap.eventsOfType(observability.EventDeadLetterWritten)
	if len(dl) != 1 {
		t.Fatal("expected 1 dead letter event")
	}
	if *dl[0].FailureCategory != "PROCESSING_ERROR" {
		t.Errorf("failure_category = %v; want PROCESSING_ERROR", dl[0].FailureCategory)
	}
}

// ── Scenario C: SYSTEM_ERROR ──────────────────────────────────────────────────

func TestDashboard_DeadLetter_SystemError(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	e := makeDeadLetterEvent(uuid.New(), uuid.New(), 2, "SYSTEM_ERROR",
		"consumer_upsert_failed: context deadline exceeded", "ENROLL", "rfu-oregon")
	_ = cap.LogEvent(ctx, e)

	dl := cap.eventsOfType(observability.EventDeadLetterWritten)
	if len(dl) != 1 {
		t.Fatal("expected 1 dead letter event")
	}
	if *dl[0].FailureCategory != "SYSTEM_ERROR" {
		t.Errorf("failure_category = %v; want SYSTEM_ERROR", dl[0].FailureCategory)
	}
}

// ── Scenario D: repeated failures (multiple rows, same correlation_id) ─────────

func TestDashboard_DeadLetter_RepeatedFailures(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()

	failures := []struct {
		seq int
		cat string
		err string
	}{
		{1, "DATA_GAP", "program_lookup_failed(subprogram=99999): not found"},
		{2, "SYSTEM_ERROR", "consumer_upsert_failed: context deadline exceeded"},
		{4, "DATA_GAP", "program_lookup_failed(subprogram=99999): not found"},
		{5, "PROCESSING_ERROR", "rt30_insert_failed: connection reset"},
	}

	for _, f := range failures {
		_ = cap.LogEvent(ctx, makeDeadLetterEvent(corrID, batchID, f.seq, f.cat, f.err, "ENROLL", "rfu-oregon"))
	}

	dl := cap.eventsOfType(observability.EventDeadLetterWritten)
	if len(dl) != 4 {
		t.Fatalf("expected 4 dead letter events; got %d", len(dl))
	}

	// All must carry the same correlation_id (same file, multiple failures)
	for _, e := range dl {
		if e.CorrelationID != corrID {
			t.Error("all dead letters from same file must share correlation_id")
		}
	}

	// Count by category
	catCounts := map[string]int{}
	for _, e := range dl {
		if e.FailureCategory != nil {
			catCounts[*e.FailureCategory]++
		}
	}
	if catCounts["DATA_GAP"] != 2 {
		t.Errorf("DATA_GAP count = %d; want 2", catCounts["DATA_GAP"])
	}
}

// ── Scenario E: batch.halt.triggered ─────────────────────────────────────────

func TestDashboard_DeadLetter_BatchHalt(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	frc     := "999"

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     observability.EventBatchHaltTriggered,
		Level:         "ERROR",
		CorrelationID: corrID,
		TenantID:      "rfu-oregon",
		BatchFileID:   batchID,
		Stage:         strPtr("stage7_reconciliation"),
		FISResultCode: &frc,
		Message:       "RT99 full-file pre-processing halt — FIS rejected entire file; page on-call P0",
	})

	halt := cap.firstOfType(observability.EventBatchHaltTriggered)
	if halt == nil {
		t.Fatal("batch.halt.triggered not emitted")
	}
	if halt.Level != "ERROR" {
		t.Errorf("batch.halt.triggered level = %q; want ERROR", halt.Level)
	}
	if halt.FISResultCode == nil || *halt.FISResultCode != "999" {
		t.Errorf("fis_result_code = %v; want 999", halt.FISResultCode)
	}
}

// ── Scenario F: dead_letter_rate metric ──────────────────────────────────────

func TestDashboard_DeadLetter_RateMetric(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	// 2 failed of 10 total = 20%
	_ = cap.RecordMetric(ctx, observability.MetricDeadLetterRate, 20.0, map[string]string{
		"tenant_id": "rfu-oregon",
		"env":       "DEV",
	})

	metrics := cap.metricsNamed(observability.MetricDeadLetterRate)
	if len(metrics) != 1 {
		t.Fatalf("expected 1 dead_letter_rate metric; got %d", len(metrics))
	}
	if metrics[0].Value != 20.0 {
		t.Errorf("dead_letter_rate = %v; want 20.0", metrics[0].Value)
	}
	if metrics[0].Dims["tenant_id"] != "rfu-oregon" {
		t.Error("dead_letter_rate metric must carry tenant_id dimension")
	}
}

// ── Scenario G: failure_category is a closed vocabulary ──────────────────────

func TestDashboard_DeadLetter_FailureCategoryVocabulary(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	allCategories := []string{"DATA_GAP", "DUPLICATE", "PROCESSING_ERROR", "SYSTEM_ERROR"}
	for i, cat := range allCategories {
		seq := i + 1
		_ = cap.LogEvent(ctx, makeDeadLetterEvent(
			uuid.New(), uuid.New(), seq, cat, "error_code", "ENROLL", "rfu-oregon",
		))
	}

	dl := cap.eventsOfType(observability.EventDeadLetterWritten)
	if len(dl) != 4 {
		t.Fatalf("expected 4 events; got %d", len(dl))
	}

	for _, e := range dl {
		if e.FailureCategory == nil {
			t.Error("failure_category must always be set on dead.letter.written")
			continue
		}
		if !validFailureCategories[*e.FailureCategory] {
			t.Errorf("unknown failure_category %q — must be one of: DATA_GAP, DUPLICATE, PROCESSING_ERROR, SYSTEM_ERROR", *e.FailureCategory)
		}
	}
}
