// Stage 3 — Row Processing (§5.1, ADR-010):
//   Sequential row-by-row domain command execution.
//   Each CSV row is an independent processing unit with its own idempotency key
//   and failure boundary. One row exception does not abort the file.
//
// Processing order per row (MANDATORY — do not reorder):
//   1. Idempotency check: domain_commands composite key
//      (correlation_id, client_member_id, command_type, benefit_period)
//      → if Accepted exists: return Duplicate, skip
//   2. Write batch_records_rt30 / rt37 / rt60 with status STAGED
//   3. Write domain state: consumers, cards, purses
//   4. Inline mart write (MartWriter): dim_member, dim_card, dim_purse, fact_enrollments
//
// On row exception:
//   - Write to dead_letter_store with failure_stage = "row_processing"
//   - Increment failure counter
//   - Continue to next row
//
// On Stage 3 completion:
//   - Compare staged_count to batch_files.record_count
//   - If counts diverge (unresolved dead letters): transition to STALLED
//   - If STALLED: emit "batch.stalled" log event, do NOT proceed to Stage 4
//   - Resolution: replay CLI tool (Open Item #24) or manual poison-message abandonment
//
// Sequential throughput: wall-clock time scales linearly with row count.
// At RFU volumes (≤200K rows), FIS batch processing SLA (~45-60 min/50K records)
// is the dominant constraint — not Stage 3 throughput. Validate in load testing (§5.6).
//
// Evolution path (ADR-010): the Stage 3 loop body is the clean extraction point
// for future parallel execution (SQS + Lambda or Fargate pool). No domain changes
// required — idempotency model and dead letter flow are identical.
//
// Do NOT implement row-level parallelism without first confirming Aurora connection
// pool sizing. RDS Proxy is sized for one persistent connection per concurrent
// Fargate task — not per-row concurrent connections.
//
// Status transition: VALIDATING → PROCESSING → (STALLED | advance to Stage 4)
package pipeline

import "context"

// RowProcessingStage implements Stage 3 of the ingest-task pipeline.
type RowProcessingStage struct {
	DomainCommands ports.DomainCommandRepository
	DeadLetters    ports.DeadLetterRepository
	BatchFiles     ports.BatchFileRepository
	Audit          ports.AuditLogWriter
	Mart           ports.MartWriter
	Obs            ports.IObservabilityPort
}

// Run processes all rows in the validated SRG file sequentially.
func (s *RowProcessingStage) Run(ctx context.Context, batchFileID string) error {
	// TODO: sequential row iteration
	// TODO: idempotency gate (domain_commands) — MUST be first write
	// TODO: batch_records writes
	// TODO: domain state writes (consumers, cards, purses)
	// TODO: MartWriter inline writes
	// TODO: staged_count vs record_count comparison → STALLED if mismatch
	return nil
}
