# ADR-008 — Aurora PostgreSQL Serverless v2 as Primary Data Store

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

Pipeline state and domain state require ACID guarantees, relational integrity, and direct
queryability. The workload is bursty: idle between file arrivals, high sequential write
throughput during Fargate task execution.

## Decision

Use Aurora PostgreSQL Serverless v2. Use RDS Proxy for connection pooling.

## Alternatives Considered

- **DynamoDB** — rejected. No native relational joins; schema stability required.
- **RDS PostgreSQL (fixed)** — rejected. Fixed ACU means over-provisioning at idle.
- **Aurora Serverless v1** — rejected. v1 has a cold-start pause when scaling from zero.

## Consequences

- ACU scales automatically with Fargate task concurrency.
- RDS Proxy pools connections, supporting multiple simultaneous Fargate tasks.
- Direct SQL queryability by engineering and compliance teams.
- **CONSTRAINT**: ACU floor must be set above zero in TST and PRD — zero-ACU idle
  scaling only in DEV.
- **CONSTRAINT**: ACU max must be explicitly set in CDK for 200K concurrent-write workloads.
- **CONSTRAINT**: Load testing of RDS Proxy + Aurora at 200K invocations required before UAT.
