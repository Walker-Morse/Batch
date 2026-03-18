package aurora

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/ports"
)

// BatchFileRepo implements ports.BatchFileRepository against Aurora PostgreSQL.
// This is the non-repudiation anchor — every write here happens before any
// processing begins. Write failures must abort Stage 1 entirely (§4.1).
type BatchFileRepo struct {
	pool *pgxpool.Pool
}

func NewBatchFileRepo(pool *pgxpool.Pool) *BatchFileRepo {
	return &BatchFileRepo{pool: pool}
}

func (r *BatchFileRepo) Create(ctx context.Context, f *ports.BatchFile) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.batch_files (
			id, correlation_id, tenant_id, client_id, file_type,
			status, malformed_count,
			sha256_encrypted, sha256_plaintext,
			arrived_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9,
			$10, $11
		)`,
		f.ID, f.CorrelationID, f.TenantID, f.ClientID, f.FileType,
		f.Status, f.MalformedCount,
		f.SHA256Encrypted, f.SHA256Plaintext,
		f.ArrivedAt, f.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("batch_files.Create: %w", err)
	}
	return nil
}

func (r *BatchFileRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status string, updatedAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.batch_files SET status=$1, updated_at=$2 WHERE id=$3`,
		status, updatedAt, id,
	)
	if err != nil {
		return fmt.Errorf("batch_files.UpdateStatus: %w", err)
	}
	return nil
}

func (r *BatchFileRepo) IncrementMalformedCount(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.batch_files SET malformed_count = malformed_count + 1, updated_at = NOW() WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("batch_files.IncrementMalformedCount: %w", err)
	}
	return nil
}

func (r *BatchFileRepo) GetByCorrelationID(ctx context.Context, correlationID uuid.UUID) (*ports.BatchFile, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, correlation_id, tenant_id, client_id, file_type,
		       status, record_count, malformed_count,
		       sha256_encrypted, sha256_plaintext,
		       arrived_at, submitted_at, updated_at
		FROM public.batch_files
		WHERE correlation_id = $1`,
		correlationID,
	)

	f := &ports.BatchFile{}
	err := row.Scan(
		&f.ID, &f.CorrelationID, &f.TenantID, &f.ClientID, &f.FileType,
		&f.Status, &f.RecordCount, &f.MalformedCount,
		&f.SHA256Encrypted, &f.SHA256Plaintext,
		&f.ArrivedAt, &f.SubmittedAt, &f.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("batch_files.GetByCorrelationID: %w", err)
	}
	return f, nil
}

func (r *BatchFileRepo) SetRecordCount(ctx context.Context, id uuid.UUID, count int) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE public.batch_files SET record_count=$1, updated_at=NOW() WHERE id=$2`,
		count, id,
	)
	if err != nil {
		return fmt.Errorf("batch_files.SetRecordCount: %w", err)
	}
	return nil
}
