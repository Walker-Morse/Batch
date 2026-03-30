// Package stdauth loads FIS Authorization XTRACT v2.7 into reporting.fact_authorization.
//
// Feed: STDAUTH — daily delta of all auth activity (approvals, declines, reversals).
// H record feed name: STDFISAUTH
// Detail record: pipe-delimited, 62+ fields.
//
// Field positions validated against spec v2.7 and UAT sample STDAUTH11012025_Morse.txt.
package stdauth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISAUTH"

type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	sourceFile string
	workOfDate interface{} // set from header before rows processed
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile}
}

func (l *Loader) SetWorkOfDate(t interface{}) { l.workOfDate = t }

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if parser.Field(fields, 0) != "D" {
		return nil
	}

	// ── Required fields ───────────────────────────────────────────────────
	issuerClientID  := parser.FieldInt64(fields, 1)    // [2]
	subprogramID    := parser.FieldInt64(fields, 3)    // [4]
	subprogramName  := parser.Field(fields, 4)         // [5]
	bin             := parser.Field(fields, 5)         // [6]
	binCurrAlpha    := parser.Field(fields, 6)         // [7]
	binCurrCode     := parser.Field(fields, 7)         // [8]
	bankName        := parser.Field(fields, 8)         // [9]
	panMasked       := parser.Field(fields, 9)         // [10]
	cardNumberMasked := parser.Field(fields, 10)       // [11]
	txnUID          := parser.Field(fields, 11)        // [12] UUID natural key
	txnTypeCode     := parser.FieldInt64(fields, 12)   // [13]
	txnTypeName     := parser.Field(fields, 13)        // [14]
	purseNumber     := parser.FieldInt64(fields, 14)   // [15]
	purseName       := parser.Field(fields, 15)        // [16]
	txnDatetimeUTC  := parser.FieldDateTime(fields, 16)// [17]
	authCode        := parser.Field(fields, 17)        // [18]
	actualReqCode   := parser.FieldInt64(fields, 18)   // [19]
	actualReqDesc   := parser.Field(fields, 19)        // [20]
	responseCode    := parser.FieldInt64(fields, 20)   // [21]
	responseDesc    := parser.Field(fields, 21)        // [22]
	reasonCode      := parser.FieldInt64(fields, 22)   // [23]
	reasonCodeDesc  := parser.Field(fields, 23)        // [24]
	sourceCode      := parser.FieldInt64(fields, 24)   // [25]
	sourceDesc      := parser.Field(fields, 25)        // [26]
	authAmount      := parser.FieldFloat64(fields, 26) // [27]
	txnLocalAmount  := parser.FieldFloat64(fields, 27) // [28]
	txnSign         := parser.FieldInt64(fields, 28)   // [29]
	txnCurrCode     := parser.Field(fields, 29)        // [30]
	txnCurrAlpha    := parser.Field(fields, 30)        // [31]

	// ── Optional fields ───────────────────────────────────────────────────
	isReversed      := parser.Field(fields, 32) == "1" // [33]
	mcc             := parser.FieldInt64(fields, 39)   // [40]
	mccDescription  := parser.Field(fields, 40)        // [41]
	merchCurrAlpha  := parser.Field(fields, 41)        // [42]
	merchCurrCode   := parser.Field(fields, 42)        // [43]
	merchantName    := parser.Field(fields, 43)        // [44]
	merchantNumber  := parser.Field(fields, 44)        // [45]
	merchantState   := parser.Field(fields, 48)        // [49]
	posEntryCode    := parser.FieldInt64(fields, 37)   // [38]
	posEntryDesc    := parser.Field(fields, 38)        // [39]
	authBalance     := parser.FieldFloat64(fields, 56) // [57]
	settleBalance   := parser.FieldFloat64(fields, 57) // [58]
	panProxyNumber  := parser.Field(fields, 61)        // [62]

	dateSK := parser.DateSK(txnDatetimeUTC)

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_authorization (
			file_date, work_of_date, source_file_name, tenant_id,
			issuer_client_id, subprogram_id, subprogram_name,
			bin, bin_currency_alpha, bin_currency_code, bank_name,
			pan_masked, card_number_masked,
			txn_uid, txn_type_code, txn_type_name,
			purse_number, purse_name,
			txn_datetime_utc,
			authorization_code, actual_request_code, actual_request_desc,
			response_code, response_description,
			reason_code, reason_code_description,
			source_code, source_description,
			authorization_amount, txn_local_amount, txn_sign,
			txn_currency_code, txn_currency_alpha,
			is_reversed, mcc, mcc_description,
			merchant_currency_alpha, merchant_currency_code,
			merchant_name, merchant_number, merchant_state,
			pos_entry_code, pos_entry_description,
			authorization_balance, settle_balance,
			pan_proxy_number,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,
			$8,$9,$10,$11,
			$12,$13,
			$14,$15,$16,
			$17,$18,
			$19,
			$20,$21,$22,
			$23,$24,
			$25,$26,
			$27,$28,
			$29,$30,$31,
			$32,$33,$34,
			$35,$36,$37,
			$38,$39,$40,$41,$42,
			$43,$44,
			$45,$46,
			$47,
			now()
		)
		ON CONFLICT (source_file_name, txn_uid) DO NOTHING`,
		l.workOfDate, l.workOfDate, l.sourceFile, l.tenantID,
		nullInt(issuerClientID), nullInt(subprogramID), nullStr(subprogramName),
		nullStr(bin), nullStr(binCurrAlpha), nullStr(binCurrCode), nullStr(bankName),
		nullStr(panMasked), nullStr(cardNumberMasked),
		nullStr(txnUID), txnTypeCode, nullStr(txnTypeName),
		nullInt(purseNumber), nullStr(purseName),
		nullTime(txnDatetimeUTC),
		nullStr(authCode), nullInt(actualReqCode), nullStr(actualReqDesc),
		nullInt(responseCode), nullStr(responseDesc),
		nullInt(reasonCode), nullStr(reasonCodeDesc),
		nullInt(sourceCode), nullStr(sourceDesc),
		nullFloat(authAmount), nullFloat(txnLocalAmount), txnSign,
		nullStr(txnCurrCode), nullStr(txnCurrAlpha),
		isReversed, nullInt(mcc), nullStr(mccDescription),
		nullStr(merchCurrAlpha), nullStr(merchCurrCode),
		nullStr(merchantName), nullStr(merchantNumber), nullStr(merchantState),
		nullInt(posEntryCode), nullStr(posEntryDesc),
		nullFloat(authBalance), nullFloat(settleBalance),
		nullStr(panProxyNumber),
	)
	if err != nil {
		return fmt.Errorf("stdauth: insert line %d txn_uid=%s date_sk=%d: %w", lineNum, txnUID, dateSK, err)
	}
	return nil
}

func nullStr(s string) interface{} {
	if s == "" { return nil }; return s
}
func nullInt(v int64) interface{} {
	if v == 0 { return nil }; return v
}
func nullFloat(v float64) interface{} {
	if v == 0 { return nil }; return v
}
func nullTime(t interface{ IsZero() bool }) interface{} {
	if t == nil { return nil }
	type zeroer interface{ IsZero() bool }
	if z, ok := t.(zeroer); ok && z.IsZero() { return nil }
	return t
}
