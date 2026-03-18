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
// batch_file_id (int), row_sequence_number (int).
// Basis: HIPAA §164.312(b), OWASP ASVS 7.1.1, §7.2.
//
// Required CloudWatch alarms (§7.3):
//   - File stall:    file not completed within 4 hours of arrival
//   - Dead letter:   dead.letter.written metric > 0 in any 5-minute window
//   - Batch halt:    batch.halt.triggered event fires
//   - Malformed row: > 5% of rows in any file are Rejected_Malformed
//
// SLA severity tiers (§6.7.3, SOW Exhibit C):
//   Severity 1 (Critical): pipeline_error_rate P0 → PagerDuty immediate
//   Severity 2 (High):     pipeline_stall / dead_letter_rate P1 → on-call
//   Severity 3 (Medium):   anomaly alert → ticket created
//   Severity 4 (Low):      logged, no automated alert
package observability

import (
	"context"

	"github.com/walker-morse/batch/shared/ports"
)

// EventTypes are the canonical structured log event names used throughout the pipeline.
// These names match the CloudWatch metric filter patterns in §7.3.
const (
	EventFileArrived          = "file.arrived"
	EventFileProcessingComplete = "file.processing.complete"
	EventReturnFileArrived    = "return.file.arrived"
	EventDeadLetterWritten    = "dead.letter.written"    // triggers CloudWatch alarm
	EventDeadLetterCaptureFailed = "dead.letter.capture.failed" // higher severity — pages immediately
	EventBatchHaltTriggered   = "batch.halt.triggered"  // RT99 full-file rejection
	EventBatchStalled         = "batch.stalled"          // unresolved dead letters at Stage 3 end
	EventBatchAssembleNightly = "batch.assemble.nightly" // EventBridge Scheduler trigger (ADR-007)
)

// MetricNames are the canonical Datadog metric names referenced in §6.7.3.
const (
	MetricPipelineErrorRate = "pipeline_error_rate" // Severity 1 P0 alarm
	MetricPipelineStall     = "pipeline_stall"      // Severity 2 P1 alarm
	MetricDeadLetterRate    = "dead_letter_rate"    // Severity 2 P1 alarm
)

// NoopObservability is a no-op implementation for testing.
type NoopObservability struct{}

func (n *NoopObservability) LogEvent(_ context.Context, _ *ports.LogEvent) error   { return nil }
func (n *NoopObservability) RecordMetric(_ context.Context, _ string, _ float64, _ map[string]string) error {
	return nil
}
