// Stage 2 — Validation (§5.1):
//   PGP decrypt → SRG format validation → malformed rows → dead_letter_store.
//
// A separate and more severe failure path exists at Stage 2: if FIS returns an
// RT99 as the FIRST AND ONLY record in the return file (Stage 7 detection),
// the ENTIRE file was rejected pre-processing. That is distinct from individual
// record failures. See stage7_reconciliation.go for RT99 halt detection.
//
// Malformed rows:
//   - Written to dead_letter_store with failure_stage = "validation"
//   - batch_files.malformed_count incremented
//   - Processing continues for well-formed rows
//
// Duplicate file detection:
//   - SHA-256 hash compared against existing batch_files rows
//   - Duplicate triggers immediate halt — no domain state touched
//
// Status transition: RECEIVED → VALIDATING
//
// ASCII transliteration for non-ASCII demographic fields (e.g. Spanish-language
// names) must be confirmed before Stage 3 implementation (§2 Assumptions, Open Item #9).
package pipeline

import "context"

// ValidationStage implements Stage 2 of the ingest-task pipeline.
type ValidationStage struct {
	BatchFiles  ports.BatchFileRepository
	DeadLetters ports.DeadLetterRepository
	Audit       ports.AuditLogWriter
	Obs         ports.IObservabilityPort
}

// Run decrypts and validates the SRG file.
// Returns parsed rows ready for Stage 3 row processing.
func (s *ValidationStage) Run(ctx context.Context, batchFileID string) error {
	// TODO: PGP decrypt using FIS public key from Secrets Manager
	// TODO: validate SRG format against confirmed column definitions (Open Item #9)
	// TODO: route malformed rows to dead_letter_store
	// TODO: check SHA-256 for duplicate file detection
	// TODO: transition batch_files status RECEIVED → VALIDATING
	return nil
}
