# One Fintech — FIS Prepaid Sunrise
## Batch ETL Integration — Session Handoff
`HANDOFF_2026-03-18_OneFin-FIS-Batch-Implementation_v01-06-00`

| Field | Value |
| --- | --- |
| Date | 2026-03-18 |
| Repo | Walker-Morse/Batch (GitHub), branch main |
| Commits this session | 993bd5e (test pyramid — stage2/3/4 unit tests + fis_sequence validation) |
| Prior commits | e5129b3 (handoff v01-04-00), 545e4ca (S3 trigger + PGP), 778e29f, 7f8c3b2b |
| Language | Go 1.25.8 |
| Test baseline | 64 passing → 122 passing (+58 new) |
| Go-Live Target | June 1, 2026 — RFU programme start July 1, 2026 |

---

# 1. What Was Done This Session

Two sessions of work landed in one commit (`993bd5e`).

**Session A (v01-05-00 — not separately committed):** DEV bootstrap tooling: `scripts/bootstrap-dev.sh` and `scripts/oidc-trust-policy.json`. These were produced but Kyle has not yet executed them. See Section 3 Step 2.

**Session B (v01-06-00 — this session):** Test pyramid gap analysis and test implementation.

## 1.1 Gap Analysis

Identified that 60% of production files had zero test coverage and the pyramid had no integration or e2e tier at all. Highest-risk gaps before go-live:

| Priority | File | Lines | Risk |
| --- | --- | --- | --- |
| Critical | `member_enrollment/pipeline/stage3_row_processing.go` | 613 | Core enrollment logic — zero tests |
| Critical | `member_enrollment/pipeline/stage2_validation.go` | 175 | Pipeline entry point — zero tests |
| Critical | `fis_reconciliation/pipeline/stage4_batch_assembly.go` | 136 | Assembly orchestration — zero tests |
| High | `_adapters/aurora/fis_sequence.go` | 69 | Durable counter — max-99 guard untested |
| High | `fbo_reconciliation/reconciler.go` | 50 | FBO integrity — all TODOs, untested |
| High | `sanctions_screening/*` | 87 | Regulatory surface — zero tests |

## 1.2 Interface Extraction (stage3_row_processing.go)

`RowProcessingStage` previously held `*aurora.BatchRecordsRepo` and `*aurora.DomainStateRepo` as concrete types. Unit testing without a live DB was impossible.

Extracted two narrow interfaces (defined in the pipeline package to avoid an import cycle with aurora):

```go
type BatchRecordWriter interface {
    InsertRT30(ctx context.Context, rec *aurora.BatchRecordRT30) error
    InsertRT37(ctx context.Context, rec *aurora.BatchRecordRT37) error
    InsertRT60(ctx context.Context, rec *aurora.BatchRecordRT60) error
}

type DomainStateWriter interface {
    UpsertConsumer(ctx context.Context, c *domain.Consumer) error
    InsertCard(ctx context.Context, c *domain.Card) error
    GetConsumerByNaturalKey(ctx context.Context, tenantID, clientMemberID string) (*domain.Consumer, error)
}
```

`aurora.BatchRecordsRepo` and `aurora.DomainStateRepo` satisfy both interfaces structurally — no changes to those types. Production wiring is unchanged.

## 1.3 New Mocks (_shared/testutil/mocks.go)

| Mock | Implements | Notes |
| --- | --- | --- |
| `MockBatchRecordWriter` | `pipeline.BatchRecordWriter` | Captures RT30/RT37/RT60 inserts; per-type error injection |
| `MockDomainStateWriter` | `pipeline.DomainStateWriter` | In-memory consumer/card store; `RegisterConsumer` for pre-seeding |
| `MockFISBatchAssembler` | `ports.FISBatchAssembler` | Returns configurable `AssembledFile`; captures `AssembleRequest` calls |
| `MockFileStore` (extended) | `ports.FileStore` | Added `GetObjectFn` injection hook for stage2 testing |

## 1.4 New Test Files (58 tests total)

### stage2_validation_test.go (12 tests)

| Test | What It Verifies |
| --- | --- |
| `TestValidation_SRG310_ParsesRows` | Happy path: rows parsed, status→VALIDATING, SHA-256 non-empty |
| `TestValidation_SRG315_Parsed` | SRG315 file type routed correctly |
| `TestValidation_SRG320_Parsed` | SRG320 file type routed correctly |
| `TestValidation_RecordCount_SetAfterParsing` | `SetRecordCount` called with total rows (Stage 3 stall detection dependency) |
| `TestValidation_SHA256_IsPlaintextHash` | SHA matches independent computation (§6.1 non-repudiation) |
| `TestValidation_PGPDecryptFails_BatchHalted` | PGP failure → HALTED, error returned |
| `TestValidation_UnknownFileType_BatchHalted` | Unknown file type → HALTED |
| `TestValidation_MalformedRow_DeadLettered` | Parse errors → dead-lettered, `malformed_count` incremented |
| `TestValidation_S3GetObjectFails_ReturnsError` | S3 failure → HALTED |
| `TestNullPGPDecrypt_Passthrough` | NullPGP returns identical bytes |

### stage3_row_processing_test.go (27 tests)

| Test | What It Verifies |
| --- | --- |
| `TestRun_SRG310_HappyPath` | RT30 + consumer + card written, status→PROCESSING, StagedCount=1 |
| `TestRun_MultipleRows_AllStaged` | 3 rows → StagedCount=3, 3 RT30 records |
| `TestRun_DuplicateRow_Skipped` | Pre-existing command → DuplicateCount=1, no re-staging |
| `TestRun_FailedRow_TriggersStall` | Failed row → Stalled=true, status→STALLED |
| `TestRun_MixedRows_PartialSuccess` | 2 good + 1 bad → StagedCount=2, FailedCount=1, Stalled=true |
| `TestSRG310Row_DomainCommandInsertFails_DeadLettered` | DB failure → dead letter, no RT30 written |
| `TestSRG310Row_RT30InsertFails_DeadLettered` | RT30 DB failure → dead letter |
| `TestSRG310Row_ConsumerUpsertFails_DeadLettered` | Consumer DB failure → dead letter |
| `TestSRG310Row_DeadLetter_PHINotInReason` | PHI values never appear in `failure_reason` (§6.5.2, §7.2) |
| `TestSRG310Row_CorrelationTracingIntact` | `correlation_id` + `row_sequence_number` on dead letter for replay-cli |
| `TestSRG315Row_UnknownEventType_DeadLettered` | Unrecognised event type → dead letter |
| `TestSRG315Row_ConsumerNotFound_DeadLettered` | RT37 without prior consumer → dead letter |
| `TestSRG315Row_MissingFISCardID_DeadLettered` | RT37 without fis_card_id → dead letter |
| `TestSRG320Row_UnknownCommandType_DeadLettered` | Unrecognised SRG320 command → dead letter |
| `TestSRG320Row_ConsumerNotFound_DeadLettered` | RT60 without prior consumer → dead letter |
| `TestSRG320Row_MissingFISCardID_DeadLettered` | RT60 without fis_card_id → dead letter |
| `TestSRGEventToCommandType` | All 5 known + unknown event types |
| `TestSRG320CommandTypeToCommand` | All 5 known + unknown command types |
| `TestCommandTypeToCardStatus` | All known types + safe default |
| `TestFISCardIDOrEmpty` | Always returns "" pre-Stage-7 |
| `TestStrPtrIfNotEmpty` / `TestTimePtrIfNotZero` | Edge cases |

### stage4_batch_assembly_test.go (10 tests)

| Test | What It Verifies |
| --- | --- |
| `TestStage4_HappyPath` | Assembler called, result returned, status→ASSEMBLED |
| `TestStage4_S3KeyContainsTenantAndFilename` | fis-exchange key namespaced by tenant + filename |
| `TestStage4_AssemblerCalledWithCorrelationID` | `AssembleRequest` carries correct correlation_id and tenant_id |
| `TestStage4_StagedObjectDeleted` | Plaintext staged S3 object deleted after encrypt+write (§5.4.3) |
| `TestStage4_AssemblerError_Propagates` | Assembler failure → error, no S3 write, no ASSEMBLED |
| `TestStage4_PGPEncryptError_Propagates` | PGP failure → error, no S3 write |
| `TestStage4_S3PutError_Propagates` | S3 write failure → error, no ASSEMBLED |
| `TestStage4_StagedDeleteFailure_NonFatal` | Delete failure → logged as ERROR but not fatal (lifecycle backstop) |
| `TestNullPGPEncrypt_Passthrough` | NullPGP returns identical bytes |

### fis_sequence_test.go (9 tests)

| Test | What It Verifies |
| --- | --- |
| `TestFISSequence_InvalidUUID_ReturnsError` | Malformed programID rejected before DB call |
| `TestFISSequence_ValidUUID_NoError` | Well-formed UUID passes validation |
| `TestFISSequence_Max99_ExactlyAtLimit_Valid` | seq=99 accepted (FIS max) |
| `TestFISSequence_Max99_ExceedsLimit_Error` | seq=100 rejected with ops-actionable error message |
| `TestFISSequence_Max99_Boundary` | seq 1–99 valid, 100+ rejected |
| `TestFISSequence_DateTruncation_NonMidnight` | 14:30 UTC truncated to midnight |
| `TestFISSequence_DateTruncation_AlreadyMidnight` | Midnight unchanged |
| `TestFISSequence_DateTruncation_LocalTimezone` | Local timezone converted to UTC before truncation |

---

# 2. Current Gap Status (Updated)

| Status | Item | Notes |
| --- | --- | --- |
| ✓ DONE | Program lookup | GetProgramByTenantAndSubprogram wired. Per-row cache. Dead-letters unknown subprogram. |
| ✓ DONE | FIS sequence counter | FISSequenceRepo + NextFISSequence(). Durable, atomic, max-99 guard. |
| ✓ DONE | Assembler queries DB | ListStagedByCorrelationID wired. RT30/RT37/RT60 queried and built into file. |
| ✓ DONE | CDK infrastructure | Full infra/ stack: VPC, Aurora, ECS, S3, IAM, Scheduler, TriggerConstruct. |
| ✓ DONE | Real PGP decrypt/encrypt | LoadDecrypter/LoadEncrypter wired. DEV falls back to NullPGP if ARN empty. |
| ✓ DONE | DEV bootstrap tooling | scripts/bootstrap-dev.sh + oidc-trust-policy.json. Not yet executed. |
| ✓ DONE | Test pyramid — unit tier | 122 tests passing. stage2/3/4 + fis_sequence now covered. |
| ○ TODO | Execute DEV prerequisites | Run bootstrap-dev.sh. Requires AWS admin credentials + GitHub secret write access. |
| ○ TODO | Provision PGP secrets | Create 3 Secrets Manager secrets, store ARNs as GitHub secrets (Open Item #41). |
| ○ TODO | DEV smoke test | Drop test SRG310 .pgp into inbound-raw S3. Verify full pipeline end-to-end. |
| ○ TODO | UpdatePurseBalance | Stage 3 TODO (line 481). FBO integrity requirement (Addendum I). Must complete before go-live. |
| ○ TODO | fbo_reconciliation/reconciler.go | All TODOs. Go-live requirement. Zero tests (integration test needed once implemented). |
| ○ TODO | Stages 5–7 | FIS SFTP transfer, return file wait, reconciliation. Apr 27 target. |
| ○ TODO | Integration + e2e test tier | Pyramid bottom two tiers are empty. Minimum: one integration test hitting Aurora + one full pipeline e2e after DEV smoke test passes. |
| ○ WATCH | sanctions_screening/* | ofac_screener (62L) + sanctions_enroller (25L). Zero tests. Regulatory surface. Not on critical path for June 1 but needs coverage before RFU July 1. |

---

# 3. How to Start Next Session

| Step | Action |
| --- | --- |
| 1 | Read memory edits. `git clone` fresh. Run `make test` — expect 122 passing. |
| 2 (highest) | Execute DEV prerequisites: `chmod +x scripts/bootstrap-dev.sh && AWS_ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text) ./scripts/bootstrap-dev.sh`. Needs AWS admin creds + GitHub secret write access. |
| 3 | After bootstrap: add `AWS_ACCOUNT_ID` to GitHub secrets. Push commit to trigger pipeline. Verify GitHub Actions runs clean. |
| 4 | Provision 3 PGP Secrets Manager secrets. Add ARNs to GitHub secrets: `PGP_PRIVATE_KEY_SECRET_ARN`, `PGP_PASSPHRASE_SECRET_ARN`, `PGP_FIS_PUBLIC_KEY_SECRET_ARN`. (Open Item #41) |
| 5 | DEV smoke test: drop a test SRG310 `.pgp` into inbound-raw S3. Verify: batch_files row created, stages 1–4 complete, assembled file in fis-exchange. |
| 6 | Implement `UpdatePurseBalance` (Stage 3, line 481). FBO integrity (Addendum I). Mandatory before go-live. |
| 7 (Apr 27) | Stages 5–7: `_adapters/sftp/` (AWS Transfer Family), `stage5_fis_transfer.go`, `stage6_return_file_wait.go`, `stage7_reconciliation.go`. |

---

# 4. Repo & Credentials

| Item | Value |
| --- | --- |
| GitHub org | Walker-Morse |
| Repo | Walker-Morse/Batch |
| Branch | main |
| PAT | REDACTED_SEE_KYLE |
| Go version | 1.25.8 |
| Canonical source | GitHub (not GitLab — this engagement predates REL_DEL GitLab convention) |
| AWS CDK | infra/ (TypeScript, aws-cdk-lib ^2.130.0) |
| ECR repo | onefintech-dev/ingest-task |
| PGP library | golang.org/x/crypto/openpgp v0.49.0 (direct dependency) |
| OIDC role | github-actions-onefintech-dev (created by bootstrap-dev.sh) |
| IAM deploy policy | OneFintechDevGitHubDeployPolicy (created by bootstrap-dev.sh) |

*Morse LLC — Speak Benefits — One Fintech | "Boring technology, radical purpose."*
