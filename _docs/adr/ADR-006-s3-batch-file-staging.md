# ADR-006 — S3 for Batch File Staging and Non-Repudiation

**Status:** ACCEPTED  
**Date:** 2026-02-15  
**Decider:** Kyle Walker

## Context

FIS requires PGP-encrypted fixed-width batch files delivered via Secure File Transfer.
Inbound client files (SRG310/315/320) arrive encrypted and must be retained in original
form. Both inbound and outbound files are audit artifacts — Morse must be able to prove
exactly what was sent to FIS and what was received from the health plan client.

## Decision

Use S3 as the staging and retention layer for all inbound and outbound files. Files are
written once and never mutated.

Three buckets / prefix groups:
- `inbound-raw/` — original encrypted client files; versioned; `DeleteObject` denied on IAM role
- `staged/` — decrypted plaintext during active pipeline processing; deleted after Stage 4
- `fis-exchange/` — PGP-encrypted FIS batch files and inbound FIS return files

## Alternatives Considered

- **EFS** — rejected. Unnecessary operational complexity; S3 object model better fit.
- **RDS / Aurora BYTEA storage** — rejected. Couples pipeline state with file retention;
  large files degrade Aurora performance.
- **Ephemeral Lambda /tmp only** — rejected. No persistence; no audit trail.

## Consequences

- S3 object immutability provides non-repudiation: Morse can produce the exact file
  received from a client or sent to FIS at any point in an audit or dispute.
- S3 lifecycle policies control retention period per prefix independently.
- **CONSTRAINT**: S3 bucket policies must enforce SSE-KMS. Public access blocked at account level.
- **CONSTRAINT**: `inbound-raw/` prefix is write-once from the Transfer Family IAM role.
  No application service has `DeleteObject` on `inbound-raw/`.
- **CONSTRAINT**: Plaintext staged files MUST be deleted by the ingest-task immediately
  after Stage 4 confirms successful PGP encryption. 24-hour S3 lifecycle policy on
  `staged/` is a safety backstop only — not a substitute for application-level deletion.
