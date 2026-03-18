# One Fintech — FIS Prepaid Sunrise
## Batch ETL Integration — Session Handoff
`HANDOFF_2026-03-18_OneFin-FIS-Batch-Implementation_v01-07-00`

| Field | Value |
| --- | --- |
| Date | 2026-03-18 |
| Repo | Walker-Morse/Batch (GitHub), branch main |
| Commit this session | 1d6a4ca (stages 5-7 + FBO reconciler + purse balance) |
| Prior commits | d338038, 993bd5e, e5129b3, 545e4ca |
| Language | Go 1.25.8 |
| Test baseline | 122 passing → 168 passing (+46 new) |
| Go-Live Target | June 1, 2026 — RFU programme start July 1, 2026 |

---

# 1. What Was Done This Session

Two parallel streams landed in one commit (`1d6a4ca`): completion of the FBO sub-ledger integrity requirement from Addendum I, and full implementation of pipeline Stages 5–7.

## 1.1 Stage 3 — UpdatePurseBalance (§I.2 FBO Integrity)

The `UpdatePurseBalance` TODO outstanding since the Stage 3 sprint is resolved.

| Item | Detail |
| --- | --- |
| `GetPurseByConsumerAndBenefitPeriod` | New method on `aurora.DomainStateRepo` — resolves purse ID for the RT60 balance update |
| `SumPlatformLiabilityCents` | Sums active/pending purse balances for daily FBO reconciliation |
| `SumPendingCommandsCents` | Sums Accepted LOAD/SWEEP commands as provisional liabilities |
| `DomainStateWriter` interface | Extended with `GetPurseByConsumerAndBenefitPeriod` + `UpdatePurseBalance` |
| Balance semantics | AT01 LOAD: `balance += AmountCents`. AT30 SWEEP: `balance = 0`. Both dead-letter on failure. |
| `MockDomainStateWriter` | Extended: purse store, `RegisterPurse`, `BalanceUpdates` slice, error injection |
| New tests | 5: load adds, sweep zeroes, purse not found, DB error, PHI not in `failure_reason` |

## 1.2 FBO Reconciliation (Addendum I)

`fbo_reconciliation/reconciler.go` fully implemented. `FBOLiabilityReader` narrow interface defined in-package (no aurora import cycle).

| Behaviour | Detail |
| --- | --- |
| TotalLiability | `PlatformLiabilityCents + PendingCommandsCents` vs FBO closing balance |
| IsMatch | `true` when `|discrepancy| <= DiscrepancyThresholdCents` (default 0 until Open Item #44) |
| Audit | Written regardless of outcome. Discrepancy → DISCREPANCY escalation message. |
| Audit write failure | Non-fatal for result but surfaces as `COMPLIANCE ALERT` error |
| Tests | 9: happy path, zero liability, total = sum, discrepancy, threshold, query failures, audit failure, date truncation |

## 1.3 Stage 5 — FIS Transfer

`FISTransferStage.Run()` implemented. Fetches PGP-encrypted file from S3 fis-exchange via `BatchAssemblyResult.S3Key`. Delivers via `FISTransport.Deliver`. Transitions `ASSEMBLED → TRANSFERRED`. Error logged and returned on delivery failure without status advance. 4 tests.

## 1.4 Stage 6 — Return File Wait

`ReturnFileWaitStage.Run()` implemented. Blocks on `FISTransport.PollForReturn` up to configurable `Timeout` (default 6h — Open Item #25). On timeout: `TRANSFERRED → STALLED`, `dead.letter.alert` emitted, audit written. Returns `ReturnFileWaitResult{Body io.ReadCloser}` to Stage 7. 4 tests.

## 1.5 Stage 7 — Reconciliation

`ReconciliationStage.Run()` fully implemented.

| Component | Detail |
| --- | --- |
| `ParseReturnFile` | 400-byte fixed-width return file parser in `fis_adapter/assembler.go` |
| `IsRT99Halt` | Full-file pre-processing halt: exactly one record AND it is RT99 (§6.5.1) |
| `reconcileRecord` | `batch_records` + `domain_commands` updated per return record |
| RT30 success | Consumer: `FISPersonID` + `FISCUID`. Card: `FISCardID` + `issued_at`. |
| RT60 success | Purse: `FISPurseNumber` stamped. BenefitPeriod derived from current month (Phase 1). |
| `handleFullFileHalt` | `TRANSFERRED → HALTED`, dead letter written, `batch.halt.triggered` P0 alert |
| Non-fatal record errors | Log `stage7.record_reconcile_error`, continue — file reaches `COMPLETE` |
| `TRANSFERRED → COMPLETE` | Final transition on success. Audit entry with completed/failed counts. |
| Tests | 8: RT30 happy path, RT60 purse number, individual RT99, full-file halt, non-fatal error, audit on complete, domain command status, parse failure |

## 1.6 New Mocks and Aurora Methods

| Item | Location |
| --- | --- |
| `MockFISTransport` | `_shared/testutil/mocks.go` |
| `MockBatchRecordsReconciler` | `_shared/testutil/mocks.go` |
| `MockDomainStateReconciler` | `_shared/testutil/mocks.go` |
| `MockMartWriter` | `_shared/testutil/mocks.go` |
| `MockDomainCommandRepository.StatusUpdates` | `_shared/testutil/mocks.go` |
| `GetStagedByCorrelationAndSequence` | `_adapters/aurora/batch_records.go` |
| `UpdatePurseFISNumber` | `_adapters/aurora/domain_state.go` |
| `GetConsumerByID` | `_adapters/aurora/domain_state.go` |
| `GetCardByConsumerID` | `_adapters/aurora/domain_state.go` |
| `BatchRecordsReconciler` (interface) | `_shared/ports/ports.go` |
| `DomainStateReconciler` (interface) | `fis_reconciliation/pipeline/stage7_reconciliation.go` |

---

# 2. Current Gap Status (Updated)

| Status | Item | Notes |
| --- | --- | --- |
| ✓ DONE | Program lookup | GetProgramByTenantAndSubprogram wired. Per-row cache. |
| ✓ DONE | FIS sequence counter | FISSequenceRepo + NextFISSequence(). Durable, atomic, max-99 guard. |
| ✓ DONE | Assembler queries DB | ListStagedByCorrelationID wired. RT30/RT37/RT60 built into file. |
| ✓ DONE | CDK infrastructure | Full infra/ stack: VPC, Aurora, ECS, S3, IAM, Scheduler, TriggerConstruct. |
| ✓ DONE | Real PGP decrypt/encrypt | LoadDecrypter/LoadEncrypter wired. DEV falls back to NullPGP if ARN empty. |
| ✓ DONE | DEV bootstrap tooling | scripts/bootstrap-dev.sh + oidc-trust-policy.json. Not yet executed. |
| ✓ DONE | Test pyramid — unit tier | 168 tests passing. All stages + fis_sequence + FBO reconciler covered. |
| ✓ DONE | UpdatePurseBalance (§I.2) | FBO sub-ledger updated at RT60 write time. AT01 adds, AT30 zeroes. |
| ✓ DONE | FBO Reconciler (Addendum I) | fbo_reconciliation/reconciler.go fully implemented + 9 tests. |
| ✓ DONE | Stage 5 — FIS Transfer | S3 → FISTransport.Deliver → TRANSFERRED. |
| ✓ DONE | Stage 6 — Return File Wait | PollForReturn with 6h timeout. Timeout → STALLED + dead.letter.alert. |
| ✓ DONE | Stage 7 — Reconciliation | Full return file processing. RT30/RT60 identifiers stamped. RT99 halt handling. |
| ○ TODO | Execute DEV prerequisites | Run bootstrap-dev.sh. Requires AWS admin creds + GitHub secret write access. |
| ○ TODO | Provision PGP secrets | 3 Secrets Manager secrets + ARNs as GitHub secrets (Open Item #41). |
| ○ TODO | DEV smoke test | Drop test SRG310 .pgp into inbound-raw S3. Verify stages 1–7 end-to-end. |
| ○ TODO | Integration + e2e test tier | Pyramid bottom two tiers empty. Min: 1 integration + 1 full pipeline e2e. |
| ○ WATCH | sanctions_screening/* | ofac_screener + sanctions_enroller. Zero tests. Regulatory. Before RFU July 1. |
| ○ WATCH | Stage 7 benefit_period (RT60) | Currently derived from current month. Should come from staged batch record. Phase 2. |

---

# 3. How to Start Next Session

| Step | Action |
| --- | --- |
| 1 | Read memory edits. `git clone` fresh. Run `make test` — expect 168 passing. |
| 2 (highest) | Execute DEV prerequisites: `chmod +x scripts/bootstrap-dev.sh && AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text) ./scripts/bootstrap-dev.sh`. Needs AWS admin creds + GitHub secret write access. |
| 3 | After bootstrap: add `AWS_ACCOUNT_ID` to GitHub secrets. Push commit to trigger pipeline. Verify GitHub Actions runs clean. |
| 4 | Provision 3 PGP Secrets Manager secrets. Add ARNs: `PGP_PRIVATE_KEY_SECRET_ARN`, `PGP_PASSPHRASE_SECRET_ARN`, `PGP_FIS_PUBLIC_KEY_SECRET_ARN`. (Open Item #41) |
| 5 | DEV smoke test: drop a test SRG310 `.pgp` into inbound-raw S3. Verify stages 1–7 complete. |
| 6 | Integration + e2e test tier. Minimum: one integration test hitting Aurora + one full pipeline e2e after smoke test passes. |
| 7 | `sanctions_screening` coverage — `ofac_screener` (62L) + `sanctions_enroller` (25L). Required before RFU July 1. |
| 8 | Stage 7 RT60 benefit_period fix: carry from staged batch record, not current month. Phase 2. |

---

# 4. Repo and Credentials

| Item | Value |
| --- | --- |
| GitHub org | Walker-Morse |
| Repo | Walker-Morse/Batch |
| Branch | main |
| Go version | 1.25.8 |
| Canonical source | GitHub (not GitLab — this engagement predates REL_DEL GitLab convention) |
| AWS CDK | infra/ (TypeScript, aws-cdk-lib ^2.130.0) |
| ECR repo | onefintech-dev/ingest-task |
| PGP library | golang.org/x/crypto/openpgp v0.49.0 (direct dependency) |
| OIDC role | github-actions-onefintech-dev (created by bootstrap-dev.sh) |
| IAM deploy policy | OneFintechDevGitHubDeployPolicy (created by bootstrap-dev.sh) |

*Morse LLC — Speak Benefits — One Fintech | "Boring technology, radical purpose."*
