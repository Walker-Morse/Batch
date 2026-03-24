// Stage 5 — FIS Egress Deposit (§5.1):
//   Write the PGP-encrypted batch file to the S3 egress bucket for FIS pickup.
//
// Design change: outbound delivery was previously SSH/SCP to AWS Transfer Family.
// Stage 5 now writes directly to the dedicated egress S3 bucket. FIS retrieves
// the file via their own S3 access credentials (IAM principal, GetObject only).
// This eliminates the SFTP private key, host key pinning (OI #19 superseded),
// and Transfer Family endpoint dependency for the outbound path.
//
// The return file path (Stage 6) is unchanged — FIS still deposits return files
// into fis-exchange via Transfer Family inbound, and Stage 6 polls S3 for them.
//
// Egress S3 key convention: {tenant_id}/{filename}
//   e.g. rfu-oregon/ppppppppmmddccyyss.issuance.txt
//
// Status transition: ASSEMBLED → SUBMITTED
package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
)

// ProcessorDepositStage implements Stage 5 of the ingest-task pipeline.
type ProcessorDepositStage struct {
	Files      ports.FileStore
	BatchFiles ports.BatchFileRepository
	Audit      ports.AuditLogWriter
	Obs        ports.IObservabilityPort
	// FISExchangeBucket holds the PGP-encrypted file written by Stage 4.
	FISExchangeBucket string
	// EgressBucket is the S3 bucket FIS reads from (PutObject only for pipeline).
	EgressBucket string
}

// Run deposits the assembled file into the egress bucket for FIS pickup.
func (s *ProcessorDepositStage) Run(ctx context.Context, batchFile *ports.BatchFile, assembly *BatchAssemblyResult) error {
	// Fetch the PGP-encrypted file from fis-exchange (written by Stage 4)
	body, err := s.Files.GetObject(ctx, s.FISExchangeBucket, assembly.S3Key)
	if err != nil {
		return fmt.Errorf("stage5: get encrypted file from S3 (key=%s): %w", assembly.S3Key, err)
	}
	defer body.Close()

	// Write to egress bucket — FIS polls this bucket for pickup
	egressKey := fmt.Sprintf("%s/%s", batchFile.TenantID, assembly.Filename)
	if err := s.Files.PutObject(ctx, s.EgressBucket, egressKey, body); err != nil {
		_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "stage5.egress_failed",
			Level:         "ERROR",
			CorrelationID: batchFile.CorrelationID,
			TenantID:      batchFile.TenantID,
			BatchFileID:   batchFile.ID,
			Stage:         strPtr("stage5_processor_deposit"),
			Message:       fmt.Sprintf("egress deposit failed: key=%s/%s", batchFile.TenantID, assembly.Filename),
			Error:         strPtr(err.Error()),
		})
		return fmt.Errorf("stage5: deposit to egress bucket (key=%s): %w", egressKey, err)
	}

	// Transition batch_files → TRANSFERRED
	if err := s.BatchFiles.UpdateStatus(ctx, batchFile.ID, string(domain.BatchFileSubmitted), time.Now().UTC()); err != nil {
		return fmt.Errorf("stage5: update status SUBMITTED: %w", err)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:      batchFile.TenantID,
		EntityType:    "batch_files",
		EntityID:      batchFile.ID.String(),
		OldState:      strPtr("ASSEMBLED"),
		NewState:      "SUBMITTED",
		ChangedBy:     "ingest-task:stage5",
		CorrelationID: &batchFile.CorrelationID,
		Notes:         strPtr(fmt.Sprintf("egress_key=%s/%s records=%d", batchFile.TenantID, assembly.Filename, assembly.RecordCount)),
	})

	fn := assembly.Filename
	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage5.complete",
		Level:         "INFO",
		CorrelationID: batchFile.CorrelationID,
		TenantID:      batchFile.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("stage5_processor_deposit"),
		Filename:      &fn,
		Message:       fmt.Sprintf("egress deposit complete: key=%s/%s records=%d", batchFile.TenantID, assembly.Filename, assembly.RecordCount),
	})

	return nil
}
