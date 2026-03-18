// Stage 7 — Reconciliation (§5.1, §6.5):
//   Ingest FIS return file → match results to staged batch records →
//   update status → write fact_reconciliation → deliver recon report to MCO.
//
// RT99 full-file pre-processing halt detection (§6.5.1):
//   If the return file contains EXACTLY ONE record and it is RT99:
//   → the entire file was rejected by FIS before any member was processed
//   → ALL members must be dead-lettered (failure_stage = "reconciliation")
//   → batch_files.status → HALTED
//   → emit "batch.halt.triggered" log event
//   → page on-call immediately (CloudWatch alarm → Datadog P0)
//   → reconciliation report to MCO must clearly identify this as full-file rejection
//
//   This is DISTINCT from individual record RT99 failures (FIS processes all other
//   records and returns RT99 only for the failed record). Read §6.5.1 carefully.
//
// Log File Indicator = 0 requirement: Stage 7 requires a return file entry for
// EVERY submitted record in order to:
//   - Confirm successful enrollment
//   - Capture FIS-assigned card IDs (cards.fis_card_id)
//   - Capture FIS-assigned purse numbers (purses.fis_purse_number)
//   - Produce an accurate reconciliation report to the health plan client
//   Setting indicator to 1 (errors only) would make all of the above impossible.
//   (§6.6.3, Open Item #30)
//
// MartWriter writes:
//   - WriteReconciliationFact per matched record → fact_reconciliation
//   - fact_reconciliation is the primary source for the Tableau reconciliation
//     pass-rate dashboard (Kendra Williams MVP requirement, §4.3.10)
//
// MCO reconciliation report (§6.1):
//   - Total records submitted, completed, failed, rejected_malformed
//   - Per failed/rejected record: Client_Member_ID, command type, FIS result code,
//     rejection reason in human-readable form
//   - Completed records include FIS-assigned cardId or purseNumber where applicable
//   - Delivered to MCO outbound Secure File Transfer directory
//
// Status transition: TRANSFERRED → COMPLETE (or HALTED on RT99 full-file halt)
package pipeline

import "context"

// ReconciliationStage implements Stage 7 of the ingest-task pipeline.
type ReconciliationStage struct {
	BatchFiles  ports.BatchFileRepository
	DeadLetters ports.DeadLetterRepository
	Audit       ports.AuditLogWriter
	Mart        ports.MartWriter
	Obs         ports.IObservabilityPort
}

// Run ingests the FIS return file and reconciles each result.
func (s *ReconciliationStage) Run(ctx context.Context, batchFileID string) error {
	// TODO: parse return file
	// TODO: detect RT99 full-file halt (IsRT99Halt check from fis_adapter)
	// TODO: match each result record to staged batch_records row
	// TODO: update batch_records status STAGED → COMPLETED|FAILED
	// TODO: write fact_reconciliation via MartWriter
	// TODO: generate reconciliation report
	// TODO: deliver report to MCO outbound Secure File Transfer
	// TODO: transition batch_files TRANSFERRED → COMPLETE
	return nil
}
