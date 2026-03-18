package srg

import (
	"strings"
	"testing"
	"time"
)

// ─── ParseSRG310 ─────────────────────────────────────────────────────────────

func TestParseSRG310_ValidRow(t *testing.T) {
	csv := `client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,benefit_type
MEM001,John,Smith,1985-03-15,123 Main St,Portland,OR,97201,2026-07,OTC`

	rows, errs := ParseSRG310(strings.NewReader(csv))
	if len(errs) != 0 {
		t.Fatalf("expected 0 errors, got %d: %v", len(errs), errs[0].Err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.ClientMemberID != "MEM001" {
		t.Errorf("ClientMemberID: got %q, want %q", r.ClientMemberID, "MEM001")
	}
	if r.FirstName != "John" { // ASCIITransliterate preserves case; FIS builder uppercases at assembly
		t.Errorf("FirstName: got %q, want %q", r.FirstName, "John")
	}
	if r.State != "OR" {
		t.Errorf("State: got %q, want %q", r.State, "OR")
	}
	if r.BenefitPeriod != "2026-07" {
		t.Errorf("BenefitPeriod: got %q, want %q", r.BenefitPeriod, "2026-07")
	}
	if r.SequenceInFile != 1 {
		t.Errorf("SequenceInFile: got %d, want 1", r.SequenceInFile)
	}
}

func TestParseSRG310_MultipleRows(t *testing.T) {
	csv := `client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,benefit_type
MEM001,John,Smith,1985-03-15,123 Main St,Portland,OR,97201,2026-07,OTC
MEM002,Jane,Doe,1990-07-22,456 Oak Ave,Eugene,OR,97401,2026-07,OTC`

	rows, errs := ParseSRG310(strings.NewReader(csv))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[1].SequenceInFile != 2 {
		t.Errorf("second row SequenceInFile: got %d, want 2", rows[1].SequenceInFile)
	}
}

func TestParseSRG310_MissingRequiredField_DeadLettered(t *testing.T) {
	// Missing first_name — should produce a parse error, not a panic
	csv := `client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period
MEM001,,Smith,1985-03-15,123 Main St,Portland,OR,97201,2026-07`

	rows, errs := ParseSRG310(strings.NewReader(csv))
	if len(errs) == 0 {
		t.Error("expected parse error for missing first_name")
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 valid rows, got %d", len(rows))
	}
}

func TestParseSRG310_MixedValidAndMalformed(t *testing.T) {
	// Row 1 valid, row 2 missing required first_name
	csv := `client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,benefit_type
MEM001,John,Smith,1985-03-15,123 Main St,Portland,OR,97201,2026-07,OTC
MEM002,,Smith,1985-03-15,456 Oak Ave,Eugene,OR,97401,2026-07,OTC`

	rows, errs := ParseSRG310(strings.NewReader(csv))
	if len(rows) != 1 {
		t.Errorf("expected 1 valid row, got %d", len(rows))
	}
	if len(errs) != 1 {
		t.Errorf("expected 1 parse error, got %d", len(errs))
	}
	if errs[0].Seq != 2 {
		t.Errorf("error sequence: got %d, want 2", errs[0].Seq)
	}
}

func TestParseSRG310_InvalidDate(t *testing.T) {
	csv := `client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period
MEM001,John,Smith,not-a-date,123 Main St,Portland,OR,97201,2026-07`

	_, errs := ParseSRG310(strings.NewReader(csv))
	if len(errs) == 0 {
		t.Error("expected parse error for invalid date")
	}
}

func TestParseSRG310_RawMapPopulated(t *testing.T) {
	// raw_payload stored in dead_letter_store for replay — must be populated
	csv := `client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,benefit_type
MEM001,John,Smith,1985-03-15,123 Main St,Portland,OR,97201,2026-07,OTC`

	rows, _ := ParseSRG310(strings.NewReader(csv))
	if len(rows) == 0 {
		t.Fatal("expected 1 row")
	}
	if rows[0].Raw == nil {
		t.Error("Raw map must be populated — required for dead_letter_store replay")
	}
	if rows[0].Raw["client_member_id"] != "MEM001" {
		t.Errorf("Raw[client_member_id]: got %q, want MEM001", rows[0].Raw["client_member_id"])
	}
}

// ─── ASCIITransliterate ───────────────────────────────────────────────────────
// FIS requires ASCII-only in all fixed-width records (§2 Assumptions, Open Item #9).

func TestASCIITransliterate_Spanish(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"García", "Garcia"},
		{"Müller", "Muller"},
		{"José", "Jose"},
		{"Héctor", "Hector"},
		{"María", "Maria"},
		{"John Smith", "John Smith"}, // pure ASCII passthrough
		{"O'Brien", "O'Brien"},       // apostrophe preserved
	}
	for _, tt := range tests {
		got := ASCIITransliterate(tt.input)
		if got != tt.want {
			t.Errorf("ASCIITransliterate(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestASCIITransliterate_Empty(t *testing.T) {
	if ASCIITransliterate("") != "" {
		t.Error("empty input should return empty string")
	}
}

// ─── ParseSRG320 ─────────────────────────────────────────────────────────────

func TestParseSRG320_LoadAmount(t *testing.T) {
	csv := `client_member_id,command_type,amount,benefit_period,benefit_type,effective_date
MEM001,LOAD,95.00,2026-07,OTC,2026-07-01`

	rows, errs := ParseSRG320(strings.NewReader(csv))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].AmountCents != 9500 {
		t.Errorf("AmountCents: got %d, want 9500 ($95.00)", rows[0].AmountCents)
	}
	if rows[0].CommandType != "LOAD" {
		t.Errorf("CommandType: got %q, want LOAD", rows[0].CommandType)
	}
}

func TestParseSRG320_DefaultExpiryIsEndOfMonth(t *testing.T) {
	// No expiry_date in file — should default to last day of effective month
	csv := `client_member_id,command_type,amount,benefit_period,benefit_type,effective_date
MEM001,LOAD,95.00,2026-07,OTC,2026-07-01`

	rows, _ := ParseSRG320(strings.NewReader(csv))
	if len(rows) == 0 {
		t.Fatal("expected 1 row")
	}
	// July 1 effective → expiry should be July 31
	expiry := rows[0].ExpiryDate
	if expiry.Month() != time.July || expiry.Day() != 31 {
		t.Errorf("expiry date: got %v, want 2026-07-31 (last day of month per SOW §2.1)", expiry)
	}
}

func TestParseSRG320_InvalidAmount(t *testing.T) {
	csv := `client_member_id,command_type,amount,benefit_period,benefit_type
MEM001,LOAD,not-a-number,2026-07,OTC`

	_, errs := ParseSRG320(strings.NewReader(csv))
	if len(errs) == 0 {
		t.Error("expected parse error for invalid amount")
	}
}

// ─── ParseSRG315 ─────────────────────────────────────────────────────────────

func TestParseSRG315_SuspendEvent(t *testing.T) {
	csv := `client_member_id,event_type,effective_date,benefit_period,benefit_type
MEM001,SUSPEND,2026-07-15,2026-07,OTC`

	rows, errs := ParseSRG315(strings.NewReader(csv))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(rows) == 0 {
		t.Fatal("expected 1 row")
	}
	if rows[0].EventType != "SUSPEND" {
		t.Errorf("EventType: got %q, want SUSPEND", rows[0].EventType)
	}
}
