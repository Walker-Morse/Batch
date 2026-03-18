package aurora

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FISSequenceRepo implements fis_adapter.SequenceStore using Aurora PostgreSQL.
// It persists the per-day per-program FIS file sequence counter (§6.6.1).
//
// FIS silently discards batch files with duplicate names (§6.5.5).
// This adapter guarantees monotonically increasing sequence numbers that survive
// container restarts and concurrent Fargate tasks (ADR-003).
//
// Sequence format: two digits in filename (ss), values 01–99.
// If the counter reaches 99, the method returns an error — the pipeline must halt.
// Caller: Assembler.AssembleFile in fis_reconciliation/fis_adapter/.
type FISSequenceRepo struct {
	pool *pgxpool.Pool
}

// NewFISSequenceRepo constructs a FISSequenceRepo backed by the Aurora pool.
func NewFISSequenceRepo(pool *pgxpool.Pool) *FISSequenceRepo {
	return &FISSequenceRepo{pool: pool}
}

// Next atomically claims the next sequence number for a given programID and date.
// The programID is the programs.id UUID (resolved in Stage 3 via ProgramLookup).
// The date is the UTC wall-clock date of the pipeline run — FIS sequences are per day.
//
// Returns an error if:
//   - The DB call fails
//   - The sequence for this program+date has reached 99 (FIS file name limit)
//
// Uses NextFISSequence() PostgreSQL function which performs an atomic
// INSERT ... ON CONFLICT DO UPDATE — safe under concurrent callers.
func (r *FISSequenceRepo) Next(ctx context.Context, programID string, date time.Time) (int, error) {
	pid, err := uuid.Parse(programID)
	if err != nil {
		return 0, fmt.Errorf("fis_sequence.Next: invalid programID %q: %w", programID, err)
	}

	sequenceDate := date.UTC().Truncate(24 * time.Hour)

	var seq int
	err = r.pool.QueryRow(ctx,
		`SELECT public.NextFISSequence($1, $2)`,
		pid, sequenceDate,
	).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("fis_sequence.Next(program=%s date=%s): %w",
			programID, sequenceDate.Format("2006-01-02"), err)
	}

	// FIS filename has a two-digit field — max value 99 (§6.6.1).
	// Exceeding this would produce a filename collision or a truncated/malformed name.
	if seq > 99 {
		return 0, fmt.Errorf(
			"fis_sequence.Next: sequence %d exceeds FIS maximum of 99 for program=%s date=%s — halt pipeline",
			seq, programID, sequenceDate.Format("2006-01-02"),
		)
	}

	return seq, nil
}
