package pipeline

// Tests for Stage 6 — Return File Wait (stage6_return_file_wait.go).
//
// Coverage:
//   - Happy path: PollForReturn succeeds, return body passed through
//   - Poll timeout: status → STALLED, dead.letter.alert emitted, error returned
//   - Default timeout applied when Timeout == 0
//   - Audit written on timeout

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
)

func newStage6(t *testing.T) (*ReturnFileWaitStage, *testutil.MockBatchFileRepository, *testutil.MockFISTransport, *testutil.MockAuditLogWriter, *testutil.MockObservability) {
	t.Helper()
	bf := testutil.NewMockBatchFileRepository()
	transport := testutil.NewMockFISTransport()
	audit := &testutil.MockAuditLogWriter{}
	obs := &testutil.MockObservability{}
	stage := &ReturnFileWaitStage{
		Transport:  transport,
		BatchFiles: bf,
		Audit:      audit,
		Obs:        obs,
		Timeout:    5 * time.Second, // short for tests
	}
	return stage, bf, transport, audit, obs
}

func makeBatchFileForStage6(bf *testutil.MockBatchFileRepository) *ports.BatchFile {
	f := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		Status:        "SUBMITTED",
	}
	bf.Files[f.ID] = f
	return f
}

// TestStage6_HappyPath verifies that a successful poll returns the body for Stage 7.
func TestStage6_HappyPath(t *testing.T) {
	stage, bf, transport, _, _ := newStage6(t)
	batchFile := makeBatchFileForStage6(bf)
	returnContent := []byte("RT10...return file content")
	transport.PollResult = io.NopCloser(bytes.NewReader(returnContent))

	result, err := stage.Run(context.Background(), batchFile)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.Body == nil {
		t.Fatal("expected non-nil result.Body")
	}
	data, _ := io.ReadAll(result.Body)
	if string(data) != string(returnContent) {
		t.Errorf("body = %q; want %q", data, returnContent)
	}
}

// TestStage6_PollTimeout transitions to STALLED and emits dead.letter.alert.
func TestStage6_PollTimeout(t *testing.T) {
	stage, bf, transport, audit, obs := newStage6(t)
	batchFile := makeBatchFileForStage6(bf)
	transport.PollErr = errors.New("context deadline exceeded")

	result, err := stage.Run(context.Background(), batchFile)

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if result != nil {
		t.Error("expected nil result on timeout")
	}
	if batchFile.Status != string(domain.BatchFileStalled) {
		t.Errorf("status = %q; want STALLED", batchFile.Status)
	}
	if obs.EventCount("dead.letter.alert") == 0 {
		t.Error("expected dead.letter.alert event; got none")
	}
	if !strings.Contains(err.Error(), "return file wait timeout") {
		t.Errorf("error = %q; want 'return file wait timeout'", err.Error())
	}
	// Audit entry must record SUBMITTED → STALLED
	if len(audit.Entries) == 0 {
		t.Fatal("expected audit entry on timeout")
	}
	if audit.Entries[0].NewState != "STALLED" {
		t.Errorf("audit NewState = %q; want STALLED", audit.Entries[0].NewState)
	}
}

// TestStage6_DefaultTimeoutApplied verifies zero Timeout is replaced with 6 hours.
func TestStage6_DefaultTimeoutApplied(t *testing.T) {
	stage, bf, transport, _, _ := newStage6(t)
	stage.Timeout = 0 // reset to trigger default
	batchFile := makeBatchFileForStage6(bf)
	transport.PollResult = io.NopCloser(bytes.NewBufferString("data"))

	_, err := stage.Run(context.Background(), batchFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The stage should have set Timeout to 6h internally
	if stage.Timeout != 6*time.Hour {
		t.Errorf("Timeout = %v; want 6h", stage.Timeout)
	}
}

// TestStage6_TimeoutIncludesCorrelationID verifies the error message surfaces
// the correlation ID for on-call triage (no PHI, but correlation ID is safe).
func TestStage6_TimeoutIncludesCorrelationID(t *testing.T) {
	stage, bf, transport, _, _ := newStage6(t)
	batchFile := makeBatchFileForStage6(bf)
	transport.PollErr = errors.New("timeout")

	_, err := stage.Run(context.Background(), batchFile)

	if !strings.Contains(err.Error(), batchFile.CorrelationID.String()) {
		t.Errorf("error = %q; want correlation_id %s in message", err.Error(), batchFile.CorrelationID)
	}
}
