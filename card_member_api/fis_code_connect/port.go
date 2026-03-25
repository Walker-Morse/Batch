// Package fis_code_connect defines the outbound port contract for the FIS Code Connect
// REST API (PrePaidSouthCardServices v1.4.0).
//
// Two implementations:
//   1. FisCodeConnectAdapter  — real HTTP (pending John Stevens auth credentials)
//   2. mock.FisCodeConnectMock — in-memory, deterministic, all tests + local dev
//
// Adapter cross-cutting concerns (all owned by impl, invisible to callers):
//   - pps-authorization: Base64({"username":"...","password":"...","source":0}) from Secrets Manager
//   - uuid header: per-request UUID from caller context
//   - encode-response: false on ALL requests (prevents HTML entity corruption)
//   - showSecureData: false hardcoded (PCI — never expose PAN/SSN)
//   - organization-id: conditional — confirm with John Stevens before enabling
//   - Exponential backoff + circuit breaker on transient failures
//
// Spec: PrePaidSouthCardServices v1.4.0
// Base URL: https://api-gw-uat.fisglobal.com/rest/prepaid-south/card-services/v1
package fis_code_connect

import (
	"context"
	"errors"
	"time"
)

// ─── Named ID types ───────────────────────────────────────────────────────────

// FisPersonID is int32 (range 1–2147483647). Returned via POST /persons → GET /persons.
type FisPersonID int32

// FisCardID is an 18-char string (^[1-9][0-9]{17}$). Returned via POST /cards → GET /cards.
// After /replace the old cardId becomes terminal Replaced; new cardId discovered via GET.
type FisCardID string

// FisTransactionID is UUID-format.
type FisTransactionID string

// FisPurseNumber is int32 (1-based). RFU: 1=OTC, 2=FOD. Required on LoadFunds.
type FisPurseNumber int32

// ─── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrFisNotFound               = errors.New("fis: not found (404)")
	ErrFisUnauthorized           = errors.New("fis: unauthorized (401)")
	ErrFisForbidden              = errors.New("fis: forbidden (403)")
	ErrFisCardTerminal           = errors.New("fis: card in terminal status")
	ErrFisCardHold               = errors.New("fis: card in hold status")
	ErrFisTooManyRequests        = errors.New("fis: rate limited (429)")
	ErrFisServerError            = errors.New("fis: server error (500/503)")
	ErrTransactionWindowExceeded = errors.New("fis: date range exceeds 30-day maximum")
	ErrReplaceCardIDUnresolvable = errors.New("fis: new cardId after replace not resolvable — confirm with John Stevens")
)

// ─── Card status ──────────────────────────────────────────────────────────────

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

func CardIsTerminal(s FisCardStatus) bool {
	switch s {
	case FisCardClosed, FisCardLost, FisCardReplaced, FisCardFraud, FisCardDestroyed:
		return true
	}
	return false
}

func CardIsHold(s FisCardStatus) bool {
	return s == FisCardSuspended || s == FisCardPFraud
}

type ChangeCardStatusTarget string

const (
	ChangeToReady     ChangeCardStatusTarget = "Ready"
	ChangeToLost      ChangeCardStatusTarget = "Lost"
	ChangeToSuspended ChangeCardStatusTarget = "Suspended"
	ChangeToFraud     ChangeCardStatusTarget = "Fraud"
	ChangeToPFraud    ChangeCardStatusTarget = "PFraud"
)

type PurseStatusCode string

const (
	PurseStatusActive    PurseStatusCode = "Active"
	PurseStatusClosed    PurseStatusCode = "Closed"
	PurseStatusSuspended PurseStatusCode = "Suspended"
)

type ShippingMethod string

const (
	ShipUSPS1stClass             ShippingMethod = "USPS 1st Class"
	ShipUSPS1stClassWithTracking ShippingMethod = "USPS 1st Class with tracking"
	ShipUPSGround                ShippingMethod = "UPS Ground"
	ShipFedExGround              ShippingMethod = "FedEx Ground"
)

// ─── Request / response types ─────────────────────────────────────────────────

type CreatePersonRequest struct {
	ClientID         int32
	ClientUniqueID   string // One Fintech clientMemberID → FIS clientUniqueId
	FirstName        string // PHI
	LastName         string // PHI
	DOB              string // PHI — "2006-01-02"
	MailingLine1     string // PHI
	MailingLine2     string // PHI
	MailingCity      string // PHI
	MailingState     string // PHI — 2-char
	MailingZIP       string // PHI
	CountryAlphaCode string // required — "USA" for RFU
	Phone            string // PHI
	Email            string // PHI
}

type UpdatePersonRequest struct {
	ClientID         int32
	FirstName        string // PHI
	LastName         string // PHI
	DOB              string // PHI
	MailingLine1     string // PHI
	MailingLine2     string // PHI
	MailingCity      string // PHI
	MailingState     string // PHI
	MailingZIP       string // PHI
	CountryAlphaCode string
	Phone            string // PHI
	Email            string // PHI
}

type FisPerson struct {
	PersonID   FisPersonID
	ClientID   int32
	FirstName  string // PHI
	LastName   string // PHI
	RiskStatus string // "Pass" or "Fail"
	InsertedAt time.Time
	UpdatedAt  time.Time
}

type IssueCardRequest struct {
	ClientID     int32
	SubprogramID int32 // e.g. 26071 — from programs table
	PackageID    int32 // from programs table (Selvi ACC upload)
	Comment      string
}

type FisCard struct {
	CardID             FisCardID
	PersonID           FisPersonID
	Status             FisCardStatus
	SubprogramID       int32
	PackageID          int32
	Last4CardNumber    string
	Proxy              string
	PhysicalExpiration string // "MM/YY"
	IsExpired          bool
	InsertedAt         time.Time
	UpdatedAt          time.Time
}

type CloseCardRequest struct {
	PhysicalExpirationDate string
	Comment                string
}

type ActivateCardRequest struct {
	PhysicalExpirationDate string
	Comment                string
}

type ChangeCardStatusRequest struct {
	Status                 ChangeCardStatusTarget
	PhysicalExpirationDate string
	Comment                string
}

type ReplaceCardRequest struct {
	ShippingMethod ShippingMethod
	Comment        string
}

type ReissueCardRequest struct {
	ShippingMethod ShippingMethod
	Comment        string
}

type LoadFundsRequest struct {
	PurseNumber     FisPurseNumber
	AmountCents     int64
	ClientReference string // traceability only — NOT idempotency
	Comment         string
}

type LoadFundsResult struct {
	TransactionID       FisTransactionID
	RunningBalanceCents int64 // parsed from FIS decimal string
}

type FisPurse struct {
	CardID                FisCardID
	PurseNumber           FisPurseNumber
	PurseName             string
	Status                PurseStatusCode
	AvailableBalanceCents int64 // parsed from FIS decimal string
	SettledBalanceCents   int64
	CurrencyAlphaCode     string
	EffectiveDate         time.Time
	ExpirationDate        time.Time
}

type TransactionFilter struct {
	StartDate   time.Time
	EndDate     time.Time
	PurseNumber FisPurseNumber // 0 = all
}

type FisTransaction struct {
	TransactionID FisTransactionID
	CardID        FisCardID
	AmountCents   int64
	InsertedAt    time.Time
	MerchantName  string
	Status        string
}

// ─── Port ─────────────────────────────────────────────────────────────────────

// IFisCodeConnectPort is the outbound seam for all 20 FIS Code Connect endpoints.
type IFisCodeConnectPort interface {
	// Person
	CreatePerson(ctx context.Context, req CreatePersonRequest) (FisPersonID, error)
	GetPerson(ctx context.Context, id FisPersonID) (*FisPerson, error)
	UpdatePerson(ctx context.Context, id FisPersonID, req UpdatePersonRequest) error

	// Card — personID typed arg on IssueCard enforces person-before-card at compile time
	IssueCard(ctx context.Context, personID FisPersonID, req IssueCardRequest) (FisCardID, error)
	GetCard(ctx context.Context, id FisCardID) (*FisCard, error)
	ActivateCard(ctx context.Context, id FisCardID, req ActivateCardRequest) error
	ChangeCardStatus(ctx context.Context, id FisCardID, req ChangeCardStatusRequest) error
	CloseCard(ctx context.Context, id FisCardID, req CloseCardRequest) error
	LoadFunds(ctx context.Context, id FisCardID, req LoadFundsRequest) (*LoadFundsResult, error)
	ReissueCard(ctx context.Context, id FisCardID, req ReissueCardRequest) error
	ReplaceCard(ctx context.Context, id FisCardID, req ReplaceCardRequest) (FisCardID, error)
	RegisterPersonToCard(ctx context.Context, cardID FisCardID, personID FisPersonID, comment string) error

	// Account / Purse
	GetPurses(ctx context.Context, cardID FisCardID) ([]FisPurse, error)
	GetPurse(ctx context.Context, cardID FisCardID, purseNumber FisPurseNumber) (*FisPurse, error)
	SetPurseStatus(ctx context.Context, cardID FisCardID, purseNumber FisPurseNumber, status PurseStatusCode) error

	// Transactions
	GetCardTransactions(ctx context.Context, cardID FisCardID, filter TransactionFilter) ([]FisTransaction, error)
	GetTransaction(ctx context.Context, id FisTransactionID) (*FisTransaction, error)

	// Translate
	TranslateCardNumber(ctx context.Context, cardNumber string) (FisCardID, error)
	TranslateProxy(ctx context.Context, proxy string, clientID int32) (FisCardID, error)
}
