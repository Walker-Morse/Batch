# Schema

Aurora PostgreSQL DDL for the One Fintech platform.
Source of truth: `OneFin_DDL_Reference_v1_0.docx` (companion to HLD §4).
Executed once per tenant database during provisioning (ADR-009: separate DB per health plan client).

## Execution Order

Scripts must be executed in this exact order due to FK dependencies:

```
pipeline_state/001_batch_files.sql          -- root table; no FKs
pipeline_state/002_domain_commands.sql      -- refs batch_files
pipeline_state/003_batch_records.sql        -- refs batch_files, domain_commands
pipeline_state/004_dead_letter_store.sql    -- refs batch_files

domain_state/000_programs.sql              -- no FKs; must precede consumers/purses
domain_state/001_consumers_cards_purses.sql -- refs programs, batch_files
domain_state/002_programs_apl.sql          -- refs programs
domain_state/003_audit_log.sql             -- no FKs by design (append-only)

roles/ingest_task_role.sql                 -- run as superuser; grants/revokes
```

## Key Constraints

- `audit_log`: INSERT-only enforced via PostgreSQL GRANT/REVOKE — not application convention
- `purses.expiry_date`: hard contractual deadline (SOW §2.1); AT30 must complete before this
- `purses.purse_type`: GENERATED column = `LEFT(fis_purse_name, 3)` — do not set manually
- `purses.available_balance_cents`: updated at `domain_commands` write time — not deferred
- `cards.pan_masked`: first 6 + last 4 only — full PAN is never stored anywhere
- `cards.fis_card_id`: FIS opt-in required (Selvi Marappan) — NULL until opt-in enabled
