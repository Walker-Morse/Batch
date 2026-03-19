package pipeline

// Tests for Stage 4 — Batch Assembly (stage4_batch_assembly.go).
//
// Strategy: FISBatchAssembler, FileStore, BatchFileRepository are all replaced
// with in-memory mocks. PGPEncrypt is swapped for NullPGPEncrypt (passthrough)
// or an error func. No real S3, DB, or FIS connection required.
//
// Coverage targets:
//
//	Run() happy path — assembled file written to fis-exchange, status→ASSEMBLED
//	Run() result carries correct S3Key, Filename, RecordCount
//	Run() assembler error → propagates as error, no S3 write
//	Run() PGP encrypt failure → propagates as error
//	Run() S3 PutObject failure → propagates as error
//	Run() staged object deleted after successful encrypt+write (§5.4.3)
//	Run() staged delete failure → logged but NOT a fatal error (S3 lifecycle backstop)
//	Run() status not updated to ASSEMBLED if S3 write fails
//	NullPGPEncrypt — passthrough, no transformation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

type stage4Mocks struct {
	assembler      *testutil.MockFISBatchAssembler
	files          *mockS3
	batchFiles     *testutil.MockBatchFileRepository
	stagedRecords  *testutil.MockBatchRecordsLister
	audit          *testutil.MockAuditLogWriter
	obs            *testutil.MockObservability
}

// mockS3 gives finer control than MockFileStore for stage4 — we need to track
// PutObject calls and inject errors per operation type.
type mockS3 struct {
	putKey    string
	putErr    error
	deleteErr error
	deleted   []string
}

func (m *mockS3) GetObject(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (m *mockS3) PutObject(_ context.Context, _, key string, _ io.Reader) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.putKey = key
	return nil
}
func (m *mockS3) DeleteObject(_ context.Context, _, key string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deleted = append(m.deleted, key)
	return nil
}
func (m *mockS3) HeadObject(_ context.Context, _, _ string) (*ports.ObjectMeta, error) {
	return &ports.ObjectMeta{}, nil
}

func newStage4(pgpFn func(io.Reader) (io.Reader, error)) (*BatchAssemblyStage, *stage4Mocks) {
	m := &stage4Mocks{
		assembler:     testutil.NewMockFISBatchAssembler("PROG000106010001.OTC.txt", 42, "FISBODY"),
		files:         &mockS3{},
		batchFiles:    testutil.NewMockBatchFileRepository(),
		stagedRecords: testutil.NewMockBatchRecordsLister(),
		audit:         &testutil.MockAuditLogWriter{},
		obs:           &testutil.MockObservability{},
	}
	if pgpFn == nil {
		pgpFn = NullPGPEncrypt
	}
	stage := &BatchAssemblyStage{
		Assembler:         m.assembler,
		Files:             m.files,
		BatchFiles:        m.batchFiles,
		StagedRecords:     m.stagedRecords,
		Audit:             m.audit,
		Obs:               m.obs,
		PGPEncrypt:        pgpFn,
		StagedBucket:      "onefintech-staged",
		FISExchangeBucket: "onefintech-fis-exchange",
	}
	return stage, m
}

// testProgramID is a stable UUID used across stage4 tests for program lookup.
var testProgramID = uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")

func seedBF(m *stage4Mocks, tenantID string) *ports.BatchFile {
	bf := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      tenantID,
		FileType:      "SRG310",
		Status:        "PROCESSING",
		ArrivedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	m.batchFiles.Files[bf.ID] = bf
	// Seed a staged RT30 row carrying the program UUID so resolveProgramID succeeds.
	subID := int64(26071)
	m.stagedRecords.Register(bf.CorrelationID.String(), &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{
				ID:             uuid.New(),
				SequenceInFile: 1,
				ClientMemberID: "MBR-001",
				ProgramID:      testProgramID,
				SubprogramID:   &subID,
			},
		},
	})
	return bf
}

// ─── happy path ───────────────────────────────────────────────────────────────

// TestStage4_HappyPath verifies the full success case:
// assembler called → PGP encrypt → S3 put to fis-exchange → status ASSEMBLED.
func TestStage4_HappyPath(t *testing.T) {
	stage, m := newStage4(nil)
	bf := seedBF(m, "rfu-oregon")

	result, err := stage.Run(context.Background(), bf)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Filename != "PROG000106010001.OTC.txt" {
		t.Errorf("Filename = %q; want PROG000106010001.OTC.txt", result.Filename)
	}
	if result.RecordCount != 42 {
		t.Errorf("RecordCount = %d; want 42", result.RecordCount)
	}
	if bf.Status != "ASSEMBLED" {
		t.Errorf("status = %q; want ASSEMBLED", bf.Status)
	}
}

// TestStage4_S3KeyContainsTenantAndFilename verifies the fis-exchange S3 key
// is namespaced by tenant and includes the FIS-prescribed filename.
// Predictable key structure is required for Stage 5 SFTP delivery.
func TestStage4_S3KeyContainsTenantAndFilename(t *testing.T) {
	stage, m := newStage4(nil)
	bf := seedBF(m, "rfu-oregon")

	result, err := stage.Run(context.Background(), bf)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !strings.Contains(result.S3Key, "rfu-oregon") {
		t.Errorf("S3Key %q does not contain tenant ID", result.S3Key)
	}
	if !strings.Contains(result.S3Key, result.Filename) {
		t.Errorf("S3Key %q does not contain filename %q", result.S3Key, result.Filename)
	}
}

// TestStage4_AssemblerCalledWithCorrelationID verifies that the assembler receives
// the correct correlation ID and tenant ID from the batch file.
// AssembleFile is the only place FIS record format knowledge lives (ADR-001).
func TestStage4_AssemblerCalledWithCorrelationID(t *testing.T) {
	stage, m := newStage4(nil)
	bf := seedBF(m, "rfu-oregon")

	_, err := stage.Run(context.Background(), bf)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(m.assembler.Calls) != 1 {
		t.Fatalf("assembler called %d times; want 1", len(m.assembler.Calls))
	}
	req := m.assembler.Calls[0]
	if req.CorrelationID != bf.CorrelationID {
		t.Errorf("AssembleRequest.CorrelationID = %s; want %s", req.CorrelationID, bf.CorrelationID)
	}
	if req.TenantID != bf.TenantID {
		t.Errorf("AssembleRequest.TenantID = %q; want %q", req.TenantID, bf.TenantID)
	}
	if req.ProgramID != testProgramID {
		t.Errorf("AssembleRequest.ProgramID = %s; want %s", req.ProgramID, testProgramID)
	}
}

// TestStage4_StagedObjectDeleted verifies that the plaintext staged object is
// deleted after successful PGP encryption and S3 write. This is the PHI
// non-persistence requirement (§5.4.3) — plaintext must not outlive Stage 4.
func TestStage4_StagedObjectDeleted(t *testing.T) {
	stage, m := newStage4(nil)
	bf := seedBF(m, "rfu-oregon")

	_, err := stage.Run(context.Background(), bf)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(m.files.deleted) == 0 {
		t.Error("staged object must be deleted after successful assembly (§5.4.3)")
	}
}

// ─── failure paths ────────────────────────────────────────────────────────────

// TestStage4_AssemblerError_Propagates verifies that an assembler failure stops
// the pipeline — no S3 write, no status update. Nothing to encrypt if assembly fails.
func TestStage4_AssemblerError_Propagates(t *testing.T) {
	stage, m := newStage4(nil)
	m.assembler.AssembleErr = errors.New("fis_adapter: lister failed")
	bf := seedBF(m, "rfu-oregon")

	_, err := stage.Run(context.Background(), bf)
	if err == nil {
		t.Fatal("expected error on assembler failure")
	}
	if m.files.putKey != "" {
		t.Error("S3 put must not happen when assembler fails")
	}
	if bf.Status == "ASSEMBLED" {
		t.Error("status must not be ASSEMBLED when assembler fails")
	}
}

// TestStage4_PGPEncryptError_Propagates verifies that a PGP encrypt failure stops
// the pipeline. An unencrypted file must never be written to fis-exchange.
func TestStage4_PGPEncryptError_Propagates(t *testing.T) {
	encryptErr := errors.New("pgp: no FIS public key loaded")
	pgpFn := func(r io.Reader) (io.Reader, error) { return nil, encryptErr }

	stage, m := newStage4(pgpFn)
	bf := seedBF(m, "rfu-oregon")

	_, err := stage.Run(context.Background(), bf)
	if err == nil {
		t.Fatal("expected error on PGP encrypt failure")
	}
	if m.files.putKey != "" {
		t.Error("S3 put must not happen when PGP encrypt fails")
	}
}

// TestStage4_S3PutError_Propagates verifies that an S3 write failure stops the
// pipeline and prevents the status from moving to ASSEMBLED.
func TestStage4_S3PutError_Propagates(t *testing.T) {
	stage, m := newStage4(nil)
	m.files.putErr = errors.New("s3: request timeout")
	bf := seedBF(m, "rfu-oregon")

	_, err := stage.Run(context.Background(), bf)
	if err == nil {
		t.Fatal("expected error on S3 put failure")
	}
	if bf.Status == "ASSEMBLED" {
		t.Error("status must not be ASSEMBLED when S3 write fails")
	}
}

// TestStage4_StagedDeleteFailure_NonFatal verifies that a staged object delete
// failure does NOT abort Stage 4. The encrypted file is already in fis-exchange;
// the 24-hour S3 lifecycle policy on staged/ is the safety backstop (§5.4.3).
// The failure must be logged as ERROR but must not propagate.
func TestStage4_StagedDeleteFailure_NonFatal(t *testing.T) {
	stage, m := newStage4(nil)
	m.files.deleteErr = errors.New("s3: access denied on staged/")
	bf := seedBF(m, "rfu-oregon")

	result, err := stage.Run(context.Background(), bf)
	if err != nil {
		t.Fatalf("Run() must not error on staged delete failure; got: %v", err)
	}
	if result == nil {
		t.Fatal("result must not be nil on staged delete failure")
	}
	if bf.Status != "ASSEMBLED" {
		t.Errorf("status = %q; want ASSEMBLED — delete failure is non-fatal", bf.Status)
	}
	// An ERROR-level log event must have been emitted
	found := false
	for _, e := range m.obs.Events {
		if e.Level == "ERROR" && strings.Contains(e.EventType, "staged_delete_failed") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected an ERROR log event for staged delete failure")
	}
}

// ─── NullPGPEncrypt ───────────────────────────────────────────────────────────

// TestNullPGPEncrypt_Passthrough verifies that NullPGPEncrypt returns the same bytes
// without transformation. Used in DEV before real FIS PGP keys are provisioned.
func TestNullPGPEncrypt_Passthrough(t *testing.T) {
	content := []byte("FIS batch body content")
	r, err := NullPGPEncrypt(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("NullPGPEncrypt error: %v", err)
	}
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, content) {
		t.Errorf("NullPGPEncrypt modified content: got %q; want %q", got, content)
	}
}

// ─── resolveProgramID error paths ─────────────────────────────────────────────

// TestStage4_ResolveProgramID_NoStagedRows verifies that Stage 4 succeeds on an
// empty file (zero staged RT30 rows). The assembler produces a valid header+trailer
// file with seq=1 — no fis_sequence.Next call, no collision risk on empty files.
func TestStage4_ResolveProgramID_NoStagedRows(t *testing.T) {
	stage, m := newStage4(nil)
	bf := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		FileType:      "SRG310",
		Status:        "PROCESSING",
		ArrivedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	m.batchFiles.Files[bf.ID] = bf
	// Register empty staged records — no RT30 rows (all-dead-lettered or empty file)
	m.stagedRecords.Register(bf.CorrelationID.String(), &ports.StagedRecords{})

	_, err := stage.Run(context.Background(), bf)
	if err != nil {
		t.Fatalf("Run() must succeed on empty file (zero staged rows); got: %v", err)
	}
	// Assembler must still be called — it produces a valid header+trailer file
	if len(m.assembler.Calls) != 1 {
		t.Errorf("assembler called %d times; want 1", len(m.assembler.Calls))
	}
	// ProgramID must be zero UUID — no program to look up
	if m.assembler.Calls[0].ProgramID != (uuid.UUID{}) {
		t.Errorf("expected zero ProgramID for empty file; got %s", m.assembler.Calls[0].ProgramID)
	}
}

// TestStage4_ResolveProgramID_NilUUID verifies that Stage 4 fails fast
// when a staged RT30 row has a nil (zero) ProgramID.
// This guards against rows inserted before migration 006.
func TestStage4_ResolveProgramID_NilUUID(t *testing.T) {
	stage, m := newStage4(nil)
	bf := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		FileType:      "SRG310",
		Status:        "PROCESSING",
		ArrivedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	m.batchFiles.Files[bf.ID] = bf
	subID := int64(26071)
	// Register a row with zero UUID — simulates pre-migration 006 data
	m.stagedRecords.Register(bf.CorrelationID.String(), &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{
				ID:             uuid.New(),
				SequenceInFile: 1,
				ClientMemberID: "MBR-001",
				ProgramID:      uuid.UUID{}, // zero UUID
				SubprogramID:   &subID,
			},
		},
	})

	_, err := stage.Run(context.Background(), bf)
	if err == nil {
		t.Fatal("expected error when RT30 row has nil program_id")
	}
	if !strings.Contains(err.Error(), "nil program_id") {
		t.Errorf("error %q should mention nil program_id", err.Error())
	}
}
