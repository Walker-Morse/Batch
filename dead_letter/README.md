# Dead Letter

Captures, triages, and replays failed pipeline records. Every dead-lettered record
represents a member that was not processed and will not appear in the FIS batch file
until resolved. This is an operational responsibility — not an automated one.

## Detection (§6.5.1)

When the ingest-task catches an unhandled exception at any stage boundary:
1. Writes a row to `dead_letter_store` **synchronously** before any retry logic
2. Emits "dead.letter.written" structured log event (zero PHI)
3. CloudWatch metric filter increments custom metric
4. CloudWatch alarm fires when metric > 0 in any 5-minute window
5. Datadog notification to on-call engineering channel
6. Unacknowledged within 30 minutes → automatic escalation

If `dead_letter_store` write itself fails (Aurora unavailable):
- Log "dead.letter.capture.failed" at ERROR level
- Transition `batch_files.status` → HALTED immediately
- This condition pages on-call immediately via the batch halt alarm

## Capture (§6.5.2)

`message_body` (JSONB) stores the raw failed record content.
**PHI present — never log its contents at any log level.**
`failure_reason` contains only a structured error code and stage identifier.
Operators accessing `dead_letter_store` for triage must use role-based DB access —
not shared credentials.

## Triage (§6.5.3)

| Failure class | Resolution |
|---------------|------------|
| Transient infrastructure (Aurora timeout, Fargate latency) | Replay |
| Data validation failure (bad field in source file) | Inspect message_body, correct, replay or abandon |
| Code defect (unhandled exception in stage logic) | Do NOT replay until hotfix deployed |
| Poison message (permanently unprocessable) | Mark resolved, notify MCO, do not replay |

## Replay (§6.5.4)

The replay-cli tool (Open Item #24 — must exist before UAT May 20):
1. Reads unresolved rows from `dead_letter_store` (resolved_at IS NULL)
2. Re-invokes ingest-task with `--replay` flag + correlation_id + row_sequence_number
3. Sets `replayed_at` timestamp on the dead_letter_store row
4. On successful replay: ingest-task sets `resolved_at = now()`, `resolved_by = 'replay-cli'`
   automatically — no separate manual step required

The three-layer idempotency guarantee makes replay safe to invoke multiple times:
1. File-level SHA-256 on batch_files
2. Record-level domain_commands composite key
3. FIS-level clientReferenceNumber

## Batch file impact (§6.5.5)

A dead-lettered record was never staged. When Stage 3 finishes:
- If staged_count < batch_files.record_count: file transitions to STALLED
- STALLED resolution options:
  a. Replay successfully → staged count matches → Stage 4 resumes normally
  b. Poison message confirmed → operator manually decrements record_count → resume

Option (b) must appear in the MCO reconciliation report identifying excluded
Client_Member_IDs and reasons.

## SLA targets (§6.5.6 — confirm with Kendra Williams before go-live)

| Event | Proposed SLA | Escalation |
|-------|-------------|------------|
| DLQ message arrives | Acknowledge within 30 min | Datadog auto-escalates to secondary |
| Triage complete | Within 1 hour of arrival | Escalate to engineering lead |
| Resolution | Within 4 hours of arrival | Escalate to John Stevens + Kendra Williams |
| Client notification (if abandoned) | Same business day | Kendra Williams notifies MCO |
