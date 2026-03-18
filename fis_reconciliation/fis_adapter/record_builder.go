package fis_adapter

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// record builds a single 400-byte FIS fixed-width record.
// FIS Generic Batch Loader Reference Guide v37 is the authoritative source
// for all field offsets and lengths. All fields are ASCII only — the SRG parser
// applies ASCIITransliterate before records reach here.
//
// Field conventions:
//   - Text fields: left-aligned, space-padded to field width
//   - Numeric fields: right-aligned, zero-padded
//   - Date fields: MMDDYYYY format
//   - Amount fields: right-aligned, zero-padded, implied 2 decimal places (cents / 100)
type record [RecordWidth]byte

// set writes value into the record at the given 0-based offset for the given length.
// Panics if offset+length > RecordWidth — would indicate a programming error in
// the field table, not a data error.
func (r *record) set(offset, length int, value string) {
	if offset+length > RecordWidth {
		panic(fmt.Sprintf("fis record: field at offset %d length %d exceeds record width %d", offset, length, RecordWidth))
	}
	// Pad or truncate to exactly length bytes
	v := []byte(value)
	for i := 0; i < length; i++ {
		if i < len(v) {
			b := v[i]
			// Enforce ASCII-only — replace any non-ASCII byte with space
			if b > 127 || !isPrintableASCII(rune(b)) {
				b = ' '
			}
			r[offset+i] = b
		} else {
			r[offset+i] = ' ' // space-pad
		}
	}
}

// setRight writes a right-aligned zero-padded numeric value.
func (r *record) setRight(offset, length int, value string) {
	padded := fmt.Sprintf("%0*s", length, value)
	if len(padded) > length {
		padded = padded[len(padded)-length:]
	}
	r.set(offset, length, padded)
}

// bytes returns the record as a byte slice (always exactly RecordWidth bytes).
func (r *record) bytes() []byte {
	b := make([]byte, RecordWidth)
	copy(b, r[:])
	return b
}

func isPrintableASCII(r rune) bool {
	return r >= 32 && r < 127 && unicode.IsPrint(r)
}

// ─── RT10 File Header ─────────────────────────────────────────────────────────

// RT10Fields carries the values needed to build an RT10 File Header record.
type RT10Fields struct {
	CompanyIDExtended string    // FIS Level 1 client identifier for Morse LLC
	FileCreationDate  time.Time
	LogFileIndicator  byte // MUST be '0' — returns all records (§6.6.3)
	TestProdIndicator byte // 'T' (DEV) or 'P' (TST/PRD) — from PIPELINE_ENV (§6.6.4)
}

// BuildRT10 constructs the mandatory File Header record.
// Log File Indicator is validated to be '0' — any other value is rejected.
// Per §6.6.3: setting to '1' (errors only) makes Stage 7 blind to successful
// enrollments and unable to capture FIS-assigned card IDs.
func BuildRT10(f RT10Fields) ([]byte, error) {
	if f.LogFileIndicator != '0' {
		return nil, fmt.Errorf("RT10: LogFileIndicator MUST be '0' (return all records per §6.6.3), got %q", string(f.LogFileIndicator))
	}
	if f.TestProdIndicator != 'T' && f.TestProdIndicator != 'P' {
		return nil, fmt.Errorf("RT10: TestProdIndicator must be 'T' or 'P', got %q", string(f.TestProdIndicator))
	}

	var r record
	// Fill with spaces
	for i := range r {
		r[i] = ' '
	}

	r.set(0, 4, "RT10")                                       // Record Type
	r.set(4, 8, f.FileCreationDate.Format("01022006"))         // File Creation Date MMDDYYYY
	r.set(12, 6, f.FileCreationDate.Format("150405"))          // File Creation Time HHMMSS
	r.set(18, 20, f.CompanyIDExtended)                         // Company ID Extended
	r.set(38, 2, "01")                                         // Format Version
	r[40] = f.LogFileIndicator                                  // Log File Indicator: '0' (§6.6.3)
	r[41] = f.TestProdIndicator                                 // Test/Prod Indicator (§6.6.4)

	return r.bytes(), nil
}

// ─── RT20 Batch Header ────────────────────────────────────────────────────────

// RT20Fields carries values for the Batch Header record.
type RT20Fields struct {
	ClientID     string // RFU FIS client identifier
	SubprogramID string // e.g. "26071" for RFU
}

// BuildRT20 constructs the Batch Header record.
// Phase 1: one client per file, so each file has exactly one RT20.
func BuildRT20(f RT20Fields) []byte {
	var r record
	for i := range r {
		r[i] = ' '
	}
	r.set(0, 4, "RT20")
	r.set(4, 15, f.ClientID)
	r.set(19, 10, f.SubprogramID)
	return r.bytes()
}

// ─── RT30 New Account / Card Issuance ─────────────────────────────────────────

// RT30Fields carries all fields needed to build an RT30 record.
type RT30Fields struct {
	ATCode         string    // action type
	ClientMemberID string
	SubprogramID   string
	PackageID      string
	FirstName      string    // already ASCII-transliterated
	LastName       string    // already ASCII-transliterated
	DOB            time.Time
	Address1       string
	Address2       string
	City           string
	State          string // 2-char
	ZIP            string
	Email          string
	CardDesignID   string
	CustomCardID   string
}

// BuildRT30 constructs a new account / card issuance record.
// All text values must be ASCII — the SRG parser enforces this.
func BuildRT30(f RT30Fields) ([]byte, error) {
	if f.ClientMemberID == "" {
		return nil, fmt.Errorf("RT30: ClientMemberID is required")
	}
	if f.FirstName == "" || f.LastName == "" {
		return nil, fmt.Errorf("RT30: FirstName and LastName are required")
	}

	var r record
	for i := range r {
		r[i] = ' '
	}

	r.set(0, 4, "RT30")
	r.set(4, 4, f.ATCode)
	r.set(8, 20, f.ClientMemberID)
	r.set(28, 10, f.SubprogramID)
	r.set(38, 10, f.PackageID)
	r.set(48, 26, formatName(f.LastName))
	r.set(74, 16, formatName(f.FirstName))
	r.set(90, 8, formatDate(f.DOB))    // DOB: MMDDYYYY
	r.set(98, 30, f.Address1)
	r.set(128, 30, f.Address2)
	r.set(158, 20, f.City)
	r.set(178, 2, strings.ToUpper(f.State))
	r.set(180, 10, f.ZIP)
	r.set(190, 50, f.Email)
	r.set(240, 10, f.CardDesignID)
	r.set(250, 20, f.CustomCardID)

	return r.bytes(), nil
}

// ─── RT37 Card Status Update ─────────────────────────────────────────────────

// RT37Fields carries fields for a card status update record.
type RT37Fields struct {
	ATCode         string
	ClientMemberID string
	FISCardID      string
	CardStatusCode string // "2"=Active "4"=Lost "6"=Suspended "7"=Closed
	ReasonCode     string
}

// BuildRT37 constructs a card status update record.
func BuildRT37(f RT37Fields) ([]byte, error) {
	if f.FISCardID == "" {
		return nil, fmt.Errorf("RT37: FISCardID is required (RT30 must be completed first)")
	}

	var r record
	for i := range r {
		r[i] = ' '
	}

	r.set(0, 4, "RT37")
	r.set(4, 4, f.ATCode)
	r.set(8, 20, f.ClientMemberID)
	r.set(28, 19, f.FISCardID)
	r.set(47, 1, f.CardStatusCode)
	r.set(48, 4, f.ReasonCode)

	return r.bytes(), nil
}

// ─── RT60 Purse Load / Fund Transfer ─────────────────────────────────────────

// RT60Fields carries fields for a purse load or period-end sweep.
type RT60Fields struct {
	ATCode          string    // "AT01"=load, "AT30"=sweep
	ClientMemberID  string
	FISCardID       string
	PurseName       string    // ACC PurseName e.g. "OTC2550"
	AmountCents     int64     // positive=load, negative=sweep
	EffectiveDate   time.Time
	ClientReference string
}

// BuildRT60 constructs a purse load or fund transfer record.
// AmountCents is converted to a 10-digit dollar amount with 2 implied decimals.
// AT30 sweeps use a negative amount to zero the purse balance.
func BuildRT60(f RT60Fields) ([]byte, error) {
	if f.FISCardID == "" {
		return nil, fmt.Errorf("RT60: FISCardID is required")
	}
	if f.ATCode != ATCodeLoad && f.ATCode != ATCodeSweep {
		return nil, fmt.Errorf("RT60: ATCode must be AT01 or AT30, got %q", f.ATCode)
	}

	// Format amount: absolute value in cents → "0000009500" for $95.00
	absCents := f.AmountCents
	if absCents < 0 {
		absCents = -absCents
	}
	amountStr := fmt.Sprintf("%010d", absCents)

	var r record
	for i := range r {
		r[i] = ' '
	}

	r.set(0, 4, "RT60")
	r.set(4, 4, f.ATCode)
	r.set(8, 20, f.ClientMemberID)
	r.set(28, 19, f.FISCardID)
	r.set(47, 7, f.PurseName)
	r.set(54, 10, amountStr)           // amount: 10 digits, 2 implied decimals
	r.set(64, 8, formatDate(f.EffectiveDate))
	r.set(72, 20, f.ClientReference)

	return r.bytes(), nil
}

// ─── RT80 Batch Trailer ───────────────────────────────────────────────────────

// BuildRT80 constructs the Batch Trailer record.
// recordCount must be the exact number of data records in this batch.
// A mismatch causes an RT99 pre-processing halt for the entire file (§6.5.1).
func BuildRT80(recordCount int) []byte {
	var r record
	for i := range r {
		r[i] = ' '
	}
	r.set(0, 4, "RT80")
	r.setRight(4, 9, fmt.Sprintf("%d", recordCount)) // zero-padded record count
	return r.bytes()
}

// ─── RT90 File Trailer ────────────────────────────────────────────────────────

// BuildRT90 constructs the mandatory File Trailer record.
// ALL THREE counts must be exact — FIS validates in its pre-processing pass.
// A mismatch causes an RT99 pre-processing halt (§6.5.1, §6.6.2).
// RT90 is ALWAYS written last, after all data records are confirmed.
func BuildRT90(totalRecords, batchCount, detailRecordCount int) []byte {
	var r record
	for i := range r {
		r[i] = ' '
	}
	r.set(0, 4, "RT90")
	r.setRight(4, 9, fmt.Sprintf("%d", totalRecords))      // all records including headers/trailers
	r.setRight(13, 6, fmt.Sprintf("%d", batchCount))        // number of RT20 batches
	r.setRight(19, 9, fmt.Sprintf("%d", detailRecordCount)) // data records only
	return r.bytes()
}

// ─── format helpers ───────────────────────────────────────────────────────────

// formatDate returns a date in MMDDYYYY format as required by FIS.
func formatDate(t time.Time) string {
	if t.IsZero() {
		return "        " // 8 spaces for missing dates
	}
	return t.Format("01022006")
}

// formatName uppercases and strips non-ASCII characters from a name field.
func formatName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r < 128 && isPrintableASCII(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
