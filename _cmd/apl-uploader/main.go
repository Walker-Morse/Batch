// Command apl-uploader generates and uploads the Approved Products List to FIS.
//
// Triggered by EventBridge Scheduler on the first business day of each month (§B.5).
// Also runnable manually for out-of-cycle APL updates (e.g. new product categories).
//
// Pipeline:
//  1. Load current active APL version for (program_id, benefit_type)
//  2. Generate fixed-width FIS APL batch file from apl_rules
//     *** BLOCKED: FIS APL File Processing Spec not yet received (Open Item #11) ***
//  3. PGP-encrypt with FIS public key from Secrets Manager
//  4. Delete plaintext after encryption
//  5. Write encrypted file to fis-exchange S3 bucket
//  6. Deliver to FIS via SFTP (skipped in dry-run mode)
//  7. Write immutable apl_versions row (skipped in dry-run mode)
//  8. Atomically update apl_rules.active_version_id (skipped in dry-run mode)
//
// RFU Phase 1: two APLs loaded monthly (BRD 3/6/2026):
//   RFUORFVM  — benefit_type=OTC, category 5D only
//   RFUORPANM — benefit_type=FOD, categories 5D + 5K
//
// Usage:
//   apl-uploader --tenant-id rfu-oregon --program-id <uuid> --benefit-type OTC [--dry-run]
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/google/uuid"
	"github.com/walker-morse/batch/_adapters/aurora"
	"github.com/walker-morse/batch/_adapters/pgp"
	"github.com/walker-morse/batch/_adapters/transport"
	"github.com/walker-morse/batch/approved_products/versioning"
)

// ErrAPLSpecNotImplemented is returned when the FIS APL file format spec has not
// yet been received. Exit code 2 — blocked, not a runtime error.
// Resolve by implementing generateAPLFile() once Open Item #11 is unblocked.
var ErrAPLSpecNotImplemented = errors.New(
	"APL file generation not yet implemented: FIS APL File Processing Spec required " +
		"(Open Item #11 — contact Kendra Williams to obtain spec from FIS EBT team)",
)

type uploaderConfig struct {
	TenantID    string
	ProgramID   uuid.UUID
	BenefitType string // OTC|FOD|CMB
	DryRun      bool
	Env         string // DEV|TST|PRD
	Region      string

	DBHost    string
	DBName    string
	DBUser    string
	DBPassword string
	DBSSLMode string

	FISExchangeBucket string
	APLKeyPrefix      string // e.g. "apl/"

	PGPFISPublicKeyARN   string
	FISSFTPHostSecretARN string
	FISSFTPUserSecretARN string
	FISSFTPKeySecretARN  string
}

func main() {
	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}

	ctx := context.Background()
	if err := run(ctx, cfg); err != nil {
		if errors.Is(err, ErrAPLSpecNotImplemented) {
			log.Printf("BLOCKED: %v", err)
			os.Exit(2)
		}
		log.Printf("apl-uploader: fatal: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *uploaderConfig) error {
	log.Printf("apl-uploader: start tenant=%s program=%s benefit_type=%s env=%s dry_run=%v",
		cfg.TenantID, cfg.ProgramID, cfg.BenefitType, cfg.Env, cfg.DryRun)

	// ── Step 1: Aurora connection ──────────────────────────────────────────────
	pool, err := aurora.NewPool(ctx, aurora.Config{
		Host:     cfg.DBHost,
		Database: cfg.DBName,
		User:     cfg.DBUser,
		Password: cfg.DBPassword,
		SSLMode:  cfg.DBSSLMode,
	})
	if err != nil {
		return fmt.Errorf("connect to aurora: %w", err)
	}
	defer pool.Close()

	aplRepo := aurora.NewAPLRepo(pool)

	// ── Step 2: Check current active version ──────────────────────────────────
	currentVersion, err := aplRepo.GetActiveVersion(ctx, cfg.ProgramID, cfg.BenefitType)
	switch {
	case errors.Is(err, versioning.ErrNoActiveVersion):
		log.Printf("apl-uploader: no active version found — this will be version 1")
	case err != nil:
		return fmt.Errorf("get active APL version: %w", err)
	default:
		log.Printf("apl-uploader: current active version=%d s3_key=%s",
			currentVersion.VersionNumber, currentVersion.S3Key)
	}

	// ── Step 3: Generate APL file ──────────────────────────────────────────────
	// BLOCKED — returns ErrAPLSpecNotImplemented until Open Item #11 resolved.
	plaintext, ruleCount, err := generateAPLFile(ctx, cfg)
	if err != nil {
		return err
	}
	log.Printf("apl-uploader: generated APL: %d rules, %d bytes", ruleCount, len(plaintext))

	// ── Step 4: PGP encrypt ────────────────────────────────────────────────────
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return fmt.Errorf("load AWS config: %w", err)
	}
	smClient := secretsmanager.NewFromConfig(awsCfg)

	encrypter, err := pgp.LoadEncrypter(ctx, smClient, cfg.PGPFISPublicKeyARN)
	if err != nil {
		return fmt.Errorf("load PGP encrypter: %w", err)
	}
	ciphertext, err := encrypter.Encrypt(bytes.NewReader(plaintext))
	if err != nil {
		return fmt.Errorf("pgp encrypt: %w", err)
	}

	// ── Step 5: Wipe plaintext ─────────────────────────────────────────────────
	for i := range plaintext {
		plaintext[i] = 0
	}
	plaintext = nil

	var encBuf bytes.Buffer
	if _, err := encBuf.ReadFrom(ciphertext); err != nil {
		return fmt.Errorf("buffer ciphertext: %w", err)
	}
	encBytes := encBuf.Bytes()
	log.Printf("apl-uploader: encrypted: %d bytes", len(encBytes))

	// ── Step 6: Write to S3 ────────────────────────────────────────────────────
	s3Client := awss3.NewFromConfig(awsCfg)
	s3Key := aplS3Key(cfg)
	if _, err = s3Client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(cfg.FISExchangeBucket),
		Key:    aws.String(s3Key),
		Body:   bytes.NewReader(encBytes),
	}); err != nil {
		return fmt.Errorf("s3 put apl file (key=%s): %w", s3Key, err)
	}
	log.Printf("apl-uploader: written s3://%s/%s", cfg.FISExchangeBucket, s3Key)

	if cfg.DryRun {
		log.Printf("apl-uploader: dry-run complete — SFTP delivery and DB writes skipped")
		return nil
	}

	// ── Step 7: Deliver to FIS via SFTP ───────────────────────────────────────
	sftpHost, err := getSecretString(ctx, smClient, cfg.FISSFTPHostSecretARN)
	if err != nil {
		return fmt.Errorf("load FIS SFTP host: %w", err)
	}
	sftpUser, err := getSecretString(ctx, smClient, cfg.FISSFTPUserSecretARN)
	if err != nil {
		return fmt.Errorf("load FIS SFTP user: %w", err)
	}
	sftpKey, err := getSecretString(ctx, smClient, cfg.FISSFTPKeySecretARN)
	if err != nil {
		return fmt.Errorf("load FIS SFTP private key: %w", err)
	}

	fisTransport := &transport.FISTransportAdapter{
		SFTPHost:       sftpHost,
		SFTPUser:       sftpUser,
		SFTPPrivateKey: []byte(sftpKey),
	}
	filename := aplFilename(cfg)
	if err := fisTransport.Deliver(ctx, bytes.NewReader(encBytes), filename); err != nil {
		return fmt.Errorf("deliver APL to FIS SFTP (file=%s): %w", filename, err)
	}
	log.Printf("apl-uploader: delivered to FIS: %s", filename)

	// ── Step 8: Write immutable apl_versions row ───────────────────────────────
	newVersionID := uuid.New()
	newVersion := &versioning.APLVersion{
		ID:          newVersionID,
		ProgramID:   cfg.ProgramID,
		BenefitType: cfg.BenefitType,
		RuleCount:   ruleCount,
		S3Key:       s3Key,
		UploadedAt:  time.Now().UTC(),
		UploadedBy:  "apl-uploader",
	}
	if err := aplRepo.CreateVersion(ctx, newVersion); err != nil {
		return fmt.Errorf("write apl_versions row: %w", err)
	}
	log.Printf("apl-uploader: wrote apl_versions id=%s version=%d",
		newVersionID, newVersion.VersionNumber)

	// ── Step 9: Atomically activate new version ────────────────────────────────
	if err := aplRepo.ActivateVersion(ctx, cfg.ProgramID, cfg.BenefitType, newVersionID); err != nil {
		// Partial success: file delivered to FIS but DB pointer not updated.
		// Log the recovery SQL so an operator can fix without another upload.
		return fmt.Errorf(
			"CRITICAL: APL delivered to FIS (version=%d s3=%s) but ActivateVersion failed: %w — "+
				"recovery SQL: UPDATE apl_rules SET active_version_id='%s', updated_at=NOW() "+
				"WHERE program_id='%s' AND benefit_type='%s'",
			newVersion.VersionNumber, s3Key, err,
			newVersionID, cfg.ProgramID, cfg.BenefitType,
		)
	}
	log.Printf("apl-uploader: done — version=%d active for program=%s benefit_type=%s",
		newVersion.VersionNumber, cfg.ProgramID, cfg.BenefitType)
	return nil
}

// generateAPLFile reads apl_rules from Aurora and builds the FIS APL batch file.
//
// STUB — NOT IMPLEMENTED (Open Item #11).
//
// Blocked on receipt of the FIS Approved Products List File Processing Spec.
// The APL file format is distinct from the card management batch file (RT30/RT60);
// it defines per-product eligibility using MCC groups, merchant IDs, and UPC codes.
//
// To implement:
//  1. Obtain FIS APL File Processing Spec from Kendra Williams / FIS EBT team
//  2. Add APL record builder to fis_reconciliation/fis_adapter/record_builder.go
//  3. Query apl_rules for (program_id, benefit_type): mcc_group, iias_group, restriction_levels
//  4. Build fixed-width file and return (plaintext, ruleCount, nil)
//  5. Remove ErrAPLSpecNotImplemented
func generateAPLFile(_ context.Context, _ *uploaderConfig) ([]byte, int, error) {
	return nil, 0, ErrAPLSpecNotImplemented
}

// aplS3Key builds the S3 object key for the APL file.
// Format: {prefix}{tenant_id}/{benefit_type}/YYYYMMDD_{program_id}.apl.pgp
func aplS3Key(cfg *uploaderConfig) string {
	date := time.Now().UTC().Format("20060102")
	return fmt.Sprintf("%s%s/%s/%s_%s.apl.pgp",
		cfg.APLKeyPrefix,
		cfg.TenantID,
		strings.ToLower(cfg.BenefitType),
		date,
		cfg.ProgramID,
	)
}

// aplFilename builds the SFTP delivery filename for FIS.
// Format: {TENANT}_{BENEFITTYPE}_{YYYYMMDD}.apl.pgp
func aplFilename(cfg *uploaderConfig) string {
	date := time.Now().UTC().Format("20060102")
	return fmt.Sprintf("%s_%s_%s.apl.pgp",
		strings.ToUpper(cfg.TenantID),
		strings.ToUpper(cfg.BenefitType),
		date,
	)
}

// ── Config parsing ─────────────────────────────────────────────────────────────

func parseConfig() (*uploaderConfig, error) {
	tenantID     := flag.String("tenant-id",     os.Getenv("TENANT_ID"),     "health plan tenant ID (required)")
	programIDStr := flag.String("program-id",    os.Getenv("PROGRAM_ID"),    "program UUID (required)")
	benefitType  := flag.String("benefit-type",  os.Getenv("BENEFIT_TYPE"),  "OTC|FOD|CMB (required)")
	dryRun       := flag.Bool("dry-run",         false,                      "generate + encrypt + S3; skip SFTP and DB writes")
	env          := flag.String("env",           envOrDefault("PIPELINE_ENV", "DEV"), "DEV|TST|PRD")
	region       := flag.String("region",        envOrDefault("AWS_REGION", "us-east-1"), "AWS region")

	dbHost  := flag.String("db-host",     os.Getenv("DB_HOST"),     "Aurora RDS Proxy endpoint")
	dbName  := flag.String("db-name",     os.Getenv("DB_NAME"),     "database name")
	dbUser  := flag.String("db-user",     os.Getenv("DB_USER"),     "database user")
	dbPass  := flag.String("db-password", os.Getenv("DB_PASSWORD"), "database password")
	dbSSL   := flag.String("db-ssl",      envOrDefault("DB_SSL", "require"), "sslmode")

	fisBucket := flag.String("fis-exchange-bucket",      os.Getenv("FIS_EXCHANGE_BUCKET"),      "fis-exchange S3 bucket")
	aplPrefix := flag.String("apl-key-prefix",           envOrDefault("APL_KEY_PREFIX", "apl/"), "S3 key prefix for APL files")

	pgpKey  := flag.String("pgp-fis-public-key-secret-arn", os.Getenv("PGP_FIS_PUBLIC_KEY_SECRET_ARN"), "ARN: FIS PGP public key")
	sftpH   := flag.String("fis-sftp-host-secret-arn",      os.Getenv("FIS_SFTP_HOST_SECRET_ARN"),      "ARN: FIS SFTP host")
	sftpU   := flag.String("fis-sftp-user-secret-arn",      os.Getenv("FIS_SFTP_USER_SECRET_ARN"),      "ARN: FIS SFTP user")
	sftpK   := flag.String("fis-sftp-key-secret-arn",       os.Getenv("FIS_SFTP_KEY_SECRET_ARN"),       "ARN: FIS SSH private key PEM")

	flag.Parse()

	if *tenantID == "" {
		return nil, fmt.Errorf("--tenant-id is required")
	}
	if *programIDStr == "" {
		return nil, fmt.Errorf("--program-id is required")
	}
	programID, err := uuid.Parse(*programIDStr)
	if err != nil {
		return nil, fmt.Errorf("invalid --program-id %q: %w", *programIDStr, err)
	}
	*benefitType = strings.ToUpper(*benefitType)
	switch *benefitType {
	case "OTC", "FOD", "CMB":
	case "":
		return nil, fmt.Errorf("--benefit-type is required")
	default:
		return nil, fmt.Errorf("--benefit-type must be OTC, FOD, or CMB; got %q", *benefitType)
	}

	return &uploaderConfig{
		TenantID:             *tenantID,
		ProgramID:            programID,
		BenefitType:          *benefitType,
		DryRun:               *dryRun,
		Env:                  strings.ToUpper(*env),
		Region:               *region,
		DBHost:               *dbHost,
		DBName:               *dbName,
		DBUser:               *dbUser,
		DBPassword:           *dbPass,
		DBSSLMode:            *dbSSL,
		FISExchangeBucket:    *fisBucket,
		APLKeyPrefix:         *aplPrefix,
		PGPFISPublicKeyARN:   *pgpKey,
		FISSFTPHostSecretARN: *sftpH,
		FISSFTPUserSecretARN: *sftpU,
		FISSFTPKeySecretARN:  *sftpK,
	}, nil
}

func getSecretString(ctx context.Context, sm *secretsmanager.Client, arn string) (string, error) {
	out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(arn)})
	if err != nil {
		return "", fmt.Errorf("GetSecretValue(%s): %w", arn, err)
	}
	if out.SecretString == nil {
		return "", fmt.Errorf("GetSecretValue(%s): SecretString is nil", arn)
	}
	return *out.SecretString, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
