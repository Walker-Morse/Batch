// Stage 5 — FIS Transfer (§5.1):
//   Deliver PGP-encrypted batch file to FIS Prepaid Sunrise via AWS Transfer Family.
//
// The FIS Transfer Family endpoint is sftp-outbound-fis (§5.1 infrastructure table).
// IAM role is scoped to fis-exchange S3 prefix only (§5.4.5).
//
// SFTP host key must be validated at connection time — host key pinning for the
// FIS endpoint must be confirmed with Kendra Williams / Sydney Ciano before
// Stage 1 implementation (§5.4.2, Open Item #19 dependencies).
//
// Duplicate filename silent suppression (§6.5.5): FIS silently discards a file
// whose name is identical to one already received. The per-day sequence counter
// in Stage 4 is the primary collision-avoidance mechanism. On-call triage for
// Stage 6 timeout must check whether FIS never processed the file vs. return
// file missed — these are different root causes.
//
// Status transition: ASSEMBLED → TRANSFERRED
package pipeline

import "context"

// FISTransferStage implements Stage 5 of the ingest-task pipeline.
type FISTransferStage struct {
	Transport  ports.FISTransport
	BatchFiles ports.BatchFileRepository
	Audit      ports.AuditLogWriter
	Obs        ports.IObservabilityPort
}

// Run delivers the assembled file to FIS.
func (s *FISTransferStage) Run(ctx context.Context, batchFileID string) error {
	// TODO: invoke FISTransport.Deliver
	// TODO: transition batch_files ASSEMBLED → TRANSFERRED
	// TODO: write audit_log entry
	return nil
}
