// Package replay implements the dead letter replay CLI tool.
//
// This tool MUST exist before UAT begins (Open Item #24, target May 20, 2026).
// On-call engineers invoke it manually to replay dead-lettered records without
// requiring direct AWS Console access.
//
// Usage:
//   replay-cli --correlation-id <uuid> [--row-seq <n>] [--dry-run]
//
// The tool:
//   1. Reads unresolved dead_letter_store rows for the given correlation_id
//   2. For each row: re-invokes the ingest-task with --replay flag
//   3. Sets replayed_at timestamp on the dead_letter_store row
//   4. On successful replay: ingest-task sets resolved_at automatically
//
// Replay is safe to invoke multiple times (idempotency guarantee — §6.5.4).
// The three-layer idempotency check prevents duplicate FIS submissions.
//
// Do NOT replay records where failure_reason indicates a code defect — wait
// for a hotfix to be deployed first (§6.5.3 triage guidance).
package replay

import (
	"context"
	"github.com/walker-morse/batch/_shared/ports"
)

// Replayer orchestrates dead letter replay for a given correlation ID.
type Replayer struct {
	DeadLetters ports.DeadLetterRepository
	Obs         ports.IObservabilityPort
}

// Replay re-processes all unresolved dead letters for the given correlation ID.
// Pass dryRun=true to preview which records would be replayed without executing.
func (r *Replayer) Replay(ctx context.Context, correlationID string, dryRun bool) error {
	// TODO: list unresolved rows
	// TODO: for each: invoke ingest-task with --replay flag
	// TODO: set replayed_at on the dead_letter_store row
	return nil
}
