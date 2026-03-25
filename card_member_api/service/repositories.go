package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/_shared/domain"
)

// CardRepository is the narrow Aurora interface required by CardService.
// Defined here (not in ports) so it can be mocked independently of domain ports.
type CardRepository interface {
	GetConsumerByID(ctx context.Context, id uuid.UUID) (*domain.Consumer, error)
	GetConsumerByClientMemberID(ctx context.Context, tenantID, clientMemberID string) (*domain.Consumer, error)
	GetCardByMemberID(ctx context.Context, memberID uuid.UUID) (*domain.Card, error)
	GetCardByFisCardID(ctx context.Context, fisCardID string) (*domain.Card, error)
	GetPursesByCardID(ctx context.Context, cardID uuid.UUID) ([]*domain.Purse, error)
	GetPurseByBenefitType(ctx context.Context, cardID uuid.UUID, benefitType string) (*domain.Purse, error)

	CreateConsumer(ctx context.Context, c *domain.Consumer) error
	CreateCard(ctx context.Context, c *domain.Card) error
	CreatePurse(ctx context.Context, p *domain.Purse) error

	SetConsumerFISIDs(ctx context.Context, consumerID uuid.UUID, fisPersonID string, fisCUID *string) error
	SetCardFISID(ctx context.Context, cardID uuid.UUID, fisCardID string) error
	SetCardStatus(ctx context.Context, cardID uuid.UUID, status domain.CardStatus, at time.Time) error
	SetPurseStatus(ctx context.Context, purseID uuid.UUID, status domain.PurseStatus, at time.Time) error
	UpdateConsumerDemographics(ctx context.Context, consumerID uuid.UUID, req DemographicsUpdate) error
}

// DemographicsUpdate carries only the fields to be updated (zero = unchanged).
type DemographicsUpdate struct {
	FirstName string
	LastName  string
	DOB       *time.Time
	Address1  string
	Address2  string
	City      string
	State     string
	ZIP       string
	Phone     string
	Email     string
}
