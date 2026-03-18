# ADR-010 — Row-Per-Member Processing Unit with Sequential In-Process Implementation

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

SRG310 files may contain tens of thousands of rows. Two independent decisions:
1. What is the processing unit — a chunk of N rows, or one row at a time?
2. What is the execution model — sequential in-process iteration, or parallel distributed?

## Decision

Each CSV row is an independent processing unit with its own idempotency key, failure
boundary, and terminal state. Current implementation: sequential in-process iteration
within Stage 3. No SQS queues, no Lambda functions.

## Consequences

- Row-level failure isolation: exception at any row → dead_letter_store, adjacent rows unaffected.
- Natural idempotency key: `(tenant_id, correlation_id, row_sequence_number)`.
- Sequential throughput scales linearly with row count. At current RFU volumes (≤200K rows),
  FIS batch processing SLA (~45-60 min / 50K records) is the dominant constraint.
  Validate in load testing (§5.6).
- **Evolution path**: Stage 3 loop body is the clean extraction point for parallel execution.
  Extract loop body to worker function, emit tasks to SQS or work queue, invoke via Lambda
  or Fargate pool. No domain logic changes required.
- **CONSTRAINT**: Do not implement row-level parallelism without confirming Aurora connection
  pool sizing. RDS Proxy is sized for one persistent connection per concurrent Fargate task —
  not per-row concurrent connections.
