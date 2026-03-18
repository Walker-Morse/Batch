# ADR-009 — Separate Database Per Health Plan Client

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

One Fintech serves multiple health plan clients (MCOs). Each client's data represents
Medicaid benefit eligibility and fund movement — highly sensitive with strict regulatory
handling. A shared schema with tenant discriminator columns creates data leak risk.

## Decision

Provision a dedicated Aurora PostgreSQL database per health plan client. No shared schema,
no shared connection pool, no cross-client queries possible at the database layer.

## Alternatives Considered

- **Shared database with tenant_id discriminator** — rejected. A single missing WHERE clause
  is a data breach. Not acceptable for Medicaid data.
- **Shared database with separate schemas per tenant** — rejected. Schema-level isolation
  weaker than database-level.

## Consequences

- Hard isolation boundary at the database layer.
- Compliance requirement and sales differentiator — clients can be shown physical isolation.
- **CONSTRAINT**: CDK must provision a new database construct per client onboarding.
- **CONSTRAINT**: RDS Proxy must be configured per database. Pool sizing per-client.
