package observability

// CloudWatchAdapter writes structured JSON to stdout.
//
// Architecture (§7.5, ADR-004):
//
//	ingest-task stdout (this adapter)
//	  → ECS awslogs driver
//	  → CloudWatch Logs /onefintech/{env}/ingest-task (HIPAA 6-year retention)
//	  → Datadog Agent ECS sidecar (when wired — pre-UAT gate)
//	  → Datadog (operational dashboards, PagerDuty alerting)
//
// No CloudWatch SDK required — the ECS task definition's awslogs log driver
// captures all stdout automatically. This adapter is intentionally thin.
//
// ZERO PHI GUARANTEE: Only correlation_id, tenant_id, client_id, batch_file_id,
// row_sequence_number, and domain_command_id are permitted in log events.
// No names, DOBs, addresses, or any HIPAA-defined identifiers (§7.2,
// HIPAA §164.312(b), OWASP ASVS 7.1.1).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
)

// CloudWatchAdapter implements IObservabilityPort by writing structured JSON
// to stdout. The ECS awslogs driver forwards all stdout to CloudWatch Logs.
type CloudWatchAdapter struct {
	// env is injected from PIPELINE_ENV (DEV/TST/PRD) for log filtering.
	env string
	// out is the write target — os.Stdout in production, injectable for tests.
	out *os.File
}

// NewCloudWatchAdapter returns an adapter ready to write to stdout.
// env should be loaded from the PIPELINE_ENV environment variable.
func NewCloudWatchAdapter(env string) *CloudWatchAdapter {
	return &CloudWatchAdapter{env: env, out: os.Stdout}
}

// cloudWatchLogEntry is the JSON schema written to stdout per event.
// Field names are snake_case to match CloudWatch Logs Insights query syntax
// and Datadog log parsing conventions.
//
// ZERO PHI: no member names, DOBs, addresses, or card numbers are permitted here.
// Only opaque identifiers (UUIDs, sequence numbers, tenant slugs) are allowed.
//
// Required fields (always present, never omitempty):
//   timestamp, level, env, event_type, correlation_id, tenant_id, batch_file_id
type cloudWatchLogEntry struct {
	// ── Always present ────────────────────────────────────────────────────
	Timestamp     string `json:"timestamp"`
	Level         string `json:"level"`
	Env           string `json:"env"`
	EventType     string `json:"event_type"`
	CorrelationID string `json:"correlation_id"` // always set — uuid.Nil before Stage 1
	TenantID      string `json:"tenant_id"`      // always set
	BatchFileID   string `json:"batch_file_id"`  // always set — uuid.Nil before Stage 1
	Message       string `json:"message"`

	// ── Optional context ─────────────────────────────────────────────────
	Stage    *string `json:"stage,omitempty"`
	ClientID *string `json:"client_id,omitempty"`

	// ── Per-row fields ────────────────────────────────────────────────────
	RowSequenceNumber *int    `json:"row_sequence_number,omitempty"`
	DomainCommandID   *string `json:"domain_command_id,omitempty"`
	CommandType       *string `json:"command_type,omitempty"`
	BenefitPeriod     *string `json:"benefit_period,omitempty"`
	SubprogramID      *int64  `json:"subprogram_id,omitempty"`
	FISResultCode     *string `json:"fis_result_code,omitempty"`

	// ── Failure fields ────────────────────────────────────────────────────
	Error           *string `json:"error,omitempty"`
	FailureCategory *string `json:"failure_category,omitempty"`

	// ── File-level fields ─────────────────────────────────────────────────
	S3Key     *string `json:"s3_key,omitempty"`
	SizeBytes *int64  `json:"size_bytes,omitempty"`
	SHA256    *string `json:"sha256,omitempty"`

	// ── Aggregate counts ──────────────────────────────────────────────────
	Total          *int    `json:"total,omitempty"`
	Malformed      *int    `json:"malformed,omitempty"`
	MalformedRate  *string `json:"malformed_rate,omitempty"`
	Staged         *int    `json:"staged,omitempty"`
	Duplicates     *int    `json:"duplicates,omitempty"`
	Failed         *int    `json:"failed,omitempty"`
	RT30Count      *int    `json:"rt30_count,omitempty"`
	RT37Count      *int    `json:"rt37_count,omitempty"`
	RT60Count      *int    `json:"rt60_count,omitempty"`
	DeadLetterRate *string `json:"dead_letter_rate,omitempty"`

	// ── Assembly / transfer ───────────────────────────────────────────────
	Filename       *string `json:"filename,omitempty"`
	RecordCount    *int    `json:"record_count,omitempty"`
	ReturnFilename *string `json:"return_filename,omitempty"`
	WaitMs         *int64  `json:"wait_ms,omitempty"`

	// ── Reconciliation ────────────────────────────────────────────────────
	Completed     *int `json:"completed,omitempty"`
	RT30Completed *int `json:"rt30_completed,omitempty"`
	RT60Completed *int `json:"rt60_completed,omitempty"`

	// ── Pipeline-level ────────────────────────────────────────────────────
	FileType     *string `json:"file_type,omitempty"`
	DurationMs   *int64  `json:"duration_ms,omitempty"`
	Enrolled     *int    `json:"enrolled,omitempty"`
	DeadLettered *int    `json:"dead_lettered,omitempty"`
}

// LogEvent serialises the event as a single-line JSON object to stdout.
// The ECS awslogs driver captures each stdout line as one CloudWatch log event.
// Errors writing to stdout are returned but never fatal — pipeline continues.
func (a *CloudWatchAdapter) LogEvent(_ context.Context, e *ports.LogEvent) error {
	corrID := e.CorrelationID.String()
	batchID := e.BatchFileID.String()

	entry := cloudWatchLogEntry{
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Level:         e.Level,
		Env:           a.env,
		EventType:     e.EventType,
		CorrelationID: corrID,
		TenantID:      e.TenantID,
		BatchFileID:   batchID,
		Message:       e.Message,

		// Optional context
		Stage:    e.Stage,
		ClientID: e.ClientID,

		// Per-row
		RowSequenceNumber: e.RowSequenceNumber,
		CommandType:       e.CommandType,
		BenefitPeriod:     e.BenefitPeriod,
		SubprogramID:      e.SubprogramID,
		FISResultCode:     e.FISResultCode,

		// Failure
		Error:           e.Error,
		FailureCategory: e.FailureCategory,

		// File-level
		S3Key:     e.S3Key,
		SizeBytes: e.SizeBytes,
		SHA256:    e.SHA256,

		// Counts
		Total:          e.Total,
		Malformed:      e.Malformed,
		MalformedRate:  e.MalformedRate,
		Staged:         e.Staged,
		Duplicates:     e.Duplicates,
		Failed:         e.Failed,
		RT30Count:      e.RT30Count,
		RT37Count:      e.RT37Count,
		RT60Count:      e.RT60Count,
		DeadLetterRate: e.DeadLetterRate,

		// Assembly / transfer
		Filename:       e.Filename,
		RecordCount:    e.RecordCount,
		ReturnFilename: e.ReturnFilename,
		WaitMs:         e.WaitMs,

		// Reconciliation
		Completed:     e.Completed,
		RT30Completed: e.RT30Completed,
		RT60Completed: e.RT60Completed,

		// Pipeline-level
		FileType:     e.FileType,
		DurationMs:   e.DurationMs,
		Enrolled:     e.Enrolled,
		DeadLettered: e.DeadLettered,
	}

	// DomainCommandID: serialize UUID pointer to string pointer
	if e.DomainCommandID != nil {
		s := e.DomainCommandID.String()
		entry.DomainCommandID = &s
	}

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("observability: marshal log event: %w", err)
	}

	_, err = fmt.Fprintln(a.out, string(b))
	return err
}

// RecordMetric writes a structured metric event to stdout using the same
// JSON format as LogEvent. CloudWatch Logs metric filters can extract
// numeric values from the "metric_value" field for alarming (§7.3).
//
// When the Datadog Agent sidecar is wired, it will additionally forward
// these as DogStatsD custom metrics.
type cloudWatchMetricEntry struct {
	Timestamp   string            `json:"timestamp"`
	Level       string            `json:"level"`
	Env         string            `json:"env"`
	EventType   string            `json:"event_type"`
	MetricName  string            `json:"metric_name"`
	MetricValue float64           `json:"metric_value"`
	Dimensions  map[string]string `json:"dimensions,omitempty"`
}

func (a *CloudWatchAdapter) RecordMetric(_ context.Context, name string, value float64, dims map[string]string) error {
	entry := cloudWatchMetricEntry{
		Timestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		Level:       "INFO",
		Env:         a.env,
		EventType:   "metric",
		MetricName:  name,
		MetricValue: value,
		Dimensions:  dims,
	}

	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("observability: marshal metric: %w", err)
	}

	_, err = fmt.Fprintln(a.out, string(b))
	return err
}

// strPtr is a helper used in tests and by stages that need *string from a literal.
func strPtr(s string) *string { return &s }

// intPtr is a helper for *int literals.
func intPtr(i int) *int { return &i }

// int64Ptr is a helper for *int64 literals.
func int64Ptr(i int64) *int64 { return &i }

// uuidPtr is a helper for *uuid.UUID literals.
func uuidPtr(u uuid.UUID) *uuid.UUID { return &u }
