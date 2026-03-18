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
	"github.com/walker-morse/batch/_shared/ports"
	"context"
	"time"
)

// ReconciliationResult is the outcome of one daily FBO balance check.
type ReconciliationResult struct {
	Date                  time.Time
	PlatformLiabilityCents int64 // sum of active purse balances
	FBOBalanceCents       int64 // MVB FBO account closing balance
	DiscrepancyCents      int64 // PlatformLiability - FBOBalance
	IsMatch               bool  // within tolerance threshold
	PendingCommandsCents  int64 // provisional liabilities from in-flight batches
}

// Reconciler performs the daily FBO balance integrity check.
type Reconciler struct {
	Audit ports.AuditLogWriter
	Obs   ports.IObservabilityPort
}

// Reconcile computes platform liability and compares to FBO balance.
// Writes result as a compliance event to audit_log regardless of outcome.
// Callers must escalate if !result.IsMatch and discrepancy exceeds configured threshold.
func (r *Reconciler) Reconcile(ctx context.Context, fboBalanceCents int64) (*ReconciliationResult, error) {
	// TODO: sum purses.available_balance_cents for all active/pending purses
	// TODO: sum pending domain_commands as provisional liabilities
	// TODO: compare to fboBalanceCents
	// TODO: write compliance event to audit_log
	// TODO: if discrepancy > threshold: escalate (Open Item #44 defines threshold)
	return nil, nil
}
