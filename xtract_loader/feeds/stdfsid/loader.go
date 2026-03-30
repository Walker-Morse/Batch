// Package stdfsid loads FIS Filtered Spend Item Details XTRACT v1.3.
//
// Feed: STDFSID — item-level spend detail for filtered spend transactions.
// H record feed name: STDFSID (note: header field 2 is "FILTEREDSPEND", not "STDFSID")
// Two record types in one file:
//   R records — transaction header (one per filtered spend transaction)
//   D records — UPC line items (one per item, child of preceding R record)
//
// The parser emits both R and D records to ProcessRow.
// We track the last R record's SK so D records can reference it as parent.
//
// Walmart monthly files (source_subtype=WALMART_MONTHLY) use the same format.
// Field positions validated against spec v1.3 and UAT sample
// STDFSID07232025_Test_Client.txt (36,197 rows: 7,218 R + 28,979 D).
package stdfsid

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

// FeedName — STDFSID H record field 2 is "FILTEREDSPEND", not "STDFSID".
// Pass "" to parser.Parse to skip feed name validation.
const FeedName = ""

type Loader struct {
	pool          *pgxpool.Pool
	tenantID      string
	sourceFile    string
	workOfDate    time.Time
	lastSpendItemSK int64  // SK of the most recently inserted R record
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string, workOfDate time.Time) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile, workOfDate: workOfDate}
}

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	switch fields[0] {
	case "R":
		return l.processRRecord(ctx, lineNum, fields)
	case "D":
		return l.processDRecord(ctx, lineNum, fields)
	default:
		return nil
	}
}

// processRRecord inserts a transaction header into fact_spend_item.
func (l *Loader) processRRecord(ctx context.Context, lineNum int, fields []string) error {
	bin            := parser.Field(fields, 1)          // [2]
	issuerClientID := parser.FieldInt64(fields, 2)    // [3]
	issuerClientName := parser.Field(fields, 3)        // [4]
	subprogramID   := parser.FieldInt64(fields, 4)    // [5]
	subprogramName := parser.Field(fields, 5)          // [6]
	panMasked      := parser.Field(fields, 6)          // [7] PAN
	panProxyNumber := parser.Field(fields, 7)          // [8]
	cardNumberMasked := parser.Field(fields, 8)        // [9]
	cardProxyNumber := parser.Field(fields, 9)         // [10]
	cardID         := parser.FieldInt64(fields, 10)   // [11]
	logID          := parser.Field(fields, 11)         // [12]
	prenoteID      := parser.Field(fields, 12)         // [13]
	txnUID         := parser.Field(fields, 13)         // [14]
	retrievalRefNo := parser.Field(fields, 14)         // [15]
	par            := parser.Field(fields, 15)         // [16] Payment Account Reference
	filterType     := parser.Field(fields, 16)         // [17]
	txnSource      := parser.Field(fields, 17)         // [18]
	sourceCode     := parser.FieldInt64(fields, 18)   // [19]
	sourceDesc     := parser.Field(fields, 19)         // [20]
	txnDateTime    := parser.FieldDateTime(fields, 20) // [21]
	postDateTime   := parser.FieldDateTime(fields, 21) // [22]
	itemInsertedDT := parser.FieldDateTime(fields, 22) // [23]
	mcc            := parser.FieldInt64(fields, 23)   // [24]
	mccDesc        := parser.Field(fields, 24)         // [25]
	merchantName   := parser.Field(fields, 27)         // [28]
	merchantNumberOrig := parser.Field(fields, 28)     // [29]
	merchantNumber := parser.Field(fields, 29)         // [30]
	merchantCity   := parser.Field(fields, 31)         // [32]
	merchantCountry := parser.Field(fields, 32)        // [33]
	merchantState  := parser.Field(fields, 35)         // [36]
	merchantStreet := parser.Field(fields, 36)         // [37]
	txnCurrCode    := parser.Field(fields, 40)         // [41]
	txnCurrAlpha   := parser.Field(fields, 41)         // [42]
	settleAmount   := parser.FieldFloat64(fields, 48)  // [49]
	spendCategoryRaw := parser.Field(fields, 39)       // [40] e.g. "5A-25.20,5D-1.00"
	filterResponse := parser.Field(fields, 39)         // [40] APL purse split
	clientRefNumber := parser.Field(fields, 58)        // [59]
	clientSpecificID := parser.Field(fields, 59)       // [60]
	cardholderUID  := parser.Field(fields, 60)         // [61]

	dateSK := parser.DateSK(txnDateTime)
	fileDate := l.workOfDate

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
			settle_amount, spend_category_raw, apl_filter_response,
			client_reference_number, client_specific_id, cardholder_client_uid,
			inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,
			$8,$9,
			$10,$11,$12,$13,$14,
			$15,$16,$17,
			$18,$19,
			$20,$21,$22,$23,
			$24,$25,$26,
			$27,$28,
			$29,$30,$31,
			$32,$33,$34,$35,
			$36,$37,
			$38,$39,$40,
			$41,$42,$43,
			now()
		)
		ON CONFLICT (source_file_name, txn_uid, log_id) DO NOTHING
		RETURNING spend_item_sk`,
		fileDate, fileDate, l.sourceFile, l.tenantID,
		nullStr(bin), nullInt(issuerClientID), nullStr(issuerClientName),
		nullInt(subprogramID), nullStr(subprogramName),
		nullStr(panMasked), nullStr(panProxyNumber), nullStr(cardNumberMasked),
		nullStr(cardProxyNumber), nullInt(cardID),
		nullStr(logID), nullStr(prenoteID), nullStr(txnUID),
		nullStr(retrievalRefNo), nullStr(par),
		nullStr(filterType), nullStr(txnSource), nullInt(sourceCode), nullStr(sourceDesc),
		nullTime(txnDateTime), nullTime(postDateTime), nullTime(itemInsertedDT),
		nullInt(mcc), nullStr(mccDesc),
		nullStr(merchantName), nullStr(merchantNumberOrig), nullStr(merchantNumber),
		nullStr(merchantCity), nullStr(merchantCountry), nullStr(merchantState), nullStr(merchantStreet),
		nullStr(txnCurrCode), nullStr(txnCurrAlpha),
		nullFloat(settleAmount), nullStr(spendCategoryRaw), nullStr(filterResponse),
		nullStr(clientRefNumber), nullStr(clientSpecificID), nullStr(cardholderUID),
	).Scan(&sk)
	if err != nil {
		// ON CONFLICT returned no row — look up existing SK for D record linking
		if err.Error() == "no rows in result set" {
			_ = l.pool.QueryRow(ctx,
				`SELECT spend_item_sk FROM reporting.fact_spend_item
				 WHERE source_file_name=$1 AND txn_uid=$2 AND log_id=$3 LIMIT 1`,
				l.sourceFile, txnUID, logID,
			).Scan(&sk)
		} else {
			return fmt.Errorf("stdfsid R line %d txn_uid=%s: %w", lineNum, txnUID, err)
		}
	}
	l.lastSpendItemSK = sk
	_ = dateSK // used for logging if needed
	return nil
}

// processDRecord inserts a UPC line item into fact_spend_item_detail.
func (l *Loader) processDRecord(ctx context.Context, lineNum int, fields []string) error {
	if l.lastSpendItemSK == 0 {
		// D record without preceding R — skip (shouldn't happen in well-formed file)
		return nil
	}

	upcType         := parser.Field(fields, 1)         // [2] UPC/PLU/etc
	upc             := parser.Field(fields, 2)         // [3] barcode
	itemDesc        := parser.Field(fields, 3)         // [4]
	itemSeq         := parser.FieldInt64(fields, 4)   // [5]
	deptNumber      := parser.Field(fields, 5)         // [6]
	spendCatCode    := parser.Field(fields, 6)         // [7] e.g. 5D
	itemAmount      := parser.FieldFloat64(fields, 8)  // [9]
	itemQty         := parser.FieldInt64(fields, 9)   // [10]
	taxAmount       := parser.FieldFloat64(fields, 10) // [11]
	discountAmount  := parser.FieldFloat64(fields, 11) // [12]
	isFunded        := parser.Field(fields, 12) == "1" // [13]
	fundedAmount    := parser.FieldFloat64(fields, 14) // [15]

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_spend_item_detail (
			spend_item_sk, txn_uid, source_file_name, file_date,
			upc_type, upc, item_description, item_sequence, dept_number,
			spend_category_code,
			item_amount, item_quantity, tax_amount, discount_amount,
			is_funded, funded_amount,
			tenant_id, inserted_at
		)
		SELECT $1,
			(SELECT txn_uid FROM reporting.fact_spend_item WHERE spend_item_sk=$1 LIMIT 1),
			$2, $3,
			$4,$5,$6,$7,$8,
			$9,
			$10,$11,$12,$13,
			$14,$15,
			$16, now()
		ON CONFLICT DO NOTHING`,
		l.lastSpendItemSK,
		l.sourceFile, l.workOfDate,
		nullStr(upcType), nullStr(upc), nullStr(itemDesc), nullInt(itemSeq), nullStr(deptNumber),
		nullStr(spendCatCode),
		nullFloat(itemAmount), nullInt(itemQty), nullFloat(taxAmount), nullFloat(discountAmount),
		isFunded, nullFloat(fundedAmount),
		l.tenantID,
	)
	if err != nil {
		return fmt.Errorf("stdfsid D line %d upc=%s: %w", lineNum, upc, err)
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
