package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/_shared/domain"
	sharedports "github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/card_member_api/fis_code_connect"
	"github.com/walker-morse/batch/card_member_api/ports"
	"github.com/walker-morse/batch/card_member_api/service"
)

// ─── Test fixtures ────────────────────────────────────────────────────────────

func newRctx() ports.RequestContext {
	return ports.RequestContext{
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		CallerType:    ports.CallerUniversalWallet,
	}
}

func resolvedConsumer() *domain.Consumer {
	personID := "900000000000000001"
	return &domain.Consumer{
		ID:             uuid.New(),
		TenantID:       "rfu-oregon",
		ClientMemberID: "MBR-001",
		Status:         domain.ConsumerActive,
		FISPersonID:    &personID,
	}
}

func resolvedCard(consumerID uuid.UUID) *domain.Card {
	fisCardID := "900000000000000002"
	return &domain.Card{
		ID:         uuid.New(),
		TenantID:   "rfu-oregon",
		ConsumerID: consumerID,
		FISCardID:  &fisCardID,
		Status:     domain.CardActive,
	}
}

func twoPurses(cardID uuid.UUID) []*domain.Purse {
	purseNum1 := int16(1)
	purseNum2 := int16(2)
	return []*domain.Purse{
		{ID: uuid.New(), CardID: cardID, FISPurseNumber: &purseNum1, Status: domain.PurseActive, BenefitType: domain.BenefitOTC},
		{ID: uuid.New(), CardID: cardID, FISPurseNumber: &purseNum2, Status: domain.PurseActive, BenefitType: domain.BenefitFOD},
	}
}

// ─── Happy path ───────────────────────────────────────────────────────────────

func TestCancelCard_HappyPath_MemberID(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	purses := twoPurses(card.ID)
	memberID := consumer.ID

	repo := &mockCardRepo{
		consumer: consumer,
		card:     card,
		purses:   purses,
	}
	fis := &mockFisPort{}
	cmds := &mockCmdRepo{}
	audit := &mockAuditWriter{}
	obs := &mockObsPort{}

	svc := service.NewCardService(fis, repo, cmds, audit, obs, noopLogger())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		MemberID:     &memberID,
		CancelReason: ports.CancelReasonLost,
	})

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.CardID != card.ID {
		t.Errorf("expected CardID %s, got %s", card.ID, result.CardID)
	}
	if result.PursesClosed != 2 {
		t.Errorf("expected 2 purses closed, got %d", result.PursesClosed)
	}
	if !fis.closeCardCalled {
		t.Error("expected CloseCard to be called at FIS")
	}
	if fis.setPurseStatusCount != 2 {
		t.Errorf("expected SetPurseStatus called 2 times, got %d", fis.setPurseStatusCount)
	}
	if !repo.cardStatusUpdated {
		t.Error("expected card status updated in Aurora")
	}
	if cmds.completedCount != 1 {
		t.Error("expected domain command marked Completed")
	}
	if audit.writeCount != 1 {
		t.Error("expected one audit log entry")
	}
}

// ─── IVR token path ───────────────────────────────────────────────────────────

func TestCancelCard_IVRTokenPath(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	purses := twoPurses(card.ID)
	token := "tok_abc123"

	repo := &mockCardRepo{
		consumer:      consumer,
		card:          card,
		purses:        purses,
		cardByFisID:   card,
	}
	fis := &mockFisPort{
		translateResult: fis_code_connect.FisCardID(*card.FISCardID),
	}
	cmds := &mockCmdRepo{}
	audit := &mockAuditWriter{}
	obs := &mockObsPort{}

	svc := service.NewCardService(fis, repo, cmds, audit, obs, noopLogger())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		CardToken:    &token,
		CancelReason: ports.CancelReasonStolen,
	})

	if err != nil {
		t.Fatalf("IVR path: expected no error, got: %v", err)
	}
	if !fis.translateCalled {
		t.Error("expected TranslateCardNumber to be called")
	}
	if result.PursesClosed != 2 {
		t.Errorf("expected 2 purses closed, got %d", result.PursesClosed)
	}
}

// ─── Idempotency: already completed ──────────────────────────────────────────

func TestCancelCard_IdempotentReplay_AlreadyCompleted(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	repo := &mockCardRepo{consumer: consumer, card: card, purses: twoPurses(card.ID)}
	fis := &mockFisPort{}
	cmds := &mockCmdRepo{
		existingStatus: string(domain.CommandCompleted),
	}

	svc := service.NewCardService(fis, repo, cmds, &mockAuditWriter{}, &mockObsPort{}, noopLogger())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		MemberID:     &memberID,
		CancelReason: ports.CancelReasonLost,
	})

	if err != nil {
		t.Fatalf("idempotent replay should not error, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result on idempotent replay")
	}
	if fis.closeCardCalled {
		t.Error("CloseCard should NOT be called on idempotent replay of completed command")
	}
}

// ─── Idempotency: in-flight duplicate ────────────────────────────────────────

func TestCancelCard_DuplicateInFlight(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	repo := &mockCardRepo{consumer: consumer, card: card}
	cmds := &mockCmdRepo{
		existingStatus: string(domain.CommandAccepted),
	}

	svc := service.NewCardService(&mockFisPort{}, repo, cmds, &mockAuditWriter{}, &mockObsPort{}, noopLogger())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		MemberID:     &memberID,
		CancelReason: ports.CancelReasonLost,
	})

	if !errors.Is(err, ports.ErrDuplicateRequest) {
		t.Errorf("expected ErrDuplicateRequest, got: %v", err)
	}
}

// ─── Card not resolved at FIS ─────────────────────────────────────────────────

func TestCancelCard_MemberNotResolved(t *testing.T) {
	consumer := resolvedConsumer()
	memberID := consumer.ID
	// Card has no FISCardID — Stage 7 not yet run
	card := &domain.Card{
		ID:         uuid.New(),
		ConsumerID: consumer.ID,
		FISCardID:  nil,
		Status:     domain.CardReady,
	}

	repo := &mockCardRepo{consumer: consumer, card: card, purses: []*domain.Purse{}}
	svc := service.NewCardService(&mockFisPort{}, repo, &mockCmdRepo{}, &mockAuditWriter{}, &mockObsPort{}, noopLogger())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		MemberID:     &memberID,
		CancelReason: ports.CancelReasonLost,
	})

	if !errors.Is(err, ports.ErrMemberNotResolved) {
		t.Errorf("expected ErrMemberNotResolved, got: %v", err)
	}
}

// ─── Already cancelled ────────────────────────────────────────────────────────

func TestCancelCard_AlreadyCancelled(t *testing.T) {
	consumer := resolvedConsumer()
	memberID := consumer.ID
	fisCardID := "900000000000000002"
	card := &domain.Card{
		ID:         uuid.New(),
		ConsumerID: consumer.ID,
		FISCardID:  &fisCardID,
		Status:     domain.CardClosed, // already closed
	}

	repo := &mockCardRepo{consumer: consumer, card: card}
	fis := &mockFisPort{}
	svc := service.NewCardService(fis, repo, &mockCmdRepo{}, &mockAuditWriter{}, &mockObsPort{}, noopLogger())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		MemberID:     &memberID,
		CancelReason: ports.CancelReasonLost,
	})

	if err != nil {
		t.Fatalf("already-cancelled should return success, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if fis.closeCardCalled {
		t.Error("CloseCard should NOT be called when card already closed in Aurora")
	}
}

// ─── FIS CloseCard failure ────────────────────────────────────────────────────

func TestCancelCard_FISCloseCardFails(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	repo := &mockCardRepo{consumer: consumer, card: card, purses: twoPurses(card.ID)}
	fis := &mockFisPort{closeCardErr: errors.New("fis: service unavailable")}
	cmds := &mockCmdRepo{}

	svc := service.NewCardService(fis, repo, cmds, &mockAuditWriter{}, &mockObsPort{}, noopLogger())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		MemberID:     &memberID,
		CancelReason: ports.CancelReasonLost,
	})

	if err == nil {
		t.Fatal("expected error when FIS CloseCard fails")
	}
	if cmds.failedCount != 1 {
		t.Errorf("expected command marked Failed, failedCount=%d", cmds.failedCount)
	}
}

// ─── Validation: missing both MemberID and CardToken ─────────────────────────

func TestCancelCard_ValidationError_NeitherIDNorToken(t *testing.T) {
	svc := service.NewCardService(&mockFisPort{}, &mockCardRepo{}, &mockCmdRepo{}, &mockAuditWriter{}, &mockObsPort{}, noopLogger())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx:         newRctx(),
		CancelReason: ports.CancelReasonLost,
		// Neither MemberID nor CardToken set
	})

	if err == nil {
		t.Fatal("expected validation error")
	}
}

// ─── Mock implementations ─────────────────────────────────────────────────────

type mockCardRepo struct {
	consumer          *domain.Consumer
	card              *domain.Card
	cardByFisID       *domain.Card
	purses            []*domain.Purse
	cardStatusUpdated bool
}

func (m *mockCardRepo) GetConsumerByID(_ context.Context, _ uuid.UUID) (*domain.Consumer, error) {
	if m.consumer == nil {
		return nil, ports.ErrMemberNotFound
	}
	return m.consumer, nil
}

func (m *mockCardRepo) GetCardByMemberID(_ context.Context, _ uuid.UUID) (*domain.Card, error) {
	if m.card == nil {
		return nil, ports.ErrCardNotFound
	}
	return m.card, nil
}

func (m *mockCardRepo) GetCardByFisCardID(_ context.Context, _ string) (*domain.Card, error) {
	if m.cardByFisID == nil {
		return nil, ports.ErrCardNotFound
	}
	return m.cardByFisID, nil
}

func (m *mockCardRepo) GetPursesByCardID(_ context.Context, _ uuid.UUID) ([]*domain.Purse, error) {
	return m.purses, nil
}

func (m *mockCardRepo) SetCardStatus(_ context.Context, _ uuid.UUID, _ domain.CardStatus, _ time.Time) error {
	m.cardStatusUpdated = true
	return nil
}

func (m *mockCardRepo) SetPurseStatus(_ context.Context, _ uuid.UUID, _ domain.PurseStatus, _ time.Time) error {
	return nil
}

type mockFisPort struct {
	translateCalled     bool
	translateResult     fis_code_connect.FisCardID
	closeCardCalled     bool
	closeCardErr        error
	setPurseStatusCount int
}

func (m *mockFisPort) CreatePerson(_ context.Context, _ fis_code_connect.CreatePersonRequest) (fis_code_connect.FisPersonID, error) {
	return "", nil
}
func (m *mockFisPort) GetPerson(_ context.Context, _ fis_code_connect.FisPersonID) (*fis_code_connect.FisPerson, error) {
	return nil, nil
}
func (m *mockFisPort) UpdatePerson(_ context.Context, _ fis_code_connect.FisPersonID, _ fis_code_connect.UpdatePersonRequest) error {
	return nil
}
func (m *mockFisPort) IssueCard(_ context.Context, _ fis_code_connect.FisPersonID, _ fis_code_connect.IssueCardRequest) (fis_code_connect.FisCardID, error) {
	return "", nil
}
func (m *mockFisPort) GetCard(_ context.Context, _ fis_code_connect.FisCardID) (*fis_code_connect.FisCard, error) {
	return nil, nil
}
func (m *mockFisPort) CloseCard(_ context.Context, _ fis_code_connect.FisCardID) error {
	m.closeCardCalled = true
	return m.closeCardErr
}
func (m *mockFisPort) LoadFunds(_ context.Context, _ fis_code_connect.FisCardID, _ fis_code_connect.LoadFundsRequest) error {
	return nil
}
func (m *mockFisPort) ReissueCard(_ context.Context, _ fis_code_connect.FisCardID, _ fis_code_connect.ReissueRequest) error {
	return nil
}
func (m *mockFisPort) ReplaceCard(_ context.Context, _ fis_code_connect.FisCardID, _ fis_code_connect.ReplaceRequest) (fis_code_connect.FisCardID, error) {
	return "", nil
}
func (m *mockFisPort) GetPurses(_ context.Context, _ fis_code_connect.FisCardID) ([]fis_code_connect.FisPurse, error) {
	return nil, nil
}
func (m *mockFisPort) GetPurse(_ context.Context, _ fis_code_connect.FisCardID, _ fis_code_connect.FisPurseNumber) (*fis_code_connect.FisPurse, error) {
	return nil, nil
}
func (m *mockFisPort) SetPurseStatus(_ context.Context, _ fis_code_connect.FisCardID, _ fis_code_connect.FisPurseNumber, _ fis_code_connect.PurseStatusCode) error {
	m.setPurseStatusCount++
	return nil
}
func (m *mockFisPort) GetCardTransactions(_ context.Context, _ fis_code_connect.FisCardID, _ fis_code_connect.TransactionRangeRequest) ([]fis_code_connect.FisTransaction, error) {
	return nil, nil
}
func (m *mockFisPort) GetTransaction(_ context.Context, _ fis_code_connect.FisTransactionID) (*fis_code_connect.FisTransaction, error) {
	return nil, nil
}
func (m *mockFisPort) TranslateCardNumber(_ context.Context, _ string) (fis_code_connect.FisCardID, error) {
	m.translateCalled = true
	return m.translateResult, nil
}
func (m *mockFisPort) TranslateProxy(_ context.Context, _ string) (fis_code_connect.FisCardID, error) {
	return "", nil
}

type mockCmdRepo struct {
	existingStatus string
	completedCount int
	failedCount    int
}

func (m *mockCmdRepo) Insert(_ context.Context, _ *sharedports.DomainCommand) error { return nil }
func (m *mockCmdRepo) FindDuplicate(_ context.Context, _, _, _, _ string) (*sharedports.DomainCommand, error) {
	if m.existingStatus == "" {
		return nil, nil
	}
	return &sharedports.DomainCommand{Status: m.existingStatus}, nil
}
func (m *mockCmdRepo) UpdateStatus(_ context.Context, _ uuid.UUID, status string, _ *string) error {
	switch status {
	case string(domain.CommandCompleted):
		m.completedCount++
	case string(domain.CommandFailed):
		m.failedCount++
	}
	return nil
}

type mockAuditWriter struct{ writeCount int }

func (m *mockAuditWriter) Write(_ context.Context, _ *sharedports.AuditEntry) error {
	m.writeCount++
	return nil
}

type mockObsPort struct{}

func (m *mockObsPort) LogEvent(_ context.Context, _ *sharedports.LogEvent) error     { return nil }
func (m *mockObsPort) RecordMetric(_ context.Context, _ string, _ float64, _ map[string]string) error {
	return nil
}

func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
