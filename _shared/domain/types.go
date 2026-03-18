// Package domain contains the core domain types shared across all business capabilities.
// These types are pure Go — no infrastructure dependencies. They represent the
// business concepts of the One Fintech platform: members, cards, purses, programs.
//
// PHI classification per §5.4.1 is annotated on every field that carries
// Personally Identifiable Information or Protected Health Information.
// Tables containing PHI: consumers, cards, purses, batch_records_rt30/rt37/rt60,
// dim_member, dim_card, dim_purse, audit_log, dead_letter_store.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// ─── Member / Consumer ───────────────────────────────────────────────────────

// ConsumerStatus is the lifecycle state of an enrolled member.
type ConsumerStatus string

const (
	ConsumerActive      ConsumerStatus = "ACTIVE"
	ConsumerSuspended   ConsumerStatus = "SUSPENDED"   // OFAC hit or operational hold
	ConsumerTerminated  ConsumerStatus = "TERMINATED"  // SRG315 termination event
	ConsumerTransferred ConsumerStatus = "TRANSFERRED" // wallet transfer
)

// Consumer is the DDD aggregate root for a health plan member (§4.2.1).
// FISPersonID and FISCUID are nil until Stage 7 (Reconciliation) confirms the RT30 return.
// All name, address, and DOB fields are PHI under HIPAA 45 CFR § 164.312.
type Consumer struct {
	ID             uuid.UUID
	TenantID       string
	ClientMemberID string // PHI — health plan member ID; natural key

	Status ConsumerStatus

	// FIS-assigned — nil until Stage 7 RT30 reconciliation
	FISPersonID *string // VARCHAR(20)
	FISCUID     *string // VARCHAR(19)

	// Demographics — PHI
	FirstName string    // PHI
	LastName  string    // PHI
	DOB       time.Time // PHI
	Address1  string    // PHI
	Address2  *string   // PHI
	City      string    // PHI
	State     string    // PHI — 2-char
	ZIP       string    // PHI
	Email     *string   // PHI

	// Program linkage
	ProgramID    uuid.UUID
	SubprogramID int64 // FIS SubprogramId e.g. 26071 for RFU

	ContractPBP  *string // Plan Benefit Package (SRG310)
	CustomCardID *string

	SourceBatchFileID uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// IsResolved returns true when FIS has confirmed the RT30 and assigned identifiers.
func (c *Consumer) IsResolved() bool {
	return c.FISPersonID != nil && c.FISCUID != nil
}

// ─── Card ────────────────────────────────────────────────────────────────────

// CardStatus mirrors FIS card status codes (§4.2.2).
type CardStatus int16

const (
	CardReady     CardStatus = 1
	CardActive    CardStatus = 2
	CardLost      CardStatus = 4
	CardSuspended CardStatus = 6
	CardClosed    CardStatus = 7
)

// Card is an owned entity within the Consumer aggregate (§4.2.2).
// Full PAN is NEVER stored. FISCardID is nil until Stage 7 and requires FIS opt-in
// (action owner: Selvi Marappan — fis_card_id is the preferred XTRACT join key).
type Card struct {
	ID             uuid.UUID
	TenantID       string
	ConsumerID     uuid.UUID
	ClientMemberID string // PHI — denormalised for query convenience

	// FIS-assigned — nil until Stage 7
	// OPT-IN REQUIRED: fis_card_id (Selvi Marappan). Primary XTRACT join key.
	FISCardID      *string // VARCHAR(19) — nil until opt-in enabled
	PANMasked      *string // first 6 + last 4 ONLY — full PAN NEVER stored
	FISProxyNumber *string // VARCHAR(30)

	Status      CardStatus
	CardDesignID *string
	PackageID   *string

	IssuedAt    *time.Time // set when RT30 return confirms issuance (Stage 7)
	ActivatedAt *time.Time
	ExpiredAt   *time.Time
	ClosedAt    *time.Time

	SourceBatchFileID uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ─── Purse ───────────────────────────────────────────────────────────────────

// PurseStatus is the lifecycle state of a benefit purse.
type PurseStatus string

const (
	PursePending     PurseStatus = "PENDING"
	PurseActive      PurseStatus = "ACTIVE"
	PurseExpired     PurseStatus = "EXPIRED"
	PurseTransferred PurseStatus = "TRANSFERRED"
	PurseClosed      PurseStatus = "CLOSED"
)

// BenefitType identifies the benefit programme associated with a purse.
// Routes APL enforcement to the correct APL version at point-of-sale (§3.1).
type BenefitType string

const (
	BenefitOTC BenefitType = "OTC" // Fruits & Vegetables (Purse 1 for RFU)
	BenefitFOD BenefitType = "FOD" // Pantry Foods (Purse 2 for RFU)
	BenefitCMB BenefitType = "CMB" // Combined
)

// Purse is an owned entity within the Consumer aggregate (§4.2.3).
// ExpiryDate encodes the contractual 11:59 PM ET month-end deadline (SOW §2.1, §3.3).
// The AT30 period-end sweep MUST complete before ExpiryDate — this is a hard
// contractual requirement, not an operational guideline.
//
// AvailableBalanceCents is the One Fintech sub-ledger view (§I.2).
// It must be reconciled against the MVB FBO account balance daily (Addendum I).
type Purse struct {
	ID             uuid.UUID
	TenantID       string
	CardID         uuid.UUID
	ConsumerID     uuid.UUID // denormalised
	ClientMemberID string

	// FIS-assigned — nil until Stage 7 RT60 return
	FISPurseNumber *int16
	FISPurseName   string // ACC PurseName e.g. OTC2550, FOD2550

	// PurseType is LEFT(FISPurseName, 3) — matches the DB GENERATED column
	PurseType string // e.g. "OTC", "FOD"

	Status                PurseStatus
	AvailableBalanceCents int64 // One Fintech sub-ledger; reconcile vs XTRACT DM-03 + FBO

	BenefitPeriod string      // ISO YYYY-MM
	EffectiveDate time.Time   // benefit period start
	ExpiryDate    time.Time   // contractual 11:59 PM ET last day of month (SOW §2.1)

	ProgramID   uuid.UUID
	BenefitType BenefitType

	ActivatedAt *time.Time
	ExpiredAt   *time.Time
	ClosedAt    *time.Time

	SourceBatchFileID uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// DeriveType returns the 3-char purse type from the FIS purse name.
// Mirrors the PostgreSQL GENERATED column: LEFT(fis_purse_name, 3).
func DeriveType(purseName string) string {
	if len(purseName) >= 3 {
		return purseName[:3]
	}
	return purseName
}

// ─── Pipeline State ──────────────────────────────────────────────────────────

// BatchFileStatus is the lifecycle of a batch file through the 7 pipeline stages (§4.1).
type BatchFileStatus string

const (
	BatchFileReceived    BatchFileStatus = "RECEIVED"
	BatchFileValidating  BatchFileStatus = "VALIDATING"
	BatchFileProcessing  BatchFileStatus = "PROCESSING"
	BatchFileStalled     BatchFileStatus = "STALLED"    // unresolved dead letters
	BatchFileHalted      BatchFileStatus = "HALTED"     // RT99 full-file rejection
	BatchFileAssembled   BatchFileStatus = "ASSEMBLED"
	BatchFileTransferred BatchFileStatus = "TRANSFERRED"
	BatchFileComplete    BatchFileStatus = "COMPLETE"
)

// FileType identifies the SRG inbound file type.
type FileType string

const (
	FileTypeSRG310 FileType = "SRG310" // member enrollment and demographic update
	FileTypeSRG315 FileType = "SRG315" // purse lifecycle events
	FileTypeSRG320 FileType = "SRG320" // fund management (recurring loads)
	FileTypeReturn FileType = "RETURN" // FIS return file
)

// CommandType maps domain operations to FIS record types (§4.1.1).
type CommandType string

const (
	CommandEnroll    CommandType = "ENROLL"    // → RT30
	CommandUpdate    CommandType = "UPDATE"    // → RT37
	CommandLoad      CommandType = "LOAD"      // → RT60 AT01
	CommandSweep     CommandType = "SWEEP"     // → RT60 AT30 (period-end expiry)
	CommandSuspend   CommandType = "SUSPEND"   // → RT37 status 6
	CommandTerminate CommandType = "TERMINATE" // → RT37 status 7
)

// CommandStatus is the lifecycle state of a domain command.
// A row with Accepted status is the idempotency gate (§4.1.1):
// any subsequent attempt with the same composite key returns Duplicate
// without touching FIS or domain state.
type CommandStatus string

const (
	CommandAccepted  CommandStatus = "Accepted"
	CommandCompleted CommandStatus = "Completed"
	CommandFailed    CommandStatus = "Failed"
	CommandDuplicate CommandStatus = "Duplicate"
)

// FailureStage identifies which pipeline stage produced a dead letter entry (§6.5).
type FailureStage string

const (
	FailureValidation     FailureStage = "validation"
	FailureRowProcessing  FailureStage = "row_processing"
	FailureBatchAssembly  FailureStage = "batch_assembly"
	FailureFISTransfer    FailureStage = "fis_transfer"
	FailureReturnFileWait FailureStage = "return_file_wait"
	FailureReconciliation FailureStage = "reconciliation"
)
