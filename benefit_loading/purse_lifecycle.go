// Package benefit_loading manages benefit fund lifecycle on member purses.
//
// One Fintech actively manages purse lifecycle — tracking benefit periods, top-off
// logic, and expiry timing — and issues the appropriate FIS commands (RT60 AT30 and
// others) as the result of domain logic. FIS executes the instruction; One Fintech
// decides when and what to instruct (§2 Assumptions).
//
// Purse complexity (top-off, benefit period transitions, expiry sequencing) is a
// core platform responsibility — not a delegated FIS ACC configuration concern.
//
// The AT30/AT01 pair invariant (§4.1.4 notes):
//   Both rows share the same domain_command_id.
//   AT30 MUST reach Stage 7 COMPLETED before the paired AT01 is submitted.
//   Violation risks double-loading a purse or loading an unexpired purse.
package benefit_loading

import (
	"time"

	"github.com/walker-morse/batch/_shared/domain"
)

// PurseSequencingInvariant checks that a new purse can be issued.
// A new purse must not be issued until the prior expirePurse command
// has returned Completed status (§9 Glossary: expirePurse).
func PurseSequencingInvariant(priorCommandStatus domain.CommandStatus) bool {
	return priorCommandStatus == domain.CommandCompleted
}

// IsExpiryImminent returns true when a purse expires within the given duration.
// Used by the EventBridge Scheduler-triggered period-end sweep to identify
// purses that need AT30 processing before the 11:59 PM ET contractual deadline.
func IsExpiryImminent(expiryDate time.Time, window time.Duration) bool {
	return time.Until(expiryDate) <= window
}

// LoadAmountCents returns the benefit load amount in cents.
// Amounts must come from program configuration — never hardcoded.
// RFU: OTC=$95.00 (9500 cents), FOD=$250.00 (25000 cents) per SOW Exhibit B.
func LoadAmountCents(benefitType domain.BenefitType, programConfig map[domain.BenefitType]int64) (int64, bool) {
	amount, ok := programConfig[benefitType]
	return amount, ok
}
