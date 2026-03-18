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
	"time"
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

// Run polls for the FIS return file within the configured timeout.
func (s *ReturnFileWaitStage) Run(ctx context.Context, correlationID string) error {
	if s.Timeout == 0 {
		s.Timeout = 6 * time.Hour
	}
	// TODO: poll FISTransport.PollForReturn with s.Timeout
	// TODO: on timeout: emit dead.letter.alert, transition batch_files → STALLED
	// TODO: on success: pass return file stream to Stage 7
	return nil
}
