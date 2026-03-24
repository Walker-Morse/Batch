// Stage 6 — Return File Wait (§5.1, §6.5.6):
//   Container remains alive and polls S3 for the FIS return file.
//
// Status transition: SUBMITTED → (Stage 7 handles COMPLETE/HALTED)
package pipeline

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
)

// ReturnFileWaitStage implements Stage 6 of the ingest-task pipeline.
type ReturnFileWaitStage struct {
	Transport  ports.FISTransport
	BatchFiles ports.BatchFileRepository
	Audit      ports.AuditLogWriter
	Obs        ports.IObservabilityPort
	// Timeout defaults to 6 hours — confirm with Kendra Williams (Open Item #25).
	Timeout time.Duration
}

// ReturnFileWaitResult carries the return file stream to Stage 7.
// Caller is responsible for closing Body.
type ReturnFileWaitResult struct {
	Body io.ReadCloser
}

// Run polls for the FIS return file within the configured timeout.
func (s *ReturnFileWaitStage) Run(ctx context.Context, batchFile *ports.BatchFile) (*ReturnFileWaitResult, error) {
	if s.Timeout == 0 {
		s.Timeout = 6 * time.Hour
	}

	returnBody, err := s.Transport.PollForReturn(ctx, batchFile.CorrelationID, s.Timeout)
	if err != nil {
		// Timeout or transport error — transition to STALLED, alert on-call
		_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "dead.letter.alert",
			Level:         "ERROR",
			CorrelationID: batchFile.CorrelationID,
			TenantID:      batchFile.TenantID,
			BatchFileID:   batchFile.ID,
			Stage:         strPtr("stage6_return_file_wait"),
			Message:       fmt.Sprintf("return file wait timeout after %s — batch STALLED; on-call: check FIS duplicate filename vs. missed return", s.Timeout),
			Error:         strPtr(err.Error()),
		})

		_ = s.BatchFiles.UpdateStatus(ctx, batchFile.ID, string(domain.BatchFileStalled), time.Now().UTC())

		_ = s.Audit.Write(ctx, &ports.AuditEntry{
			TenantID:      batchFile.TenantID,
			EntityType:    "batch_files",
			EntityID:      batchFile.ID.String(),
			OldState:      strPtr("SUBMITTED"),
			NewState:      "STALLED",
			ChangedBy:     "ingest-task:stage6",
			CorrelationID: &batchFile.CorrelationID,
			Notes:         strPtr(fmt.Sprintf("return_file_wait_timeout after %s", s.Timeout)),
		})

		return nil, fmt.Errorf("stage6: return file wait timeout (correlation_id=%s): %w", batchFile.CorrelationID, err)
	}

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage6.complete",
		Level:         "INFO",
		CorrelationID: batchFile.CorrelationID,
		TenantID:      batchFile.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("stage6_return_file_wait"),
		Message:       "return file received",
	})

	return &ReturnFileWaitResult{Body: returnBody}, nil
}
