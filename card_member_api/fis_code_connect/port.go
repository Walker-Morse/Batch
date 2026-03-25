// Package fis_code_connect defines the outbound port contract for the FIS Code Connect
// REST API (PrePaidSouthCardServices v1.3.0).
//
// This is the adapter seam between One Fintech domain logic and FIS.
// Callers speak in One Fintech domain terms (MemberID, CardID). This package
// translates to and from FIS-assigned identifiers (FisPersonID, FisCardID).
//
// All 18 FIS endpoints surface through this interface. The concrete adapter
// (FisCodeConnectAdapter) owns:
//   - OAuth2 token acquisition and cache (implicit flow, token TTL ~55 min)
//   - encode-response: false on all GET requests (prevents HTML entity corruption)
//   - showSecureData: false hardcoded (PCI — never expose PAN/SSN)
//   - organization-id header (conditional — confirm with John Stevens / Kendra Williams)
//   - Retry with exponential backoff + circuit breaker
//   - Structured error mapping FIS HTTP status → typed sentinel errors
//
// Key invariants enforced at this boundary:
//   - FisPersonID must exist before IssueCard is called (type system enforces this)
//   - FisCardID is FIS-generated (18-digit, pattern ^[1-9][0-9]{17}$) — store immediately
//   - Multi-purse: purseNumber is required on LoadFunds; omitting routes to wrong purse
//   - 30-day transaction window: GetCardTransactions enforces before making HTTP call
package fis_code_connect

import (
	"context"
	"errors"
	"time"
)

// ─── FIS-assigned identifier types ───────────────────────────────────────────
//
// These are distinct named types so the compiler prevents passing a cardId
// where a personId is expected and vice versa. They are never stored as plain
// strings in domain structs — always as *FisPersonID / *FisCardID to encode
// the nil-until-resolved lifecycle.

// FisPersonID is the FIS-assigned person identifier returned by POST /persons.
// 18 digits, pattern ^[1-9][0-9]{17}$. Nil in domain until POST /persons succeeds.
type FisPersonID string

// FisCardID is the FIS-assigned card identifier returned by POST /cards.
// 18 digits, pattern ^[1-9][0-9]{17}$. Nil in domain until POST /cards succeeds.
// Replaced on every reissue/replace — must update Aurora atomically.
type FisCardID string

// FisTransactionID is the FIS-assigned transaction identifier.
type FisTransactionID string

// FisPurseNumber is the FIS-assigned purse slot (1-based, program-defined).
// For RFU: purse 1 = F&V (OTC), purse 2 = Pantry (FOD).
// Required on LoadFunds — omitting causes wrong-purse routing or rejection.
type FisPurseNumber int16

// ─── Sentinel errors ─────────────────────────────────────────────────────────

var (
	// ErrFisNotFound is returned when FIS responds 404.
	ErrFisNotFound = errors.New("fis: resource not found")

	// ErrFisDuplicate is returned when FIS rejects due to duplicate clientReferenceNumber.
	// Note: FIS clientReferenceNumber does NOT guarantee idempotency under race conditions.
	// One Fintech's 3-layer idempotency (domain_commands table) is the authoritative gate.
	ErrFisDuplicate = errors.New("fis: duplicate submission")

	// ErrFisUnauthorized is returned on 401 — token expired mid-request.
	// Adapter retries once with a fresh token before surfacing this error.
	ErrFisUnauthorized = errors.New("fis: unauthorized — token refresh failed")

	// ErrFisCardNotActive is returned when a card operation is attempted on a
	// non-active card (e.g. loading funds to a closed card).
	ErrFisCardNotActive = errors.New("fis: card not in active state")

	// ErrTransactionWindowExceeded is returned by GetCardTransactions when the
	// requested date range exceeds 30 days. Validated before the HTTP call is made.
	ErrTransactionWindowExceeded = errors.New("fis: transaction date range exceeds 30-day maximum")
)

// ─── Request / response types ─────────────────────────────────────────────────

// CreatePersonRequest maps to POST /persons.
// subprogramId and packageId are not known until Selvi uploads ACC —
// sourced from programs table, never hardcoded.
type CreatePersonRequest struct {
	ClientMemberID string // One Fintech natural key — sent as clientReferenceNumber
	SubprogramID   int64
	PackageID      string
	FirstName      string // PHI
	LastName       string // PHI
	DOB            string // PHI — "MMDDYYYY" per FIS spec
	Address1       string // PHI
	Address2       string // PHI — empty string when absent
	City           string // PHI
	State          string // PHI — 2-char
	ZIP            string // PHI
	Phone          string // PHI — digits only, 10 chars
	Email          string // PHI — empty string when absent
}

// FisPerson is the response from GET /persons/{personId}.
type FisPerson struct {
	PersonID  FisPersonID
	FirstName string // PHI
	LastName  string // PHI
	Status    string
}

// UpdatePersonRequest maps to PUT /persons/{personId}.
// Only populated fields are sent — zero values are omitted.
type UpdatePersonRequest struct {
	FirstName string // PHI
	LastName  string // PHI
	DOB       string // PHI
	Address1  string // PHI
	Address2  string // PHI
	City      string // PHI
	State     string // PHI
	ZIP       string // PHI
	Phone     string // PHI
	Email     string // PHI
}

// IssueCardRequest maps to POST /cards.
// PersonID is a required typed argument on IssueCard — not part of this struct —
// so the compiler enforces person-before-card sequencing.
type IssueCardRequest struct {
	ClientMemberID string
	SubprogramID   int64
	PackageID      string
	CardDesignID   string // empty string when not specified
	CustomCardID   string // empty string when not specified
}

// FisCard is the response from GET /cards/{cardId}.
type FisCard struct {
	CardID    FisCardID
	PersonID  FisPersonID
	Status    int16
	PANMasked string // first 6 + last 4 — showSecureData always false
}

// LoadFundsRequest maps to POST /cards/{cardId}/loads.
// PurseNumber is required for RFU (multi-purse program).
// Omitting it on a multi-purse account causes FIS rejection.
type LoadFundsRequest struct {
	PurseNumber     FisPurseNumber
	AmountCents     int64
	EffectiveDate   string // "MMDDYYYY"
	ClientReference string // One Fintech correlation ID — for traceability, not idempotency
}

// ReissueRequest maps to POST /cards/{cardId}/reissue.
type ReissueRequest struct {
	ReasonCode   string // FIS reason code
	ClientMemberID string
}

// ReplaceRequest maps to POST /cards/{cardId}/replace.
// Returns a new FisCardID — Aurora must be updated atomically.
type ReplaceRequest struct {
	ReasonCode     string // L=lost, C=compromised, R=renewal, B=BIN substitution
	ClientMemberID string
}

// FisPurse is one purse slot from GET /accounts/{cardId}/purses.
type FisPurse struct {
	PurseNumber     FisPurseNumber
	PurseName       string // e.g. "OTC2550", "FOD2550"
	Status          string
	AvailableBalance int64 // cents
	LedgerBalance    int64 // cents
}

// PurseStatusCode is the value sent to POST /accounts/{cardId}/purses/{n}/status.
type PurseStatusCode string

const (
	PurseStatusActive    PurseStatusCode = "ACTIVE"
	PurseStatusSuspended PurseStatusCode = "SUSPENDED"
	PurseStatusClosed    PurseStatusCode = "CLOSED"
)

// TransactionRangeRequest is the query window for transaction history.
// The adapter validates From..To ≤ 30 days before making the HTTP call.
type TransactionRangeRequest struct {
	From time.Time
	To   time.Time
}

// FisTransaction is one row from GET /cards/{cardId}/transactions or
// GET /transactions/{transactionId}.
type FisTransaction struct {
	TransactionID FisTransactionID
	CardID        FisCardID
	AmountCents   int64
	PostedAt      time.Time
	MerchantName  string
	Status        string
}

// ─── Outbound port ────────────────────────────────────────────────────────────

// IFisCodeConnectPort is the outbound adapter seam for all FIS Code Connect
// real-time operations. Callers never import net/http or FIS JSON structs.
//
// Compile-time assertion in the concrete adapter:
//   var _ IFisCodeConnectPort = (*FisCodeConnectAdapter)(nil)
//
// The adapter is constructed once per service instance. Auth state (OAuth2 token,
// expiry) is encapsulated inside the adapter — not visible to callers.
type IFisCodeConnectPort interface {

	// ── Person ───────────────────────────────────────────────────────────────

	// CreatePerson calls POST /persons.
	// Returns the FIS-assigned personId. Store in consumers.fis_person_id immediately.
	CreatePerson(ctx context.Context, req CreatePersonRequest) (FisPersonID, error)

	// GetPerson calls GET /persons/{personId}.
	GetPerson(ctx context.Context, id FisPersonID) (*FisPerson, error)

	// UpdatePerson calls PUT /persons/{personId}.
	UpdatePerson(ctx context.Context, id FisPersonID, req UpdatePersonRequest) error

	// ── Card ─────────────────────────────────────────────────────────────────

	// IssueCard calls POST /cards.
	// personID is a typed argument — not part of IssueCardRequest — so the compiler
	// prevents calling IssueCard without a resolved FisPersonID.
	// Returns the FIS-assigned cardId. Store in cards.fis_card_id immediately.
	IssueCard(ctx context.Context, personID FisPersonID, req IssueCardRequest) (FisCardID, error)

	// GetCard calls GET /cards/{cardId}.
	GetCard(ctx context.Context, id FisCardID) (*FisCard, error)

	// CloseCard calls POST /cards/{cardId}/close.
	// Used by CancelCard domain operation. Does not set purse status —
	// SetPurseStatus must be called separately per purse.
	CloseCard(ctx context.Context, id FisCardID) error

	// LoadFunds calls POST /cards/{cardId}/loads.
	// purseNumber is a typed argument — required for multi-purse programs.
	LoadFunds(ctx context.Context, id FisCardID, req LoadFundsRequest) error

	// ReissueCard calls POST /cards/{cardId}/reissue.
	// Returns same cardId (reissue preserves the card number).
	ReissueCard(ctx context.Context, id FisCardID, req ReissueRequest) error

	// ReplaceCard calls POST /cards/{cardId}/replace.
	// Returns a NEW FisCardID — the old cardId is invalidated.
	// Caller must update cards.fis_card_id in Aurora atomically after this call.
	ReplaceCard(ctx context.Context, id FisCardID, req ReplaceRequest) (FisCardID, error)

	// ── Account / Purse ──────────────────────────────────────────────────────

	// GetPurses calls GET /accounts/{cardId}/purses.
	// Returns all purse slots. For RFU: always 2 purses.
	GetPurses(ctx context.Context, cardID FisCardID) ([]FisPurse, error)

	// GetPurse calls GET /accounts/{cardId}/purses/{purseNumber}.
	GetPurse(ctx context.Context, cardID FisCardID, purseNumber FisPurseNumber) (*FisPurse, error)

	// SetPurseStatus calls POST /accounts/{cardId}/purses/{purseNumber}/status.
	// Called as part of card cancel — close each purse explicitly.
	SetPurseStatus(ctx context.Context, cardID FisCardID, purseNumber FisPurseNumber, status PurseStatusCode) error

	// ── Transactions ─────────────────────────────────────────────────────────

	// GetCardTransactions calls GET /cards/{cardId}/transactions.
	// Returns ErrTransactionWindowExceeded if req.To - req.From > 30 days.
	// For history beyond 30 days use FIS XTRACT feeds (DM-03 — Kendra Williams).
	GetCardTransactions(ctx context.Context, cardID FisCardID, req TransactionRangeRequest) ([]FisTransaction, error)

	// GetTransaction calls GET /transactions/{transactionId}.
	GetTransaction(ctx context.Context, id FisTransactionID) (*FisTransaction, error)

	// ── Translate ────────────────────────────────────────────────────────────

	// TranslateCardNumber calls POST /translate-cardnumber.
	// Resolves a physical card number / token to a FisCardID.
	// Used by IVR cancel flow — IVR presents the token from the card swipe.
	TranslateCardNumber(ctx context.Context, token string) (FisCardID, error)

	// TranslateProxy calls POST /translate-proxy.
	// Resolves a proxy number to a FisCardID.
	TranslateProxy(ctx context.Context, proxy string) (FisCardID, error)
}
