// Command xtract-loader ingests FIS Data XTRACT feeds from S3 into the
// reporting data mart (§4.3.13–4.3.20).
//
// Triggered by EventBridge S3 ObjectCreated on the xtract bucket.
// Environment variables injected by EventBridge:
//   S3_BUCKET  — xtract bucket name
//   S3_KEY     — object key of the arriving XTRACT file
//   TENANT_ID  — optional override; inferred from S3 key prefix otherwise
//
// Non-repudiation sequence:
//   1. GetObject from S3 — read fully into memory, compute SHA-256
//   2. Write xtract_file_log row (RECEIVED + SHA-256) BEFORE any parsing
//   3. Parse + dispatch to feed-specific loader
//   4. Update xtract_file_log to LOADED or FAILED
//
// No temp files — runs safely in a scratch container with no writable FS.
// All file content is held in a bytes.Reader after SHA-256 computation.
// Largest expected file (STDFSID) is ~10MB — well within Fargate 1GB RAM.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/walker-morse/batch/xtract_loader/feeds/ccx"
	"github.com/walker-morse/batch/xtract_loader/feeds/stdacctbal"
	"github.com/walker-morse/batch/xtract_loader/feeds/stdauth"
	"github.com/walker-morse/batch/xtract_loader/feeds/stdfsid"
	"github.com/walker-morse/batch/xtract_loader/feeds/stdmon"
	"github.com/walker-morse/batch/xtract_loader/feeds/stdnonmon"
	"github.com/walker-morse/batch/xtract_loader/filelogger"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(ctx, log); err != nil {
		log.Error("xtract-loader fatal", "error", err)
		os.Exit(1)
	}
	log.Info("xtract-loader complete")
}

func run(ctx context.Context, log *slog.Logger) error {
	bucket   := mustEnv("S3_BUCKET")
	s3Key    := mustEnv("S3_KEY")
	tenantID := envOr("TENANT_ID", inferTenantID(s3Key))

	dbHost := mustEnv("DB_HOST")
	dbName := mustEnv("DB_NAME")
	dbUser := mustEnv("DB_USER")
	dbPass := mustEnv("DB_PASSWORD")

	log.Info("xtract-loader start", "bucket", bucket, "key", s3Key, "tenant", tenantID)

	// ── DB pool ───────────────────────────────────────────────────────────
	dsn := fmt.Sprintf("host=%s dbname=%s user=%s password=%s sslmode=require",
		dbHost, dbName, dbUser, dbPass)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()

	// ── S3 client ─────────────────────────────────────────────────────────
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	s3Client := awss3.NewFromConfig(cfg)

	// ── Step 1: Read S3 object fully into memory, compute SHA-256 ─────────
	// No temp files — scratch container has no writable filesystem.
	// Largest XTRACT file (STDFSID) is ~10MB; Fargate has 1GB RAM.
	obj, err := s3Client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: &bucket,
		Key:    &s3Key,
	})
	if err != nil {
		return fmt.Errorf("s3 GetObject s3://%s/%s: %w", bucket, s3Key, err)
	}
	defer obj.Body.Close()

	h := sha256.New()
	rawBytes, err := io.ReadAll(io.TeeReader(obj.Body, h))
	if err != nil {
		return fmt.Errorf("read s3 body: %w", err)
	}
	sha256Hex := hex.EncodeToString(h.Sum(nil))
	log.Info("file received", "key", s3Key, "bytes", len(rawBytes), "sha256", sha256Hex[:16]+"...")

	// ── Step 2: Write xtract_file_log (non-repudiation) ───────────────────
	fl   := filelogger.New(pool)
	meta := filelogger.ParseFilename(s3Key)

	sk, err := fl.CreateReceived(ctx,
		s3Key, meta.XtractType, meta.ClientName,
		nil, meta.IsRerun, meta.IsInitialLoad, meta.SourceSubtype,
		sha256Hex, tenantID,
	)
	if err != nil {
		return fmt.Errorf("filelogger CreateReceived: %w", err)
	}
	if sk == 0 {
		log.Info("file already processed — skipping", "key", s3Key)
		return nil
	}
	log.Info("file_log row created", "file_log_sk", sk, "xtract_type", meta.XtractType)

	// ── Step 3: Parse and load ─────────────────────────────────────────────
	result, loadErr := dispatch(ctx, log, pool, meta.XtractType, s3Key, tenantID, rawBytes)

	// ── Step 4: Update file log ────────────────────────────────────────────
	if loadErr != nil {
		_ = fl.SetFailed(ctx, sk, loadErr.Error())
		return fmt.Errorf("dispatch %s: %w", meta.XtractType, loadErr)
	}

	hfd := result.Header.FileDate
	wod := result.Header.WorkOfDate
	_ = fl.SetProcessing(ctx, sk, &hfd, &wod, result.DetailCount)
	if err := fl.SetLoaded(ctx, sk, result.DetailCount, result.TrailerCount, result.CountMismatch); err != nil {
		return err
	}

	log.Info("file loaded",
		"key", s3Key,
		"xtract_type", meta.XtractType,
		"detail_rows", result.DetailCount,
		"trailer_count", result.TrailerCount,
		"count_match", !result.CountMismatch,
	)
	return nil
}

// dispatch routes to the correct feed loader.
// rawBytes is held in memory — caller passes a fresh bytes.Reader per call.
func dispatch(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool,
	xtractType, s3Key, tenantID string, rawBytes []byte) (*parser.ParseResult, error) {

	newReader := func() *bytes.Reader { return bytes.NewReader(rawBytes) }

	switch xtractType {
	case "STDMON":
		loader := stdmon.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, newReader(), s3Key, stdmon.FeedName, loader.ProcessRow)

	case "STDAUTH":
		loader := stdauth.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, newReader(), s3Key, stdauth.FeedName, loader.ProcessRow)

	case "STDACCTBAL":
		loader := stdacctbal.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, newReader(), s3Key, stdacctbal.FeedName, loader.ProcessRow)

	case "STDNONMON":
		loader := stdnonmon.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, newReader(), s3Key, stdnonmon.FeedName, loader.ProcessRow)

	case "STDFSID":
		// STDFSID needs workOfDate from H record. Since rawBytes is in memory,
		// do a lightweight first pass (noop rows) to get the header, then a
		// second pass with the real loader. No seeking required.
		firstResult, err := parser.Parse(ctx, newReader(), s3Key, stdfsid.FeedName, noopRow)
		if err != nil {
			return nil, fmt.Errorf("stdfsid header pass: %w", err)
		}
		loader := stdfsid.NewLoader(pool, tenantID, s3Key, firstResult.Header.WorkOfDate)
		return parser.Parse(ctx, newReader(), s3Key, stdfsid.FeedName, loader.ProcessRow)

	case "CCX":
		loader := ccx.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, newReader(), s3Key, ccx.FeedName, loader.ProcessRow)

	case "CLIENTHIERARCHY":
		log.Info("CLIENTHIERARCHY loader not yet implemented — file logged", "key", s3Key)
		return parser.Parse(ctx, newReader(), s3Key, "", noopRow)

	default:
		log.Warn("unknown XTRACT type — file logged for non-repudiation only", "type", xtractType, "key", s3Key)
		return parser.Parse(ctx, newReader(), s3Key, "", noopRow)
	}
}

func noopRow(_ context.Context, _ int, _ []string) error { return nil }

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "xtract-loader: required env var %s not set\n", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func inferTenantID(s3Key string) string {
	parts := strings.SplitN(s3Key, "/", 2)
	if len(parts) >= 2 && parts[0] != "" {
		return parts[0]
	}
	return "unknown"
}
