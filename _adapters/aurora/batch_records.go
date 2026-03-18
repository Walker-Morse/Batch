package aurora

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
			client_member_id, subprogram_id, package_id,
			first_name, last_name, date_of_birth,
			address_1, address_2, city, state, zip, email,
			card_design_id, custom_card_id,
			raw_payload
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10, $11,
			$12, $13, $14,
			$15, $16, $17, $18, $19, $20,
			$21, $22,
			$23
		)`,
		rec.ID, rec.BatchFileID, rec.DomainCommandID, rec.CorrelationID,
		rec.TenantID, rec.SequenceInFile, rec.Status, rec.StagedAt,
		rec.ClientMemberID, rec.SubprogramID, rec.PackageID,
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
