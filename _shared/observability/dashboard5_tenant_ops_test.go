package observability_test

// Dashboard 5 — Tenant Operations
//
// Validates that all events required for cross-tenant analysis carry
// tenant_id correctly, and that per-tenant metrics can be distinguished.
//
// Scenarios:
//   A — Two tenants emit identical event types; correlation_ids are distinct
//   B — Per-tenant dead_letter_rate metric carries tenant_id dimension
//   C — Per-tenant subprogram distribution (multiple subprogram_ids)
//   D — Per-tenant benefit period distribution
//   E — file.arrived events carry size_bytes for cross-tenant byte volume
//   F — stage3.complete rt30_count/rt37_count/rt60_count per tenant

package observability_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
)

// ── Scenario A: two tenants, distinct correlation_ids ─────────────────────────

func TestDashboard_TenantOps_MultiTenantIsolation(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	type tenantRun struct {
		tenantID string
		corrID   uuid.UUID
		batchID  uuid.UUID
		staged   int
	}

	runs := []tenantRun{
		{"rfu-oregon", uuid.New(), uuid.New(), 10},
		{"acme-health", uuid.New(), uuid.New(), 7},
		{"bluesky-medicaid", uuid.New(), uuid.New(), 3},
	}

	for _, r := range runs {
		stg := r.staged; dlr := "0.0%"
		rt30 := r.staged; rt37 := 0; rt60 := 0; dups := 0; fail := 0
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType:      "stage3.complete",
			Level:          "INFO",
			CorrelationID:  r.corrID,
			TenantID:       r.tenantID,
			BatchFileID:    r.batchID,
			Stage:          strPtr("stage3_row_processing"),
			Staged:         &stg,
			RT30Count:      &rt30,
			RT37Count:      &rt37,
			RT60Count:      &rt60,
			Duplicates:     &dups,
			Failed:         &fail,
			DeadLetterRate: &dlr,
			Message:        "stage3 complete",
		})
	}

	events := cap.eventsOfType("stage3.complete")
	if len(events) != 3 {
		t.Fatalf("expected 3 stage3.complete events; got %d", len(events))
	}

	// Each event must carry a distinct tenant_id
	tenantsSeen := map[string]bool{}
	for _, e := range events {
		tenantsSeen[e.TenantID] = true
	}
	if len(tenantsSeen) != 3 {
		t.Errorf("expected 3 distinct tenant_ids; got %d", len(tenantsSeen))
	}

	// Each event must carry a distinct correlation_id
	corrsSeen := map[uuid.UUID]bool{}
	for _, e := range events {
		corrsSeen[e.CorrelationID] = true
	}
	if len(corrsSeen) != 3 {
		t.Errorf("expected 3 distinct correlation_ids; got %d", len(corrsSeen))
	}
}

// ── Scenario B: per-tenant dead_letter_rate metric dimension ─────────────────

func TestDashboard_TenantOps_PerTenantDeadLetterMetric(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	rates := map[string]float64{
		"rfu-oregon":       5.0,
		"acme-health":      0.0,
		"bluesky-medicaid": 20.0,
	}

	for tenant, rate := range rates {
		_ = cap.RecordMetric(ctx, observability.MetricDeadLetterRate, rate, map[string]string{
			"tenant_id": tenant,
			"env":       "PRD",
		})
	}

	metrics := cap.metricsNamed(observability.MetricDeadLetterRate)
	if len(metrics) != 3 {
		t.Fatalf("expected 3 dead_letter_rate metrics (one per tenant); got %d", len(metrics))
	}

	for _, m := range metrics {
		if m.Dims["tenant_id"] == "" {
			t.Error("dead_letter_rate metric must carry tenant_id dimension")
		}
		expected := rates[m.Dims["tenant_id"]]
		if m.Value != expected {
			t.Errorf("tenant %s dead_letter_rate = %v; want %v", m.Dims["tenant_id"], m.Value, expected)
		}
	}
}

// ── Scenario C: per-tenant subprogram distribution ───────────────────────────

func TestDashboard_TenantOps_SubprogramDistribution(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()

	// One tenant with multiple subprograms (OTC + FOD)
	subprograms := []struct {
		seq    int
		sub    int64
		bp     string
	}{
		{1, 26071, "2026-06"}, // OTC
		{2, 26072, "2026-06"}, // FOD
		{3, 26071, "2026-06"},
		{4, 26072, "2026-06"},
		{5, 26071, "2026-07"},
	}

	for _, sp := range subprograms {
		s   := sp.seq
		sub := sp.sub
		bp  := sp.bp
		ct  := "ENROLL"
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
	if len(staged) != 5 {
		t.Fatalf("expected 5 row.staged events; got %d", len(staged))
	}

	subCount := map[int64]int{}
	for _, e := range staged {
		if e.SubprogramID != nil {
			subCount[*e.SubprogramID]++
		}
	}
	if subCount[26071] != 3 {
		t.Errorf("subprogram 26071 count = %d; want 3", subCount[26071])
	}
	if subCount[26072] != 2 {
		t.Errorf("subprogram 26072 count = %d; want 2", subCount[26072])
	}
}

// ── Scenario D: benefit period distribution ───────────────────────────────────

func TestDashboard_TenantOps_BenefitPeriodDistribution(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()
	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()

	periodRows := []struct{ seq int; bp string }{
		{1, "2026-05"}, {2, "2026-05"},
		{3, "2026-06"}, {4, "2026-06"}, {5, "2026-06"},
		{6, "2026-07"},
	}

	for _, pr := range periodRows {
		s  := pr.seq
		bp := pr.bp
		ct := "ENROLL"
		sub := int64(26071)
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType: "row.staged", Level: "INFO",
			CorrelationID: corrID, TenantID: "rfu-oregon", BatchFileID: batchID,
			RowSequenceNumber: &s, DomainCommandID: &cmdID,
			CommandType: &ct, BenefitPeriod: &bp, SubprogramID: &sub,
			Message: "row staged",
		})
	}

	periodCounts := map[string]int{}
	for _, e := range cap.eventsOfType("row.staged") {
		if e.BenefitPeriod != nil {
			periodCounts[*e.BenefitPeriod]++
		}
	}
	if periodCounts["2026-06"] != 3 {
		t.Errorf("2026-06 count = %d; want 3", periodCounts["2026-06"])
	}
	if periodCounts["2026-05"] != 2 {
		t.Errorf("2026-05 count = %d; want 2", periodCounts["2026-05"])
	}
}

// ── Scenario E: file.arrived size_bytes for byte volume dashboard ─────────────

func TestDashboard_TenantOps_FileArrivedSizeBytes(t *testing.T) {
	cap := &capturingAdapter{}
	ctx := context.Background()

	files := []struct {
		tenant string
		size   int64
		s3key  string
	}{
		{"rfu-oregon",  4821,  "inbound-raw/2026/03/19/rfu_srg310_20260319.srg310.pgp"},
		{"acme-health", 12048, "inbound-raw/2026/03/19/acme_srg310_20260319.srg310.pgp"},
	}

	for _, f := range files {
		sz  := f.size
		sha := "a3f9b2c1"
		key := f.s3key
		_ = cap.LogEvent(ctx, &ports.LogEvent{
			EventType:     "file.arrived",
			Level:         "INFO",
			CorrelationID: uuid.New(),
			TenantID:      f.tenant,
			BatchFileID:   uuid.New(),
			Stage:         strPtr("stage1_file_arrival"),
			S3Key:         &key,
			SizeBytes:     &sz,
			SHA256:        &sha,
			Message:       "file arrived",
		})
	}

	arrivals := cap.eventsOfType("file.arrived")
	if len(arrivals) != 2 {
		t.Fatalf("expected 2 file.arrived events; got %d", len(arrivals))
	}
	for _, e := range arrivals {
		if e.SizeBytes == nil || *e.SizeBytes <= 0 {
			t.Errorf("file.arrived for tenant %s must carry size_bytes > 0", e.TenantID)
		}
		if e.SHA256 == nil || *e.SHA256 == "" {
			t.Errorf("file.arrived for tenant %s must carry sha256", e.TenantID)
		}
		if e.S3Key == nil {
			t.Errorf("file.arrived for tenant %s must carry s3_key", e.TenantID)
		}
	}
}
