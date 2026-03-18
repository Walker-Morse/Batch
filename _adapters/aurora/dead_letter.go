package aurora

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/ports"
)

// DeadLetterRepo implements ports.DeadLetterRepository against Aurora.
//
// Write is called synchronously by the ingest-task on unrecoverable exception —
// BEFORE any retry or stage-skip logic. If this write itself fails, the ingest-task
// must transition batch_files → HALTED and page on-call immediately (§6.5.2).
//
// message_body stores PHI. Access controlled at DB layer via ingest_task_role.
// Never log message_body contents at any log level (§6.5.2, §7.2).
type DeadLetterRepo struct {
	pool *pgxpool.Pool
}

func NewDeadLetterRepo(pool *pgxpool.Pool) *DeadLetterRepo {
	return &DeadLetterRepo{pool: pool}
}

func (r *DeadLetterRepo) Write(ctx context.Context, e *ports.DeadLetterEntry) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.dead_letter_store (
			id, correlation_id, batch_file_id, row_sequence_number,
			tenant_id, client_member_id,
			failure_stage, failure_reason,
			message_body, retry_count, created_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8,
			$9, $10, $11
		)`,
		e.ID, e.CorrelationID, e.BatchFileID, e.RowSequenceNumber,
		e.TenantID, e.ClientMemberID,
		e.FailureStage, e.FailureReason,
		e.MessageBody, e.RetryCount, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("dead_letter.Write: %w", err)
	}
	return nil
}

func (r *DeadLetterRepo) ListUnresolved(ctx context.Context, correlationID uuid.UUID) ([]*ports.DeadLetterEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, correlation_id, batch_file_id, row_sequence_number,
		       tenant_id, client_member_id,
		       failure_stage, failure_reason,
		       retry_count, last_retry_at, replayed_at,
		       resolved_at, resolved_by, resolution_notes, created_at
		FROM public.dead_letter_store
		WHERE correlation_id = $1
		  AND resolved_at IS NULL
		ORDER BY created_at`,
		correlationID,
	)
	if err != nil {
		return nil, fmt.Errorf("dead_letter.ListUnresolved: %w", err)
	}
	defer rows.Close()

	var entries []*ports.DeadLetterEntry
	for rows.Next() {
		e := &ports.DeadLetterEntry{}
		if err := rows.Scan(
			&e.ID, &e.CorrelationID, &e.BatchFileID, &e.RowSequenceNumber,
			&e.TenantID, &e.ClientMemberID,
			&e.FailureStage, &e.FailureReason,
			&e.RetryCount, &e.LastRetryAt, &e.ReplayedAt,
			&e.ResolvedAt, &e.ResolvedBy, &e.ResolutionNotes, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("dead_letter.ListUnresolved scan: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (r *DeadLetterRepo) MarkReplayed(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.dead_letter_store
		 SET replayed_at = $1,
		     retry_count = retry_count + 1,
		     last_retry_at = $1
		 WHERE id = $2`,
		at, id,
	)
	if err != nil {
		return fmt.Errorf("dead_letter.MarkReplayed: %w", err)
	}
	return nil
}

func (r *DeadLetterRepo) MarkResolved(ctx context.Context, id uuid.UUID, resolvedBy, notes string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.dead_letter_store
		 SET resolved_at = NOW(),
		     resolved_by = $1,
		     resolution_notes = $2
		 WHERE id = $3`,
		resolvedBy, notes, id,
	)
	if err != nil {
		return fmt.Errorf("dead_letter.MarkResolved: %w", err)
	}
	return nil
}
