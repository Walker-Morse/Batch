# Benefit Loading

Manages the lifecycle of benefit funds on member purses: initial loads, monthly
reloads, period-end sweeps, and the top-off model.

## What this package does

Translates SRG320 fund management records into RT60 (AT01/AT30) FIS batch records.
Enforces the contractual expiry constraint and the purse sequencing invariant.

## Benefit amounts (SOW Exhibit B — RFU Phase 1)

| Purse | Type | Amount per member per month |
|-------|------|-----------------------------|
| Purse 1 | Fruits & Vegetables (OTC) | $95.00 |
| Purse 2 | Pantry Foods (FOD) | $250.00 |

Amounts are stored in RT60 `amount_cents` at load time — not hardcoded here.
Any change requires a written SOW amendment. Per §2 Assumptions (owner: Selvi Marappan).

## Top-off model (§2 Assumptions)

- **Intra-period**: RT60 AT01 loads funds to an existing active purse additively
- **Period-end cycle**:
  1. RT60 AT30 — sweep expiring purse (zero-balance close)
  2. RT60 AT01 — full-benefit load on new purse for next period
  Both issued by One Fintech as a **correlated pair** (same domain_command_id).
  AT30 MUST complete (Stage 7 COMPLETED) before paired AT01 is submitted.
  ACC Autoload is NOT used for either operation.

## Benefit expiry constraint (SOW §2.1, §3.3) — HARD CONTRACTUAL REQUIREMENT

Benefit funds expire at **11:59 PM Eastern Time on the last day of each calendar month**.
There is no rollover. Unused balances do not carry forward.

The AT30 period-end sweep MUST be issued and confirmed by FIS **before** this deadline.
Late submission = contract breach. Not recoverable.

EventBridge Scheduler rule (ADR-007) governing period-end sweep must target
early-morning submission — recommended no later than 06:00 Pacific (Open Item #29).

The `purses.expiry_date` column is the source of truth for sweep targeting.
Stage 4 derives its sweep target date from this column — not from any approximate schedule.

## 6-month enrollment mechanic (SOW §2.1, §2 Assumptions)

- Member enrolled for 6-month benefit duration per purse type
- Purse 1 (OTC) first, then Purse 2 (FOD) for additional 6 months
- The two purse types are NOT concurrent — sequential, member-level
- Maximum program duration: 12 months across both purse types
- Transitions are member-level (not program-wide bulk events)

## FBO integrity dependency (Addendum I)

Every RT60 benefit load or expiry issued by One Fintech MUST be reflected in
`purses.available_balance_cents` at the time the command is written to
`domain_commands` — not deferred until the FIS return file confirms execution.
This maintains One Fintech as the authoritative sub-ledger. Daily reconciliation
against the MVB FBO account balance is required before go-live (Open Item #44).
