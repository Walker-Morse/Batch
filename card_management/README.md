# Card Management

Manages the physical prepaid card lifecycle: issuance, status updates (suspend,
unsuspend, lost, close), and replacement card processing.

## What this package does

Translates SRG310 enrollment records into RT30 (new account / card issuance) and
RT37 (card status update) FIS batch records. Tracks card lifecycle from issuance
through all status transitions.

## Card lifecycle (§4.2.2)

```
RT30 submitted → FIS return file → card.fis_card_id populated (Stage 7)
                                 → card.issued_at set
                                 → card.status = Active (2)
RT37 status 6 → Suspended
RT37 status 4 → Lost
RT37 status 7 → Closed
```

## FISCardID opt-in (Open Item: Selvi Marappan)

`fis_card_id` is a FIS opt-in field. Morse UAT is currently NOT opted in —
the field is empty on all UAT NONMON rows. Until opt-in is enabled:
- Join strategy falls back to PAN proxy
- `dim_card.fis_card_id` is NULL for all existing cards

Once enabled, `fis_card_id` becomes the preferred join key for all FIS XTRACT feeds.
Action owner: Selvi Marappan.

## RT37 dependency on RT30 completion

`fis_card_id` must be populated from a prior RT30 return before RT37 can be staged.
A dead-lettered RT37 where `fis_card_id` is NULL means the member's RT30 has not
yet completed Stage 7 reconciliation (§4.1.3 notes).

## BIN 650463

The confirmed Morse BIN for Phase 1. Physical card issuance via batch pipeline.
Digital card provisioning is out of scope (Digital Wallet team, RQ-008).
