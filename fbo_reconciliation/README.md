# FBO Purse Balance Integrity

One Fintech is the authoritative sub-ledger for all member purse balances.
The aggregate of all active positive purse balances across all tenants must be
reconcilable to the MVB FBO account balance at all times.

This is a regulatory and contractual requirement — not an operational preference.

## Regulatory basis (Addendum I)

**FDIC 12 CFR Part 330 + FDIC General Counsel Opinion No. 8**: For cardholder funds in
a pooled FBO account to qualify for FDIC pass-through insurance coverage (up to $250K
per beneficial owner), the program manager must maintain accurate records identifying
each beneficial owner and the amount held on their behalf. The aggregate of all
individual beneficial-owner balances must be reconcilable to the total FBO balance.

**Consequence of failure**: The Synapse Financial Technologies bankruptcy (2024)
established industry precedent — account freezes, loss of cardholder access to funds,
regulatory consent orders against the sponsoring bank.

**MVB sponsoring bank program agreement**: FBO account balance must always equal or
exceed the sum of all positive cardholder purse balances.

## One Fintech as authoritative sub-ledger (§I.2)

`purses.available_balance_cents` is the authoritative per-member balance figure.
Every RT60 fund load or expiry MUST be reflected in this column at the time the
command is written to `domain_commands` — not deferred until the FIS return file
confirms execution.

Outstanding `domain_commands` with status Pending are treated as provisional
liabilities in the reconciliation methodology.

## Daily reconciliation requirement (§I.3, Open Item #44)

Compare:
- Sum of `purses.available_balance_cents` across all active purses (platform liability)
- Against: closing balance of the MVB FBO account for that business day

Must complete before the start of the next business day's file processing window.
Result recorded as a compliance event in `audit_log`.

Open Item #44 (before go-live, owner: Kendra Williams):
- Process owner named
- Tooling identified
- Escalation threshold defined
- MVB notification SLA documented

## Consequences of discrepancy (§I.4)

**Platform liability > FBO balance**: FDIC pass-through insurance eligibility at risk.
Compliance failure — not an operational anomaly. Escalate immediately to Kendra Williams
and notify MVB Bank.

**FBO balance > platform liability**: Operational surplus. Investigate but not a
regulatory exposure.

## Go-live gates (§I.5)

1. Daily reconciliation process defined (Open Item #44)
2. FBO account balance confirmed sufficient for initial RFU member population
3. Reconciliation run in § 5.3a First-Client Onboarding Checklist as PRD gate
4. Reconciliation compliance event class added to audit_log and implemented before UAT
