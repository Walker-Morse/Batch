# Dead Letter Triage Runbook

Source: HLD §6.5. Must be reviewed and approved before UAT (May 20, 2026).

## SLA Targets (confirm with Kendra Williams before go-live)

| Event | SLA | Escalation |
|-------|-----|------------|
| Alert fires | Acknowledge within 30 min | Datadog auto-escalates to secondary |
| Triage complete | Within 1 hour of arrival | Escalate to engineering lead |
| Resolution | Within 4 hours of arrival | Escalate to John Stevens + Kendra Williams |
| Client notification (if abandoned) | Same business day | Kendra Williams notifies MCO |

## Step 1 — Acknowledge the alert

Acknowledge in Datadog. Check CloudWatch for `dead.letter.written` metric.
Note: `batch.halt.triggered` = higher severity — pages immediately without SLA window.

## Step 2 — Query the dead letter store

```sql
SELECT id, correlation_id, failure_stage, failure_reason,
       retry_count, created_at
FROM   public.dead_letter_store
WHERE  correlation_id = '<uuid>'
AND    resolved_at IS NULL
ORDER  BY created_at;
```

**Do NOT log or display `message_body` — it contains PHI.**

## Step 3 — Determine failure class

| failure_reason prefix | Class | Action |
|----------------------|-------|--------|
| `TRANSIENT_*` | Transient infrastructure | Replay |
| `VALIDATION_*` | Data validation | Inspect message_body (PHI access), correct, replay or abandon |
| `DEFECT_*` | Code defect | Do NOT replay. Wait for hotfix. |
| `POISON_*` | Poison message | Abandon. Notify MCO. |
| `OFAC_HIT` | Sanctions screening | Escalate to Compliance Officer (Open Item #41) |

## Step 4 — Check for RT99 full-file halt

If `batch_files.status = 'HALTED'`:
- This is a full-file FIS pre-processing rejection (§6.5.1)
- ALL members in the file were not processed
- Do not use the replay-cli for individual rows
- The ENTIRE file must be corrected and resubmitted
- Notify MCO that this is a full-file rejection, not individual record failures

Distinguish from individual record failures:
- Full-file halt: return file contains exactly 1 record (RT99 only)
- Individual failures: return file contains RT99 entries mixed with other records

## Step 5 — Check for duplicate filename (§6.5.5)

If Stage 6 timeout expired but no return file was received:
Confirm whether FIS never processed the file vs. return file arrived but was missed.
Check whether a file with the same name was previously submitted on the same calendar day.
FIS silently discards duplicate filenames without notification.

## Step 6 — Replay (for transient/validation failures)

```bash
replay-cli --correlation-id <uuid>
# Preview without executing:
replay-cli --correlation-id <uuid> --dry-run
# Replay single row:
replay-cli --correlation-id <uuid> --row-seq <n>
```

Replay is safe to invoke multiple times (three-layer idempotency guarantee).
Do NOT replay a code defect failure until a hotfix is deployed.

## Step 7 — Poison message abandonment

```sql
UPDATE public.dead_letter_store
SET    resolved_at = NOW(),
       resolved_by = '<operator_id>',
       resolution_notes = 'Poison message: <reason>. MCO notified.'
WHERE  id = <id>;

-- Manually adjust record_count if file is STALLED due to this row
UPDATE public.batch_files
SET    record_count = record_count - 1,
       updated_at = NOW()
WHERE  id = <batch_file_id>;

-- Write audit_log entry for the adjustment
INSERT INTO public.audit_log (tenant_id, entity_type, entity_id, old_state, new_state,
                               changed_by, correlation_id, notes)
VALUES ('<tenant>', 'batch_files', '<id>', 'STALLED', 'STALLED',
        '<operator_id>', '<correlation_id>',
        'record_count adjusted: poison message <client_member_id> abandoned. MCO notified.');
```

The MCO reconciliation report MUST identify excluded Client_Member_IDs and reasons.

## Step 8 — Close the loop

- If Kendra Williams must notify the MCO: provide Client_Member_IDs and reason codes
- Update resolution_notes in dead_letter_store
- Confirm CloudWatch alarm clears after resolution
