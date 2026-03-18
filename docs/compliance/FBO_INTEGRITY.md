# FBO Purse Balance Integrity

Source: HLD Addendum I

## Regulatory Basis

- FDIC 12 CFR Part 330 + GC Opinion No. 8: pass-through insurance eligibility
  requires accurate sub-ledger reconcilable to FBO account total
- MVB program agreement: FBO balance must always equal or exceed sum of all
  positive cardholder purse balances
- Synapse Financial Technologies bankruptcy (2024): industry precedent for
  consequences of FBO ledger failures

## One Fintech as Authoritative Sub-Ledger

`purses.available_balance_cents` = per-member balance of record.

Every RT60 fund load or expiry MUST update this column at `domain_commands`
write time — not deferred until FIS return file confirms execution.

Outstanding `domain_commands` with status Pending = provisional liabilities
included in daily reconciliation.

## Daily Reconciliation

Compare: `SUM(purses.available_balance_cents WHERE status IN ('ACTIVE','PENDING'))`
Against: MVB FBO account closing balance for the business day.

Must complete before next business day's file processing window.
Result recorded as compliance event in `audit_log`.

## Consequences of Discrepancy

Platform liability > FBO balance = FDIC pass-through insurance at risk.
This is a compliance failure — escalate immediately to Kendra Williams and notify MVB Bank.

## Open Item #44

Before go-live (owner: Kendra Williams):
- Process owner named
- Tooling to compute aggregate purse balance
- Escalation threshold defined
- MVB notification SLA documented
