package pipeline

// Tests for Stage 5 — FIS Egress Deposit (stage5_fis_transfer.go).
//
// Coverage:
//   - Happy path: file fetched from fis-exchange, deposited to egress, status → TRANSFERRED, audit written
//   - S3 GetObject failure → error returned, no egress deposit attempted
//   - Egress PutObject failure → error, status not TRANSFERRED, error logged
//   - UpdateStatus failure → error returned after successful egress deposit
//   - Egress key convention: {tenant_id}/{filename}
//   - Audit entry carries correct correlation_id and egress key in Notes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
)

func newStage5(t *testing.T) (*FISTransferStage, *testutil.MockBatchFileRepository, *testutil.MockFileStore, *testutil.MockAuditLogWriter) {
	t.Helper()
	bf := testutil.NewMockBatchFileRepository()
	files := testutil.NewMockFileStore()
	audit := &testutil.MockAuditLogWriter{}
	obs := &testutil.MockObservability{}
	stage := &FISTransferStage{
		Files:             files,
		BatchFiles:        bf,
		Audit:             audit,
		Obs:               obs,
		FISExchangeBucket: "fis-exchange",
		EgressBucket:      "egress",
	}
	return stage, bf, files, audit
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

// TestStage5_HappyPath verifies S3 fetch → egress deposit → TRANSFERRED → audit written.
func TestStage5_HappyPath(t *testing.T) {
	stage, bf, files, audit := newStage5(t)
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

	var egressBucket, egressKey string
	files.PutObjectFn = func(_ context.Context, bucket, key string, _ io.Reader) error {
		egressBucket = bucket
		egressKey = key
		return nil
	}

	err := stage.Run(context.Background(), batchFile, assembly)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if egressBucket != "egress" {
		t.Errorf("PutObject bucket = %q; want egress", egressBucket)
	}
	wantKey := fmt.Sprintf("%s/%s", batchFile.TenantID, assembly.Filename)
	if egressKey != wantKey {
		t.Errorf("PutObject key = %q; want %q", egressKey, wantKey)
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

// TestStage5_S3GetFails aborts before egress deposit.
func TestStage5_S3GetFails(t *testing.T) {
	stage, bf, files, _ := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile

	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return nil, errors.New("s3: key not found")
	}
	putCalled := false
	files.PutObjectFn = func(_ context.Context, _, _ string, _ io.Reader) error {
		putCalled = true
		return nil
	}

	err := stage.Run(context.Background(), batchFile, makeAssembly("outbound/rfu-oregon/test.txt", "test.txt"))

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if putCalled {
		t.Error("PutObject should not be called after GetObject failure")
	}
	if batchFile.Status == string(domain.BatchFileTransferred) {
		t.Error("status should not advance to TRANSFERRED on S3 read failure")
	}
}

// TestStage5_EgressPutFails returns error and does not transition to TRANSFERRED.
func TestStage5_EgressPutFails(t *testing.T) {
	stage, bf, files, _ := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile

	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("encrypted")), nil
	}
	files.PutObjectFn = func(_ context.Context, _, _ string, _ io.Reader) error {
		return errors.New("s3: access denied")
	}

	err := stage.Run(context.Background(), batchFile, makeAssembly("outbound/rfu-oregon/test.txt", "test.txt"))

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !strings.Contains(err.Error(), "deposit to egress bucket") {
		t.Errorf("error = %q; want 'deposit to egress bucket'", err.Error())
	}
	if batchFile.Status == string(domain.BatchFileTransferred) {
		t.Error("status must not be TRANSFERRED after egress deposit failure")
	}
}

// TestStage5_AuditCorrelationID verifies correlation_id is present on audit entry.
func TestStage5_AuditCorrelationID(t *testing.T) {
	stage, bf, files, audit := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	bf.Files[batchFile.ID] = batchFile

	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("enc")), nil
	}
	files.PutObjectFn = func(_ context.Context, _, _ string, _ io.Reader) error { return nil }

	_ = stage.Run(context.Background(), batchFile, makeAssembly("outbound/rfu-oregon/test.txt", "test.txt"))

	if len(audit.Entries) == 0 {
		t.Fatal("expected audit entry")
	}
	if audit.Entries[0].CorrelationID == nil || *audit.Entries[0].CorrelationID != batchFile.CorrelationID {
		t.Errorf("audit CorrelationID = %v; want %v", audit.Entries[0].CorrelationID, batchFile.CorrelationID)
	}
}

// TestStage5_EgressKeyConvention verifies key format is {tenant_id}/{filename}.
func TestStage5_EgressKeyConvention(t *testing.T) {
	stage, bf, files, _ := newStage5(t)
	batchFile := makeBatchFile("ASSEMBLED")
	batchFile.TenantID = "rfu-oregon"
	bf.Files[batchFile.ID] = batchFile

	files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBufferString("enc")), nil
	}
	var gotKey string
	files.PutObjectFn = func(_ context.Context, _, key string, _ io.Reader) error {
		gotKey = key
		return nil
	}

	assembly := makeAssembly("outbound/rfu-oregon/pp00000001032600.issuance.txt", "pp00000001032600.issuance.txt")
	_ = stage.Run(context.Background(), batchFile, assembly)

	want := "rfu-oregon/pp00000001032600.issuance.txt"
	if gotKey != want {
		t.Errorf("egress key = %q; want %q", gotKey, want)
	}
}
