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
// and row_sequence_number are permitted in log events. No names, DOBs, addresses,
// or any HIPAA-defined identifiers (§7.2, HIPAA §164.312(b), OWASP ASVS 7.1.1).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

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
type cloudWatchLogEntry struct {
	Timestamp         string  `json:"timestamp"`
	Level             string  `json:"level"`
	Env               string  `json:"env"`
	EventType         string  `json:"event_type"`
	Stage             *string `json:"stage,omitempty"`
	CorrelationID     *string `json:"correlation_id,omitempty"`
	TenantID          *string `json:"tenant_id,omitempty"`
	ClientID          *string `json:"client_id,omitempty"`
	BatchFileID       *string `json:"batch_file_id,omitempty"`
	RowSequenceNumber *int    `json:"row_sequence_number,omitempty"`
	Message           string  `json:"message"`
	Error             *string `json:"error,omitempty"`
}

// LogEvent serialises the event as a single-line JSON object to stdout.
// The ECS awslogs driver captures each stdout line as one CloudWatch log event.
// Errors writing to stdout are returned but never fatal — pipeline continues.
func (a *CloudWatchAdapter) LogEvent(_ context.Context, e *ports.LogEvent) error {
	entry := cloudWatchLogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Level:     e.Level,
		Env:       a.env,
		EventType: e.EventType,
		Stage:     e.Stage,
		Message:   e.Message,
		Error:     e.Error,
	}

	if e.CorrelationID != nil {
		s := e.CorrelationID.String()
		entry.CorrelationID = &s
	}
	if e.TenantID != nil {
		entry.TenantID = e.TenantID
	}
	if e.ClientID != nil {
		entry.ClientID = e.ClientID
	}
	if e.BatchFileID != nil {
		s := e.BatchFileID.String()
		entry.BatchFileID = &s
	}
	if e.RowSequenceNumber != nil {
		entry.RowSequenceNumber = e.RowSequenceNumber
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
