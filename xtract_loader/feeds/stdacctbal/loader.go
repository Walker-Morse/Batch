// Package stdacctbal loads FIS Account Balance XTRACT v1.6 → reporting.fact_account_balance.
//
// Feed: STDACCTBAL — daily delta of purse-level closing balances.
// Grain: one row per PAN × purse_name per file date (confirmed from UAT:
// separate D records per purse per PAN — OTC2100, FOD2100, CMB2100 distinct).
// H record feed name: STDFISACCTBAL
// Detail record: 42 required fields + optional purse fields 38-42.
package stdacctbal

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISACCTBAL"

type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	sourceFile string
	previousDay interface{}
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile}
}

func (l *Loader) SetPreviousDay(t interface{}) { l.previousDay = t }

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if parser.Field(fields, 0) != "D" {
		return nil
	}

	issuerClientID := parser.FieldInt64(fields, 1)    // [2]
	clientName     := parser.Field(fields, 2)          // [3]
	bin            := parser.Field(fields, 3)          // [4]
	bankName       := parser.Field(fields, 4)          // [5]
	panMasked      := parser.Field(fields, 5)          // [6]

	openingBalance := parser.FieldFloat64(fields, 6)   // [7]
	valueloadAmt   := parser.FieldFloat64(fields, 7)   // [8]
	valueloadCnt   := parser.FieldInt64(fields, 8)     // [9]
	purchaseAmt    := parser.FieldFloat64(fields, 9)   // [10]
	purchaseCnt    := parser.FieldInt64(fields, 10)    // [11]
	otcAmt         := parser.FieldFloat64(fields, 11)  // [12]
	otcCnt         := parser.FieldInt64(fields, 12)    // [13]
	atmAmt         := parser.FieldFloat64(fields, 13)  // [14]
	atmCnt         := parser.FieldInt64(fields, 14)    // [15]
	returnAmt      := parser.FieldFloat64(fields, 15)  // [16]
	returnCnt      := parser.FieldInt64(fields, 16)    // [17]
	adjustAmt      := parser.FieldFloat64(fields, 17)  // [18]
	adjustCnt      := parser.FieldInt64(fields, 18)    // [19]
	feesAmt        := parser.FieldFloat64(fields, 19)  // [20]
	feesCnt        := parser.FieldInt64(fields, 20)    // [21]
	otherCreditAmt := parser.FieldFloat64(fields, 21)  // [22]
	otherCreditCnt := parser.FieldInt64(fields, 22)    // [23]
	otherDebitAmt  := parser.FieldFloat64(fields, 23)  // [24]
	otherDebitCnt  := parser.FieldInt64(fields, 24)    // [25]
	totalCreditAmt := parser.FieldFloat64(fields, 25)  // [26]
	totalCreditCnt := parser.FieldInt64(fields, 26)    // [27]
	totalDebitAmt  := parser.FieldFloat64(fields, 27)  // [28]
	totalDebitCnt  := parser.FieldInt64(fields, 28)    // [29]
	totalTxnAmt    := parser.FieldFloat64(fields, 29)  // [30]
	totalTxnCnt    := parser.FieldInt64(fields, 30)    // [31]
	closingBalance := parser.FieldFloat64(fields, 31)  // [32]
	binCurrAlpha   := parser.Field(fields, 32)         // [33]
	binCurrCode    := parser.Field(fields, 33)         // [34]
	panProxyNumber := parser.Field(fields, 34)         // [35]
	personID       := parser.FieldInt64(fields, 35)    // [36]

	// Optional fields 37-41 (purse context — present in UAT, opt-in)
	cardholderUID  := parser.Field(fields, 36)         // [37]
	purseName      := parser.Field(fields, 37)         // [38] e.g. OTC2100
	purseStatus    := parser.Field(fields, 38)         // [39]
	purseCreation  := parser.FieldDate(fields, 39)     // [40]
	purseExpiry    := parser.FieldDate(fields, 40)     // [41]
	purseStatusDate := parser.FieldDate(fields, 41)    // [42]

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_account_balance (
			file_date, previous_day, source_file_name, tenant_id,
			issuer_client_id, client_name, bin, bank_name,
			bin_currency_alpha, bin_currency_code,
			pan_masked, pan_proxy_number, person_id, cardholder_client_uid,
			purse_name, purse_status, purse_creation_date, purse_expiration_date, purse_status_date,
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
			$15,$16,$17,$18,$19,
			$20,$21,
			$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,
			$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,
			now()
		)
		ON CONFLICT (source_file_name, pan_masked, purse_name) DO NOTHING`,
		l.previousDay, l.previousDay, l.sourceFile, l.tenantID,
		nullInt(issuerClientID), nullStr(clientName), nullStr(bin), nullStr(bankName),
		nullStr(binCurrAlpha), nullStr(binCurrCode),
		nullStr(panMasked), nullStr(panProxyNumber), nullInt(personID), nullStr(cardholderUID),
		nullStr(purseName), nullStr(purseStatus),
		nullTime(purseCreation), nullTime(purseExpiry), nullTime(purseStatusDate),
		openingBalance, closingBalance,
		valueloadAmt, valueloadCnt, purchaseAmt, purchaseCnt,
		otcAmt, otcCnt, atmAmt, atmCnt, returnAmt, returnCnt,
		adjustAmt, adjustCnt, feesAmt, feesCnt,
		otherCreditAmt, otherCreditCnt, otherDebitAmt, otherDebitCnt,
		totalCreditAmt, totalCreditCnt, totalDebitAmt, totalDebitCnt,
		totalTxnAmt, totalTxnCnt,
	)
	if err != nil {
		return fmt.Errorf("stdacctbal: insert line %d pan=%s purse=%s: %w",
			lineNum, panMasked, purseName, err)
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
