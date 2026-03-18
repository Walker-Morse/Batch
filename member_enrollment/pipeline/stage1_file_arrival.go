// Package pipeline implements the One Fintech ingest-task pipeline stages
// for member enrollment processing.
//
// Stage 1 — File Arrival (§5.1):
//   S3 arrival event received → SHA-256 hashes computed → non-repudiation row
//   written to batch_files → file acknowledgement delivered to MCO outbound SFTP.
//
// The batch_files row is the non-repudiation anchor for the entire pipeline.
// It is written BEFORE any processing begins. SHA-256 of the encrypted file
// and SHA-256 of the decrypted file are written at this stage.
//
// Status transitions: (new) → RECEIVED
//
// Structured log event emitted: "file.arrived" — compatible with a future
// EventBridge file.arrived rule (ADR-001 evolution-ready seam).
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/shared/observability"
	"github.com/walker-morse/batch/shared/ports"
)

// FileArrivalStage implements Stage 1 of the ingest-task pipeline.
type FileArrivalStage struct {
	Files      ports.FileStore
	BatchFiles ports.BatchFileRepository
	Audit      ports.AuditLogWriter
	Obs        ports.IObservabilityPort
}

// FileArrivalInput carries the S3 event payload for Stage 1.
type FileArrivalInput struct {
	CorrelationID uuid.UUID
	TenantID      string
	ClientID      string
	S3Bucket      string
	S3Key         string
	FileType      string // SRG310|SRG315|SRG320
}

// Run executes Stage 1.
// Returns the BatchFile row written to the database for downstream stages.
func (s *FileArrivalStage) Run(ctx context.Context, in *FileArrivalInput) (*ports.BatchFile, error) {
	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     observability.EventFileArrived,
		Level:         "INFO",
		CorrelationID: &in.CorrelationID,
		TenantID:      &in.TenantID,
		ClientID:      &in.ClientID,
		Stage:         strPtr("stage1_file_arrival"),
		Message:       "file arrived",
	})

	// TODO: compute sha256_encrypted via HeadObject/GetObject
	// TODO: write batch_files row with status=RECEIVED
	// TODO: deliver file acknowledgement to MCO outbound SFTP
	// TODO: write audit_log entry

	now := time.Now().UTC()
	f := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: in.CorrelationID,
		TenantID:      in.TenantID,
		ClientID:      in.ClientID,
		FileType:      in.FileType,
		Status:        "RECEIVED",
		ArrivedAt:     now,
		UpdatedAt:     now,
	}

	if err := s.BatchFiles.Create(ctx, f); err != nil {
		return nil, fmt.Errorf("stage1: failed to write batch_files row: %w", err)
	}

	return f, nil
}

func strPtr(s string) *string { return &s }
