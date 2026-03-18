# ADR-003 — Single ECS Fargate Container for All Pipeline Compute

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

Three compute patterns were evaluated:
1. Lambda for row-level fan-out (original design)
2. Lambda for batch-assembler with ECS Fargate for row processing (hybrid)
3. Single ECS Fargate container for the complete file lifecycle including return file
   wait, reconciliation, and data mart writes

## Decision

Use a single ECS Fargate container (`ingest-task`) for all pipeline compute.
No Lambda functions required on any path. The container handles the complete file
lifecycle across seven named stages.

Design is evolution-ready: each stage is behind a named Go interface, emits structured
log events with the same schema as a future EventBridge event, and can be extracted
to a separate compute unit without domain logic changes.

## Alternatives Considered

- **Lambda for row-processor (fan-out, SQS-triggered)** — evaluated and superseded by
  ADR-010. Unbounded Lambda concurrency, Aurora connection exhaustion risk, SQS/DLQ
  operational overhead.
- **Lambda for batch-assembler** — eliminated. Assembly, encryption, transfer, return
  file wait, and reconciliation are too tightly coupled to benefit from Lambda's
  stateless model.
- **Two ECS Fargate tasks (ingest + reconciliation)** — considered. Rejected for Phase 1:
  adds task coordination complexity not justified at current RFU volumes. The Stage 6/7
  seam is the **first natural decomposition point** when volumes grow.

## Consequences

- Simplest possible topology: one ECR image, one ECS task definition, one CloudWatch
  log group, one IAM task role.
- Fargate billing applies for return file wait duration (up to 6 hours). Accepted
  cost trade-off for Phase 1.
- **Evolution path**: first decomposition candidate = split Stage 6–7 (Return File Wait
  + Reconciliation) into a separate ECS task triggered by `batch.transferred` event.
- Vertical scale is the throughput lever. Fargate task sizing must be validated in
  load testing for the full lifecycle memory profile.
