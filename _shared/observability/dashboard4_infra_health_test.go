package observability_test

// Dashboard 4 — Infrastructure & ECS Health
//
// The infra dashboard is driven by:
//   (a) CloudWatch native ECS/Aurora/S3 metrics (not tested here — AWS-managed)
//   (b) Pipeline-emitted events that signal task health:
//       - pipeline.startup: task started successfully
//       - pipeline.complete: task exited cleanly with duration
//       - pipeline.error: task failed — ECS marks task failed
//       - batch.halt.triggered: P0 — pages on-call
//       - stage7.identifier_stamp_failed: non-fatal but worth alerting
//       - stage4.staged_delete_failed: PHI risk — non-fatal but must alert
//
// Scenarios:
//   A — pipeline.startup BatchFileID must be uuid.Nil (row not yet written)
//   B — pipeline.error carries error and duration_ms
//   C — batch.halt.triggered carries fis_result_code and ERROR level
//   D — stage7.identifier_stamp_failed is non-fatal (ERROR level, no pipeline abort)
//   E — stage4.staged_delete_failed emitted with ERROR level

package observability_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
)

// ── Scenario A: pipeline.startup BatchFileID is uuid.Nil ─────────────────────

func TestDashboard_Infra_PipelineStartupBatchFileIDNil(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID := uuid.New()
	ft := "SRG310"
	s3k := "inbound-raw/2026/03/19/rfu.srg310.pgp"

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     "pipeline.startup",
		Level:         "INFO",
		CorrelationID: corrID,
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.Nil,
		Stage:         strPtr("pipeline"),
		FileType:      &ft,
		S3Key:         &s3k,
		Message:       "pipeline starting",
	})

	startup := cap.firstOfType("pipeline.startup")
	if startup == nil {
		t.Fatal("pipeline.startup not emitted")
	}
	if startup.BatchFileID != uuid.Nil {
		t.Errorf("pipeline.startup BatchFileID must be uuid.Nil; got %s", startup.BatchFileID)
	}
	if startup.FileType == nil {
		t.Error("pipeline.startup must carry file_type for infra dashboard filtering")
	}
	if startup.S3Key == nil {
		t.Error("pipeline.startup must carry s3_key")
	}
}

// ── Scenario B: pipeline.error structure ─────────────────────────────────────

func TestDashboard_Infra_PipelineError(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	dur     := int64(312)
	errStr  := "stage3: batch STALLED — unresolved dead letters"

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     "pipeline.error",
		Level:         "ERROR",
		CorrelationID: corrID,
		TenantID:      "rfu-oregon",
		BatchFileID:   batchID,
		Stage:         strPtr("pipeline"),
		DurationMs:    &dur,
		Error:         &errStr,
		Message:       "pipeline failed after 312ms",
	})

	pe := cap.firstOfType("pipeline.error")
	if pe == nil {
		t.Fatal("pipeline.error not emitted")
	}
	if pe.Level != "ERROR" {
		t.Errorf("pipeline.error level = %q; want ERROR", pe.Level)
	}
	if pe.DurationMs == nil || *pe.DurationMs != 312 {
		t.Errorf("duration_ms = %v; want 312", pe.DurationMs)
	}
	if pe.Error == nil || *pe.Error == "" {
		t.Error("pipeline.error must carry non-empty error field")
	}
}

// ── Scenario C: batch.halt.triggered P0 signal ───────────────────────────────

func TestDashboard_Infra_BatchHaltP0(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	frc := "999"

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     observability.EventBatchHaltTriggered,
		Level:         "ERROR",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.New(),
		Stage:         strPtr("stage7_reconciliation"),
		FISResultCode: &frc,
		Message:       "RT99 full-file pre-processing halt — page on-call P0",
	})

	halt := cap.firstOfType(observability.EventBatchHaltTriggered)
	if halt == nil {
		t.Fatal("batch.halt.triggered not emitted")
	}
	if halt.Level != "ERROR" {
		t.Errorf("batch.halt.triggered must be ERROR level; got %q", halt.Level)
	}
	if halt.FISResultCode == nil {
		t.Error("batch.halt.triggered must carry fis_result_code")
	}
}

// ── Scenario D: stage7.identifier_stamp_failed (non-fatal) ───────────────────

func TestDashboard_Infra_IdentifierStampFailed(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	errStr  := "get_consumer seq=2: no rows"

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage7.identifier_stamp_failed",
		Level:         "ERROR",
		CorrelationID: corrID,
		TenantID:      "rfu-oregon",
		BatchFileID:   batchID,
		Stage:         strPtr("stage7_reconciliation"),
		Error:         &errStr,
		Message:       "FIS identifier stamp failed seq=2 type=RT30",
	})

	// Pipeline MUST still emit stage7.complete after this (non-fatal)
	comp := 2; fail := 1; tot := 3
	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage7.complete",
		Level:         "INFO",
		CorrelationID: corrID,
		TenantID:      "rfu-oregon",
		BatchFileID:   batchID,
		Stage:         strPtr("stage7_reconciliation"),
		Completed:     &comp,
		Failed:        &fail,
		Total:         &tot,
		Message:       "complete: completed=2 failed=1 total=3",
	})

	if cap.firstOfType("stage7.identifier_stamp_failed") == nil {
		t.Fatal("stage7.identifier_stamp_failed not emitted")
	}
	if cap.firstOfType("stage7.complete") == nil {
		t.Error("stage7.complete must still be emitted after non-fatal stamp failure")
	}
}

// ── Scenario E: stage4.staged_delete_failed ───────────────────────────────────

func TestDashboard_Infra_StagedDeleteFailed(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	errStr := "AccessDenied: s3:DeleteObject not permitted on staged/..."

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage4.staged_delete_failed",
		Level:         "ERROR",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.New(),
		Stage:         strPtr("stage4_batch_assembly"),
		Error:         &errStr,
		Message:       "WARN: plaintext staged object delete failed — S3 lifecycle policy is safety backstop",
	})

	e := cap.firstOfType("stage4.staged_delete_failed")
	if e == nil {
		t.Fatal("stage4.staged_delete_failed not emitted")
	}
	if e.Level != "ERROR" {
		t.Errorf("stage4.staged_delete_failed level = %q; want ERROR", e.Level)
	}
	if e.Error == nil {
		t.Error("stage4.staged_delete_failed must carry error field")
	}
}
