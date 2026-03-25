package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/card_member_api/fis_code_connect/mock"
	"github.com/walker-morse/batch/card_member_api/ports"
	"github.com/walker-morse/batch/card_member_api/service"
)

func newMemberSvc(repo *mockRepo, cmds *mockCmds, fisMock *mock.FisCodeConnectMock) ports.IMemberService {
	return service.NewMemberService(
		fisMock, repo, cmds, &mockAudit{}, &mockObs{}, noopLog(),
		100, 26071, 456,
	)
}

// ─── EnrollMember: happy path ─────────────────────────────────────────────────

func TestEnrollMember_HappyPath(t *testing.T) {
	fisMock := mock.NewFisCodeConnectMock()
	repo := &mockRepo{} // empty — no pre-existing records
	cmds := &mockCmds{}

	svc := newMemberSvc(repo, cmds, fisMock)

	result, err := svc.EnrollMember(context.Background(), ports.EnrollMemberRequest{
		Rctx:           newRctx(),
		ClientMemberID: "MBR-NEW-001",
		SubprogramID:   26071,
		BenefitPeriod:  "2026-06",
		FirstName:      "Jane",
		LastName:       "Smith",
		DOB:            time.Date(1985, 3, 15, 0, 0, 0, 0, time.UTC),
		Address1:       "123 Main St",
		City:           "Portland",
		State:          "OR",
		ZIP:            "97201",
		Phone:          "5031234567",
		Email:          "jane@example.com",
	})

	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result.MemberID == uuid.Nil {
		t.Error("expected non-nil MemberID")
	}
	if result.CardID == uuid.Nil {
		t.Error("expected non-nil CardID")
	}
	if !result.FISResolved {
		t.Error("expected FISResolved=true after synchronous enrollment")
	}
	if fisMock.Calls["CreatePerson"] != 1 {
		t.Errorf("expected 1 CreatePerson, got %d", fisMock.Calls["CreatePerson"])
	}
	if fisMock.Calls["IssueCard"] != 1 {
		t.Errorf("expected 1 IssueCard, got %d", fisMock.Calls["IssueCard"])
	}
	if cmds.completedCount != 1 {
		t.Errorf("expected command Completed, got %d", cmds.completedCount)
	}
}

// ─── EnrollMember: idempotent replay ─────────────────────────────────────────

func TestEnrollMember_IdempotentReplay(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	fisMock := mock.NewFisCodeConnectMock()

	repo := &mockRepo{consumer: consumer, card: card}
	cmds := &mockCmds{existingStatus: string(domain.CommandCompleted)}

	svc := newMemberSvc(repo, cmds, fisMock)

	result, err := svc.EnrollMember(context.Background(), ports.EnrollMemberRequest{
		Rctx: newRctx(), ClientMemberID: "MBR-001",
		SubprogramID: 26071, BenefitPeriod: "2026-06",
		FirstName: "Jane", LastName: "Smith",
		DOB: time.Now(), Address1: "123 Main", City: "Portland",
		State: "OR", ZIP: "97201", Phone: "5031234567",
	})

	if err != nil {
		t.Fatalf("idempotent replay error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if fisMock.Calls["CreatePerson"] != 0 {
		t.Error("CreatePerson must NOT be called on idempotent replay")
	}
	if fisMock.Calls["IssueCard"] != 0 {
		t.Error("IssueCard must NOT be called on idempotent replay")
	}
}

// ─── EnrollMember: orphan recovery — person created, card not ────────────────

func TestEnrollMember_OrphanRecovery_PersonCreatedCardNot(t *testing.T) {
	fisPersonIDStr := "1001"
	consumer := &domain.Consumer{
		ID: uuid.New(), TenantID: "rfu-oregon",
		ClientMemberID: "MBR-ORPHAN", Status: domain.ConsumerActive,
		FISPersonID: &fisPersonIDStr, // person already at FIS
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	// No card row yet — GetCardByMemberID returns nil
	repo := &mockRepo{consumer: consumer, card: nil}
	cmds := &mockCmds{}
	fisMock := mock.NewFisCodeConnectMock()

	svc := newMemberSvc(repo, cmds, fisMock)

	result, err := svc.EnrollMember(context.Background(), ports.EnrollMemberRequest{
		Rctx: newRctx(), ClientMemberID: "MBR-ORPHAN",
		SubprogramID: 26071, BenefitPeriod: "2026-06",
		FirstName: "Orphan", LastName: "Recovery",
		DOB: time.Now(), Address1: "1 Orphan Ln", City: "Portland",
		State: "OR", ZIP: "97201", Phone: "5030000000",
	})

	if err != nil {
		t.Fatalf("orphan recovery error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if fisMock.Calls["CreatePerson"] != 0 {
		t.Errorf("CreatePerson must be SKIPPED for orphan recovery, got %d calls", fisMock.Calls["CreatePerson"])
	}
	if fisMock.Calls["IssueCard"] != 1 {
		t.Errorf("IssueCard must be called for orphan recovery, got %d calls", fisMock.Calls["IssueCard"])
	}
}

// ─── EnrollMember: FIS CreatePerson fails ────────────────────────────────────

func TestEnrollMember_FISCreatePersonFails(t *testing.T) {
	fisMock := mock.NewFisCodeConnectMock()
	fisMock.InjectError("CreatePerson", errors.New("fis: service unavailable"))

	repo := &mockRepo{} // empty
	cmds := &mockCmds{}

	svc := newMemberSvc(repo, cmds, fisMock)

	_, err := svc.EnrollMember(context.Background(), ports.EnrollMemberRequest{
		Rctx: newRctx(), ClientMemberID: "MBR-FAIL",
		SubprogramID: 26071, BenefitPeriod: "2026-06",
		FirstName: "Fail", LastName: "Test",
		DOB: time.Now(), Address1: "1 Fail St", City: "Portland",
		State: "OR", ZIP: "97201", Phone: "5030000001",
	})

	if err == nil {
		t.Fatal("expected error when CreatePerson fails")
	}
	if cmds.failedCount != 1 {
		t.Errorf("expected command marked Failed, got %d", cmds.failedCount)
	}
}

// ─── EnrollMember: validation ─────────────────────────────────────────────────

func TestEnrollMember_ValidationError_MissingClientMemberID(t *testing.T) {
	svc := newMemberSvc(&mockRepo{}, &mockCmds{}, mock.NewFisCodeConnectMock())

	_, err := svc.EnrollMember(context.Background(), ports.EnrollMemberRequest{
		Rctx: newRctx(), BenefitPeriod: "2026-06",
	})

	if !errors.Is(err, ports.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got: %v", err)
	}
}

// ─── UpdateMember: Aurora always updated, FIS best-effort ────────────────────

func TestUpdateMember_AuroraUpdatedEvenWhenFISFails(t *testing.T) {
	consumer := resolvedConsumer()
	fisMock := mock.NewFisCodeConnectMock()
	fisMock.InjectError("UpdatePerson", errors.New("fis: temporary error"))

	repo := &mockRepo{consumer: consumer}
	svc := newMemberSvc(repo, &mockCmds{}, fisMock)

	err := svc.UpdateMember(context.Background(), ports.UpdateMemberRequest{
		Rctx:      newRctx(),
		MemberID:  consumer.ID,
		FirstName: "Janet",
		Address1:  "456 New St",
	})

	// Should NOT error — FIS failure is non-fatal for demographics update
	if err != nil {
		t.Fatalf("unexpected error (FIS failure should be non-fatal): %v", err)
	}
	if !repo.demographicsUpdated {
		t.Error("expected Aurora demographics updated even when FIS fails")
	}
}

// ─── GetMember: member not found ─────────────────────────────────────────────

func TestGetMember_NotFound(t *testing.T) {
	repo := &mockRepo{} // empty
	svc := newMemberSvc(repo, &mockCmds{}, mock.NewFisCodeConnectMock())

	_, err := svc.GetMember(context.Background(), newRctx(), uuid.New())

	if !errors.Is(err, ports.ErrMemberNotFound) {
		t.Errorf("expected ErrMemberNotFound, got: %v", err)
	}
}

// ─── GetMember: serves from Aurora, no FIS call ──────────────────────────────

func TestGetMember_ServesFromAurora(t *testing.T) {
	consumer := resolvedConsumer()
	card := resolvedCard(consumer.ID)
	fisMock := mock.NewFisCodeConnectMock()

	repo := &mockRepo{consumer: consumer, card: card}
	svc := newMemberSvc(repo, &mockCmds{}, fisMock)

	view, err := svc.GetMember(context.Background(), newRctx(), consumer.ID)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if view.MemberID != consumer.ID {
		t.Errorf("expected %s, got %s", consumer.ID, view.MemberID)
	}
	if !view.FISResolved {
		t.Error("expected FISResolved=true")
	}
	if view.CardID == nil {
		t.Error("expected CardID set")
	}
	// GetMember must NEVER call FIS
	if fisMock.Calls["GetPerson"] != 0 {
		t.Error("GetMember must not call FIS GetPerson — Aurora is source of truth")
	}
}
