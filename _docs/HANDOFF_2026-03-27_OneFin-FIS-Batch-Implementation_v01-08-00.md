# One Fintech — FIS Prepaid Sunrise
## Batch ETL Integration — Session Handoff
`HANDOFF_2026-03-27_OneFin-FIS-Batch-Implementation_v01-08-00`

| Field | Value |
| --- | --- |
| Date | 2026-03-27 |
| Repo | Walker-Morse/Batch (GitHub), branch main |
| HEAD commit | 13f392d |
| Session commits | 3d7c3f9 → 13f392d (36 commits) |
| Language | Go 1.25.8 |
| Test baseline | 168 passing (v01-07-00) → 305 passing (+137 new) |
| CI | ✅ Both green: CI #144, Build and Deploy #118 |
| Go-Live Target | June 1, 2026 — RFU programme start July 1, 2026 |

---

# 1. What Was Done This Session

Two major workstreams: Card & Member REST API (new service) and a series of
infrastructure, observability, and correctness fixes across the platform.

---

## 1.1 Card & Member API (`card_member_api/`)

A new domain-shaped REST API for card and member lifecycle operations, deployed
to ECS Fargate behind a public ALB in DEV.

### Architecture

| Concern | Detail |
| --- | --- |
| Pattern | Domain-shaped REST — callers speak One Fintech language, not FIS |
| FIS adapter | `fis_code_connect/` — IFisCodeConnectPort interface, 20 FIS endpoints typed |
| FIS mode (DEV) | `-fis-mock=true` — in-memory mock, no FIS credentials needed (OI #31) |
| Auth | `X-Dev-Claims` header bypass in DEV; Cognito JWT RS256 in TST/PRD (pending) |
| Idempotency | Two-layer (see §1.3 below) |
| Swagger UI | http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com/docs/ |

### 8 Domain Endpoints

| Method | Path | Operation |
| --- | --- | --- |
| POST | /v1/members | EnrollMember |
| GET | /v1/members/{id} | GetMember |
| PUT | /v1/members/{id} | UpdateMember |
| POST | /v1/cards/{id}/cancel | CancelCard |
| POST | /v1/cards/{id}/replace | ReplaceCard |
| POST | /v1/cards/{id}/load | LoadFunds |
| GET | /v1/cards/{id}/balance | GetBalance |
| POST | /v1/cards/resolve | ResolveCard (IVR token → member) |

### Package structure

```
card_member_api/
  fis_code_connect/       IFisCodeConnectPort (20 endpoints), typed IDs
  fis_code_connect/mock/  In-memory mock — SeedCard, SeedPerson, InjectError
  ports/                  ICardService, IMemberService, all request/response types
  service/                CardService, MemberService — business logic + gate
  handler/                8 HTTP handlers (thin skin over services)
  handler/middleware/     JWTAuth, RequireCaller, CorrelationID, JSONContentType
  router/                 Route table, Go 1.22 pattern matching
  openapi/                openapi.yaml (embedded), Swagger UI with Dev Claims Builder
_cmd/api-server/          Server entry point, stub repos, graceful shutdown
```

### FIS spec findings (PrePaidSouthCardServices.json v1.4.0)

| Finding | Impact |
| --- | --- |
| FisPersonID is int32 (not string) | Types corrected throughout |
| POST /persons and POST /cards return 201 NO BODY | Must POST then GET to resolve assigned IDs |
| POST /cards/replace returns 201 NO BODY | New cardId not in response — GET pattern needed (confirm with John Stevens OI #47) |
| pps-authorization = Base64({"username":…,"password":…,"source":0}) | Not OAuth2 — one Secrets Manager key |
| encode-response: false required | Hardcoded in adapter |
| clientReferenceNumber has NO idempotency guarantee | FIS documents this explicitly — our gate is authoritative |

---

## 1.2 CorrelationID vs IdempotencyKey — Separation of Concerns

These were previously conflated. Now cleanly separated throughout the stack.

| Header | Purpose | Lifecycle |
| --- | --- | --- |
| `X-Correlation-ID` | Per-request trace ID | Changes every HTTP call. Auto-generated if absent. Stored in logs, audit, observability only. |
| `X-Idempotency-Key` | Operation dedup key | Caller-supplied. Stable across retries. Feeds domain_commands gate exclusively. |

`RequestContext` struct carries both as separate `uuid.UUID` fields.

---

## 1.3 Two-Layer Idempotency (Option C)

**The problem with the prior design:** The `domain_commands` UNIQUE constraint
incorrectly included `correlation_id`, scoping uniqueness to a single batch file.
The `FindDuplicate` query excluded it (correct), but the DB constraint didn't enforce
what the query assumed. Fixed.

**Layer 1 — UUID short-circuit (`X-Idempotency-Key`)**

Caller supplies a UUID stable across retries. `FindByIdempotencyKey()` checked first.

| Found status | Action |
| --- | --- |
| Completed | Return cached result — no FIS call |
| Accepted | 409 in-flight |
| Failed | Fall through to Layer 2 (allow retry) |
| Not found | Fall through to Layer 2 |

**Layer 2 — Composite business-rule dedup (cross-caller protection)**

`(tenant_id, client_member_id, command_type, benefit_period)` checked regardless of
idempotency key. Catches the IVR calling cancel on a member the UW already cancelled,
even with a completely different UUID. Batch pipeline rows never have an idempotency
key — they are protected entirely by this layer.

**DDL changes** (`_schema/pipeline_state/002_domain_commands.sql`):
- Added `idempotency_key UUID` column with partial UNIQUE index `WHERE NOT NULL`
- Removed `correlation_id` from UNIQUE constraint (was wrong — scoped to file)
- Added `tenant_id` to UNIQUE constraint (correct cross-file scope)
- Added REST API command types to CHECK constraint
- `correlation_id` column kept — now tracing only, not part of any constraint

⚠️ **Migration pending:** The DDL changes have not been applied to the live Aurora
instance. `ALTER TABLE` migration required — NOT drop/recreate (live data exists).
The Aurora adapter will fail at runtime until this runs.

---

## 1.4 ECS / CDK Infrastructure

New `ApiServerConstruct` (`infra/lib/constructs/api-server.ts`):

| Resource | Detail |
| --- | --- |
| ALB | `onefintech-dev-api-721349829` — internet-facing (DEV), public subnets |
| ECS Service | `onefintech-dev-api-server` — 1 task, Fargate, private subnets |
| ECR Repo | `onefintech-dev/api-server` — `emptyOnDelete: true` prevents rollback loop |
| Task def | SHA-pinned image tag — forces redeployment on every image push |
| Circuit breaker | `{ rollback: false }` — fails fast, no infinite CFN hang |
| Health check | ALB target group GET /healthz → 200 only (scratch image, no shell) |
| Log group | `/onefintech/dev/api-server` — 6-year retention |

**deploy.yml changes:**
- `build-ingest-task` and `build-api-server` run in parallel after test
- `cdk-deploy` depends on both builds
- `API_SERVER_IMAGE_TAG=${{github.sha}}` passed to CDK — SHA tag in task def

---

## 1.5 Grafana Dashboard Improvements

| Commit | Change |
| --- | --- |
| 6944a25 | File Detail dashboard (`onefintech-detail`) — 11 table panels, row-level drill-through |
| 3040742 | Data links from overview to detail view panels |
| 397e5da | Column filters on all table panels |
| 2e06ad5 | Morse branding replaces Grafana logo |
| 150b566 | File row links fixed, File label column added |

---

## 1.6 Other Fixes

| Commit | Fix |
| --- | --- |
| 6501739 | Dead-letter on FIS field-width violations in Stage 3 validation |
| 3137c01 | smoke_test.go — nullFISTransport deleted but referenced; replaced with testutil.NewMockFISTransport() |
| 13f392d | Retired stale "DEV bootstrap blocked — John Stevens" open item. Environment has been live all session. |

---

# 2. Current State

## 2.1 What Is Running in DEV

| Service | State | URL / ARN |
| --- | --- | --- |
| One Fintech Stack | UPDATE_COMPLETE | OneFintechDev (CloudFormation) |
| API server | 1/1 ECS tasks, steady state | http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com/docs/ |
| Grafana | Running | http://onefintech-dev-grafana-445593983.us-east-1.elb.amazonaws.com |
| Aurora (DEV) | Running | onefintech-dev-proxy.proxy-cehsk0igwsbz.us-east-1.rds.amazonaws.com:5432 |
| ingest-task | ECS Fargate, triggered by S3 events | cluster: onefintech-dev |
| CI/CD | Both green — CI #144, Build and Deploy #118 | Walker-Morse/Batch |

## 2.2 Gap Status

| Status | Item | Notes |
| --- | --- | --- |
| ✓ DONE | All 7 pipeline stages | Implemented, tested, deployed |
| ✓ DONE | Card & Member API | 8 endpoints, mock FIS, Swagger UI live |
| ✓ DONE | Two-layer idempotency | Option C — UUID + composite, DDL written |
| ✓ DONE | DEV environment | Live in account 307871782435 — was never actually blocked |
| ✓ DONE | CI/CD pipeline | Tests → build-ingest + build-api → CDK deploy, both green |
| ✓ DONE | Grafana dashboards | 5 dashboards, drill-through, Morse branding |
| ✓ DONE | CorrelationID/IdempotencyKey separation | Clean throughout all layers |
| ⚠ MIGRATION | domain_commands DDL | ALTER TABLE needed on live Aurora before card API uses real DB |
| ○ TODO | Aurora adapter for card API | noopCardRepo/noopCommandRepo stubs in main.go — wire before UAT May 18 |
| ○ TODO | FIS Code Connect real adapter | Pending John Stevens credentials (OI #31) |
| ○ TODO | Cognito JWT verification | X-Dev-Claims bypass only in DEV — real auth before TST |
| ○ TODO | ReplaceCard GET pattern | POST /cards/replace returns 201 no body — new cardId resolution TBD (OI #47) |
| ○ TODO | Integration + e2e tests | Stub repos mean no real DB coverage yet |
| ○ TODO | apl-uploader full implementation | Generator stubbed pending FIS APL File Processing Spec (OI #11) |
| ○ TODO | Load testing | 200K-row scenarios before UAT |
| ○ WATCH | RT60 benefit_period bug | stage7_reconciliation.go uses time.Now() — should read from staged batch_records row |
| ○ WATCH | sanctions_screening | Zero tests, regulatory requirement before RFU July 1 |
| ○ WATCH | DM-01 purse_code mapping | John Stevens — blocks reporting data mart |
| ○ WATCH | DM-03 XTRACT contract | Kendra Williams — blocks purchase/transaction reporting |

---

# 3. How to Start Next Session

| Step | Action |
| --- | --- |
| 1 | `git clone` fresh. Run `make test` — expect 305 passing. |
| 2 (highest priority) | Apply domain_commands migration to Aurora. ALTER TABLE, not drop/recreate — live data in table. Add `idempotency_key UUID`, drop old UNIQUE constraint, add two new constraints per updated DDL. |
| 3 | Wire real Aurora adapter into api-server: replace noopCardRepo + noopCommandRepo stubs in `_cmd/api-server/main.go` with real aurora implementations. |
| 4 | Fix RT60 benefit_period bug in `stage7_reconciliation.go` — `stampFISIdentifiers()` uses `time.Now()` instead of reading `benefit_period` from staged `batch_records` row. 5-file change documented in memory. |
| 5 | Integration test tier: at minimum one test hitting real Aurora via RDS Data API. |
| 6 | OI #31 + #47: when John Stevens provides FIS Code Connect credentials, wire the real adapter and confirm the ReplaceCard GET pattern. |

---

# 4. Repo and Credentials

| Item | Value |
| --- | --- |
| GitHub org | Walker-Morse |
| Repo | Walker-Morse/Batch |
| Branch | main |
| HEAD | 13f392d |
| Go version | 1.25.8 |
| AWS account | 307871782435 (us-east-1) |
| AWS credentials | cdk-deployer — Key ID: AKIAUPLUWSYR3YJ3HZUF (rotated 2026-03-20) |
| GitHub PAT | See Kyle's 1Password vault (Walker-Morse/Batch deploy token) |
| Aurora proxy | onefintech-dev-proxy.proxy-cehsk0igwsbz.us-east-1.rds.amazonaws.com:5432 |
| Aurora DB | onefintech, user: ingest_task |
| Aurora master secret | AuroraClusterSecretD25348DD-8IIFZe5Ng6w8-r4PTV2 |
| ECR: ingest-task | 307871782435.dkr.ecr.us-east-1.amazonaws.com/onefintech-dev/ingest-task |
| ECR: api-server | 307871782435.dkr.ecr.us-east-1.amazonaws.com/onefintech-dev/api-server |
| Grafana ALB | http://onefintech-dev-grafana-445593983.us-east-1.elb.amazonaws.com |
| API server ALB | http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com |
| Swagger UI | http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com/docs/ |
| Grafana datasource UIDs | CW: bfglj9ie589oge, PG: cfglj9j8ghwqob, S3: efglouhbbm680c |
| S3 inbound | onefintech-dev-inbound-raw-placeholder |
| CloudWatch log group | /onefintech/dev/ingest-task, /onefintech/dev/api-server |
| HLD current base | v00.01.33 (OFAC consolidation — sanctions.io single-vendor) |

---

*Morse LLC — Speak Benefits — One Fintech | "Boring technology, radical purpose."*
