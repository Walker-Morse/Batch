package srg

import (
	"strings"
	"testing"
	"time"
)

// ─── ParseSRG310 ─────────────────────────────────────────────────────────────

func TestParseSRG310_ValidRow(t *testing.T) {
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|benefit_type\n" +
		"MEM001|John|Smith|1985-03-15|123 Main St|Portland|OR|97201|2026-07|OTC"

	rows, errs := ParseSRG310(strings.NewReader(psv))
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
	if r.FirstName != "John" {
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
	if r.PhoneNumber != 0 {
		t.Errorf("PhoneNumber: got %d, want 0 (not in file)", r.PhoneNumber)
	}
}

func TestParseSRG310_PhoneNumber(t *testing.T) {
	tests := []struct {
		name    string
		phone   string
		want    int64
		wantErr bool
	}{
		{"plain 10 digits", "5035551234", 5035551234, false},
		{"formatted with dashes", "503-555-1234", 5035551234, false},
		{"formatted with parens", "(503) 555-1234", 5035551234, false},
		{"empty", "", 0, false},
		{"11 digits with country code - too long", "15035551234", 0, true},
		{"9 digits - too short", "503555123", 0, true},
		{"non-numeric non-empty", "abc", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePhone(tt.phone)
			if (err != nil) != tt.wantErr {
				t.Errorf("parsePhone(%q) error = %v, wantErr %v", tt.phone, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parsePhone(%q) = %d, want %d", tt.phone, got, tt.want)
			}
		})
	}
}

func TestParseSRG310_PhoneNumberInRow(t *testing.T) {
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|benefit_type|phone_number\n" +
		"MEM001|John|Smith|1985-03-15|123 Main St|Portland|OR|97201|2026-07|OTC|5035551234"

	rows, errs := ParseSRG310(strings.NewReader(psv))
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs[0].Err)
	}
	if rows[0].PhoneNumber != 5035551234 {
		t.Errorf("PhoneNumber: got %d, want 5035551234", rows[0].PhoneNumber)
	}
}

func TestParseSRG310_MultipleRows(t *testing.T) {
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|benefit_type\n" +
		"MEM001|John|Smith|1985-03-15|123 Main St|Portland|OR|97201|2026-07|OTC\n" +
		"MEM002|Jane|Doe|1990-07-22|456 Oak Ave|Eugene|OR|97401|2026-07|OTC"

	rows, errs := ParseSRG310(strings.NewReader(psv))
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
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period\n" +
		"MEM001||Smith|1985-03-15|123 Main St|Portland|OR|97201|2026-07"

	rows, errs := ParseSRG310(strings.NewReader(psv))
	if len(errs) == 0 {
		t.Error("expected parse error for missing first_name")
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 valid rows, got %d", len(rows))
	}
}

func TestParseSRG310_MixedValidAndMalformed(t *testing.T) {
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|benefit_type\n" +
		"MEM001|John|Smith|1985-03-15|123 Main St|Portland|OR|97201|2026-07|OTC\n" +
		"MEM002||Smith|1985-03-15|456 Oak Ave|Eugene|OR|97401|2026-07|OTC"

	rows, errs := ParseSRG310(strings.NewReader(psv))
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
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period\n" +
		"MEM001|John|Smith|not-a-date|123 Main St|Portland|OR|97201|2026-07"

	_, errs := ParseSRG310(strings.NewReader(psv))
	if len(errs) == 0 {
		t.Error("expected parse error for invalid date")
	}
}

func TestParseSRG310_RawMapPopulated(t *testing.T) {
	psv := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|benefit_type\n" +
		"MEM001|John|Smith|1985-03-15|123 Main St|Portland|OR|97201|2026-07|OTC"

	rows, _ := ParseSRG310(strings.NewReader(psv))
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
		{"John Smith", "John Smith"},
		{"O'Brien", "O'Brien"},
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
	psv := "client_member_id|command_type|amount|benefit_period|benefit_type|effective_date\n" +
		"MEM001|LOAD|95.00|2026-07|OTC|2026-07-01"

	rows, errs := ParseSRG320(strings.NewReader(psv))
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
	psv := "client_member_id|command_type|amount|benefit_period|benefit_type|effective_date\n" +
		"MEM001|LOAD|95.00|2026-07|OTC|2026-07-01"

	rows, _ := ParseSRG320(strings.NewReader(psv))
	if len(rows) == 0 {
		t.Fatal("expected 1 row")
	}
	expiry := rows[0].ExpiryDate
	if expiry.Month() != time.July || expiry.Day() != 31 {
		t.Errorf("expiry date: got %v, want 2026-07-31 (last day of month per SOW §2.1)", expiry)
	}
}

func TestParseSRG320_InvalidAmount(t *testing.T) {
	psv := "client_member_id|command_type|amount|benefit_period|benefit_type\n" +
		"MEM001|LOAD|not-a-number|2026-07|OTC"

	_, errs := ParseSRG320(strings.NewReader(psv))
	if len(errs) == 0 {
		t.Error("expected parse error for invalid amount")
	}
}

// ─── ParseSRG315 ─────────────────────────────────────────────────────────────

func TestParseSRG315_SuspendEvent(t *testing.T) {
	psv := "client_member_id|event_type|effective_date|benefit_period|benefit_type\n" +
		"MEM001|SUSPEND|2026-07-15|2026-07|OTC"

	rows, errs := ParseSRG315(strings.NewReader(psv))
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
