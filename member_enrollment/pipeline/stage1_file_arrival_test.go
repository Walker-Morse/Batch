package pipeline

import (
	"fmt"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/testutil"
)

func TestFileArrivalStage_WritesReceivedRowToDB(t *testing.T) {
	repo := testutil.NewMockBatchFileRepository()
	obs  := &testutil.MockObservability{}
	audit := &testutil.MockAuditLogWriter{}

	stage := &FileArrivalStage{
		Files:      testutil.NewMockFileStore(),
		BatchFiles: repo,
		Audit:      audit,
		Obs:        obs,
	}

	correlationID := uuid.New()
	in := &FileArrivalInput{
		CorrelationID: correlationID,
		TenantID:      "rfu-oregon",
		ClientID:      "rfu",
		S3Bucket:      "onefintech-inbound-raw",
		S3Key:         "rfu/2026-06-01/enrollment.pgp",
		FileType:      string(domain.FileTypeSRG310),
	}

	f, err := stage.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	// Row must be written to DB before any processing begins (§4.1 non-repudiation anchor)
	if len(repo.Files) != 1 {
		t.Errorf("expected 1 batch_files row, got %d", len(repo.Files))
	}

	// Status must be RECEIVED at this point
	if f.Status != "RECEIVED" {
		t.Errorf("expected status RECEIVED, got %q", f.Status)
	}

	// CorrelationID must be propagated
	if f.CorrelationID != correlationID {
		t.Error("correlation_id not propagated to batch_files row")
	}
}

func TestFileArrivalStage_SetsArrivedAt(t *testing.T) {
	repo  := testutil.NewMockBatchFileRepository()
	before := time.Now().Add(-time.Second)

	stage := &FileArrivalStage{
		Files:      testutil.NewMockFileStore(),
		BatchFiles: repo,
		Audit:      &testutil.MockAuditLogWriter{},
		Obs:        &testutil.MockObservability{},
	}

	f, err := stage.Run(context.Background(), &FileArrivalInput{
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		ClientID:      "rfu",
		FileType:      string(domain.FileTypeSRG310),
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// arrived_at must be set and recent
	if f.ArrivedAt.Before(before) {
		t.Error("arrived_at should be set to current time")
	}
}

func TestFileArrivalStage_EmitsFileArrivedEvent(t *testing.T) {
	obs := &testutil.MockObservability{}

	stage := &FileArrivalStage{
		Files:      testutil.NewMockFileStore(),
		BatchFiles: testutil.NewMockBatchFileRepository(),
		Audit:      &testutil.MockAuditLogWriter{},
		Obs:        obs,
	}

	_, err := stage.Run(context.Background(), &FileArrivalInput{
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		ClientID:      "rfu",
		FileType:      string(domain.FileTypeSRG310),
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Stage 1 emits two file.arrived events: one on receipt, one after
	// the non-repudiation row is written (§7.5 EventBridge-compatible schema).
	if obs.EventCount("file.arrived") < 1 {
		t.Errorf("expected at least 1 file.arrived event, got %d", obs.EventCount("file.arrived"))
	}
}

func TestFileArrivalStage_PropagatesTenantID(t *testing.T) {
	repo := testutil.NewMockBatchFileRepository()

	stage := &FileArrivalStage{
		Files:      testutil.NewMockFileStore(),
		BatchFiles: repo,
		Audit:      &testutil.MockAuditLogWriter{},
		Obs:        &testutil.MockObservability{},
	}

	f, err := stage.Run(context.Background(), &FileArrivalInput{
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		ClientID:      "rfu",
		FileType:      string(domain.FileTypeSRG310),
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// tenant_id is the multi-tenancy isolation key — must be propagated to every row
	if f.TenantID != "rfu-oregon" {
		t.Errorf("tenant_id not propagated: got %q", f.TenantID)
	}
}

func TestFileArrivalStage_DBWriteFailureReturnsError(t *testing.T) {
	repo := testutil.NewMockBatchFileRepository()
	repo.CreateErr = errDBDown

	stage := &FileArrivalStage{
		Files:      testutil.NewMockFileStore(),
		BatchFiles: repo,
		Audit:      &testutil.MockAuditLogWriter{},
		Obs:        &testutil.MockObservability{},
	}

	_, err := stage.Run(context.Background(), &FileArrivalInput{
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		ClientID:      "rfu",
		FileType:      string(domain.FileTypeSRG310),
	})

	// If batch_files row cannot be written, Stage 1 must fail —
	// non-repudiation anchor is the prerequisite for all downstream processing
	if err == nil {
		t.Error("expected error when DB write fails — non-repudiation row is mandatory")
	}
}

// sentinel error for test injection
var errDBDown = fmt.Errorf("aurora: connection refused")
