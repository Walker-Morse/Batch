# One Fintech — UAT Manual Test Guide

**Version:** 1.0  
**Environment:** DEV (UAT window opens TST May 18, 2026)  
**Audience:** John Dennis (Engineering), Kendra Williams (Operations & Client Success)  
**Contact:** Kyle Walker (Principal Solutions Architect)

---

## Overview

This guide covers manual UAT test cases for the One Fintech ingest-task pipeline (Stages 1–7). Each test case includes prerequisites, steps, and explicit pass/fail criteria. Tests are designed to be run sequentially against a live environment with real Aurora and S3.

**Infrastructure endpoints (DEV):**
- API ALB: `http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com`
- S3 inbound bucket: `onefintech-dev-inbound-raw-placeholder`
- S3 Lister API: `https://scrhttu8rc.execute-api.us-east-1.amazonaws.com`
- CloudWatch log group: `/onefintech/dev/ingest-task`
- Grafana: `http://onefintech-dev-grafana-445593983.us-east-1.elb.amazonaws.com`

**Test client:** RFU Oregon (`tenant_id = rfu-oregon`, `client_id = rfu`, `subprogram_id = 26071`)

---

## Prerequisites

Before running any test case:

1. Confirm DEV ECS cluster `onefintech-dev` is healthy (ECS console → Services → `ingest-task` shows ACTIVE)
2. Confirm Aurora proxy is reachable (check CloudWatch for recent DB connection logs)
3. Confirm the `programs` table has a row for `subprogram_id = 26071` / `tenant_id = rfu-oregon` (required for Stage 3 program lookup)
4. Have the S3 Lister API available for checking inbound/staged bucket contents
5. Have Grafana open on Dashboard 1 (pipeline overview) to observe metrics in real time

---

## Test Case 1 — SRG310 Happy Path: Single Member Enrollment

**Owner:** John Dennis  
**Purpose:** Verify the full Stages 1–7 pipeline processes a clean SRG310 enrollment file and produces correct Aurora state.

### Setup

Create a test SRG310 file named `uat_srg310_tc01_<YYYYMMDD>.txt` with the following content (pipe-delimited, header row required):

```
client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id
UAT-TC01-001|Jane|Testmember|1985-06-15|123 Main St|Portland|OR|97201|2026-06|26071|OTC|PKG-001
```

Upload to: `s3://onefintech-dev-inbound-raw-placeholder/inbound-raw/rfu-oregon/rfu/2026/06/01/uat_srg310_tc01_<YYYYMMDD>.txt`

### Steps

1. Upload the file to S3 (triggers EventBridge rule → ECS ingest-task)
2. Monitor CloudWatch log group `/onefintech/dev/ingest-task` — filter for `pipeline.complete`
3. Wait for `pipeline.complete` event (expected: under 30 seconds for 1 row, excluding FIS return file wait)

### Pass Criteria

- [ ] `pipeline.complete` event appears in CloudWatch with `exit=0` (no error)
- [ ] `batch_files` table has one row with `status = COMPLETE`, `record_count = 1`, `malformed_count = 0`
- [ ] `batch_records_rt30` has one row with `client_member_id = UAT-TC01-001`, `status = COMPLETED`
- [ ] `domain_commands` has one row with `command_type = ENROLL`, `status = Completed`, `benefit_period = 2026-06`
- [ ] `consumers` has one row for `client_member_id = UAT-TC01-001`, `tenant_id = rfu-oregon`
- [ ] `cards` has one row linked to the consumer with `fis_card_id` populated (from FIS RT30 return)
- [ ] `consumers` row has `fis_person_id` and `fis_cuid` populated (from FIS RT30 return)
- [ ] `audit_log` has entries for stages 1, 3, and 7 at minimum
- [ ] Grafana Dashboard 1: `enrollment_success_rate` = 100%, `dead_letter_rate` = 0%

### Fail Indicators

- `pipeline.error` event in CloudWatch → check `error` field for stage and reason
- `batch_files.status = STALLED` → dead letters present; check `dead_letter_store` table
- `batch_files.status = HALTED` → FIS returned RT99 full-file halt; check FIS return file in S3 exchange bucket

---

## Test Case 2 — SRG310 Multi-Member File

**Owner:** John Dennis  
**Purpose:** Verify pipeline handles a file with multiple members and correct counts.

### Setup

Create `uat_srg310_tc02_<YYYYMMDD>.txt` with 5 rows:

```
client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id
UAT-TC02-001|Alice|One|1980-01-15|100 Oak St|Portland|OR|97201|2026-06|26071|OTC|PKG-001
UAT-TC02-002|Bob|Two|1981-02-20|200 Pine St|Portland|OR|97202|2026-06|26071|OTC|PKG-001
UAT-TC02-003|Carol|Three|1982-03-25|300 Elm St|Portland|OR|97203|2026-06|26071|OTC|PKG-001
UAT-TC02-004|David|Four|1983-04-10|400 Maple St|Portland|OR|97204|2026-06|26071|OTC|PKG-001
UAT-TC02-005|Eve|Five|1984-05-05|500 Cedar St|Portland|OR|97205|2026-06|26071|OTC|PKG-001
```

### Pass Criteria

- [ ] `batch_files.record_count = 5`, `malformed_count = 0`, `status = COMPLETE`
- [ ] 5 rows in `batch_records_rt30`, all `status = COMPLETED`
- [ ] 5 rows in `domain_commands`, all `status = Completed`
- [ ] 5 rows in `consumers`, 5 rows in `cards` — all with FIS identifiers populated
- [ ] Grafana: `enrollment_success_rate = 100%`, `staged` metric = 5

---

## Test Case 3 — SRG310 Malformed Row

**Owner:** John Dennis  
**Purpose:** Verify a malformed row is dead-lettered without stopping the pipeline. The good row must still be processed.

### Setup

Create `uat_srg310_tc03_<YYYYMMDD>.txt` with one good row and one row missing `date_of_birth`:

```
client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id
UAT-TC03-GOOD|Good|Member|1990-07-04|111 Good St|Portland|OR|97201|2026-06|26071|OTC|PKG-001
UAT-TC03-BAD|Bad|Member||222 Bad St|Portland|OR|97202|2026-06|26071|OTC|PKG-001
```

### Pass Criteria

- [ ] `batch_files.status = COMPLETE` (not STALLED — one good row was processed)
- [ ] `batch_files.record_count = 2`, `malformed_count = 1`
- [ ] `dead_letter_store` has one entry for `UAT-TC03-BAD` with a reason referencing `date_of_birth`
- [ ] `batch_records_rt30` has exactly 1 row: `UAT-TC03-GOOD` with `status = COMPLETED`
- [ ] CloudWatch `malformed_rate` metric = 50% (1 of 2 rows malformed)

---

## Test Case 4 — SRG310 Idempotency: Resubmit Same File

**Owner:** John Dennis  
**Purpose:** Verify that resubmitting a file that was already fully processed does not create duplicate records.

### Prerequisites

Test Case 1 must have completed successfully. Use the same file and S3 key.

### Steps

1. Re-upload the **same file** from Test Case 1 to the **same S3 key**
2. Wait for `pipeline.complete` in CloudWatch

### Pass Criteria

- [ ] Pipeline completes without error
- [ ] `domain_commands` count for `UAT-TC01-001` is still **1** (no duplicate inserted)
- [ ] `batch_records_rt30` count for `UAT-TC01-001` is still **1**
- [ ] CloudWatch shows `duplicate` count > 0 in the `pipeline.complete` event

---

## Test Case 5 — SRG310 Stall: Unresolvable Program

**Owner:** John Dennis  
**Purpose:** Verify pipeline stalls when a row references an unknown program and cannot be staged.

### Setup

Create `uat_srg310_tc05_<YYYYMMDD>.txt` with a row referencing `subprogram_id = 99999` (not seeded in `programs` table):

```
client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id
UAT-TC05-STALL|Stall|Member|1975-03-15|999 Stall Rd|Portland|OR|97201|2026-06|99999|OTC|PKG-001
```

### Pass Criteria

- [ ] `batch_files.status = STALLED`
- [ ] `dead_letter_store` has one entry for `UAT-TC05-STALL` with reason referencing unknown program
- [ ] CloudWatch shows `batch.stalled` event
- [ ] Pipeline does **not** proceed to Stage 4 (no assembled file appears in S3 exchange bucket)

### Recovery Step (optional — confirm replay path)

After the stall, mark the dead letter as resolved via the replay-cli or API, then resubmit the file with a corrected `subprogram_id`. Verify the pipeline completes on the second run.

---

## Test Case 6 — SRG315 Card Update

**Owner:** John Dennis  
**Purpose:** Verify a SRG315 card event file (suspend/unsuspend) processes correctly through Stage 3.

### Prerequisites

Test Case 1 or 2 must have completed so `UAT-TC01-001` (or `UAT-TC02-001`) has an enrolled card with a `fis_card_id`.

### Setup

Create `uat_srg315_tc06_<YYYYMMDD>.txt`:

```
client_member_id|event_type|effective_date|subprogram_id
UAT-TC01-001|SUSPEND|2026-06-15|26071
```

### Pass Criteria

- [ ] `batch_files.status = COMPLETE`
- [ ] `batch_records_rt37` has one row for `UAT-TC01-001`, `status = COMPLETED`
- [ ] `domain_commands` has a `SUSPEND` command for `UAT-TC01-001`, `status = Completed`
- [ ] `cards` row for `UAT-TC01-001` has updated `card_status` reflecting the suspension

---

## Test Case 7 — SRG320 Fund Load

**Owner:** John Dennis  
**Purpose:** Verify a SRG320 benefit load file processes correctly and updates the purse balance.

### Prerequisites

Test Case 1 must have completed and `UAT-TC01-001` must have an enrolled card with `fis_card_id` populated.

### Setup

Create `uat_srg320_tc07_<YYYYMMDD>.txt`:

```
client_member_id|command_type|amount|benefit_period|subprogram_id
UAT-TC01-001|LOAD|95.00|2026-06|26071
```

### Pass Criteria

- [ ] `batch_files.status = COMPLETE`
- [ ] `batch_records_rt60` has one row for `UAT-TC01-001`, `status = COMPLETED`
- [ ] `domain_commands` has a `LOAD` command, `status = Completed`
- [ ] `purses` row for `UAT-TC01-001`, `benefit_period = 2026-06` has `available_balance_cents = 9500`
- [ ] Stage 7: `fis_purse_number` is populated on the purse row (from FIS RT60 return)
- [ ] `benefit_period` on the purse FIS update matches `2026-06` — **not** the current calendar month. This is the RT60 regression guard.

---

## Test Case 8 — Audit Log Completeness

**Owner:** Kendra Williams  
**Purpose:** Verify audit trail meets BSA 5-year retention requirement. Every stage must write at least one `audit_log` entry per pipeline run.

### Steps

After any completed Test Case 1–7 run, query `audit_log` for the correlation ID:

```sql
SELECT entity_type, changed_by, old_state, new_state, notes
FROM audit_log
WHERE correlation_id = '<correlation_id_from_cloudwatch>'
ORDER BY created_at;
```

### Pass Criteria

- [ ] At least one entry per pipeline run exists in `audit_log`
- [ ] Entries cover `batch_files` status transitions (RECEIVED → VALIDATED → STAGED → ASSEMBLED → DEPOSITED → COMPLETE)
- [ ] `changed_by` is `ingest-task:stage<N>` for all pipeline entries
- [ ] No PHI (first name, last name, date of birth, address) appears in `notes` or `new_state` fields
- [ ] Total entries for a 5-row file: minimum 10 entries (≥2 per stage)

---

## Test Case 9 — Dead Letter Replay

**Owner:** John Dennis / Kendra Williams  
**Purpose:** Verify the replay-cli can resolve a stalled batch by replaying a dead-lettered row after the root cause is fixed.

### Prerequisites

Test Case 5 (Stall) must have been run and `UAT-TC05-STALL` is present in `dead_letter_store`.

### Steps

1. Seed the missing program: insert a row for `subprogram_id = 99999` in the `programs` table (or correct the test to use a valid subprogram)
2. Use the replay-cli:
   ```
   replay-cli --correlation-id <from_tc05> --row-sequence 1
   ```
3. Monitor CloudWatch for `pipeline.complete`

### Pass Criteria

- [ ] `dead_letter_store` entry for `UAT-TC05-STALL` has `replayed_at` timestamp populated
- [ ] `batch_records_rt30` has a new row for `UAT-TC05-STALL` with `status = COMPLETED`
- [ ] `batch_files.status = COMPLETE`

---

## Test Case 10 — Grafana Dashboard Validation

**Owner:** Kendra Williams  
**Purpose:** Verify all five Grafana dashboards display accurate data for the UAT runs.

### Steps

After completing Test Cases 1–7, open Grafana at `http://onefintech-dev-grafana-445593983.us-east-1.elb.amazonaws.com`.

### Pass Criteria — Dashboard 1 (Pipeline Overview)

- [ ] `pipeline_duration_ms` panels show values for all completed runs
- [ ] `enrollment_success_rate` shows 100% for clean test cases
- [ ] `dead_letter_rate` shows 0% for clean test cases, 50% for TC03
- [ ] `staged` count matches file row counts per run

### Pass Criteria — Dashboard 2 (Stage Detail)

- [ ] All 7 stage duration panels populate without "No data" for completed runs
- [ ] No stage shows consistently 0ms (would indicate stage was skipped)

### Pass Criteria — Dashboard 3–5 (Drill-through)

- [ ] Correlation ID filter works: entering a known correlation ID from CloudWatch returns records only for that run
- [ ] Dead letter rate panel links through to individual dead letter entries

---

## Test Case 11 — Card/Member API Smoke

**Owner:** John Dennis  
**Purpose:** Verify the 8 Card/Member API endpoints return correct data for enrolled members.

### Prerequisites

Test Case 1 must have completed and `UAT-TC01-001` is enrolled.

### Steps

Using `curl` or Postman against `http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com`:

```bash
# Health check
curl http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com/health

# Get member
curl "http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com/v1/members/UAT-TC01-001?tenant_id=rfu-oregon"

# Get card for member
curl "http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com/v1/members/UAT-TC01-001/card?tenant_id=rfu-oregon"
```

### Pass Criteria

- [ ] `GET /health` returns `200 OK`
- [ ] `GET /v1/members/{id}` returns the member record with `fis_person_id` populated
- [ ] `GET /v1/members/{id}/card` returns the card with `fis_card_id` populated
- [ ] No 500 errors on any endpoint
- [ ] Response includes `tenant_id = rfu-oregon` (multi-tenant isolation confirmed)

---

## Defect Reporting

Log defects with the following fields:

| Field | Content |
|---|---|
| Test Case | TC number and name |
| Step | Which step failed |
| Expected | The pass criterion that was not met |
| Actual | What was observed (include correlation_id and CloudWatch timestamp) |
| Severity | P1 (blocks go-live) / P2 (workaround exists) / P3 (cosmetic) |

P1 defects block UAT sign-off. Kendra Williams holds the UAT sign-off authority per the First-Client Onboarding Checklist (§5.3a).

---

## UAT Sign-Off Checklist

The following must be complete before Kendra Williams signs off:

- [ ] Test Cases 1–7 all pass in TST environment
- [ ] Test Case 8 (audit log) verified by Kendra Williams
- [ ] Test Case 9 (replay) verified by John Dennis
- [ ] Test Case 10 (Grafana) verified by Kendra Williams
- [ ] Test Case 11 (API) verified by John Dennis
- [ ] Zero P1 defects open
- [ ] `dead_letter_store` for the UAT run has no unexplained unresolved entries
- [ ] MVB § 10.3(e) notification drafted and ready for submission (OI #43, owner: Kendra Williams)
- [ ] OFAC escalation runbook documented (OI #41, owner: Kyle Walker / Nir Dagan)
