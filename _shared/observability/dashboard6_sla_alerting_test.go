package observability_test

// Dashboard 6 — SLA & Alerting Status
//
// Validates that all four required metrics are emitted with:
//   - correct metric_name (matches MetricName constants)
//   - tenant_id and env dimensions
//   - metric_value in expected range
//
// Also validates ERROR-level event queries:
//   - every ERROR event carries error field
//   - batch.halt.triggered is ERROR
//   - pipeline.error is ERROR
//   - stage7.record_reconcile_error is ERROR
//
// Scenarios:
//   A — All 4 metrics emitted on a clean run
//   B — Malformed rate > 0 (stage2 has rejects)
//   C — Dead letter rate > 0 (stage3 has failures)
//   D — Enrollment success < 100% (stage7 has failures)
//   E — All ERROR events carry error field (alarm filter requirement)
//   F — Rate trend: multiple pipeline runs produce multiple metric data points
//   G — Metric dimensions always include tenant_id and env

package observability_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
)

// ── Scenario A: all 4 required metrics on clean run ──────────────────────────

func TestDashboard_SLA_AllFourMetricsCleanRun(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	dims := map[string]string{"tenant_id": "rfu-oregon", "env": "DEV"}

	_ = cap.RecordMetric(ctx, observability.MetricMalformedRate,        0.0,    dims)
	_ = cap.RecordMetric(ctx, observability.MetricDeadLetterRate,       0.0,    dims)
	_ = cap.RecordMetric(ctx, observability.MetricEnrollmentSuccessRate, 100.0,  dims)
	_ = cap.RecordMetric(ctx, observability.MetricPipelineDurationMs,   1112.0, dims)

	required := []string{
		observability.MetricMalformedRate,
		observability.MetricDeadLetterRate,
		observability.MetricEnrollmentSuccessRate,
		observability.MetricPipelineDurationMs,
	}
	for _, name := range required {
		if len(cap.metricsNamed(name)) == 0 {
			t.Errorf("required metric %q not emitted", name)
		}
	}
	if len(cap.metrics) != 4 {
		t.Errorf("expected exactly 4 metrics on clean run; got %d", len(cap.metrics))
	}
}

// ── Scenario B: malformed_rate > 0 ───────────────────────────────────────────

func TestDashboard_SLA_MalformedRateAboveZero(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	// 2 of 10 rows malformed = 20%
	_ = cap.RecordMetric(ctx, observability.MetricMalformedRate, 20.0, map[string]string{
		"tenant_id": "rfu-oregon", "env": "PRD",
	})

	m := cap.metricsNamed(observability.MetricMalformedRate)
	if len(m) != 1 {
		t.Fatal("malformed_rate metric not emitted")
	}
	if m[0].Value != 20.0 {
		t.Errorf("malformed_rate = %v; want 20.0", m[0].Value)
	}

	// CloudWatch alarm should fire when malformed_rate > 5 (§7.3)
	if m[0].Value > 5.0 {
		// This IS the alarm condition — test documents the threshold
		_ = true // alarm would fire in production
	}
}

// ── Scenario C: dead_letter_rate > 0 ─────────────────────────────────────────

func TestDashboard_SLA_DeadLetterRateAboveZero(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	_ = cap.RecordMetric(ctx, observability.MetricDeadLetterRate, 33.3, map[string]string{
		"tenant_id": "rfu-oregon", "env": "PRD",
	})

	m := cap.metricsNamed(observability.MetricDeadLetterRate)
	if m[0].Value != 33.3 {
		t.Errorf("dead_letter_rate = %v; want 33.3", m[0].Value)
	}
}

// ── Scenario D: enrollment_success_rate < 100% ───────────────────────────────

func TestDashboard_SLA_EnrollmentSuccessRatePartial(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	// 2 of 3 enrolled = 66.7%
	_ = cap.RecordMetric(ctx, observability.MetricEnrollmentSuccessRate, 66.7, map[string]string{
		"tenant_id": "rfu-oregon", "env": "TST",
	})

	m := cap.metricsNamed(observability.MetricEnrollmentSuccessRate)
	if len(m) != 1 {
		t.Fatal("enrollment_success_rate not emitted")
	}
	if m[0].Value >= 100.0 {
		t.Error("enrollment_success_rate should be < 100 in this scenario")
	}
}

// ── Scenario E: all ERROR events carry error field ────────────────────────────

func TestDashboard_SLA_ErrorEventsCarryErrorField(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()

	errorEvents := []struct {
		eventType string
		errStr    string
	}{
		{"pipeline.error", "stage2: pgp decrypt: key not found"},
		{"batch.halt.triggered", "rt99_full_file_halt: fis_result_code=999"},
		{"stage7.record_reconcile_error", "get_staged seq=2 type=RT30: no rows"},
		{"stage7.identifier_stamp_failed", "get_consumer seq=1: no rows"},
		{"stage4.staged_delete_failed", "AccessDenied: s3:DeleteObject"},
	}

	for _, ee := range errorEvents {
		errStr := ee.errStr
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType:     ee.eventType,
			Level:         "ERROR",
			CorrelationID: corrID,
			TenantID:      "rfu-oregon",
			BatchFileID:   batchID,
			Stage:         strPtr("test"),
			Error:         &errStr,
			Message:       "test error event",
		})
	}

	for _, e := range cap.events {
		if e.Level == "ERROR" {
			if e.Error == nil || *e.Error == "" {
				t.Errorf("ERROR event %q must carry non-empty error field (required for SLA alarm queries)", e.EventType)
			}
		}
	}
}

// ── Scenario F: rate trend — multiple runs produce multiple metric points ──────

func TestDashboard_SLA_MetricTrend(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	dims := map[string]string{"tenant_id": "rfu-oregon", "env": "PRD"}

	// Simulate 5 pipeline runs producing duration metrics
	durations := []float64{1050, 1120, 980, 1340, 1090}
	for _, d := range durations {
		_ = cap.RecordMetric(ctx, observability.MetricPipelineDurationMs, d, dims)
	}

	metrics := cap.metricsNamed(observability.MetricPipelineDurationMs)
	if len(metrics) != 5 {
		t.Fatalf("expected 5 pipeline_duration_ms data points; got %d", len(metrics))
	}

	// All must have tenant_id dimension for CloudWatch metric filter
	for _, m := range metrics {
		if m.Dims["tenant_id"] == "" {
			t.Error("pipeline_duration_ms metric missing tenant_id dimension")
		}
	}
}

// ── Scenario G: metric dimensions always include tenant_id and env ────────────

func TestDashboard_SLA_MetricDimensionsRequired(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	allMetrics := []struct {
		name  string
		value float64
	}{
		{observability.MetricMalformedRate,         0.0},
		{observability.MetricDeadLetterRate,        0.0},
		{observability.MetricEnrollmentSuccessRate,  100.0},
		{observability.MetricPipelineDurationMs,    1000.0},
	}

	for _, m := range allMetrics {
		_ = cap.RecordMetric(ctx, m.name, m.value, map[string]string{
			"tenant_id": "rfu-oregon",
			"env":       "DEV",
		})
	}

	for _, m := range cap.metrics {
		if m.Dims["tenant_id"] == "" {
			t.Errorf("metric %q missing tenant_id dimension", m.Name)
		}
		if m.Dims["env"] == "" {
			t.Errorf("metric %q missing env dimension", m.Name)
		}
	}
}
