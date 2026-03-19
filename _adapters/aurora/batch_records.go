package aurora

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/walker-morse/batch/_shared/ports"
)

// BatchRecordRT30 is the staged FIS RT30 record (new account / card issuance).
// All demographic fields are PHI. raw_payload must never be logged.
type BatchRecordRT30 struct {
	ID               uuid.UUID
	BatchFileID      uuid.UUID
	DomainCommandID  uuid.UUID
	CorrelationID    uuid.UUID
	TenantID         string
	SequenceInFile   int
	Status           string // STAGED|SUBMITTED|COMPLETED|FAILED
	StagedAt         time.Time
	ClientMemberID   string
	ProgramID        uuid.UUID // programs.id — resolved by Stage 3; required by Stage 4 for fis_sequence.Next
	SubprogramID     *int64
	PackageID        *string
	FirstName        *string
	LastName         *string
	DateOfBirth      *time.Time
	Address1         *string
	Address2         *string
	City             *string
	State            *string
	ZIP              *string
	Email            *string
	CardDesignID     *string
	CustomCardID     *string
	// FIS return fields — nil until Stage 7
	FISPersonID      *string
	FISCUID          *string
	FISCardID        *string
	RawPayload       json.RawMessage // PHI — never log
}

// BatchRecordRT37 is the staged FIS RT37 record (card status update).
type BatchRecordRT37 struct {
	ID              uuid.UUID
	BatchFileID     uuid.UUID
	DomainCommandID uuid.UUID
	CorrelationID   uuid.UUID
	TenantID        string
	SequenceInFile  int
	Status          string
	StagedAt        time.Time
	ClientMemberID  string
	FISCardID       string // NOT NULL — required to stage RT37
	CardStatusCode  int16  // 2=Active 4=Lost 6=Suspended 7=Closed
	ReasonCode      *string
	RawPayload      json.RawMessage
}

// BatchRecordRT60 is the staged FIS RT60 record (purse load / fund transfer).
type BatchRecordRT60 struct {
	ID              uuid.UUID
	BatchFileID     uuid.UUID
	DomainCommandID uuid.UUID
	CorrelationID   uuid.UUID
	TenantID        string
	SequenceInFile  int
	Status          string
	StagedAt        time.Time
	ATCode          *string // AT01=load, AT30=sweep
	ClientMemberID  string
	FISCardID       string
	FISPurseNumber  *int16
	PurseName       *string
	AmountCents     int64 // positive=load, negative=sweep
	EffectiveDate   time.Time
	ExpiryDate      *time.Time
	ClientReference *string
	RawPayload      json.RawMessage
}

// BatchRecordsRepo handles writes for all three RT record types.
type BatchRecordsRepo struct {
	pool *pgxpool.Pool
}

func NewBatchRecordsRepo(pool *pgxpool.Pool) *BatchRecordsRepo {
	return &BatchRecordsRepo{pool: pool}
}

func (r *BatchRecordsRepo) InsertRT30(ctx context.Context, rec *BatchRecordRT30) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.batch_records_rt30 (
			id, batch_file_id, domain_command_id, correlation_id,
			tenant_id, sequence_in_file, status, staged_at,
			client_member_id, program_id, subprogram_id, package_id,
			first_name, last_name, date_of_birth,
			address_1, address_2, city, state, zip, email,
			card_design_id, custom_card_id,
			raw_payload
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11, $12,
			$13, $14, $15,
			$16, $17, $18, $19, $20, $21,
			$22, $23,
			$24
		)`,
		rec.ID, rec.BatchFileID, rec.DomainCommandID, rec.CorrelationID,
		rec.TenantID, rec.SequenceInFile, rec.Status, rec.StagedAt,
		rec.ClientMemberID, rec.ProgramID, rec.SubprogramID, rec.PackageID,
		rec.FirstName, rec.LastName, rec.DateOfBirth,
		rec.Address1, rec.Address2, rec.City, rec.State, rec.ZIP, rec.Email,
		rec.CardDesignID, rec.CustomCardID,
		rec.RawPayload,
	)
	if err != nil {
		return fmt.Errorf("batch_records_rt30.Insert: %w", err)
	}
	return nil
}

func (r *BatchRecordsRepo) InsertRT37(ctx context.Context, rec *BatchRecordRT37) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.batch_records_rt37 (
			id, batch_file_id, domain_command_id, correlation_id,
			tenant_id, sequence_in_file, status, staged_at,
			client_member_id, fis_card_id, card_status_code,
			reason_code, raw_payload
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13
		)`,
		rec.ID, rec.BatchFileID, rec.DomainCommandID, rec.CorrelationID,
		rec.TenantID, rec.SequenceInFile, rec.Status, rec.StagedAt,
		rec.ClientMemberID, rec.FISCardID, rec.CardStatusCode,
		rec.ReasonCode, rec.RawPayload,
	)
	if err != nil {
		return fmt.Errorf("batch_records_rt37.Insert: %w", err)
	}
	return nil
}

func (r *BatchRecordsRepo) InsertRT60(ctx context.Context, rec *BatchRecordRT60) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO public.batch_records_rt60 (
			id, batch_file_id, domain_command_id, correlation_id,
			tenant_id, sequence_in_file, status, staged_at,
			at_code, client_member_id, fis_card_id,
			fis_purse_number, purse_name,
			amount_cents, effective_date, expiry_date,
			client_reference, raw_payload
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13,
			$14, $15, $16,
			$17, $18
		)`,
		rec.ID, rec.BatchFileID, rec.DomainCommandID, rec.CorrelationID,
		rec.TenantID, rec.SequenceInFile, rec.Status, rec.StagedAt,
		rec.ATCode, rec.ClientMemberID, rec.FISCardID,
		rec.FISPurseNumber, rec.PurseName,
		rec.AmountCents, rec.EffectiveDate, rec.ExpiryDate,
		rec.ClientReference, rec.RawPayload,
	)
	if err != nil {
		return fmt.Errorf("batch_records_rt60.Insert: %w", err)
	}
	return nil
}

func (r *BatchRecordsRepo) UpdateStatus(ctx context.Context, id uuid.UUID, recordType string, status string, fisResultCode, fisResultMessage *string) error {
	var table string
	switch recordType {
	case "RT30":
		table = "batch_records_rt30"
	case "RT37":
		table = "batch_records_rt37"
	case "RT60":
		table = "batch_records_rt60"
	default:
		return fmt.Errorf("batch_records.UpdateStatus: unknown record type %q", recordType)
	}

	query := fmt.Sprintf(`
		UPDATE public.%s
		SET status = $1,
		    fis_result_code = $2,
		    fis_result_message = $3,
		    completed_at = CASE WHEN $1 IN ('COMPLETED','FAILED') THEN NOW() ELSE completed_at END
		WHERE id = $4`, table)

	_, err := r.pool.Exec(ctx, query, status, fisResultCode, fisResultMessage, id)
	if err != nil {
		return fmt.Errorf("batch_records.UpdateStatus (%s): %w", recordType, err)
	}
	return nil
}

// ListStagedByCorrelationID returns all STAGED batch records for the given
// correlation ID, ordered by sequence_in_file ASC within each record type.
// Called by AssemblerImpl (Stage 4) to populate the FIS batch file body.
// Returns an empty StagedRecords (not an error) if no staged rows exist —
// the assembler will produce a structurally valid but empty file in that case.
func (r *BatchRecordsRepo) ListStagedByCorrelationID(ctx context.Context, correlationID uuid.UUID) (*ports.StagedRecords, error) {
	result := &ports.StagedRecords{}

	// ── RT30 ──────────────────────────────────────────────────────────────────
	rows30, err := r.pool.Query(ctx, `
		SELECT id, sequence_in_file,
		       client_member_id, program_id, subprogram_id, package_id,
		       first_name, last_name, date_of_birth,
		       address_1, address_2, city, state, zip, email,
		       card_design_id, custom_card_id
		FROM public.batch_records_rt30
		WHERE correlation_id = $1
		  AND status = 'STAGED'
		ORDER BY sequence_in_file ASC`,
		correlationID,
	)
	if err != nil {
		return nil, fmt.Errorf("batch_records_rt30.ListStaged: %w", err)
	}
	defer rows30.Close()

	for rows30.Next() {
		rec := &ports.StagedRT30{}
		err := rows30.Scan(
			&rec.ID, &rec.SequenceInFile,
			&rec.ClientMemberID, &rec.ProgramID, &rec.SubprogramID, &rec.PackageID,
			&rec.FirstName, &rec.LastName, &rec.DateOfBirth,
			&rec.Address1, &rec.Address2, &rec.City, &rec.State, &rec.ZIP, &rec.Email,
			&rec.CardDesignID, &rec.CustomCardID,
		)
		if err != nil {
			return nil, fmt.Errorf("batch_records_rt30.ListStaged scan: %w", err)
		}
		result.RT30 = append(result.RT30, rec)
	}
	if err := rows30.Err(); err != nil {
		return nil, fmt.Errorf("batch_records_rt30.ListStaged rows: %w", err)
	}

	// ── RT37 ──────────────────────────────────────────────────────────────────
	rows37, err := r.pool.Query(ctx, `
		SELECT id, sequence_in_file,
		       client_member_id, fis_card_id, card_status_code, reason_code
		FROM public.batch_records_rt37
		WHERE correlation_id = $1
		  AND status = 'STAGED'
		ORDER BY sequence_in_file ASC`,
		correlationID,
	)
	if err != nil {
		return nil, fmt.Errorf("batch_records_rt37.ListStaged: %w", err)
	}
	defer rows37.Close()

	for rows37.Next() {
		rec := &ports.StagedRT37{}
		err := rows37.Scan(
			&rec.ID, &rec.SequenceInFile,
			&rec.ClientMemberID, &rec.FISCardID, &rec.CardStatusCode, &rec.ReasonCode,
		)
		if err != nil {
			return nil, fmt.Errorf("batch_records_rt37.ListStaged scan: %w", err)
		}
		result.RT37 = append(result.RT37, rec)
	}
	if err := rows37.Err(); err != nil {
		return nil, fmt.Errorf("batch_records_rt37.ListStaged rows: %w", err)
	}

	// ── RT60 ──────────────────────────────────────────────────────────────────
	rows60, err := r.pool.Query(ctx, `
		SELECT id, sequence_in_file,
		       at_code, client_member_id, fis_card_id,
		       purse_name, amount_cents, effective_date, client_reference
		FROM public.batch_records_rt60
		WHERE correlation_id = $1
		  AND status = 'STAGED'
		ORDER BY sequence_in_file ASC`,
		correlationID,
	)
	if err != nil {
		return nil, fmt.Errorf("batch_records_rt60.ListStaged: %w", err)
	}
	defer rows60.Close()

	for rows60.Next() {
		rec := &ports.StagedRT60{}
		err := rows60.Scan(
			&rec.ID, &rec.SequenceInFile,
			&rec.ATCode, &rec.ClientMemberID, &rec.FISCardID,
			&rec.PurseName, &rec.AmountCents, &rec.EffectiveDate, &rec.ClientReference,
		)
		if err != nil {
			return nil, fmt.Errorf("batch_records_rt60.ListStaged scan: %w", err)
		}
		result.RT60 = append(result.RT60, rec)
	}
	if err := rows60.Err(); err != nil {
		return nil, fmt.Errorf("batch_records_rt60.ListStaged rows: %w", err)
	}

	return result, nil
}

// GetStagedByCorrelationAndSequence looks up a single staged record by its
// (correlation_id, sequence_in_file) composite key. Used by Stage 7 to match
// return file records to their staged counterparts.
// recordType must be "RT30", "RT37", or "RT60".
// Returns (uuid.Nil, uuid.Nil, "", error) if no matching row is found.
//
// benefitPeriod is sourced from the domain_commands row via JOIN — it is the
// ISO YYYY-MM period under which the command was originally submitted. Stage 7
// uses it for the RT60 path to stamp the correct purse without relying on
// wall-clock time, which fails on cross-month batches and replay scenarios.
func (r *BatchRecordsRepo) GetStagedByCorrelationAndSequence(
	ctx context.Context,
	correlationID uuid.UUID,
	sequenceInFile int,
	recordType string,
) (recordID uuid.UUID, domainCommandID uuid.UUID, benefitPeriod string, err error) {
	var table string
	switch recordType {
	case "RT30":
		table = "batch_records_rt30"
	case "RT37":
		table = "batch_records_rt37"
	case "RT60":
		table = "batch_records_rt60"
	default:
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("batch_records.GetStaged: unknown record type %q", recordType)
	}

	// JOIN domain_commands to retrieve benefit_period alongside the record IDs.
	// The domain_commands row is guaranteed to exist: Stage 3 writes it before
	// the batch_records row (idempotency contract, §4.1.1).
	query := fmt.Sprintf(`
		SELECT br.id, br.domain_command_id, dc.benefit_period
		FROM public.%s br
		JOIN public.domain_commands dc ON dc.id = br.domain_command_id
		WHERE br.correlation_id = $1
		  AND br.sequence_in_file = $2
		LIMIT 1`, table)

	err = r.pool.QueryRow(ctx, query, correlationID, sequenceInFile).Scan(&recordID, &domainCommandID, &benefitPeriod)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("batch_records.GetStaged(%s seq=%d): %w", recordType, sequenceInFile, err)
	}
	return recordID, domainCommandID, benefitPeriod, nil
}
