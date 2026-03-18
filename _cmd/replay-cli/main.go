// Command replay-cli re-processes dead-lettered records.
//
// Must exist before UAT begins (Open Item #24, target May 20, 2026).
// On-call engineers invoke this manually — no AWS Console access required.
//
// Usage:
//   replay-cli --correlation-id <uuid>
//   replay-cli --correlation-id <uuid> --row-seq <n>
//   replay-cli --correlation-id <uuid> --dry-run
//
// Replay is safe to invoke multiple times:
//   Layer 1: file-level SHA-256 on batch_files
//   Layer 2: record-level domain_commands composite key
//   Layer 3: FIS-level clientReferenceNumber
//
// Do NOT replay records with failure_reason indicating a code defect.
// Wait for a hotfix to be deployed first (§6.5.3 triage guidance).
// See docs/runbooks/dead_letter_triage.md for the full triage procedure.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	correlationID := flag.String("correlation-id", "", "correlation UUID of the batch file")
	rowSeq        := flag.Int("row-seq", 0, "specific row sequence number (0 = replay all unresolved)")
	dryRun        := flag.Bool("dry-run", false, "preview without executing")
	flag.Parse()

	if *correlationID == "" {
		fmt.Fprintln(os.Stderr, "error: --correlation-id is required")
		flag.Usage()
		os.Exit(1)
	}

	log.Printf("replay-cli: correlation_id=%s row_seq=%d dry_run=%v",
		*correlationID, *rowSeq, *dryRun)

	// TODO: connect to Aurora via Secrets Manager credentials
	// TODO: list unresolved dead_letter_store rows for correlation_id
	// TODO: for each: invoke ingest-task with --replay flag
	// TODO: set replayed_at on dead_letter_store row
}
