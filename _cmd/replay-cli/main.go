// Command replay-cli re-processes dead-lettered records.
//
// Must exist before UAT begins (Open Item #24, target May 20, 2026).
// On-call engineers invoke this manually — no AWS Console access required.
//
// Usage:
//   replay-cli --correlation-id <uuid>
//   replay-cli --correlation-id <uuid> --row-seq <n>
//   replay-cli --correlation-id <uuid> --dry-run
//
// Replay is safe to invoke multiple times (three-layer idempotency — §6.5.4).
// Do NOT replay records with failure_reason indicating a code defect.
// See _docs/runbooks/dead_letter_triage.md for triage procedure.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/walker-morse/batch/_adapters/aurora"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/dead_letter/replay"
)

func main() {
	correlationID := flag.String("correlation-id", "", "correlation UUID of the batch file (required)")
	rowSeq        := flag.Int("row-seq", 0, "specific row sequence number (0 = replay all unresolved)")
	dryRun        := flag.Bool("dry-run", false, "preview without executing replay")
	dbHost        := flag.String("db-host",     os.Getenv("DB_HOST"),     "Aurora RDS Proxy endpoint")
	dbName        := flag.String("db-name",     os.Getenv("DB_NAME"),     "database name")
	dbUser        := flag.String("db-user",     os.Getenv("DB_USER"),     "database user")
	dbPass        := flag.String("db-password", os.Getenv("DB_PASSWORD"), "database password")
	dbSSL         := flag.String("db-ssl",      envOrDefault("DB_SSL", "require"), "sslmode")
	ingestBin     := flag.String("ingest-task", "ingest-task", "path to ingest-task binary")
	flag.Parse()

	if *correlationID == "" {
		fmt.Fprintln(os.Stderr, "error: --correlation-id is required")
		flag.Usage()
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := aurora.NewPool(ctx, aurora.Config{
		Host:     *dbHost,
		Database: *dbName,
		User:     *dbUser,
		Password: *dbPass,
		SSLMode:  *dbSSL,
	})
	if err != nil {
		log.Fatalf("replay-cli: connect aurora: %v", err)
	}
	defer pool.Close()

	deadLetterRepo := aurora.NewDeadLetterRepo(pool)
	obs := &observability.NoopObservability{}

	// ExtraArgs carries the DB connection flags so the replayed ingest-task
	// invocation can connect to the same database.
	extraArgs := []string{
		"--db-host", *dbHost,
		"--db-name", *dbName,
		"--db-user", *dbUser,
		"--db-password", *dbPass,
		"--db-ssl", *dbSSL,
		"--env", envOrDefault("PIPELINE_ENV", "DEV"),
		"--region", envOrDefault("AWS_REGION", "us-east-1"),
	}

	r := &replay.Replayer{
		DeadLetters:    deadLetterRepo,
		Obs:            obs,
		IngestTaskPath: *ingestBin,
		ExtraArgs:      extraArgs,
	}

	result, err := r.Replay(ctx, *correlationID, *rowSeq, *dryRun)
	if err != nil {
		log.Fatalf("replay-cli: %v", err)
	}

	log.Printf("replay-cli complete: total=%d replayed=%d skipped=%d failed=%d",
		result.Total, result.Replayed, result.Skipped, result.Failed)

	if len(result.Errors) > 0 {
		for _, e := range result.Errors {
			log.Printf("replay-cli error: %s", e)
		}
		os.Exit(1)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
