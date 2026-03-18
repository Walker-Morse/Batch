package domain

import "testing"

// ─── DeriveType ──────────────────────────────────────────────────────────────
// Mirrors the PostgreSQL GENERATED column: LEFT(fis_purse_name, 3).
// purse_type is the primary Tableau filter key for benefit-type aggregation (§4.3.3).
// Inconsistency between Go and DB would break Tableau OTC/FOD filtering.

func TestDeriveType_Standard(t *testing.T) {
	tests := []struct {
		purseName string
		want      string
	}{
		{"OTC2550", "OTC"}, // Fruits & Veg purse (RFU Purse 1)
		{"FOD2550", "FOD"}, // Pantry Foods purse (RFU Purse 2)
		{"CMB1000", "CMB"}, // Combined purse
	}
	for _, tt := range tests {
		got := DeriveType(tt.purseName)
		if got != tt.want {
			t.Errorf("DeriveType(%q) = %q, want %q", tt.purseName, got, tt.want)
		}
	}
}

func TestDeriveType_ExactlyThreeChars(t *testing.T) {
	got := DeriveType("OTC")
	if got != "OTC" {
		t.Errorf("DeriveType(%q) = %q, want %q", "OTC", got, "OTC")
	}
}

func TestDeriveType_ShortName(t *testing.T) {
	// Shorter than 3 chars — return what we have rather than panic
	got := DeriveType("OT")
	if got != "OT" {
		t.Errorf("DeriveType(%q) = %q, want %q", "OT", got, "OT")
	}
}

func TestDeriveType_Empty(t *testing.T) {
	got := DeriveType("")
	if got != "" {
		t.Errorf("DeriveType(%q) = %q, want empty", "", got)
	}
}

// ─── Consumer.IsResolved ─────────────────────────────────────────────────────
// Returns true when FIS has confirmed the RT30 and assigned FIS identifiers.
// False until Stage 7 reconciliation populates fis_person_id and fis_cuid.
// Cards should not be considered issued until IsResolved() is true.

func TestConsumer_IsResolved_TrueWhenBothPopulated(t *testing.T) {
	personID := "P123456"
	cuid := "C789012"
	c := &Consumer{FISPersonID: &personID, FISCUID: &cuid}
	if !c.IsResolved() {
		t.Error("expected IsResolved() = true when both FIS identifiers are set")
	}
}

func TestConsumer_IsResolved_FalseWhenPersonIDNil(t *testing.T) {
	cuid := "C789012"
	c := &Consumer{FISPersonID: nil, FISCUID: &cuid}
	if c.IsResolved() {
		t.Error("expected IsResolved() = false when FISPersonID is nil (Stage 7 not complete)")
	}
}

func TestConsumer_IsResolved_FalseWhenCUIDNil(t *testing.T) {
	personID := "P123456"
	c := &Consumer{FISPersonID: &personID, FISCUID: nil}
	if c.IsResolved() {
		t.Error("expected IsResolved() = false when FISCUID is nil (Stage 7 not complete)")
	}
}

func TestConsumer_IsResolved_FalseWhenBothNil(t *testing.T) {
	c := &Consumer{FISPersonID: nil, FISCUID: nil}
	if c.IsResolved() {
		t.Error("expected IsResolved() = false when neither FIS identifier is set (pre-Stage 7)")
	}
}

// ─── CommandStatus constants ──────────────────────────────────────────────────
// These string values are written directly to the domain_commands table.
// Wrong values break the idempotency gate — a critical pipeline invariant.

func TestCommandStatusValues(t *testing.T) {
	tests := []struct{ name string; got CommandStatus; want string }{
		{"Accepted",  CommandAccepted,  "Accepted"},
		{"Completed", CommandCompleted, "Completed"},
		{"Failed",    CommandFailed,    "Failed"},
		{"Duplicate", CommandDuplicate, "Duplicate"},
	}
	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("CommandStatus %s: got %q, want %q — breaks domain_commands idempotency gate", tt.name, tt.got, tt.want)
		}
	}
}

// ─── BenefitType constants ────────────────────────────────────────────────────
// These string values are written to purses.benefit_type and route APL enforcement.
// Wrong values mean the wrong APL is applied at point-of-sale.

func TestBenefitTypeValues(t *testing.T) {
	tests := []struct{ name string; got BenefitType; want string }{
		{"OTC", BenefitOTC, "OTC"},
		{"FOD", BenefitFOD, "FOD"},
		{"CMB", BenefitCMB, "CMB"},
	}
	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("BenefitType %s: got %q, want %q — wrong value routes to wrong APL", tt.name, tt.got, tt.want)
		}
	}
}

// ─── FailureStage constants ───────────────────────────────────────────────────
// Written to dead_letter_store.failure_stage.
// These values must match the PostgreSQL CHECK constraint exactly.

func TestFailureStageValues(t *testing.T) {
	tests := []struct{ name string; got FailureStage; want string }{
		{"validation",       FailureValidation,     "validation"},
		{"row_processing",   FailureRowProcessing,  "row_processing"},
		{"batch_assembly",   FailureBatchAssembly,  "batch_assembly"},
		{"fis_transfer",     FailureFISTransfer,    "fis_transfer"},
		{"return_file_wait", FailureReturnFileWait, "return_file_wait"},
		{"reconciliation",   FailureReconciliation, "reconciliation"},
	}
	for _, tt := range tests {
		if string(tt.got) != tt.want {
			t.Errorf("FailureStage %s: got %q, want %q — must match PostgreSQL CHECK constraint", tt.name, tt.got, tt.want)
		}
	}
}
