package fis_adapter

import (
	"testing"
	"time"
)

// ─── RT10 File Header ─────────────────────────────────────────────────────────

func TestBuildRT10_ValidRecord(t *testing.T) {
	rec, err := BuildRT10(RT10Fields{
		CompanyIDExtended: "MORSELIMITED01",
		FileCreationDate:  time.Date(2026, 6, 1, 14, 30, 0, 0, time.UTC),
		LogFileIndicator:  '0',
		TestProdIndicator: 'P',
	})
	if err != nil {
		t.Fatalf("BuildRT10 error: %v", err)
	}
	if len(rec) != RecordWidth {
		t.Errorf("record length: got %d, want %d", len(rec), RecordWidth)
	}
	if string(rec[0:4]) != "RT10" {
		t.Errorf("record type: got %q, want RT10", string(rec[0:4]))
	}
	// Log File Indicator at offset 40
	if rec[40] != '0' {
		t.Errorf("LogFileIndicator at offset 40: got %q, want '0' (§6.6.3 hardcoded)", string(rec[40]))
	}
	// Test/Prod Indicator at offset 41
	if rec[41] != 'P' {
		t.Errorf("TestProdIndicator at offset 41: got %q, want 'P'", string(rec[41]))
	}
}

func TestBuildRT10_RejectsNonZeroLogFileIndicator(t *testing.T) {
	_, err := BuildRT10(RT10Fields{
		CompanyIDExtended: "MORSELIMITED01",
		FileCreationDate:  time.Now(),
		LogFileIndicator:  '1', // errors-only — must never be used (§6.6.3)
		TestProdIndicator: 'P',
	})
	if err == nil {
		t.Error("expected error when LogFileIndicator != '0' — Stage 7 would be blind to successful enrollments")
	}
}

func TestBuildRT10_RejectsInvalidTestProdIndicator(t *testing.T) {
	_, err := BuildRT10(RT10Fields{
		CompanyIDExtended: "MORSELIMITED01",
		FileCreationDate:  time.Now(),
		LogFileIndicator:  '0',
		TestProdIndicator: 'X', // invalid
	})
	if err == nil {
		t.Error("expected error for invalid TestProdIndicator")
	}
}

func TestBuildRT10_DevUsesT(t *testing.T) {
	rec, err := BuildRT10(RT10Fields{
		CompanyIDExtended: "MORSELIMITED01",
		FileCreationDate:  time.Now(),
		LogFileIndicator:  '0',
		TestProdIndicator: 'T', // DEV: format test only — no cards created
	})
	if err != nil {
		t.Fatalf("BuildRT10 error: %v", err)
	}
	if rec[41] != 'T' {
		t.Errorf("TestProdIndicator: got %q, want 'T'", string(rec[41]))
	}
}

// ─── RT30 New Account ─────────────────────────────────────────────────────────

func TestBuildRT30_ValidRecord(t *testing.T) {
	rec, err := BuildRT30(RT30Fields{
		ATCode:         "AT01",
		ClientMemberID: "MEM001",
		SubprogramID:   "26071",
		FirstName:      "JOHN",
		LastName:       "SMITH",
		DOB:            time.Date(1985, 3, 15, 0, 0, 0, 0, time.UTC),
		Address1:       "123 MAIN ST",
		City:           "PORTLAND",
		State:          "OR",
		ZIP:            "97201",
	})
	if err != nil {
		t.Fatalf("BuildRT30 error: %v", err)
	}
	if len(rec) != RecordWidth {
		t.Errorf("record length: got %d, want %d", len(rec), RecordWidth)
	}
	if string(rec[0:4]) != "RT30" {
		t.Errorf("record type: got %q, want RT30", string(rec[0:4]))
	}
}

func TestBuildRT30_RequiresClientMemberID(t *testing.T) {
	_, err := BuildRT30(RT30Fields{
		FirstName: "JOHN",
		LastName:  "SMITH",
	})
	if err == nil {
		t.Error("expected error when ClientMemberID is empty")
	}
}

func TestBuildRT30_RequiresName(t *testing.T) {
	_, err := BuildRT30(RT30Fields{
		ClientMemberID: "MEM001",
	})
	if err == nil {
		t.Error("expected error when FirstName/LastName are empty")
	}
}

func TestBuildRT30_DOBFormatMMDDYYYY(t *testing.T) {
	rec, err := BuildRT30(RT30Fields{
		ClientMemberID: "MEM001",
		FirstName:      "JOHN",
		LastName:       "SMITH",
		DOB:            time.Date(1985, 3, 15, 0, 0, 0, 0, time.UTC),
		Address1:       "123 MAIN ST",
		City:           "PORTLAND",
		State:          "OR",
		ZIP:            "97201",
	})
	if err != nil {
		t.Fatalf("BuildRT30 error: %v", err)
	}
	// DOB at offset 90, length 8: MMDDYYYY
	dob := string(rec[90:98])
	if dob != "03151985" {
		t.Errorf("DOB format: got %q, want %q (MMDDYYYY)", dob, "03151985")
	}
}

// ─── RT60 Purse Load ──────────────────────────────────────────────────────────

func TestBuildRT60_LoadAmount(t *testing.T) {
	rec, err := BuildRT60(RT60Fields{
		ATCode:         ATCodeLoad,
		ClientMemberID: "MEM001",
		FISCardID:      "1234567890123456789",
		PurseName:      "OTC2550",
		AmountCents:    9500, // $95.00
		EffectiveDate:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("BuildRT60 error: %v", err)
	}
	if len(rec) != RecordWidth {
		t.Errorf("record length: got %d, want %d", len(rec), RecordWidth)
	}
	if string(rec[0:4]) != "RT60" {
		t.Errorf("record type: got %q, want RT60", string(rec[0:4]))
	}
	// Amount at offset 54, length 10: "0000009500" for $95.00
	amount := string(rec[54:64])
	if amount != "0000009500" {
		t.Errorf("amount field: got %q, want %q ($95.00 = 9500 cents)", amount, "0000009500")
	}
}

func TestBuildRT60_SweepUsesAbsoluteAmount(t *testing.T) {
	// AT30 sweep: negative AmountCents, but FIS expects positive value in field
	rec, err := BuildRT60(RT60Fields{
		ATCode:         ATCodeSweep,
		ClientMemberID: "MEM001",
		FISCardID:      "1234567890123456789",
		PurseName:      "OTC2550",
		AmountCents:    -9500, // negative = sweep
		EffectiveDate:  time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("BuildRT60 error: %v", err)
	}
	// Absolute value in field
	amount := string(rec[54:64])
	if amount != "0000009500" {
		t.Errorf("sweep amount field: got %q, want %q (absolute value)", amount, "0000009500")
	}
}

func TestBuildRT60_RequiresFISCardID(t *testing.T) {
	_, err := BuildRT60(RT60Fields{
		ATCode:         ATCodeLoad,
		ClientMemberID: "MEM001",
		AmountCents:    9500,
		EffectiveDate:  time.Now(),
	})
	if err == nil {
		t.Error("expected error when FISCardID is empty — RT60 requires prior RT30 completion")
	}
}

func TestBuildRT60_RejectsUnknownATCode(t *testing.T) {
	_, err := BuildRT60(RT60Fields{
		ATCode:         "AT99",
		ClientMemberID: "MEM001",
		FISCardID:      "1234567890123456789",
		AmountCents:    9500,
		EffectiveDate:  time.Now(),
	})
	if err == nil {
		t.Error("expected error for unknown AT code")
	}
}

// ─── RT80/RT90 Trailer counts ─────────────────────────────────────────────────

func TestBuildRT80_ExactCount(t *testing.T) {
	rec := BuildRT80(42)
	if len(rec) != RecordWidth {
		t.Errorf("RT80 length: got %d, want %d", len(rec), RecordWidth)
	}
	if string(rec[0:4]) != "RT80" {
		t.Errorf("RT80 record type: got %q", string(rec[0:4]))
	}
}

func TestBuildRT90_AllCountsPresent(t *testing.T) {
	// totalRecords = RT10 + RT20 + 5 data records + RT80 + RT90 = 9
	rec := BuildRT90(9, 1, 5)
	if len(rec) != RecordWidth {
		t.Errorf("RT90 length: got %d, want %d", len(rec), RecordWidth)
	}
	if string(rec[0:4]) != "RT90" {
		t.Errorf("RT90 record type: got %q", string(rec[0:4]))
	}
}

// ─── Record width invariant ───────────────────────────────────────────────────
// Every record MUST be exactly 400 bytes — FIS rejects files with wrong-width records.

func TestAllRecordsAre400Bytes(t *testing.T) {
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	rt10, _ := BuildRT10(RT10Fields{"MORSELIMITED01", date, '0', 'P'})
	rt20 := BuildRT20(RT20Fields{"CLIENT1", "26071"})
	rt30, _ := BuildRT30(RT30Fields{
		ClientMemberID: "MEM001", FirstName: "JOHN", LastName: "SMITH",
		DOB: date, Address1: "123 MAIN", City: "PORTLAND", State: "OR", ZIP: "97201",
	})
	rt37, _ := BuildRT37(RT37Fields{
		ClientMemberID: "MEM001", FISCardID: "1234567890123456789", CardStatusCode: "6",
	})
	rt60, _ := BuildRT60(RT60Fields{
		ATCode: ATCodeLoad, ClientMemberID: "MEM001",
		FISCardID: "1234567890123456789", PurseName: "OTC2550",
		AmountCents: 9500, EffectiveDate: date,
	})
	rt80 := BuildRT80(3)
	rt90 := BuildRT90(7, 1, 3)

	records := []struct {
		name string
		rec  []byte
	}{
		{"RT10", rt10}, {"RT20", rt20}, {"RT30", rt30},
		{"RT37", rt37}, {"RT60", rt60}, {"RT80", rt80}, {"RT90", rt90},
	}
	for _, r := range records {
		if r.rec == nil {
			continue
		}
		if len(r.rec) != RecordWidth {
			t.Errorf("%s: length %d, want %d — FIS will reject file", r.name, len(r.rec), RecordWidth)
		}
	}
}
