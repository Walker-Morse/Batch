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

// MemberService implements IMemberService.
//
// EnrollMember compound operation:
//  1. Idempotency gate (ENROLL_MEMBER)
//  2. OFAC check (sanctions_screening — called externally before this service)
//  3. Create consumer row in Aurora (status=ACTIVE)
//  4. POST /persons → GET /persons to resolve personId
//  5. Create card row in Aurora (status=READY, fisCardID=nil)
//  6. POST /cards (personID required — typed arg enforces this) → resolve cardId
//  7. Store fisPersonID + fisCardID in Aurora
//  8. Seed purse rows from FIS purse list
//  9. Audit log + mark Completed
//
// Orphan recovery on retry:
//   - If consumer row exists but fisPersonID is nil → skip step 3, retry step 4
//   - If fisPersonID set but card row has no fisCardID → skip steps 3-5, retry step 6
//   - If fisCardID set → all FIS calls done; only Aurora/audit steps remain
type MemberService struct {
	fis          fis_code_connect.IFisCodeConnectPort
	repo         CardRepository
	cmds         sharedports.DomainCommandRepository
	audit        sharedports.AuditLogWriter
	obs          sharedports.IObservabilityPort
	log          *slog.Logger
	fisClientID  int32 // FIS client ID from program config — injected at construction
	fisSubprogID int32 // FIS subprogram ID (e.g. 26071 for RFU) — from program config
	fisPackageID int32 // FIS package ID — from Selvi ACC upload
}

func NewMemberService(
	fis fis_code_connect.IFisCodeConnectPort,
	repo CardRepository,
	cmds sharedports.DomainCommandRepository,
	audit sharedports.AuditLogWriter,
	obs sharedports.IObservabilityPort,
	log *slog.Logger,
	fisClientID, fisSubprogID, fisPackageID int32,
) *MemberService {
	return &MemberService{
		fis: fis, repo: repo, cmds: cmds, audit: audit, obs: obs, log: log,
		fisClientID: fisClientID, fisSubprogID: fisSubprogID, fisPackageID: fisPackageID,
	}
}

var _ ports.IMemberService = (*MemberService)(nil)

// ─── EnrollMember ─────────────────────────────────────────────────────────────

func (s *MemberService) EnrollMember(ctx context.Context, req ports.EnrollMemberRequest) (*ports.EnrollMemberResult, error) {
	if req.ClientMemberID == "" {
		return nil, fmt.Errorf("%w: ClientMemberID required", ports.ErrInvalidRequest)
	}
	if req.BenefitPeriod == "" {
		return nil, fmt.Errorf("%w: BenefitPeriod required", ports.ErrInvalidRequest)
	}

	rctx := req.Rctx
	now := time.Now().UTC()

	// ── Idempotency gate ──────────────────────────────────────────────────────
	cs := &CardService{cmds: s.cmds}
	completed, cmdID, err := cs.idempotencyGate(ctx, rctx, req.ClientMemberID, "ENROLL_MEMBER", req.BenefitPeriod)
	if err != nil {
		return nil, err
	}
	if completed != nil {
		// Already enrolled — find and return existing member
		existing, err := s.repo.GetConsumerByClientMemberID(ctx, rctx.TenantID, req.ClientMemberID)
		if err != nil {
			return nil, ports.ErrMemberNotFound
		}
		existingCard, _ := s.repo.GetCardByMemberID(ctx, existing.ID)
		result := &ports.EnrollMemberResult{MemberID: existing.ID, FISResolved: existing.FISPersonID != nil, CreatedAt: existing.CreatedAt}
		if existingCard != nil {
			result.CardID = existingCard.ID
			result.FISResolved = existingCard.FISCardID != nil
		}
		return result, nil
	}

	// ── Step 1: Resolve or create consumer row ────────────────────────────────
	consumer, err := s.repo.GetConsumerByClientMemberID(ctx, rctx.TenantID, req.ClientMemberID)
	if err != nil {
		// New consumer
		consumer = &domain.Consumer{
			ID: uuid.New(), TenantID: rctx.TenantID,
			ClientMemberID: req.ClientMemberID,
			Status:         domain.ConsumerActive,
			FirstName:      req.FirstName, LastName: req.LastName,
			DOB:            req.DOB, Address1: req.Address1, Address2: &req.Address2,
			City: req.City, State: req.State, ZIP: req.ZIP,
			Email: &req.Email,
			SubprogramID: req.SubprogramID,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.repo.CreateConsumer(ctx, consumer); err != nil {
			reason := fmt.Sprintf("create consumer: %v", err)
			_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
			return nil, fmt.Errorf("enroll_member: %w", err)
		}
	}

	// ── Step 2: FIS CreatePerson (skip if already resolved) ───────────────────
	var fisPersonID fis_code_connect.FisPersonID
	fisPersonResolved := consumer.FISPersonID != nil
	if !fisPersonResolved {
		fisPersonID, err = s.fis.CreatePerson(ctx, fis_code_connect.CreatePersonRequest{
			ClientID:         s.fisClientID,
			ClientUniqueID:   req.ClientMemberID,
			FirstName:        req.FirstName,
			LastName:         req.LastName,
			DOB:              req.DOB.Format("2006-01-02"),
			MailingLine1:     req.Address1,
			MailingLine2:     req.Address2,
			MailingCity:      req.City,
			MailingState:     req.State,
			MailingZIP:       req.ZIP,
			CountryAlphaCode: "USA",
			Phone:            req.Phone,
			Email:            req.Email,
		})
		if err != nil {
			reason := fmt.Sprintf("FIS CreatePerson: %v", err)
			_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
			return nil, fmt.Errorf("enroll_member: FIS person creation failed: %w", err)
		}
		fisPersonResolved = true
		fisPersonIDStr := fmt.Sprintf("%d", fisPersonID)
		if err := s.repo.SetConsumerFISIDs(ctx, consumer.ID, fisPersonIDStr, nil); err != nil {
			s.log.ErrorContext(ctx, "failed to store fisPersonID — will orphan on retry",
				slog.String("consumer_id", consumer.ID.String()),
				slog.Int("fis_person_id", int(fisPersonID)),
			)
		}
	} else {
		// Parse stored personID back to FisPersonID type
		var pid int32
		fmt.Sscanf(*consumer.FISPersonID, "%d", &pid)
		fisPersonID = fis_code_connect.FisPersonID(pid)
	}

	// ── Step 3: Resolve or create card row ────────────────────────────────────
	card, cardErr := s.repo.GetCardByMemberID(ctx, consumer.ID)
	if cardErr != nil {
		card = &domain.Card{
			ID: uuid.New(), TenantID: rctx.TenantID,
			ConsumerID: consumer.ID, ClientMemberID: consumer.ClientMemberID,
			Status: domain.CardReady, SourceBatchFileID: uuid.Nil,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.repo.CreateCard(ctx, card); err != nil {
			reason := fmt.Sprintf("create card: %v", err)
			_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
			return nil, fmt.Errorf("enroll_member: %w", err)
		}
	}

	// ── Step 4: FIS IssueCard (skip if already resolved) ─────────────────────
	fisCardResolved := card.FISCardID != nil
	if !fisCardResolved {
		fisCardID, err := s.fis.IssueCard(ctx, fisPersonID, fis_code_connect.IssueCardRequest{
			ClientID: s.fisClientID, SubprogramID: s.fisSubprogID,
			PackageID: s.fisPackageID,
			Comment:   fmt.Sprintf("enroll correlation=%s", rctx.CorrelationID),
		})
		if err != nil {
			reason := fmt.Sprintf("FIS IssueCard: %v", err)
			_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandFailed), &reason)
			return nil, fmt.Errorf("enroll_member: FIS card issuance failed: %w", err)
		}
		fisCardResolved = true
		if err := s.repo.SetCardFISID(ctx, card.ID, string(fisCardID)); err != nil {
			s.log.ErrorContext(ctx, "failed to store fisCardID after IssueCard",
				slog.String("card_id", card.ID.String()),
				slog.String("fis_card_id", string(fisCardID)),
			)
		}

		// Seed purse rows from FIS
		fisPurses, _ := s.fis.GetPurses(ctx, fisCardID)
		for _, fp := range fisPurses {
			purseNum := int16(fp.PurseNumber)
			benefitType := "OTC"
			if len(fp.PurseName) >= 3 {
				benefitType = fp.PurseName[:3]
			}
			_ = s.repo.CreatePurse(ctx, &domain.Purse{
				ID: uuid.New(), TenantID: rctx.TenantID,
				CardID: card.ID, ConsumerID: consumer.ID,
				ClientMemberID: consumer.ClientMemberID,
				FISPurseNumber: &purseNum,
				FISPurseName:   fp.PurseName,
				PurseType:      benefitType,
				Status:         domain.PurseActive,
				BenefitPeriod:  req.BenefitPeriod,
				EffectiveDate:  fp.EffectiveDate,
				ExpiryDate:     fp.ExpirationDate,
				BenefitType:    domain.BenefitType(benefitType),
				CreatedAt:      now, UpdatedAt: now,
			})
		}
	}

	// ── Audit + complete ──────────────────────────────────────────────────────
	_ = s.audit.Write(ctx, &sharedports.AuditEntry{
		TenantID: rctx.TenantID, EntityType: "consumer", EntityID: consumer.ID.String(),
		NewState: "ACTIVE", ChangedBy: string(rctx.CallerType),
		CorrelationID: &rctx.CorrelationID, ClientMemberID: &consumer.ClientMemberID,
		Notes: strPtr(fmt.Sprintf("enrolled benefit_period=%s", req.BenefitPeriod)),
	})
	_ = s.cmds.UpdateStatus(ctx, cmdID, string(domain.CommandCompleted), nil)
	_ = s.obs.LogEvent(ctx, &sharedports.LogEvent{
		CorrelationID: rctx.CorrelationID, TenantID: rctx.TenantID, BatchFileID: uuid.Nil,
		EventType: "member.enroll.completed", Level: "INFO", Message: "member enrolled",
	})

	return &ports.EnrollMemberResult{
		MemberID: consumer.ID, CardID: card.ID,
		FISResolved: fisPersonResolved && fisCardResolved, CreatedAt: consumer.CreatedAt,
	}, nil
}

// ─── UpdateMember ─────────────────────────────────────────────────────────────

func (s *MemberService) UpdateMember(ctx context.Context, req ports.UpdateMemberRequest) error {
	consumer, err := s.repo.GetConsumerByID(ctx, req.MemberID)
	if err != nil {
		return ports.ErrMemberNotFound
	}

	// Aurora update
	update := DemographicsUpdate{
		FirstName: req.FirstName, LastName: req.LastName,
		Address1: req.Address1, Address2: req.Address2,
		City: req.City, State: req.State, ZIP: req.ZIP,
		Phone: req.Phone, Email: req.Email,
	}
	if !req.DOB.IsZero() {
		update.DOB = &req.DOB
	}
	if err := s.repo.UpdateConsumerDemographics(ctx, req.MemberID, update); err != nil {
		return fmt.Errorf("update_member: aurora write failed: %w", err)
	}

	// FIS update (only if resolved)
	if consumer.FISPersonID != nil {
		var pid int32
		fmt.Sscanf(*consumer.FISPersonID, "%d", &pid)
		fisPersonID := fis_code_connect.FisPersonID(pid)
		fisReq := fis_code_connect.UpdatePersonRequest{
			ClientID:         s.fisClientID,
			FirstName:        req.FirstName, LastName: req.LastName,
			MailingLine1: req.Address1, MailingLine2: req.Address2,
			MailingCity: req.City, MailingState: req.State, MailingZIP: req.ZIP,
			CountryAlphaCode: "USA",
			Phone: req.Phone, Email: req.Email,
		}
		if !req.DOB.IsZero() {
			fisReq.DOB = req.DOB.Format("2006-01-02")
		}
		if err := s.fis.UpdatePerson(ctx, fisPersonID, fisReq); err != nil {
			// Non-fatal: Aurora is updated; FIS will be reconciled on next full sync
			s.log.WarnContext(ctx, "FIS UpdatePerson failed — Aurora updated, FIS stale",
				slog.String("member_id", req.MemberID.String()),
				slog.String("error", err.Error()),
			)
		}
	}

	_ = s.audit.Write(ctx, &sharedports.AuditEntry{
		TenantID: req.Rctx.TenantID, EntityType: "consumer", EntityID: req.MemberID.String(),
		NewState: "UPDATED", ChangedBy: string(req.Rctx.CallerType),
		CorrelationID: &req.Rctx.CorrelationID, ClientMemberID: &consumer.ClientMemberID,
	})
	return nil
}

// ─── GetMember ────────────────────────────────────────────────────────────────

func (s *MemberService) GetMember(ctx context.Context, _ ports.RequestContext, memberID uuid.UUID) (*ports.MemberView, error) {
	consumer, err := s.repo.GetConsumerByID(ctx, memberID)
	if err != nil {
		return nil, ports.ErrMemberNotFound
	}
	view := &ports.MemberView{
		MemberID:       consumer.ID,
		ClientMemberID: consumer.ClientMemberID,
		Status:         string(consumer.Status),
		FISResolved:    consumer.FISPersonID != nil,
		CreatedAt:      consumer.CreatedAt,
		UpdatedAt:      consumer.UpdatedAt,
	}
	card, err := s.repo.GetCardByMemberID(ctx, memberID)
	if err == nil {
		view.CardID = &card.ID
		statusStr := cardStatusName(card.Status)
		view.CardStatus = &statusStr
		view.FISResolved = view.FISResolved && card.FISCardID != nil
	}
	return view, nil
}
