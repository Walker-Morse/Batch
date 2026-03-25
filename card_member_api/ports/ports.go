// Package ports defines the domain-facing contracts for the Card and Member API.
// Callers (UW, IVR, batch pipeline, dead letter repair) speak One Fintech language only.
// No FIS identifiers, status codes, or purse numbers appear at this boundary.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ─── Sentinel errors ─────────────────────────────────────────────────────────

var (
	ErrMemberNotFound    = errors.New("member not found")
	ErrCardNotFound      = errors.New("card not found")
	ErrMemberNotResolved = errors.New("member FIS identifiers not yet assigned — batch return file pending")
	ErrAlreadyCancelled  = errors.New("card already cancelled")
	ErrDuplicateRequest  = errors.New("duplicate request — idempotency key already in progress")
	ErrInvalidRequest    = errors.New("invalid request")
)

// ─── Caller context ───────────────────────────────────────────────────────────

type CallerType string

const (
	CallerUniversalWallet  CallerType = "universal_wallet"
	CallerIVR              CallerType = "ivr"
	CallerBatchPipeline    CallerType = "batch_pipeline"
	CallerDeadLetterRepair CallerType = "dead_letter_repair"
	CallerOpsPortal        CallerType = "ops_portal"
)

// RequestContext is populated by JWT middleware and passed on every service call.
type RequestContext struct {
	CorrelationID   uuid.UUID  // idempotency key — same UUID reused on dead letter repair
	TenantID        string
	CallerType      CallerType
	SubjectMemberID *uuid.UUID // non-nil for UW member-authenticated requests
}

// ─── Card service ─────────────────────────────────────────────────────────────

type CancelReason string

const (
	CancelReasonLost          CancelReason = "LOST"
	CancelReasonStolen        CancelReason = "STOLEN"
	CancelReasonMemberRequest CancelReason = "MEMBER_REQUEST"
	CancelReasonProgramEnd    CancelReason = "PROGRAM_END"
	CancelReasonFraud         CancelReason = "FRAUD"
	CancelReasonOpsOverride   CancelReason = "OPS_OVERRIDE"
)

type CancelCardRequest struct {
	Rctx         RequestContext
	MemberID     *uuid.UUID // mutually exclusive with CardToken
	CardToken    *string    // IVR path — token from card swipe
	CancelReason CancelReason
}

type CancelCardResult struct {
	CardID       uuid.UUID
	MemberID     uuid.UUID
	CancelledAt  time.Time
	PursesClosed int
}

type ReplaceReason string

const (
	ReplaceReasonLost        ReplaceReason = "LOST"
	ReplaceReasonCompromised ReplaceReason = "COMPROMISED"
	ReplaceReasonRenewal     ReplaceReason = "RENEWAL"
	ReplaceReasonBINChange   ReplaceReason = "BIN_CHANGE"
)

type ReplaceCardRequest struct {
	Rctx     RequestContext
	MemberID uuid.UUID
	Reason   ReplaceReason
}

type ReplaceCardResult struct {
	OldCardID  uuid.UUID
	NewCardID  uuid.UUID
	MemberID   uuid.UUID
	ReplacedAt time.Time
}

type LoadFundsRequest struct {
	Rctx          RequestContext
	MemberID      uuid.UUID
	BenefitType   string // "OTC" or "FOD" — routes to correct FisPurseNumber
	AmountCents   int64
	BenefitPeriod string // ISO YYYY-MM — scopes idempotency key
}

type LoadFundsResult struct {
	CardID      uuid.UUID
	PurseID     uuid.UUID
	AmountCents int64
	LoadedAt    time.Time
}

type GetBalanceRequest struct {
	Rctx     RequestContext
	MemberID uuid.UUID
}

type PurseBalance struct {
	BenefitType    string // "OTC" or "FOD"
	PurseName      string
	AvailableCents int64
	SettledCents   int64
	Status         string
}

type GetBalanceResult struct {
	MemberID uuid.UUID
	CardID   uuid.UUID
	Purses   []PurseBalance
	AsOf     time.Time
}

type ResolveCardRequest struct {
	Rctx  RequestContext
	Token string // card number token or proxy
}

type ResolveCardResult struct {
	MemberID uuid.UUID
	CardID   uuid.UUID
	Status   string
}

// ICardService is the domain service for card lifecycle operations.
// All methods are idempotent within the same CorrelationID.
type ICardService interface {
	CancelCard(ctx context.Context, req CancelCardRequest) (*CancelCardResult, error)
	ReplaceCard(ctx context.Context, req ReplaceCardRequest) (*ReplaceCardResult, error)
	LoadFunds(ctx context.Context, req LoadFundsRequest) (*LoadFundsResult, error)
	GetBalance(ctx context.Context, req GetBalanceRequest) (*GetBalanceResult, error)
	ResolveCard(ctx context.Context, req ResolveCardRequest) (*ResolveCardResult, error)
}

// ─── Member service ───────────────────────────────────────────────────────────

type EnrollMemberRequest struct {
	Rctx           RequestContext
	ClientMemberID string // PHI
	SubprogramID   int64
	BenefitPeriod  string // ISO YYYY-MM
	FirstName      string // PHI
	LastName       string // PHI
	DOB            time.Time
	Address1       string // PHI
	Address2       string // PHI
	City           string // PHI
	State          string // PHI
	ZIP            string // PHI
	Phone          string // PHI
	Email          string // PHI
	CardDesignID   string
}

type EnrollMemberResult struct {
	MemberID    uuid.UUID
	CardID      uuid.UUID
	FISResolved bool // true when personId + cardId assigned synchronously
	CreatedAt   time.Time
}

type UpdateMemberRequest struct {
	Rctx      RequestContext
	MemberID  uuid.UUID
	FirstName string // PHI
	LastName  string // PHI
	DOB       time.Time
	Address1  string // PHI
	Address2  string // PHI
	City      string // PHI
	State     string // PHI
	ZIP       string // PHI
	Phone     string // PHI
	Email     string // PHI
}

type MemberView struct {
	MemberID       uuid.UUID
	ClientMemberID string // PHI
	Status         string
	FISResolved    bool
	CardID         *uuid.UUID
	CardStatus     *string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// IMemberService is the domain service for member lifecycle operations.
type IMemberService interface {
	EnrollMember(ctx context.Context, req EnrollMemberRequest) (*EnrollMemberResult, error)
	UpdateMember(ctx context.Context, req UpdateMemberRequest) error
	GetMember(ctx context.Context, rctx RequestContext, memberID uuid.UUID) (*MemberView, error)
}
