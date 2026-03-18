package aurora

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/domain"
)

// DomainStateRepo handles writes and reads for consumers, cards, and purses.
// These are the DDD aggregate root and its owned entities.
// All PHI fields annotated — access controlled at DB layer via ingest_task_role.
type DomainStateRepo struct {
	pool *pgxpool.Pool
}

func NewDomainStateRepo(pool *pgxpool.Pool) *DomainStateRepo {
	return &DomainStateRepo{pool: pool}
}

// UpsertConsumer inserts or updates a consumer record.
// Uses ON CONFLICT (client_member_id, tenant_id) for idempotent replay support.
func (r *DomainStateRepo) UpsertConsumer(ctx context.Context, c *domain.Consumer) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.consumers (
			id, tenant_id, client_member_id, status,
			fis_person_id, fis_cuid,
			first_name, last_name, date_of_birth,
			address_1, address_2, city, state, zip, email,
			program_id, subprogram_id, contract_pbp, custom_card_id,
			source_batch_file_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8, $9,
			$10, $11, $12, $13, $14, $15,
			$16, $17, $18, $19,
			$20, $21, $22
		)
		ON CONFLICT (client_member_id, tenant_id) DO UPDATE SET
			status = EXCLUDED.status,
			first_name = EXCLUDED.first_name,
			last_name = EXCLUDED.last_name,
			date_of_birth = EXCLUDED.date_of_birth,
			address_1 = EXCLUDED.address_1,
			address_2 = EXCLUDED.address_2,
			city = EXCLUDED.city,
			state = EXCLUDED.state,
			zip = EXCLUDED.zip,
			email = EXCLUDED.email,
			updated_at = EXCLUDED.updated_at`,
		c.ID, c.TenantID, c.ClientMemberID, string(c.Status),
		c.FISPersonID, c.FISCUID,
		c.FirstName, c.LastName, c.DOB,
		c.Address1, c.Address2, c.City, c.State, c.ZIP, c.Email,
		c.ProgramID, c.SubprogramID, c.ContractPBP, c.CustomCardID,
		c.SourceBatchFileID, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("consumers.Upsert: %w", err)
	}
	return nil
}

// UpdateConsumerFISIdentifiers populates fis_person_id and fis_cuid from the RT30 return file.
// Called during Stage 7 reconciliation. Marks the consumer as resolved (§4.2.1).
func (r *DomainStateRepo) UpdateConsumerFISIdentifiers(ctx context.Context, id uuid.UUID, fisPersonID, fisCUID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.consumers SET fis_person_id=$1, fis_cuid=$2, updated_at=NOW() WHERE id=$3`,
		fisPersonID, fisCUID, id,
	)
	if err != nil {
		return fmt.Errorf("consumers.UpdateFISIdentifiers: %w", err)
	}
	return nil
}

// GetConsumerByNaturalKey looks up a consumer by the composite natural key.
func (r *DomainStateRepo) GetConsumerByNaturalKey(ctx context.Context, tenantID, clientMemberID string) (*domain.Consumer, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, client_member_id, status,
		       fis_person_id, fis_cuid,
		       first_name, last_name, date_of_birth,
		       address_1, address_2, city, state, zip, email,
		       program_id, subprogram_id
		FROM public.consumers
		WHERE tenant_id=$1 AND client_member_id=$2`,
		tenantID, clientMemberID,
	)

	c := &domain.Consumer{}
	var status string
	err := row.Scan(
		&c.ID, &c.TenantID, &c.ClientMemberID, &status,
		&c.FISPersonID, &c.FISCUID,
		&c.FirstName, &c.LastName, &c.DOB,
		&c.Address1, &c.Address2, &c.City, &c.State, &c.ZIP, &c.Email,
		&c.ProgramID, &c.SubprogramID,
	)
	if err != nil {
		return nil, fmt.Errorf("consumers.GetByNaturalKey: %w", err)
	}
	c.Status = domain.ConsumerStatus(status)
	return c, nil
}

// InsertCard creates a new card record for a consumer.
func (r *DomainStateRepo) InsertCard(ctx context.Context, c *domain.Card) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.cards (
			id, tenant_id, consumer_id, client_member_id,
			fis_card_id, pan_masked, fis_proxy_number,
			card_status, card_design_id, package_id,
			source_batch_file_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10,
			$11, $12, $13
		)`,
		c.ID, c.TenantID, c.ConsumerID, c.ClientMemberID,
		c.FISCardID, c.PANMasked, c.FISProxyNumber,
		int16(c.Status), c.CardDesignID, c.PackageID,
		c.SourceBatchFileID, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("cards.Insert: %w", err)
	}
	return nil
}

// UpdateCardFISCardID populates fis_card_id and issued_at from the RT30 return file.
// Called during Stage 7 reconciliation.
func (r *DomainStateRepo) UpdateCardFISCardID(ctx context.Context, id uuid.UUID, fisCardID string, issuedAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.cards SET fis_card_id=$1, issued_at=$2, card_status=2, updated_at=NOW() WHERE id=$3`,
		fisCardID, issuedAt, id,
	)
	if err != nil {
		return fmt.Errorf("cards.UpdateFISCardID: %w", err)
	}
	return nil
}

// InsertPurse creates a new purse record for a card.
func (r *DomainStateRepo) InsertPurse(ctx context.Context, p *domain.Purse) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.purses (
			id, tenant_id, card_id, consumer_id, client_member_id,
			fis_purse_number, fis_purse_name,
			status, available_balance_cents,
			benefit_period, effective_date, expiry_date,
			program_id, benefit_type,
			source_batch_file_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9,
			$10, $11, $12,
			$13, $14,
			$15, $16, $17
		)`,
		p.ID, p.TenantID, p.CardID, p.ConsumerID, p.ClientMemberID,
		p.FISPurseNumber, p.FISPurseName,
		string(p.Status), p.AvailableBalanceCents,
		p.BenefitPeriod, p.EffectiveDate, p.ExpiryDate,
		p.ProgramID, string(p.BenefitType),
		p.SourceBatchFileID, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("purses.Insert: %w", err)
	}
	return nil
}

// UpdatePurseBalance updates the One Fintech sub-ledger balance.
// Must be called at domain_commands write time — not deferred until FIS return (§I.2).
func (r *DomainStateRepo) UpdatePurseBalance(ctx context.Context, id uuid.UUID, balanceCents int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.purses SET available_balance_cents=$1, updated_at=NOW() WHERE id=$2`,
		balanceCents, id,
	)
	if err != nil {
		return fmt.Errorf("purses.UpdateBalance: %w", err)
	}
	return nil
}

// UpdatePurseFISNumber stamps the FIS-assigned purse number from the RT60 return file.
// Called during Stage 7 reconciliation. Only set after FIS confirms the load.
func (r *DomainStateRepo) UpdatePurseFISNumber(ctx context.Context, consumerID uuid.UUID, benefitPeriod string, fisNumber int16) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.purses
		 SET fis_purse_number = $1, updated_at = NOW()
		 WHERE consumer_id = $2 AND benefit_period = $3`,
		fisNumber, consumerID, benefitPeriod,
	)
	if err != nil {
		return fmt.Errorf("purses.UpdateFISNumber: %w", err)
	}
	return nil
}

// GetConsumerByID looks up a consumer by primary key UUID.
// Used by Stage 7 to resolve consumer identity from the batch record.
func (r *DomainStateRepo) GetConsumerByID(ctx context.Context, id uuid.UUID) (*domain.Consumer, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, client_member_id, status,
		       fis_person_id, fis_cuid,
		       first_name, last_name, date_of_birth,
		       address_1, address_2, city, state, zip, email,
		       program_id, subprogram_id
		FROM public.consumers
		WHERE id = $1`, id,
	)
	c := &domain.Consumer{}
	var status string
	err := row.Scan(
		&c.ID, &c.TenantID, &c.ClientMemberID, &status,
		&c.FISPersonID, &c.FISCUID,
		&c.FirstName, &c.LastName, &c.DOB,
		&c.Address1, &c.Address2, &c.City, &c.State, &c.ZIP, &c.Email,
		&c.ProgramID, &c.SubprogramID,
	)
	if err != nil {
		return nil, fmt.Errorf("consumers.GetByID(%s): %w", id, err)
	}
	c.Status = domain.ConsumerStatus(status)
	return c, nil
}

// GetCardByConsumerID looks up a card by consumer ID.
// Phase 1: one card per consumer. Returns the first match.
func (r *DomainStateRepo) GetCardByConsumerID(ctx context.Context, consumerID uuid.UUID) (*domain.Card, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, consumer_id, client_member_id,
		       fis_card_id, pan_masked, fis_proxy_number,
		       card_status
		FROM public.cards
		WHERE consumer_id = $1
		LIMIT 1`, consumerID,
	)
	c := &domain.Card{}
	var status int16
	err := row.Scan(
		&c.ID, &c.TenantID, &c.ConsumerID, &c.ClientMemberID,
		&c.FISCardID, &c.PANMasked, &c.FISProxyNumber,
		&status,
	)
	if err != nil {
		return nil, fmt.Errorf("cards.GetByConsumerID(%s): %w", consumerID, err)
	}
	c.Status = domain.CardStatus(status)
	return c, nil
}

// GetPurseByConsumerAndBenefitPeriod looks up a purse by consumer + benefit period.
// Called during Stage 3 SRG320 processing to resolve the purse ID before updating
// the sub-ledger balance. Returns an error if no matching purse exists.
func (r *DomainStateRepo) GetPurseByConsumerAndBenefitPeriod(
	ctx context.Context,
	consumerID uuid.UUID,
	benefitPeriod string, // ISO YYYY-MM
) (*domain.Purse, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, card_id, consumer_id, client_member_id,
		       fis_purse_number, fis_purse_name,
		       status, available_balance_cents,
		       benefit_period, effective_date, expiry_date,
		       program_id, benefit_type
		FROM public.purses
		WHERE consumer_id = $1
		  AND benefit_period = $2
		  AND status IN ('ACTIVE', 'PENDING')
		LIMIT 1`,
		consumerID, benefitPeriod,
	)

	p := &domain.Purse{}
	var status, benefitType string
	err := row.Scan(
		&p.ID, &p.TenantID, &p.CardID, &p.ConsumerID, &p.ClientMemberID,
		&p.FISPurseNumber, &p.FISPurseName,
		&status, &p.AvailableBalanceCents,
		&p.BenefitPeriod, &p.EffectiveDate, &p.ExpiryDate,
		&p.ProgramID, &benefitType,
	)
	if err != nil {
		return nil, fmt.Errorf("purses.GetByConsumerAndBenefitPeriod(consumer=%s period=%s): %w",
			consumerID, benefitPeriod, err)
	}
	p.Status = domain.PurseStatus(status)
	p.BenefitType = domain.BenefitType(benefitType)
	return p, nil
}

// SumPlatformLiabilityCents returns the total available_balance_cents across all
// active and pending purses. Used by the daily FBO reconciliation (Addendum I).
func (r *DomainStateRepo) SumPlatformLiabilityCents(ctx context.Context) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(available_balance_cents), 0)
		FROM public.purses
		WHERE status IN ('ACTIVE', 'PENDING')`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("purses.SumPlatformLiability: %w", err)
	}
	return total, nil
}

// SumPendingCommandsCents returns the total AmountCents for domain_commands in
// Accepted status — in-flight batches submitted to FIS but not yet return-file
// confirmed. Counted as provisional liabilities in FBO reconciliation (Addendum I).
func (r *DomainStateRepo) SumPendingCommandsCents(ctx context.Context) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_cents), 0)
		FROM public.domain_commands
		WHERE status = 'Accepted'
		  AND command_type IN ('LOAD', 'SWEEP')`).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("domain_commands.SumPendingCents: %w", err)
	}
	return total, nil
}

// GetProgramByTenantAndSubprogram resolves the programs.id UUID from the FIS
// subprogram identifier carried on every SRG310 row.
// Called per-row during Stage 3; the RowProcessingStage caches results by
// (tenantID, fisSubprogramID) so the DB is queried at most once per unique
// subprogram value per file — not once per row.
// Returns an error if no active program row exists; Stage 3 dead-letters the row.
func (r *DomainStateRepo) GetProgramByTenantAndSubprogram(
	ctx context.Context,
	tenantID string,
	fisSubprogramID string, // string from SRG310 row e.g. "26071"; NUMERIC(10) in DB
) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx,
		`SELECT id FROM public.programs
		 WHERE tenant_id = $1
		   AND fis_subprogram_id = $2
		   AND is_active = true
		 LIMIT 1`,
		tenantID, fisSubprogramID,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf(
			"programs.GetByTenantAndSubprogram(tenant=%s subprogram=%s): %w",
			tenantID, fisSubprogramID, err,
		)
	}
	return id, nil
}
