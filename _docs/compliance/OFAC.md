# OFAC / Sanctions Screening Compliance

Source: HLD Addendum H, ADR-011

## Regulatory Basis

- BSA/PATRIOT Act § 326 (31 U.S.C. § 5318(l)): CIP requirements for prepaid card programs
- OFAC regulations (31 CFR § 501 et seq.): prohibit transactions with SDNs and blocked persons
- MVB Program Standards §§ 10.3–10.3.1: contractual screening obligations

## Required Lists (MVB § 10.3.1)

| List | Source |
|------|--------|
| OFAC SDN | US Treasury — Specially Designated Nationals |
| OFAC Non-SDN Consolidated | FSE, SSI, and other non-SDN lists |
| OFAC Sanctioned Countries | Comprehensive sanctions programs |
| FinCEN 311 and 9714 Special Measures | 31 U.S.C. §§ 5318A and 9714 |

## Architecture (ADR-011)

See `sanctions_screening/README.md` for full architecture.

**Onboarding**: OFAC-API.com pre-pipeline Lambda, batches of 500, minScore=95
**Ongoing**: sanctions.io Continuous Monitoring, enrolled at Stage 7 RT30 Completed

## Open Items

| Item | Owner | Target |
|------|-------|--------|
| #41: OFAC TRUE match escalation path | Kyle Walker / Legal | Before go-live |
| #43: MVB written notification of tools, lists, parties, frequency | Kendra Williams | Before go-live |

## Documentation and Retention

All screening results (including negatives) must be saved to PDF.
TRUE match escalations and all remediation actions must be documented.
Screening run records captured in `audit_log` as compliance event class.
