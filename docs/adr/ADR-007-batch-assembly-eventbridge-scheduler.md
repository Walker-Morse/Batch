# ADR-007 — Batch Assembly on Receipt; EventBridge Scheduler as Per-Client Delivery Cadence Gate

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

FIS expects one batch file per client per delivery window. Client SRG files may arrive
at any hour. Assembly and delivery must be decoupled — assembling immediately on arrival
ensures records are ready; delivery timing is controlled separately per client.

## Decision

Batch assembly executes inline within the ingest-task container immediately after Stage 3
completes. Delivery to FIS is gated by an EventBridge Scheduler rule configured per client
delivery cadence (e.g., nightly 02:00 UTC), which triggers an ECS RunTask targeting the
ingest-task task definition.

## Consequences

- Late-arriving SRG files assemble immediately regardless of delivery window.
- Delivery cadence is per-client configuration in EventBridge Scheduler — not a code change.
- Scheduler fires `batch.assemble.nightly` regardless of whether new records are present —
  the assembler is idempotent if nothing has changed since the last run.
- **CONSTRAINT**: EventBridge Scheduler rules must be provisioned per client in CDK.
  Adding a client requires a CDK deployment.
- Recommended submission window: no later than 06:00 Pacific (confirm with Kendra Williams,
  Open Item #29) — so FIS processing completes within normal business hours.
