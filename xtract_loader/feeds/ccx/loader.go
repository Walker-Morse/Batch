// Package ccx loads FIS Client Configuration XTRACT v1.17.
//
// Feed: CCX — daily delta of all sub-program configuration changes.
// H record field 2: "CLIENT CONFIGURATION XTRACT"
// Three record types (no "D" prefix — each has a unique type code):
//   DSPG — Sub-program configuration (one row per active subprogram)
//   DPUR — Purse configuration (one or more per DSPG)
//   DPKG — Package (card art + fulfillment) configuration
//
// The initial load fires on first configuration and delivers all subprograms.
// Subsequent runs are daily deltas (only changed subprograms).
//
// Field positions validated against spec v1.17 and UAT sample
// CCX11122025_Morse.txt (subprogram 902160, purses OTC2100/FOD2100/CMB2100/REW2100/CSH2100).
//
// NOTE: FeedName uses empty string — pass "" to parser.Parse to skip feed name
// validation (CCX header field 2 is "CLIENT CONFIGURATION XTRACT", a multi-word
// value with spaces that doesn't match a simple constant pattern check).
package ccx

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

// FeedName — CCX H record field 2 is "CLIENT CONFIGURATION XTRACT".
// Pass "" to parser.Parse to skip feed name check.
const FeedName = ""

type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	sourceFile string
	fileDate   interface{} // from H record
	// currentSubprogramID tracks the most recently seen DSPG subprogram_id
	// so that DPUR and DPKG records can be associated with their parent.
	currentSubprogramID int64
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile}
}

func (l *Loader) SetFileDate(t interface{}) { l.fileDate = t }

// ProcessRow dispatches on the CCX record type code (DSPG, DPUR, DPKG).
func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	recType := strings.ToUpper(strings.TrimSpace(fields[0]))
	switch recType {
	case "DSPG":
		return l.processDSPG(ctx, lineNum, fields)
	case "DPUR":
		return l.processDPUR(ctx, lineNum, fields)
	case "DPKG":
		return l.processDPKG(ctx, lineNum, fields)
	default:
		return nil
	}
}

// processDSPG inserts into reporting.dim_subprogram.
// DSPG fields per spec v1.17 (239 fields total; we map key fields):
//   [1] Sub Program ID
//   [2] Sub Program Name
//   [3] Is Sub Program Active (1/0)
//   [4] Program ID
//   [5] Program Name
//   [6] Client ID
//   [7] Client Name
//   [8] Client Alt Value
//   [14] Market Segment
//   [11] PROXY Name
//   [12] AKA Name
//   [13] Pseudo BIN
//   [15] At what level does program get added
//   [30] Are cards Reloadable (1/0)
//   [31] Initial Card Status
//   [34] Physical Expiration Method (STATIC/DYNAMIC/NONE)
//   [37] Logical (1/0)
// Purse list: derived from DPUR records for this subprogram.
func (l *Loader) processDSPG(ctx context.Context, lineNum int, fields []string) error {
	subprogramID   := parser.FieldInt64(fields, 1)
	subprogramName := parser.Field(fields, 2)
	isActive       := parser.Field(fields, 3) == "1"
	programID      := parser.FieldInt64(fields, 4)
	programName    := parser.Field(fields, 5)
	clientID       := parser.FieldInt64(fields, 6)
	clientName     := parser.Field(fields, 7)
	clientAltValue := parser.Field(fields, 8)
	marketSegment  := parser.Field(fields, 14)
	proxyName      := parser.Field(fields, 11)
	akaName        := parser.Field(fields, 12)
	pseudoBIN      := parser.Field(fields, 13)
	isReloadable   := parser.Field(fields, 30) == "1"
	initialStatus  := parser.Field(fields, 31)
	physExpMethod  := parser.Field(fields, 34)
	logicalExp     := parser.Field(fields, 37) == "1"

	l.currentSubprogramID = subprogramID

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.dim_subprogram (
			subprogram_id, file_date, effective_from,
			program_id, program_name,
			client_id, client_name,
			subprogram_name, is_active,
			client_alt_value, market_segment,
			proxy_name, aka_name, pseudo_bin,
			is_reloadable, initial_card_status,
			physical_expiration_method, logical_expiration_enabled,
			inserted_at
		) VALUES (
			$1,$2,$2,
			$3,$4,
			$5,$6,
			$7,$8,
			$9,$10,
			$11,$12,$13,
			$14,$15,
			$16,$17,
			now()
		)
		ON CONFLICT (subprogram_id, file_date) DO UPDATE SET
			is_active              = EXCLUDED.is_active,
			subprogram_name        = EXCLUDED.subprogram_name,
			program_id             = EXCLUDED.program_id,
			program_name           = EXCLUDED.program_name,
			client_alt_value       = EXCLUDED.client_alt_value,
			market_segment         = EXCLUDED.market_segment,
			is_reloadable          = EXCLUDED.is_reloadable,
			initial_card_status    = EXCLUDED.initial_card_status,
			physical_expiration_method = EXCLUDED.physical_expiration_method,
			logical_expiration_enabled = EXCLUDED.logical_expiration_enabled`,
		subprogramID, l.fileDate,
		nullInt(programID), nullStr(programName),
		nullInt(clientID), nullStr(clientName),
		nullStr(subprogramName), isActive,
		nullStr(clientAltValue), nullStr(marketSegment),
		nullStr(proxyName), nullStr(akaName), nullStr(pseudoBIN),
		isReloadable, nullStr(initialStatus),
		nullStr(physExpMethod), logicalExp,
	)
	if err != nil {
		return fmt.Errorf("ccx DSPG line %d subprogram=%d: %w", lineNum, subprogramID, err)
	}
	return nil
}

// processDPUR inserts into reporting.dim_purse_config.
// DPUR fields (observed from UAT CCX11122025_Morse.txt):
//   [1] Subprogram ID
//   [2] Purse Number (e.g. 12100)
//   [3] Purse Code (e.g. OTC2100)
//   [4] Filter Type (e.g. MCC)
//   [5] Filter Network ID
//   [6] Filter Network Name
//   [7] Is Active (1/0)
//   [8] Min Load Amount
//   [9] Max Load Amount
//   [10] Min Balance Amount
//   [11] Max Balance Amount
//   [12] Purse Status (ACTIVE/CLOSED/etc)
//   [13] Purse Start Date (MM/DD/YYYY format)
//   [14] Purse End Date
//   [last non-null] Spend Categories raw (e.g. "30100(5A,OF,OV)")
func (l *Loader) processDPUR(ctx context.Context, lineNum int, fields []string) error {
	subprogramID  := parser.FieldInt64(fields, 1)
	purseNumber   := parser.FieldInt64(fields, 2)
	purseCode     := parser.Field(fields, 3)
	filterType    := parser.Field(fields, 4)
	filterNetID   := parser.FieldInt64(fields, 5)
	filterNetName := parser.Field(fields, 6)
	isActive      := parser.Field(fields, 7) == "1"
	minLoad       := parser.FieldFloat64(fields, 8)
	maxLoad       := parser.FieldFloat64(fields, 9)
	minBalance    := parser.FieldFloat64(fields, 10)
	maxBalance    := parser.FieldFloat64(fields, 11)
	purseStatus   := parser.Field(fields, 12)
	purseStartRaw := parser.Field(fields, 13) // MM/DD/YYYY
	purseEndRaw   := parser.Field(fields, 14)

	// Parse MM/DD/YYYY date format used in CCX (different from MMDDYYYY in other feeds)
	purseStart := parseCCXDate(purseStartRaw)
	purseEnd   := parseCCXDate(purseEndRaw)

	// Spend categories are in the last non-empty field
	spendCats := ""
	for i := len(fields) - 1; i >= 15; i-- {
		if v := strings.TrimSpace(fields[i]); v != "" {
			spendCats = v
			break
		}
	}

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.dim_purse_config (
			subprogram_id, purse_number, purse_code, file_date,
			filter_type, filter_network_id, filter_network_name,
			is_active, min_load_amount, max_load_amount,
			min_balance_amount, max_balance_amount,
			purse_status, purse_start_date, purse_end_date,
			spend_categories_raw, inserted_at
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,
			$8,$9,$10,
			$11,$12,
			$13,$14,$15,
			$16, now()
		)
		ON CONFLICT (subprogram_id, purse_number, file_date) DO UPDATE SET
			purse_code          = EXCLUDED.purse_code,
			is_active           = EXCLUDED.is_active,
			min_load_amount     = EXCLUDED.min_load_amount,
			max_load_amount     = EXCLUDED.max_load_amount,
			purse_status        = EXCLUDED.purse_status,
			purse_start_date    = EXCLUDED.purse_start_date,
			purse_end_date      = EXCLUDED.purse_end_date,
			spend_categories_raw = EXCLUDED.spend_categories_raw`,
		subprogramID, nullInt(purseNumber), nullStr(purseCode), l.fileDate,
		nullStr(filterType), nullInt(filterNetID), nullStr(filterNetName),
		isActive, nullFloat(minLoad), nullFloat(maxLoad),
		nullFloat(minBalance), nullFloat(maxBalance),
		nullStr(purseStatus), nullTime(purseStart), nullTime(purseEnd),
		nullStr(spendCats),
	)
	if err != nil {
		return fmt.Errorf("ccx DPUR line %d subprogram=%d purse=%s: %w",
			lineNum, subprogramID, purseCode, err)
	}
	return nil
}

// processDPKG inserts into reporting.dim_package.
// DPKG fields (from UAT):
//   [1] Subprogram ID
//   [2] Package ID
//   [3] Package Name
//   [4] Package Code (e.g. Morse_317)
//   [5] BIN
func (l *Loader) processDPKG(ctx context.Context, lineNum int, fields []string) error {
	subprogramID := parser.FieldInt64(fields, 1)
	packageID    := parser.FieldInt64(fields, 2)
	packageName  := parser.Field(fields, 3)
	packageCode  := parser.Field(fields, 4)
	bin          := parser.Field(fields, 5)

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.dim_package (
			subprogram_id, package_id, package_name, package_code, bin, file_date, inserted_at
		) VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (subprogram_id, package_id, file_date) DO UPDATE SET
			package_name = EXCLUDED.package_name,
			package_code = EXCLUDED.package_code,
			bin          = EXCLUDED.bin`,
		nullInt(subprogramID), nullInt(packageID),
		nullStr(packageName), nullStr(packageCode), nullStr(bin), l.fileDate,
	)
	if err != nil {
		return fmt.Errorf("ccx DPKG line %d subprogram=%d package=%d: %w",
			lineNum, subprogramID, packageID, err)
	}
	return nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────

// parseCCXDate parses MM/DD/YYYY format used in CCX purse dates.
func parseCCXDate(s string) interface{} {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return nil
	}
	if len(parts[0]) == 1 {
		parts[0] = "0" + parts[0]
	}
	if len(parts[1]) == 1 {
		parts[1] = "0" + parts[1]
	}
	// Reconstruct as MMDDYYYY for parser.FieldDate
	mmddyyyy := parts[0] + parts[1] + parts[2]
	f := []string{"", mmddyyyy}
	t := parser.FieldDate(f, 1)
	if t.IsZero() {
		return nil
	}
	return t
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
func nullTime(t interface{}) interface{} {
	if t == nil { return nil }; return t
}
