// Stage 5 — FIS Transfer (§5.1):
//   Deliver PGP-encrypted batch file to FIS Prepaid Sunrise via AWS Transfer Family.
//
// Status transition: ASSEMBLED → TRANSFERRED
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
)

// FISTransferStage implements Stage 5 of the ingest-task pipeline.
type FISTransferStage struct {
	Transport  ports.FISTransport
	Files      ports.FileStore
	BatchFiles ports.BatchFileRepository
	Audit      ports.AuditLogWriter
	Obs        ports.IObservabilityPort
	// FISExchangeBucket is the S3 bucket holding the PGP-encrypted outbound file.
	FISExchangeBucket string
}

// Run delivers the assembled file to FIS via the transport adapter.
func (s *FISTransferStage) Run(ctx context.Context, batchFile *ports.BatchFile, assembly *BatchAssemblyResult) error {
	// Fetch the PGP-encrypted file from S3
	body, err := s.Files.GetObject(ctx, s.FISExchangeBucket, assembly.S3Key)
	if err != nil {
		return fmt.Errorf("stage5: get encrypted file from S3 (key=%s): %w", assembly.S3Key, err)
	}
	defer body.Close()

	// Deliver to FIS via AWS Transfer Family SFTP
	if err := s.Transport.Deliver(ctx, body, assembly.Filename); err != nil {
		_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "stage5.transfer_failed",
			Level:         "ERROR",
			CorrelationID: batchFile.CorrelationID,
			TenantID:      batchFile.TenantID,
			BatchFileID:   batchFile.ID,
			Stage:         strPtr("stage5_fis_transfer"),
			Message:       fmt.Sprintf("FIS transfer failed: filename=%s", assembly.Filename),
			Error:         strPtr(err.Error()),
		})
		return fmt.Errorf("stage5: deliver to FIS (filename=%s): %w", assembly.Filename, err)
	}

	// Transition batch_files → TRANSFERRED
	if err := s.BatchFiles.UpdateStatus(ctx, batchFile.ID, string(domain.BatchFileTransferred), time.Now().UTC()); err != nil {
		return fmt.Errorf("stage5: update status TRANSFERRED: %w", err)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:      batchFile.TenantID,
		EntityType:    "batch_files",
		EntityID:      batchFile.ID.String(),
		OldState:      strPtr("ASSEMBLED"),
		NewState:      "TRANSFERRED",
		ChangedBy:     "ingest-task:stage5",
		CorrelationID: &batchFile.CorrelationID,
		Notes:         strPtr(fmt.Sprintf("filename=%s records=%d", assembly.Filename, assembly.RecordCount)),
	})

	fn := assembly.Filename
	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage5.complete",
		Level:         "INFO",
		CorrelationID: batchFile.CorrelationID,
		TenantID:      batchFile.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("stage5_fis_transfer"),
		Filename:      &fn,
		Message:       fmt.Sprintf("transferred: filename=%s records=%d", assembly.Filename, assembly.RecordCount),
	})

	return nil
}
