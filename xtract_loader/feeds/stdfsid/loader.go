// Package stdfsid loads FIS Filtered Spend Item Details XTRACT v1.3.
// R records → fact_spend_item, D records → fact_spend_item_detail.
package stdfsid

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = ""

type Loader struct {
	pool            *pgxpool.Pool
	tenantID        string
	sourceFile      string
	workOfDate      time.Time
	lastSpendItemSK int64
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string, workOfDate time.Time) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile, workOfDate: workOfDate}
}

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	switch fields[0] {
	case "R":
		return l.processR(ctx, lineNum, fields)
	case "D":
		return l.processD(ctx, lineNum, fields)
	}
	return nil
}

func (l *Loader) processR(ctx context.Context, lineNum int, fields []string) error {
	bin              := parser.Field(fields, 1)
	issuerClientID   := parser.FieldInt64(fields, 2)
	issuerClientName := parser.Field(fields, 3)
	subprogramID     := parser.FieldInt64(fields, 4)
	subprogramName   := parser.Field(fields, 5)
	panMasked        := parser.Field(fields, 6)
	panProxyNum      := parser.Field(fields, 7)
	cardNumMasked    := parser.Field(fields, 8)
	cardProxyNum     := parser.Field(fields, 9)
	cardID           := parser.FieldInt64(fields, 10)
	logID            := parser.Field(fields, 11)
	prenoteID        := parser.Field(fields, 12)
	txnUID           := parser.Field(fields, 13)
	retrievalRefNo   := parser.Field(fields, 14)
	par              := parser.Field(fields, 15)
	filterType       := parser.Field(fields, 16)
	txnSource        := parser.Field(fields, 17)
	sourceCode       := parser.FieldInt64(fields, 18)
	sourceDesc       := parser.Field(fields, 19)
	txnDT            := parser.FieldDateTime(fields, 20)
	postDT           := parser.FieldDateTime(fields, 21)
	itemInsertedDT   := parser.FieldDateTime(fields, 22)
	mcc              := parser.FieldInt64(fields, 23)
	mccDesc          := parser.Field(fields, 24)
	merchantName     := parser.Field(fields, 27)
	merchantNumOrig  := parser.Field(fields, 28)
	merchantNum      := parser.Field(fields, 29)
	merchantCity     := parser.Field(fields, 31)
	merchantCountry  := parser.Field(fields, 32)
	merchantState    := parser.Field(fields, 35)
	merchantStreet   := parser.Field(fields, 36)
	txnCurrCode      := parser.Field(fields, 40)
	txnCurrAlpha     := parser.Field(fields, 41)
	settleAmount     := parser.FieldFloat64(fields, 48)
	spendCategoryRaw := parser.Field(fields, 39)
	clientRefNum     := parser.Field(fields, 58)
	clientSpecificID := parser.Field(fields, 59)
	cardholderUID    := parser.Field(fields, 60)

	// log_id and prenote_id may be non-UUID strings — cast safely
	var sk int64
	err := l.pool.QueryRow(ctx, `
		INSERT INTO reporting.fact_spend_item (
			file_date, work_of_date, source_file_name, tenant_id,
			bin, issuer_client_id, issuer_client_name,
			subprogram_id, subprogram_name,
			pan_masked, pan_proxy_number, card_number_masked, card_proxy_number, card_id,
			log_id, prenote_id, txn_uid,
			retrieval_reference_number, payment_account_reference,
			filter_type, transaction_source, source_code, source_description,
			txn_datetime, post_datetime, item_inserted_datetime,
			mcc, mcc_description,
			merchant_name, merchant_number_original, merchant_number,
			merchant_city, merchant_country_code, merchant_state, merchant_street,
			txn_currency_code, txn_currency_alpha,
			settle_amount, spend_category_raw,
			client_reference_number, client_specific_id, cardholder_client_uid,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,$9,
			$10,$11,$12,$13,$14,
			$15::text::uuid,$16::text::uuid,($17::text)::uuid,
			$18,$19,
			$20,$21,$22,$23,
			$24,$25,$26,
			$27,$28,
			$29,$30,$31,
			$32,$33,$34,$35,
			$36,$37,
			$38,$39,
			$40,$41,$42,
			now()
		)
		ON CONFLICT (source_file_name, txn_uid, log_id) DO NOTHING
		RETURNING spend_item_sk`,
		l.workOfDate, l.workOfDate, l.sourceFile, l.tenantID,
		nullStr(bin), nullInt(issuerClientID), nullStr(issuerClientName),
		nullInt(subprogramID), nullStr(subprogramName),
		nullStr(panMasked), nullStr(panProxyNum), nullStr(cardNumMasked),
		nullStr(cardProxyNum), nullInt(cardID),
		nullStr(logID), nullStr(prenoteID), nullStr(txnUID),
		nullStr(retrievalRefNo), nullStr(par),
		nullStr(filterType), nullStr(txnSource), nullInt(sourceCode), nullStr(sourceDesc),
		nullTime(txnDT), nullTime(postDT), nullTime(itemInsertedDT),
		nullInt(mcc), nullStr(mccDesc),
		nullStr(merchantName), nullStr(merchantNumOrig), nullStr(merchantNum),
		nullStr(merchantCity), nullChar(merchantCountry), nullChar(merchantState), nullStr(merchantStreet),
		nullChar(txnCurrCode), nullChar(txnCurrAlpha),
		nullFloat(settleAmount), nullStr(spendCategoryRaw),
		nullStr(clientRefNum), nullStr(clientSpecificID), nullStr(cardholderUID),
	).Scan(&sk)
	if err != nil {
		// ON CONFLICT — look up existing SK
		_ = l.pool.QueryRow(ctx,
			`SELECT spend_item_sk FROM reporting.fact_spend_item
			 WHERE source_file_name=$1 AND txn_uid=($2::text)::uuid LIMIT 1`,
			l.sourceFile, txnUID,
		).Scan(&sk)
	}
	l.lastSpendItemSK = sk
	return nil
}

func (l *Loader) processD(ctx context.Context, lineNum int, fields []string) error {
	if l.lastSpendItemSK == 0 {
		return nil
	}
	upcType        := parser.Field(fields, 1)
	upc            := parser.Field(fields, 2)
	itemDesc       := parser.Field(fields, 3)
	itemSeq        := parser.FieldInt64(fields, 4)
	deptNum        := parser.Field(fields, 5)
	spendCatCode   := parser.Field(fields, 6)
	// fields[7] and [8] are empty padding in generated files; amount at [9]
	itemAmount     := parser.FieldFloat64(fields, 9)
	itemQty        := parser.FieldInt64(fields, 10)
	taxAmount      := parser.FieldFloat64(fields, 11)
	discountAmount := parser.FieldFloat64(fields, 12)
	isFunded       := parser.Field(fields, 13) == "1"
	fundedAmount   := parser.FieldFloat64(fields, 15)

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_spend_item_detail (
			spend_item_sk, source_file_name, file_date,
			upc_type, upc, item_description, item_sequence, dept_number,
			spend_category_code,
			item_amount, item_quantity, tax_amount, discount_amount,
			is_funded, funded_amount,
			tenant_id, inserted_at
		)
		SELECT $1, $2, $3,
			$4,$5,$6,$7,$8,$9,
			$10,$11,$12,$13,$14,$15,
			$16, now()
		ON CONFLICT DO NOTHING`,
		l.lastSpendItemSK, l.sourceFile, l.workOfDate,
		nullStr(upcType), nullStr(upc), nullStr(itemDesc), nullInt(itemSeq), nullStr(deptNum),
		nullStr(spendCatCode),
		itemAmount, itemQty, nullFloat(taxAmount), nullFloat(discountAmount),
		isFunded, fundedAmount,
		l.tenantID,
	)
	if err != nil {
		return fmt.Errorf("stdfsid D line %d: %w", lineNum, err)
	}
	return nil
}

func nullStr(s string) interface{}    { if s == "" { return nil }; return s }
func nullChar(s string) interface{}   { if s == "" { return nil }; return s[:min(1, len(s))] }
func nullInt(v int64) interface{}     { if v == 0 { return nil }; return v }
func nullFloat(v float64) interface{} { if v == 0 { return nil }; return v }
func nullTime(t time.Time) interface{} { if t.IsZero() { return nil }; return t }
func min(a, b int) int                { if a < b { return a }; return b }
