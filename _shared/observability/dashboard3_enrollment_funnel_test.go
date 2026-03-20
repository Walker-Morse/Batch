package observability_test

// Dashboard 3 — Member Enrollment Funnel
//
// Validates the per-row event chain:
//   row.staged → member.enrolled (clean path)
//   row.staged → dead.letter.written (failure path)
//   row.duplicate (idempotency skip)
//
// Also validates:
//   - benefit_period is ISO YYYY-MM on every per-row event
//   - subprogram_id is present on row.staged
//   - domain_command_id links row.staged to member.enrolled
//   - fis_result_code "000" on member.enrolled
//   - enrollment_success_rate metric emitted after stage7


import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
)

// ── Scenario A: row.staged fields ────────────────────────────────────────────

func TestDashboard_EnrollmentFunnel_RowStagedFields(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()

	seqs := []int{1, 2, 3}
	for _, seq := range seqs {
		s   := seq
		sub := int64(26071)
		ct  := "ENROLL"
		bp  := "2026-06"
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType:         "row.staged",
			Level:             "INFO",
			CorrelationID:     corrID,
			TenantID:          "rfu-oregon",
			BatchFileID:       batchID,
			Stage:             strPtr("stage3_row_processing"),
			RowSequenceNumber: &s,
			DomainCommandID:   &cmdID,
			CommandType:       &ct,
			BenefitPeriod:     &bp,
			SubprogramID:      &sub,
			Message:           "row staged",
		})
	}

	staged := cap.eventsOfType("row.staged")
	if len(staged) != 3 {
		t.Fatalf("expected 3 row.staged events; got %d", len(staged))
	}

	for _, e := range staged {
		if e.RowSequenceNumber == nil {
			t.Error("row.staged must carry row_sequence_number")
		}
		if e.DomainCommandID == nil || *e.DomainCommandID == (uuid.UUID{}) {
			t.Error("row.staged must carry non-nil domain_command_id")
		}
		if e.CommandType == nil || *e.CommandType != "ENROLL" {
			t.Errorf("row.staged command_type = %v; want ENROLL", e.CommandType)
		}
		if e.BenefitPeriod == nil || *e.BenefitPeriod != "2026-06" {
			t.Errorf("row.staged benefit_period = %v; want 2026-06", e.BenefitPeriod)
		}
		if e.SubprogramID == nil || *e.SubprogramID != 26071 {
			t.Errorf("row.staged subprogram_id = %v; want 26071", e.SubprogramID)
		}
	}
}

// ── Scenario B: member.enrolled fields ───────────────────────────────────────

func TestDashboard_EnrollmentFunnel_MemberEnrolledFields(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()

	for i := 1; i <= 3; i++ {
		s   := i
		bp  := "2026-06"
		frc := "000"
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType:         "member.enrolled",
			Level:             "INFO",
			CorrelationID:     corrID,
			TenantID:          "rfu-oregon",
			BatchFileID:       batchID,
			Stage:             strPtr("stage7_reconciliation"),
			RowSequenceNumber: &s,
			DomainCommandID:   &cmdID,
			BenefitPeriod:     &bp,
			FISResultCode:     &frc,
			Message:           "member enrolled",
		})
	}

	enrolled := cap.eventsOfType("member.enrolled")
	if len(enrolled) != 3 {
		t.Fatalf("expected 3 member.enrolled events; got %d", len(enrolled))
	}

	for _, e := range enrolled {
		if e.DomainCommandID == nil {
			t.Error("member.enrolled must carry domain_command_id")
		}
		if e.BenefitPeriod == nil || *e.BenefitPeriod != "2026-06" {
			t.Errorf("member.enrolled benefit_period = %v; want 2026-06", e.BenefitPeriod)
		}
		if e.FISResultCode == nil || *e.FISResultCode != "000" {
			t.Errorf("member.enrolled fis_result_code = %v; want 000", e.FISResultCode)
		}
	}
}

// ── Scenario C: row.duplicate ─────────────────────────────────────────────────

func TestDashboard_EnrollmentFunnel_RowDuplicate(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	seq     := 2
	ct      := "ENROLL"
	bp      := "2026-06"

	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType:         "row.duplicate",
		Level:             "WARN",
		CorrelationID:     corrID,
		TenantID:          "rfu-oregon",
		BatchFileID:       batchID,
		Stage:             strPtr("stage3_row_processing"),
		RowSequenceNumber: &seq,
		CommandType:       &ct,
		BenefitPeriod:     &bp,
		Message:           "duplicate skipped: seq=2",
	})

	dups := cap.eventsOfType("row.duplicate")
	if len(dups) != 1 {
		t.Fatalf("expected 1 row.duplicate event; got %d", len(dups))
	}
	if dups[0].Level != "WARN" {
		t.Errorf("row.duplicate level = %q; want WARN", dups[0].Level)
	}
	if dups[0].RowSequenceNumber == nil || *dups[0].RowSequenceNumber != 2 {
		t.Errorf("row.duplicate row_sequence_number = %v; want 2", dups[0].RowSequenceNumber)
	}
}

// ── Scenario D: funnel counts (staged → enrolled gap) ─────────────────────────

func TestDashboard_EnrollmentFunnel_FunnelCounts(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()

	// 5 rows: 3 staged, 1 duplicate, 1 dead-lettered
	stagedSeqs  := []int{1, 2, 3}
	enrolledSeqs := []int{1, 2} // seq 3 failed in stage 7

	for _, s := range stagedSeqs {
		seq := s; ct := "ENROLL"; bp := "2026-06"; sub := int64(26071)
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType: "row.staged", Level: "INFO",
			CorrelationID: corrID, TenantID: "rfu-oregon", BatchFileID: batchID,
			RowSequenceNumber: &seq, DomainCommandID: &cmdID,
			CommandType: &ct, BenefitPeriod: &bp, SubprogramID: &sub,
			Message: "row staged",
		})
	}

	dupSeq := 4; ct := "ENROLL"; bp := "2026-06"
	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType: "row.duplicate", Level: "WARN",
		CorrelationID: corrID, TenantID: "rfu-oregon", BatchFileID: batchID,
		RowSequenceNumber: &dupSeq, CommandType: &ct, BenefitPeriod: &bp,
		Message: "duplicate skipped",
	})

	dlSeq := 5; fc := "DATA_GAP"; er := "program_lookup_failed(subprogram=00000)"
	_ = cap.LogEvent(ctx, &ports.LogEvent{
		EventType: observability.EventDeadLetterWritten, Level: "WARN",
		CorrelationID: corrID, TenantID: "rfu-oregon", BatchFileID: batchID,
		RowSequenceNumber: &dlSeq, FailureCategory: &fc, Error: &er,
		Message: "dead_letter: seq=5",
	})

	for _, s := range enrolledSeqs {
		seq := s; frc := "000"
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType: "member.enrolled", Level: "INFO",
			CorrelationID: corrID, TenantID: "rfu-oregon", BatchFileID: batchID,
			RowSequenceNumber: &seq, DomainCommandID: &cmdID, FISResultCode: &frc,
			Message: "member enrolled",
		})
	}

	if len(cap.eventsOfType("row.staged")) != 3 {
		t.Errorf("row.staged count = %d; want 3", len(cap.eventsOfType("row.staged")))
	}
	if len(cap.eventsOfType("row.duplicate")) != 1 {
		t.Errorf("row.duplicate count = %d; want 1", len(cap.eventsOfType("row.duplicate")))
	}
	if len(cap.eventsOfType("member.enrolled")) != 2 {
		t.Errorf("member.enrolled count = %d; want 2", len(cap.eventsOfType("member.enrolled")))
	}
	// Gap: 3 staged but only 2 enrolled = 1 member not yet enrolled (reconcile error)
	gap := len(cap.eventsOfType("row.staged")) - len(cap.eventsOfType("member.enrolled"))
	if gap != 1 {
		t.Errorf("enrollment gap = %d; want 1", gap)
	}
}

// ── Scenario E: enrollment_success_rate metric ────────────────────────────────

func TestDashboard_EnrollmentFunnel_SuccessRateMetric(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	// 3 completed of 3 total = 100%
	_ = cap.RecordMetric(ctx, observability.MetricEnrollmentSuccessRate, 100.0, map[string]string{
		"tenant_id": "rfu-oregon",
		"env":       "DEV",
	})

	metrics := cap.metricsNamed(observability.MetricEnrollmentSuccessRate)
	if len(metrics) != 1 {
		t.Fatalf("expected 1 enrollment_success_rate metric; got %d", len(metrics))
	}
	if metrics[0].Value != 100.0 {
		t.Errorf("enrollment_success_rate = %v; want 100.0", metrics[0].Value)
	}
}

// ── Scenario F: benefit_period is always ISO YYYY-MM ─────────────────────────

func TestDashboard_EnrollmentFunnel_BenefitPeriodFormat(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()

	periods := []string{"2026-06", "2026-07", "2026-08"}
	for i, bp := range periods {
		s   := i + 1
		period := bp
		ct  := "ENROLL"
		sub := int64(26071)
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType: "row.staged", Level: "INFO",
			CorrelationID: corrID, TenantID: "rfu-oregon", BatchFileID: batchID,
			RowSequenceNumber: &s, DomainCommandID: &cmdID,
			CommandType: &ct, BenefitPeriod: &period, SubprogramID: &sub,
			Message: "row staged",
		})
	}

	for _, e := range cap.eventsOfType("row.staged") {
		if e.BenefitPeriod == nil {
			t.Error("benefit_period must be set on row.staged")
			continue
		}
		// Must match YYYY-MM
		if len(*e.BenefitPeriod) != 7 || (*e.BenefitPeriod)[4] != '-' {
			t.Errorf("benefit_period %q does not match YYYY-MM format", *e.BenefitPeriod)
		}
	}
}
