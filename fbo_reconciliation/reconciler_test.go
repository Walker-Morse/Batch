package fbo_reconciliation

// Tests for the daily FBO balance integrity check (Addendum I, §I.2).
//
// All tests use in-memory mock dependencies — no DB connection required.
// Coverage:
//   - Happy path: platform + pending match FBO → IsMatch=true, audit written
//   - Exact match at zero: no liability, zero FBO balance
//   - Discrepancy detected: delta exceeds threshold → IsMatch=false
//   - Discrepancy within threshold: IsMatch=true
//   - PlatformLiability query failure → error returned
//   - PendingCommands query failure → error returned
//   - Audit write failure → result returned + error (compliance alert)
//   - TotalLiability = PlatformLiability + PendingCommandsCents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/walker-morse/batch/_shared/ports"
)

// ─── mocks ────────────────────────────────────────────────────────────────────

type mockLiabilityReader struct {
	PlatformCents int64
	PendingCents  int64
	PlatformErr   error
	PendingErr    error
}

func (m *mockLiabilityReader) SumPlatformLiabilityCents(_ context.Context) (int64, error) {
	return m.PlatformCents, m.PlatformErr
}
func (m *mockLiabilityReader) SumPendingCommandsCents(_ context.Context) (int64, error) {
	return m.PendingCents, m.PendingErr
}

type mockAuditWriter struct {
	Entries []*ports.AuditEntry
	Err     error
}

func (m *mockAuditWriter) Write(_ context.Context, e *ports.AuditEntry) error {
	m.Entries = append(m.Entries, e)
	return m.Err
}

func newReconciler(db *mockLiabilityReader, audit *mockAuditWriter, threshold int64) *Reconciler {
	return &Reconciler{DB: db, Audit: audit, DiscrepancyThresholdCents: threshold}
}

var testDate = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// ─── tests ────────────────────────────────────────────────────────────────────

// TestReconcile_HappyPath verifies that matching totals produce IsMatch=true
// and a compliance audit entry is written.
func TestReconcile_HappyPath(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 100000, PendingCents: 5000}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 105000)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsMatch {
		t.Errorf("IsMatch = false; want true (no discrepancy)")
	}
	if result.DiscrepancyCents != 0 {
		t.Errorf("DiscrepancyCents = %d; want 0", result.DiscrepancyCents)
	}
	if result.TotalLiabilityCents != 105000 {
		t.Errorf("TotalLiabilityCents = %d; want 105000", result.TotalLiabilityCents)
	}
	if len(audit.Entries) != 1 {
		t.Fatalf("audit entries = %d; want 1", len(audit.Entries))
	}
	if audit.Entries[0].EntityType != "fbo_reconciliation" {
		t.Errorf("EntityType = %q; want fbo_reconciliation", audit.Entries[0].EntityType)
	}
}

// TestReconcile_ZeroLiability verifies the zero-balance base case.
func TestReconcile_ZeroLiability(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 0, PendingCents: 0}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 0)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsMatch {
		t.Errorf("IsMatch = false; want true")
	}
}

// TestReconcile_TotalLiabilityIsSumOfBoth verifies that PendingCommandsCents
// is added to PlatformLiabilityCents before comparison.
func TestReconcile_TotalLiabilityIsSumOfBoth(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 80000, PendingCents: 20000}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 100000)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PlatformLiabilityCents != 80000 {
		t.Errorf("PlatformLiabilityCents = %d; want 80000", result.PlatformLiabilityCents)
	}
	if result.PendingCommandsCents != 20000 {
		t.Errorf("PendingCommandsCents = %d; want 20000", result.PendingCommandsCents)
	}
	if result.TotalLiabilityCents != 100000 {
		t.Errorf("TotalLiabilityCents = %d; want 100000", result.TotalLiabilityCents)
	}
	if !result.IsMatch {
		t.Error("IsMatch = false; want true")
	}
}

// TestReconcile_DiscrepancyDetected verifies that a delta above threshold
// sets IsMatch=false and the audit note contains an escalation message.
func TestReconcile_DiscrepancyDetected(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 100000, PendingCents: 0}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 99000) // $10 short

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsMatch {
		t.Error("IsMatch = true; want false (discrepancy present)")
	}
	if result.DiscrepancyCents != 1000 {
		t.Errorf("DiscrepancyCents = %d; want 1000", result.DiscrepancyCents)
	}
	if len(audit.Entries) != 1 {
		t.Fatalf("audit entries = %d; want 1", len(audit.Entries))
	}
	if !strings.Contains(*audit.Entries[0].Notes, "DISCREPANCY") {
		t.Errorf("audit Notes = %q; want DISCREPANCY escalation message", *audit.Entries[0].Notes)
	}
}

// TestReconcile_DiscrepancyWithinThreshold verifies that a delta within the
// configured threshold still produces IsMatch=true.
func TestReconcile_DiscrepancyWithinThreshold(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 100000, PendingCents: 0}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 500) // threshold: $5.00

	result, err := r.Reconcile(context.Background(), testDate, 99700) // $3 delta

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsMatch {
		t.Errorf("IsMatch = false; want true (delta %d within threshold 500)", result.DiscrepancyCents)
	}
}

// TestReconcile_PlatformQueryFails_ReturnsError verifies that a DB error on the
// platform liability sum propagates as an error (no partial result).
func TestReconcile_PlatformQueryFails_ReturnsError(t *testing.T) {
	db := &mockLiabilityReader{PlatformErr: errors.New("db: timeout")}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 0)

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if result != nil {
		t.Errorf("result = %+v; want nil on error", result)
	}
	if len(audit.Entries) != 0 {
		t.Errorf("audit entries = %d; want 0 (no partial writes)", len(audit.Entries))
	}
}

// TestReconcile_PendingQueryFails_ReturnsError verifies the same for the
// pending commands sum.
func TestReconcile_PendingQueryFails_ReturnsError(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 50000, PendingErr: errors.New("db: timeout")}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 50000)

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if result != nil {
		t.Errorf("result = %+v; want nil on error", result)
	}
}

// TestReconcile_AuditWriteFails_ResultReturnedWithError verifies that an audit
// write failure still returns the reconciliation result (data was computed) but
// also surfaces the error — the compliance trail must not silently drop.
func TestReconcile_AuditWriteFails_ResultReturnedWithError(t *testing.T) {
	db := &mockLiabilityReader{PlatformCents: 100000, PendingCents: 0}
	audit := &mockAuditWriter{Err: errors.New("audit: write failed")}
	r := newReconciler(db, audit, 0)

	result, err := r.Reconcile(context.Background(), testDate, 100000)

	if err == nil {
		t.Fatal("expected error for audit write failure; got nil")
	}
	if result == nil {
		t.Fatal("expected result even on audit failure; got nil")
	}
	if !strings.Contains(err.Error(), "COMPLIANCE ALERT") {
		t.Errorf("error = %q; want COMPLIANCE ALERT in message", err.Error())
	}
}

// TestReconcile_DateTruncatedToMidnight verifies that non-midnight dates are
// normalised to UTC midnight in the result — consistent with FBO statement dates.
func TestReconcile_DateTruncatedToMidnight(t *testing.T) {
	db := &mockLiabilityReader{}
	audit := &mockAuditWriter{}
	r := newReconciler(db, audit, 0)

	nonMidnight := time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC)
	result, err := r.Reconcile(context.Background(), nonMidnight, 0)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Date.Equal(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("Date = %v; want 2026-06-01 00:00:00 UTC", result.Date)
	}
}
