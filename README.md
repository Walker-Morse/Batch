# One Fintech — FIS Prepaid Sunrise Batch ETL Integration

**Operator:** Morse LLC · **Brand:** Speak Benefits  
**First client:** Rogue Foods United (RFU), Oregon Medicaid food benefit programme  
**Go-live target:** June 1, 2026 · **RFU programme start:** July 1, 2026  
**Language:** Go (ADR-002) · **Infrastructure:** AWS  
**HLD version:** v00.01.25 (2026-03-17)

---

## What This System Does

One Fintech is the batch ETL platform powering Speak Benefits. It receives eligibility
files from Managed Care Organizations (MCOs), translates them into FIS Prepaid Sunrise
card issuance and benefit load commands, sends those commands to FIS, reconciles the
results, and reports back to the MCO.

When a cardholder stands at a checkout and swipes their Speak Benefits card for a bag
of apples, FIS checks against the Approved Products List that One Fintech maintains.
The apple goes through. A bag of chips does not. That enforcement is not magic — it
is the direct result of this system doing its job correctly every month.

---

## Screaming Architecture

Open this repository and the top-level directories tell you exactly what the system does.
Not the framework. Not the layers. The business.

```
member_enrollment/      Processes SRG310/315/320 files — Stages 1, 2, 3
benefit_loading/        Manages benefit fund lifecycle: loads, sweeps, expiry
card_management/        Card issuance, status updates, replacement processing
approved_products/      APL: the mechanism that enforces spend restrictions at POS
fis_reconciliation/     Stages 4, 5, 6, 7 — assembly through reconciliation report
dead_letter/            Triage and replay for failed pipeline records
sanctions_screening/    OFAC/MVB compliance: OFAC-API.com + sanctions.io
fbo_reconciliation/     FBO purse balance integrity — FDIC pass-through insurance gate

shared/                 Domain types, port contracts, observability interface
schema/                 Aurora PostgreSQL DDL — pipeline state, domain state, reporting
docs/adr/               Architecture Decision Records (ADR-001 through ADR-011)
docs/compliance/        HIPAA, OFAC, FBO integrity requirement summaries
docs/runbooks/          Operational runbooks (dead letter triage, etc.)
infra/                  AWS CDK constructs (networking, ECS, Aurora, storage, IAM)
cmd/
  ingest-task/          ECS Fargate entry point — all 7 pipeline stages
  replay-cli/           Dead letter replay tool (Open Item #24)
  apl-uploader/         APL file generation and upload to FIS
```

---

## Seven Pipeline Stages

```
Stage 1  File Arrival         S3 event received → SHA-256 hashes → batch_files row written
Stage 2  Validation           PGP decrypt → SRG format check → malformed rows → dead_letter_store
Stage 3  Row Processing       Sequential row-by-row · idempotency gate · domain + mart writes
Stage 4  Batch Assembly       FIS 400-byte fixed-width records · PGP-encrypt · S3 write
Stage 5  FIS Transfer         AWS Transfer Family → FIS Prepaid Sunrise SFTP
Stage 6  Return File Wait     Container polls S3 (6h timeout) · dead.letter.alert on timeout
Stage 7  Reconciliation       Match results · update status · fact_reconciliation · MCO report
```

One Fargate task per inbound file. Concurrent file arrivals run concurrent tasks (§5.2a).

---

## Architecture Decisions

| ADR | Decision | Status |
|-----|----------|--------|
| [ADR-001](docs/adr/ADR-001-enterprise-batch-etl-pattern.md) | Enterprise Batch ETL with adapter seams (path to hexagonal) | ACCEPTED |
| [ADR-002](docs/adr/ADR-002-go-implementation-language.md) | Go as implementation language | ACCEPTED |
| [ADR-003](docs/adr/ADR-003-single-ecs-fargate-container.md) | Single ECS Fargate container for all pipeline compute | ACCEPTED |
| [ADR-004](docs/adr/ADR-004-datadog-opentelemetry-observability.md) | Datadog + OpenTelemetry; zero PHI to Datadog by design | ACCEPTED |
| [ADR-005](docs/adr/ADR-005-aws-cdk-infrastructure.md) | AWS CDK as IaC tooling | ACCEPTED |
| [ADR-006](docs/adr/ADR-006-s3-batch-file-staging.md) | S3 for batch file staging and non-repudiation | ACCEPTED |
| [ADR-007](docs/adr/ADR-007-batch-assembly-eventbridge-scheduler.md) | Batch assembly on receipt; EventBridge Scheduler cadence gate | ACCEPTED |
| [ADR-008](docs/adr/ADR-008-aurora-postgresql-serverless-v2.md) | Aurora PostgreSQL Serverless v2 as primary data store | ACCEPTED |
| [ADR-009](docs/adr/ADR-009-separate-database-per-health-plan-client.md) | Separate database per health plan client | ACCEPTED |
| [ADR-010](docs/adr/ADR-010-row-per-member-processing-unit.md) | Row-per-member processing unit; sequential in-process (Stage 3) | ACCEPTED |
| [ADR-011](docs/adr/ADR-011-ofac-sanctions-screening.md) | OFAC-API.com (onboarding) + sanctions.io (continuous monitoring) | ACCEPTED |

---

## Hard Constraints

These are non-negotiable. They come from contracts, regulations, or architectural
decisions with load-bearing consequences.

| Constraint | Source | Where enforced |
|------------|--------|----------------|
| FIS record format knowledge lives ONLY in `fis_reconciliation/fis_adapter/` | ADR-001 | Code review |
| `audit_log` is INSERT-only — no UPDATE or DELETE | §6.2, HIPAA §164.312(b) | PostgreSQL role |
| Idempotency gate (`domain_commands`) written BEFORE any domain state mutation | §4.1.1 | Stage 3 |
| Plaintext staged S3 files deleted immediately after Stage 4 | §5.4.3 | ingest-task |
| Full PAN NEVER stored — proxy numbers only | PCI DSS 4.0 | DB + code review |
| Datadog receives ZERO PHI — stripped at instrumentation layer | §7.2, HIPAA | IObservabilityPort |
| Log File Indicator = `0` hardcoded in assembler — not overridable | §6.6.3 | fis_adapter |
| `expiry_date` = 11:59 PM ET month-end — AT30 MUST complete before this | SOW §2.1 | benefit_loading |
| `available_balance_cents` updated at `domain_commands` write — not deferred | Addendum I | Stage 3 |
| No credential in ECS task environment variables | §5.4.5 | Secrets Manager |
| TLS 1.0 and 1.1 explicitly disabled — not just TLS 1.2 required | §5.4.2, PCI DSS 4.0 Req 4.2.1 | CDK |

---

## Go-Live Critical Path (June 1, 2026)

| Date | Milestone | Risk if missed |
|------|-----------|----------------|
| Mar 30 | SRG310/315/320 column definitions confirmed | Stage 1–3 built on unconfirmed specs |
| Apr 6 | DEV AWS environment provisioned | No implementation sprint start possible |
| Apr 7 | APL compute ownership decided (Open Item #11) | APL misses Apr 27 target |
| Apr 13 | Stages 1–3 complete | Stage 4+ compress |
| Apr 14 | Kendra: WATS form + Risk Config Form + XTRACT unblocked | FIS program not activated; UAT blocked |
| Apr 27 | Stage 4–5 complete; APL upload complete | Stage 6 bleeds into UAT window |
| May 16 | Stage 6–7 complete; full pipeline validated in DEV | UAT starts with incomplete pipeline |
| May 20 | Code freeze; UAT begins | Go-live June 1 at risk |
| June 1 | UAT completion; FIS sign-off; go-live | — |

---

## Compliance

| Framework | Status | Key gap |
|-----------|--------|---------|
| HIPAA Security Rule + HITECH | Design | Formal risk analysis required before go-live |
| PCI DSS 4.0 | Scoping required | Open Item #35 — QSA engagement needed |
| OFAC / MVB § 10.3 | In scope (ADR-011) | Open Items #41, #43 |
| FBO Balance Integrity | In scope (Addendum I) | Open Item #44 |
| SOC 2 Type II | Post-go-live | 6-month observation period after go-live |

See `docs/compliance/` for full compliance documentation.

---

## Open Items (blocking go-live)

| # | Item | Owner | Target |
|---|------|-------|--------|
| 1 | SubprogramId + PackageId from Selvi's ACC upload | Selvi Marappan | Mar 10–11 |
| 5 | WATS form | Kendra Williams | Apr 14 — AT RISK |
| 6 | FIS Data XTRACT contract | Kendra Williams | Apr 14 — BLOCKED on MVB Xnet |
| 9 | SRG310/315/320 column definitions confirmed | Kyle Walker / John Stevens | Mar 30 |
| 11 | APL compute ownership decision | Kyle Walker | Apr 7 |
| 24 | Replay CLI tool | Kyle Walker | Before May 20 UAT |
| 28 | File split strategy for >100K rows | Kyle Walker | Before Apr 27 |
| 34 | BAA execution (RFU) | Kendra Williams | Before UAT — PHI gate |
| 35 | PCI DSS 4.0 scoping determination | Kyle Walker / Kendra Williams | Before UAT |
| 36 | Audit log 6-year archival to S3 Glacier | Kyle Walker | Before go-live |
| 41 | OFAC TRUE match escalation path | Kyle Walker / Legal | Before go-live |
| 43 | MVB written notification of OFAC tools | Kendra Williams | Before go-live |
| 44 | FBO daily reconciliation process defined | Kendra Williams | Before go-live |

Full open item register: HLD §10.

---

*Morse LLC — Speak Benefits — One Fintech*  
*"Boring technology, radical purpose."*
