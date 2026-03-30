// Package filelogger manages the xtract_file_log table.
//
// Non-repudiation contract:
//   1. CreateReceived — called BEFORE any parsing. Records S3 key + SHA-256.
//      If this fails, abort — don't process a file we can't audit.
//   2. SetProcessing  — called when parsing begins. Status transitions to PROCESSING.
//   3. SetLoaded      — called after all rows inserted. Records row count.
//   4. SetFailed      — called on any error. Records error message.
//
// Idempotency: CreateReceived is a no-op if the s3_key already exists
// (ON CONFLICT DO NOTHING). The caller checks the returned sk — if 0, the
// file was already processed and the loader should skip it (status=SKIPPED).
package filelogger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FileLogger writes to reporting.xtract_file_log.
type FileLogger struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *FileLogger {
	return &FileLogger{pool: pool}
}

// CreateReceived inserts a new RECEIVED row. Returns the file_log_sk.
// Returns (0, nil) if the s3_key already exists — caller should skip the file.
func (f *FileLogger) CreateReceived(ctx context.Context, s3Key, xtractType, clientName string,
	fileDate *time.Time, isRerun, isInitLoad bool, sourceSubtype, sha256 string, tenantID string) (int64, error) {

	var sk int64
	err := f.pool.QueryRow(ctx, `
		INSERT INTO reporting.xtract_file_log (
			s3_key, xtract_type, client_name, file_date,
			is_rerun, is_initial_load, source_subtype,
			status, sha256, tenant_id, received_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'RECEIVED', $8, $9, now())
		ON CONFLICT (s3_key) DO NOTHING
		RETURNING file_log_sk`,
		s3Key, xtractType, nullStr(clientName), fileDate,
		isRerun, isInitLoad, nullStr(sourceSubtype),
		nullStr(sha256), tenantID,
	).Scan(&sk)
	if err != nil {
		// No row returned = already exists (DO NOTHING fired)
		return 0, nil
	}
	return sk, nil
}

// SetProcessing transitions status to PROCESSING and records start time.
func (f *FileLogger) SetProcessing(ctx context.Context, sk int64, headerFileDate, headerWorkOfDate *time.Time, detailCount int) error {
	_, err := f.pool.Exec(ctx, `
		UPDATE reporting.xtract_file_log SET
			status = 'PROCESSING',
			header_file_date = $2,
			header_work_of_date = $3,
			detail_record_count = $4,
			processing_started_at = now()
		WHERE file_log_sk = $1`,
		sk, headerFileDate, headerWorkOfDate, detailCount,
	)
	if err != nil {
		return fmt.Errorf("filelogger: SetProcessing sk=%d: %w", sk, err)
	}
	return nil
}

// SetLoaded transitions status to LOADED and records completion.
func (f *FileLogger) SetLoaded(ctx context.Context, sk int64, rowsInserted int, trailerCount int, countMismatch bool) error {
	status := "LOADED"
	var errMsg *string
	if countMismatch {
		msg := fmt.Sprintf("trailer count mismatch: trailer=%d inserted=%d", trailerCount, rowsInserted)
		errMsg = &msg
		status = "LOADED_WITH_WARNINGS"
	}
	_, err := f.pool.Exec(ctx, `
		UPDATE reporting.xtract_file_log SET
			status = $2,
			rows_inserted = $3,
			trailer_record_count = $4,
			error_message = $5,
			processing_completed_at = now()
		WHERE file_log_sk = $1`,
		sk, status, rowsInserted, trailerCount, errMsg,
	)
	if err != nil {
		return fmt.Errorf("filelogger: SetLoaded sk=%d: %w", sk, err)
	}
	return nil
}

// SetFailed records a terminal failure.
func (f *FileLogger) SetFailed(ctx context.Context, sk int64, errMsg string) error {
	_, err := f.pool.Exec(ctx, `
		UPDATE reporting.xtract_file_log SET
			status = 'FAILED',
			error_message = $2,
			processing_completed_at = now()
		WHERE file_log_sk = $1`,
		sk, errMsg,
	)
	if err != nil {
		return fmt.Errorf("filelogger: SetFailed sk=%d: %w", sk, err)
	}
	return nil
}

// SetSkipped marks a duplicate file (already processed).
func (f *FileLogger) SetSkipped(ctx context.Context, s3Key string) error {
	_, err := f.pool.Exec(ctx, `
		INSERT INTO reporting.xtract_file_log (s3_key, xtract_type, status, tenant_id, received_at)
		VALUES ($1, 'UNKNOWN', 'SKIPPED', 'system', now())
		ON CONFLICT (s3_key) DO UPDATE SET status = 'SKIPPED'`,
		s3Key,
	)
	return err
}

// ─── Filename parsing ──────────────────────────────────────────────────────

// ParseFilename extracts xtract type, client name, file date, and flags
// from an XTRACT S3 key.
//
// Naming conventions per PDS spec:
//   STDMONmmddccyy_<ClientName>.txt
//   STDMONmmddccyy_<ClientName>_RERUN.txt
//   STDMONmmddccyy_<ClientName>_INITIALLOAD_<#>.txt
//   STDFSIDmmddyyyy_WM_<ClientName>.txt  (Walmart monthly)
//   CCXmmddccyy_<ClientName>
//   <ClientID>_<ClientName>_CLIENTHIERARCHY_mmddyyyy_FIS.txt
type FileMeta struct {
	XtractType    string
	ClientName    string
	IsRerun       bool
	IsInitialLoad bool
	SourceSubtype string // "DAILY" | "WALMART_MONTHLY"
}

func ParseFilename(key string) FileMeta {
	// Strip path prefix — key may be "STDMON10302025_Morse.txt" or "subdir/STDMON..."
	parts := strings.Split(key, "/")
	filename := parts[len(parts)-1]
	// Strip .txt extension
	name := strings.TrimSuffix(filename, ".txt")
	upper := strings.ToUpper(name)

	meta := FileMeta{SourceSubtype: "DAILY"}

	switch {
	case strings.HasPrefix(upper, "STDMON"):
		meta.XtractType = "STDMON"
	case strings.HasPrefix(upper, "STDAUTH"):
		meta.XtractType = "STDAUTH"
	case strings.HasPrefix(upper, "STDACCTBAL"):
		meta.XtractType = "STDACCTBAL"
	case strings.HasPrefix(upper, "STDNONMON"):
		meta.XtractType = "STDNONMON"
	case strings.HasPrefix(upper, "STDFSID"):
		meta.XtractType = "STDFSID"
		if strings.Contains(upper, "_WM_") {
			meta.SourceSubtype = "WALMART_MONTHLY"
		}
	case strings.HasPrefix(upper, "CCX"):
		meta.XtractType = "CCX"
	case strings.Contains(upper, "CLIENTHIERARCHY"):
		meta.XtractType = "CLIENTHIERARCHY"
	default:
		meta.XtractType = "UNKNOWN"
	}

	if strings.Contains(upper, "_RERUN") {
		meta.IsRerun = true
	}
	if strings.Contains(upper, "_INITIALLOAD") {
		meta.IsInitialLoad = true
	}

	return meta
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
