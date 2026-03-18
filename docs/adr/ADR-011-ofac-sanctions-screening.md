# ADR-011 — OFAC Sanctions Screening: OFAC-API.com (Onboarding) + sanctions.io (Continuous Monitoring)

**Status:** ACCEPTED  
**Date:** 2026-03-17  
**Decider:** Kyle Walker

## Context

One Fintech must comply with two distinct OFAC screening obligations (MVB Program Standards
§§ 10.3–10.3.1, BSA/PATRIOT Act § 326, 31 CFR § 501 et seq.):
1. Screen every new member prior to card issuance
2. Rescreen the full member roster whenever sanctions lists are updated

The initial HLD described these as a DIY fuzzy-match against raw OFAC list downloads.
That approach is not buildable: 200K serial HTTP calls inside Stage 2 would add hours
to processing, and the "iLEX" vendor referenced as the source of the 85% confidence
threshold recommendation does not exist as an OFAC screening provider.

## Decision

Two vendors for two architecturally distinct obligations:

**Obligation 1 — Onboarding (new SRG310 members): OFAC-API.com**
- Pre-pipeline Lambda fires on S3 arrival, before Stage 1
- Batches of up to 500 cases per request
- Sources: `["SDN", "NONSDN", "FINCEN", "UN"]` (all four MVB-required lists)
- `minScore: 95` (US Treasury fuzzy matching guideline default)
- `case.id = client_member_id` — direct SRG310 row mapping
- Any `matchCount > 0` → `dead_letter_store` with `reason_code = OFAC_HIT`
- Ingest-task receives only the cleared member set
- Stage 2 no longer performs OFAC screening

**Obligation 2 — Ongoing re-screen: sanctions.io Continuous Monitoring**
- Enrolled at Stage 7 when RT30 Completed confirms card physically issued
- sanctions.io monitors enrolled members against global watchlist (updated every 60 min)
- Webhook pushes to One Fintech-hosted endpoint on list update match
- Webhook handler: write to dead_letter_store, set consumer.status = SUSPENDED

## Why pre-pipeline batch is superior to inline Stage 2 screening

1. **Throughput**: 200K members = 400 batch requests of 500 each (viable).
   200K serial synchronous calls inside Stage 2 = ~5+ hours (not viable).
2. **Pipeline integrity**: OFAC-API.com outage cannot stall a file mid-processing.
3. **Regulatory precision**: No card is ever issued for an unscreened member.
4. **Audit clarity**: Results written to `ofac_screening_results` before any pipeline state.

## Why continuous monitoring is superior to scheduled weekly batch

1. **Regulatory precision**: MVB § 10.3 requires "whenever lists are updated" —
   weekly cadence leaves exposure windows on every list update day.
2. **Operational simplicity**: No EventBridge rule, no batch job.
3. **SOC 2**: sanctions.io is SOC 2 certified.

## Consequences

- New pre-pipeline screening Lambda added to architecture
- New webhook handler Lambda for sanctions.io alerts
- New `ofac_screening_results` Aurora table for audit retention
- Both OFAC-API.com and sanctions.io named in MVB § 10.3(e) written notification
  (owner: Kendra Williams, Open Item #43)
- Handoff mechanism between screening Lambda and ingest-task: implementation decision
  for John Stevens (Step Functions orchestration or Stage 1 polling for screening-complete signal)
- Open Item #41: OFAC TRUE match escalation path (Morse Compliance Officer identity)
