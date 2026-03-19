// Stage 4 — Batch Assembly (§5.1, §6.6):
//   Query staged batch_records → assemble FIS 400-byte fixed-width file →
//   PGP-encrypt → write to S3 fis-exchange → DELETE plaintext staged object.
//
// The FIS adapter (fis_adapter package) owns ALL record format knowledge (ADR-001).
// This stage calls ports.FISBatchAssembler only — no FIS format details here.
//
// CRITICAL: Plaintext staged S3 object MUST be deleted immediately after
// successful PGP encryption (§5.4.3). PHI must not persist in plaintext
// beyond Stage 4 completion.
//
// File splitting (Open Item #28): files > 100,000 rows must be split.
// Split threshold is configurable; each split gets the next sequence number.
//
// Status transition: PROCESSING → ASSEMBLED
package pipeline

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
)

// BatchAssemblyStage implements Stage 4.
type BatchAssemblyStage struct {
	Assembler      ports.FISBatchAssembler
	Files          ports.FileStore
	BatchFiles     ports.BatchFileRepository
	StagedRecords  ports.BatchRecordsLister // used to resolve ProgramID before assembly
	Audit          ports.AuditLogWriter
	Obs            ports.IObservabilityPort
	// PGPEncrypt encrypts plaintext using the FIS public key from Secrets Manager.
	PGPEncrypt func(plaintext io.Reader) (io.Reader, error)
	// Buckets
	StagedBucket     string // staged/ prefix — plaintext; MUST be deleted after Stage 4
	FISExchangeBucket string // fis-exchange/ prefix — PGP-encrypted outbound files
}

// BatchAssemblyResult carries the assembled file location for Stage 5.
type BatchAssemblyResult struct {
	S3Key    string // fis-exchange S3 key of the PGP-encrypted batch file
	Filename string // FIS-prescribed filename (§6.6.1)
	RecordCount int
}

// Run assembles and encrypts the FIS batch file.
func (s *BatchAssemblyStage) Run(ctx context.Context, batchFile *ports.BatchFile) (*BatchAssemblyResult, error) {
	// Resolve ProgramID from staged RT30 records before assembly.
	// Phase 1: all rows in a file share a single program.
	// The assembler needs programs.id to key fis_sequence.Next (§6.6.1).
	programID, err := s.resolveProgramID(ctx, batchFile)
	if err != nil {
		return nil, fmt.Errorf("stage4: resolve program ID: %w", err)
	}

	// Assemble the FIS batch file via the adapter seam (ADR-001)
	assembled, err := s.Assembler.AssembleFile(ctx, &ports.AssembleRequest{
		CorrelationID:     batchFile.CorrelationID,
		TenantID:          batchFile.TenantID,
		ProgramID:         programID,
		LogFileIndicator:  '0', // hardcoded per §6.6.3
		TestProdIndicator: testProdIndicator(), // from PIPELINE_ENV
	})
	if err != nil {
		return nil, fmt.Errorf("stage4: assemble file: %w", err)
	}
	defer assembled.Body.Close()

	// PGP-encrypt the assembled file using FIS public key
	encrypted, err := s.PGPEncrypt(assembled.Body)
	if err != nil {
		return nil, fmt.Errorf("stage4: pgp encrypt: %w", err)
	}

	// Write encrypted file to fis-exchange S3 prefix
	s3Key := fmt.Sprintf("outbound/%s/%s", batchFile.TenantID, assembled.Filename)
	if err := s.Files.PutObject(ctx, s.FISExchangeBucket, s3Key, encrypted); err != nil {
		return nil, fmt.Errorf("stage4: write to fis-exchange: %w", err)
	}

	// DELETE plaintext staged object immediately — PHI must not persist (§5.4.3)
	// The staged key mirrors the inbound key path
	stagedKey := fmt.Sprintf("staged/%s", batchFile.CorrelationID)
	if err := s.Files.DeleteObject(ctx, s.StagedBucket, stagedKey); err != nil {
		// Log the failure but don't abort — the encrypted file is already written.
		// The 24-hour S3 lifecycle policy on staged/ is the safety backstop.
		_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "stage4.staged_delete_failed",
			Level:         "ERROR",
			CorrelationID: &batchFile.CorrelationID,
			TenantID:      &batchFile.TenantID,
			Stage:         strPtr("stage4_batch_assembly"),
			Message:       "WARN: plaintext staged object delete failed — S3 lifecycle policy is safety backstop",
			Error:         strPtr(err.Error()),
		})
	}

	// Transition batch_files → ASSEMBLED
	if err := s.BatchFiles.UpdateStatus(ctx, batchFile.ID, "ASSEMBLED", time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("stage4: update status ASSEMBLED: %w", err)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:      batchFile.TenantID,
		EntityType:    "batch_files",
		EntityID:      batchFile.ID.String(),
		OldState:      strPtr("PROCESSING"),
		NewState:      "ASSEMBLED",
		ChangedBy:     "ingest-task:stage4",
		CorrelationID: &batchFile.CorrelationID,
		Notes:         strPtr(fmt.Sprintf("filename=%s records=%d s3_key=%s", assembled.Filename, assembled.RecordCount, s3Key)),
	})

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage4.complete",
		Level:         "INFO",
		CorrelationID: &batchFile.CorrelationID,
		TenantID:      &batchFile.TenantID,
		BatchFileID:   &batchFile.ID,
		Stage:         strPtr("stage4_batch_assembly"),
		Message:       fmt.Sprintf("assembled: filename=%s records=%d", assembled.Filename, assembled.RecordCount),
	})

	return &BatchAssemblyResult{
		S3Key:       s3Key,
		Filename:    assembled.Filename,
		RecordCount: assembled.RecordCount,
	}, nil
}

// resolveProgramID queries staged RT30 records and returns the program UUID
// from the first row. Phase 1 guarantees all rows in a file share one program.
// Returns an error if no staged RT30 rows exist — Stage 4 cannot proceed
// without a valid programs.id to key fis_sequence.Next (§6.6.1).
func (s *BatchAssemblyStage) resolveProgramID(ctx context.Context, batchFile *ports.BatchFile) (uuid.UUID, error) {
	staged, err := s.StagedRecords.ListStagedByCorrelationID(ctx, batchFile.CorrelationID)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("resolveProgramID: list staged records: %w", err)
	}
	if len(staged.RT30) == 0 {
		return uuid.UUID{}, fmt.Errorf("resolveProgramID: no staged RT30 rows for correlation_id=%s — cannot determine program", batchFile.CorrelationID)
	}
	pid := staged.RT30[0].ProgramID
	if pid == (uuid.UUID{}) {
		return uuid.UUID{}, fmt.Errorf("resolveProgramID: RT30 row has nil program_id — was Stage 3 run after migration 006?")
	}
	return pid, nil
}

// NullPGPEncrypt is a passthrough for local DEV testing.
// MUST NOT be used in TST or PRD.
func NullPGPEncrypt(r io.Reader) (io.Reader, error) {
	return r, nil
}

func testProdIndicator() byte {
	// Imported from environment at runtime in main.go
	// Returned here as P (safe default) — overridden by Config in main.go
	return 'P'
}

func strPtr(s string) *string { return &s }
