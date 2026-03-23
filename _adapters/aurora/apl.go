package aurora

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/approved_products/versioning"
)

// APLRepo implements versioning.APLVersionRepository against Aurora PostgreSQL.
//
// apl_versions is immutable once created (§4.2.5). Rows are NEVER updated or deleted.
// Activating a new version = updating apl_rules.active_version_id atomically (§4.2.6).
//
// version_number is monotonically increasing per (program_id, benefit_type). This repo
// derives the next version number by querying MAX(version_number) + 1 within the same
// transaction as the INSERT — no sequence needed, and the UNIQUE constraint on
// (program_id, benefit_type, version_number) enforces uniqueness under concurrent writes.
type APLRepo struct {
	pool *pgxpool.Pool
}

// NewAPLRepo constructs an APLRepo backed by the given connection pool.
func NewAPLRepo(pool *pgxpool.Pool) *APLRepo {
	return &APLRepo{pool: pool}
}

// CreateVersion inserts a new immutable APL version row.
//
// v.VersionNumber is ignored on input — it is derived atomically inside the transaction
// as MAX(version_number)+1 for the same (program_id, benefit_type). The assigned version
// number is written back to v.VersionNumber on success.
//
// v.ID must be set by the caller (gen_random_uuid() is not used here — caller owns the UUID
// so tests can assert against a known value and replays produce stable IDs).
func (r *APLRepo) CreateVersion(ctx context.Context, v *versioning.APLVersion) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("apl.CreateVersion: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Derive next version number atomically within the transaction.
	// COALESCE handles the first version (no prior rows → 0 + 1 = 1).
	var nextVersion int
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM public.apl_versions
		WHERE program_id   = $1
		  AND benefit_type = $2`,
		v.ProgramID, v.BenefitType,
	).Scan(&nextVersion)
	if err != nil {
		return fmt.Errorf("apl.CreateVersion: derive version number: %w", err)
	}

	uploadedBy := v.UploadedBy
	if uploadedBy == "" {
		uploadedBy = "apl-uploader"
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO public.apl_versions (
			id, program_id, benefit_type,
			version_number, rule_count, s3_key,
			uploaded_at, uploaded_by,
			fis_ack_received_at, notes
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8,
			$9, $10
		)`,
		v.ID, v.ProgramID, v.BenefitType,
		nextVersion, v.RuleCount, v.S3Key,
		v.UploadedAt, uploadedBy,
		v.FISAckReceivedAt, v.Notes,
	)
	if err != nil {
		return fmt.Errorf("apl.CreateVersion: insert apl_versions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("apl.CreateVersion: commit: %w", err)
	}

	// Write the derived version number back to the caller's struct.
	v.VersionNumber = nextVersion
	return nil
}

// ActivateVersion atomically updates apl_rules.active_version_id for the given
// (programID, benefitType) to point at versionID.
//
// The apl_rules row must already exist — this is a pure UPDATE, not an upsert.
// apl_rules rows are created during program setup (Selvi Marappan / ACC process).
// Returns an error if no apl_rules row exists for the (programID, benefitType) pair.
func (r *APLRepo) ActivateVersion(
	ctx context.Context,
	programID uuid.UUID,
	benefitType string,
	versionID uuid.UUID,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE public.apl_rules
		SET
			active_version_id = $1,
			updated_at        = $2
		WHERE program_id   = $3
		  AND benefit_type = $4`,
		versionID, time.Now().UTC(),
		programID, benefitType,
	)
	if err != nil {
		return fmt.Errorf("apl.ActivateVersion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf(
			"apl.ActivateVersion: no apl_rules row found for program_id=%s benefit_type=%s — "+
				"program setup must be completed before activating an APL version",
			programID, benefitType,
		)
	}
	return nil
}

// GetActiveVersion returns the currently active APL version for the given
// (programID, benefitType), joining apl_rules → apl_versions.
//
// Returns versioning.ErrNoActiveVersion if no apl_rules row exists for the pair
// (program not yet set up) or if active_version_id is not yet set.
func (r *APLRepo) GetActiveVersion(
	ctx context.Context,
	programID uuid.UUID,
	benefitType string,
) (*versioning.APLVersion, error) {
	v := &versioning.APLVersion{}
	var fisAck *time.Time
	var notes *string

	err := r.pool.QueryRow(ctx, `
		SELECT
			av.id, av.program_id, av.benefit_type,
			av.version_number, av.rule_count, av.s3_key,
			av.uploaded_at, av.uploaded_by,
			av.fis_ack_received_at, av.notes
		FROM public.apl_rules ar
		JOIN public.apl_versions av ON av.id = ar.active_version_id
		WHERE ar.program_id   = $1
		  AND ar.benefit_type = $2`,
		programID, benefitType,
	).Scan(
		&v.ID, &v.ProgramID, &v.BenefitType,
		&v.VersionNumber, &v.RuleCount, &v.S3Key,
		&v.UploadedAt, &v.UploadedBy,
		&fisAck, &notes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, versioning.ErrNoActiveVersion
		}
		return nil, fmt.Errorf("apl.GetActiveVersion: %w", err)
	}

	v.FISAckReceivedAt = fisAck
	v.Notes = notes
	return v, nil
}
