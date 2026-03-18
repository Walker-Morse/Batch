# One Fintech — FIS Prepaid Sunrise
## Batch ETL Integration — Session Handoff
`HANDOFF_2026-03-18_OneFin-FIS-Batch-Implementation_v01-04-00`

| Field | Value |
| --- | --- |
| Date | 2026-03-18 |
| Repo | Walker-Morse/Batch (GitHub), branch main |
| Commits this session | 545e4ca (S3 trigger + PGP adapters) |
| Prior commits | 778e29f, 7f8c3b2b, 6f3799d2 |
| Language | Go 1.25.8 |
| Test baseline | 64 passing (unchanged — new PGP tests added, not yet run in CI) |
| Go-Live Target | June 1, 2026 — RFU programme start July 1, 2026 |

---

# 1. What Was Done This Session

One commit: `545e4ca` — S3 file-arrival trigger construct (CDK) and real PGP decrypt/encrypt adapters (Go).

## 1.1 TriggerConstruct — S3 → EventBridge → ECS RunTask (ADR-006b)

**New file:** `infra/lib/constructs/trigger.ts`

| Item | Detail |
| --- | --- |
| EventBridge rule | S3 ObjectCreated on inbound-raw bucket, scoped to `.pgp` suffix only |
| Target | ECS RunTask (Fargate) via `targets.EcsTask` |
| Container overrides | `S3_BUCKET` and `S3_KEY` injected from EventBridge event fields |
| IAM role | `onefintech-{env}-s3-trigger-role`: `ecs:RunTask` + `iam:PassRole` (task + execution roles) |
| CORRELATION_ID / TENANT_ID | Generated inside the task from S3_KEY at runtime (not injected by EB) |
| Stack wiring | `TriggerConstruct` added after `SchedulerConstruct` in `onefintech-stack.ts` |
| PGP props | `pgpPrivateKeySecretArn`, `pgpPassphraseSecretArn`, `pgpFisPublicKeySecretArn` added to stack interface |

## 1.2 PGP Adapters — `_adapters/pgp/`

**Four new files:** `decrypt.go`, `encrypt.go`, `secrets.go`, `pgp_test.go`

| File | What It Does |
| --- | --- |
| `decrypt.go` | `LoadDecrypter`: loads ASCII-armored private key + optional passphrase from Secrets Manager. `Decrypt(io.Reader)` satisfies `ValidationStage.PGPDecrypt`. `NewDecrypterFromEntity` for tests. |
| `encrypt.go` | `LoadEncrypter`: loads FIS public key from Secrets Manager. `Encrypt(io.Reader)` satisfies `BatchAssemblyStage.PGPEncrypt`. Binary (non-armored) output. `NewEncrypterFromEntity` for tests. |
| `secrets.go` | Shared `getSecret` helper: Secrets Manager `GetSecretValue`, returns `SecretString`. |
| `pgp_test.go` | 6 unit tests: round-trip, wrong-key error, empty-input error, large payload (400KB), non-empty output, ciphertext != plaintext. Uses generated test keys (no Secrets Manager). |

## 1.3 main.go Wiring (`_cmd/ingest-task/`)

`NullPGPDecrypt` and `NullPGPEncrypt` stubs replaced with real adapter loading at startup.

| Change | Detail |
| --- | --- |
| New `PipelineConfig` fields | `PGPPrivateKeySecretARN`, `PGPPassphraseSecretARN`, `PGPFISPublicKeySecretARN` (env vars + flags) |
| Startup guard | Non-DEV env with empty ARN calls `log.Fatalf`. DEV with empty ARN logs WARN + falls back to NullPGP (backwards-compatible). |
| Secrets Manager client | `smClient := secretsmanager.NewFromConfig(awsCfg)` — shared by both adapters |
| `go.mod` | `golang.org/x/crypto v0.49.0` promoted from indirect to direct |
| IAM (`iam.ts`) | `SecretsManagerRead` policy expanded to include PGP secret ARNs (passphrase filtered if empty) |
| `app.ts` | Three PGP props passed from env vars (empty string = DEV NullPGP fallback) |

---

# 2. Current Gap Status (Updated)

| Status | Item | Notes |
| --- | --- | --- |
| ✓ DONE | Program lookup | GetProgramByTenantAndSubprogram wired. Per-row cache. Dead-letters unknown subprogram. |
| ✓ DONE | FIS sequence counter | FISSequenceRepo + NextFISSequence(). Durable, atomic, max-99 guard. |
| ✓ DONE | Assembler queries DB | ListStagedByCorrelationID wired. RT30/RT37/RT60 records queried and built into file. |
| ✓ DONE | AssemblerImpl unit tests | 8 tests: RT30/RT60, sequence order, 400-byte width, record count, lister error. |
| ✓ DONE | CDK infrastructure | Full infra/ stack: VPC, Aurora, ECS, S3, IAM, Scheduler. Dockerfile + CI/CD pipeline. |
| ✓ DONE | S3 trigger construct (ADR-006b) | TriggerConstruct wired. EventBridge S3 ObjectCreated → ECS RunTask. |
| ✓ DONE | Real PGP decrypt (Stage 2) | LoadDecrypter wired at startup. DEV falls back to NullPGP if ARN empty. |
| ✓ DONE | Real PGP encrypt (Stage 4) | LoadEncrypter wired at startup. DEV falls back to NullPGP if ARN empty. |
| ○ TODO | 4 DEV prerequisites | AWS account ID, OIDC role, CDK bootstrap, schema migrations. (S3→ECS trigger now done in CDK.) |
| ○ TODO | Provision PGP secrets | Create 3 Secrets Manager secrets, store ARNs in GitHub secrets / CDK env vars (Open Item #41). |
| ○ TODO | fis_card_id RT37/RT60 | fisCardIDOrEmpty() always returns empty. Correct for first-time enrollments — RT30 must arrive first. |
| ○ TODO | UpdatePurseBalance | Stage 3 TODO. FBO integrity requirement (Addendum I). Must complete before go-live. |
| ○ TODO | Stages 5–7 | FIS SFTP transfer, return file wait, reconciliation. Apr 27 target. |

---

# 3. How to Start Next Session

| Step | Action |
| --- | --- |
| 1 | Read memory edits. Pull Walker-Morse/Batch main. Run `make test` — expect 64 passing. |
| 2 (highest) | Complete the 4 remaining DEV prerequisites: AWS account ID, OIDC role, CDK bootstrap, schema migrations. S3 trigger is now done in CDK. |
| 3 | Provision 3 PGP Secrets Manager secrets (private key, passphrase, FIS public key). Add ARNs to GitHub secrets as `PGP_PRIVATE_KEY_SECRET_ARN`, `PGP_PASSPHRASE_SECRET_ARN`, `PGP_FIS_PUBLIC_KEY_SECRET_ARN`. |
| 4 | Deploy CDK. Drop a test SRG310 `.pgp` file into inbound-raw S3. Verify: `batch_files` row created, stages 1–4 complete, assembled file in fis-exchange. DEV smoke test. |
| 5 | Implement `UpdatePurseBalance` (Stage 3). FBO integrity requirement (Addendum I). Must be complete before go-live. |
| 6 (Apr 27) | Implement Stages 5–7: `_adapters/sftp/` (AWS Transfer Family), `stage5_fis_transfer.go`, `stage6_return_file_wait.go`, `stage7_reconciliation.go`. |

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

*Morse LLC — Speak Benefits — One Fintech | "Boring technology, radical purpose."*
