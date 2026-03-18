// Package fbo_reconciliation implements the daily FBO purse balance integrity check.
//
// Regulatory basis: 12 CFR Part 330, FDIC GC Opinion No. 8, MVB program agreement.
// One Fintech is the authoritative sub-ledger — purses.available_balance_cents is
// the per-member balance of record (§I.2).
//
// The reconciliation compares:
//   sum(purses.available_balance_cents WHERE status IN ('ACTIVE', 'PENDING'))
//   against the MVB FBO account closing balance for the business day.
//
// Outstanding domain_commands with status Pending are included as provisional
// liabilities (in-flight batch files submitted to FIS but not yet return-file confirmed).
//
// Result is recorded as a compliance event in audit_log.
// Discrepancy threshold and MVB notification SLA: Open Item #44 (Kendra Williams).
package fbo_reconciliation

import (
	"context"
	"fmt"
	"time"

	"github.com/walker-morse/batch/_shared/ports"
)

// ReconciliationResult is the outcome of one daily FBO balance check.
type ReconciliationResult struct {
	Date                   time.Time
	PlatformLiabilityCents int64 // sum of active/pending purse balances
	PendingCommandsCents   int64 // provisional liabilities from in-flight batches
	TotalLiabilityCents    int64 // PlatformLiability + PendingCommands
	FBOBalanceCents        int64 // MVB FBO account closing balance
	DiscrepancyCents       int64 // TotalLiability - FBOBalance
	IsMatch                bool  // true when |discrepancy| <= threshold
}

// FBOLiabilityReader is the narrow query interface required by the Reconciler.
// Implemented by aurora.DomainStateRepo. Defined here to keep fbo_reconciliation
// free of aurora import dependencies.
type FBOLiabilityReader interface {
	SumPlatformLiabilityCents(ctx context.Context) (int64, error)
	SumPendingCommandsCents(ctx context.Context) (int64, error)
}

// Reconciler performs the daily FBO balance integrity check.
type Reconciler struct {
	DB    FBOLiabilityReader
	Audit ports.AuditLogWriter
	Obs   ports.IObservabilityPort
	// DiscrepancyThresholdCents is the maximum tolerated delta before IsMatch=false.
	// Open Item #44 (Kendra Williams) will define the production value.
	// Defaults to 0 (exact match required) until threshold is confirmed.
	DiscrepancyThresholdCents int64
}

// Reconcile computes platform liability and compares to the MVB FBO closing balance.
// Writes a compliance event to audit_log regardless of outcome (§4.2.7).
// Callers must escalate (page ops, notify MVB) if !result.IsMatch.
// The discrepancy escalation SLA is defined in Open Item #44.
func (r *Reconciler) Reconcile(ctx context.Context, date time.Time, fboBalanceCents int64) (*ReconciliationResult, error) {
	platformCents, err := r.DB.SumPlatformLiabilityCents(ctx)
	if err != nil {
		return nil, fmt.Errorf("fbo_reconciliation: sum platform liability: %w", err)
	}

	pendingCents, err := r.DB.SumPendingCommandsCents(ctx)
	if err != nil {
		return nil, fmt.Errorf("fbo_reconciliation: sum pending commands: %w", err)
	}

	totalLiability := platformCents + pendingCents
	discrepancy := totalLiability - fboBalanceCents

	threshold := r.DiscrepancyThresholdCents
	isMatch := discrepancy >= -threshold && discrepancy <= threshold

	result := &ReconciliationResult{
		Date:                   date.UTC().Truncate(24 * time.Hour),
		PlatformLiabilityCents: platformCents,
		PendingCommandsCents:   pendingCents,
		TotalLiabilityCents:    totalLiability,
		FBOBalanceCents:        fboBalanceCents,
		DiscrepancyCents:       discrepancy,
		IsMatch:                isMatch,
	}

	newState := fmt.Sprintf(
		`{"date":%q,"platform_cents":%d,"pending_cents":%d,"total_cents":%d,"fbo_cents":%d,"discrepancy_cents":%d,"is_match":%v}`,
		result.Date.Format("2006-01-02"),
		platformCents, pendingCents, totalLiability, fboBalanceCents, discrepancy, isMatch,
	)

	auditEntry := &ports.AuditEntry{
		TenantID:   "system",
		EntityType: "fbo_reconciliation",
		EntityID:   date.Format("2006-01-02"),
		NewState:   newState,
		ChangedBy:  "ingest-task",
		Notes:      strPtr("daily FBO balance integrity check (Addendum I)"),
	}
	if !isMatch {
		msg := fmt.Sprintf("DISCREPANCY: delta=%d cents — escalate per Open Item #44", discrepancy)
		auditEntry.Notes = strPtr(msg)
	}

	if err := r.Audit.Write(ctx, auditEntry); err != nil {
		// Audit failure is non-fatal for the reconciliation result itself,
		// but must be surfaced — compliance trail must not silently drop.
		return result, fmt.Errorf("fbo_reconciliation: audit write failed (COMPLIANCE ALERT): %w", err)
	}

	return result, nil
}

func strPtr(s string) *string { return &s }
