// Package stdmon loads FIS Monetary XTRACT v3.8 into reporting.fact_monetary.
package stdmon

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISMON"

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
	bin             := parser.Field(fields, 5)
	binCurrAlpha   := parser.Field(fields, 6)
	binCurrCode    := parser.Field(fields, 7)
	bankName        := parser.Field(fields, 8)
	panMasked       := parser.Field(fields, 9)
	cardNumMasked   := parser.Field(fields, 10)
	authAmount      := parser.FieldFloat64(fields, 11)
	authCode        := parser.Field(fields, 12)
	txnLocalAmt     := parser.FieldFloat64(fields, 13)
	txnLocalDT      := parser.FieldDateTime(fields, 14)
	txnSign         := parser.FieldInt64(fields, 15)
	txnCurrCode     := parser.Field(fields, 16)
	txnCurrAlpha    := parser.Field(fields, 17)
	txnTypeCode     := parser.FieldInt64(fields, 18)
	reasonCode      := parser.FieldInt64(fields, 19)
	derivedReqCode  := parser.FieldInt64(fields, 20)
	responseCode    := parser.FieldInt64(fields, 21)
	matchStatusCode := parser.FieldInt64(fields, 22)
	matchTypeCode   := parser.FieldInt64(fields, 23)
	initLoadDate    := parser.FieldDate(fields, 24)
	mcc             := parser.FieldInt64(fields, 25)
	merchCurrAlpha  := parser.Field(fields, 26)
	merchCurrCode   := parser.Field(fields, 27)
	merchantName    := parser.Field(fields, 28)
	merchantNum     := parser.Field(fields, 29)
	refNumber       := parser.Field(fields, 30)
	payMethodID     := parser.FieldInt64(fields, 31)
	settleAmt       := parser.FieldFloat64(fields, 32)
	wcsPostDate     := parser.FieldDateTime(fields, 33)
	sourceCode      := parser.FieldInt64(fields, 34)
	acqRefNum       := parser.Field(fields, 35)
	acquirerID      := parser.FieldInt64(fields, 36)
	panProxyNum     := parser.Field(fields, 78)
	txnUID          := parser.Field(fields, 79)   // uuid string
	purseCode       := parser.Field(fields, 80)
	purseStatus     := parser.Field(fields, 81)
	purseStatusDate := parser.FieldDate(fields, 82)
	purseStartDate  := parser.FieldDate(fields, 83)
	purseEndDate    := parser.FieldDate(fields, 84)

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_monetary (
			file_date, work_of_date, source_file_name, tenant_id,
			issuer_client_id, client_name, subprogram_id, subprogram_name,
			bin, bin_currency_alpha, bin_currency_code, bank_name,
			pan_masked, card_number_masked,
			authorization_amount, authorization_code,
			txn_local_amount, txn_local_datetime,
			txn_sign, txn_currency_code, txn_currency_alpha,
			txn_type_code, reason_code, derived_request_code, response_code,
			match_status_code, match_type_code, initial_load_date_flag,
			mcc, merchant_currency_alpha, merchant_currency_code,
			merchant_name, merchant_number, reference_number,
			payment_method_id, settle_amount, wcs_utc_post_date,
			source_code, acquirer_reference_number, acquirer_id,
			pan_proxy_number, txn_uid,
			purse_code, purse_status, purse_status_date,
			purse_start_date, purse_end_date,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,
			$9,$10,$11,$12,
			$13,$14,
			$15,$16,
			$17,$18,
			$19,$20,$21,
			$22,$23,$24,$25,
			$26,$27,$28,
			$29,$30,$31,
			$32,$33,$34,
			$35,$36,$37,
			$38,$39,$40,
			$41,($42::text)::uuid,
			$43,$44,$45,
			$46,$47,
			now()
		)
		ON CONFLICT DO NOTHING`,
		nullDate(l.workOfDate), nullDate(l.workOfDate), l.sourceFile, l.tenantID,
		nullInt(issuerClientID), nullStr(clientName), subprogramID, nullStr(subprogramName),
		nullStr(bin), nullChar(binCurrAlpha), nullChar(binCurrCode), nullStr(bankName),
		nullStr(panMasked), nullStr(cardNumMasked),
		nullFloat(authAmount), nullStr(authCode),
		nullFloat(txnLocalAmt), nullTime(txnLocalDT),
		int16(txnSign), nullChar(txnCurrCode), nullChar(txnCurrAlpha),
		nullInt(txnTypeCode), nullInt(reasonCode), nullInt(derivedReqCode), nullInt(responseCode),
		nullInt16(matchStatusCode), nullInt(matchTypeCode), nullDate(initLoadDate),
		nullInt(mcc), nullChar(merchCurrAlpha), nullChar(merchCurrCode),
		nullStr(merchantName), nullStr(merchantNum), nullStr(refNumber),
		nullInt(payMethodID), nullFloat(settleAmt), nullTime(wcsPostDate),
		nullInt(sourceCode), nullStr(acqRefNum), nullInt(acquirerID),
		nullStr(panProxyNum), nullStr(txnUID),
		nullStr(purseCode), nullStr(purseStatus), nullDate(purseStatusDate),
		nullDate(purseStartDate), nullDate(purseEndDate),
	)
	if err != nil {
		return fmt.Errorf("stdmon: insert row line %d txn_uid=%s: %w", lineNum, txnUID, err)
	}
	return nil
}

func nullStr(s string) interface{}       { if s == "" { return nil }; return s }
func nullChar(s string) interface{}      { if s == "" { return nil }; return s[:min(1,len(s))] }
func nullInt(v int64) interface{}        { if v == 0 { return nil }; return v }
func nullInt16(v int64) interface{}      { if v == 0 { return nil }; return int16(v) }
func nullFloat(v float64) interface{}    { if v == 0 { return nil }; return v }
func nullDate(t time.Time) interface{}   { if t.IsZero() { return nil }; return t }
func nullTime(t time.Time) interface{}   { if t.IsZero() { return nil }; return t }
func min(a, b int) int                   { if a < b { return a }; return b }
