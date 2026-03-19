// Package observability implements the IObservabilityPort for the One Fintech pipeline.
//
// Architecture per §7.5 and ADR-004:
//   ingest-task stdout (structured JSON)
//     → Datadog Agent ECS sidecar container
//     → CloudWatch Logs (HIPAA 6-year retention substrate)
//     → Datadog Lambda Extension
//     → Datadog (operational analysis, alerting, on-call)
//
// CloudWatch and Datadog are complementary, not interchangeable:
// - CloudWatch: AWS-native alarm integration (§7.3) + 6-year HIPAA log retention
// - Datadog: trace correlation, operational dashboards, PagerDuty integration
//
// The IObservabilityPort boundary means swapping the backend = new adapter only.
//
// ZERO PHI GUARANTEE: This package enforces that no PHI or PII ever reaches
// structured logs. Permitted identifiers: correlation_id, tenant_id, client_id,
// batch_file_id, row_sequence_number, domain_command_id.
// Basis: HIPAA §164.312(b), OWASP ASVS 7.1.1, §7.2.
package observability

import (
	"context"

	"github.com/walker-morse/batch/_shared/ports"
)

// EventTypes are the canonical structured log event names used throughout the pipeline.
// These names match the CloudWatch metric filter patterns in §7.3.
const (
	EventFileArrived             = "file.arrived"
	EventFileProcessingComplete  = "file.processing.complete"
	EventReturnFileArrived       = "return.file.arrived"
	EventDeadLetterWritten       = "dead.letter.written"    // triggers CloudWatch alarm
	EventDeadLetterCaptureFailed = "dead.letter.capture.failed" // higher severity — pages immediately
	EventBatchHaltTriggered      = "batch.halt.triggered"   // RT99 full-file rejection
	EventBatchStalled            = "batch.stalled"          // unresolved dead letters at Stage 3 end
	EventBatchAssembleNightly    = "batch.assemble.nightly" // EventBridge Scheduler trigger (ADR-007)
)

// MetricNames are the canonical metric names emitted via RecordMetric.
// CloudWatch metric filters extract metric_value from log events with these names.
// Datadog Agent sidecar forwards them as custom metrics when wired (pre-UAT gate).
//
// All four are required. See §7.3 for alarm thresholds.
const (
	MetricDeadLetterRate       = "dead_letter_rate"       // % of rows dead-lettered in Stage 3
	MetricMalformedRate        = "malformed_rate"         // % of rows malformed in Stage 2
	MetricEnrollmentSuccessRate = "enrollment_success_rate" // % of RT30s successfully enrolled in Stage 7
	MetricPipelineDurationMs   = "pipeline_duration_ms"   // wall time for full pipeline run
	MetricPipelineErrorRate    = "pipeline_error_rate"    // Severity 1 P0 alarm
	MetricPipelineStall        = "pipeline_stall"         // Severity 2 P1 alarm
)

// NoopObservability is a no-op implementation for testing.
type NoopObservability struct{}

func (n *NoopObservability) LogEvent(_ context.Context, _ *ports.LogEvent) error { return nil }
func (n *NoopObservability) RecordMetric(_ context.Context, _ string, _ float64, _ map[string]string) error {
	return nil
}
