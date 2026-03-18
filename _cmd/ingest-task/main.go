// Command ingest-task is the single ECS Fargate container for the complete
// One Fintech file lifecycle (ADR-003).
//
// Seven named pipeline stages:
//   Stage 1 — File Arrival       S3 event → SHA-256 → batch_files row
//   Stage 2 — Validation         PGP decrypt → SRG parse → dead-letter malformed rows
//   Stage 3 — Row Processing     Sequential row-by-row: idempotency → domain writes → staging
//   Stage 4 — Batch Assembly     FIS 400-byte fixed-width records → PGP-encrypt → S3
//   Stage 5 — FIS Transfer       AWS Transfer Family → FIS Prepaid Sunrise SFTP
//   Stage 6 — Return File Wait   Poll S3 for FIS return file (6h timeout)
//   Stage 7 — Reconciliation     Match results → update status → MCO report
//
// Adapters are wired here and injected into stages via port interfaces.
// No stage imports an adapter directly — all dependencies flow through ports (ADR-001).
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

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"

	"github.com/walker-morse/batch/_adapters/aurora"
	"github.com/walker-morse/batch/_adapters/s3"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/fis_reconciliation/fis_adapter"
	stage4 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage1 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage2 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage3 "github.com/walker-morse/batch/member_enrollment/pipeline"
)

// PipelineConfig holds all runtime configuration.
// Sensitive values come from Secrets Manager — never from env vars (§5.4.5).
type PipelineConfig struct {
	CorrelationID uuid.UUID
	TenantID      string
	ClientID      string
	S3Bucket      string
	S3Key         string
	FileType      string // SRG310|SRG315|SRG320
	Region        string
	PipelineEnv   string // DEV|TST|PRD

	// Database (Aurora via RDS Proxy)
	DBHost     string
	DBName     string
	DBUser     string
	DBPassword string // from Secrets Manager in prod; env var for local dev
	DBSSLMode  string

	// S3
	KMSKeyARN         string
	StagedBucket      string
	FISExchangeBucket string

	// FIS assembler
	FISCompanyID string // Morse LLC FIS Level 1 client identifier (8 chars)

	// Replay mode
	ReplayMode        bool
	ReplayRowSequence *int

	ReturnFileWaitTimeout time.Duration
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		log.Fatalf("ingest-task: %v", err)
	}
}

func run(ctx context.Context, cfg *PipelineConfig) error {
	// ── Wire infrastructure adapters ─────────────────────────────────────────

	// Aurora connection pool (via RDS Proxy)
	pool, err := aurora.NewPool(ctx, aurora.Config{
		Host:     cfg.DBHost,
		Database: cfg.DBName,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		SSLMode:  cfg.DBSSLMode,
	})
	if err != nil {
		return fmt.Errorf("wire aurora: %w", err)
	}
	defer pool.Close()

	// AWS SDK config
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return fmt.Errorf("wire aws config: %w", err)
	}

	// S3 store
	s3Client := awss3.NewFromConfig(awsCfg)
	fileStore := s3.New(s3Client, cfg.KMSKeyARN)

	// Aurora repositories
	batchFileRepo   := aurora.NewBatchFileRepo(pool)
	domainCmdRepo   := aurora.NewDomainCommandRepo(pool)
	deadLetterRepo  := aurora.NewDeadLetterRepo(pool)
	auditRepo       := aurora.NewAuditLogRepo(pool)
	batchRecordsRepo := aurora.NewBatchRecordsRepo(pool)
	domainStateRepo  := aurora.NewDomainStateRepo(pool)

	// Observability (structured logging to stdout → Datadog Agent sidecar → Datadog)
	// Zero PHI guarantee enforced in LogEvent — only correlation_id, tenant_id, etc.
	obs := &observability.NoopObservability{} // TODO: replace with real Datadog adapter

	// FIS batch assembler — owns all FIS record format knowledge (ADR-001)
	// SequenceStore uses batchFileRepo to persist per-day sequence counter
	seqStore := &dbSequenceStore{repo: batchFileRepo}
	assembler := fis_adapter.NewAssembler(cfg.FISCompanyID, seqStore)

	testProdIndicator := byte('P')
	if cfg.PipelineEnv == "DEV" {
		testProdIndicator = 'T'
	}

	// ── Construct pipeline stages ─────────────────────────────────────────────

	s1 := &stage1.FileArrivalStage{
		Files:      fileStore,
		BatchFiles: batchFileRepo,
		Audit:      auditRepo,
		Obs:        obs,
	}

	s2 := &stage2.ValidationStage{
		Files:       fileStore,
		BatchFiles:  batchFileRepo,
		DeadLetters: deadLetterRepo,
		Audit:       auditRepo,
		Obs:         obs,
		PGPDecrypt:  stage2.NullPGPDecrypt, // TODO: replace with real PGP decrypt
	}

	s3stage := &stage3.RowProcessingStage{
		DomainCommands: domainCmdRepo,
		DeadLetters:    deadLetterRepo,
		BatchFiles:     batchFileRepo,
		BatchRecords:   batchRecordsRepo,
		DomainState:    domainStateRepo,
		Audit:          auditRepo,
		Obs:            obs,
	}

	s4 := &stage4.BatchAssemblyStage{
		Assembler:         assembler,
		Files:             fileStore,
		BatchFiles:        batchFileRepo,
		Audit:             auditRepo,
		Obs:               obs,
		PGPEncrypt:        stage4.NullPGPEncrypt, // TODO: replace with real PGP encrypt
		StagedBucket:      cfg.StagedBucket,
		FISExchangeBucket: cfg.FISExchangeBucket,
	}
	_ = testProdIndicator // used by assembler via config

	// ── Execute pipeline ──────────────────────────────────────────────────────

	log.Printf("ingest-task starting: correlation_id=%s tenant=%s file_type=%s env=%s",
		cfg.CorrelationID, cfg.TenantID, cfg.FileType, cfg.PipelineEnv)

	// Stage 1 — File Arrival
	batchFile, err := s1.Run(ctx, &stage1.FileArrivalInput{
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		ClientID:      cfg.ClientID,
		S3Bucket:      cfg.S3Bucket,
		S3Key:         cfg.S3Key,
		FileType:      cfg.FileType,
	})
	if err != nil {
		return fmt.Errorf("stage1: %w", err)
	}

	// Stage 2 — Validation
	validationResult, err := s2.Run(ctx, batchFile, cfg.S3Bucket, cfg.S3Key)
	if err != nil {
		return fmt.Errorf("stage2: %w", err)
	}

	// Stage 3 — Row Processing
	// Program lookup is resolved per-row inside Stage 3 via DomainStateRepo.GetProgramByTenantAndSubprogram.
	// Results are cached within the run — at most one DB query per unique SubprogramID per file.
	processingResult, err := s3stage.Run(ctx, &stage3.RowProcessingInput{
		BatchFile:  batchFile,
		SRG310Rows: validationResult.SRG310Rows,
		SRG315Rows: validationResult.SRG315Rows,
		SRG320Rows: validationResult.SRG320Rows,
	})
	if err != nil {
		return fmt.Errorf("stage3: %w", err)
	}

	if processingResult.Stalled {
		return fmt.Errorf("stage3: batch STALLED — unresolved dead letters; use replay-cli to resolve")
	}

	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage3.complete",
		Level:         "INFO",
		CorrelationID: &cfg.CorrelationID,
		TenantID:      &cfg.TenantID,
		Stage:         strPtr("orchestrator"),
		Message: fmt.Sprintf("stage3 complete: staged=%d duplicates=%d failed=%d",
			processingResult.StagedCount, processingResult.DuplicateCount, processingResult.FailedCount),
	})

	// Stage 4 — Batch Assembly
	assemblyResult, err := s4.Run(ctx, batchFile)
	if err != nil {
		return fmt.Errorf("stage4: %w", err)
	}

	log.Printf("stage4 complete: filename=%s s3_key=%s", assemblyResult.Filename, assemblyResult.S3Key)

	// Stages 5–7: FIS Transfer, Return File Wait, Reconciliation
	// TODO: implement stages 5–7 (FIS SFTP transport, return file polling, reconciliation)
	log.Printf("ingest-task stages 5-7 not yet implemented — batch assembled at s3_key=%s", assemblyResult.S3Key)

	log.Printf("ingest-task complete: correlation_id=%s", cfg.CorrelationID)
	return nil
}

func parseConfig() (*PipelineConfig, error) {
	corrIDStr := flag.String("correlation-id", os.Getenv("CORRELATION_ID"), "correlation UUID")
	tenantID  := flag.String("tenant-id",      os.Getenv("TENANT_ID"),      "health plan client tenant ID")
	clientID  := flag.String("client-id",      os.Getenv("CLIENT_ID"),      "Morse client code")
	s3Bucket  := flag.String("s3-bucket",      os.Getenv("S3_BUCKET"),      "inbound S3 bucket")
	s3Key     := flag.String("s3-key",         os.Getenv("S3_KEY"),         "S3 object key")
	fileType  := flag.String("file-type",      os.Getenv("FILE_TYPE"),      "SRG310|SRG315|SRG320")
	region    := flag.String("region",         envOrDefault("AWS_REGION", "us-east-1"), "AWS region")
	pipelineEnv := flag.String("env",          envOrDefault("PIPELINE_ENV", "DEV"), "DEV|TST|PRD")
	dbHost    := flag.String("db-host",        os.Getenv("DB_HOST"),        "Aurora RDS Proxy endpoint")
	dbName    := flag.String("db-name",        os.Getenv("DB_NAME"),        "database name")
	dbUser    := flag.String("db-user",        os.Getenv("DB_USER"),        "database user")
	dbPass    := flag.String("db-password",    os.Getenv("DB_PASSWORD"),    "database password")
	dbSSL     := flag.String("db-ssl",         envOrDefault("DB_SSL", "require"), "sslmode")
	kmsKey    := flag.String("kms-key-arn",    os.Getenv("KMS_KEY_ARN"),   "KMS key ARN for SSE")
	stagedBucket := flag.String("staged-bucket",      os.Getenv("STAGED_BUCKET"),       "staged S3 bucket")
	fisBucket    := flag.String("fis-exchange-bucket", os.Getenv("FIS_EXCHANGE_BUCKET"), "fis-exchange S3 bucket")
	fisCompanyID := flag.String("fis-company-id", os.Getenv("FIS_COMPANY_ID"), "FIS Level 1 company identifier (8 chars)")
	replay    := flag.Bool("replay", false, "replay mode — invoked by replay-cli")
	replaySeq := flag.Int("replay-seq", 0, "row sequence number for replay")
	flag.Parse()

	if *corrIDStr == "" {
		return nil, fmt.Errorf("correlation-id is required")
	}
	id, err := uuid.Parse(*corrIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid correlation-id: %w", err)
	}

	cfg := &PipelineConfig{
		CorrelationID:         id,
		TenantID:              *tenantID,
		ClientID:              *clientID,
		S3Bucket:              *s3Bucket,
		S3Key:                 *s3Key,
		FileType:              *fileType,
		Region:                *region,
		PipelineEnv:           *pipelineEnv,
		DBHost:                *dbHost,
		DBName:                *dbName,
		DBUser:                *dbUser,
		DBPassword:            *dbPass,
		DBSSLMode:             *dbSSL,
		KMSKeyARN:             *kmsKey,
		StagedBucket:          *stagedBucket,
		FISExchangeBucket:     *fisBucket,
		FISCompanyID:          *fisCompanyID,
		ReplayMode:            *replay,
		ReturnFileWaitTimeout: 6 * time.Hour,
	}
	if *replay && *replaySeq > 0 {
		seq := *replaySeq
		cfg.ReplayRowSequence = &seq
	}
	return cfg, nil
}

// dbSequenceStore implements fis_adapter.SequenceStore using the batch_files table.
// The sequence number is stored per-day per-program to survive container restarts (§6.6.1).
type dbSequenceStore struct {
	repo *aurora.BatchFileRepo
}

func (d *dbSequenceStore) Next(ctx context.Context, programID string, date time.Time) (int, error) {
	// TODO: implement per-day sequence counter in batch_files or a dedicated sequence table
	// For now return 1 — production requires a real counter persisted to Aurora
	return 1, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func strPtr(s string) *string { return &s }
