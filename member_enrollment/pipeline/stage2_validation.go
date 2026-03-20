// Stage 2 — Validation (§5.1):
//   PGP decrypt (or passthrough for plaintext) → SRG format validation →
//   malformed rows → dead_letter_store → sha256_plaintext written.
//
// Status transition: RECEIVED → VALIDATING
//
// Two distinct failure paths:
//   1. Per-row parse failure: row goes to dead_letter_store, processing continues
//   2. File-level failure (decrypt error, unknown file type): batch_files → HALTED
//
// Encryption detection: handled upstream in wireDeps (main.go) by inspecting
// the S3 key suffix. PGPDecrypt is wired with either the real decrypter (for .pgp
// files) or PassthroughDecrypt (for plaintext .csv/.srg310/.srg315/.srg320 files).
// Stage 2 itself has no knowledge of whether the file is encrypted — it only
// calls PGPDecrypt and processes the resulting reader.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/member_enrollment/srg"
)

// ValidationStage implements Stage 2 of the ingest-task pipeline.
type ValidationStage struct {
	Files       ports.FileStore
	BatchFiles  ports.BatchFileRepository
	DeadLetters ports.DeadLetterRepository
	Audit       ports.AuditLogWriter
	Obs         ports.IObservabilityPort
	// PGPDecrypt is the decrypt function for this file.
	// Wire with the real PGP decrypter for .pgp files, or PassthroughDecrypt
	// for plaintext files. Selected by main.go based on S3 key suffix.
	PGPDecrypt func(r io.Reader) (io.Reader, error)
}

// ValidationResult carries parsed rows and per-row errors from Stage 2.
type ValidationResult struct {
	SRG310Rows   []*srg.SRG310Row
	SRG315Rows   []*srg.SRG315Row
	SRG320Rows   []*srg.SRG320Row
	ParseErrors  []srg.ParseError
	TotalRows    int
	PlaintextSHA string
}

// Run decrypts (or passes through) and validates the SRG file.
func (s *ValidationStage) Run(ctx context.Context, batchFile *ports.BatchFile, s3Bucket, s3Key string) (*ValidationResult, error) {
	_ = s.BatchFiles.UpdateStatus(ctx, batchFile.ID, "VALIDATING", time.Now().UTC())

	encrypted, err := s.Files.GetObject(ctx, s3Bucket, s3Key)
	if err != nil {
		return nil, s.halt(ctx, batchFile, fmt.Errorf("stage2: get object: %w", err))
	}
	defer encrypted.Close()

	// PGPDecrypt is either a real decrypter or PassthroughDecrypt depending on
	// the file's S3 key suffix — decided at wire time in main.go.
	plaintext, err := s.PGPDecrypt(encrypted)
	if err != nil {
		return nil, s.halt(ctx, batchFile, fmt.Errorf("stage2: pgp decrypt: %w", err))
	}

	hasher := sha256.New()
	tee := io.TeeReader(plaintext, hasher)

	result := &ValidationResult{}
	var parseErrs []srg.ParseError

	switch batchFile.FileType {
	case "SRG310":
		result.SRG310Rows, parseErrs = srg.ParseSRG310(tee)
	case "SRG315":
		result.SRG315Rows, parseErrs = srg.ParseSRG315(tee)
	case "SRG320":
		result.SRG320Rows, parseErrs = srg.ParseSRG320(tee)
	default:
		return nil, s.halt(ctx, batchFile, fmt.Errorf("stage2: unknown file_type %q", batchFile.FileType))
	}

	_, _ = io.Copy(io.Discard, tee)

	result.ParseErrors = parseErrs
	result.PlaintextSHA = hex.EncodeToString(hasher.Sum(nil))
	result.TotalRows = len(result.SRG310Rows) + len(result.SRG315Rows) +
		len(result.SRG320Rows) + len(parseErrs)

	if err := s.BatchFiles.SetRecordCount(ctx, batchFile.ID, result.TotalRows); err != nil {
		return nil, fmt.Errorf("stage2: set record_count: %w", err)
	}

	for _, pe := range parseErrs {
		s.deadLetterParseError(ctx, batchFile, pe)
		_ = s.BatchFiles.IncrementMalformedCount(ctx, batchFile.ID)
	}

	total := result.TotalRows
	malformed := len(parseErrs)
	malformedRate := "0.0%"
	if total > 0 {
		malformedRate = fmt.Sprintf("%.1f%%", float64(malformed)/float64(total)*100)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:      batchFile.TenantID,
		EntityType:    "batch_files",
		EntityID:      batchFile.ID.String(),
		OldState:      strPtr("RECEIVED"),
		NewState:      "VALIDATING",
		ChangedBy:     "ingest-task:stage2",
		CorrelationID: &batchFile.CorrelationID,
		Notes: strPtr(fmt.Sprintf("total_rows=%d malformed=%d sha_plaintext=%s",
			result.TotalRows, len(parseErrs), result.PlaintextSHA[:8])),
	})

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage2.complete",
		Level:         "INFO",
		CorrelationID: batchFile.CorrelationID,
		TenantID:      batchFile.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("stage2_validation"),
		Total:         &total,
		Malformed:     &malformed,
		MalformedRate: &malformedRate,
		Message: fmt.Sprintf("validation complete: total=%d malformed=%d",
			result.TotalRows, len(parseErrs)),
	})

	return result, nil
}

func (s *ValidationStage) halt(ctx context.Context, batchFile *ports.BatchFile, err error) error {
	_ = s.BatchFiles.UpdateStatus(ctx, batchFile.ID, "HALTED", time.Now().UTC())
	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "batch.halt.triggered",
		Level:         "ERROR",
		CorrelationID: batchFile.CorrelationID,
		TenantID:      batchFile.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("stage2_validation"),
		Message:       "batch HALTED at stage2",
		Error:         strPtr(err.Error()),
	})
	return err
}

func (s *ValidationStage) deadLetterParseError(ctx context.Context, batchFile *ports.BatchFile, pe srg.ParseError) {
	bfID := batchFile.ID
	seq := pe.Seq
	msgBody, _ := json.Marshal(map[string]interface{}{"raw": pe.RawRecord})
	_ = s.DeadLetters.Write(ctx, &ports.DeadLetterEntry{
		ID:                uuid.New(),
		CorrelationID:     batchFile.CorrelationID,
		BatchFileID:       &bfID,
		RowSequenceNumber: &seq,
		TenantID:          batchFile.TenantID,
		FailureStage:      "validation",
		FailureReason:     fmt.Sprintf("parse_error: %v", pe.Err),
		MessageBody:       msgBody,
		CreatedAt:         time.Now().UTC(),
	})
}

// PassthroughDecrypt is a no-op decrypt for plaintext SRG files
// (.csv, .srg310, .srg315, .srg320). Selected automatically by main.go
// when the S3 key does not end in .pgp.
func PassthroughDecrypt(r io.Reader) (io.Reader, error) {
	return r, nil
}

// NullPGPDecrypt is an alias for PassthroughDecrypt — kept for smoke test
// compatibility. Prefer PassthroughDecrypt in new code.
func NullPGPDecrypt(r io.Reader) (io.Reader, error) {
	return PassthroughDecrypt(r)
}
