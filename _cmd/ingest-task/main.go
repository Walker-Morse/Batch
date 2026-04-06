// Command ingest-task is the single ECS Fargate container image for the
// One Fintech file lifecycle (ADR-003). It runs in two modes:
//
//   --mode ingest (default)
//     Stages 1–5: File Arrival → Validation → Row Processing →
//     Batch Assembly → FIS Transfer. Exits after Stage 5.
//     Triggered by EventBridge file.arrived.
//
//   --mode reconcile
//     Stage 6: Reconciliation. Reads the FIS return file from S3 and
//     reconciles results against staged batch records.
//     Triggered by EventBridge return.file.arrived (hours after ingest).
//     Requires --return-file-bucket, --return-file-key.
//
// Same image, two ECS task definitions, different TASK_MODE env var.
// This eliminates idle Fargate billing during FIS processing (up to 6 hours).
//
// Adapters are wired here and injected into stages via port interfaces.
// No stage imports an adapter directly — all dependencies flow through ports (ADR-001).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/google/uuid"

	"github.com/walker-morse/batch/_adapters/aurora"
	pgpadapter "github.com/walker-morse/batch/_adapters/pgp"
	"github.com/walker-morse/batch/_adapters/s3"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/fis_reconciliation/fis_adapter"
	fisp "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage1 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage2 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage3 "github.com/walker-morse/batch/member_enrollment/pipeline"
)

// PipelineConfig holds all runtime configuration.
// Sensitive values come from Secrets Manager — never from env vars (§5.4.5).
type PipelineConfig struct {
	// Mode selects the execution path: "ingest" (Stages 1–5) or "reconcile" (Stage 6).
	// Set via --mode flag or TASK_MODE env var.
	Mode string

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
	DBPassword string
	DBSSLMode  string

	// S3
	KMSKeyARN         string
	StagedBucket      string
	SCPExchangeBucket string
	EgressBucket      string

	// SCP assembler
	SCPCompanyID string

	// PGP key ARNs (Secrets Manager). Empty = NullPGP passthrough (DEV only).
	PGPPrivateKeySecretARN   string
	PGPPassphraseSecretARN   string
	PGPSCPPublicKeySecretARN string

	// Reconcile mode — required when Mode == "reconcile"
	ReturnFileBucket string // S3 bucket containing the FIS return file
	ReturnFileKey    string // S3 key of the FIS return file

	// Replay mode
	ReplayMode        bool
	ReplayRowSequence *int
}

// PipelineDeps holds all wired port implementations.
type PipelineDeps struct {
	FileStore ports.FileStore // shared S3 adapter — used in both modes
	Stage1    *stage1.FileArrivalStage
	Stage2    *stage2.ValidationStage
	Stage3    *stage3.RowProcessingStage
	Stage4    *fisp.BatchAssemblyStage
	Stage5    *fisp.ProcessorDepositStage
	// Stage 6 (ReturnFileWaitStage) removed — polling replaced by EventBridge trigger.
	// ReconciliationStage is used in reconcile mode (triggered by return.file.arrived).
	Stage7 *fisp.ReconciliationStage
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	deps, err := wireDeps(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wire: %v\n", err)
		os.Exit(1)
	}

	if err := runWithDeps(ctx, cfg, deps); err != nil {
		fmt.Fprintf(os.Stderr, "ingest-task: %v\n", err)
		os.Exit(1)
	}
}

// wireDeps constructs all real infrastructure adapters and wires them into stages.
func wireDeps(ctx context.Context, cfg *PipelineConfig) (*PipelineDeps, error) {
	pool, err := aurora.NewPool(ctx, aurora.Config{
		Host:     cfg.DBHost,
		Database: cfg.DBName,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		SSLMode:  cfg.DBSSLMode,
	})
	if err != nil {
		return nil, fmt.Errorf("wire aurora: %w", err)
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("wire aws config: %w", err)
	}

	smClient := secretsmanager.NewFromConfig(awsCfg)
	s3Client := awss3.NewFromConfig(awsCfg)
	fileStore := s3.New(s3Client, cfg.KMSKeyARN)

	batchFileRepo    := aurora.NewBatchFileRepo(pool)
	domainCmdRepo    := aurora.NewDomainCommandRepo(pool)
	deadLetterRepo   := aurora.NewDeadLetterRepo(pool)
	auditRepo        := aurora.NewAuditLogRepo(pool)
	batchRecordsRepo := aurora.NewBatchRecordsRepo(pool)
	domainStateRepo  := aurora.NewDomainStateRepo(pool)

	obs := observability.NewCloudWatchAdapter(envOrDefault("PIPELINE_ENV", "DEV"))

	seqStore  := aurora.NewFISSequenceRepo(pool)
	assembler := fis_adapter.NewAssembler(cfg.SCPCompanyID, seqStore, batchRecordsRepo)

	testProdIndicator := byte('P')
	if cfg.PipelineEnv == "DEV" {
		testProdIndicator = 'T'
	}
	_ = testProdIndicator

	// PGP decrypt (Stage 2) — detect from S3 key suffix at runtime
	var pgpDecrypt func(io.Reader) (io.Reader, error)
	if strings.HasSuffix(strings.ToLower(cfg.S3Key), ".pgp") {
		if cfg.PGPPrivateKeySecretARN == "" {
			pool.Close()
			return nil, fmt.Errorf("S3 key %q has .pgp suffix but PGP_PRIVATE_KEY_SECRET_ARN is not set", cfg.S3Key)
		}
		dec, err := pgpadapter.LoadDecrypter(ctx, smClient, cfg.PGPPrivateKeySecretARN, cfg.PGPPassphraseSecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load PGP decrypter: %w", err)
		}
		pgpDecrypt = dec.Decrypt
		_ = obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "pipeline.startup",
			Level:         "INFO",
			CorrelationID: cfg.CorrelationID,
			TenantID:      cfg.TenantID,
			BatchFileID:   uuid.Nil,
			Stage:         strPtr("pipeline"),
			Message:       "PGP decrypt active: file is encrypted",
		})
	} else {
		pgpDecrypt = stage2.PassthroughDecrypt
		_ = obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "pipeline.startup",
			Level:         "INFO",
			CorrelationID: cfg.CorrelationID,
			TenantID:      cfg.TenantID,
			BatchFileID:   uuid.Nil,
			Stage:         strPtr("pipeline"),
			Message:       "passthrough decrypt active: file is plaintext (no .pgp suffix)",
		})
	}

	// PGP encrypt (Stage 4)
	var pgpEncrypt func(io.Reader) (io.Reader, error)
	if cfg.PGPSCPPublicKeySecretARN == "" {
		if cfg.PipelineEnv != "DEV" {
			pool.Close()
			return nil, fmt.Errorf("PGP_SCP_PUBLIC_KEY_SECRET_ARN required in %s", cfg.PipelineEnv)
		}
		_ = obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "pipeline.warn",
			Level:         "WARN",
			CorrelationID: cfg.CorrelationID,
			TenantID:      cfg.TenantID,
			BatchFileID:   uuid.Nil,
			Stage:         strPtr("pipeline"),
			Message:       "NullPGPEncrypt active — DEV only; never use in TST or PRD",
		})
		pgpEncrypt = fisp.NullPGPEncrypt
	} else {
		enc, err := pgpadapter.LoadEncrypter(ctx, smClient, cfg.PGPSCPPublicKeySecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load PGP encrypter: %w", err)
		}
		pgpEncrypt = enc.Encrypt
	}

	var martWriter ports.MartWriter = aurora.NewMartWriterRepo(pool)

	return &PipelineDeps{
		FileStore: fileStore,
		Stage1: &stage1.FileArrivalStage{
			Files:      fileStore,
			BatchFiles: batchFileRepo,
			Audit:      auditRepo,
			Obs:        obs,
		},
		Stage2: &stage2.ValidationStage{
			Files:       fileStore,
			BatchFiles:  batchFileRepo,
			DeadLetters: deadLetterRepo,
			Audit:       auditRepo,
			Obs:         obs,
			PGPDecrypt:  pgpDecrypt,
		},
		Stage3: &stage3.RowProcessingStage{
			DomainCommands: domainCmdRepo,
			DeadLetters:    deadLetterRepo,
			BatchFiles:     batchFileRepo,
			BatchRecords:   batchRecordsRepo,
			DomainState:    domainStateRepo,
			Programs:       domainStateRepo,
			Audit:          auditRepo,
			Obs:            obs,
			Mart:           martWriter,
		},
		Stage4: &fisp.BatchAssemblyStage{
			Assembler:         assembler,
			Files:             fileStore,
			BatchFiles:        batchFileRepo,
			StagedRecords:     batchRecordsRepo,
			Audit:             auditRepo,
			Obs:               obs,
			PGPEncrypt:        pgpEncrypt,
			StagedBucket:      cfg.StagedBucket,
			FISExchangeBucket: cfg.SCPExchangeBucket,
		},
		Stage5: &fisp.ProcessorDepositStage{
			Files:             fileStore,
			BatchFiles:        batchFileRepo,
			Audit:             auditRepo,
			Obs:               obs,
			FISExchangeBucket: cfg.SCPExchangeBucket,
			EgressBucket:      cfg.EgressBucket,
		},
		Stage7: &fisp.ReconciliationStage{
			BatchFiles:     batchFileRepo,
			BatchRecords:   batchRecordsRepo,
			DomainCommands: domainCmdRepo,
			DomainState:    domainStateRepo,
			DeadLetters:    deadLetterRepo,
			Audit:          auditRepo,
			Mart:           martWriter,
			Obs:            obs,
		},
	}, nil
}

// runWithDeps dispatches to runIngest or runReconcile based on cfg.Mode.
func runWithDeps(ctx context.Context, cfg *PipelineConfig, deps *PipelineDeps) error {
	if cfg.Mode == "reconcile" {
		return runReconcile(ctx, cfg, deps)
	}
	return runIngest(ctx, cfg, deps)
}

// runIngest executes Stages 1–5 and exits. Triggered by file.arrived.
func runIngest(ctx context.Context, cfg *PipelineConfig, deps *PipelineDeps) error {
	obs := deps.Stage1.Obs
	startTime := time.Now()
	ft := cfg.FileType
	s3k := cfg.S3Key

	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "pipeline.startup",
		Level:         "INFO",
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		BatchFileID:   uuid.Nil,
		ClientID:      &cfg.ClientID,
		Stage:         strPtr("pipeline"),
		FileType:      &ft,
		S3Key:         &s3k,
		Message:       fmt.Sprintf("ingest starting: tenant=%s file_type=%s env=%s", cfg.TenantID, cfg.FileType, cfg.PipelineEnv),
	})

	// ── Stage 1 — File Arrival ────────────────────────────────────────────
	s1Start := time.Now()
	batchFile, err := deps.Stage1.Run(ctx, &stage1.FileArrivalInput{
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		ClientID:      cfg.ClientID,
		S3Bucket:      cfg.S3Bucket,
		S3Key:         cfg.S3Key,
		FileType:      cfg.FileType,
	})
	if err != nil {
		return pipelineError(ctx, obs, cfg, uuid.Nil, startTime, fmt.Errorf("stage1: %w", err))
	}
	_ = obs.RecordMetric(ctx, observability.MetricStageDurationMs, float64(time.Since(s1Start).Milliseconds()), map[string]string{
		"stage": "stage1_file_arrival", "tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	// ── Stage 2 — Validation ──────────────────────────────────────────────
	s2Start := time.Now()
	validationResult, err := deps.Stage2.Run(ctx, batchFile, cfg.S3Bucket, cfg.S3Key)
	if err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage2: %w", err))
	}
	_ = obs.RecordMetric(ctx, observability.MetricStageDurationMs, float64(time.Since(s2Start).Milliseconds()), map[string]string{
		"stage": "stage2_validation", "tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	// ── Stage 3 — Row Processing ──────────────────────────────────────────
	s3Start := time.Now()
	processingResult, err := deps.Stage3.Run(ctx, &stage3.RowProcessingInput{
		BatchFile:  batchFile,
		SRG310Rows: validationResult.SRG310Rows,
		SRG315Rows: validationResult.SRG315Rows,
		SRG320Rows: validationResult.SRG320Rows,
	})
	if err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage3: %w", err))
	}
	if processingResult.Stalled {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime,
			fmt.Errorf("stage3: batch STALLED — unresolved dead letters; use replay-cli to resolve"))
	}
	_ = obs.RecordMetric(ctx, observability.MetricStageDurationMs, float64(time.Since(s3Start).Milliseconds()), map[string]string{
		"stage": "stage3_row_processing", "tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	// Emit dead_letter_rate and malformed_rate metrics
	total3 := processingResult.StagedCount + processingResult.DuplicateCount + processingResult.FailedCount
	dlRate := 0.0
	if total3 > 0 {
		dlRate = float64(processingResult.FailedCount) / float64(total3) * 100
	}
	_ = obs.RecordMetric(ctx, observability.MetricDeadLetterRate, dlRate, map[string]string{"tenant_id": cfg.TenantID, "env": cfg.PipelineEnv})
	malformedRate := 0.0
	if validationResult.TotalRows > 0 {
		malformedRate = float64(len(validationResult.ParseErrors)) / float64(validationResult.TotalRows) * 100
	}
	_ = obs.RecordMetric(ctx, "malformed_rate", malformedRate, map[string]string{"tenant_id": cfg.TenantID, "env": cfg.PipelineEnv})

	// ── Stage 3 guard: skip assembly if nothing staged ────────────────────
	// All rows dead-lettered or duplicate — do not assemble or transfer an
	// empty file to FIS. Log pipeline.complete and exit cleanly.
	if processingResult.StagedCount == 0 {
		durationMs := time.Since(startTime).Milliseconds()
		staged := processingResult.StagedCount
		dl := processingResult.FailedCount
		dups := processingResult.DuplicateCount
		zero := 0
		_ = obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "pipeline.complete",
			Level:         "INFO",
			CorrelationID: cfg.CorrelationID,
			TenantID:      cfg.TenantID,
			BatchFileID:   batchFile.ID,
			Stage:         strPtr("pipeline"),
			DurationMs:    &durationMs,
			Staged:        &staged,
			Enrolled:      &zero,
			DeadLettered:  &dl,
			Duplicates:    &dups,
			Message: fmt.Sprintf(
				"ingest complete (no staged records — assembly skipped): duration=%dms staged=0 dead_lettered=%d duplicates=%d",
				durationMs, dl, dups),
		})
		return nil
	}

	// ── Stage 4 — Batch Assembly ──────────────────────────────────────────
	s4Start := time.Now()
	assemblyResult, err := deps.Stage4.Run(ctx, batchFile)
	if err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage4: %w", err))
	}
	_ = obs.RecordMetric(ctx, observability.MetricStageDurationMs, float64(time.Since(s4Start).Milliseconds()), map[string]string{
		"stage": "stage4_batch_assembly", "tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	// ── Stage 5 — FIS Transfer ────────────────────────────────────────────
	s5Start := time.Now()
	if err := deps.Stage5.Run(ctx, batchFile, assemblyResult); err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage5: %w", err))
	}
	_ = obs.RecordMetric(ctx, observability.MetricStageDurationMs, float64(time.Since(s5Start).Milliseconds()), map[string]string{
		"stage": "stage5_processor_deposit", "tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	// ── Ingest complete — container exits ─────────────────────────────────
	// Reconciliation runs in a separate reconcile-task container triggered
	// by EventBridge return.file.arrived when FIS deposits the return file.
	durationMs := time.Since(startTime).Milliseconds()
	staged := processingResult.StagedCount
	dl := processingResult.FailedCount
	dups := processingResult.DuplicateCount

	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "ingest.complete",
		Level:         "INFO",
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("pipeline"),
		DurationMs:    &durationMs,
		Staged:        &staged,
		DeadLettered:  &dl,
		Duplicates:    &dups,
		Message: fmt.Sprintf(
			"ingest complete: duration=%dms staged=%d dead_lettered=%d duplicates=%d — awaiting return.file.arrived for reconciliation",
			durationMs, staged, dl, dups),
	})

	_ = obs.RecordMetric(ctx, observability.MetricPipelineDurationMs, float64(durationMs), map[string]string{
		"tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	return nil
}

// runReconcile executes Stage 6 Reconciliation. Triggered by return.file.arrived.
// Reads the FIS return file from S3 using cfg.ReturnFileBucket / cfg.ReturnFileKey.
func runReconcile(ctx context.Context, cfg *PipelineConfig, deps *PipelineDeps) error {
	obs := deps.Stage7.Obs
	startTime := time.Now()

	if cfg.ReturnFileBucket == "" || cfg.ReturnFileKey == "" {
		return fmt.Errorf("reconcile mode requires --return-file-bucket and --return-file-key")
	}

	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "reconcile.startup",
		Level:         "INFO",
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		BatchFileID:   uuid.Nil,
		Stage:         strPtr("pipeline"),
		Message: fmt.Sprintf("reconcile starting: tenant=%s correlation_id=%s return_file=%s/%s",
			cfg.TenantID, cfg.CorrelationID, cfg.ReturnFileBucket, cfg.ReturnFileKey),
	})

	// Look up the original batch file by correlation ID
	batchFile, err := deps.Stage7.BatchFiles.GetByCorrelationID(ctx, cfg.CorrelationID)
	if err != nil {
		return fmt.Errorf("reconcile: get batch file for correlation_id=%s: %w", cfg.CorrelationID, err)
	}

	// Read FIS return file from S3
	returnBody, err := deps.FileStore.GetObject(ctx, cfg.ReturnFileBucket, cfg.ReturnFileKey)
	if err != nil {
		return fmt.Errorf("reconcile: read return file %s/%s: %w", cfg.ReturnFileBucket, cfg.ReturnFileKey, err)
	}

	// Run Stage 6 — Reconciliation (was Stage 7 in single-container architecture)
	reconcResult, err := deps.Stage7.Run(ctx, batchFile, returnBody)
	if err != nil {
		return fmt.Errorf("reconcile: stage6_reconciliation: %w", err)
	}

	durationMs := time.Since(startTime).Milliseconds()
	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "reconcile.complete",
		Level:         "INFO",
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		BatchFileID:   batchFile.ID,
		Stage:         strPtr("pipeline"),
		DurationMs:    &durationMs,
		Completed:     &reconcResult.Completed,
		Failed:        &reconcResult.Failed,
		Total:         &reconcResult.Total,
		Message: fmt.Sprintf("reconcile complete: duration=%dms completed=%d failed=%d total=%d",
			durationMs, reconcResult.Completed, reconcResult.Failed, reconcResult.Total),
	})

	enrollmentRate := 0.0
	if reconcResult.Total > 0 {
		enrollmentRate = float64(reconcResult.Completed) / float64(reconcResult.Total) * 100
	}
	_ = obs.RecordMetric(ctx, observability.MetricEnrollmentSuccessRate, enrollmentRate, map[string]string{
		"tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})
	_ = obs.RecordMetric(ctx, observability.MetricPipelineDurationMs, float64(durationMs), map[string]string{
		"tenant_id": cfg.TenantID, "env": cfg.PipelineEnv,
	})

	return nil
}

// pipelineError emits pipeline.error then returns the error.
func pipelineError(ctx context.Context, obs ports.IObservabilityPort, cfg *PipelineConfig, batchFileID uuid.UUID, startTime time.Time, err error) error {
	durationMs := time.Since(startTime).Milliseconds()
	errStr := err.Error()
	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "pipeline.error",
		Level:         "ERROR",
		CorrelationID: cfg.CorrelationID,
		TenantID:      cfg.TenantID,
		BatchFileID:   batchFileID,
		Stage:         strPtr("pipeline"),
		DurationMs:    &durationMs,
		Error:         &errStr,
		Message:       fmt.Sprintf("pipeline failed after %dms", durationMs),
	})
	return err
}

// ─── Noop adapters for DEV / deferred features ───────────────────────────────

type noopMartWriter struct{}

func (n *noopMartWriter) UpsertMember(_ context.Context, _ *ports.MemberRecord) (int64, error)         { return 0, nil }
func (n *noopMartWriter) UpsertCard(_ context.Context, _ *ports.CardRecord) (int64, error)             { return 0, nil }
func (n *noopMartWriter) UpsertPurse(_ context.Context, _ *ports.PurseRecord) (int64, error)           { return 0, nil }
func (n *noopMartWriter) WriteEnrollmentFact(_ context.Context, _ *ports.EnrollmentFact) error         { return nil }
func (n *noopMartWriter) WritePurseLifecycleFact(_ context.Context, _ *ports.PurseLifecycleFact) error { return nil }
func (n *noopMartWriter) WriteReconciliationFact(_ context.Context, _ *ports.ReconciliationFact) error { return nil }

// ─── Config helpers ───────────────────────────────────────────────────────────

func getSecretString(ctx context.Context, sm *secretsmanager.Client, arn string) (string, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &arn})
	if err != nil {
		return "", fmt.Errorf("GetSecretValue(%s): %w", arn, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("GetSecretValue(%s): nil SecretString", arn)
	}
	return *out.SecretString, nil
}

func parseConfig() (*PipelineConfig, error) {
	mode                  := flag.String("mode",                          envOrDefault("TASK_MODE", "ingest"),       "ingest|reconcile")
	corrIDStr             := flag.String("correlation-id",                os.Getenv("CORRELATION_ID"),               "correlation UUID")
	tenantID              := flag.String("tenant-id",                     os.Getenv("TENANT_ID"),                    "health plan tenant ID")
	clientID              := flag.String("client-id",                     os.Getenv("CLIENT_ID"),                    "Morse client code")
	s3Bucket              := flag.String("s3-bucket",                     os.Getenv("S3_BUCKET"),                    "inbound S3 bucket")
	s3Key                 := flag.String("s3-key",                        os.Getenv("S3_KEY"),                       "S3 object key")
	fileType              := flag.String("file-type",                     os.Getenv("FILE_TYPE"),                    "SRG310|SRG315|SRG320")
	region                := flag.String("region",                        envOrDefault("AWS_REGION", "us-east-1"),   "AWS region")
	pipelineEnv           := flag.String("env",                           envOrDefault("PIPELINE_ENV", "DEV"),       "DEV|TST|PRD")
	dbHost                := flag.String("db-host",                       os.Getenv("DB_HOST"),                      "Aurora RDS Proxy endpoint")
	dbName                := flag.String("db-name",                       os.Getenv("DB_NAME"),                      "database name")
	dbUser                := flag.String("db-user",                       os.Getenv("DB_USER"),                      "database user")
	dbPass                := flag.String("db-password",                   os.Getenv("DB_PASSWORD"),                  "database password")
	dbSSL                 := flag.String("db-ssl",                        envOrDefault("DB_SSL", "require"),         "sslmode")
	kmsKey                := flag.String("kms-key-arn",                   os.Getenv("KMS_KEY_ARN"),                  "KMS key ARN")
	stagedBucket          := flag.String("staged-bucket",                 os.Getenv("STAGED_BUCKET"),                "staged S3 bucket")
	scpBucket             := flag.String("scp-exchange-bucket",           os.Getenv("SCP_EXCHANGE_BUCKET"),          "scp-exchange S3 bucket")
	egressBucket          := flag.String("egress-bucket",                 os.Getenv("EGRESS_BUCKET"),                "egress S3 bucket for FIS pickup")
	scpCompanyID          := flag.String("scp-company-id",                os.Getenv("SCP_COMPANY_ID"),               "SCP company ID (8 chars)")
	pgpPrivateKeyARN      := flag.String("pgp-private-key-secret-arn",    os.Getenv("PGP_PRIVATE_KEY_SECRET_ARN"),   "ARN: Morse PGP private key")
	pgpPassphraseARN      := flag.String("pgp-passphrase-secret-arn",     os.Getenv("PGP_PASSPHRASE_SECRET_ARN"),    "ARN: PGP passphrase")
	pgpSCPPublicKeyARN    := flag.String("pgp-scp-public-key-secret-arn", os.Getenv("PGP_SCP_PUBLIC_KEY_SECRET_ARN"),"ARN: SCP PGP public key")
	returnFileBucket      := flag.String("return-file-bucket",            os.Getenv("RETURN_FILE_BUCKET"),           "S3 bucket for FIS return file (reconcile mode)")
	returnFileKey         := flag.String("return-file-key",               os.Getenv("RETURN_FILE_KEY"),              "S3 key for FIS return file (reconcile mode)")
	replay                := flag.Bool("replay", false, "replay mode")
	replaySeq             := flag.Int("replay-seq", 0, "row sequence for replay")
	flag.Parse()

	// Derive TENANT_ID and CLIENT_ID from S3 key path when not explicitly set
	if *tenantID == "" && *s3Key != "" {
		parts := strings.SplitN(*s3Key, "/", 4)
		if len(parts) >= 3 {
			*tenantID = parts[1]
		}
	}
	if *clientID == "" && *s3Key != "" {
		parts := strings.SplitN(*s3Key, "/", 4)
		if len(parts) >= 3 {
			*clientID = parts[2]
		}
	}

	// Derive FILE_TYPE from S3 key when not explicitly set
	if *fileType == "" && *s3Key != "" {
		lower := strings.ToLower(*s3Key)
		switch {
		case strings.Contains(lower, "srg315"):
			*fileType = "SRG315"
		case strings.Contains(lower, "srg320"):
			*fileType = "SRG320"
		default:
			*fileType = "SRG310"
		}
	}

	// Generate CORRELATION_ID deterministically from (s3Bucket + s3Key) when not set
	if *corrIDStr == "" {
		if *s3Key == "" {
			return nil, fmt.Errorf("--correlation-id is required when --s3-key is not set")
		}
		seed := *s3Bucket + "/" + *s3Key
		*corrIDStr = uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
	}
	id, err := uuid.Parse(*corrIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid correlation-id: %w", err)
	}

	cfg := &PipelineConfig{
		Mode:                     *mode,
		CorrelationID:            id,
		TenantID:                 *tenantID,
		ClientID:                 *clientID,
		S3Bucket:                 *s3Bucket,
		S3Key:                    *s3Key,
		FileType:                 *fileType,
		Region:                   *region,
		PipelineEnv:              *pipelineEnv,
		DBHost:                   *dbHost,
		DBName:                   *dbName,
		DBUser:                   *dbUser,
		DBPassword:               *dbPass,
		DBSSLMode:                *dbSSL,
		KMSKeyARN:                *kmsKey,
		StagedBucket:             *stagedBucket,
		SCPExchangeBucket:        *scpBucket,
		EgressBucket:             *egressBucket,
		SCPCompanyID:             *scpCompanyID,
		PGPPrivateKeySecretARN:   *pgpPrivateKeyARN,
		PGPPassphraseSecretARN:   *pgpPassphraseARN,
		PGPSCPPublicKeySecretARN: *pgpSCPPublicKeyARN,
		ReturnFileBucket:         *returnFileBucket,
		ReturnFileKey:            *returnFileKey,
		ReplayMode:               *replay,
	}
	if *replay && *replaySeq > 0 {
		seq := *replaySeq
		cfg.ReplayRowSequence = &seq
	}
	return cfg, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func strPtr(s string) *string { return &s }
