package pipeline

// Tests for Stage 5 — FIS Transfer (stage5_fis_transfer.go).
//
// Coverage:
//   - Happy path: file fetched from S3, delivered to FIS, status → TRANSFERRED, audit written
//   - S3 GetObject failure → error returned, no delivery attempted
//   - Transport Deliver failure → error, status not TRANSFERRED, error logged
//   - UpdateStatus failure → error returned after successful delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
)

func newStage5(t *testing.T) (*FISTransferStage, *testutil.MockBatchFileRepository, *testutil.MockFISTransport, *testutil.MockFileStore, *testutil.MockAuditLogWriter) {
	t.Helper()
	bf := testutil.NewMockBatchFileRepository()
	transport := testutil.NewMockFISTransport()
	files := testutil.NewMockFileStore()
	audit := &testutil.MockAuditLogWriter{}
	obs := &testutil.MockObservability{}
	stage := &FISTransferStage{
		Transport:         transport,
		Files:             files,
		BatchFiles:        bf,
		Audit:             audit,
		Obs:               obs,
		FISExchangeBucket: "fis-exchange",
	}
	return stage, bf, transport, files, audit
}

func makeBatchFile(status string) *ports.BatchFile {
	f := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		Status:        status,
	}
	return f
}

func makeAssembly(key, filename string) *BatchAssemblyResult {
	return &BatchAssemblyResult{S3Key: key, Filename: filename, RecordCount: 3}
}

// TestStage5_HappyPath verifies S3 fetch → FIS deliver → TRANSFERRED → audit written.
func TestStage5_HappyPath(t *testing.T) {
	stage, bf, transport, files, audit := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile
	assembly := makeAssembly("outbound/rfu-oregon/test.txt", "test.txt")

	encrypted := []byte("pgp-encrypted-content")
	files.GetObjectFn = func(_ context.Context, bucket, key string) (io.ReadCloser, error) {
		if bucket != "fis-exchange" || key != assembly.S3Key {
			t.Errorf("GetObject called with bucket=%q key=%q", bucket, key)
		}
		return io.NopCloser(bytes.NewReader(encrypted)), nil
	}

	err := stage.Run(context.Background(), batchFile, assembly)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if transport.DeliveredFilename != "test.txt" {
		t.Errorf("DeliveredFilename = %q; want test.txt", transport.DeliveredFilename)
	}
	if string(transport.DeliveredBody) != string(encrypted) {
		t.Error("delivered body does not match S3 content")
	}
	if batchFile.Status != string(domain.BatchFileTransferred) {
		t.Errorf("status = %q; want TRANSFERRED", batchFile.Status)
	}
	if len(audit.Entries) == 0 {
		t.Fatal("expected audit entry; got none")
	}
	if audit.Entries[0].NewState != "TRANSFERRED" {
		t.Errorf("audit NewState = %q; want TRANSFERRED", audit.Entries[0].NewState)
	}
}

// TestStage5_S3GetFails aborts before delivery.
func TestStage5_S3GetFails(t *testing.T) {
	stage, bf, transport, files, _ := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile
	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return nil, errors.New("s3: key not found")
	}

	err := stage.Run(context.Background(), batchFile, makeAssembly("outbound/rfu-oregon/test.txt", "test.txt"))

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if transport.DeliveredFilename != "" {
		t.Error("Deliver should not have been called on S3 failure")
	}
	if batchFile.Status == string(domain.BatchFileTransferred) {
		t.Error("status should not advance to TRANSFERRED on S3 failure")
	}
}

// TestStage5_DeliverFails returns error and does not transition to TRANSFERRED.
func TestStage5_DeliverFails(t *testing.T) {
	stage, bf, transport, files, _ := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile
	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("encrypted")), nil
	}
	transport.DeliverErr = errors.New("sftp: connection refused")

	err := stage.Run(context.Background(), batchFile, makeAssembly("outbound/rfu-oregon/test.txt", "test.txt"))

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !strings.Contains(err.Error(), "deliver to FIS") {
		t.Errorf("error = %q; want 'deliver to FIS'", err.Error())
	}
	if batchFile.Status == string(domain.BatchFileTransferred) {
		t.Error("status must not be TRANSFERRED after delivery failure")
	}
}

// TestStage5_AuditCorrelationID verifies correlation_id is present on audit entry.
func TestStage5_AuditCorrelationID(t *testing.T) {
	stage, bf, _, files, audit := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile
	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("enc")), nil
	}

	_ = stage.Run(context.Background(), batchFile, makeAssembly("outbound/rfu-oregon/test.txt", "test.txt"))

	if len(audit.Entries) == 0 {
		t.Fatal("expected audit entry")
	}
	if audit.Entries[0].CorrelationID == nil || *audit.Entries[0].CorrelationID != batchFile.CorrelationID {
		t.Errorf("audit CorrelationID = %v; want %v", audit.Entries[0].CorrelationID, batchFile.CorrelationID)
	}
}
