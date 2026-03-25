package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/_shared/domain"
	sharedports "github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/card_member_api/fis_code_connect"
	"github.com/walker-morse/batch/card_member_api/ports"
)

// CardService implements ICardService.
type CardService struct {
	fis   fis_code_connect.IFisCodeConnectPort
	repo  CardRepository
	cmds  sharedports.DomainCommandRepository
	audit sharedports.AuditLogWriter
	obs   sharedports.IObservabilityPort
	log   *slog.Logger
}

func NewCardService(
	fis fis_code_connect.IFisCodeConnectPort,
	repo CardRepository,
	cmds sharedports.DomainCommandRepository,
	audit sharedports.AuditLogWriter,
	obs sharedports.IObservabilityPort,
	log *slog.Logger,
) *CardService {
	return &CardService{fis: fis, repo: repo, cmds: cmds, audit: audit, obs: obs, log: log}
}

var _ ports.ICardService = (*CardService)(nil)

// ─── CancelCard ───────────────────────────────────────────────────────────────

func (s *CardService) CancelCard(ctx context.Context, req ports.CancelCardRequest) (*ports.CancelCardResult, error) {
	if req.MemberID == nil && req.CardToken == nil {
		return nil, fmt.Errorf("%w: exactly one of MemberID or CardToken required", ports.ErrInvalidRequest)
	}
	if req.MemberID != nil && req.CardToken != nil {
		return nil, fmt.Errorf("%w: MemberID and CardToken are mutually exclusive", ports.ErrInvalidRequest)
	}
	if req.CancelReason == "" {
		return nil, fmt.Errorf("%w: CancelReason required", ports.ErrInvalidRequest)
	}

	rctx := req.Rctx
	now := time.Now().UTC()

	// Resolve card and consumer
	consumer, card, err := s.resolveCardIdentity(ctx, req.MemberID, req.CardToken)
	if err != nil {
		return nil, err
	}

	// Idempotency gate
	result, cmdID, err := s.idempotencyGate(ctx, rctx, consumer.ClientMemberID, "CANCEL_CARD", "")
	if err != nil {
		return nil, err
	}
	if result != nil {
		// Already completed — return idempotent success
		return &ports.CancelCardResult{CardID: card.ID, MemberID: consumer.ID, CancelledAt: now}, nil
	}

	// Guard: already cancelled in Aurora
	if card.Status == domain.CardClosed {
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)
		return &ports.CancelCardResult{CardID: card.ID, MemberID: consumer.ID, CancelledAt: now}, nil
	}

	if card.FISCardID == nil {
		reason := "fis_card_id not yet assigned"
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, ports.ErrMemberNotResolved
	}
	fisCardID := fis_code_connect.FisCardID(*card.FISCardID)

	// FIS: close card
	if err := s.fis.CloseCard(ctx, fisCardID, fis_code_connect.CloseCardRequest{
		Comment: fmt.Sprintf("cancel_reason=%s correlation=%s", req.CancelReason, rctx.CorrelationID),
	}); err != nil {
		reason := err.Error()
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("cancel_card: FIS close failed: %w", err)
	}

	// FIS: close all purses
	purses, _ := s.repo.GetPursesByCardID(ctx, card.ID)
	pursesClosed := 0
	for _, p := range purses {
		if p.Status == domain.PurseClosed || p.FISPurseNumber == nil {
			pursesClosed++
			continue
		}
		if err := s.fis.SetPurseStatus(ctx, fisCardID,
			fis_code_connect.FisPurseNumber(*p.FISPurseNumber),
			fis_code_connect.PurseStatusClosed,
		); err != nil {
			reason := fmt.Sprintf("purse %d close failed: %v", *p.FISPurseNumber, err)
			_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
			return nil, fmt.Errorf("cancel_card: partial purse close: %w", err)
		}
		pursesClosed++
	}

	// Aurora writes
	_ = s.repo.SetCardStatus(ctx, card.ID, domain.CardClosed, now)
	for _, p := range purses {
		if p.Status != domain.PurseClosed {
			_ = s.repo.SetPurseStatus(ctx, p.ID, domain.PurseClosed, now)
		}
	}

	// Audit + complete
	_ = s.audit.Write(ctx, &sharedports.AuditEntry{
		TenantID: rctx.TenantID, EntityType: "card", EntityID: card.ID.String(),
		OldState: strPtr(cardStatusName(card.Status)), NewState: "CLOSED",
		ChangedBy: string(rctx.CallerType), CorrelationID: &rctx.CorrelationID,
		ClientMemberID: &consumer.ClientMemberID,
		Notes:          strPtr(fmt.Sprintf("cancel_reason=%s", req.CancelReason)),
	})
	_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)
	_ = s.obs.LogEvent(ctx, &sharedports.LogEvent{
		CorrelationID: rctx.CorrelationID, TenantID: rctx.TenantID, BatchFileID: uuid.Nil,
		EventType: "card.cancel.completed", Level: "INFO", Message: "card cancelled",
		DomainCommandID: &cmdID,
	})

	return &ports.CancelCardResult{CardID: card.ID, MemberID: consumer.ID, CancelledAt: now, PursesClosed: pursesClosed}, nil
}

// ─── ReplaceCard ──────────────────────────────────────────────────────────────

func (s *CardService) ReplaceCard(ctx context.Context, req ports.ReplaceCardRequest) (*ports.ReplaceCardResult, error) {
	now := time.Now().UTC()
	consumer, err := s.repo.GetConsumerByID(ctx, req.MemberID)
	if err != nil {
		return nil, ports.ErrMemberNotFound
	}
	card, err := s.repo.GetCardByMemberID(ctx, req.MemberID)
	if err != nil {
		return nil, ports.ErrCardNotFound
	}
	if card.FISCardID == nil {
		return nil, ports.ErrMemberNotResolved
	}

	_, cmdID, err := s.idempotencyGate(ctx, req.Rctx, consumer.ClientMemberID, "REPLACE_CARD", "")
	if err != nil {
		return nil, err
	}

	fisCardID := fis_code_connect.FisCardID(*card.FISCardID)
	newFisCardID, err := s.fis.ReplaceCard(ctx, fisCardID, fis_code_connect.ReplaceCardRequest{
		ShippingMethod: fis_code_connect.ShipUSPS1stClass,
		Comment:        fmt.Sprintf("reason=%s", req.Reason),
	})
	if err != nil {
		reason := err.Error()
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("replace_card: FIS replace failed: %w", err)
	}

	// Create new card row in Aurora
	newCard := &domain.Card{
		ID:                uuid.New(),
		TenantID:          card.TenantID,
		ConsumerID:        card.ConsumerID,
		ClientMemberID:    card.ClientMemberID,
		FISCardID:         strPtr(string(newFisCardID)),
		Status:            domain.CardReady,
		SourceBatchFileID: card.SourceBatchFileID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	_ = s.repo.CreateCard(ctx, newCard)
	// Mark old card closed
	_ = s.repo.SetCardStatus(ctx, card.ID, domain.CardClosed, now)

	_ = s.audit.Write(ctx, &sharedports.AuditEntry{
		TenantID: req.Rctx.TenantID, EntityType: "card", EntityID: card.ID.String(),
		OldState: strPtr("ACTIVE"), NewState: "REPLACED",
		ChangedBy: string(req.Rctx.CallerType), CorrelationID: &req.Rctx.CorrelationID,
	})
	_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)

	return &ports.ReplaceCardResult{OldCardID: card.ID, NewCardID: newCard.ID, MemberID: req.MemberID, ReplacedAt: now}, nil
}

// ─── LoadFunds ────────────────────────────────────────────────────────────────

func (s *CardService) LoadFunds(ctx context.Context, req ports.LoadFundsRequest) (*ports.LoadFundsResult, error) {
	now := time.Now().UTC()
	consumer, err := s.repo.GetConsumerByID(ctx, req.MemberID)
	if err != nil {
		return nil, ports.ErrMemberNotFound
	}
	card, err := s.repo.GetCardByMemberID(ctx, req.MemberID)
	if err != nil {
		return nil, ports.ErrCardNotFound
	}
	if card.FISCardID == nil {
		return nil, ports.ErrMemberNotResolved
	}
	purse, err := s.repo.GetPurseByBenefitType(ctx, card.ID, req.BenefitType)
	if err != nil || purse.FISPurseNumber == nil {
		return nil, fmt.Errorf("load_funds: purse not found for benefit_type=%s", req.BenefitType)
	}

	_, cmdID, err := s.idempotencyGate(ctx, req.Rctx, consumer.ClientMemberID, "LOAD_FUNDS", req.BenefitPeriod)
	if err != nil {
		return nil, err
	}

	fisCardID := fis_code_connect.FisCardID(*card.FISCardID)
	_, err = s.fis.LoadFunds(ctx, fisCardID, fis_code_connect.LoadFundsRequest{
		PurseNumber:     fis_code_connect.FisPurseNumber(*purse.FISPurseNumber),
		AmountCents:     req.AmountCents,
		ClientReference: req.Rctx.CorrelationID.String(),
	})
	if err != nil {
		reason := err.Error()
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("load_funds: FIS load failed: %w", err)
	}

	_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)
	_ = s.obs.LogEvent(ctx, &sharedports.LogEvent{
		CorrelationID: req.Rctx.CorrelationID, TenantID: req.Rctx.TenantID, BatchFileID: uuid.Nil,
		EventType: "card.load_funds.completed", Level: "INFO", Message: "funds loaded",
	})

	return &ports.LoadFundsResult{CardID: card.ID, PurseID: purse.ID, AmountCents: req.AmountCents, LoadedAt: now}, nil
}

// ─── GetBalance ───────────────────────────────────────────────────────────────

func (s *CardService) GetBalance(ctx context.Context, req ports.GetBalanceRequest) (*ports.GetBalanceResult, error) {
	card, err := s.repo.GetCardByMemberID(ctx, req.MemberID)
	if err != nil {
		return nil, ports.ErrCardNotFound
	}
	if card.FISCardID == nil {
		return nil, ports.ErrMemberNotResolved
	}
	fisPurses, err := s.fis.GetPurses(ctx, fis_code_connect.FisCardID(*card.FISCardID))
	if err != nil {
		return nil, fmt.Errorf("get_balance: FIS purse read failed: %w", err)
	}
	balances := make([]ports.PurseBalance, 0, len(fisPurses))
	for _, p := range fisPurses {
		benefitType := "OTC"
		if len(p.PurseName) >= 3 {
			benefitType = p.PurseName[:3]
		}
		balances = append(balances, ports.PurseBalance{
			BenefitType:    benefitType,
			PurseName:      p.PurseName,
			AvailableCents: p.AvailableBalanceCents,
			SettledCents:   p.SettledBalanceCents,
			Status:         string(p.Status),
		})
	}
	return &ports.GetBalanceResult{MemberID: req.MemberID, CardID: card.ID, Purses: balances, AsOf: time.Now().UTC()}, nil
}

// ─── ResolveCard ─────────────────────────────────────────────────────────────

func (s *CardService) ResolveCard(ctx context.Context, req ports.ResolveCardRequest) (*ports.ResolveCardResult, error) {
	fisCardID, err := s.fis.TranslateCardNumber(ctx, req.Token)
	if err != nil {
		// Try proxy fallback
		fisCardID, err = s.fis.TranslateProxy(ctx, req.Token, 0)
		if err != nil {
			return nil, fmt.Errorf("resolve_card: token not found: %w", ports.ErrCardNotFound)
		}
	}
	card, err := s.repo.GetCardByFisCardID(ctx, string(fisCardID))
	if err != nil {
		return nil, ports.ErrCardNotFound
	}
	return &ports.ResolveCardResult{
		MemberID: card.ConsumerID,
		CardID:   card.ID,
		Status:   cardStatusName(card.Status),
	}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *CardService) resolveCardIdentity(ctx context.Context, memberID *uuid.UUID, cardToken *string) (*domain.Consumer, *domain.Card, error) {
	if memberID != nil {
		consumer, err := s.repo.GetConsumerByID(ctx, *memberID)
		if err != nil {
			return nil, nil, ports.ErrMemberNotFound
		}
		card, err := s.repo.GetCardByMemberID(ctx, *memberID)
		if err != nil {
			return nil, nil, ports.ErrCardNotFound
		}
		return consumer, card, nil
	}
	// IVR token path
	fisCardID, err := s.fis.TranslateCardNumber(ctx, *cardToken)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve token: %w", ports.ErrCardNotFound)
	}
	card, err := s.repo.GetCardByFisCardID(ctx, string(fisCardID))
	if err != nil {
		return nil, nil, ports.ErrCardNotFound
	}
	consumer, err := s.repo.GetConsumerByID(ctx, card.ConsumerID)
	if err != nil {
		return nil, nil, ports.ErrMemberNotFound
	}
	return consumer, card, nil
}

// idempotencyGate checks for existing commands and inserts a new Accepted row.
// Returns (non-nil result, uuid.Nil, nil) if already Completed (idempotent replay).
// Returns (nil, cmdID, nil) if new — proceed with operation.
// Returns (nil, uuid.Nil, err) if Accepted in-flight (duplicate) or DB error.
func (s *CardService) idempotencyGate(ctx context.Context, rctx ports.RequestContext, clientMemberID, cmdType, benefitPeriod string) (*struct{}, uuid.UUID, error) {
	dup, err := s.cmds.FindDuplicate(ctx, rctx.TenantID, clientMemberID, cmdType, benefitPeriod)
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("idempotency check: %w", err)
	}
	if dup != nil {
		switch dup.Status {
		case string(domain.CommandCompleted):
			return &struct{}{}, uuid.Nil, nil
		case string(domain.CommandAccepted):
			return nil, uuid.Nil, ports.ErrDuplicateRequest
		}
		// Failed — allow retry
	}
	cmdID := uuid.New()
	if err := s.cmds.Insert(ctx, &sharedports.DomainCommand{
		ID: cmdID, CorrelationID: rctx.IdempotencyKey,
		TenantID: rctx.TenantID, ClientMemberID: clientMemberID,
		CommandType: cmdType, BenefitPeriod: benefitPeriod,
		Status: string(domain.CommandAccepted), BatchFileID: uuid.Nil,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return nil, uuid.Nil, fmt.Errorf("insert command: %w", err)
	}
	return nil, cmdID, nil
}

func strPtr(s string) *string { return &s }

func cardStatusName(s domain.CardStatus) string {
	m := map[domain.CardStatus]string{
		domain.CardReady: "READY", domain.CardActive: "ACTIVE",
		domain.CardLost: "LOST", domain.CardSuspended: "SUSPENDED", domain.CardClosed: "CLOSED",
	}
	if n, ok := m[s]; ok {
		return n
	}
	return fmt.Sprintf("UNKNOWN(%d)", s)
}
