// Command xtract-loader ingests FIS Data XTRACT feeds from S3 into the
// reporting data mart (§4.3.13–4.3.20).
//
// Triggered by EventBridge S3 ObjectCreated on the xtract bucket.
// No temp files — runs in a scratch container. All file content held in
// a bytes.Reader after SHA-256 computation.
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

	dsn := fmt.Sprintf("host=%s dbname=%s user=%s password=%s sslmode=require",
		mustEnv("DB_HOST"), mustEnv("DB_NAME"), mustEnv("DB_USER"), mustEnv("DB_PASSWORD"))
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("db pool: %w", err)
	}
	defer pool.Close()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("aws config: %w", err)
	}
	s3Client := awss3.NewFromConfig(cfg)

	log.Info("xtract-loader start", "bucket", bucket, "key", s3Key, "tenant", tenantID)

	// ── Read into memory + SHA-256 ────────────────────────────────────────
	obj, err := s3Client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &bucket, Key: &s3Key})
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

	// ── Non-repudiation: write log row before any parsing ────────────────
	fl   := filelogger.New(pool)
	meta := filelogger.ParseFilename(s3Key)

	sk, err := fl.CreateReceived(ctx, s3Key, meta.XtractType, meta.ClientName,
		nil, meta.IsRerun, meta.IsInitialLoad, meta.SourceSubtype, sha256Hex, tenantID)
	if err != nil {
		return fmt.Errorf("filelogger CreateReceived: %w", err)
	}
	if sk == 0 {
		log.Info("file already processed — skipping", "key", s3Key)
		return nil
	}
	log.Info("file_log row created", "file_log_sk", sk, "xtract_type", meta.XtractType)

	// ── Phase 1: header-only parse to get workOfDate ──────────────────────
	newR := func() *bytes.Reader { return bytes.NewReader(rawBytes) }
	headerResult, err := parser.Parse(ctx, newR(), s3Key, "", noopRow)
	if err != nil {
		_ = fl.SetFailed(ctx, sk, err.Error())
		return fmt.Errorf("header parse %s: %w", meta.XtractType, err)
	}
	workOfDate := headerResult.Header.WorkOfDate
	if workOfDate.IsZero() {
		workOfDate = headerResult.Header.FileDate
	}

	// ── Phase 2: dispatch to feed loader with workOfDate ─────────────────
	result, loadErr := dispatch(ctx, log, pool, meta.XtractType, s3Key, tenantID, workOfDate, newR())

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

func dispatch(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool,
	xtractType, s3Key, tenantID string, workOfDate time.Time, r io.Reader) (*parser.ParseResult, error) {

	switch xtractType {
	case "STDMON":
		loader := stdmon.NewLoader(pool, tenantID, s3Key, workOfDate)
		return parser.Parse(ctx, r, s3Key, stdmon.FeedName, loader.ProcessRow)

	case "STDAUTH":
		loader := stdauth.NewLoader(pool, tenantID, s3Key, workOfDate)
		return parser.Parse(ctx, r, s3Key, stdauth.FeedName, loader.ProcessRow)

	case "STDACCTBAL":
		loader := stdacctbal.NewLoader(pool, tenantID, s3Key, workOfDate)
		return parser.Parse(ctx, r, s3Key, stdacctbal.FeedName, loader.ProcessRow)

	case "STDNONMON":
		loader := stdnonmon.NewLoader(pool, tenantID, s3Key, workOfDate)
		return parser.Parse(ctx, r, s3Key, stdnonmon.FeedName, loader.ProcessRow)

	case "STDFSID":
		// STDFSID: Phase 1 already extracted workOfDate. Run real loader directly.
		loader := stdfsid.NewLoader(pool, tenantID, s3Key, workOfDate)
		return parser.Parse(ctx, r, s3Key, stdfsid.FeedName, loader.ProcessRow)

	case "CCX":
		loader := ccx.NewLoader(pool, tenantID, s3Key, workOfDate)
		return parser.Parse(ctx, r, s3Key, ccx.FeedName, loader.ProcessRow)

	case "CLIENTHIERARCHY":
		log.Info("CLIENTHIERARCHY not yet implemented — file logged for non-repudiation", "key", s3Key)
		return parser.Parse(ctx, r, s3Key, "", noopRow)

	default:
		log.Warn("unknown XTRACT type — file logged for non-repudiation only", "type", xtractType, "key", s3Key)
		return parser.Parse(ctx, r, s3Key, "", noopRow)
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
