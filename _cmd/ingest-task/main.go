// Command ingest-task is the single ECS Fargate container for the complete
// One Fintech file lifecycle (ADR-003).
//
// Seven named pipeline stages — each a clean extraction point for future
// decomposition to separate compute units (ADR-001, ADR-003):
//
//   Stage 1 — File Arrival       S3 event received; SHA-256 hashes; batch_files row
//   Stage 2 — Validation         PGP decrypt; SRG format validation; dead-letter malformed rows
//   Stage 3 — Row Processing     Sequential row-by-row; idempotency gate; domain + mart writes
//   Stage 4 — Batch Assembly     FIS 400-byte fixed-width records; PGP-encrypt; S3 write
//   Stage 5 — FIS Transfer       AWS Transfer Family → FIS Prepaid Sunrise SFTP
//   Stage 6 — Return File Wait   Container polls S3 (6h timeout); emits dead.letter.alert on timeout
//   Stage 7 — Reconciliation     Match results; update status; write fact_reconciliation; deliver recon report
//
// One Fargate task is launched per inbound file. Concurrent MCO file arrivals
// run concurrent tasks — multi-client fan-in (§5.2a).
//
// Replay mode (--replay): invoked by replay-cli (Open Item #24) to re-process
// a specific dead-lettered row by correlation_id + row_sequence_number.
// Three-layer idempotency guarantee makes replay safe to invoke multiple times.
//
// TestProdIndicator (§6.6.4):
//   DEV: 'T' (format test only — no cards created at FIS)
//   TST: 'P' (real FIS processing — UAT requires this)
//   PRD: 'P' (always)
// Driven by PIPELINE_ENV environment variable — never hardcoded.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
)

// Config holds runtime configuration for the ingest-task.
// All sensitive values (PGP keys, DB password, Datadog API key) resolved
// from Secrets Manager at startup — never from environment variables (§5.4.5).
type Config struct {
	CorrelationID uuid.UUID
	S3Bucket      string
	S3Key         string
	TenantID      string
	Region        string

	// Stage 6 timeout — default 6h matching FIS SLA
	// Confirm with Kendra Williams before TST provisioning (Open Item #25)
	ReturnFileWaitTimeout time.Duration

	// TestProdIndicator: 'T' (DEV) or 'P' (TST/PRD) — driven by PIPELINE_ENV (§6.6.4)
	TestProdIndicator byte

	// Replay mode (invoked by replay-cli, Open Item #24)
	ReplayMode        bool
	ReplayRowSequence *int
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		log.Fatalf("ingest-task failed: %v", err)
	}
}

func parseConfig() (*Config, error) {
	correlationIDStr := flag.String("correlation-id", os.Getenv("CORRELATION_ID"), "correlation UUID")
	s3Bucket         := flag.String("s3-bucket", os.Getenv("S3_BUCKET"), "inbound S3 bucket")
	s3Key            := flag.String("s3-key", os.Getenv("S3_KEY"), "S3 object key")
	tenantID         := flag.String("tenant-id", os.Getenv("TENANT_ID"), "health plan client tenant ID")
	region           := flag.String("region", os.Getenv("AWS_REGION"), "AWS region")
	replay           := flag.Bool("replay", false, "replay mode — invoked by replay-cli")
	replaySeq        := flag.Int("replay-seq", 0, "row sequence number for replay")
	flag.Parse()

	if *correlationIDStr == "" {
		return nil, fmt.Errorf("correlation-id is required")
	}
	id, err := uuid.Parse(*correlationIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid correlation-id: %w", err)
	}

	cfg := &Config{
		CorrelationID:         id,
		S3Bucket:              *s3Bucket,
		S3Key:                 *s3Key,
		TenantID:              *tenantID,
		Region:                *region,
		ReplayMode:            *replay,
		ReturnFileWaitTimeout: 6 * time.Hour,
	}

	// TestProdIndicator from PIPELINE_ENV — never hardcoded (§6.6.4)
	switch os.Getenv("PIPELINE_ENV") {
	case "DEV":
		cfg.TestProdIndicator = 'T' // format test only — no cards created
	case "TST", "PRD":
		cfg.TestProdIndicator = 'P' // real FIS processing
	default:
		return nil, fmt.Errorf("PIPELINE_ENV must be DEV|TST|PRD")
	}

	if *replay && *replaySeq > 0 {
		seq := *replaySeq
		cfg.ReplayRowSequence = &seq
	}

	return cfg, nil
}

// run is the top-level pipeline orchestrator.
// Each stage is implemented in member_enrollment/pipeline/ or fis_reconciliation/pipeline/.
// Dependencies are injected via the shared/ports interfaces — no adapter imports here.
func run(ctx context.Context, cfg *Config) error {
	log.Printf("ingest-task starting: correlation_id=%s tenant_id=%s replay=%v",
		cfg.CorrelationID, cfg.TenantID, cfg.ReplayMode)

	// TODO: wire adapters from Secrets Manager + environment
	// TODO: Stage 1 — member_enrollment/pipeline.FileArrivalStage
	// TODO: Stage 2 — member_enrollment/pipeline.ValidationStage
	// TODO: Stage 3 — member_enrollment/pipeline.RowProcessingStage
	// TODO: Stage 4 — fis_reconciliation/pipeline.BatchAssemblyStage
	// TODO: Stage 5 — fis_reconciliation/pipeline.FISTransferStage
	// TODO: Stage 6 — fis_reconciliation/pipeline.ReturnFileWaitStage
	// TODO: Stage 7 — fis_reconciliation/pipeline.ReconciliationStage

	log.Printf("ingest-task complete: correlation_id=%s", cfg.CorrelationID)
	return nil
}
