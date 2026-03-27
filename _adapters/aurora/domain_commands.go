package aurora

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/ports"
)

// DomainCommandRepo implements ports.DomainCommandRepository against Aurora.
//
// This is the two-layer idempotency gate (§4.1.1).
// Layer 1: FindByIdempotencyKey — UUID short-circuit for HTTP retry safety.
// Layer 2: FindDuplicate — composite business-rule check across all callers.
// The DB enforces both via partial unique index (idempotency_key) and
// composite unique constraint (tenant_id, client_member_id, command_type, benefit_period). Insert attempts that violate it return a duplicate key error
// which we surface as a Duplicate status — never a fatal error.
//
// MANDATORY: Insert must be called BEFORE any domain state mutation.
// The sequence is: Insert → check status → if Duplicate return early → write domain state.
type DomainCommandRepo struct {
	pool *pgxpool.Pool
}

func NewDomainCommandRepo(pool *pgxpool.Pool) *DomainCommandRepo {
	return &DomainCommandRepo{pool: pool}
}

func (r *DomainCommandRepo) Insert(ctx context.Context, cmd *ports.DomainCommand) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.domain_commands (
			id, correlation_id, idempotency_key, tenant_id, client_member_id,
			command_type, benefit_period, status,
			batch_file_id, sequence_in_file,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10,
			$11
		)`,
		cmd.ID, cmd.CorrelationID, cmd.IdempotencyKey, cmd.TenantID, cmd.ClientMemberID,
		cmd.CommandType, cmd.BenefitPeriod, cmd.Status,
		cmd.BatchFileID, cmd.SequenceInFile,
		cmd.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("domain_commands.Insert: %w", err)
	}
	return nil
}

// FindByIdempotencyKey is the Layer 1 short-circuit lookup.
// Called before the composite check — if the caller retries with the same
// X-Idempotency-Key UUID, we return the cached result immediately without
// re-evaluating the composite business key.
func (r *DomainCommandRepo) FindByIdempotencyKey(ctx context.Context, key uuid.UUID) (*ports.DomainCommand, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, correlation_id, idempotency_key, tenant_id, client_member_id,
		       command_type, benefit_period, status,
		       batch_file_id, sequence_in_file,
		       created_at, completed_at, failure_reason
		FROM public.domain_commands
		WHERE idempotency_key = $1`,
		key,
	)
	cmd := &ports.DomainCommand{}
	err := row.Scan(
		&cmd.ID, &cmd.CorrelationID, &cmd.IdempotencyKey, &cmd.TenantID, &cmd.ClientMemberID,
		&cmd.CommandType, &cmd.BenefitPeriod, &cmd.Status,
		&cmd.BatchFileID, &cmd.SequenceInFile,
		&cmd.CreatedAt, &cmd.CompletedAt, &cmd.FailureReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("domain_commands.FindByIdempotencyKey: %w", err)
	}
	return cmd, nil
}

func (r *DomainCommandRepo) FindDuplicate(
	ctx context.Context,
	tenantID, clientMemberID, commandType, benefitPeriod string,
) (*ports.DomainCommand, error) {
	// Check across ALL files — correlation_id is intentionally excluded.
	// A member submitted in any prior file for this benefit period is a duplicate.
	// Only FAILED commands are excluded — they may be retried.
	row := r.pool.QueryRow(ctx, `
		SELECT id, correlation_id, idempotency_key, tenant_id, client_member_id,
		       command_type, benefit_period, status,
		       batch_file_id, sequence_in_file,
		       created_at, completed_at, failure_reason
		FROM public.domain_commands
		WHERE tenant_id       = $1
		  AND client_member_id = $2
		  AND command_type    = $3
		  AND benefit_period  = $4
		  AND status         != 'Failed'
		LIMIT 1`,
		tenantID, clientMemberID, commandType, benefitPeriod,
	)

	cmd := &ports.DomainCommand{}
	err := row.Scan(
		&cmd.ID, &cmd.CorrelationID, &cmd.IdempotencyKey, &cmd.TenantID, &cmd.ClientMemberID,
		&cmd.CommandType, &cmd.BenefitPeriod, &cmd.Status,
		&cmd.BatchFileID, &cmd.SequenceInFile,
		&cmd.CreatedAt, &cmd.CompletedAt, &cmd.FailureReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // not a duplicate
	}
	if err != nil {
		return nil, fmt.Errorf("domain_commands.FindDuplicate: %w", err)
	}
	return cmd, nil
}

func (r *DomainCommandRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string, failureReason *string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE public.domain_commands
		SET status = $1,
		    failure_reason = $2,
		    completed_at = CASE WHEN $1 IN ('Completed','Failed') THEN NOW() ELSE completed_at END
		WHERE id = $3`,
		status, failureReason, id,
	)
	if err != nil {
		return fmt.Errorf("domain_commands.UpdateStatus: %w", err)
	}
	return nil
}
