# ADR-002 — Go as Implementation Language

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

**Amendment:** 2026-03-18 — Upgraded from Go 1.22 to Go 1.25.8. Seven stdlib CVEs
(GO-2025-3750, GO-2025-4007, GO-2025-4009, GO-2025-4010, GO-2025-4011,
GO-2026-4601, GO-2026-4602) identified by govulncheck via reachable paths through
`golang.org/x/text` (Unicode normalisation) and `github.com/google/uuid`. All fixed
in go1.25.8. Concurrent dep bumps: `x/text` v0.35.0, `x/crypto` v0.49.0,
`pgx/v5` v5.8.0. Go 1.22 EOL is February 2026 — upgrade was overdue.

## Context

The batch pipeline runs as a single ECS Fargate container processing inbound SRG files
sequentially, row by row. Low startup time, efficient sequential I/O, strong type system,
and low memory footprint are first-order concerns. Morse's engineering team has existing
Go capability.

## Decision

Use Go for all One Fintech batch pipeline services.

## Alternatives Considered

- **Java / Spring Boot** — rejected. JVM startup time (500ms–1s+) is material cost
  in a container-per-file model.
- **Node.js / TypeScript** — rejected. Weaker type system for domain modelling;
  less predictable memory behaviour under high concurrency.
- **Python** — rejected. GIL limits true concurrency; runtime behaviour less predictable
  under sustained sequential I/O load in a Fargate task.

## Consequences

- Go binary startup time is sub-100ms — appropriate for Fargate task initialisation.
- Low memory footprint suitable for 200K-row sequential processing without GC pressure.
- Goroutines available for within-stage parallelism if needed in future decomposed architecture.
- **CONSTRAINT**: Idiomatic Go preferred over heavy abstraction. Go generics adoption
  at team discretion.
