// Package stdacctbal loads FIS Account Balance XTRACT v1.6 → reporting.fact_account_balance.
package stdacctbal

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISACCTBAL"

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
	bin            := parser.Field(fields, 3)
	bankName       := parser.Field(fields, 4)
	panMasked      := parser.Field(fields, 5)
	openingBal     := parser.FieldFloat64(fields, 6)
	valueloadAmt   := parser.FieldFloat64(fields, 7)
	valueloadCnt   := parser.FieldInt64(fields, 8)
	purchaseAmt    := parser.FieldFloat64(fields, 9)
	purchaseCnt    := parser.FieldInt64(fields, 10)
	otcAmt         := parser.FieldFloat64(fields, 11)
	otcCnt         := parser.FieldInt64(fields, 12)
	atmAmt         := parser.FieldFloat64(fields, 13)
	atmCnt         := parser.FieldInt64(fields, 14)
	returnAmt      := parser.FieldFloat64(fields, 15)
	returnCnt      := parser.FieldInt64(fields, 16)
	adjustAmt      := parser.FieldFloat64(fields, 17)
	adjustCnt      := parser.FieldInt64(fields, 18)
	feesAmt        := parser.FieldFloat64(fields, 19)
	feesCnt        := parser.FieldInt64(fields, 20)
	otherCreditAmt := parser.FieldFloat64(fields, 21)
	otherCreditCnt := parser.FieldInt64(fields, 22)
	otherDebitAmt  := parser.FieldFloat64(fields, 23)
	otherDebitCnt  := parser.FieldInt64(fields, 24)
	totalCreditAmt := parser.FieldFloat64(fields, 25)
	totalCreditCnt := parser.FieldInt64(fields, 26)
	totalDebitAmt  := parser.FieldFloat64(fields, 27)
	totalDebitCnt  := parser.FieldInt64(fields, 28)
	totalTxnAmt    := parser.FieldFloat64(fields, 29)
	totalTxnCnt    := parser.FieldInt64(fields, 30)
	closingBal     := parser.FieldFloat64(fields, 31)
	binCurrAlpha   := parser.Field(fields, 32)
	binCurrCode    := parser.Field(fields, 33)
	panProxyNum    := parser.Field(fields, 34)
	personID       := parser.FieldInt64(fields, 35)
	cardholderUID  := parser.Field(fields, 36)
	purseName      := parser.Field(fields, 37)
	purseStatus    := parser.Field(fields, 38)
	purseCreation  := parser.FieldDate(fields, 39)
	purseExpiry    := parser.FieldDate(fields, 40)
	purseStatusDt  := parser.FieldDate(fields, 41)

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_account_balance (
			file_date, previous_day, source_file_name, tenant_id,
			issuer_client_id, client_name, bin, bank_name,
			bin_currency_alpha, bin_currency_code,
			pan_masked, pan_proxy_number, person_id, cardholder_client_uid,
			purse_name, purse_status,
			purse_creation_date, purse_expiration_date, purse_status_date,
			opening_balance, closing_balance,
			total_valueload_amount, total_valueload_count,
			total_purchase_amount, total_purchase_count,
			total_otc_amount, total_otc_count,
			total_atm_withdrawal_amount, total_atm_withdrawal_count,
			total_return_amount, total_return_count,
			total_adjustment_amount, total_adjustment_count,
			total_fees, total_fees_count,
			other_credit_amount, other_credit_count,
			other_debit_amount, other_debit_count,
			total_credit_amount, total_credit_count,
			total_debit_amount, total_debit_count,
			total_transaction_amount, total_transaction_count,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,
			$9,$10,
			$11,$12,$13,$14,
			$15,$16,
			$17,$18,$19,
			$20,$21,
			$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,
			$32,$33,$34,$35,$36,$37,$38,$39,
			$40,$41,$42,$43,$44,$45,
			now()
		)
		ON CONFLICT (source_file_name, pan_masked, purse_name) DO NOTHING`,
		l.workOfDate, l.workOfDate, l.sourceFile, l.tenantID,
		nullInt(issuerClientID), nullStr(clientName), nullStr(bin), nullStr(bankName),
		nullChar(binCurrAlpha), nullChar(binCurrCode),
		panMasked, nullStr(panProxyNum), nullInt(personID), nullStr(cardholderUID),
		nullStr(purseName), nullStr(purseStatus),
		nullDate(purseCreation), nullDate(purseExpiry), nullDate(purseStatusDt),
		openingBal, closingBal,
		valueloadAmt, valueloadCnt, purchaseAmt, purchaseCnt,
		otcAmt, otcCnt, atmAmt, atmCnt, returnAmt, returnCnt,
		adjustAmt, adjustCnt, feesAmt, feesCnt,
		otherCreditAmt, otherCreditCnt, otherDebitAmt, otherDebitCnt,
		totalCreditAmt, totalCreditCnt, totalDebitAmt, totalDebitCnt,
		totalTxnAmt, totalTxnCnt,
	)
	if err != nil {
		return fmt.Errorf("stdacctbal: insert line %d pan=%s purse=%s: %w", lineNum, panMasked, purseName, err)
	}
	return nil
}

func nullStr(s string) interface{}    { if s == "" { return nil }; return s }
func nullChar(s string) interface{}   { if s == "" { return nil }; return s[:min(1, len(s))] }
func nullInt(v int64) interface{}     { if v == 0 { return nil }; return v }
func nullDate(t time.Time) interface{} { if t.IsZero() { return nil }; return t }
func min(a, b int) int                { if a < b { return a }; return b }
