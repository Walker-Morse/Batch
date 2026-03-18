# ADR-006b — S3 Event Notification as ingest-task Trigger

**Status:** ACCEPTED
**Date:** 2026-03-18
**Decider:** Kyle Walker

## Context

An SRG file arriving in the inbound-raw S3 bucket must trigger the ingest-task
ECS Fargate container. This ADR documents the trigger mechanism for file arrival,
distinct from the nightly delivery cadence gate (ADR-007).

## Decision

Enable EventBridge notifications on the inbound-raw bucket (`eventBridgeEnabled: true`
in StorageConstruct). An EventBridge rule routes `Object Created` events from the
`inbound-raw` prefix to ECS RunTask via an input transformer that extracts
`s3.object.key` → `S3_KEY` and `s3.bucket.name` → `S3_BUCKET`.

Two separate trigger paths:
1. **File arrival** → S3 ObjectCreated → EventBridge → ECS RunTask (Stages 1-4: process immediately)
2. **Nightly schedule** → EventBridge Scheduler → ECS RunTask (delivery gate: submit to FIS)

## Consequences

- `eventBridgeEnabled: true` on inbound-raw bucket in StorageConstruct (done).
- EventBridge rule + ECS RunTask target must be wired in CDK before first DEV file drop.
  (TODO: add EventBridgeRule construct — Open Item for next CDK session).
- Input transformer extracts S3 key and bucket from the event and injects as
  ECS task overrides for CORRELATION_ID, S3_KEY, S3_BUCKET, FILE_TYPE, TENANT_ID.
- FIS Transfer is a separate process, not triggered by file arrival — it runs on
  the nightly schedule once assembly is complete.
