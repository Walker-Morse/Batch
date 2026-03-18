# One Fintech — FIS Prepaid Sunrise
## Batch ETL Integration — Session Handoff
`HANDOFF_2026-03-18_OneFin-FIS-Batch-Implementation_v01-03-00`

| Field | Value |
| --- | --- |
| Date | 2026-03-18 |
| Repo | Walker-Morse/Batch (GitHub), branch main |
| Prior commits | 102b6f87, dcf6f44a (last session) |
| This session commits | 6f3799d2 (Block 1 — tests), 7f8c3b2b (Block 2 — CDK) |
| Language | Go 1.25.8 |
| Test baseline | 64 passing (unchanged — new tests added, not yet run in CI) |
| Go-Live Target | June 1, 2026 — RFU programme start July 1, 2026 |

---

# 1. What Was Done This Session

Two blocks completed: AssemblerImpl unit tests (highest priority gap per previous handoff) and the complete AWS CDK infrastructure stack enabling the first DEV deployment.

## 1.1 AssemblerImpl Unit Tests (commit 6f3799d2)

`MockBatchRecordsLister` added to `_shared/testutil/mocks.go`. Eight tests in `fis_reconciliation/fis_adapter/assembler_impl_test.go`:

| Test | What It Verifies |
| --- | --- |
| TestAssembleFile_EmptyStagedRecords | Empty correlation ID produces valid RT10/RT20/RT80/RT90 file (idempotency) |
| TestAssembleFile_RT30RecordsAppear | RT30 present with ClientMemberID and demographic fields |
| TestAssembleFile_RT30SequenceOrder | Multiple RT30 records in sequence_in_file ASC order |
| TestAssembleFile_RT60RecordsAppear | RT60 fund load records appear with AT code |
| TestAssembleFile_RecordCountIntegrity | RecordCount matches RT10+RT20+data+RT80+RT90 — mismatch triggers RT99 halt |
| TestAssembleFile_EachRecordIs400Bytes | Every CRLF-delimited record is exactly 400 bytes (§6.6.2) |
| TestAssembleFile_FilenameFormat | Filename has .txt suffix and program ID prefix (§6.6.1) |
| TestAssembleFile_ListerErrorPropagates | BatchRecordsLister error surfaces correctly |

## 1.2 CDK Infrastructure Stack (commit 7f8c3b2b)

Complete `infra/` CDK TypeScript project from scratch — zero infrastructure existed before this session. Per ADR-005 construct layout:

| Construct | What It Provisions |
| --- | --- |
| networking.ts | VPC, 2 AZs, 1 NAT gateway (DEV), VPC endpoints: S3 gateway, Secrets Manager, ECR, ECR Docker, CloudWatch Logs |
| storage.ts | KMS key (auto-rotate), 3 S3 buckets (inbound-raw, staged, fis-exchange): SSE-KMS, versioning, access logs, lifecycle rules (24h staged expiry §5.4.3, 7yr fis-exchange), EventBridge enabled on inbound-raw |
| aurora.ts | Aurora PostgreSQL Serverless v2 (15.4), RDS Proxy, explicit ACU min 0.5 / max 4 (DEV), isolated subnets, deletion protection off (DEV) |
| iam.ts | Task role (least-privilege §5.4.5: GetObject-only on inbound-raw, full CRUD on staged, PutObject/GetObject on fis-exchange), execution role, scheduler role |
| ecs.ts | ECR repository, CloudWatch log group (6yr HIPAA retention), ECS cluster with Container Insights, Fargate task def 1 vCPU/2 GB, task security group |
| scheduler.ts | EventBridge Scheduler nightly delivery gate (02:00 UTC, 15-min flex window, ADR-007) |

Also committed: `Dockerfile` (multi-stage Go 1.25.8 build → scratch runtime), `.github/workflows/deploy.yml` (test → ECR push → CDK deploy pipeline), `ADR-006b` (S3 event notification trigger pattern).

---

# 2. Prerequisites Before First DEV Deploy

The CDK stack is complete but cannot deploy until these five steps are done. None require code changes.

| # | Step | How |
| --- | --- | --- |
| 1 | AWS account ID | Add `AWS_ACCOUNT_ID` to GitHub repo secrets (Settings → Secrets → Actions) |
| 2 | OIDC role | Create IAM role `github-actions-onefintech-dev` in AWS account with OIDC trust to GitHub Actions and AdministratorAccess (tighten before TST) |
| 3 | CDK bootstrap | `cd infra && npm ci && npx cdk bootstrap aws://ACCOUNT/us-east-1` |
| 4 | Schema migrations | After first CDK deploy: run `_schema/*.sql` against Aurora via psql using `AuroraProxyEndpoint` from CDK output |
| 5 | S3 → ECS trigger | Wire EventBridge rule: S3 ObjectCreated on inbound-raw → ECS RunTask (ADR-006b TODO — add EventBridgeRule construct next session) |

---

# 3. Current Gap Status (Updated)

| Status | Item | Notes |
| --- | --- | --- |
| ✓ DONE | Program lookup | GetProgramByTenantAndSubprogram wired. Per-row cache. Dead-letters unknown subprogram. |
| ✓ DONE | FIS sequence counter | FISSequenceRepo + NextFISSequence(). Durable, atomic, max-99 guard. |
| ✓ DONE | Assembler queries DB | ListStagedByCorrelationID wired. RT30/37/60 records queried and built into file. |
| ✓ DONE | AssemblerImpl unit tests | 8 tests covering RT30/RT60, sequence order, 400-byte width, record count, lister error propagation. |
| ✓ DONE | CDK infrastructure | Full infra/ stack: VPC, Aurora, ECS, S3, IAM, Scheduler. Dockerfile + CI/CD pipeline. |
| ○ TODO | 5 prerequisites above | Account ID, OIDC role, CDK bootstrap, schema migration, S3→ECS trigger rule. |
| ○ TODO | Real PGP decrypt (Stage 2) | NullPGPDecrypt passthrough. Wire golang.org/x/crypto from Secrets Manager. |
| ○ TODO | Real PGP encrypt (Stage 4) | NullPGPEncrypt passthrough. Wire FIS public key from Secrets Manager. |
| ○ TODO | fis_card_id RT37/RT60 | fisCardIDOrEmpty() always returns empty. Correct for first-time enrollments — RT30 must arrive first. |
| ○ TODO | UpdatePurseBalance | Stage 3 TODO. FBO integrity requirement (Addendum I). Must complete before go-live. |
| ○ TODO | Stages 5–7 | FIS SFTP transfer, return file wait, reconciliation. Apr 27 target. |

---

# 4. How to Start Next Session

| Step | Action |
| --- | --- |
| 1 | Read memory edits. Pull Walker-Morse/Batch main. Run `make test` — expect 64 passing. |
| 2 (highest) | Complete the 5 DEV prerequisites in Section 2. Get CDK deployed. Get a green pipeline run. |
| 3 | Wire S3 ObjectCreated → EventBridge → ECS RunTask in CDK (ADR-006b TODO). Add EventBridgeRule construct to StorageConstruct or a new trigger.ts construct. |
| 4 | Drop a test SRG310 file into inbound-raw S3. Verify: batch_files row created, stages 1–4 complete, assembled file in fis-exchange. DEV smoke test. |
| 5 | Implement real PGP decrypt (Stage 2): wire golang.org/x/crypto from Secrets Manager. Store private key as Secrets Manager secret, add ARN to task role policy in iam.ts. |
| 6 | Implement real PGP encrypt (Stage 4): wire FIS public key. Store as Secrets Manager secret, add ARN to task role policy. |
| 7 (Apr 27) | Implement Stages 5–7: `_adapters/sftp/` (AWS Transfer Family), stage5_fis_transfer.go, stage6_return_file_wait.go, stage7_reconciliation.go. |

---

# 5. Repo & Credentials

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

*Morse LLC — Speak Benefits — One Fintech | "Boring technology, radical purpose."*
