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
//   2. For each row (or the specific row if --row-seq is set):
//      a. Invokes ingest-task subprocess with --replay --correlation-id --row-seq
//      b. Sets replayed_at timestamp on the dead_letter_store row
//   3. Prints a summary of replayed / skipped / failed rows
//
// Replay is safe to invoke multiple times (idempotency guarantee — §6.5.4).
// The three-layer idempotency check prevents duplicate FIS submissions:
//   Layer 1: file-level SHA-256 on batch_files
//   Layer 2: record-level domain_commands composite key
//   Layer 3: FIS-level clientReferenceNumber
//
// Do NOT replay records where failure_reason indicates a code defect — wait
// for a hotfix to be deployed first (§6.5.3 triage guidance).
// See _docs/runbooks/dead_letter_triage.md for the full triage procedure.
package replay

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
)

// Replayer orchestrates dead letter replay for a given correlation ID.
type Replayer struct {
	DeadLetters     ports.DeadLetterRepository
	Obs             ports.IObservabilityPort
	// IngestTaskPath is the path to the ingest-task binary.
	// Defaults to "ingest-task" (expects it on PATH).
	IngestTaskPath  string
	// ExtraArgs are additional flags passed to ingest-task on every replay invocation.
	// Callers should set at minimum: --db-host, --db-name, --db-user, --db-password,
	// --env, --region (matching the environment of the dead-lettered record).
	ExtraArgs       []string
}

// ReplayResult summarises the outcome of a Replay call.
type ReplayResult struct {
	Total    int
	Replayed int
	Skipped  int // already replayed (replayed_at is non-nil)
	Failed   int
	Errors   []string
}

// Replay re-processes unresolved dead letters for the given correlation ID.
// If rowSeq > 0, only that specific row sequence is replayed.
// Pass dryRun=true to preview which records would be replayed without executing.
func (r *Replayer) Replay(ctx context.Context, correlationID string, rowSeq int, dryRun bool) (*ReplayResult, error) {
	corrUUID, err := uuid.Parse(correlationID)
	if err != nil {
		return nil, fmt.Errorf("replay: invalid correlation_id %q: %w", correlationID, err)
	}

	// List all unresolved dead letter rows for this correlation ID
	entries, err := r.DeadLetters.ListUnresolved(ctx, corrUUID)
	if err != nil {
		return nil, fmt.Errorf("replay: ListUnresolved: %w", err)
	}

	result := &ReplayResult{Total: len(entries)}

	if len(entries) == 0 {
		log.Printf("replay: no unresolved dead letters for correlation_id=%s", correlationID)
		return result, nil
	}

	for _, entry := range entries {
		// Filter to specific row sequence if requested
		if rowSeq > 0 && (entry.RowSequenceNumber == nil || *entry.RowSequenceNumber != rowSeq) {
			continue
		}

		// Skip already-replayed rows (safety belt — ListUnresolved filters resolved_at IS NULL
		// but replayed_at may be set on an in-flight replay)
		if entry.ReplayedAt != nil {
			log.Printf("replay: skip seq=%v already replayed at %s",
				entry.RowSequenceNumber, entry.ReplayedAt.Format(time.RFC3339))
			result.Skipped++
			continue
		}

		seqStr := "all"
		if entry.RowSequenceNumber != nil {
			seqStr = fmt.Sprintf("%d", *entry.RowSequenceNumber)
		}

		if dryRun {
			log.Printf("replay [DRY RUN]: would replay correlation_id=%s seq=%s failure_stage=%s reason=%s",
				correlationID, seqStr, entry.FailureStage, entry.FailureReason)
			result.Replayed++
			continue
		}

		// Build ingest-task invocation args
		args := []string{
			"--correlation-id", correlationID,
			"--replay",
		}
		if entry.RowSequenceNumber != nil {
			args = append(args, "--replay-seq", fmt.Sprintf("%d", *entry.RowSequenceNumber))
		}
		args = append(args, r.ExtraArgs...)

		binaryPath := r.IngestTaskPath
		if binaryPath == "" {
			binaryPath = "ingest-task"
		}

		log.Printf("replay: invoking %s --correlation-id=%s --replay --replay-seq=%s",
			binaryPath, correlationID, seqStr)

		cmd := exec.CommandContext(ctx, binaryPath, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			errMsg := fmt.Sprintf("seq=%s: ingest-task failed: %v\noutput: %s", seqStr, err, string(out))
			log.Printf("replay: ERROR %s", errMsg)
			result.Failed++
			result.Errors = append(result.Errors, errMsg)
			continue
		}

		// Mark the dead letter row as replayed
		if err := r.DeadLetters.MarkReplayed(ctx, entry.ID, time.Now().UTC()); err != nil {
			// Non-fatal — the replay succeeded but the timestamp update failed.
			// Log it; ops can set replayed_at manually if needed.
			errMsg := fmt.Sprintf("seq=%s: MarkReplayed failed (replay succeeded): %v", seqStr, err)
			log.Printf("replay: WARN %s", errMsg)
			result.Errors = append(result.Errors, errMsg)
		}

		log.Printf("replay: success seq=%s correlation_id=%s", seqStr, correlationID)
		result.Replayed++
	}

	return result, nil
}
