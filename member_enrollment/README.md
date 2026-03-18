# Member Enrollment

Processes SRG310 (new enrollment), SRG315 (status change), and SRG320 (fund management)
inbound files from health plan clients (MCOs).

## What this package does

1. **File Arrival (Stage 1)**: Receives the MCO file from S3, computes SHA-256 hashes
   (encrypted and plaintext), writes the non-repudiation row to `batch_files` before
   any processing begins. Delivers file acknowledgement to MCO outbound SFTP.

2. **Validation (Stage 2)**: PGP-decrypts the file. Validates SRG format, field types,
   and lengths against the confirmed column definitions. Malformed rows go to
   `dead_letter_store` with `failure_stage = validation`. The file continues for
   well-formed rows.

3. **Row Processing (Stage 3)**: Sequential row-by-row per ADR-010.
   For each row:
   - Idempotency check against `domain_commands` (MUST precede all writes)
   - Write `batch_records_rt30 / rt37 / rt60` with status STAGED
   - Write domain state: `consumers`, `cards`, `purses`
   - Inline mart write via MartWriter: `dim_member`, `dim_card`, `dim_purse`, `fact_enrollments`

   On row exception: write to `dead_letter_store`, continue.
   On Stage 3 completion: compare staged count to `batch_files.record_count`.
   If counts diverge → STALLED. Do not proceed to batch assembly.

## SRG file types (§1.2)

| File   | Purpose                              | FIS Records Produced   |
|--------|--------------------------------------|------------------------|
| SRG310 | Member enrollment and demographics   | RT30 + RT37 + RT60     |
| SRG315 | Purse lifecycle (activate, expire, transfer) | RT37 + RT60   |
| SRG320 | Fund management (recurring loads, cashOut) | RT60              |

## Idempotency key (§4.1.1)

`(correlation_id, client_member_id, command_type, benefit_period)` — composite unique
on `domain_commands`. A row with `status = Accepted` is the gate. Any replay with
the same key returns `Duplicate` without touching FIS or domain state.

## OFAC screening dependency (ADR-011, Addendum H)

The pre-pipeline OFAC screening Lambda (in `sanctions_screening/`) MUST complete
and produce its quarantine manifest BEFORE Stage 1 begins. The ingest-task only
receives the cleared member set. Any member with `matchCount > 0` from OFAC-API.com
is written to `dead_letter_store` with `reason_code = OFAC_HIT` before this package
is invoked.
