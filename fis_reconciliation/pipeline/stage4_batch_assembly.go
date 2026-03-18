// Stage 4 — Batch Assembly (§5.1, §6.6):
//   Assemble FIS 400-byte fixed-width records → PGP-encrypt → write to S3 fis-exchange.
//
// The FIS adapter (fis_adapter package) owns ALL record format knowledge.
// This stage calls the ports.FISBatchAssembler interface only.
//
// CRITICAL: Plaintext staged S3 object MUST be deleted immediately after
// successful PGP encryption (§5.4.3). PHI must not persist in plaintext
// beyond Stage 4 completion. S3 lifecycle policy on staged/ prefix provides
// a 24-hour safety backstop but is not a substitute for application-level deletion.
//
// File splitting (Open Item #28): FIS recommends max 100,000 rows per
// personalized card issuance file. At 3 records per member (RT30+RT37+RT60),
// a 200K-member file = ~600K rows — well above the ceiling. Stage 4 must
// implement file splitting logic before the Batch Assembly sprint (Apr 27, 2026).
// Split threshold must be a configurable parameter, not hardcoded.
//
// Status transition: PROCESSING → ASSEMBLED
package pipeline

import (
	"context"
	"github.com/walker-morse/batch/_shared/ports"
)

// BatchAssemblyStage implements Stage 4 of the ingest-task pipeline.
type BatchAssemblyStage struct {
	Assembler  ports.FISBatchAssembler
	Files      ports.FileStore
	BatchFiles ports.BatchFileRepository
	Audit      ports.AuditLogWriter
	Obs        ports.IObservabilityPort
}

// Run assembles and PGP-encrypts the FIS batch file.
func (s *BatchAssemblyStage) Run(ctx context.Context, batchFileID string) error {
	// TODO: invoke FISBatchAssembler with correct TestProdIndicator from env
	// TODO: PGP-encrypt with FIS public key from Secrets Manager
	// TODO: write encrypted file to S3 fis-exchange prefix
	// TODO: DELETE plaintext staged object immediately after encryption
	// TODO: apply file splitting if record count > 100,000 (Open Item #28)
	// TODO: transition batch_files PROCESSING → ASSEMBLED
	return nil
}
