# Sanctions Screening

OFAC and sanctions list screening for all member records, implementing the two
architecturally distinct obligations imposed by MVB Program Standards §§ 10.3–10.3.1,
BSA/PATRIOT Act § 326, and 31 CFR § 501 et seq.

Per ADR-011: two vendors for two obligations. They are not interchangeable and not redundant.

## Obligation 1 — Onboarding screen: OFAC-API.com

**Trigger**: Pre-pipeline screening Lambda, fires on S3 file arrival BEFORE Stage 1.
**What it does**: Submits the full SRG310 member set to OFAC-API.com in batches of 500.

API call: `POST https://api.ofac-api.com/v4/screen`

Parameters:
- `sources`: `["SDN", "NONSDN", "FINCEN", "UN"]` — all four MVB-required lists
- `minScore`: `95` — US Treasury fuzzy matching guideline default
- `cases[].id`: set to `client_member_id` — maps results back to SRG310 rows
- `cases[].type`: `"person"`
- `cases[].name` + `cases[].dob`

On `matchCount > 0`:
- Write to `dead_letter_store` with `reason_code = OFAC_HIT`
- Member is excluded from the ingest-task manifest
- Ingest-task receives only the cleared member set
- Stage 2 no longer performs OFAC screening

**Why pre-pipeline batch is superior to inline Stage 2 screening:**
1. **Throughput**: 200K members = 400 batch requests of 500 each (viable).
   200K serial synchronous API calls inside Stage 2 = ~5+ hours (not viable).
2. **Pipeline integrity**: OFAC-API.com outage cannot stall a file mid-processing.
   The screening Lambda fails cleanly before Stage 1 begins.
3. **Regulatory precision**: No card is ever issued for an unscreened member —
   quarantined records never reach Stage 4 (Batch Assembly).
4. **Audit clarity**: Screening results written to `ofac_screening_results` table
   with timestamps before any pipeline state is touched.

**Handoff mechanism** (implementation decision for John Stevens — before Stage 2 sprint):
Either Step Functions orchestration between screening Lambda and ingest-task,
or ingest-task Stage 1 polling for a "screening-complete" signal.

## Obligation 2 — Ongoing re-screen: sanctions.io Continuous Monitoring

**Trigger**: Stage 7 — when FIS return file confirms RT30 Completed (card physically issued).
**What it does**: Enrolls the member in sanctions.io Continuous Monitoring via API.

sanctions.io monitors the enrolled member against its global watchlist database
(updated every 60 minutes) and pushes a webhook notification to a One Fintech-hosted
endpoint if the member appears on any newly updated list.

**Why continuous monitoring via webhook is superior to scheduled weekly batch:**
1. **Regulatory precision**: MVB § 10.3 requires rescreening "whenever lists are updated"
   — a weekly cadence leaves exposure windows. Webhook-push eliminates this.
2. **Operational simplicity**: No EventBridge rule, no batch job to maintain.
3. **SOC 2**: sanctions.io is SOC 2 certified — audit-ready evidence.

**On webhook notification**:
- Webhook handler Lambda validates signature
- Writes to `dead_letter_store` with `reason_code = OFAC_HIT`
- Sets `consumers.status = SUSPENDED`
- Writes compliance event to `audit_log`

## Required lists (MVB Program Standards § 10.3.1)

| List | Source |
|------|--------|
| OFAC SDN | US Treasury Office of Foreign Assets Control |
| OFAC Non-SDN Consolidated | OFAC (FSE, SSI, and other non-SDN lists) |
| OFAC Sanctioned Countries | OFAC comprehensive sanctions programs |
| FinCEN 311 and 9714 Special Measures | 31 U.S.C. §§ 5318A and 9714 |

## TRUE match escalation (Open Item #41)

A TRUE match (confirmed hit following manual compliance review) requires escalation.
The escalation path (Morse Compliance Officer identity and notification procedure)
must be documented in the operational runbook before go-live.

## MVB notification obligation (Open Item #43, §H.5)

Morse must notify MVB in writing before go-live naming:
- OFAC-API.com as the onboarding batch screening tool
- sanctions.io as the continuous monitoring provider
- All four lists screened
- Frequency: at onboarding + continuously on list update via webhook
Owner: Kendra Williams.

## Documentation and retention (§H.3)

All screening results — including negative (no match) results — must be saved to PDF
and retained per Morse's internal record retention policy. Screening run records
(date, list version, member count, match count) must be captured in `audit_log`
as a compliance event class.
