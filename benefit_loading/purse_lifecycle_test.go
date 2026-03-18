package benefit_loading

import (
	"testing"
	"time"

	"github.com/walker-morse/batch/_shared/domain"
)

// ─── PurseSequencingInvariant ────────────────────────────────────────────────
// Contractual rule: a new purse must not be issued until the prior expirePurse
// command has returned Completed status (§9 Glossary, §4.1.4).
// Violation risks double-loading a purse or loading an unexpired purse.

func TestPurseSequencingInvariant_AllowsWhenCompleted(t *testing.T) {
	if !PurseSequencingInvariant(domain.CommandCompleted) {
		t.Error("expected true when prior command is Completed")
	}
}

func TestPurseSequencingInvariant_BlocksWhenAccepted(t *testing.T) {
	if PurseSequencingInvariant(domain.CommandAccepted) {
		t.Error("must not allow new purse while prior command is still Accepted (in-flight)")
	}
}

func TestPurseSequencingInvariant_BlocksWhenFailed(t *testing.T) {
	if PurseSequencingInvariant(domain.CommandFailed) {
		t.Error("must not allow new purse when prior command Failed — requires manual resolution")
	}
}

func TestPurseSequencingInvariant_BlocksWhenDuplicate(t *testing.T) {
	if PurseSequencingInvariant(domain.CommandDuplicate) {
		t.Error("must not allow new purse when prior command returned Duplicate — state unclear")
	}
}

// ─── IsExpiryImminent ────────────────────────────────────────────────────────
// Used by the EventBridge Scheduler-triggered period-end sweep to identify purses
// needing AT30 before the 11:59 PM ET contractual deadline (SOW §2.1, §3.3).

func TestIsExpiryImminent_TrueWhenWithinWindow(t *testing.T) {
	// Expires in 1 hour, window is 4 hours — imminent
	expiry := time.Now().Add(1 * time.Hour)
	if !IsExpiryImminent(expiry, 4*time.Hour) {
		t.Error("expected imminent when expiry is within window")
	}
}

func TestIsExpiryImminent_TrueWhenAlreadyExpired(t *testing.T) {
	// Already past expiry
	expiry := time.Now().Add(-1 * time.Minute)
	if !IsExpiryImminent(expiry, 4*time.Hour) {
		t.Error("expected imminent when purse is already expired")
	}
}

func TestIsExpiryImminent_FalseWhenFarFuture(t *testing.T) {
	// Expires in 30 days, window is 4 hours — not imminent
	expiry := time.Now().Add(30 * 24 * time.Hour)
	if IsExpiryImminent(expiry, 4*time.Hour) {
		t.Error("expected not imminent when expiry is 30 days away")
	}
}

func TestIsExpiryImminent_TrueExactlyAtBoundary(t *testing.T) {
	// Expires in exactly the window duration — boundary condition
	// time.Until will return <= window, so this should be true
	expiry := time.Now().Add(4 * time.Hour)
	// Give 1 second of slack for test execution time
	if !IsExpiryImminent(expiry, 4*time.Hour+time.Second) {
		t.Error("expected imminent at window boundary")
	}
}

// ─── LoadAmountCents ─────────────────────────────────────────────────────────
// Amounts must come from program configuration — never hardcoded (§2 Assumptions).
// RFU: OTC=$95.00 (9500 cents), FOD=$250.00 (25000 cents) per SOW Exhibit B.

func TestLoadAmountCents_ReturnsConfiguredAmount(t *testing.T) {
	config := map[domain.BenefitType]int64{
		domain.BenefitOTC: 9500,  // $95.00 RFU OTC
		domain.BenefitFOD: 25000, // $250.00 RFU FOD
	}

	amount, ok := LoadAmountCents(domain.BenefitOTC, config)
	if !ok {
		t.Fatal("expected OTC amount to be found in config")
	}
	if amount != 9500 {
		t.Errorf("expected 9500 cents, got %d", amount)
	}
}

func TestLoadAmountCents_FODAmount(t *testing.T) {
	config := map[domain.BenefitType]int64{
		domain.BenefitOTC: 9500,
		domain.BenefitFOD: 25000,
	}

	amount, ok := LoadAmountCents(domain.BenefitFOD, config)
	if !ok {
		t.Fatal("expected FOD amount to be found in config")
	}
	if amount != 25000 {
		t.Errorf("expected 25000 cents, got %d", amount)
	}
}

func TestLoadAmountCents_ReturnsFalseWhenNotConfigured(t *testing.T) {
	config := map[domain.BenefitType]int64{
		domain.BenefitOTC: 9500,
	}

	_, ok := LoadAmountCents(domain.BenefitFOD, config)
	if ok {
		t.Error("expected false when benefit type not in config — must not assume defaults")
	}
}

func TestLoadAmountCents_EmptyConfigReturnsFalse(t *testing.T) {
	_, ok := LoadAmountCents(domain.BenefitOTC, map[domain.BenefitType]int64{})
	if ok {
		t.Error("expected false for empty config")
	}
}
