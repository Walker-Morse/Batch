# FIS Reconciliation

Handles the complete FIS Prepaid Sunrise submission and reconciliation cycle:
batch file assembly, PGP encryption, Secure File Transfer delivery, return file
processing, and MCO reconciliation report delivery.

## What this package does

4. **Batch Assembly (Stage 4)**: Assembles FIS 400-byte fixed-width records.
   File structure is mandatory (§6.6.2):
   - RT10 File Header (Log File Indicator = **0**, hardcoded — not overridable)
   - RT20 Batch Header
   - Data records: RT30/RT37/RT60/RT62 (400 bytes, CRLF-separated)
   - RT80 Batch Trailer (exact record count — mismatch = RT99 halt)
   - RT90 File Trailer (written LAST — total/batch/detail counts must be exact)

   PGP-encrypts with FIS public key. Writes to S3 fis-exchange prefix.
   **Deletes plaintext staged object immediately after successful encryption (§5.4.3).**
   Applies file splitting if row count > 100,000 (§5.2a, Open Item #28).

5. **FIS Transfer (Stage 5)**: Delivers encrypted file via AWS Transfer Family
   to FIS Prepaid Sunrise SFTP endpoint. Transitions `batch_files` → TRANSFERRED.

6. **Return File Wait (Stage 6)**: Container remains alive and polls S3 for
   the FIS return file. Configurable timeout (default 6h — confirm with Kendra
   Williams, Open Item #25). On timeout: emit `dead.letter.alert`, transition → STALLED.

7. **Reconciliation (Stage 7)**: Ingests return file. Matches each FIS result
   record to its staged `batch_records` row. Updates status STAGED → COMPLETED|FAILED.
   Writes `fact_reconciliation` via MartWriter. Delivers reconciliation report to MCO.
   Detects RT99 full-file rejection (§6.5.1).

## FIS record format knowledge

ALL field offsets, padding rules, AT codes, and RT10/RT20/RT80/RT90 wrapper
structure live in `fis_adapter/` — never in pipeline stage logic (ADR-001).
This boundary is what makes future hexagonal formalisation possible without
a domain rewrite.

## RT99 pre-processing halt (§6.5.1)

FIS validates the file structure before executing any data records. If the
submitted file fails this pass (e.g. record count mismatch in RT90), FIS
returns RT99 as the **first and only record** in the return file. No member
records are processed. Stage 7 detects this by checking whether the return
file contains exactly one record. On detection:
- `batch_files.status` → HALTED
- All members written to `dead_letter_store`
- `batch.halt.triggered` event emitted
- On-call paged immediately

Individual record errors produce RT99 **per record only** — that is distinct
from the file-level halt. Read §6.5.1 carefully.

## File naming convention (§6.6.1)

```
ppppppppmmddccyyss.*.txt
```
- `pppppppp`: 8-char FIS program identifier (Open Item #31 — confirm with Selvi Marappan)
- `mmddccyy`: file creation date
- `ss`: 2-digit sequence number, increments per file per calendar day, resets at midnight
- `*`: issuance | disbursement | maintenance | all

The sequence counter survives container restarts — stored in `batch_files` table.
Duplicate filenames cause FIS to silently discard the second file (§6.5.5).

## Log File Indicator (§6.6.3)

MUST be **0** (return all records). Hardcoded in `fis_adapter/` — not overridable
at runtime. Setting to 1 (errors only) would make Stage 7 blind to successful
enrollments and unable to capture FIS-assigned card IDs and purse numbers.

## Test/Prod Indicator (§6.6.4)

| Environment | Indicator | Behaviour                          |
|-------------|-----------|-------------------------------------|
| DEV         | T         | Format test only — no cards created |
| TST         | P         | Real FIS processing — UAT requires this |
| PRD         | P         | Always P                            |

Driven by `PIPELINE_ENV` environment variable — never hardcoded.
