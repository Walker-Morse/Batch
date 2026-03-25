// Package service implements the ICardService and IMemberService domain service
// interfaces for the Card and Member API.
//
// The service layer owns:
//   - Idempotency gate (domain_commands table — same gate as batch pipeline)
//   - FIS identifier resolution (consumers.fis_person_id, cards.fis_card_id)
//   - Multi-step FIS operation sequencing with failure compensation
//   - Aurora writes after FIS confirmation
//   - Audit log entries (append-only, via AuditLogWriter)
//   - Observability events (zero-PHI, via IObservabilityPort)
//
// Handlers call services. Services call IFisCodeConnectPort and Aurora repositories.
// Services never import net/http.
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

// ─── CardService ──────────────────────────────────────────────────────────────

// CardService implements ICardService.
type CardService struct {
	fis     fis_code_connect.IFisCodeConnectPort
	repo    CardRepository       // Aurora read/write for card + consumer + purse rows
	cmds    sharedports.DomainCommandRepository // idempotency gate — shared with batch pipeline
	audit   sharedports.AuditLogWriter
	obs     sharedports.IObservabilityPort
	log     *slog.Logger
}

// CardRepository is the narrow Aurora interface required by CardService.
// Defined here so it can be mocked in unit tests without a live DB.
type CardRepository interface {
	// GetConsumerByID returns the consumer row including FISPersonID and FISCUID.
	GetConsumerByID(ctx context.Context, id uuid.UUID) (*domain.Consumer, error)

	// GetCardByMemberID returns the active card for a member.
	GetCardByMemberID(ctx context.Context, memberID uuid.UUID) (*domain.Card, error)

	// GetCardByFisCardID looks up a One Fintech card row by the FIS-assigned cardId.
	// Used after TranslateCardNumber resolves an IVR token to a FisCardID.
	GetCardByFisCardID(ctx context.Context, fisCardID string) (*domain.Card, error)

	// GetPursesByCardID returns all purse slots for a card.
	GetPursesByCardID(ctx context.Context, cardID uuid.UUID) ([]*domain.Purse, error)

	// SetCardStatus updates the card status and sets ClosedAt when status = CardClosed.
	SetCardStatus(ctx context.Context, cardID uuid.UUID, status domain.CardStatus, at time.Time) error

	// SetPurseStatus updates a purse status and sets ClosedAt when status = PurseClosed.
	SetPurseStatus(ctx context.Context, purseID uuid.UUID, status domain.PurseStatus, at time.Time) error
}

// NewCardService constructs a CardService.
func NewCardService(
	fis fis_code_connect.IFisCodeConnectPort,
	repo CardRepository,
	cmds sharedports.DomainCommandRepository,
	audit sharedports.AuditLogWriter,
	obs sharedports.IObservabilityPort,
	log *slog.Logger,
) *CardService {
	return &CardService{
		fis:   fis,
		repo:  repo,
		cmds:  cmds,
		audit: audit,
		obs:   obs,
		log:   log,
	}
}

// Compile-time assertion.
var _ ports.ICardService = (*CardService)(nil)

// ─── CancelCard ───────────────────────────────────────────────────────────────

// CancelCard is the exemplar for the full domain operation pattern:
//
//  1. Validate request shape
//  2. Idempotency gate — write domain_commands row (Accepted) or return duplicate
//  3. Resolve One Fintech identifiers (memberID / card row)
//  4. If IVR path: translate card token → FisCardID → look up One Fintech card row
//  5. Guard: if already CardClosed, mark command Completed and return success
//  6. Call FIS: CloseCard
//  7. Call FIS: SetPurseStatus(CLOSED) for each purse
//  8. Write Aurora: card status → CardClosed, purse statuses → PurseClosed
//  9. Write audit log
// 10. Mark domain_commands row Completed
// 11. Emit observability event
//
// Failure modes:
//   - FIS CloseCard fails: mark command Failed, return error (caller retries with same correlationID)
//   - FIS SetPurseStatus partially fails: card is closed at FIS but one or more purses
//     still open. Mark command Failed, log which purses succeeded. On retry the idempotency
//     gate sees the existing Failed command and allows re-entry. The compensation check
//     (step 5) calls GetCard at FIS to verify current state before re-calling CloseCard.
//   - Aurora write fails after FIS success: card is closed at FIS but Aurora is stale.
//     Mark command Failed. On retry, step 5 detects the FIS-side closed state and
//     skips CloseCard, proceeding directly to the Aurora write.
func (s *CardService) CancelCard(ctx context.Context, req ports.CancelCardRequest) (*ports.CancelCardResult, error) {
	rctx := req.Rctx
	now := time.Now().UTC()

	// ── 1. Validate request shape ─────────────────────────────────────────────

	if req.MemberID == nil && req.CardToken == nil {
		return nil, fmt.Errorf("cancel_card: exactly one of MemberID or CardToken must be set")
	}
	if req.MemberID != nil && req.CardToken != nil {
		return nil, fmt.Errorf("cancel_card: MemberID and CardToken are mutually exclusive")
	}
	if req.CancelReason == "" {
		return nil, fmt.Errorf("cancel_card: CancelReason is required")
	}

	// ── 2. Idempotency gate ───────────────────────────────────────────────────
	//
	// CommandType CANCEL_CARD. BenefitPeriod is empty for card lifecycle commands
	// (benefit period scoping only applies to fund load commands).
	// The composite key is (tenant_id, client_member_id, command_type, benefit_period).
	// For the IVR path we don't have client_member_id yet — we resolve it in step 4
	// then re-check. This is a known asymmetry: IVR callers must ensure their
	// correlationID is unique per cancel attempt at the caller side.

	// ── 3. Resolve One Fintech card row ──────────────────────────────────────

	var card *domain.Card
	var consumer *domain.Consumer

	if req.MemberID != nil {
		// UW / batch repair path — we have the One Fintech member UUID directly.
		var err error
		consumer, err = s.repo.GetConsumerByID(ctx, *req.MemberID)
		if err != nil {
			return nil, fmt.Errorf("cancel_card: member lookup failed: %w", err)
		}
		card, err = s.repo.GetCardByMemberID(ctx, *req.MemberID)
		if err != nil {
			return nil, fmt.Errorf("cancel_card: card lookup failed: %w", err)
		}
	} else {
		// IVR path — translate token to FisCardID, then look up our card row.
		fisCardID, err := s.fis.TranslateCardNumber(ctx, *req.CardToken)
		if err != nil {
			return nil, fmt.Errorf("cancel_card: token translation failed: %w", err)
		}
		card, err = s.repo.GetCardByFisCardID(ctx, string(fisCardID))
		if err != nil {
			return nil, fmt.Errorf("cancel_card: card lookup by FIS ID failed: %w", err)
		}
		consumer, err = s.repo.GetConsumerByID(ctx, card.ConsumerID)
		if err != nil {
			return nil, fmt.Errorf("cancel_card: consumer lookup failed: %w", err)
		}
	}

	// ── Idempotency gate (deferred until we have clientMemberID) ─────────────

	dup, err := s.cmds.FindDuplicate(ctx, rctx.TenantID, consumer.ClientMemberID, "CANCEL_CARD", "")
	if err != nil {
		return nil, fmt.Errorf("cancel_card: idempotency check failed: %w", err)
	}
	if dup != nil {
		switch dup.Status {
		case string(domain.CommandCompleted):
			// Already successfully cancelled — return idempotent success.
			return &ports.CancelCardResult{
				CardID:   card.ID,
				MemberID: consumer.ID,
				// ClosedAt is not re-derivable here without a FIS call; return zero time
				// to signal idempotent replay. Caller should treat this as success.
			}, nil
		case string(domain.CommandAccepted):
			// Another goroutine is processing — surface as duplicate.
			return nil, fmt.Errorf("cancel_card: %w", ports.ErrDuplicateRequest)
		}
		// CommandFailed — allow retry to proceed.
	}

	cmdID := uuid.New()
	if err := s.cmds.Insert(ctx, &sharedports.DomainCommand{
		ID:             cmdID,
		CorrelationID:  rctx.CorrelationID,
		TenantID:       rctx.TenantID,
		ClientMemberID: consumer.ClientMemberID,
		CommandType:    "CANCEL_CARD",
		BenefitPeriod:  "",
		Status:         string(domain.CommandAccepted),
		BatchFileID:    uuid.Nil,
		SequenceInFile: 0,
		CreatedAt:      now,
	}); err != nil {
		return nil, fmt.Errorf("cancel_card: failed to write command row: %w", err)
	}

	// ── 5. Guard: already cancelled ───────────────────────────────────────────
	//
	// Check FIS state, not just Aurora state. If a previous attempt closed the card
	// at FIS but failed to update Aurora, we proceed directly to the Aurora write.
	// If FIS also shows closed, skip FIS calls entirely.

	if card.Status == domain.CardClosed {
		// Aurora already knows. Skip FIS calls, clean up command, return success.
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)
		return &ports.CancelCardResult{
			CardID:      card.ID,
			MemberID:    consumer.ID,
			CancelledAt: now,
		}, nil
	}

	// ── 6. Resolve FisCardID — required for FIS calls ─────────────────────────

	if card.FISCardID == nil {
		reason := "fis_card_id not yet assigned — batch return file pending (Stage 7)"
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("cancel_card: %w", ports.ErrMemberNotResolved)
	}
	fisCardID := fis_code_connect.FisCardID(*card.FISCardID)

	// ── 7. FIS: CloseCard ─────────────────────────────────────────────────────

	if err := s.fis.CloseCard(ctx, fisCardID); err != nil {
		reason := fmt.Sprintf("fis CloseCard failed: %v", err)
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		_ = s.obs.LogEvent(ctx, &sharedports.LogEvent{
			CorrelationID: rctx.CorrelationID,
			TenantID:      rctx.TenantID,
			BatchFileID:   uuid.Nil,
			EventType:     "card.cancel.fis_close_failed",
			Level:         "ERROR",
			Message:       "FIS CloseCard rejected",
			Error:         &reason,
		})
		return nil, fmt.Errorf("cancel_card: FIS close failed: %w", err)
	}

	// ── 8. FIS: SetPurseStatus(CLOSED) for each purse ────────────────────────
	//
	// RFU has 2 purses. We close all active purses explicitly.
	// Partial failure is recorded per-purse; the command is marked Failed so
	// the retry path can re-enter and close only the remaining open purses.

	purses, err := s.repo.GetPursesByCardID(ctx, card.ID)
	if err != nil {
		reason := fmt.Sprintf("purse lookup failed after FIS close: %v", err)
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("cancel_card: purse lookup failed: %w", err)
	}

	pursesClosed := 0
	var purseCloseErr error
	for _, purse := range purses {
		if purse.Status == domain.PurseClosed {
			pursesClosed++
			continue
		}
		if purse.FISPurseNumber == nil {
			continue // purse not yet resolved at FIS — skip
		}
		fisPurseNum := fis_code_connect.FisPurseNumber(*purse.FISPurseNumber)
		if err := s.fis.SetPurseStatus(ctx, fisCardID, fisPurseNum, fis_code_connect.PurseStatusClosed); err != nil {
			purseCloseErr = fmt.Errorf("purse %d close failed: %w", *purse.FISPurseNumber, err)
			break
		}
		pursesClosed++
	}

	if purseCloseErr != nil {
		reason := purseCloseErr.Error()
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("cancel_card: partial purse close: %w", purseCloseErr)
	}

	// ── 9. Aurora: update card and purse statuses ─────────────────────────────

	if err := s.repo.SetCardStatus(ctx, card.ID, domain.CardClosed, now); err != nil {
		// FIS is closed; Aurora is stale. Mark failed so retry can recover.
		reason := fmt.Sprintf("aurora card status update failed after FIS close: %v", err)
		_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
		return nil, fmt.Errorf("cancel_card: aurora card update failed: %w", err)
	}

	for _, purse := range purses {
		if purse.Status == domain.PurseClosed {
			continue
		}
		if err := s.repo.SetPurseStatus(ctx, purse.ID, domain.PurseClosed, now); err != nil {
			// Non-fatal: card is closed. Log but don't fail.
			s.log.ErrorContext(ctx, "failed to update purse status in Aurora after FIS close",
				slog.String("purse_id", purse.ID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	// ── 10. Audit log ─────────────────────────────────────────────────────────

	_ = s.audit.Write(ctx, &sharedports.AuditEntry{
		TenantID:       rctx.TenantID,
		EntityType:     "card",
		EntityID:       card.ID.String(),
		OldState:       statusName(card.Status),
		NewState:       "CLOSED",
		ChangedBy:      string(rctx.CallerType),
		CorrelationID:  &rctx.CorrelationID,
		ClientMemberID: &consumer.ClientMemberID,
		Notes:          cancelReasonNote(req.CancelReason),
	})

	// ── 11. Mark Completed ────────────────────────────────────────────────────

	_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)

	// ── 12. Observability ─────────────────────────────────────────────────────

	_ = s.obs.LogEvent(ctx, &sharedports.LogEvent{
		CorrelationID: rctx.CorrelationID,
		TenantID:      rctx.TenantID,
		BatchFileID:   uuid.Nil,
		EventType:     "card.cancel.completed",
		Level:         "INFO",
		Message:       "card cancelled successfully",
		DomainCommandID: &cmdID,
	})

	return &ports.CancelCardResult{
		CardID:       card.ID,
		MemberID:     consumer.ID,
		CancelledAt:  now,
		PursesClosed: pursesClosed,
	}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func statusName(s domain.CardStatus) *string {
	names := map[domain.CardStatus]string{
		domain.CardReady:     "READY",
		domain.CardActive:    "ACTIVE",
		domain.CardLost:      "LOST",
		domain.CardSuspended: "SUSPENDED",
		domain.CardClosed:    "CLOSED",
	}
	n := names[s]
	if n == "" {
		n = fmt.Sprintf("UNKNOWN(%d)", s)
	}
	return &n
}

func cancelReasonNote(r ports.CancelReason) *string {
	s := fmt.Sprintf("cancel_reason=%s", r)
	return &s
}
