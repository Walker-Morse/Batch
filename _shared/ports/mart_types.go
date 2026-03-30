package ports

import (
	"time"

	"github.com/google/uuid"
)

// ─── MartWriter record types ──────────────────────────────────────────────────
// These structs carry the data Stage 3 and Stage 7 need to populate the
// reporting schema. Fields map directly to reporting.dim_* and reporting.fact_*
// columns as documented in §4.3.

// MemberRecord drives reporting.dim_member upsert.
// Populated from the domain.Consumer written by Stage 3.
// Natural key: (plan_member_id, tenant_id).
type MemberRecord struct {
	// Source IDs
	ConsumerID    uuid.UUID // public.consumers.id — used as member_id
	TenantID      string
	PlanMemberID  string    // = ClientMemberID from SRG310
	// Demographics (PHI — never log)
	FirstName     string
	LastName      string
	DOB           time.Time
	Address1      string
	Address2      string
	City          string
	State         string
	ZIP           string
	Email         string
	// Program context
	SubprogramID  int64
	ContractPBP   string
	CustomCardID  string
	// SCD-2 scaffolding
	EffectiveDate time.Time
}

// CardRecord drives reporting.dim_card upsert.
// Populated from the domain.Card written by Stage 3.
// Natural key: fis_proxy_number (until fis_card_id opt-in is enabled).
type CardRecord struct {
	CardID       uuid.UUID // public.cards.id
	MemberSK     int64     // reporting.dim_member.member_sk (returned by UpsertMember)
	TenantID     string
	FISCardID    string    // NULL until Stage 7 stamps it; join falls back to proxy
	ProxyNumber  string
	PANMasked    string
	CardStatus   int16     // 1=Ready 2=Active 4=Lost 6=Suspended 7=Closed
	PackageID    string
	BIN          string
	IssueDate    time.Time
	// SCD-2 scaffolding
	EffectiveDate time.Time
}

// PurseRecord drives reporting.dim_purse upsert.
// Populated from public.purses via Stage 3 domain state write.
// Grain: one row per card-purse-benefit-period.
type PurseRecord struct {
	PurseID         uuid.UUID // public.purses.id
	CardSK          int64     // reporting.dim_card.card_sk (returned by UpsertCard)
	TenantID        string
	FISPurseNumber  int16
	FISPurseName    string    // e.g. OTC2550, FOD2550 — first 3 chars = purse_type
	PurseCode       string    // nullable — DM-01 blocker (health-plan namespace)
	PurseStatus     string    // ACTIVE | SUSPENDED | EXPIRED | CLOSED
	EffectiveDate   time.Time // benefit period start
	ExpirationDate  time.Time // benefit period end
	MaxValue        int64     // cents
	MaxLoad         int64     // cents
	AutoloadAmount  int64     // cents; 0 if not configured
}

// EnrollmentFact drives reporting.fact_enrollments insert.
// Idempotency key: (correlation_id, row_sequence_number).
type EnrollmentFact struct {
	MemberSK          int64
	ProgramSK         int64
	DateSK            int       // YYYYMMDD
	TenantID          string
	CorrelationID     uuid.UUID
	RowSequenceNumber int
	SRGFileType       string    // SRG310 | SRG315 | SRG320
	EventType         string    // NEW_ENROLLMENT | CHANGE | TERMINATION | RELOAD
	FISRecordType     string    // RT30 | RT37 | RT60
	ProcessingStatus  string    // STAGED | ACCEPTED | REJECTED | FAILED
}

// PurseLifecycleFact drives reporting.fact_purse_lifecycle insert.
// Idempotency key: (correlation_id, row_sequence_number, event_type).
type PurseLifecycleFact struct {
	PurseSK           int64
	MemberSK          int64
	DateSK            int
	TenantID          string
	CorrelationID     uuid.UUID
	RowSequenceNumber int
	EventType         string    // LOAD | RELOAD | EXPIRE | TERMINATE | SUSPEND | NEW_PERIOD
	AmountCents       *int64    // nil for non-monetary events
	BalanceBeforeCents *int64
	BalanceAfterCents  *int64
}

// ReconciliationFact drives reporting.fact_reconciliation insert.
// Idempotency key: (batch_file_id, row_sequence_number).
type ReconciliationFact struct {
	MemberSK             int64
	DateSK               int
	TenantID             string
	CorrelationID        uuid.UUID
	BatchFileID          uuid.UUID
	RowSequenceNumber    int
	FISResultCode        string
	FISResultMessage     string
	ReconciliationStatus string // MATCHED | UNMATCHED | ERROR
}
