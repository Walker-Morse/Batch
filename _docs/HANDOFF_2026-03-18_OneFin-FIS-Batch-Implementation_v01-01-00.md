# One Fintech — FIS Prepaid Sunrise
## Batch ETL Integration — Session Handoff
`HANDOFF_2026-03-18_OneFin-FIS-Batch-Implementation_v01-01-00`

| Field | Value |
|---|---|
| Date | 2026-03-18 |
| Repo | Walker-Morse/Batch (GitHub), branch main |
| Base commit | ee70494 (inherited) |
| This session commits | d1d84778, 1b4dd009, c81dbf55, a883f82b, c4edd83e, ec3367d6, f7609149, e2ad6833, 73e9f81e, ac372acb, 1b821adf |
| Language | Go 1.22 |
| Tests | 60 passing (inherited) + 4 new program lookup unit tests |
| Go-Live Target | June 1, 2026 — RFU programme start July 1, 2026 |

---

## 1. What Was Done This Session

Two critical gaps blocking the first real file run were closed.

### 1.1 Program Lookup Gap — Closed

`processSRG310Row` previously passed a zero UUID as `consumers.program_id`, causing a FK violation on every real file. Four parts:

- **`GetProgramByTenantAndSubprogram`** added to `DomainStateRepo` (`_adapters/aurora/domain_state.go`). Queries `programs WHERE tenant_id=$1 AND fis_subprogram_id=$2 AND is_active=true`.
- **`ProgramLookup` interface** added to `_shared/ports/ports.go` — single-method, allows Stage 3 to be unit-tested without a live DB.
- **`lookupProgram()` method** on `RowProcessingStage` — calls `s.Programs`, memoises in `programCache map[string]uuid.UUID`. At most one DB round-trip per unique subprogram per file.
- **`row.SubprogramID`** (string, e.g. `"26071"`) parsed to `int64` via `strconv.ParseInt` for the RT30 record. `consumer.ProgramID` is now the resolved UUID.
- Unknown subprogram dead-letters the row with `failure_reason=program_lookup_failed(subprogram=X)` — not a file abort.
- `main.go`: `domainStateRepo` passed as both `DomainState` and `Programs` fields. Zero-UUID TODO removed.

### 1.2 FIS Sequence Counter — Implemented

`dbSequenceStore.Next()` previously returned hardcoded `1`. FIS silently discards files with duplicate names (§6.5.5).

- **`_schema/pipeline_state/005_fis_sequence.sql`**: `fis_file_sequence(program_id, sequence_date)` table + `NextFISSequence()` PostgreSQL function. Atomic `INSERT...ON CONFLICT DO UPDATE` — safe under concurrent Fargate tasks.
- **`_adapters/aurora/fis_sequence.go`**: `FISSequenceRepo` implementing `fis_adapter.SequenceStore`. Rejects sequences > 99 (FIS two-digit field limit) with a halt error.
- **`main.go`**: `dbSequenceStore` stub removed. `aurora.NewFISSequenceRepo(pool)` wired in its place.
- **Schema execution order**: run `005_fis_sequence.sql` after `004_dead_letter_store.sql` during DEV provisioning (`programs` FK dependency).

### 1.3 Unit Tests — 4 New Tests

`member_enrollment/pipeline/stage3_program_lookup_test.go` added. `MockProgramLookup`, `MockDeadLetterRepository`, `MockDomainCommandRepository` added to `_shared/testutil/mocks.go`.

| Test | What it verifies |
|---|---|
| `TestLookupProgram_ReturnsKnownProgramID` | Happy path — correct UUID returned |
| `TestLookupProgram_CachesResult` | 5 calls → 1 DB hit; cache absorbs rest |
| `TestLookupProgram_DeadLettersOnUnknownProgram` | Unknown subprogram → dead letter, not file abort |
| `TestLookupProgram_MultipleSubprograms` | Two distinct subprograms → two DB hits, each cached |

Note: end-to-end `processSRG310Row` tests require a live DB (`DomainStateRepo` and `BatchRecordsRepo` are concrete types). Integration tests are a future suite.

---

## 2. Current State

### 2.1 Known Gaps Before First Real File Can Run

| Status | Item | Notes |
|---|---|---|
| ✓ DONE | Program lookup | GetProgramByTenantAndSubprogram wired. Per-row cache. Dead-letters unknown subprogram. |
| ✓ DONE | FIS sequence counter | FISSequenceRepo + NextFISSequence(). Durable, atomic, max-99 guard. |
| ○ TODO | Assembler queries DB | AssembleFile builds valid but empty file. Needs `BatchRecordsRepo.ListStagedByCorrelationID()`. Highest remaining priority. |
| ○ TODO | Real PGP decrypt (Stage 2) | NullPGPDecrypt passthrough. Needs golang.org/x/crypto wired from Secrets Manager. |
| ○ TODO | Real PGP encrypt (Stage 4) | NullPGPEncrypt passthrough. Needs FIS public key from Secrets Manager. |
| ○ TODO | fis_card_id RT37/RT60 | fisCardIDOrEmpty() always returns empty. Correct for first-time enrollments — RT30 must arrive first. |
| ○ TODO | UpdatePurseBalance | Commented TODO in Stage 3. FBO integrity requirement (Addendum I). Must complete before go-live. |
| ○ TODO | DEV AWS environment | No real runs possible. Target Apr 6. |

### 2.2 Pipeline Stage Status

| Status | Stage | Notes |
|---|---|---|
| ✓ DONE | Stage 1 — File Arrival | SHA-256, batch_files write, audit_log. Tested. |
| ✓ DONE | Stage 2 — Validation | PGP passthrough, SRG parse, dead-letter malformed rows. |
| ✓ DONE | Stage 3 — Row Processing | Idempotency gate, RT30/37/60 staging, domain state, program lookup wired. |
| ✓ DONE | Stage 4 — Batch Assembly | FIS assembler, PGP passthrough, S3 write, plaintext delete. |
| ○ TODO | Stage 5 — FIS Transfer | Struct defined, Run() is stub. Apr 27 target. |
| ○ TODO | Stage 6 — Return File Wait | S3 polling, 6h timeout (Open Item #25). Stub. |
| ○ TODO | Stage 7 — Reconciliation | Return file parse, RT99 halt, MCO report. Stub. |

---

## 3. Test Coverage

| Tests | Scope | Notes |
|---|---|---|
| 60 (inherited) | All prior packages | go test clean. |
| +4 new | stage3_program_lookup_test.go | lookupProgram: cache, happy path, dead-letter, multi-subprogram. |
| Not yet | Aurora adapters (integration) | Require live DB. fis_sequence.go, domain_state.go. |
| Not yet | Stages 2/3/4 full run | Need interfaces for BatchRecordsRepo and DomainStateRepo concrete types. |

---

## 4. How to Start Next Session

Read memory edits first. Then:

| Step | Action |
|---|---|
| Step 1 | Clone/pull Walker-Morse/Batch on main. Run `make test` — expect 64 passing. |
| Step 2 | Run `005_fis_sequence.sql` against DEV Aurora (after programs table exists). Verify `SELECT NextFISSequence(program_uuid, current_date)` returns 1. |
| Step 3 (highest priority) | Implement `BatchRecordsRepo.ListStagedByCorrelationID()`. Wire into `AssembleFile` in `assembler_impl.go`. Without this the assembled file is structurally valid but empty. |
| Step 4 (Apr 27 target) | Stages 5–7: `_adapters/sftp/`, `stage5_fis_transfer.go`, `stage6_return_file_wait.go`, `stage7_reconciliation.go`. |
| Step 5 | `make test`. All tests must pass. Add tests before declaring done. |
| Step 6 | Commit. No session ends without a commit. |

---

*Morse LLC — Speak Benefits — One Fintech | "Boring technology, radical purpose."*
