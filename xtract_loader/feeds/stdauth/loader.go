// Package stdauth loads FIS Authorization XTRACT v2.7 → reporting.fact_authorization.
package stdauth

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISAUTH"

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

	issuerClientID := parser.FieldInt64(fields, 1)
	clientName     := parser.Field(fields, 2)
	subprogramID   := parser.FieldInt64(fields, 3)
	subprogramName := parser.Field(fields, 4)
	bin            := parser.Field(fields, 5)
	binCurrAlpha   := parser.Field(fields, 6)
	binCurrCode    := parser.Field(fields, 7)
	bankName       := parser.Field(fields, 8)
	panMasked      := parser.Field(fields, 9)
	cardNumMasked  := parser.Field(fields, 10)
	txnUID         := parser.Field(fields, 11)
	txnTypeCode    := parser.FieldInt64(fields, 12)
	txnTypeName    := parser.Field(fields, 13)
	purseNumber    := parser.FieldInt64(fields, 14)
	purseName      := parser.Field(fields, 15)
	txnDT          := parser.FieldDateTime(fields, 16)
	authCode       := parser.Field(fields, 17)
	actualReqCode  := parser.FieldInt64(fields, 18)
	actualReqDesc  := parser.Field(fields, 19)
	responseCode   := parser.FieldInt64(fields, 20)
	responseDesc   := parser.Field(fields, 21)
	reasonCode     := parser.FieldInt64(fields, 22)
	reasonCodeDesc := parser.Field(fields, 23)
	sourceCode     := parser.FieldInt64(fields, 24)
	sourceDesc     := parser.Field(fields, 25)
	authAmount     := parser.FieldFloat64(fields, 26)
	txnLocalAmt    := parser.FieldFloat64(fields, 27)
	txnSign        := parser.FieldInt64(fields, 28)
	txnCurrCode    := parser.Field(fields, 29)
	txnCurrAlpha   := parser.Field(fields, 30)
	isReversed     := parser.Field(fields, 32) == "1"
	posEntryCode   := parser.FieldInt64(fields, 37)
	posEntryDesc   := parser.Field(fields, 38)
	mcc            := parser.FieldInt64(fields, 39)
	mccDesc        := parser.Field(fields, 40)
	merchCurrAlpha := parser.Field(fields, 41)
	merchCurrCode  := parser.Field(fields, 42)
	merchantName   := parser.Field(fields, 43)
	merchantNum    := parser.Field(fields, 44)
	merchantState  := parser.Field(fields, 48)
	authBalance    := parser.FieldFloat64(fields, 56)
	settleBalance  := parser.FieldFloat64(fields, 57)
	panProxyNum    := parser.Field(fields, 61)

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_authorization (
			file_date, work_of_date, source_file_name, tenant_id,
			issuer_client_id, client_name, subprogram_id, subprogram_name,
			bin, bin_currency_alpha, bin_currency_code, bank_name,
			pan_masked, card_number_masked,
			txn_uid, txn_type_code, txn_type_name,
			purse_number, purse_name, txn_datetime_utc,
			authorization_code, actual_request_code, actual_request_desc,
			response_code, response_description,
			reason_code, reason_code_description,
			source_code, source_description,
			authorization_amount, txn_local_amount, txn_sign,
			txn_currency_code, txn_currency_alpha,
			is_reversed, pos_entry_code, pos_entry_description,
			mcc, mcc_description,
			merchant_currency_alpha, merchant_currency_code,
			merchant_name, merchant_number, merchant_state,
			authorization_balance, settle_balance,
			pan_proxy_number, inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,
			$9,$10,$11,$12,
			$13,$14,
			($15::text)::uuid,$16,$17,
			$18,$19,$20,
			$21,$22,$23,
			$24,$25,
			$26,$27,
			$28,$29,
			$30,$31,$32,
			$33,$34,
			$35,$36,$37,
			$38,$39,
			$40,$41,
			$42,$43,$44,
			$45,$46,
			$47, now()
		)
		ON CONFLICT (source_file_name, txn_uid) DO NOTHING`,
		nullDate(l.workOfDate), nullDate(l.workOfDate), l.sourceFile, l.tenantID,
		nullInt(issuerClientID), nullStr(clientName), subprogramID, nullStr(subprogramName),
		nullStr(bin), nullChar(binCurrAlpha), nullChar(binCurrCode), nullStr(bankName),
		nullStr(panMasked), nullStr(cardNumMasked),
		nullStr(txnUID), nullInt(txnTypeCode), nullStr(txnTypeName),
		nullInt(purseNumber), nullStr(purseName), nullTime(txnDT),
		nullStr(authCode), nullInt(actualReqCode), nullStr(actualReqDesc),
		nullInt(responseCode), nullStr(responseDesc),
		nullInt(reasonCode), nullStr(reasonCodeDesc),
		nullInt(sourceCode), nullStr(sourceDesc),
		nullFloat(authAmount), nullFloat(txnLocalAmt), int16(txnSign),
		nullChar(txnCurrCode), nullChar(txnCurrAlpha),
		isReversed, nullInt(posEntryCode), nullStr(posEntryDesc),
		nullInt(mcc), nullStr(mccDesc),
		nullChar(merchCurrAlpha), nullChar(merchCurrCode),
		nullStr(merchantName), nullStr(merchantNum), nullChar(merchantState),
		nullFloat(authBalance), nullFloat(settleBalance),
		nullStr(panProxyNum),
	)
	if err != nil {
		return fmt.Errorf("stdauth: insert line %d txn_uid=%s: %w", lineNum, txnUID, err)
	}
	return nil
}

func nullStr(s string) interface{}    { if s == "" { return nil }; return s }
func nullChar(s string) interface{}   { if s == "" { return nil }; return s[:min(1, len(s))] }
func nullInt(v int64) interface{}     { if v == 0 { return nil }; return v }
func nullFloat(v float64) interface{} { if v == 0 { return nil }; return v }
func nullDate(t time.Time) interface{} { if t.IsZero() { return nil }; return t }
func nullTime(t time.Time) interface{} { if t.IsZero() { return nil }; return t }
func min(a, b int) int                { if a < b { return a }; return b }
