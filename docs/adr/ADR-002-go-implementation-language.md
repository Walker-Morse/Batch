# ADR-002 — Go as Implementation Language

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

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
