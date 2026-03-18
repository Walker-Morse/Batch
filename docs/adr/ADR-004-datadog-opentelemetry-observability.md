# ADR-004 — Datadog and OpenTelemetry for Observability

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

The pipeline runs as a single ECS Fargate container spanning Aurora, S3, AWS Transfer
Family, and EventBridge. A single benefit enrollment event moves through seven named
pipeline stages. Debugging stage-boundary failures without structured log correlation
is operationally untenable. Morse's portfolio standardises on Datadog.

## Decision

Instrument all services with the OpenTelemetry SDK. Use Datadog as the observability
backend for traces, metrics, and logs. Propagate correlation IDs via structured log
fields and the `correlation_id` column in all pipeline state tables.

Architecture (§7.5): CloudWatch and Datadog in series, not in parallel.
- CloudWatch: AWS-native substrate, log transport, 6-year HIPAA retention
- Datadog: operational analysis, trace correlation, on-call alerting

## Zero PHI Guarantee

All structured log events emitted by ingest-task write to stdout and are captured by
the Datadog Agent ECS sidecar container. The same JSON is simultaneously retained in
CloudWatch Logs. Because both destinations receive the identical structured log stream,
and because that stream is stripped of PHI at the instrumentation layer (§7.2), Datadog
by design receives **zero PHI or cardholder data**. This is the technical basis for the
Datadog vendor due diligence position.

## Consequences

- OTel instrumentation is backend-agnostic. Datadog can be swapped for another
  OTel-compatible backend without re-instrumenting services.
- Dead letter alerts (`dead.letter.alert`) and batch halt alarms (`batch.halt.triggered`)
  route to Datadog for on-call pages.
- **CONSTRAINT**: Datadog API key from Secrets Manager only — never in ECS task environment
  variables. The Datadog Agent ECS sidecar container reads the key from Secrets Manager at
  sidecar startup via the ECS task role.
- **CONSTRAINT**: OTel SDK version must be pinned. Datadog Agent sidecar image version
  must be pinned in ECS task definition — do not use `latest` tag.
