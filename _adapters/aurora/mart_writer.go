package aurora

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/ports"
)

// MartWriterRepo is the Aurora implementation of ports.MartWriter.
// Column names match the live DEV Aurora reporting schema.
// All methods are idempotent (§4.3.12).
type MartWriterRepo struct {
	pool *pgxpool.Pool
}

func NewMartWriterRepo(pool *pgxpool.Pool) *MartWriterRepo {
	return &MartWriterRepo{pool: pool}
}

// UpsertMember inserts into reporting.dim_member if not already present.
// Natural key: (client_member_id, tenant_id). Returns member_sk.
func (r *MartWriterRepo) UpsertMember(ctx context.Context, m *ports.MemberRecord) (int64, error) {
	var sk int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO reporting.dim_member (
			tenant_id, client_member_id,
			first_name, last_name, date_of_birth,
			address1, address2, city, state, zip,
			fis_subprogram_id, processor,
			row_effective_date, row_expiry_date, is_current, created_at
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'FIS',
		       $12, '9999-12-31'::timestamptz, true, $12
		WHERE NOT EXISTS (
			SELECT 1 FROM reporting.dim_member
			WHERE client_member_id = $2 AND tenant_id = $1
		)
		RETURNING member_sk`,
		m.TenantID, m.PlanMemberID,
		nullString(m.FirstName), nullString(m.LastName), nullTime(m.DOB),
		nullString(m.Address1), nullString(m.Address2),
		nullString(m.City), nullString(m.State), nullString(m.ZIP),
		nullInt64(m.SubprogramID), m.EffectiveDate,
	).Scan(&sk)
	if err != nil {
		// Row already exists — look up sk
		if lookupErr := r.pool.QueryRow(ctx,
			`SELECT member_sk FROM reporting.dim_member
			 WHERE client_member_id = $1 AND tenant_id = $2 LIMIT 1`,
			m.PlanMemberID, m.TenantID,
		).Scan(&sk); lookupErr != nil {
			return 0, fmt.Errorf("mart: UpsertMember (member=%s): %w", m.PlanMemberID, lookupErr)
		}
	}
	return sk, nil
}

// UpsertCard inserts into reporting.dim_card if not already present.
// Natural key: (dim_member_sk). Returns card_sk.
func (r *MartWriterRepo) UpsertCard(ctx context.Context, c *ports.CardRecord) (int64, error) {
	var sk int64
	proxyNumber := c.ProxyNumber
	if len(proxyNumber) == 0 {
		proxyNumber = c.CardID.String()[:30]
	}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO reporting.dim_card (
			tenant_id, client_member_id,
			fis_card_id, pan_masked, fis_proxy_number,
			card_status, package_id,
			dim_member_sk, processor,
			row_effective_date, row_expiry_date, is_current, created_at
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, 'FIS',
		       $9, '9999-12-31'::timestamptz, true, $9
		WHERE NOT EXISTS (
			SELECT 1 FROM reporting.dim_card WHERE dim_member_sk = $8
		)
		RETURNING card_sk`,
		c.TenantID, c.CardID.String(),
		nullString(c.FISCardID), nullString(c.PANMasked), proxyNumber,
		c.CardStatus, nullString(c.PackageID),
		c.MemberSK, c.EffectiveDate,
	).Scan(&sk)
	if err != nil {
		if lookupErr := r.pool.QueryRow(ctx,
			`SELECT card_sk FROM reporting.dim_card WHERE dim_member_sk = $1 LIMIT 1`,
			c.MemberSK,
		).Scan(&sk); lookupErr != nil {
			return 0, fmt.Errorf("mart: UpsertCard (member_sk=%d): %w", c.MemberSK, lookupErr)
		}
	}
	return sk, nil
}

// UpsertPurse inserts into reporting.dim_purse if not already present.
// Natural key: (fk_card, fis_purse_name). Returns purse_sk.
func (r *MartWriterRepo) UpsertPurse(ctx context.Context, p *ports.PurseRecord) (int64, error) {
	var sk int64
	err := r.pool.QueryRow(ctx, `
		INSERT INTO reporting.dim_purse (
			fk_card, fis_purse_number, fis_purse_name,
			purse_status, effective_date, expiration_date,
			max_value, max_load, autoload_amount, created_at
		)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, now()
		WHERE NOT EXISTS (
			SELECT 1 FROM reporting.dim_purse
			WHERE fk_card = $1 AND fis_purse_name = $3
		)
		RETURNING purse_sk`,
		p.CardSK, p.FISPurseNumber, p.FISPurseName,
		p.PurseStatus, nullTime(p.EffectiveDate), nullTime(p.ExpirationDate),
		nullInt64(p.MaxValue), nullInt64(p.MaxLoad), nullInt64(p.AutoloadAmount),
	).Scan(&sk)
	if err != nil {
		if lookupErr := r.pool.QueryRow(ctx,
			`SELECT purse_sk FROM reporting.dim_purse
			 WHERE fk_card = $1 AND fis_purse_name = $2 LIMIT 1`,
			p.CardSK, p.FISPurseName,
		).Scan(&sk); lookupErr != nil {
			return 0, fmt.Errorf("mart: UpsertPurse (card_sk=%d purse=%s): %w",
				p.CardSK, p.FISPurseName, lookupErr)
		}
	}
	return sk, nil
}

// WriteEnrollmentFact inserts into reporting.fact_enrollments.
// Idempotency: ON CONFLICT (correlation_id, row_sequence_number) DO NOTHING.
func (r *MartWriterRepo) WriteEnrollmentFact(ctx context.Context, f *ports.EnrollmentFact) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reporting.fact_enrollments (
			dim_member_sk, dim_program_sk, date_sk, tenant_id,
			correlation_id, row_sequence_number,
			srg_file_type, event_type, fis_record_type,
			processing_status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), now())
		ON CONFLICT (correlation_id, row_sequence_number) DO NOTHING`,
		nullInt64SK(f.MemberSK), nullInt64SK(f.ProgramSK), f.DateSK, f.TenantID,
		f.CorrelationID, f.RowSequenceNumber,
		f.SRGFileType, f.EventType, f.FISRecordType, f.ProcessingStatus,
	)
	if err != nil {
		return fmt.Errorf("mart: WriteEnrollmentFact (corr=%s seq=%d): %w",
			f.CorrelationID, f.RowSequenceNumber, err)
	}
	return nil
}

// WritePurseLifecycleFact inserts into reporting.fact_purse_lifecycle.
// Idempotency: ON CONFLICT (correlation_id, row_sequence_number, event_type) DO NOTHING.
func (r *MartWriterRepo) WritePurseLifecycleFact(ctx context.Context, f *ports.PurseLifecycleFact) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reporting.fact_purse_lifecycle (
			dim_purse_sk, dim_member_sk, date_sk, tenant_id,
			correlation_id, row_sequence_number, event_type,
			amount_cents, balance_before_cents, balance_after_cents,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (correlation_id, row_sequence_number, event_type) DO NOTHING`,
		nullInt64SK(f.PurseSK), nullInt64SK(f.MemberSK), f.DateSK, f.TenantID,
		f.CorrelationID, f.RowSequenceNumber, f.EventType,
		f.AmountCents, f.BalanceBeforeCents, f.BalanceAfterCents,
	)
	if err != nil {
		return fmt.Errorf("mart: WritePurseLifecycleFact (corr=%s event=%s): %w",
			f.CorrelationID, f.EventType, err)
	}
	return nil
}

// WriteReconciliationFact inserts into reporting.fact_reconciliation.
// Idempotency: ON CONFLICT (batch_file_id, row_sequence_number) DO NOTHING.
func (r *MartWriterRepo) WriteReconciliationFact(ctx context.Context, f *ports.ReconciliationFact) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reporting.fact_reconciliation (
			dim_member_sk, date_sk, tenant_id,
			correlation_id, batch_file_id, row_sequence_number,
			fis_result_code, fis_result_message,
			reconciliation_status, reconciled_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (batch_file_id, row_sequence_number) DO NOTHING`,
		nullInt64SK(f.MemberSK), f.DateSK, f.TenantID,
		f.CorrelationID, f.BatchFileID, f.RowSequenceNumber,
		f.FISResultCode, f.FISResultMessage, f.ReconciliationStatus,
	)
	if err != nil {
		return fmt.Errorf("mart: WriteReconciliationFact (file=%s seq=%d): %w",
			f.BatchFileID, f.RowSequenceNumber, err)
	}
	return nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func nullString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nullInt64SK(sk int64) interface{} {
	if sk == 0 {
		return nil
	}
	return sk
}
