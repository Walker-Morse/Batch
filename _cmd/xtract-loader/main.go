// Command xtract-loader ingests FIS Data XTRACT feeds from S3 into the
// reporting data mart (§4.3.13–4.3.20).
//
// No temp files — runs in a scratch container with no writable FS.
// Full file buffered in memory (max ~10MB for STDFSID, within 1GB Fargate RAM).
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

	log.Info("xtract-loader start", "bucket", bucket, "key", s3Key, "tenant", tenantID)

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

	// Stream S3 → memory → SHA-256
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

	// Phase 1: lightweight parse to get workOfDate from H record
	firstResult, err := parser.Parse(ctx, bytes.NewReader(rawBytes), s3Key, "", noopRow)
	if err != nil {
		_ = fl.SetFailed(ctx, sk, err.Error())
		return fmt.Errorf("header parse %s: %w", meta.XtractType, err)
	}
	workOfDate := firstResult.Header.WorkOfDate
	fileDate   := firstResult.Header.FileDate
	if workOfDate.IsZero() {
		workOfDate = fileDate
	}

	// Phase 2: full parse with real loader
	result, loadErr := dispatch(ctx, log, pool, meta.XtractType, s3Key, tenantID,
		rawBytes, workOfDate, fileDate)

	if loadErr != nil {
		_ = fl.SetFailed(ctx, sk, loadErr.Error())
		return fmt.Errorf("dispatch %s: %w", meta.XtractType, loadErr)
	}

	_ = fl.SetProcessing(ctx, sk, &fileDate, &workOfDate, result.DetailCount)
	if err := fl.SetLoaded(ctx, sk, result.DetailCount, result.TrailerCount, result.CountMismatch); err != nil {
		return err
	}

	log.Info("file loaded",
		"key", s3Key, "xtract_type", meta.XtractType,
		"detail_rows", result.DetailCount, "trailer_count", result.TrailerCount,
		"count_match", !result.CountMismatch,
	)
	return nil
}

func dispatch(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool,
	xtractType, s3Key, tenantID string, rawBytes []byte,
	workOfDate, fileDate time.Time) (*parser.ParseResult, error) {

	r := func() *bytes.Reader { return bytes.NewReader(rawBytes) }

	switch xtractType {
	case "STDMON":
		return parser.Parse(ctx, r(), s3Key, stdmon.FeedName,
			stdmon.NewLoader(pool, tenantID, s3Key, workOfDate).ProcessRow)

	case "STDAUTH":
		return parser.Parse(ctx, r(), s3Key, stdauth.FeedName,
			stdauth.NewLoader(pool, tenantID, s3Key, workOfDate).ProcessRow)

	case "STDACCTBAL":
		return parser.Parse(ctx, r(), s3Key, stdacctbal.FeedName,
			stdacctbal.NewLoader(pool, tenantID, s3Key, workOfDate).ProcessRow)

	case "STDNONMON":
		return parser.Parse(ctx, r(), s3Key, stdnonmon.FeedName,
			stdnonmon.NewLoader(pool, tenantID, s3Key, workOfDate).ProcessRow)

	case "STDFSID":
		// workOfDate already extracted in Phase 1 — pass directly
		return parser.Parse(ctx, r(), s3Key, stdfsid.FeedName,
			stdfsid.NewLoader(pool, tenantID, s3Key, workOfDate).ProcessRow)

	case "CCX":
		// CCX uses fileDate (the config snapshot date)
		fd := fileDate
		if fd.IsZero() {
			fd = workOfDate
		}
		return parser.Parse(ctx, r(), s3Key, ccx.FeedName,
			ccx.NewLoader(pool, tenantID, s3Key, fd).ProcessRow)

	case "CLIENTHIERARCHY":
		log.Info("CLIENTHIERARCHY not yet implemented — file logged", "key", s3Key)
		return parser.Parse(ctx, r(), s3Key, "", noopRow)

	default:
		log.Warn("unknown XTRACT type — file logged for non-repudiation only", "type", xtractType, "key", s3Key)
		return parser.Parse(ctx, r(), s3Key, "", noopRow)
	}
}

func noopRow(_ context.Context, _ int, _ []string) error { return nil }

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "required env var %s not set\n", key)
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
	if parts := strings.SplitN(s3Key, "/", 2); len(parts) >= 2 && parts[0] != "" {
		return parts[0]
	}
	return "unknown"
}
