// Package stdnonmon loads FIS Non-Monetary XTRACT v3.0 → reporting.fact_non_monetary.
//
// Field positions validated against STDNONMON11012025_MORSE.txt (156 fields):
//   [0]  record_type
//   [1]  top_client_id
//   [2]  top_client_name
//   [3]  issuer_client_id
//   [4]  client_name
//   [5]  program_id
//   [6]  program_name
//   [7]  subprogram_id
//   [8]  subprogram_name
//   [9]  bin
//   [10] bin_currency_alpha
//   [11] bin_currency_code
//   [12] package_id
//   [13] package_name
//   [14] pan_masked
//   [15] card_number_masked
//   [16] activate_date
//   [17] card_status_code
//   [18] cardholder_last_name  (NOT top_client_name — confirmed from UAT file)
//   [19] cardholder_first_name
//   [20] middle_initial
//   [21] mailing_address_line1
//   [23] mailing_city
//   [24] mailing_state         (char 1)
//   [25] mailing_zip
//   [30] card_start_date
//   [31] card_end_date
//   [34] event_type_code       (field 32 = market segment, 33 = market_seg_name, 34/35 = event codes)
//   [36] event_datetime
//   [51] event_description
//   [55] pan_proxy_number
//   [81] event_source
//   [86] event_actor (first name part)
//   [87] event_actor (last name part)
//   [88] person_id
package stdnonmon

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISNONMON"

type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	sourceFile string
	workOfDate time.Time
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string, workOfDate time.Time) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile, workOfDate: workOfDate}
}

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if parser.Field(fields, 0) != "D" {
		return nil
	}

	topClientID    := parser.FieldInt64(fields, 1)
	topClientName  := parser.Field(fields, 2)
	issuerClientID := parser.FieldInt64(fields, 3)
	clientName     := parser.Field(fields, 4)
	programID      := parser.FieldInt64(fields, 5)
	programName    := parser.Field(fields, 6)
	subprogramID   := parser.FieldInt64(fields, 7)
	subprogramName := parser.Field(fields, 8)
	bin            := parser.Field(fields, 9)
	binCurrAlpha   := parser.Field(fields, 10)
	binCurrCode    := parser.Field(fields, 11)
	packageID      := parser.FieldInt64(fields, 12)
	packageName    := parser.Field(fields, 13)
	panMasked      := parser.Field(fields, 14)
	cardNumMasked  := parser.Field(fields, 15)
	activateDate   := parser.FieldDate(fields, 16)
	cardStatusCode := parser.FieldInt64(fields, 17)
	lastName       := parser.Field(fields, 18)
	firstName      := parser.Field(fields, 19)
	middleInitial  := parser.Field(fields, 20)
	address1       := parser.Field(fields, 21)
	city           := parser.Field(fields, 23)
	state          := parser.Field(fields, 24)
	zip            := parser.Field(fields, 25)
	cardStartDate  := parser.FieldDate(fields, 30)
	cardEndDate    := parser.FieldDate(fields, 31)
	eventTypeCode  := parser.FieldInt64(fields, 34)
	eventDT        := parser.FieldDateTime(fields, 36)
	eventDesc      := parser.Field(fields, 51)
	panProxyNum    := parser.Field(fields, 55)
	eventSource    := parser.Field(fields, 81)
	personID       := parser.FieldInt64(fields, 88)

	// state must be char(1)
	stateChar := ""
	if len(state) > 0 {
		stateChar = state[:1]
	}
	middleChar := ""
	if len(middleInitial) > 0 {
		middleChar = middleInitial[:1]
	}

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_non_monetary (
			file_date, work_of_date, source_file_name, tenant_id,
			top_client_id, top_client_name,
			issuer_client_id, client_name,
			program_id, program_name, subprogram_id, subprogram_name,
			bin, bin_currency_alpha, bin_currency_code,
			package_id, package_name,
			pan_masked, card_number_masked, activate_date, card_status_code,
			cardholder_first_name, cardholder_last_name, cardholder_middle_initial,
			mailing_address_line1, mailing_city, mailing_state, mailing_zip,
			event_type_code, event_description, event_source, event_datetime,
			card_start_date, card_end_date,
			person_id, pan_proxy_number,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,
			$9,$10,$11,$12,
			$13,$14,$15,$16,$17,
			$18,$19,$20,$21,
			$22,$23,$24,
			$25,$26,$27,$28,
			$29,$30,$31,$32,
			$33,$34,
			$35,$36,
			now()
		)`,
		l.workOfDate, l.workOfDate, l.sourceFile, l.tenantID,
		nullInt(topClientID), nullStr(topClientName),
		issuerClientID, nullStr(clientName),
		nullInt(programID), nullStr(programName), subprogramID, nullStr(subprogramName),
		nullStr(bin), nullChar(binCurrAlpha), nullChar(binCurrCode),
		nullInt(packageID), nullStr(packageName),
		nullStr(panMasked), nullStr(cardNumMasked), nullDate(activateDate), nullInt16(cardStatusCode),
		nullStr(firstName), nullStr(lastName), nullChar1(middleChar),
		nullStr(address1), nullStr(city), nullChar1(stateChar), nullStr(zip),
		eventTypeCode, nullStr(eventDesc), nullStr(eventSource), nullTime(eventDT),
		nullDate(cardStartDate), nullDate(cardEndDate),
		nullInt(personID), nullStr(panProxyNum),
	)
	if err != nil {
		return fmt.Errorf("stdnonmon: insert line %d event_type=%d: %w", lineNum, eventTypeCode, err)
	}
	return nil
}

func nullStr(s string) interface{}     { if s == "" { return nil }; return s }
func nullChar(s string) interface{}    { if s == "" { return nil }; return s[:min(1, len(s))] }
func nullChar1(s string) interface{}   { if s == "" { return nil }; return s }
func nullInt(v int64) interface{}      { if v == 0 { return nil }; return v }
func nullInt16(v int64) interface{}    { if v == 0 { return nil }; return int16(v) }
func nullDate(t time.Time) interface{} { if t.IsZero() { return nil }; return t }
func nullTime(t time.Time) interface{} { if t.IsZero() { return nil }; return t }
func min(a, b int) int                 { if a < b { return a }; return b }
