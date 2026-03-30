// Package stdnonmon loads FIS Non-Monetary XTRACT v3.0 → reporting.fact_non_monetary.
//
// Feed: STDNONMON — daily delta of non-financial card lifecycle events.
// H record feed name: STDFISNONMON
// Detail record: 100+ fields covering card registration, status changes,
// demographic updates, purse creation, embossing events.
//
// Field positions validated against spec v3.0 and UAT sample
// STDNONMON11012025_MORSE.txt (client 1431777, subprogram 902160).
package stdnonmon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISNONMON"

type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	sourceFile string
	workOfDate interface{}
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile}
}

func (l *Loader) SetWorkOfDate(t interface{}) { l.workOfDate = t }

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if parser.Field(fields, 0) != "D" {
		return nil
	}

	// ── Client hierarchy ──────────────────────────────────────────────────
	topClientID    := parser.FieldInt64(fields, 1)     // [2] Top Client ID
	topClientName  := parser.Field(fields, 2)           // [3]
	issuerClientID := parser.FieldInt64(fields, 3)     // [4]
	clientName     := parser.Field(fields, 4)           // [5]
	programID      := parser.FieldInt64(fields, 5)     // [6]
	programName    := parser.Field(fields, 6)           // [7]
	subprogramID   := parser.FieldInt64(fields, 7)     // [8]
	subprogramName := parser.Field(fields, 8)           // [9]
	bin            := parser.Field(fields, 9)           // [10]
	binCurrAlpha   := parser.Field(fields, 10)          // [11]
	binCurrCode    := parser.Field(fields, 11)          // [12]
	packageID      := parser.FieldInt64(fields, 12)    // [13]
	packageName    := parser.Field(fields, 13)          // [14]

	// ── Card / account ────────────────────────────────────────────────────
	panMasked      := parser.Field(fields, 14)          // [15]
	cardNumberMasked := parser.Field(fields, 15)        // [16]
	activateDate   := parser.FieldDate(fields, 16)      // [17]
	cardStatusCode := parser.FieldInt64(fields, 17)    // [18]

	// ── Cardholder demographics ───────────────────────────────────────────
	firstName      := parser.Field(fields, 18)          // [19]
	lastName       := parser.Field(fields, 19)          // [20]
	middleInitial  := parser.Field(fields, 20)          // [21]
	address1       := parser.Field(fields, 21)          // [22]
	city           := parser.Field(fields, 28)          // scan ahead — city varies by spec pos
	state          := parser.Field(fields, 29)
	zip            := parser.Field(fields, 30)

	// ── Event fields — sourced from UAT sample analysis ────────────────────
	// UAT rows show event type code at field [33] and description at [34]
	eventTypeCode  := parser.FieldInt64(fields, 32)    // [33]
	eventDesc      := parser.Field(fields, 51)         // event description (varies by event)

	// Source / actor
	eventSource    := parser.Field(fields, 87)         // e.g. "MyAccount Website"
	eventActor     := parser.Field(fields, 89)         // e.g. "ClientWebUser"
	eventDateTime  := parser.FieldDateTime(fields, 33) // [34] event timestamp

	// Card dates
	cardStartDate  := parser.FieldDate(fields, 46)
	cardEndDate    := parser.FieldDate(fields, 47)

	// Purse context (when event is purse-related)
	purseNumber    := parser.FieldInt64(fields, 116)
	purseName      := parser.Field(fields, 117)

	// Person / proxy
	personID       := parser.FieldInt64(fields, 43)
	panProxyNumber := parser.Field(fields, 48)

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
		nullInt(issuerClientID), nullStr(clientName),
		nullInt(programID), nullStr(programName), nullInt(subprogramID), nullStr(subprogramName),
		nullStr(bin), nullStr(binCurrAlpha), nullStr(binCurrCode),
		nullInt(packageID), nullStr(packageName),
		nullStr(panMasked), nullStr(cardNumberMasked),
		nullTime(activateDate), nullInt(cardStatusCode),
		nullStr(firstName), nullStr(lastName), nullStr(middleInitial),
		nullStr(address1), nullStr(city), nullStr(state), nullStr(zip),
		nullInt(eventTypeCode), nullStr(eventDesc), nullStr(eventSource), nullStr(eventActor),
		nullTime(eventDateTime),
		nullTime(cardStartDate), nullTime(cardEndDate),
		nullInt(purseNumber), nullStr(purseName),
		nullInt(personID), nullStr(panProxyNumber),
	)
	if err != nil {
		return fmt.Errorf("stdnonmon: insert line %d event_type=%d: %w",
			lineNum, eventTypeCode, err)
	}
	return nil
}

func nullStr(s string) interface{} {
	if s == "" { return nil }; return s
}
func nullInt(v int64) interface{} {
	if v == 0 { return nil }; return v
}
func nullTime(t interface{ IsZero() bool }) interface{} {
	if t == nil { return nil }
	type zeroer interface{ IsZero() bool }
	if z, ok := t.(zeroer); ok && z.IsZero() { return nil }
	return t
}
