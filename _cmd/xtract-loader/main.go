// Command xtract-loader ingests FIS Data XTRACT feeds from S3 into the
// reporting data mart (§4.3.13–4.3.20).
//
// Triggered by EventBridge S3 ObjectCreated on the xtract bucket.
// Environment variables injected by EventBridge (same pattern as ingest-task):
//   S3_BUCKET   — xtract bucket name
//   S3_KEY      — object key of the arriving XTRACT file
//   TENANT_ID   — from S3 key prefix (xtract/{tenant_id}/...)
//                 or from TENANT_ID env var for manual invocations
//
// Non-repudiation sequence:
//   1. GetObject from S3 — stream through SHA-256 hasher
//   2. Write xtract_file_log row (RECEIVED + SHA-256) BEFORE any parsing
//   3. Parse file — validate H/T structure, emit detail rows
//   4. Dispatch rows to feed-specific loader (fact_monetary, etc.)
//   5. Update xtract_file_log to LOADED (or FAILED on error)
//
// The raw file is never deleted — S3 bucket is versioned + write-once.
// xtract_file_log.s3_key is unique; duplicate files are detected and skipped.
package main

import (
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
	// ── Config ────────────────────────────────────────────────────────────
	bucket  := mustEnv("S3_BUCKET")
	s3Key   := mustEnv("S3_KEY")
	tenantID := envOr("TENANT_ID", inferTenantID(s3Key))

	dbHost  := mustEnv("DB_HOST")
	dbName  := mustEnv("DB_NAME")
	dbUser  := mustEnv("DB_USER")
	dbPass  := mustEnv("DB_PASSWORD")

	log.Info("xtract-loader start",
		"bucket", bucket, "key", s3Key, "tenant", tenantID)

	// ── DB pool ──────────────────────────────────────────────────────────
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

	// ── Step 1: Get object from S3 ────────────────────────────────────────
	obj, err := s3Client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: &bucket,
		Key:    &s3Key,
	})
	if err != nil {
		return fmt.Errorf("s3 GetObject s3://%s/%s: %w", bucket, s3Key, err)
	}
	defer obj.Body.Close()

	// ── Step 2: Stream through SHA-256 hasher, buffer to temp file ────────
	// We buffer to a temp file so we can stream twice: once for SHA-256,
	// once for parsing. This avoids loading the full 9.9MB STDFSID into RAM.
	tmp, err := os.CreateTemp("", "xtract-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), obj.Body); err != nil {
		return fmt.Errorf("stream to temp: %w", err)
	}
	sha256Hex := hex.EncodeToString(h.Sum(nil))

	log.Info("file received", "key", s3Key, "sha256", sha256Hex[:16]+"...")

	// ── Step 3: Write xtract_file_log (non-repudiation) ──────────────────
	fl := filelogger.New(pool)
	meta := filelogger.ParseFilename(s3Key)

	sk, err := fl.CreateReceived(ctx,
		s3Key, meta.XtractType, meta.ClientName,
		nil, // fileDate — will be updated from H record after parse
		meta.IsRerun, meta.IsInitialLoad, meta.SourceSubtype,
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

	// ── Step 4: Parse and load ─────────────────────────────────────────────
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("temp seek: %w", err)
	}

	result, loadErr := dispatch(ctx, log, pool, meta.XtractType, s3Key, tenantID, tmp)

	// ── Step 5: Update file log ────────────────────────────────────────────
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

// dispatch routes the file to the correct feed-specific loader.
func dispatch(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool,
	xtractType, s3Key, tenantID string, r io.Reader) (*parser.ParseResult, error) {

	switch xtractType {
	case "STDMON":
		loader := stdmon.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, r, s3Key, stdmon.FeedName, func(ctx context.Context, lineNum int, fields []string) error {
			return loader.ProcessRow(ctx, lineNum, fields)
		})

	case "STDAUTH":
		loader := stdauth.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, r, s3Key, stdauth.FeedName, func(ctx context.Context, lineNum int, fields []string) error {
			return loader.ProcessRow(ctx, lineNum, fields)
		})

	case "STDACCTBAL":
		loader := stdacctbal.NewLoader(pool, tenantID, s3Key)
		result, err := parser.Parse(ctx, r, s3Key, stdacctbal.FeedName, func(ctx context.Context, lineNum int, fields []string) error {
			return loader.ProcessRow(ctx, lineNum, fields)
		})
		return result, err

	case "STDNONMON":
		loader := stdnonmon.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, r, s3Key, stdnonmon.FeedName, func(ctx context.Context, lineNum int, fields []string) error {
			return loader.ProcessRow(ctx, lineNum, fields)
		})

	case "STDFSID":
		// STDFSID needs workOfDate from the H record to initialize the loader.
		// The parser provides this in ParseResult.Header after the full parse.
		// We use a two-phase approach: first pass captures header and counts rows
		// (cheap — all rows are nooped), second pass runs the real loader.
		// The temp file is seekable so both passes read from the same buffer.
		seeker, canSeek := r.(io.ReadSeeker)
		if !canSeek {
			log.Warn("STDFSID source not seekable — using zero workOfDate", "key", s3Key)
			loader := stdfsid.NewLoader(pool, tenantID, s3Key, time.Time{})
			return parser.Parse(ctx, r, s3Key, stdfsid.FeedName, loader.ProcessRow)
		}
		// Phase 1: parse header only (noop rows)
		firstResult, err := parser.Parse(ctx, seeker, s3Key, stdfsid.FeedName, noopRow)
		if err != nil {
			return nil, fmt.Errorf("stdfsid header pass: %w", err)
		}
		// Phase 2: seek back, parse with real loader
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("stdfsid seek: %w", err)
		}
		loader := stdfsid.NewLoader(pool, tenantID, s3Key, firstResult.Header.WorkOfDate)
		return parser.Parse(ctx, seeker, s3Key, stdfsid.FeedName, loader.ProcessRow)

	case "CCX":
		loader := ccx.NewLoader(pool, tenantID, s3Key)
		return parser.Parse(ctx, r, s3Key, ccx.FeedName, func(ctx context.Context, lineNum int, fields []string) error {
			return loader.ProcessRow(ctx, lineNum, fields)
		})

	case "CLIENTHIERARCHY":
		log.Info("CLIENTHIERARCHY loader not yet implemented — file logged for non-repudiation", "key", s3Key)
		return parser.Parse(ctx, r, s3Key, "", noopRow)

	default:
		log.Warn("unknown XTRACT type — file logged for non-repudiation only", "type", xtractType, "key", s3Key)
		return parser.Parse(ctx, r, s3Key, "", noopRow)
	}
}

// noopRow is used for feeds not yet implemented — validates file structure
// and records SHA-256 for non-repudiation without writing to any fact table.
func noopRow(_ context.Context, _ int, _ []string) error { return nil }

// ─── Helpers ──────────────────────────────────────────────────────────────

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

// inferTenantID extracts the tenant from the S3 key convention:
// {tenant_id}/STDMON10302025_Morse.txt → "rfu-oregon"
// Falls back to "unknown" so the file log row is still written.
func inferTenantID(s3Key string) string {
	parts := strings.SplitN(s3Key, "/", 2)
	if len(parts) >= 2 && parts[0] != "" {
		return parts[0]
	}
	return "unknown"
}
