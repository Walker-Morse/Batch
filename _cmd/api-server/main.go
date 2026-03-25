package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/walker-morse/batch/_shared/domain"
	sharedports "github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/card_member_api/fis_code_connect/mock"
	"github.com/walker-morse/batch/card_member_api/router"
	"github.com/walker-morse/batch/card_member_api/service"
)

var errNotFound = fmt.Errorf("not found (stub repo — wire Aurora adapter before UAT)")

func main() {
	addr         := flag.String("addr", ":8080", "listen address")
	fisMock      := flag.Bool("fis-mock", true, "use in-memory FIS mock")
	fisClientID  := flag.Int("fis-client-id", 0, "FIS client ID")
	fisSubprogID := flag.Int("fis-subprog-id", 26071, "FIS subprogram ID")
	fisPackageID := flag.Int("fis-package-id", 0, "FIS package ID")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if !*fisMock {
		log.Error("real FIS adapter not yet implemented — run with -fis-mock until John Stevens credentials ready")
		os.Exit(1)
	}

	log.Info("FIS mock mode enabled — no real FIS calls will be made")
	fisMockAdapter := mock.NewFisCodeConnectMock()

	cmdRepo     := &noopCommandRepo{}
	auditWriter := &noopAuditWriter{}
	obsPort     := &noopObsPort{}
	cardRepo    := &noopCardRepo{}

	cardSvc := service.NewCardService(fisMockAdapter, cardRepo, cmdRepo, auditWriter, obsPort, log)
	memberSvc := service.NewMemberService(
		fisMockAdapter, cardRepo, cmdRepo, auditWriter, obsPort, log,
		int32(*fisClientID), int32(*fisSubprogID), int32(*fisPackageID),
	)

	h := router.New(memberSvc, cardSvc)
	srv := &http.Server{
		Addr:         *addr,
		Handler:      h,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("api-server starting",
			slog.String("addr", *addr),
			slog.String("swagger_ui", "http://localhost"+*addr+"/docs/"),
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Info("api-server stopped")
}

// ─── No-op stubs — replace with aurora.* before UAT (May 18) ─────────────────

type noopCommandRepo struct{}
func (r *noopCommandRepo) Insert(_ context.Context, _ *sharedports.DomainCommand) error { return nil }
func (r *noopCommandRepo) FindDuplicate(_ context.Context, _, _, _, _ string) (*sharedports.DomainCommand, error) { return nil, nil }
func (r *noopCommandRepo) UpdateStatus(_ context.Context, _ uuid.UUID, _ string, _ *string) error { return nil }

type noopAuditWriter struct{}
func (r *noopAuditWriter) Write(_ context.Context, _ *sharedports.AuditEntry) error { return nil }

type noopObsPort struct{}
func (r *noopObsPort) LogEvent(_ context.Context, _ *sharedports.LogEvent) error { return nil }
func (r *noopObsPort) RecordMetric(_ context.Context, _ string, _ float64, _ map[string]string) error { return nil }

type noopCardRepo struct{}
func (r *noopCardRepo) GetConsumerByID(_ context.Context, _ uuid.UUID) (*domain.Consumer, error) { return nil, errNotFound }
func (r *noopCardRepo) GetConsumerByClientMemberID(_ context.Context, _, _ string) (*domain.Consumer, error) { return nil, errNotFound }
func (r *noopCardRepo) GetCardByMemberID(_ context.Context, _ uuid.UUID) (*domain.Card, error) { return nil, errNotFound }
func (r *noopCardRepo) GetCardByFisCardID(_ context.Context, _ string) (*domain.Card, error) { return nil, errNotFound }
func (r *noopCardRepo) GetPursesByCardID(_ context.Context, _ uuid.UUID) ([]*domain.Purse, error) { return nil, nil }
func (r *noopCardRepo) GetPurseByBenefitType(_ context.Context, _ uuid.UUID, _ string) (*domain.Purse, error) { return nil, errNotFound }
func (r *noopCardRepo) CreateConsumer(_ context.Context, _ *domain.Consumer) error { return nil }
func (r *noopCardRepo) CreateCard(_ context.Context, _ *domain.Card) error { return nil }
func (r *noopCardRepo) CreatePurse(_ context.Context, _ *domain.Purse) error { return nil }
func (r *noopCardRepo) SetConsumerFISIDs(_ context.Context, _ uuid.UUID, _ string, _ *string) error { return nil }
func (r *noopCardRepo) SetCardFISID(_ context.Context, _ uuid.UUID, _ string) error { return nil }
func (r *noopCardRepo) SetCardStatus(_ context.Context, _ uuid.UUID, _ domain.CardStatus, _ time.Time) error { return nil }
func (r *noopCardRepo) SetPurseStatus(_ context.Context, _ uuid.UUID, _ domain.PurseStatus, _ time.Time) error { return nil }
func (r *noopCardRepo) UpdateConsumerDemographics(_ context.Context, _ uuid.UUID, _ service.DemographicsUpdate) error { return nil }
