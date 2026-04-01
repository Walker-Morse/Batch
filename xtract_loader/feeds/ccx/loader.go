// Package ccx loads FIS Client Configuration XTRACT v1.17.
// DSPG → dim_subprogram, DPUR → dim_purse_config, DPKG → dim_package.
package ccx

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/xtract_loader/parser"
)

const FeedName = ""

type Loader struct {
	pool       *pgxpool.Pool
	tenantID   string
	sourceFile string
	fileDate   time.Time
}

func NewLoader(pool *pgxpool.Pool, tenantID, sourceFile string, fileDate time.Time) *Loader {
	return &Loader{pool: pool, tenantID: tenantID, sourceFile: sourceFile, fileDate: fileDate}
}

func (l *Loader) ProcessRow(ctx context.Context, lineNum int, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(fields[0])) {
	case "DSPG":
		return l.processDSPG(ctx, lineNum, fields)
	case "DPUR":
		return l.processDPUR(ctx, lineNum, fields)
	case "DPKG":
		return l.processDPKG(ctx, lineNum, fields)
	}
	return nil
}

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
	isReloadable   := parser.Field(fields, 30) == "1"
	initialStatus  := parser.Field(fields, 31)
	physExpMethod  := parser.Field(fields, 34)
	logicalExp     := parser.Field(fields, 37) == "1"

	_, err := l.pool.Exec(ctx, `
		INSERT INTO reporting.dim_subprogram (
			subprogram_id, file_date, effective_from,
			program_id, program_name,
			client_id, client_name,
			subprogram_name, is_active,
			client_alt_value, market_segment,
			is_reloadable, initial_card_status,
			physical_expiration_method, logical_expiration_enabled,
			inserted_at
		) VALUES (
			$1,$2,$2,
			$3,$4,$5,$6,
			$7,$8,$9,$10,
			$11,$12,$13,$14,
			now()
		)
		ON CONFLICT (subprogram_id, file_date) DO UPDATE SET
			is_active                  = EXCLUDED.is_active,
			subprogram_name            = EXCLUDED.subprogram_name,
			is_reloadable              = EXCLUDED.is_reloadable,
			physical_expiration_method = EXCLUDED.physical_expiration_method,
			logical_expiration_enabled = EXCLUDED.logical_expiration_enabled`,
		subprogramID, l.fileDate,
		nullInt(programID), nullStr(programName),
		clientID, nullStr(clientName),
		subprogramName, isActive, nullStr(clientAltValue), nullStr(marketSegment),
		isReloadable, nullStr(initialStatus), nullStr(physExpMethod), logicalExp,
	)
	if err != nil {
		return fmt.Errorf("ccx DSPG line %d subprogram=%d: %w", lineNum, subprogramID, err)
	}
	return nil
}

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
	purseStart    := parseCCXDate(parser.Field(fields, 13))
	purseEnd      := parseCCXDate(parser.Field(fields, 14))

	// spend categories: last non-empty field
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
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now())
		ON CONFLICT (subprogram_id, purse_number, file_date) DO UPDATE SET
			purse_status         = EXCLUDED.purse_status,
			spend_categories_raw = EXCLUDED.spend_categories_raw`,
		subprogramID, nullInt(purseNumber), nullStr(purseCode), l.fileDate,
		nullStr(filterType), nullInt(filterNetID), nullStr(filterNetName),
		isActive, minLoad, maxLoad, minBalance, maxBalance,
		nullStr(purseStatus), purseStart, purseEnd,
		nullStr(spendCats),
	)
	if err != nil {
		return fmt.Errorf("ccx DPUR line %d subprogram=%d purse=%s: %w", lineNum, subprogramID, purseCode, err)
	}
	return nil
}

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
			bin          = EXCLUDED.bin`,
		nullInt(subprogramID), nullInt(packageID),
		nullStr(packageName), nullStr(packageCode), nullStr(bin), l.fileDate,
	)
	if err != nil {
		return fmt.Errorf("ccx DPKG line %d subprogram=%d package=%d: %w", lineNum, subprogramID, packageID, err)
	}
	return nil
}

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
	t := parser.FieldDate([]string{"", parts[0] + parts[1] + parts[2]}, 1)
	if t.IsZero() {
		return nil
	}
	return t
}

func nullStr(s string) interface{} { if s == "" { return nil }; return s }
func nullInt(v int64) interface{}   { if v == 0 { return nil }; return v }
