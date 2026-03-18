# ADR-001 — Enterprise Batch ETL Pattern with Adapter Seams (Path to Hexagonal)

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

One Fintech must support multiple card processors (FIS Prepaid Sunrise, InComm Healthcare)
and a future data warehouse adapter without coupling domain logic to any one integration
target. The domain is rich: multiple aggregates (Consumer, Card, Purse), strong invariants,
event-driven state transitions, and compliance-grade audit requirements.

Hexagonal architecture (ports and adapters) was the original design direction and remains
the correct long-term pattern. However, the engineering team found the full hexagonal
formalism too complex to deliver within the June 1, 2026 go-live constraint.

## Decision

Implement an enterprise-ready Batch ETL pipeline with clean stage boundaries
(ingest, validate, transform, assemble, transfer, reconcile) and named adapter seams
that preserve a clear migration path to hexagonal architecture.

- Apply DDD aggregates with explicit invariant ownership
- Apply CQRS to separate write commands (`domain_commands`) from read projections
- The FIS translation layer (`fis_reconciliation/fis_adapter`) and a future data warehouse
  adapter are the two explicitly named seams — their boundaries are defined now so that
  formalising them as ports later requires no domain-layer changes

This is not a retreat from hexagonal. It is a deliberate staging decision.

## Alternatives Considered

- **Transaction script** — rejected. No boundary between domain logic and FIS record
  construction; untestable without live FIS connection; no migration path.
- **Full hexagonal now** — evaluated seriously, remains the correct long-term pattern.
  Rejected for Phase 1: the team found the full formalism too complex for the June 1
  go-live constraint.

## Consequences

- `fis_reconciliation/fis_adapter` and a future InComm adapter are named seams.
  When ready to formalise hexagonal, these become `ICardProcessorPort` implementors
  with no domain-layer changes required.
- Domain logic is testable without a card processor. Adapters are swappable without
  touching aggregates.
- CQRS read projections (`member_360_view`, `balance_view`, `settlement_view`) are
  deferred; write path is fully specified in Phase 1. `domain_commands` is the
  CQRS write-side record.
- **CONSTRAINT**: All processor-specific record construction (RT30, RT37, RT60 format,
  FIS field widths) MUST live inside `fis_reconciliation/fis_adapter` — never in
  pipeline stage logic. This boundary is what makes future hexagonal formalisation
  possible without a domain rewrite.
