# Approved Products List (APL)

The APL is the mechanism by which One Fintech enforces benefit restrictions at the
point of sale. It is the primary Phase 1 differentiator — what makes a Speak Benefits
card a restricted-benefit instrument rather than a general-purpose prepaid card.

Without a correctly maintained APL, the programme cannot function.
Getting the APL wrong — uploading a stale version, or failing to upload before the
monthly reload — means members are declined at checkout for food they are entitled to buy.
This is the highest-visibility failure mode in the platform.

## How APL enforcement works (§3.1)

When a cardholder swipes at checkout, FIS checks in real time against three levels
simultaneously (for RFU — the strictest configuration):

1. **Merchant Category Code (MCC)**: Is this a food retailer?
2. **Merchant ID**: Is this specific retailer approved?
3. **Item-Level (UPC)**: Is this specific product on the eligible list?

All three must pass for a transaction to be approved.

## RFU Phase 1 APL configuration (§3.2, BRD 3/6/2026)

| APL | Purse | Scope |
|-----|-------|-------|
| RFUORFVM | Purse 1 (Fruits & Veg) | Category 5D only (narrower) |
| RFUORPANM | Purse 2 (Pantry) | Categories 5D + 5K (broader superset) |

**Concentric circle design**: Purse 1 is a strict subset of Purse 2.
Two APLs loaded monthly (BRD 3/6/2026).

## APL versioning (§3.2, §4.2.5, §4.2.6)

- Each APL upload creates an immutable row in `apl_versions`
- `apl_rules.active_version_id` is the atomic pointer to the live version
- Prior versions are NEVER deleted — complete audit trail preserved
- Activating a new APL = update `active_version_id` atomically
- `benefit_type` discriminator ensures OTC APL never routes to FOD purse

## APL upload pipeline (§B.5, Open Item #11)

1. EventBridge Scheduler fires on first business day of each month
2. APL file generator reads `apl_rules` for the target program + benefit_type
3. Assembles fixed-width FIS batch file (same format as card management files)
4. PGP-encrypts with FIS public key
5. Deletes plaintext after encryption
6. Delivers via AWS Transfer Family to FIS SFTP
7. Writes new `apl_versions` row on success
8. Atomically updates `apl_rules.active_version_id`

Compute ownership: APL uploader entry point — see `cmd/apl-uploader/` (Open Item #11, Apr 7 target).

## APL process is manual on the FIS side

APL uploads to FIS are handled via email with the FIS EBT team.
Morse must build a solution to streamline APL curation and upload (Kendra Williams).
The `apl-uploader` command automates the Morse side of this process.
