# HIPAA Security Rule and HITECH Compliance

Source: HLD Addendum D

## Business Associate Relationship

One Fintech ingests, processes, and stores PHI on behalf of MCO clients.
MCO clients are Covered Entities under HIPAA. Morse LLC is a Business Associate
under 45 CFR § 160.103. HITECH (42 U.S.C. § 17931) extends HIPAA Security Rule
obligations directly to Business Associates.

**A signed BAA must be in place with each MCO client before PHI is processed in
any environment.** Processing PHI without a BAA = HIPAA violation.
Open Item #34 (owner: Kendra Williams).

## PHI Fields in This System

The following tables contain PHI (§5.4.1):
`consumers`, `cards`, `purses`, `batch_records_rt30`, `batch_records_rt37`,
`batch_records_rt60`, `dim_member`, `dim_card`, `dim_purse`, `audit_log`,
`dead_letter_store`

PHI identifiers present:
- Names (first_name, last_name)
- Geographic data (address, city, state, zip)
- Dates (date_of_birth, benefit effective date, benefit expiry date)
- Account numbers (client_member_id, proxy card number)
- Health plan beneficiary number

`dead_letter_store.message_body` (JSONB) may contain raw member row data.
Treat as PHI for all retention, access control, and audit purposes.

## Technical Safeguards (45 CFR § 164.312)

| Safeguard | Implementation |
|-----------|---------------|
| 164.312(a)(1) Access Control | Aurora IAM authentication; ingest-task role scoped to minimum tables; tableau_ro read-only |
| 164.312(a)(2)(iv) Encryption/Decryption | SSE-KMS at rest; TLS 1.2+ in transit (1.0/1.1 disabled); PGP at application layer |
| 164.312(b) Audit Controls | audit_log (INSERT-only, application layer); CloudTrail (API-level, log file validation enabled) |
| 164.312(c)(1) Integrity | SHA-256 hashes on all batch files; S3 Object Lock on inbound-raw and fis-exchange |
| 164.312(d) Authentication | MFA via SCP for all human access; Aurora IAM auth; no shared DB passwords |
| 164.312(e)(1) Transmission Security | TLS 1.2+; PGP for file transport; both required simultaneously |

## Plaintext Staged Files

`staged/` S3 prefix is the **only point where PHI exists unencrypted at rest**.
The ingest-task MUST delete the plaintext staged object immediately after
confirming successful Stage 4 (Batch Assembly) completion and PGP encryption.
24-hour S3 lifecycle policy on `staged/` is a safety backstop — not a substitute.

## Breach Notification

HIPAA floor: 60 days after discovery (45 CFR Part 164 Subpart D).
**MSA §7 with RFU: stricter contractual deadline of 72 hours.**
The 72-hour contractual deadline is the operative obligation for RFU.
Document breach response procedure incorporating 72-hour trigger in operational runbook.

## Audit Log Retention

HIPAA requires 6-year retention (45 CFR § 164.316(b)(2)).
Aurora automated backup max = 35 days — **insufficient**.
S3 export pipeline with Glacier lifecycle and Object Lock must be designed
and implemented before go-live (Open Item #36).

## Formal Risk Analysis Required

A formal risk analysis under 45 CFR § 164.308(a)(1)(ii)(A) must be conducted
by Morse LLC before go-live. This document does not constitute a risk analysis.
