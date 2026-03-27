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
	fis "github.com/walker-morse/batch/card_member_api/fis_code_connect"
	"github.com/walker-morse/batch/card_member_api/fis_code_connect/mock"
	"github.com/walker-morse/batch/card_member_api/ports"
	"github.com/walker-morse/batch/card_member_api/service"
)

// ─── Fixtures ─────────────────────────────────────────────────────────────────

func newRctx() ports.RequestContext {
	return ports.RequestContext{
		CorrelationID:  uuid.New(),
		IdempotencyKey: uuid.New(),
		TenantID:       "rfu-oregon",
		CallerType:     ports.CallerUniversalWallet,
	}
}

func resolvedConsumer() *domain.Consumer {
	pid := "1001"
	return &domain.Consumer{
		ID: uuid.New(), TenantID: "rfu-oregon",
		ClientMemberID: "MBR-001", Status: domain.ConsumerActive,
		FISPersonID: &pid, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func resolvedCard(consumerID uuid.UUID) *domain.Card {
	fid := "900000000000000001"
	return &domain.Card{
		ID: uuid.New(), TenantID: "rfu-oregon",
		ConsumerID: consumerID, ClientMemberID: "MBR-001",
		FISCardID: &fid, Status: domain.CardActive,
	}
}

func twoPurses(cardID uuid.UUID) []*domain.Purse {
	n1, n2 := int16(1), int16(2)
	return []*domain.Purse{
		{ID: uuid.New(), CardID: cardID, FISPurseNumber: &n1, Status: domain.PurseActive},
		{ID: uuid.New(), CardID: cardID, FISPurseNumber: &n2, Status: domain.PurseActive},
	}
}

func noopLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ─── CancelCard: happy path (MemberID) ───────────────────────────────────────

func TestCancelCard_HappyPath_MemberID(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	purses := twoPurses(card.ID)
	memberID := consumer.ID

	fisMock := mock.NewFisCodeConnectMock()
	// Seed card so FIS mock knows about it
	fisMock.SeedCard(fis.FisCard{
		CardID:   fis.FisCardID(*card.FISCardID),
		PersonID: 1001,
		Status:   fis.FisCardActive,
	}, []fis.FisPurse{
		{CardID: fis.FisCardID(*card.FISCardID), PurseNumber: 1, Status: fis.PurseStatusActive},
		{CardID: fis.FisCardID(*card.FISCardID), PurseNumber: 2, Status: fis.PurseStatusActive},
	})

	repo := &mockRepo{consumer: consumer, card: card, purses: purses}
	cmds := &mockCmds{}

	svc := service.NewCardService(fisMock, repo, cmds, &mockAudit{}, &mockObs{}, noopLog())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})

	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if result.PursesClosed != 2 {
		t.Errorf("expected 2 purses closed, got %d", result.PursesClosed)
	}
	if fisMock.Calls["CloseCard"] != 1 {
		t.Errorf("expected 1 CloseCard call, got %d", fisMock.Calls["CloseCard"])
	}
	if fisMock.Calls["SetPurseStatus"] != 2 {
		t.Errorf("expected 2 SetPurseStatus calls, got %d", fisMock.Calls["SetPurseStatus"])
	}
	if !repo.cardStatusUpdated {
		t.Error("expected card status updated in Aurora")
	}
	if cmds.completedCount != 1 {
		t.Errorf("expected 1 command Completed, got %d", cmds.completedCount)
	}
}

// ─── CancelCard: IVR token path ───────────────────────────────────────────────

func TestCancelCard_IVRTokenPath(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)

	fisMock := mock.NewFisCodeConnectMock()
	fisCardID := fis.FisCardID(*card.FISCardID)
	fisMock.SeedCard(fis.FisCard{CardID: fisCardID, PersonID: 1001, Status: fis.FisCardActive},
		[]fis.FisPurse{{CardID: fisCardID, PurseNumber: 1, Status: fis.PurseStatusActive}})
	// Register token → cardId in mock
	fisMock.SeedCard(fis.FisCard{CardID: fisCardID, PersonID: 1001, Status: fis.FisCardActive},
		[]fis.FisPurse{{CardID: fisCardID, PurseNumber: 1, Status: fis.PurseStatusActive}})

	repo := &mockRepo{consumer: consumer, card: card, cardByFisID: card,
		purses: []*domain.Purse{{ID: uuid.New(), CardID: card.ID,
			FISPurseNumber: func() *int16 { n := int16(1); return &n }(),
			Status: domain.PurseActive}}}
	// Wire token into mock's tokenIndex via a manual seed
	fisMock.Reset()
	fisMock.SeedCard(fis.FisCard{CardID: fisCardID, PersonID: 1001, Status: fis.FisCardActive},
		[]fis.FisPurse{{CardID: fisCardID, PurseNumber: 1, Status: fis.PurseStatusActive}})
	// TranslateCardNumber in the mock uses tokenIndex where token==cardId
	// So set token to match fisCardID for this test
	tokenAsCardID := string(fisCardID)
	repo.cardByFisID = card

	svc := service.NewCardService(fisMock, repo, &mockCmds{}, &mockAudit{}, &mockObs{}, noopLog())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), CardToken: &tokenAsCardID, CancelReason: ports.CancelReasonStolen,
	})

	if err != nil {
		t.Fatalf("IVR path: %v", err)
	}
	if result.MemberID != consumer.ID {
		t.Errorf("expected member %s, got %s", consumer.ID, result.MemberID)
	}
	if fisMock.Calls["TranslateCardNumber"] != 1 {
		t.Errorf("expected TranslateCardNumber called once, got %d", fisMock.Calls["TranslateCardNumber"])
	}
}

// ─── CancelCard: idempotent replay (already Completed) ───────────────────────

func TestCancelCard_IdempotentReplay(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	fisMock := mock.NewFisCodeConnectMock()
	repo := &mockRepo{consumer: consumer, card: card, purses: twoPurses(card.ID)}
	cmds := &mockCmds{existingStatus: string(domain.CommandCompleted)}

	svc := service.NewCardService(fisMock, repo, cmds, &mockAudit{}, &mockObs{}, noopLog())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})

	if err != nil {
		t.Fatalf("idempotent replay should not error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result on idempotent replay")
	}
	if fisMock.Calls["CloseCard"] != 0 {
		t.Error("CloseCard must NOT be called on idempotent replay of completed command")
	}
}

// ─── CancelCard: duplicate in-flight ─────────────────────────────────────────

func TestCancelCard_DuplicateInFlight(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	repo := &mockRepo{consumer: consumer, card: card}
	cmds := &mockCmds{existingStatus: string(domain.CommandAccepted)}

	svc := service.NewCardService(mock.NewFisCodeConnectMock(), repo, cmds, &mockAudit{}, &mockObs{}, noopLog())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})

	if !errors.Is(err, ports.ErrDuplicateRequest) {
		t.Errorf("expected ErrDuplicateRequest, got: %v", err)
	}
}

// ─── CancelCard: member not FIS-resolved ─────────────────────────────────────

func TestCancelCard_MemberNotResolved(t *testing.T) {
	consumer := resolvedConsumer()
	memberID := consumer.ID
	card := &domain.Card{ID: uuid.New(), ConsumerID: consumer.ID, FISCardID: nil, Status: domain.CardReady}

	repo := &mockRepo{consumer: consumer, card: card, purses: []*domain.Purse{}}
	svc := service.NewCardService(mock.NewFisCodeConnectMock(), repo, &mockCmds{}, &mockAudit{}, &mockObs{}, noopLog())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})

	if !errors.Is(err, ports.ErrMemberNotResolved) {
		t.Errorf("expected ErrMemberNotResolved, got: %v", err)
	}
}

// ─── CancelCard: already closed ──────────────────────────────────────────────

func TestCancelCard_AlreadyClosed(t *testing.T) {
	consumer := resolvedConsumer()
	memberID := consumer.ID
	fid := "900000000000000001"
	card := &domain.Card{ID: uuid.New(), ConsumerID: consumer.ID, FISCardID: &fid, Status: domain.CardClosed}

	repo := &mockRepo{consumer: consumer, card: card}
	fisMock := mock.NewFisCodeConnectMock()
	svc := service.NewCardService(fisMock, repo, &mockCmds{}, &mockAudit{}, &mockObs{}, noopLog())

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})

	if err != nil {
		t.Fatalf("already-closed should succeed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if fisMock.Calls["CloseCard"] != 0 {
		t.Error("CloseCard must NOT be called when card already closed in Aurora")
	}
}

// ─── CancelCard: FIS CloseCard fails ─────────────────────────────────────────

func TestCancelCard_FISCloseCardFails(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	fisMock := mock.NewFisCodeConnectMock()
	fisMock.SeedCard(fis.FisCard{CardID: fis.FisCardID(*card.FISCardID), PersonID: 1001, Status: fis.FisCardActive}, nil)
	fisMock.InjectError("CloseCard", errors.New("fis: service unavailable"))

	repo := &mockRepo{consumer: consumer, card: card, purses: twoPurses(card.ID)}
	cmds := &mockCmds{}

	svc := service.NewCardService(fisMock, repo, cmds, &mockAudit{}, &mockObs{}, noopLog())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), MemberID: &memberID, CancelReason: ports.CancelReasonFraud,
	})

	if err == nil {
		t.Fatal("expected error when FIS CloseCard fails")
	}
	if cmds.failedCount != 1 {
		t.Errorf("expected command marked Failed, got failedCount=%d", cmds.failedCount)
	}
}

// ─── CancelCard: validation — neither MemberID nor CardToken ─────────────────

func TestCancelCard_ValidationError(t *testing.T) {
	svc := service.NewCardService(mock.NewFisCodeConnectMock(), &mockRepo{}, &mockCmds{}, &mockAudit{}, &mockObs{}, noopLog())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: newRctx(), CancelReason: ports.CancelReasonLost,
		// Neither MemberID nor CardToken
	})

	if !errors.Is(err, ports.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got: %v", err)
	}
}

// ─── GetBalance: reads live from FIS ─────────────────────────────────────────

func TestGetBalance_LiveFromFIS(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)

	fisMock := mock.NewFisCodeConnectMock()
	fisCardID := fis.FisCardID(*card.FISCardID)
	fisMock.SeedCard(fis.FisCard{CardID: fisCardID, PersonID: 1001, Status: fis.FisCardActive},
		[]fis.FisPurse{
			{CardID: fisCardID, PurseNumber: 1, PurseName: "OTC2550", Status: fis.PurseStatusActive, AvailableBalanceCents: 9500},
			{CardID: fisCardID, PurseNumber: 2, PurseName: "FOD2550", Status: fis.PurseStatusActive, AvailableBalanceCents: 25000},
		})

	repo := &mockRepo{consumer: consumer, card: card}
	svc := service.NewCardService(fisMock, repo, &mockCmds{}, &mockAudit{}, &mockObs{}, noopLog())

	result, err := svc.GetBalance(context.Background(), ports.GetBalanceRequest{
		Rctx: newRctx(), MemberID: consumer.ID,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Purses) != 2 {
		t.Errorf("expected 2 purses, got %d", len(result.Purses))
	}
	if result.Purses[0].AvailableCents != 9500 {
		t.Errorf("expected OTC balance 9500, got %d", result.Purses[0].AvailableCents)
	}
	if fisMock.Calls["GetPurses"] != 1 {
		t.Error("expected GetPurses called on FIS mock")
	}
}

// ─── ResolveCard: token → member ─────────────────────────────────────────────

func TestResolveCard_TokenToMember(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)

	fisMock := mock.NewFisCodeConnectMock()
	fisCardID := fis.FisCardID(*card.FISCardID)
	fisMock.SeedCard(fis.FisCard{CardID: fisCardID, PersonID: 1001, Status: fis.FisCardActive}, nil)

	repo := &mockRepo{consumer: consumer, card: card, cardByFisID: card}
	svc := service.NewCardService(fisMock, repo, &mockCmds{}, &mockAudit{}, &mockObs{}, noopLog())

	// In the mock, tokenIndex[string(cardID)] = cardID
	result, err := svc.ResolveCard(context.Background(), ports.ResolveCardRequest{
		Rctx:  newRctx(),
		Token: string(fisCardID),
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.MemberID != consumer.ID {
		t.Errorf("expected member %s, got %s", consumer.ID, result.MemberID)
	}
}

// ─── Mock implementations ─────────────────────────────────────────────────────

type mockRepo struct {
	consumer            *domain.Consumer
	card                *domain.Card
	cardByFisID         *domain.Card
	purses              []*domain.Purse
	cardStatusUpdated   bool
	demographicsUpdated bool
}

func (m *mockRepo) GetConsumerByID(_ context.Context, _ uuid.UUID) (*domain.Consumer, error) {
	if m.consumer == nil { return nil, ports.ErrMemberNotFound }
	return m.consumer, nil
}
func (m *mockRepo) GetConsumerByClientMemberID(_ context.Context, _, _ string) (*domain.Consumer, error) {
	if m.consumer == nil { return nil, ports.ErrMemberNotFound }
	return m.consumer, nil
}
func (m *mockRepo) GetCardByMemberID(_ context.Context, _ uuid.UUID) (*domain.Card, error) {
	if m.card == nil { return nil, ports.ErrCardNotFound }
	return m.card, nil
}
func (m *mockRepo) GetCardByFisCardID(_ context.Context, _ string) (*domain.Card, error) {
	if m.cardByFisID == nil { return nil, ports.ErrCardNotFound }
	return m.cardByFisID, nil
}
func (m *mockRepo) GetPursesByCardID(_ context.Context, _ uuid.UUID) ([]*domain.Purse, error) {
	return m.purses, nil
}
func (m *mockRepo) GetPurseByBenefitType(_ context.Context, _ uuid.UUID, _ string) (*domain.Purse, error) {
	for _, p := range m.purses {
		if p.FISPurseNumber != nil { return p, nil }
	}
	return nil, errors.New("purse not found")
}
func (m *mockRepo) CreateConsumer(_ context.Context, _ *domain.Consumer) error  { return nil }
func (m *mockRepo) CreateCard(_ context.Context, _ *domain.Card) error          { return nil }
func (m *mockRepo) CreatePurse(_ context.Context, _ *domain.Purse) error        { return nil }
func (m *mockRepo) SetConsumerFISIDs(_ context.Context, _ uuid.UUID, _ string, _ *string) error { return nil }
func (m *mockRepo) SetCardFISID(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (m *mockRepo) SetCardStatus(_ context.Context, _ uuid.UUID, _ domain.CardStatus, _ time.Time) error {
	m.cardStatusUpdated = true
	return nil
}
func (m *mockRepo) SetPurseStatus(_ context.Context, _ uuid.UUID, _ domain.PurseStatus, _ time.Time) error { return nil }
func (m *mockRepo) UpdateConsumerDemographics(_ context.Context, _ uuid.UUID, _ service.DemographicsUpdate) error {
	m.demographicsUpdated = true
	return nil
}

type mockCmds struct {
	existingStatus  string
	existingIdemKey *uuid.UUID  // for Layer 1 test: simulate a row with this idempotency key
	completedCount  int
	failedCount     int
	lastInserted    *sharedports.DomainCommand
}
func (m *mockCmds) Insert(_ context.Context, cmd *sharedports.DomainCommand) error {
	m.lastInserted = cmd
	return nil
}
func (m *mockCmds) FindByIdempotencyKey(_ context.Context, key uuid.UUID) (*sharedports.DomainCommand, error) {
	if m.existingIdemKey != nil && *m.existingIdemKey == key && m.existingStatus != "" {
		return &sharedports.DomainCommand{Status: m.existingStatus, IdempotencyKey: m.existingIdemKey}, nil
	}
	return nil, nil
}
func (m *mockCmds) FindDuplicate(_ context.Context, _, _, _, _ string) (*sharedports.DomainCommand, error) {
	if m.existingStatus == "" { return nil, nil }
	return &sharedports.DomainCommand{Status: m.existingStatus}, nil
}
func (m *mockCmds) UpdateStatus(_ context.Context, _ uuid.UUID, status string, _ *string) error {
	switch status {
	case string(domain.CommandCompleted): m.completedCount++
	case string(domain.CommandFailed):   m.failedCount++
	}
	return nil
}

type mockAudit struct{}
func (m *mockAudit) Write(_ context.Context, _ *sharedports.AuditEntry) error { return nil }

type mockObs struct{}
func (m *mockObs) LogEvent(_ context.Context, _ *sharedports.LogEvent) error       { return nil }
func (m *mockObs) RecordMetric(_ context.Context, _ string, _ float64, _ map[string]string) error { return nil }

// ─── Two-layer idempotency tests ──────────────────────────────────────────────

// TestCancelCard_Layer1_UUIDShortCircuit verifies that a retry with the same
// X-Idempotency-Key is caught by Layer 1 without re-evaluating the composite key.
func TestCancelCard_Layer1_UUIDShortCircuit(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	fisCardID := fis.FisCardID(*card.FISCardID)
	memberID := consumer.ID

	fisMock := mock.NewFisCodeConnectMock()
	fisMock.SeedCard(fis.FisCard{CardID: fisCardID, PersonID: 1001, Status: fis.FisCardActive},
		[]fis.FisPurse{{CardID: fisCardID, PurseNumber: 1, Status: fis.PurseStatusActive}})

	repo := &mockRepo{consumer: consumer, card: card, purses: []*domain.Purse{{
		ID: uuid.New(), CardID: card.ID,
		FISPurseNumber: func() *int16 { n := int16(1); return &n }(),
		Status: domain.PurseActive,
	}}}

	// First call — succeeds, inserts Completed command with idempotency key
	idemKey := uuid.New()
	rctx := ports.RequestContext{
		CorrelationID:  uuid.New(),
		IdempotencyKey: idemKey,
		TenantID:       "rfu-oregon",
		CallerType:     ports.CallerUniversalWallet,
	}

	cmds := &mockCmds{}
	svc := service.NewCardService(fisMock, repo, cmds, &mockAudit{}, &mockObs{}, noopLog())

	_, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: rctx, MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	firstCallFISCalls := fisMock.Calls["CloseCard"]

	// Second call — same idempotency key, Layer 1 should short-circuit
	// Simulate: command row now shows Completed with our idemKey
	cmds.existingStatus = string(domain.CommandCompleted)
	cmds.existingIdemKey = &idemKey

	_, err = svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: rctx, MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})
	if err != nil {
		t.Fatalf("idempotent retry should succeed: %v", err)
	}

	// FIS must NOT have been called again on the retry
	if fisMock.Calls["CloseCard"] != firstCallFISCalls {
		t.Errorf("Layer 1 short-circuit failed: CloseCard was called again on retry")
	}
}

// TestCancelCard_Layer2_BusinessRuleDedup verifies that a different caller with
// a different idempotency key is blocked by Layer 2 if the operation already succeeded.
func TestCancelCard_Layer2_BusinessRuleDedup(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	memberID := consumer.ID

	repo := &mockRepo{consumer: consumer, card: card}
	// Different idempotency key from a different caller, but same member+operation
	cmds := &mockCmds{existingStatus: string(domain.CommandCompleted)}

	svc := service.NewCardService(mock.NewFisCodeConnectMock(), repo, cmds, &mockAudit{}, &mockObs{}, noopLog())

	differentIdemKey := uuid.New()
	rctx := ports.RequestContext{
		CorrelationID:  uuid.New(),
		IdempotencyKey: differentIdemKey, // different key from original caller
		TenantID:       "rfu-oregon",
		CallerType:     ports.CallerIVR,
	}

	result, err := svc.CancelCard(context.Background(), ports.CancelCardRequest{
		Rctx: rctx, MemberID: &memberID, CancelReason: ports.CancelReasonLost,
	})

	// Layer 2 finds the composite duplicate — should return idempotent success
	if err != nil {
		t.Fatalf("Layer 2 dedup should return success (operation already completed): %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from Layer 2 dedup")
	}
}
