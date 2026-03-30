// Package stdmon loads FIS Monetary XTRACT v3.8 into reporting.fact_monetary.
//
// Feed: STDMON — daily delta of all settled/posted monetary activity.
// File naming: STDMONmmddccyy_<ClientName>.txt
// H record feed name: STDFISMON
// Detail record: pipe-delimited, 114 fields (with all optional fields opted in).
//
// Key field positions validated against:
//   - Spec v3.8 (Data XTRACT Monetary 3.8.pdf)
//   - UAT sample: STDMON10302025_Morse.txt (client 1431777, subprogram 902160)
package stdmon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = "STDFISMON"

// Loader ingests STDMON detail records into reporting.fact_monetary.
type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	fileLogSK  int64  // xtract_file_log.file_log_sk — set before rows processed
	sourceFile string // S3 key
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile}
}

func (l *Loader) SetFileLogSK(sk int64) { l.fileLogSK = sk }

// ProcessRow is the parser.DetailRowFunc for STDMON.
// fields[0] is always "D" for standard monetary detail records.
// Field positions are 1-indexed in the spec but 0-indexed here after split.
// Spec field N = fields[N-1] after the leading record-type byte.
// We use 0-indexed: fields[0]="D", fields[1]=IssuerClientID, etc.
func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if parser.Field(fields, 0) != "D" {
		// Skip non-detail records (shouldn't occur for STDMON but be safe)
		return nil
	}

	// ── Core fields (always present) ──────────────────────────────────────
	issuerClientID  := parser.FieldInt64(fields, 1)   // [2] Issuer ClientID
	subprogramID    := parser.FieldInt64(fields, 3)    // [4] Sub Program ID
	subprogramName  := parser.Field(fields, 4)         // [5] Sub Program Name
	bin             := parser.Field(fields, 5)         // [6] BIN
	binCurrAlpha    := parser.Field(fields, 6)         // [7] BIN Currency Alpha
	binCurrCode     := parser.Field(fields, 7)         // [8] BIN Currency Code
	bankName        := parser.Field(fields, 8)         // [9] Bank Name
	panMasked       := parser.Field(fields, 9)         // [10] PAN
	cardNumberMasked := parser.Field(fields, 10)       // [11] CardNumber
	authAmount      := parser.FieldFloat64(fields, 11) // [12] Authorization Amount
	authCode        := parser.Field(fields, 12)        // [13] Authorization Code
	txnLocalAmount  := parser.FieldFloat64(fields, 13) // [14] Txn Local Amount
	txnLocalDT      := parser.FieldDateTime(fields, 14)// [15] TxnLocDateTime MMDDYYYY HH:MM:SS
	txnSign         := parser.FieldInt64(fields, 15)   // [16] TxnSign 1=credit -1=debit
	txnCurrCode     := parser.Field(fields, 16)        // [17] Transaction Currency Code
	txnCurrAlpha    := parser.Field(fields, 17)        // [18] Transaction Currency Alpha
	txnTypeCode     := parser.FieldInt64(fields, 18)   // [19] TxnTypeCode
	reasonCode      := parser.FieldInt64(fields, 19)   // [20] Reason Code
	derivedReqCode  := parser.FieldInt64(fields, 20)   // [21] Derived Request Code
	responseCode    := parser.FieldInt64(fields, 21)   // [22] Response Code
	matchStatusCode := parser.FieldInt64(fields, 22)   // [23] Match Status Code
	matchTypeCode   := parser.FieldInt64(fields, 23)   // [24] Match Type Code
	initialLoadDate := parser.FieldDate(fields, 24)    // [25] Initial Load Date Flag
	mcc             := parser.FieldInt64(fields, 25)   // [26] MCC
	merchCurrAlpha  := parser.Field(fields, 26)        // [27] Merchant Currency Alpha
	merchCurrCode   := parser.Field(fields, 27)        // [28] Merchant Currency Code
	merchantName    := parser.Field(fields, 28)        // [29] Merchant Name
	merchantNumber  := parser.Field(fields, 29)        // [30] Merchant Number
	referenceNumber := parser.Field(fields, 30)        // [31] Reference Number
	paymentMethodID := parser.FieldInt64(fields, 31)   // [32] Payment Method ID
	settleAmount    := parser.FieldFloat64(fields, 32) // [33] Settle Amount
	wcsUTCPostDate  := parser.FieldDateTime(fields, 33)// [34] WCSUTCPostDate MMDDYYYY HH:MM:SS
	sourceCode      := parser.FieldInt64(fields, 34)   // [35] Source Code
	acqRefNumber    := parser.Field(fields, 35)        // [36] Acquirer Reference Number
	acquirerID      := parser.FieldInt64(fields, 36)   // [37] AcquirerID

	// ── Optional fields (opted-in, present as empty pipe if not) ────────────
	// Positions validated from UAT sample (114 fields total)
	panProxyNumber  := parser.Field(fields, 78)        // [79] PAN Proxy Number
	txnUID          := parser.Field(fields, 79)        // [80] TxnUID (UUID)
	purseCode       := parser.Field(fields, 80)        // [81] Purse code e.g. OTC2100
	purseStatus     := parser.Field(fields, 81)        // [82] Purse Status
	purseStatusDate := parser.FieldDate(fields, 82)    // [83] Purse Status Date
	purseStartDate  := parser.FieldDate(fields, 83)    // [84] Purse Start Date
	purseEndDate    := parser.FieldDate(fields, 84)    // [85] Purse End Date

	// date_sk from WCSUTCPostDate (authoritative settlement date)
	dateSK := parser.DateSK(wcsUTCPostDate)
	if dateSK == 0 {
		dateSK = parser.DateSK(txnLocalDT)
	}

	workOfDate := parser.FieldDate(fields, 0) // populated by caller from header

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.fact_monetary (
			file_date, work_of_date, source_file_name, tenant_id,
			issuer_client_id, subprogram_id, subprogram_name,
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
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10, $11,
			$12, $13,
			$14, $15,
			$16, $17,
			$18, $19, $20,
			$21, $22, $23, $24,
			$25, $26, $27,
			$28, $29, $30,
			$31, $32, $33,
			$34, $35, $36,
			$37, $38, $39,
			$40, $41,
			$42, $43, $44,
			$45, $46,
			now()
		)
		ON CONFLICT (source_file_name, txn_uid) DO NOTHING`,
		// file metadata
		workOfDate, workOfDate, l.sourceFile, l.tenantID,
		// FIS identity
		nullInt(issuerClientID), nullInt(subprogramID), nullStr(subprogramName),
		nullStr(bin), nullStr(binCurrAlpha), nullStr(binCurrCode), nullStr(bankName),
		// card
		nullStr(panMasked), nullStr(cardNumberMasked),
		// transaction
		nullFloat(authAmount), nullStr(authCode),
		nullFloat(txnLocalAmount), nullTime(txnLocalDT),
		txnSign, nullStr(txnCurrCode), nullStr(txnCurrAlpha),
		txnTypeCode, nullInt(reasonCode), nullInt(derivedReqCode), nullInt(responseCode),
		nullInt(matchStatusCode), nullInt(matchTypeCode), nullTime(initialLoadDate),
		nullInt(mcc), nullStr(merchCurrAlpha), nullStr(merchCurrCode),
		nullStr(merchantName), nullStr(merchantNumber), nullStr(referenceNumber),
		nullInt(paymentMethodID), nullFloat(settleAmount), nullTime(wcsUTCPostDate),
		nullInt(sourceCode), nullStr(acqRefNumber), nullInt(acquirerID),
		// optional
		nullStr(panProxyNumber), nullStr(txnUID),
		nullStr(purseCode), nullStr(purseStatus), nullTime(purseStatusDate),
		nullTime(purseStartDate), nullTime(purseEndDate),
	)
	if err != nil {
		return fmt.Errorf("stdmon: insert row line %d txn_uid=%s: %w", lineNum, txnUID, err)
	}
	return nil
}

// ─── null helpers ─────────────────────────────────────────────────────────

func nullStr(s string) interface{} {
	if s == "" { return nil }
	return s
}

func nullInt(v int64) interface{} {
	if v == 0 { return nil }
	return v
}

func nullFloat(v float64) interface{} {
	if v == 0 { return nil }
	return v
}

func nullTime(t interface{ IsZero() bool }) interface{} {
	if t == nil { return nil }
	// duck-typed: works for time.Time
	type zeroer interface{ IsZero() bool }
	if z, ok := t.(zeroer); ok && z.IsZero() {
		return nil
	}
	return t
}
