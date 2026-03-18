// Stage 6 — Return File Wait (§5.1, §6.5.6):
//   Container remains alive and polls S3 for the FIS return file.
//
// The container running through Stage 5 (FIS Transfer) is the natural compute unit
// for Stage 6 — this is why the single-container architecture was chosen (ADR-003).
// Fargate billing accrues for the full wait duration (up to 6 hours).
//
// Timeout: 6 hours default — matches FIS SLA (~45-60 min / 50K records).
// Confirm with Kendra Williams before TST provisioning (Open Item #25).
// If FIS SLA is confirmed shorter, reduce timeout to control Fargate cost.
//
// FIS-side processing model (§5.2a):
//   - Each client has a dedicated FIS batch loader
//   - Files are processed FIFO within a single client
//   - A second file submitted while the first is processing must wait
//   - Stage 6 timeout must account for FIFO queuing depth on multi-submission days
//
// On timeout:
//   - emit "dead.letter.alert" log event
//   - transition batch_files → STALLED
//   - CloudWatch alarm fires → Datadog P1 alert → on-call notification
//   - On-call triage: confirm whether return file was never generated (FIS never
//     processed the file — check for duplicate filename) vs. return file arrived
//     but was missed
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
// Returns the return file stream for Stage 7 consumption on success.
// On timeout: transitions batch_files → STALLED, emits dead.letter.alert, returns error.
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
			CorrelationID: &batchFile.CorrelationID,
			TenantID:      &batchFile.TenantID,
			BatchFileID:   &batchFile.ID,
			Stage:         strPtr("stage6_return_file_wait"),
			Message:       fmt.Sprintf("return file wait timeout after %s — batch STALLED; on-call: check FIS duplicate filename vs. missed return", s.Timeout),
			Error:         strPtr(err.Error()),
		})

		_ = s.BatchFiles.UpdateStatus(ctx, batchFile.ID, string(domain.BatchFileStalled), time.Now().UTC())

		_ = s.Audit.Write(ctx, &ports.AuditEntry{
			TenantID:      batchFile.TenantID,
			EntityType:    "batch_files",
			EntityID:      batchFile.ID.String(),
			OldState:      strPtr("TRANSFERRED"),
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
		CorrelationID: &batchFile.CorrelationID,
		TenantID:      &batchFile.TenantID,
		BatchFileID:   &batchFile.ID,
		Stage:         strPtr("stage6_return_file_wait"),
		Message:       "return file received",
	})

	return &ReturnFileWaitResult{Body: returnBody}, nil
}
