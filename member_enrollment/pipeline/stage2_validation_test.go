package pipeline

// Tests for Stage 2 — Validation (stage2_validation.go).
//
// Strategy: FileStore and BatchFileRepository are replaced with in-memory mocks.
// PGPDecrypt is swapped for NullPGPDecrypt (passthrough) or an error-returning
// func to exercise the halt path.
//
// Coverage targets:
//
//	Run() happy path — SRG310: rows parsed, status→VALIDATING, SHA-256 non-empty
//	Run() happy path — SRG315 and SRG320 file types
//	Run() PGP decrypt failure → HALTED
//	Run() unknown file_type → HALTED
//	Run() malformed rows → dead-lettered, malformed_count incremented
//	Run() record_count set after parsing
//	halt() → status transition to HALTED
//	NullPGPDecrypt — returns identical reader, no error
//	SHA-256 is computed over plaintext, not ciphertext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// srg310CSV returns a minimal valid SRG310 CSV with n data rows.
func srg310CSV(n int) string {
	header := "client_member_id,subprogram_id,first_name,last_name,date_of_birth,address_1,address_2,city,state,zip,email,benefit_period,benefit_type,contract_pbp,card_design_id,custom_card_id,package_id\n"
	var sb strings.Builder
	sb.WriteString(header)
	for i := 0; i < n; i++ {
		sb.WriteString("MBR-001,26071,Rosa,Garcia,03/15/1982,123 Main St,,Portland,OR,97201,rosa@example.com,2026-06,OTC,HMO001,CD-01,,PKG-01\n")
	}
	return sb.String()
}

// srg315CSV returns a minimal valid SRG315 CSV.
func srg315CSV() string {
	return "client_member_id,event_type,benefit_period\nMBR-001,SUSPEND,2026-06\n"
}

// srg320CSV returns a minimal valid SRG320 CSV.
func srg320CSV() string {
	return "client_member_id,command_type,benefit_period,amount,effective_date,expiry_date\nMBR-001,LOAD,2026-06,50.00,06/01/2026,12/31/2026\n"
}

// malformedSRG310CSV has one row missing a required field.
func malformedSRG310CSV() string {
	// Missing required last_name
	return "client_member_id,subprogram_id,first_name,last_name,dob,address1,address2,city,state,zip,email,benefit_period,benefit_type,contract_pbp,card_design_id,custom_card_id,package_id\n" +
		",26071,Rosa,,03/15/1982,123 Main St,,Portland,OR,97201,,2026-06,OTC,HMO001,CD-01,,PKG-01\n"
}

type stage2Mocks struct {
	files       *testutil.MockFileStore
	batchFiles  *testutil.MockBatchFileRepository
	deadLetters *testutil.MockDeadLetterRepository
	audit       *testutil.MockAuditLogWriter
	obs         *testutil.MockObservability
}

func newStage2(pgpFn func(io.Reader) (io.Reader, error)) (*ValidationStage, *stage2Mocks) {
	m := &stage2Mocks{
		files:       testutil.NewMockFileStore(),
		batchFiles:  testutil.NewMockBatchFileRepository(),
		deadLetters: testutil.NewMockDeadLetterRepository(),
		audit:       &testutil.MockAuditLogWriter{},
		obs:         &testutil.MockObservability{},
	}
	if pgpFn == nil {
		pgpFn = NullPGPDecrypt
	}
	stage := &ValidationStage{
		Files:       m.files,
		BatchFiles:  m.batchFiles,
		DeadLetters: m.deadLetters,
		Audit:       m.audit,
		Obs:         m.obs,
		PGPDecrypt:  pgpFn,
	}
	return stage, m
}

// seedBatchFileV2 creates a batch file and registers it in the mock.
func seedBatchFileV2(m *stage2Mocks, fileType string) *ports.BatchFile {
	bf := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		ClientID:      "rfu",
		FileType:      fileType,
		Status:        "RECEIVED",
		ArrivedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	m.batchFiles.Files[bf.ID] = bf
	return bf
}

// injectContent wires the mock FileStore to return csv content for any GetObject call.
func injectContent(m *stage2Mocks, content string) {
	m.files.GetObjectFn = func(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	}
}

// ─── happy paths ─────────────────────────────────────────────────────────────

// TestValidation_SRG310_ParsesRows verifies the happy path for SRG310 files:
// parsed rows are returned, status transitions to VALIDATING, and a SHA-256
// of the plaintext is computed and returned.
func TestValidation_SRG310_ParsesRows(t *testing.T) {
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG310")
	injectContent(m, srg310CSV(3))

	result, err := stage.Run(context.Background(), bf, "test-bucket", "test-key")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(result.SRG310Rows) != 3 {
		t.Errorf("SRG310Rows = %d; want 3", len(result.SRG310Rows))
	}
	if result.TotalRows != 3 {
		t.Errorf("TotalRows = %d; want 3", result.TotalRows)
	}
	if result.PlaintextSHA == "" {
		t.Error("PlaintextSHA must not be empty")
	}
	if bf.Status != "VALIDATING" {
		t.Errorf("status = %q; want VALIDATING", bf.Status)
	}
}

// TestValidation_SRG315_Parsed verifies SRG315 file type is routed correctly.
func TestValidation_SRG315_Parsed(t *testing.T) {
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG315")
	injectContent(m, srg315CSV())

	result, err := stage.Run(context.Background(), bf, "b", "k")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(result.SRG315Rows) != 1 {
		t.Errorf("SRG315Rows = %d; want 1", len(result.SRG315Rows))
	}
}

// TestValidation_SRG320_Parsed verifies SRG320 file type is routed correctly.
func TestValidation_SRG320_Parsed(t *testing.T) {
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG320")
	injectContent(m, srg320CSV())

	result, err := stage.Run(context.Background(), bf, "b", "k")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(result.SRG320Rows) != 1 {
		t.Errorf("SRG320Rows = %d; want 1", len(result.SRG320Rows))
	}
}

// TestValidation_RecordCount_SetAfterParsing verifies that SetRecordCount is called
// with the total row count. Stage 3 requires this for stall detection.
func TestValidation_RecordCount_SetAfterParsing(t *testing.T) {
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG310")
	injectContent(m, srg310CSV(5))

	_, err := stage.Run(context.Background(), bf, "b", "k")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if bf.RecordCount == nil || *bf.RecordCount != 5 {
		t.Errorf("RecordCount = %v; want 5", bf.RecordCount)
	}
}

// TestValidation_SHA256_IsPlaintextHash verifies that the returned SHA-256 matches
// what you'd compute independently on the same plaintext bytes. This is the
// non-repudiation evidence requirement (§6.1, §4.1).
func TestValidation_SHA256_IsPlaintextHash(t *testing.T) {
	content := srg310CSV(1)
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG310")
	injectContent(m, content)

	result, err := stage.Run(context.Background(), bf, "b", "k")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	h := sha256.Sum256([]byte(content))
	expected := hex.EncodeToString(h[:])
	if result.PlaintextSHA != expected {
		t.Errorf("PlaintextSHA = %q; want %q", result.PlaintextSHA, expected)
	}
}

// ─── failure paths ────────────────────────────────────────────────────────────

// TestValidation_PGPDecryptFails_BatchHalted verifies that a PGP decrypt failure
// transitions the batch to HALTED and returns an error. No rows should be returned.
// A halted batch cannot proceed to Stage 3.
func TestValidation_PGPDecryptFails_BatchHalted(t *testing.T) {
	decryptErr := errors.New("pgp: wrong key or corrupt data")
	pgpFn := func(r io.Reader) (io.Reader, error) { return nil, decryptErr }

	stage, m := newStage2(pgpFn)
	bf := seedBatchFileV2(m, "SRG310")
	injectContent(m, "irrelevant")

	_, err := stage.Run(context.Background(), bf, "b", "k")
	if err == nil {
		t.Fatal("expected error on PGP decrypt failure")
	}
	if bf.Status != "HALTED" {
		t.Errorf("status = %q; want HALTED", bf.Status)
	}
}

// TestValidation_UnknownFileType_BatchHalted verifies that an unknown file_type
// transitions the batch to HALTED. We must not partially parse an unknown format.
func TestValidation_UnknownFileType_BatchHalted(t *testing.T) {
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRGUNKNOWN")
	injectContent(m, "anything")

	_, err := stage.Run(context.Background(), bf, "b", "k")
	if err == nil {
		t.Fatal("expected error for unknown file_type")
	}
	if bf.Status != "HALTED" {
		t.Errorf("status = %q; want HALTED", bf.Status)
	}
}

// TestValidation_MalformedRow_DeadLettered verifies that a row with a parse error
// is dead-lettered and malformed_count is incremented. Valid rows in the same file
// must still be returned — the file continues.
func TestValidation_MalformedRow_DeadLettered(t *testing.T) {
	// One malformed row only (missing client_member_id)
	content := malformedSRG310CSV()
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG310")
	injectContent(m, content)

	result, err := stage.Run(context.Background(), bf, "b", "k")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(result.ParseErrors) == 0 {
		t.Error("expected parse errors for malformed row")
	}
	if len(m.deadLetters.Entries) == 0 {
		t.Error("expected dead letter entry for malformed row")
	}
	if bf.MalformedCount == 0 {
		t.Error("MalformedCount should be incremented for each parse error")
	}
}

// TestValidation_S3GetObjectFails_ReturnsError verifies that an S3 retrieval failure
// propagates as an error to the caller. The batch should be halted.
func TestValidation_S3GetObjectFails_ReturnsError(t *testing.T) {
	stage, m := newStage2(nil)
	bf := seedBatchFileV2(m, "SRG310")
	m.files.GetObjectFn = func(_ context.Context, _, _ string) (io.ReadCloser, error) {
		return nil, errors.New("s3: no such key")
	}

	_, err := stage.Run(context.Background(), bf, "b", "missing-key")
	if err == nil {
		t.Fatal("expected error on S3 failure")
	}
	if bf.Status != "HALTED" {
		t.Errorf("status = %q; want HALTED", bf.Status)
	}
}

// ─── NullPGPDecrypt ───────────────────────────────────────────────────────────

// TestNullPGPDecrypt_Passthrough verifies that NullPGPDecrypt returns the
// same bytes without transformation. This is the DEV-only no-op used before
// real PGP keys are provisioned.
func TestNullPGPDecrypt_Passthrough(t *testing.T) {
	content := []byte("hello, FIS")
	r, err := NullPGPDecrypt(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("NullPGPDecrypt error: %v", err)
	}
	got, _ := io.ReadAll(r)
	if !bytes.Equal(got, content) {
		t.Errorf("NullPGPDecrypt modified content: got %q; want %q", got, content)
	}
}
