// Package fis_code_connect defines the outbound port contract for the FIS Code Connect
// REST API (PrePaidSouthCardServices v1.4.0).
//
// This is the adapter seam between One Fintech domain logic and FIS.
// Callers speak in One Fintech domain terms (MemberID, CardID). This package
// translates to and from FIS-assigned identifiers (FisPersonID int32, FisCardID string).
//
// The concrete adapter (FisCodeConnectAdapter) owns:
//
//   - pps-authorization header: Base64({"username":"...","password":"...","source":0})
//     sourced from Secrets Manager — never hardcoded.
//   - uuid header: caller-supplied per-request UUID (One Fintech correlation ID).
//   - encode-response: false on ALL requests — prevents HTML entity corruption of strings.
//   - showSecureData: false hardcoded — PCI; never expose PAN/SSN.
//   - organization-id header: conditional — include only if FIS directed Morse to send one.
//     Confirm with John Stevens / Kendra Williams. Including when not required = failure.
//   - Retry with exponential backoff + circuit breaker.
//   - Structured error mapping FIS HTTP status → typed sentinel errors.
//
// ── Spec corrections vs prior version (v1.3.0 → v1.4.0) ─────────────────────
//
//  1. FisPersonID is int32, NOT a string. FIS spec: PersonResponse.personId int32.
//     Our prior type was `type FisPersonID string` — wrong. Fixed to int32.
//
//  2. POST /persons and POST /cards both return 201 NO BODY.
//     The new personId / cardId must be retrieved via a subsequent GET call.
//     Adapter sequence for CreatePerson: POST /persons → GET /persons/{personId}
//     ... but personId is not in the 201 response. See FisPersonIDFromHeader below.
//
//  3. POST /cards/replace returns 201 NO BODY — new cardId is NOT in the response.
//     The new cardId must be retrieved by calling GET /cards/{oldCardId} after
//     replace and reading the replacement linkage, or via GET on the new proxy.
//     Open question for John Stevens: how does FIS surface the new cardId?
//
//  4. Two endpoints we missed from v1.3.0 scan:
//     - POST /cards/{cardId}/activate  (Ready → Active)
//     - POST /cards/{cardId}/change-status  (status transitions: Lost, Suspended, Fraud, PFraud)
//     Both are needed for full card lifecycle ownership.
//
//  5. POST /cards/{cardId}/close, /reissue, /activate, /change-status,
//     POST /accounts/{cardId}/purses/{purseNumber}/status all return 204 NO BODY on success.
//
//  6. availableBalance and settledBalance on PurseResponse are decimal strings
//     (pattern ^[-]?\d{0,15}(\.\d{0,4})?$), not integers. Adapter must parse to cents.
//
//  7. Transaction date filtering uses startInserted / endInserted (date strings),
//     not a from/to range. No 30-day limit enforced server-side per spec — but
//     FIS documentation (separate from OpenAPI) states 30-day ceiling. Enforce client-side.
package fis_code_connect

import (
	"errors"
	"time"
)

// ─── FIS-assigned identifier types ───────────────────────────────────────────

// FisPersonID is the FIS-assigned person identifier.
// int32, range 1–2147483647. Returned by POST /persons (via Location header or
// subsequent GET — see FisPersonIDFromHeader). Stored as consumers.fis_person_id VARCHAR(20).
//
// CORRECTION from v1.3.0: this was typed as string — the spec defines it as int32.
type FisPersonID int32

// FisCardID is the FIS-assigned card identifier.
// 18-char string, pattern ^[1-9][0-9]{17}$. Returned by POST /cards.
// Replaced on /replace — new cardId must be discovered via GET after replace.
type FisCardID string

// FisTransactionID is the FIS-assigned transaction identifier (UUID format).
type FisTransactionID string

// FisPurseNumber is the FIS-assigned purse slot (int32, 1-based).
// For RFU: purse 1 = F&V (OTC), purse 2 = Pantry (FOD).
// Required on LoadFunds for multi-purse programs — omitting causes wrong-purse routing.
type FisPurseNumber int32

// ─── Sentinel errors ─────────────────────────────────────────────────────────

var (
	ErrFisNotFound               = errors.New("fis: resource not found")
	ErrFisDuplicate              = errors.New("fis: duplicate clientReferenceNumber")
	ErrFisUnauthorized           = errors.New("fis: unauthorized — pps-authorization invalid or token expired")
	ErrFisCardNotActive          = errors.New("fis: card not in active state")
	ErrFisCardTerminal           = errors.New("fis: card in terminal status — operation not permitted")
	ErrFisCardHold               = errors.New("fis: card in hold status (Suspended/PFraud) — some operations blocked")
	ErrFisTooManyRequests        = errors.New("fis: rate limit exceeded (429)")
	ErrTransactionWindowExceeded = errors.New("fis: transaction date range exceeds 30-day maximum")
	// ErrReplaceCardIDUnresolvable is returned when the new cardId cannot be
	// determined after POST /replace (201 no body). Caller must retry GET /cards/{oldId}
	// to discover replacement linkage. Open item — confirm with John Stevens.
	ErrReplaceCardIDUnresolvable = errors.New("fis: new cardId after replace not yet resolvable — GET /cards/{oldId} required")
)

// ─── Card status values (from CardResponse.status enum) ──────────────────────

type FisCardStatus string

const (
	FisCardDormant   FisCardStatus = "Dormant"
	FisCardReady     FisCardStatus = "Ready"
	FisCardActive    FisCardStatus = "Active"
	FisCardClosed    FisCardStatus = "Closed"
	FisCardLost      FisCardStatus = "Lost"
	FisCardReplaced  FisCardStatus = "Replaced"
	FisCardSuspended FisCardStatus = "Suspended"
	FisCardFraud     FisCardStatus = "Fraud"
	FisCardPFraud    FisCardStatus = "PFraud"
	FisCardWarning   FisCardStatus = "Warning"
	FisCardDestroyed FisCardStatus = "Destroyed"
)

// FisCardStatusGroup classifies card statuses per FIS documentation.
// Used to gate operations before calling FIS.
type FisCardStatusGroup string

const (
	// FisStatusNonTerminal: Dormant, Ready, Active — card can be used or transitioned.
	FisStatusNonTerminal FisCardStatusGroup = "non_terminal"
	// FisStatusHold: Suspended, PFraud — card cannot be used; hold can be lifted.
	FisStatusHold FisCardStatusGroup = "hold"
	// FisStatusTerminal: Closed, Lost, Replaced, Fraud, Destroyed — card is permanently done.
	FisStatusTerminal FisCardStatusGroup = "terminal"
)

// CardStatusGroup returns the lifecycle group for a given FIS card status.
func CardStatusGroup(s FisCardStatus) FisCardStatusGroup {
	switch s {
	case FisCardClosed, FisCardLost, FisCardReplaced, FisCardFraud, FisCardDestroyed:
		return FisStatusTerminal
	case FisCardSuspended, FisCardPFraud:
		return FisStatusHold
	default:
		return FisStatusNonTerminal
	}
}

// ─── ChangeCardStatus valid target statuses ───────────────────────────────────

// ChangeCardStatusTarget is the set of statuses reachable via POST /cards/{cardId}/change-status.
// NOTE: Closed and Fraud (terminal) are NOT in this set — use /close for Closed.
type ChangeCardStatusTarget string

const (
	ChangeToReady     ChangeCardStatusTarget = "Ready"     // only from Dormant
	ChangeToLost      ChangeCardStatusTarget = "Lost"      // from any non-terminal, non-hold
	ChangeToSuspended ChangeCardStatusTarget = "Suspended" // from any non-terminal, non-hold
	ChangeToFraud     ChangeCardStatusTarget = "Fraud"     // from any non-terminal, non-hold
	ChangeToPFraud    ChangeCardStatusTarget = "PFraud"    // from any non-terminal, non-hold
)

// ─── Purse status values ──────────────────────────────────────────────────────

type PurseStatusCode string

const (
	PurseStatusActive    PurseStatusCode = "Active"
	PurseStatusClosed    PurseStatusCode = "Closed"
	PurseStatusSuspended PurseStatusCode = "Suspended"
)

// ─── Request types ────────────────────────────────────────────────────────────

// CreatePersonRequest maps to POST /persons (PersonRequest schema).
// clientId is the FIS client ID — sourced from program config, not hardcoded.
// addresses.mailing.countryAlphaCode is required by FIS.
type CreatePersonRequest struct {
	ClientID       int32  // FIS client ID from program config
	ClientUniqueID string // One Fintech clientMemberID — used as the FIS clientUniqueId
	FirstName      string // PHI
	LastName       string // PHI
	DOB            string // PHI — "2006-01-02" (date format)
	MailingLine1   string // PHI
	MailingLine2   string // PHI — empty string when absent
	MailingCity    string // PHI
	MailingState   string // PHI — 2-char
	MailingZIP     string // PHI
	// CountryAlphaCode is required by FIS spec. "USA" for all RFU members.
	CountryAlphaCode string
	Phone            string // PHI — home phone number, digits only
	Email            string // PHI — empty string when absent
}

// UpdatePersonRequest maps to PUT /persons/{personId} (PersonUpdateRequest schema).
// clientId is required. Only non-empty fields are sent.
type UpdatePersonRequest struct {
	ClientID         int32
	FirstName        string // PHI
	LastName         string // PHI
	DOB              string // PHI — "2006-01-02"
	MailingLine1     string // PHI
	MailingLine2     string // PHI
	MailingCity      string // PHI
	MailingState     string // PHI
	MailingZIP       string // PHI
	CountryAlphaCode string
	Phone            string // PHI
	Email            string // PHI
}

// FisPerson is the read model from GET /persons/{personId}.
type FisPerson struct {
	PersonID    FisPersonID
	ClientID    int32
	FirstName   string // PHI
	LastName    string // PHI
	RiskStatus  string // "Pass" or "Fail" — KYC result from FIS
	InsertedAt  time.Time
	UpdatedAt   time.Time
}

// IssueCardRequest maps to POST /cards (CardRequest schema).
// PersonID is a typed argument on IssueCard, not in this struct, so the compiler
// enforces person-before-card sequencing.
// subprogramId and packageId are int32 — sourced from programs table, never hardcoded.
type IssueCardRequest struct {
	ClientID     int32  // FIS client ID from program config
	SubprogramID int32  // FIS subprogram ID e.g. 26071 for RFU Oregon
	PackageID    int32  // FIS package ID — assigned by Selvi via ACC upload
	Comment      string // optional — journaling only
}

// FisCard is the read model from GET /cards/{cardId}.
type FisCard struct {
	CardID               FisCardID
	PersonID             FisPersonID
	Status               FisCardStatus
	SubprogramID         int32
	PackageID            int32
	Last4CardNumber      string
	Proxy                string
	PhysicalExpiration   string    // "MM/YY" as encoded on card
	IsExpired            bool
	InsertedAt           time.Time
	UpdatedAt            time.Time
}

// ShippingMethod is the FIS-defined fulfillment shipping option.
type ShippingMethod string

const (
	ShipUSPS1stClass            ShippingMethod = "USPS 1st Class"
	ShipUSPS1stClassWithTracking ShippingMethod = "USPS 1st Class with tracking"
	ShipUPSGround               ShippingMethod = "UPS Ground"
	ShipUPS2ndDay               ShippingMethod = "UPS 2nd Day"
	ShipUPSNextDay              ShippingMethod = "UPS Next Day"
	ShipFedExGround             ShippingMethod = "FedEx Ground"
	ShipFedEx2ndDay             ShippingMethod = "FedEx 2nd Day"
	ShipFedExNextDay            ShippingMethod = "FedEx Next Day"
)

// ReplaceCardRequest maps to POST /cards/{cardId}/replace (ReplaceCardRequest schema).
// IMPORTANT: Returns 201 NO BODY. New cardId is not in the response.
// After calling ReplaceCard, the adapter must call GetCard(oldCardId) and check
// for replacement linkage to discover the new cardId. Open item — confirm with John Stevens.
type ReplaceCardRequest struct {
	ShippingMethod ShippingMethod
	Comment        string
}

// ReissueCardRequest maps to POST /cards/{cardId}/reissue. Returns 204 no body.
// Reissue preserves card number — new physical card, same PAN.
type ReissueCardRequest struct {
	ShippingMethod ShippingMethod
	Comment        string
}

// ActivateCardRequest maps to POST /cards/{cardId}/activate. Returns 204 no body.
// Card must be in Ready status. Idempotent: already-Active cards return success.
type ActivateCardRequest struct {
	// PhysicalExpirationDate targets a specific card instance. Required if the card
	// has multiple instances (e.g. after renewal). Format: "MM/YY".
	PhysicalExpirationDate string // optional for single-instance cards
	Comment                string
}

// ChangeCardStatusRequest maps to POST /cards/{cardId}/change-status. Returns 204 no body.
// Valid targets: Ready (from Dormant only), Lost, Suspended, Fraud, PFraud.
// Cannot be used to set Closed — use CloseCard for that.
type ChangeCardStatusRequest struct {
	Status                 ChangeCardStatusTarget
	PhysicalExpirationDate string // optional — targets specific card instance
	Comment                string
}

// CloseCardRequest maps to POST /cards/{cardId}/close. Returns 204 no body.
type CloseCardRequest struct {
	PhysicalExpirationDate string // optional — targets specific card instance
	Comment                string
}

// LoadFundsRequest maps to POST /cards/{cardId}/loads.
// purseNumber is required for multi-purse programs (RFU).
// amount is a decimal string e.g. "95.00" — adapter converts from cents.
// clientReferenceNumber is for back-office traceability only, NOT idempotency.
type LoadFundsRequest struct {
	PurseNumber     FisPurseNumber
	AmountCents     int64
	ClientReference string // One Fintech correlationID as string — traceability only
	Comment         string
}

// LoadFundsResult is the response from POST /cards/{cardId}/loads.
// runningBalance is a decimal string — adapter converts to cents.
type LoadFundsResult struct {
	TransactionID   FisTransactionID
	RunningBalanceCents int64
}

// FisPurse is the read model from GET /accounts/{cardId}/purses/{purseNumber}.
// availableBalance and settledBalance are decimal strings parsed to cents by adapter.
type FisPurse struct {
	CardID              FisCardID
	PurseNumber         FisPurseNumber
	PurseName           string          // e.g. "OTC2550", "FOD2550"
	Status              PurseStatusCode
	AvailableBalanceCents int64
	SettledBalanceCents   int64
	CurrencyAlphaCode   string
	EffectiveDate       time.Time
	ExpirationDate      time.Time
}

// TransactionFilter is the optional filter for GET /cards/{cardId}/transactions.
// startInserted and endInserted are date strings "2006-01-02".
// Adapter validates that the range does not exceed 30 days before calling FIS.
type TransactionFilter struct {
	StartDate   time.Time // maps to startInserted
	EndDate     time.Time // maps to endInserted
	PurseNumber FisPurseNumber // 0 = all purses
	RequestCode int            // 0 = all
	TypeCode    int            // 0 = all
}

// FisTransaction is one record from transaction list or detail endpoints.
type FisTransaction struct {
	TransactionID FisTransactionID
	CardID        FisCardID
	AmountCents   int64
	InsertedAt    time.Time
	MerchantName  string
	Status        string
}

// TranslateResponse is the result of POST /translate-cardnumber or /translate-proxy.
type TranslateResponse struct {
	CardID FisCardID
}

// ─── Outbound port ────────────────────────────────────────────────────────────

// IFisCodeConnectPort is the outbound adapter seam for all FIS Code Connect v1.4.0
// real-time operations (PrePaidSouthCardServices).
//
// All 20 FIS endpoints are represented. Callers never import net/http.
//
// Compile-time assertion in concrete adapter:
//
//	var _ IFisCodeConnectPort = (*FisCodeConnectAdapter)(nil)
type IFisCodeConnectPort interface {

	// ── Person ───────────────────────────────────────────────────────────────

	// CreatePerson calls POST /persons (201 no body) then GET /persons/{personId}
	// to resolve the assigned personId. Returns FisPersonID (int32).
	// Store in consumers.fis_person_id immediately.
	CreatePerson(ctx interface{ Done() <-chan struct{} }, req CreatePersonRequest) (FisPersonID, error)

	// GetPerson calls GET /persons/{personId}.
	GetPerson(ctx interface{ Done() <-chan struct{} }, id FisPersonID) (*FisPerson, error)

	// UpdatePerson calls PUT /persons/{personId}.
	UpdatePerson(ctx interface{ Done() <-chan struct{} }, id FisPersonID, req UpdatePersonRequest) error

	// ── Card ─────────────────────────────────────────────────────────────────

	// IssueCard calls POST /cards (201 no body) then GET /cards/{cardId} to confirm.
	// personID is a typed int32 argument — compiler enforces person-before-card.
	// Returns FisCardID (18-char string). Store in cards.fis_card_id immediately.
	IssueCard(ctx interface{ Done() <-chan struct{} }, personID FisPersonID, req IssueCardRequest) (FisCardID, error)

	// GetCard calls GET /cards/{cardId}.
	GetCard(ctx interface{ Done() <-chan struct{} }, id FisCardID) (*FisCard, error)

	// ActivateCard calls POST /cards/{cardId}/activate (204 no body).
	// Transitions Ready → Active. Idempotent: already-Active returns success.
	ActivateCard(ctx interface{ Done() <-chan struct{} }, id FisCardID, req ActivateCardRequest) error

	// ChangeCardStatus calls POST /cards/{cardId}/change-status (204 no body).
	// Valid targets: Ready (from Dormant), Lost, Suspended, Fraud, PFraud.
	// Returns ErrFisCardTerminal if card is in a terminal status.
	ChangeCardStatus(ctx interface{ Done() <-chan struct{} }, id FisCardID, req ChangeCardStatusRequest) error

	// CloseCard calls POST /cards/{cardId}/close (204 no body).
	// Transitions card to terminal Closed status.
	// Used by CancelCard domain operation — purses must be closed separately.
	CloseCard(ctx interface{ Done() <-chan struct{} }, id FisCardID, req CloseCardRequest) error

	// LoadFunds calls POST /cards/{cardId}/loads.
	// purseNumber embedded in req — required for RFU multi-purse.
	LoadFunds(ctx interface{ Done() <-chan struct{} }, id FisCardID, req LoadFundsRequest) (*LoadFundsResult, error)

	// ReissueCard calls POST /cards/{cardId}/reissue (204 no body).
	// Preserves card number (PAN) — new physical card, new expiry.
	ReissueCard(ctx interface{ Done() <-chan struct{} }, id FisCardID, req ReissueCardRequest) error

	// ReplaceCard calls POST /cards/{cardId}/replace (201 no body).
	// New card number (PAN) and CVV. Old card becomes terminal Replaced status.
	// KNOWN GAP: new cardId is not in the 201 response. Adapter calls GetCard(oldId)
	// after replace to discover new cardId via replacement linkage.
	// Returns ErrReplaceCardIDUnresolvable if new cardId cannot be determined.
	// Confirm resolution pattern with John Stevens before implementing.
	ReplaceCard(ctx interface{ Done() <-chan struct{} }, id FisCardID, req ReplaceCardRequest) (FisCardID, error)

	// RegisterPersonToCard calls POST /cards/{cardId}/register.
	// Associates a person with a pre-allocated (inventory) card.
	// Only applicable for inventory card programs — confirm with John Stevens.
	RegisterPersonToCard(ctx interface{ Done() <-chan struct{} }, cardID FisCardID, personID FisPersonID, comment string) error

	// ── Account / Purse ──────────────────────────────────────────────────────

	// GetPurses calls GET /accounts/{cardId}/purses.
	// Returns all purse slots. For RFU: always 2 (OTC + FOD).
	GetPurses(ctx interface{ Done() <-chan struct{} }, cardID FisCardID) ([]FisPurse, error)

	// GetPurse calls GET /accounts/{cardId}/purses/{purseNumber}.
	GetPurse(ctx interface{ Done() <-chan struct{} }, cardID FisCardID, purseNumber FisPurseNumber) (*FisPurse, error)

	// SetPurseStatus calls POST /accounts/{cardId}/purses/{purseNumber}/status (204 no body).
	SetPurseStatus(ctx interface{ Done() <-chan struct{} }, cardID FisCardID, purseNumber FisPurseNumber, status PurseStatusCode) error

	// ── Transactions ─────────────────────────────────────────────────────────

	// GetCardTransactions calls GET /cards/{cardId}/transactions.
	// Returns ErrTransactionWindowExceeded if filter date range > 30 days.
	// For history beyond 30 days use FIS XTRACT feeds (DM-03 — Kendra Williams).
	GetCardTransactions(ctx interface{ Done() <-chan struct{} }, cardID FisCardID, filter TransactionFilter) ([]FisTransaction, error)

	// GetTransaction calls GET /transactions/{transactionId}.
	GetTransaction(ctx interface{ Done() <-chan struct{} }, id FisTransactionID) (*FisTransaction, error)

	// ── Translate ────────────────────────────────────────────────────────────

	// TranslateCardNumber calls POST /translate-cardnumber.
	// Resolves physical card number to FisCardID.
	// IVR cancel path: IVR presents card token → TranslateCardNumber → CancelCard.
	TranslateCardNumber(ctx interface{ Done() <-chan struct{} }, cardNumber string) (FisCardID, error)

	// TranslateProxy calls POST /translate-proxy.
	// Resolves proxy number to FisCardID. clientId required by FIS for proxy lookups.
	TranslateProxy(ctx interface{ Done() <-chan struct{} }, proxy string, clientID int32) (FisCardID, error)
}
