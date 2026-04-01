// Package stdnonmon loads FIS Non-Monetary XTRACT v3.0 → reporting.fact_non_monetary.
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
	firstName      := parser.Field(fields, 18)
	lastName       := parser.Field(fields, 19)
	middleInitial  := parser.Field(fields, 20)
	address1       := parser.Field(fields, 21)
	city           := parser.Field(fields, 23)  // field 24 in spec
	state          := parser.Field(fields, 24)  // field 25 — char(1)
	zip            := parser.Field(fields, 25)
	eventTypeCode  := parser.FieldInt64(fields, 32)
	eventDT        := parser.FieldDateTime(fields, 33)
	eventDesc      := parser.Field(fields, 51)
	eventSource    := parser.Field(fields, 87)
	eventActor     := parser.Field(fields, 89)
	cardStartDate  := parser.FieldDate(fields, 46)
	cardEndDate    := parser.FieldDate(fields, 47)
	purseNumber    := parser.FieldInt64(fields, 116)
	purseName      := parser.Field(fields, 117)
	personID       := parser.FieldInt64(fields, 43)
	panProxyNum    := parser.Field(fields, 48)

	// state must be char(1) — take first char only
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
			event_type_code, event_description, event_source, event_actor, event_datetime,
			card_start_date, card_end_date,
			purse_number, purse_name,
			person_id, pan_proxy_number,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,
			$9,$10,$11,$12,
			$13,$14,$15,
			$16,$17,
			$18,$19,$20,$21,
			$22,$23,$24,
			$25,$26,$27,$28,
			$29,$30,$31,$32,$33,
			$34,$35,
			$36,$37,
			$38,$39,
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
		nullInt(eventTypeCode), nullStr(eventDesc), nullStr(eventSource), nullStr(eventActor), nullTime(eventDT),
		nullDate(cardStartDate), nullDate(cardEndDate),
		nullInt(purseNumber), nullStr(purseName),
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
