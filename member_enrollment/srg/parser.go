package srg

import (
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ParseSRG310 parses a decrypted SRG310 pipe-delimited reader into typed rows.
// Returns rows and a slice of parse errors (one per malformed row).
// Parse errors do not abort — malformed rows are collected and sent to
// dead_letter_store by Stage 2 after this returns.
//
// ASCII transliteration is applied to all demographic text fields.
// Non-ASCII characters (e.g. Spanish accented names) are NFD-normalised
// and combining characters stripped, producing ASCII-safe strings suitable
// for the FIS 400-byte fixed-width record format (§2 Assumptions, Open Item #9).
func ParseSRG310(r io.Reader) ([]*SRG310Row, []ParseError) {
	reader := csv.NewReader(r)
	reader.Comma = '|'
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // flexible — we validate required fields manually

	header, err := reader.Read()
	if err != nil {
		return nil, []ParseError{{Seq: 0, Err: fmt.Errorf("read header: %w", err)}}
	}
	colIdx := headerIndex(header)

	var rows []*SRG310Row
	var errs []ParseError
	seq := 0

	for {
		seq++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, ParseError{Seq: seq, Err: fmt.Errorf("csv read: %w", err)})
			continue
		}

		row, parseErr := parseSRG310Row(seq, colIdx, record)
		if parseErr != nil {
			errs = append(errs, ParseError{Seq: seq, Err: parseErr, RawRecord: record})
			continue
		}
		rows = append(rows, row)
	}

	return rows, errs
}

// ParseSRG315 parses a decrypted SRG315 pipe-delimited reader into typed rows.
func ParseSRG315(r io.Reader) ([]*SRG315Row, []ParseError) {
	reader := csv.NewReader(r)
	reader.Comma = '|'
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, []ParseError{{Seq: 0, Err: fmt.Errorf("read header: %w", err)}}
	}
	colIdx := headerIndex(header)

	var rows []*SRG315Row
	var errs []ParseError
	seq := 0

	for {
		seq++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, ParseError{Seq: seq, Err: err, RawRecord: record})
			continue
		}

		row, parseErr := parseSRG315Row(seq, colIdx, record)
		if parseErr != nil {
			errs = append(errs, ParseError{Seq: seq, Err: parseErr, RawRecord: record})
			continue
		}
		rows = append(rows, row)
	}

	return rows, errs
}

// ParseSRG320 parses a decrypted SRG320 pipe-delimited reader into typed rows.
func ParseSRG320(r io.Reader) ([]*SRG320Row, []ParseError) {
	reader := csv.NewReader(r)
	reader.Comma = '|'
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1

	header, err := reader.Read()
	if err != nil {
		return nil, []ParseError{{Seq: 0, Err: fmt.Errorf("read header: %w", err)}}
	}
	colIdx := headerIndex(header)

	var rows []*SRG320Row
	var errs []ParseError
	seq := 0

	for {
		seq++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, ParseError{Seq: seq, Err: err, RawRecord: record})
			continue
		}

		row, parseErr := parseSRG320Row(seq, colIdx, record)
		if parseErr != nil {
			errs = append(errs, ParseError{Seq: seq, Err: parseErr, RawRecord: record})
			continue
		}
		rows = append(rows, row)
	}

	return rows, errs
}

// ParseError carries a per-row parse failure.
// RawRecord is stored as the dead_letter message_body — it may contain PHI.
type ParseError struct {
	Seq       int
	Err       error
	RawRecord []string // PHI — never log; stored in dead_letter_store.message_body
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func headerIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return idx
}

func col(colIdx map[string]int, record []string, name string) string {
	i, ok := colIdx[name]
	if !ok || i >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[i])
}

func required(colIdx map[string]int, record []string, name string) (string, error) {
	v := col(colIdx, record, name)
	if v == "" {
		return "", fmt.Errorf("required field %q is empty", name)
	}
	return v, nil
}

func parseDate(s, field string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("field %q is required", field)
	}
	// Try common date formats
	for _, layout := range []string{"2006-01-02", "01/02/2006", "20060102"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("field %q: cannot parse date %q", field, s)
}

func parseCents(s, field string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("field %q is required", field)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("field %q: cannot parse amount %q: %w", field, s, err)
	}
	// Convert dollars to cents, round to avoid floating point drift
	return int64(math.Round(f * 100)), nil
}

// parsePhone parses a phone_number field to int64.
// Empty or whitespace-only values return 0 (field is optional per spec).
// Non-numeric characters (dashes, parens, spaces) are stripped before parsing.
// Returns an error only if the field is non-empty and unparseable after stripping.
func parsePhone(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	// Strip common formatting characters
	var digits strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits.WriteRune(c)
		}
	}
	clean := digits.String()
	if clean == "" {
		return 0, fmt.Errorf("field \"phone_number\": non-empty value %q contains no digits", s)
	}
	if len(clean) != 10 {
		return 0, fmt.Errorf("field \"phone_number\": must be exactly 10 digits after stripping formatting, got %d digits in %q", len(clean), s)
	}
	v, err := strconv.ParseInt(clean, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("field \"phone_number\": cannot parse %q: %w", s, err)
	}
	return v, nil
}

// ASCIITransliterate applies NFD normalisation and strips combining characters,
// producing ASCII-safe output for FIS fixed-width records (§2 Assumptions, Open Item #9).
// e.g. "García" → "Garcia", "Müller" → "Muller"
func ASCIITransliterate(s string) string {
	// NFD decomposes accented characters into base + combining mark
	normalized := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(normalized))
	for i := 0; i < len(normalized); {
		r, size := utf8.DecodeRuneInString(normalized[i:])
		i += size
		if r == utf8.RuneError {
			continue
		}
		// Keep ASCII printable characters; drop combining marks and non-ASCII
		if r < 128 && unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseSRG310Row(seq int, colIdx map[string]int, record []string) (*SRG310Row, error) {
	clientMemberID, err := required(colIdx, record, "client_member_id")
	if err != nil {
		return nil, err
	}

	dobStr, err := required(colIdx, record, "date_of_birth")
	if err != nil {
		return nil, err
	}
	dob, err := parseDate(dobStr, "date_of_birth")
	if err != nil {
		return nil, err
	}

	firstName, err := required(colIdx, record, "first_name")
	if err != nil {
		return nil, err
	}
	lastName, err := required(colIdx, record, "last_name")
	if err != nil {
		return nil, err
	}
	address1, err := required(colIdx, record, "address_1")
	if err != nil {
		return nil, err
	}
	city, err := required(colIdx, record, "city")
	if err != nil {
		return nil, err
	}
	state, err := required(colIdx, record, "state")
	if err != nil {
		return nil, err
	}
	zip, err := required(colIdx, record, "zip")
	if err != nil {
		return nil, err
	}

	// Phone number: optional, numeric, empty = 0
	phoneNumber, err := parsePhone(col(colIdx, record, "phone_number"))
	if err != nil {
		return nil, err
	}

	// Benefit period: YYYY-MM — required for idempotency key
	benefitPeriod, err := required(colIdx, record, "benefit_period")
	if err != nil {
		// Fall back to current month if not in file — log as warning, not error
		benefitPeriod = time.Now().Format("2006-01")
	}

	// Build raw map for replay storage (PHI — never log)
	raw := make(map[string]string, len(colIdx))
	for name, i := range colIdx {
		if i < len(record) {
			raw[name] = record[i]
		}
	}

	return &SRG310Row{
		SequenceInFile: seq,
		ClientMemberID: clientMemberID,
		SubprogramID:   col(colIdx, record, "subprogram_id"),
		// Apply ASCII transliteration to all demographic text fields (§2 Assumptions)
		FirstName:     ASCIITransliterate(firstName),
		LastName:      ASCIITransliterate(lastName),
		DOB:           dob,
		Address1:      ASCIITransliterate(address1),
		Address2:      ASCIITransliterate(col(colIdx, record, "address_2")),
		City:          ASCIITransliterate(city),
		State:         strings.ToUpper(state),
		ZIP:           zip,
		PhoneNumber:   phoneNumber,
		Email:         col(colIdx, record, "email"),
		PackageID:     col(colIdx, record, "package_id"),
		CardDesignID:  col(colIdx, record, "card_design_id"),
		CustomCardID:  col(colIdx, record, "custom_card_id"),
		ContractPBP:   col(colIdx, record, "contract_pbp"),
		BenefitType:   strings.ToUpper(col(colIdx, record, "benefit_type")),
		BenefitPeriod: benefitPeriod,
		Raw:           raw,
	}, nil
}

func parseSRG315Row(seq int, colIdx map[string]int, record []string) (*SRG315Row, error) {
	clientMemberID, err := required(colIdx, record, "client_member_id")
	if err != nil {
		return nil, err
	}
	eventType, err := required(colIdx, record, "event_type")
	if err != nil {
		return nil, err
	}

	effectiveDate := time.Now()
	if s := col(colIdx, record, "effective_date"); s != "" {
		if d, err := parseDate(s, "effective_date"); err == nil {
			effectiveDate = d
		}
	}

	raw := make(map[string]string, len(colIdx))
	for name, i := range colIdx {
		if i < len(record) {
			raw[name] = record[i]
		}
	}

	return &SRG315Row{
		SequenceInFile: seq,
		ClientMemberID: clientMemberID,
		EventType:      strings.ToUpper(eventType),
		EffectiveDate:  effectiveDate,
		BenefitPeriod:  col(colIdx, record, "benefit_period"),
		BenefitType:    strings.ToUpper(col(colIdx, record, "benefit_type")),
		Raw:            raw,
	}, nil
}

func parseSRG320Row(seq int, colIdx map[string]int, record []string) (*SRG320Row, error) {
	clientMemberID, err := required(colIdx, record, "client_member_id")
	if err != nil {
		return nil, err
	}
	commandType, err := required(colIdx, record, "command_type")
	if err != nil {
		return nil, err
	}
	amountStr, err := required(colIdx, record, "amount")
	if err != nil {
		return nil, err
	}
	amountCents, err := parseCents(amountStr, "amount")
	if err != nil {
		return nil, err
	}

	effectiveDate := time.Now()
	if s := col(colIdx, record, "effective_date"); s != "" {
		if d, parseErr := parseDate(s, "effective_date"); parseErr == nil {
			effectiveDate = d
		}
	}

	// Expiry: if not in file, default to last day of current month at 11:59 PM ET
	// per the contractual rule (SOW §2.1, §3.3)
	expiryDate := lastDayOfMonth(effectiveDate)
	if s := col(colIdx, record, "expiry_date"); s != "" {
		if d, parseErr := parseDate(s, "expiry_date"); parseErr == nil {
			expiryDate = d
		}
	}

	raw := make(map[string]string, len(colIdx))
	for name, i := range colIdx {
		if i < len(record) {
			raw[name] = record[i]
		}
	}

	return &SRG320Row{
		SequenceInFile: seq,
		ClientMemberID: clientMemberID,
		CommandType:    strings.ToUpper(commandType),
		AmountCents:    amountCents,
		BenefitPeriod:  col(colIdx, record, "benefit_period"),
		BenefitType:    strings.ToUpper(col(colIdx, record, "benefit_type")),
		EffectiveDate:  effectiveDate,
		ExpiryDate:     expiryDate,
		Raw:            raw,
	}, nil
}

// lastDayOfMonth returns the last day of the month for the given date.
// Used as the default expiry for purse loads when not specified in the SRG file.
func lastDayOfMonth(t time.Time) time.Time {
	// First day of next month, minus one day
	first := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
	return first.Add(-24 * time.Hour)
}
