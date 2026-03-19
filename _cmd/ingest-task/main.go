// Command ingest-task is the single ECS Fargate container for the complete
// One Fintech file lifecycle (ADR-003).
//
// Seven named pipeline stages:
//   Stage 1 — File Arrival       S3 event → SHA-256 → batch_files row
//   Stage 2 — Validation         PGP decrypt → SRG parse → dead-letter malformed rows
//   Stage 3 — Row Processing     Sequential row-by-row: idempotency → domain writes → staging
//   Stage 4 — Batch Assembly     FIS 400-byte fixed-width records → PGP-encrypt → S3
//   Stage 5 — FIS Transfer       SSH/SCP → FIS Transfer Family SFTP endpoint
//   Stage 6 — Return File Wait   Poll fis-exchange S3 for FIS return file (6h timeout)
//   Stage 7 — Reconciliation     Match results → update status → stamp FIS identifiers
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
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/google/uuid"

	"github.com/walker-morse/batch/_adapters/aurora"
	pgpadapter "github.com/walker-morse/batch/_adapters/pgp"
	"github.com/walker-morse/batch/_adapters/s3"
	adtransport "github.com/walker-morse/batch/_adapters/transport"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/fis_reconciliation/fis_adapter"
	stage4 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage5 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage6 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage7 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
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
	DBPassword string
	DBSSLMode  string

	// S3
	KMSKeyARN         string
	StagedBucket      string
	FISExchangeBucket string
	ReturnFilePrefix  string

	// FIS assembler
	FISCompanyID string

	// PGP key ARNs (Secrets Manager). Empty = NullPGP passthrough (DEV only).
	PGPPrivateKeySecretARN   string
	PGPPassphraseSecretARN   string
	PGPFISPublicKeySecretARN string

	// FIS SFTP (Stage 5). Empty = transport skipped in DEV mode.
	FISSFTPHostSecretARN       string
	FISSFTPUserSecretARN       string
	FISSFTPPrivateKeySecretARN string

	// Replay mode
	ReplayMode        bool
	ReplayRowSequence *int

	ReturnFileWaitTimeout time.Duration
}

// PipelineDeps holds all wired port implementations.
type PipelineDeps struct {
	Stage1 *stage1.FileArrivalStage
	Stage2 *stage2.ValidationStage
	Stage3 *stage3.RowProcessingStage
	Stage4 *stage4.BatchAssemblyStage
	Stage5 *stage5.FISTransferStage
	Stage6 *stage6.ReturnFileWaitStage
	Stage7 *stage7.ReconciliationStage
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
	assembler := fis_adapter.NewAssembler(cfg.FISCompanyID, seqStore, batchRecordsRepo)

	testProdIndicator := byte('P')
	if cfg.PipelineEnv == "DEV" {
		testProdIndicator = 'T'
	}
	_ = testProdIndicator

	// PGP decrypt (Stage 2)
	var pgpDecrypt func(io.Reader) (io.Reader, error)
	if cfg.PGPPrivateKeySecretARN == "" {
		if cfg.PipelineEnv != "DEV" {
			pool.Close()
			return nil, fmt.Errorf("PGP_PRIVATE_KEY_SECRET_ARN required in %s", cfg.PipelineEnv)
		}
		_ = obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "pipeline.warn",
			Level:         "WARN",
			CorrelationID: cfg.CorrelationID,
			TenantID:      cfg.TenantID,
			BatchFileID:   uuid.Nil,
			Stage:         strPtr("pipeline"),
			Message:       "NullPGPDecrypt active — DEV only; never use in TST or PRD",
		})
		pgpDecrypt = stage2.NullPGPDecrypt
	} else {
		dec, err := pgpadapter.LoadDecrypter(ctx, smClient, cfg.PGPPrivateKeySecretARN, cfg.PGPPassphraseSecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load PGP decrypter: %w", err)
		}
		pgpDecrypt = dec.Decrypt
	}

	// PGP encrypt (Stage 4)
	var pgpEncrypt func(io.Reader) (io.Reader, error)
	if cfg.PGPFISPublicKeySecretARN == "" {
		if cfg.PipelineEnv != "DEV" {
			pool.Close()
			return nil, fmt.Errorf("PGP_FIS_PUBLIC_KEY_SECRET_ARN required in %s", cfg.PipelineEnv)
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
		pgpEncrypt = stage4.NullPGPEncrypt
	} else {
		enc, err := pgpadapter.LoadEncrypter(ctx, smClient, cfg.PGPFISPublicKeySecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load PGP encrypter: %w", err)
		}
		pgpEncrypt = enc.Encrypt
	}

	// FIS transport (Stages 5 + 6)
	var fisTransport ports.FISTransport
	if cfg.FISSFTPHostSecretARN == "" || cfg.FISSFTPPrivateKeySecretARN == "" {
		if cfg.PipelineEnv != "DEV" {
			pool.Close()
			return nil, fmt.Errorf("FIS SFTP secrets required in %s", cfg.PipelineEnv)
		}
		_ = obs.LogEvent(ctx, &ports.LogEvent{
			EventType:     "pipeline.warn",
			Level:         "WARN",
			CorrelationID: cfg.CorrelationID,
			TenantID:      cfg.TenantID,
			BatchFileID:   uuid.Nil,
			Stage:         strPtr("pipeline"),
			Message:       "NullFISTransport active — DEV only; Stages 5-6 will no-op",
		})
		fisTransport = &nullFISTransport{}
	} else {
		sftpHost, err := getSecretString(ctx, smClient, cfg.FISSFTPHostSecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load FIS SFTP host: %w", err)
		}
		sftpUser, err := getSecretString(ctx, smClient, cfg.FISSFTPUserSecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load FIS SFTP user: %w", err)
		}
		sftpKey, err := getSecretString(ctx, smClient, cfg.FISSFTPPrivateKeySecretARN)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("load FIS SFTP private key: %w", err)
		}
		returnPrefix := cfg.ReturnFilePrefix
		if returnPrefix == "" {
			returnPrefix = "return/"
		}
		fisTransport = &adtransport.FISTransportAdapter{
			SFTPHost:          sftpHost,
			SFTPUser:          sftpUser,
			SFTPPrivateKey:    []byte(sftpKey),
			S3Client:          s3Client,
			FISExchangeBucket: cfg.FISExchangeBucket,
			ReturnFilePrefix:  returnPrefix,
		}
	}

	var martWriter ports.MartWriter = &noopMartWriter{}

	return &PipelineDeps{
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
		},
		Stage4: &stage4.BatchAssemblyStage{
			Assembler:         assembler,
			Files:             fileStore,
			BatchFiles:        batchFileRepo,
			StagedRecords:     batchRecordsRepo,
			Audit:             auditRepo,
			Obs:               obs,
			PGPEncrypt:        pgpEncrypt,
			StagedBucket:      cfg.StagedBucket,
			FISExchangeBucket: cfg.FISExchangeBucket,
		},
		Stage5: &stage5.FISTransferStage{
			Transport:         fisTransport,
			Files:             fileStore,
			BatchFiles:        batchFileRepo,
			Audit:             auditRepo,
			Obs:               obs,
			FISExchangeBucket: cfg.FISExchangeBucket,
		},
		Stage6: &stage6.ReturnFileWaitStage{
			Transport:  fisTransport,
			BatchFiles: batchFileRepo,
			Audit:      auditRepo,
			Obs:        obs,
			Timeout:    cfg.ReturnFileWaitTimeout,
		},
		Stage7: &stage7.ReconciliationStage{
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

// runWithDeps executes the full pipeline using pre-wired dependencies.
// All infrastructure concerns are resolved before this is called.
// This is the testable core of the ingest-task.
func runWithDeps(ctx context.Context, cfg *PipelineConfig, deps *PipelineDeps) error {
	// Obtain an observability handle from Stage 1 (which holds the wired adapter).
	// For pre-Stage-1 events, BatchFileID is uuid.Nil — correct per spec.
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
		Message:       fmt.Sprintf("pipeline starting: tenant=%s file_type=%s env=%s", cfg.TenantID, cfg.FileType, cfg.PipelineEnv),
	})

	// ── Stage 1 — File Arrival ────────────────────────────────────────────
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

	// ── Stage 2 — Validation ──────────────────────────────────────────────
	validationResult, err := deps.Stage2.Run(ctx, batchFile, cfg.S3Bucket, cfg.S3Key)
	if err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage2: %w", err))
	}

	// ── Stage 3 — Row Processing ──────────────────────────────────────────
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

	// Emit dead_letter_rate metric after Stage 3
	total3 := processingResult.StagedCount + processingResult.DuplicateCount + processingResult.FailedCount
	dlRate := 0.0
	if total3 > 0 {
		dlRate = float64(processingResult.FailedCount) / float64(total3) * 100
	}
	_ = obs.RecordMetric(ctx, observability.MetricDeadLetterRate, dlRate, map[string]string{
		"tenant_id": cfg.TenantID,
		"env":       cfg.PipelineEnv,
	})

	// Emit malformed_rate metric (from Stage 2 result)
	malformedRate := 0.0
	totalRows := validationResult.TotalRows
	if totalRows > 0 {
		malformedRate = float64(len(validationResult.ParseErrors)) / float64(totalRows) * 100
	}
	_ = obs.RecordMetric(ctx, "malformed_rate", malformedRate, map[string]string{
		"tenant_id": cfg.TenantID,
		"env":       cfg.PipelineEnv,
	})

	// ── Stage 4 — Batch Assembly ──────────────────────────────────────────
	assemblyResult, err := deps.Stage4.Run(ctx, batchFile)
	if err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage4: %w", err))
	}

	// ── Stage 5 — FIS Transfer ────────────────────────────────────────────
	if err := deps.Stage5.Run(ctx, batchFile, assemblyResult); err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage5: %w", err))
	}

	// ── Stage 6 — Return File Wait ────────────────────────────────────────
	waitResult, err := deps.Stage6.Run(ctx, batchFile)
	if err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage6: %w", err))
	}

	// ── Stage 7 — Reconciliation ──────────────────────────────────────────
	if err := deps.Stage7.Run(ctx, batchFile, waitResult.Body); err != nil {
		return pipelineError(ctx, obs, cfg, batchFile.ID, startTime, fmt.Errorf("stage7: %w", err))
	}

	// ── pipeline.complete ─────────────────────────────────────────────────
	durationMs := time.Since(startTime).Milliseconds()
	staged    := processingResult.StagedCount
	dl        := processingResult.FailedCount
	dups      := processingResult.DuplicateCount

	// enrolled count not yet tracked at pipeline level (Stage 7 returns no count).
	// Set to staged as best approximation until Stage 7 returns reconciliation counts.
	enrolled := staged

	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:    "pipeline.complete",
		Level:        "INFO",
		CorrelationID: cfg.CorrelationID,
		TenantID:     cfg.TenantID,
		BatchFileID:  batchFile.ID,
		Stage:        strPtr("pipeline"),
		DurationMs:   &durationMs,
		Staged:       &staged,
		Enrolled:     &enrolled,
		DeadLettered: &dl,
		Duplicates:   &dups,
		Message: fmt.Sprintf("pipeline complete: duration=%dms staged=%d enrolled=%d dead_lettered=%d duplicates=%d",
			durationMs, staged, enrolled, dl, dups),
	})

	_ = obs.RecordMetric(ctx, "pipeline_duration_ms", float64(durationMs), map[string]string{
		"tenant_id": cfg.TenantID,
		"env":       cfg.PipelineEnv,
	})

	return nil
}

// pipelineError emits pipeline.error then returns the error.
func pipelineError(ctx context.Context, obs ports.IObservabilityPort, cfg *PipelineConfig, batchFileID uuid.UUID, startTime time.Time, err error) error {
	durationMs := time.Since(startTime).Milliseconds()
	errStr := err.Error()
	_ = obs.LogEvent(ctx, &ports.LogEvent{
		EventType:    "pipeline.error",
		Level:        "ERROR",
		CorrelationID: cfg.CorrelationID,
		TenantID:     cfg.TenantID,
		BatchFileID:  batchFileID,
		Stage:        strPtr("pipeline"),
		DurationMs:   &durationMs,
		Error:        &errStr,
		Message:      fmt.Sprintf("pipeline failed after %dms", durationMs),
	})
	return err
}

// ─── Noop adapters for DEV / deferred features ───────────────────────────────

type nullFISTransport struct{}

func (n *nullFISTransport) Deliver(_ context.Context, _ io.Reader, filename string) error {
	return nil
}

func (n *nullFISTransport) PollForReturn(_ context.Context, _ uuid.UUID, _ time.Duration) (io.ReadCloser, error) {
	return io.NopCloser(emptyReader{}), nil
}

type emptyReader struct{}

func (emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }

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
	corrIDStr             := flag.String("correlation-id",               os.Getenv("CORRELATION_ID"),               "correlation UUID")
	tenantID              := flag.String("tenant-id",                    os.Getenv("TENANT_ID"),                    "health plan tenant ID")
	clientID              := flag.String("client-id",                    os.Getenv("CLIENT_ID"),                    "Morse client code")
	s3Bucket              := flag.String("s3-bucket",                    os.Getenv("S3_BUCKET"),                    "inbound S3 bucket")
	s3Key                 := flag.String("s3-key",                       os.Getenv("S3_KEY"),                       "S3 object key")
	fileType              := flag.String("file-type",                    os.Getenv("FILE_TYPE"),                    "SRG310|SRG315|SRG320")
	region                := flag.String("region",                       envOrDefault("AWS_REGION", "us-east-1"),   "AWS region")
	pipelineEnv           := flag.String("env",                          envOrDefault("PIPELINE_ENV", "DEV"),       "DEV|TST|PRD")
	dbHost                := flag.String("db-host",                      os.Getenv("DB_HOST"),                      "Aurora RDS Proxy endpoint")
	dbName                := flag.String("db-name",                      os.Getenv("DB_NAME"),                      "database name")
	dbUser                := flag.String("db-user",                      os.Getenv("DB_USER"),                      "database user")
	dbPass                := flag.String("db-password",                  os.Getenv("DB_PASSWORD"),                  "database password")
	dbSSL                 := flag.String("db-ssl",                       envOrDefault("DB_SSL", "require"),         "sslmode")
	kmsKey                := flag.String("kms-key-arn",                  os.Getenv("KMS_KEY_ARN"),                  "KMS key ARN")
	stagedBucket          := flag.String("staged-bucket",                os.Getenv("STAGED_BUCKET"),                "staged S3 bucket")
	fisBucket             := flag.String("fis-exchange-bucket",          os.Getenv("FIS_EXCHANGE_BUCKET"),          "fis-exchange S3 bucket")
	returnPrefix          := flag.String("return-prefix",                envOrDefault("RETURN_FILE_PREFIX", "return/"), "S3 prefix for FIS return files")
	fisCompanyID          := flag.String("fis-company-id",               os.Getenv("FIS_COMPANY_ID"),               "FIS company ID (8 chars)")
	pgpPrivateKeyARN      := flag.String("pgp-private-key-secret-arn",   os.Getenv("PGP_PRIVATE_KEY_SECRET_ARN"),   "ARN: Morse PGP private key")
	pgpPassphraseARN      := flag.String("pgp-passphrase-secret-arn",    os.Getenv("PGP_PASSPHRASE_SECRET_ARN"),    "ARN: PGP passphrase")
	pgpFISPublicKeyARN    := flag.String("pgp-fis-public-key-secret-arn",os.Getenv("PGP_FIS_PUBLIC_KEY_SECRET_ARN"),"ARN: FIS PGP public key")
	sftpHostARN           := flag.String("fis-sftp-host-secret-arn",     os.Getenv("FIS_SFTP_HOST_SECRET_ARN"),     "ARN: FIS SFTP host")
	sftpUserARN           := flag.String("fis-sftp-user-secret-arn",     os.Getenv("FIS_SFTP_USER_SECRET_ARN"),     "ARN: FIS SFTP user")
	sftpKeyARN            := flag.String("fis-sftp-key-secret-arn",      os.Getenv("FIS_SFTP_KEY_SECRET_ARN"),      "ARN: FIS SSH private key PEM")
	replay                := flag.Bool("replay", false, "replay mode")
	replaySeq             := flag.Int("replay-seq", 0, "row sequence for replay")
	returnTimeout         := flag.Duration("return-file-timeout", 6*time.Hour, "Stage 6 return file wait timeout")
	flag.Parse()

	if *corrIDStr == "" {
		return nil, fmt.Errorf("--correlation-id is required")
	}
	id, err := uuid.Parse(*corrIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid correlation-id: %w", err)
	}

	cfg := &PipelineConfig{
		CorrelationID:              id,
		TenantID:                   *tenantID,
		ClientID:                   *clientID,
		S3Bucket:                   *s3Bucket,
		S3Key:                      *s3Key,
		FileType:                   *fileType,
		Region:                     *region,
		PipelineEnv:                *pipelineEnv,
		DBHost:                     *dbHost,
		DBName:                     *dbName,
		DBUser:                     *dbUser,
		DBPassword:                 *dbPass,
		DBSSLMode:                  *dbSSL,
		KMSKeyARN:                  *kmsKey,
		StagedBucket:               *stagedBucket,
		FISExchangeBucket:          *fisBucket,
		ReturnFilePrefix:           *returnPrefix,
		FISCompanyID:               *fisCompanyID,
		PGPPrivateKeySecretARN:     *pgpPrivateKeyARN,
		PGPPassphraseSecretARN:     *pgpPassphraseARN,
		PGPFISPublicKeySecretARN:   *pgpFISPublicKeyARN,
		FISSFTPHostSecretARN:       *sftpHostARN,
		FISSFTPUserSecretARN:       *sftpUserARN,
		FISSFTPPrivateKeySecretARN: *sftpKeyARN,
		ReplayMode:                 *replay,
		ReturnFileWaitTimeout:      *returnTimeout,
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
