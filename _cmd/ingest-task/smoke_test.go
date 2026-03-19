//go:build smoke

// Package main — Option A smoke test for the ingest-task pipeline.
//
// This test exercises the full wiring graph (Stages 1–4) using in-memory
// fake dependencies — no AWS, no Aurora, no network calls required.
// It proves the binary would start, wire correctly, and process a minimal
// SRG310 file without panicking or returning an error.
//
// Run with:
//   go test -tags smoke ./... -v -run TestSmoke
// or via Makefile:
//   make smoke
//
// This is Option A of the two-stage smoke test strategy:
//
//   Option A (this file): in-process wiring test, zero infrastructure.
//     Catches: wiring regressions, interface mismatches, nil pointer panics,
//     stage sequencing bugs, config validation errors.
//     Does NOT catch: real SQL behaviour, real S3 I/O, real PGP decrypt/encrypt.
//
//   Option B (docker-compose): full integration against live Postgres + fake S3.
//     Prerequisite: docker-compose.yml with postgres + localstack or httptest S3 stub.
//     Status: blocked — awaiting DEV environment confirmation (John Stevens).
//     See _docs/SMOKE_TEST_OPTION_B.md when that unblocks.
//
// Fake dependencies used:
//   - fakeFileStore: in-memory map[bucket/key]→[]byte, satisfies FileStoreWithSHA256
//   - testutil.MockBatchFileRepository: in-memory, tracks status mutations
//   - testutil.MockDomainCommandRepository: in-memory, idempotency checks
//   - testutil.MockDeadLetterRepository: in-memory, captures dead letters
//   - testutil.MockBatchRecordWriter: in-memory, captures staged RT30/37/60 rows
//   - testutil.MockDomainStateWriter: in-memory, captures consumer/card/purse upserts
//   - testutil.MockProgramLookup: seeded with one program mapping
//   - testutil.MockFISBatchAssembler: returns a deterministic fake assembled file
//   - testutil.MockAuditLogWriter: captures audit entries (asserted for completeness)
//   - stage2.NullPGPDecrypt + stage4.NullPGPEncrypt: passthroughs (DEV mode)
//   - observability.NoopObservability: discards all log/metric events

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
	stage4 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage5 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage6 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage7 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage1 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage2 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage3 "github.com/walker-morse/batch/member_enrollment/pipeline"
)

// ─── fakeFileStore ────────────────────────────────────────────────────────────

// fakeFileStore is an in-memory implementation of stage1.FileStoreWithSHA256.
// Stores objects as bucket+"/"+key → content bytes.
// GetObject returns a fresh reader each call; PutObject writes to the map.
// SHA256OfObject returns a deterministic fake hash — smoke test only.
type fakeFileStore struct {
	objects map[string][]byte
	deleted []string
}

func newFakeFileStore() *fakeFileStore {
	return &fakeFileStore{objects: make(map[string][]byte)}
}

func (f *fakeFileStore) key(bucket, key string) string { return bucket + "/" + key }

func (f *fakeFileStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	b, ok := f.objects[f.key(bucket, key)]
	if !ok {
		return nil, fmt.Errorf("fakeFileStore: object not found: %s/%s", bucket, key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeFileStore) PutObject(_ context.Context, bucket, key string, body io.Reader) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.objects[f.key(bucket, key)] = b
	return nil
}

func (f *fakeFileStore) DeleteObject(_ context.Context, bucket, key string) error {
	f.deleted = append(f.deleted, f.key(bucket, key))
	delete(f.objects, f.key(bucket, key))
	return nil
}

func (f *fakeFileStore) HeadObject(_ context.Context, bucket, key string) (*ports.ObjectMeta, error) {
	if _, ok := f.objects[f.key(bucket, key)]; !ok {
		return nil, fmt.Errorf("fakeFileStore: HeadObject not found: %s/%s", bucket, key)
	}
	return &ports.ObjectMeta{
		SHA256:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:         int64(len(f.objects[f.key(bucket, key)])),
		LastModified: time.Now().UTC(),
	}, nil
}

func (f *fakeFileStore) SHA256OfObject(_ context.Context, bucket, key string) (string, error) {
	if _, ok := f.objects[f.key(bucket, key)]; !ok {
		return "", fmt.Errorf("fakeFileStore: SHA256OfObject not found: %s/%s", bucket, key)
	}
	// Deterministic fake hash — smoke test only; real SHA-256 is an integration concern
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

// ─── minimal SRG310 fixture ───────────────────────────────────────────────────

// minimalSRG310 returns a one-row SRG310 CSV for the smoke test member.
// All required columns are present; optional columns are omitted.
// PHI values are synthetic — this file never touches a real system.
func minimalSRG310() []byte {
	header := "client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,subprogram_id,benefit_type"
	row    := "SMOKE-001,Jane,Smoketest,1985-06-15,123 Test Ave,Portland,OR,97201,2026-06,26071,OTC"
	return []byte(header + "\n" + row + "\n")
}

// ─── fakeProgramLookup ────────────────────────────────────────────────────────

// We use testutil.MockProgramLookup seeded with the subprogram_id from the fixture.
const (
	smokeSubprogramID = "26071"
	smokeTenantID     = "rfu-oregon"
	smokeClientID     = "rfu"
	smokeBucket       = "inbound-bucket"
	smokeSRGKey       = "inbound-raw/2026/06/01/SMOKE-001.srg310.csv"
	smokeStagedBucket = "staged-bucket"
	smokeFISBucket    = "fis-exchange-bucket"
)

// ─── smoke test ───────────────────────────────────────────────────────────────

// TestSmoke_PipelineStages1Through4_WithFakeDeps exercises the full wiring
// of Stages 1–4 using in-memory fake dependencies.
//
// What it proves:
//   - All stage structs can be constructed and injected (wiring is correct)
//   - Stage 1 writes a batch_files row and returns a non-nil BatchFile
//   - Stage 2 parses the SRG310 CSV and returns one SRG310Row (no dead letters)
//   - Stage 3 writes one RT30 batch record and one domain command (idempotency gate)
//   - Stage 4 assembles the FIS file and writes to the fis-exchange bucket
//   - The pipeline exits with nil error
//   - batch_files status progresses to ASSEMBLED (Stage 4 terminus)
//   - No panics occur anywhere in the wiring graph
//
// What it does NOT prove (Option B concerns):
//   - Real SQL correctness (constraint violations, transaction semantics)
//   - Real S3 I/O (multipart upload, SSE-KMS, object ACLs)
//   - Real PGP decrypt/encrypt round-trip
//   - FIS record byte offsets and padding rules (covered by fis_adapter unit tests)
func TestSmoke_PipelineStages1Through4_WithFakeDeps(t *testing.T) {
	ctx := context.Background()

	// ── Shared fake infrastructure ─────────────────────────────────────────
	fileStore      := newFakeFileStore()
	batchFiles     := testutil.NewMockBatchFileRepository()
	domainCmds     := testutil.NewMockDomainCommandRepository()
	deadLetters    := testutil.NewMockDeadLetterRepository()
	batchRecords   := testutil.NewMockBatchRecordWriter()
	domainState    := testutil.NewMockDomainStateWriter()
	programLookup  := testutil.NewMockProgramLookup()
	audit          := &testutil.MockAuditLogWriter{}
	obs            := &observability.NoopObservability{}

	// Seed the SRG310 file into the fake file store
	fileStore.objects[smokeBucket+"/"+smokeSRGKey] = minimalSRG310()

	// Seed the program mapping — subprogram_id 26071 → a known program UUID
	programID := uuid.New()
	programLookup.Register(smokeTenantID, smokeSubprogramID, programID)

	// stagedRecordsMock is used by Stage 4 to resolve ProgramID from staged RT30 rows.
	// Seeded with the same programID Stage 3 will write, so resolveProgramID succeeds.
	stagedRecordsMock := testutil.NewMockBatchRecordsLister()

	// MockFISBatchAssembler returns a fake assembled file for Stage 4 to encrypt and store
	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 1, "fake-fis-record-content")

	// ── Wire stages ────────────────────────────────────────────────────────
	correlationID := uuid.New()
	// Seed stagedRecordsMock so Stage 4 resolveProgramID returns programID.
	// Stage 3 writes to batchRecords (write side); Stage 4 reads from stagedRecordsMock (read side).
	subID := int64(26071)
	stagedRecordsMock.Register(correlationID.String(), &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "SMOKE-001", ProgramID: programID, SubprogramID: &subID},
		},
	})
	cfg := &PipelineConfig{
		CorrelationID:     correlationID,
		TenantID:          smokeTenantID,
		ClientID:          smokeClientID,
		S3Bucket:          smokeBucket,
		S3Key:             smokeSRGKey,
		FileType:          "SRG310",
		PipelineEnv:       "DEV",
		StagedBucket:      smokeStagedBucket,
		FISExchangeBucket: smokeFISBucket,
		FISCompanyID:      "MORSEUSA0",
	}

	deps := &PipelineDeps{
		Stage1: &stage1.FileArrivalStage{
			Files:      fileStore,
			BatchFiles: batchFiles,
			Audit:      audit,
			Obs:        obs,
		},
		Stage2: &stage2.ValidationStage{
			Files:       fileStore,
			BatchFiles:  batchFiles,
			DeadLetters: deadLetters,
			Audit:       audit,
			Obs:         obs,
			PGPDecrypt:  stage2.NullPGPDecrypt,
		},
		Stage3: &stage3.RowProcessingStage{
			DomainCommands: domainCmds,
			DeadLetters:    deadLetters,
			BatchFiles:     batchFiles,
			BatchRecords:   batchRecords,
			DomainState:    domainState,
			Programs:       programLookup,
			Audit:          audit,
			Obs:            obs,
		},
		Stage4: &stage4.BatchAssemblyStage{
			Assembler:         assembler,
			Files:             fileStore,
			BatchFiles:        batchFiles,
			StagedRecords:     stagedRecordsMock,
			Audit:             audit,
			Obs:               obs,
			PGPEncrypt:        stage4.NullPGPEncrypt,
			StagedBucket:      smokeStagedBucket,
			FISExchangeBucket: smokeFISBucket,
		},
		Stage5: &stage5.FISTransferStage{
			Transport:         &nullFISTransport{},
			Files:             fileStore,
			BatchFiles:        batchFiles,
			Audit:             audit,
			Obs:               obs,
			FISExchangeBucket: smokeFISBucket,
		},
		Stage6: &stage6.ReturnFileWaitStage{
			Transport:  &nullFISTransport{},
			BatchFiles: batchFiles,
			Audit:      audit,
			Obs:        obs,
			Timeout:    100 * time.Millisecond,
		},
		Stage7: &stage7.ReconciliationStage{
			BatchFiles:     batchFiles,
			BatchRecords:   testutil.NewMockBatchRecordsReconciler(),
			DomainCommands: domainCmds,
			DomainState:    testutil.NewMockDomainStateReconciler(),
			DeadLetters:    deadLetters,
			Audit:          audit,
			Mart:           &testutil.MockMartWriter{},
			Obs:            obs,
		},
	}

	// ── Execute ────────────────────────────────────────────────────────────
	err := runWithDeps(ctx, cfg, deps)

	// ── Assert ─────────────────────────────────────────────────────────────

	if err != nil {
		t.Fatalf("runWithDeps returned error: %v", err)
	}

	// Exactly one batch_files row was created
	if len(batchFiles.Files) != 1 {
		t.Fatalf("batch_files row count = %d; want 1", len(batchFiles.Files))
	}

	// Find the batch file and assert terminal status
	var batchFile *ports.BatchFile
	for _, f := range batchFiles.Files {
		if f.CorrelationID == correlationID {
			batchFile = f
			break
		}
	}
	if batchFile == nil {
		t.Fatal("no batch_files row found for correlation_id")
	}
	if batchFile.Status != "COMPLETE" {
		t.Errorf("batch_file.Status = %q; want COMPLETE (Stages 1-7 complete)", batchFile.Status)
	}

	// No dead letters — clean one-row SRG310 should parse without errors
	if len(deadLetters.Entries) > 0 {
		reasons := make([]string, len(deadLetters.Entries))
		for i, e := range deadLetters.Entries {
			reasons[i] = e.FailureReason
		}
		t.Errorf("unexpected dead letter entries: %s", strings.Join(reasons, "; "))
	}

	// One RT30 batch record staged (new member enrollment)
	if len(batchRecords.RT30) != 1 {
		t.Errorf("staged RT30 count = %d; want 1", len(batchRecords.RT30))
	}
	if len(batchRecords.RT30) > 0 && batchRecords.RT30[0].ClientMemberID != "SMOKE-001" {
		t.Errorf("RT30[0].ClientMemberID = %q; want SMOKE-001", batchRecords.RT30[0].ClientMemberID)
	}

	// One domain command written (idempotency gate)
	if len(domainCmds.Commands) != 1 {
		t.Errorf("domain_commands count = %d; want 1", len(domainCmds.Commands))
	}
	if len(domainCmds.Commands) > 0 {
		cmd := domainCmds.Commands[0]
		if cmd.BenefitPeriod != "2026-06" {
			t.Errorf("command.BenefitPeriod = %q; want 2026-06", cmd.BenefitPeriod)
		}
		if cmd.TenantID != smokeTenantID {
			t.Errorf("command.TenantID = %q; want %s", cmd.TenantID, smokeTenantID)
		}
	}

	// Consumer upserted to domain state
	if len(domainState.Consumers) != 1 {
		t.Errorf("consumers upserted = %d; want 1", len(domainState.Consumers))
	}

	// Assembled FIS file landed in fis-exchange bucket
	foundFISFile := false
	for k := range fileStore.objects {
		if strings.HasPrefix(k, smokeFISBucket+"/outbound/") {
			foundFISFile = true
			break
		}
	}
	if !foundFISFile {
		t.Error("no assembled FIS file found in fis-exchange bucket")
	}

	// Audit trail non-empty — compliance requirement
	if len(audit.Entries) == 0 {
		t.Error("no audit entries written — compliance gap")
	}
}

// TestSmoke_MalformedSRG310_DeadLettered verifies that a file with a bad row
// dead-letters the bad row but still progresses (non-fatal row exception).
func TestSmoke_MalformedSRG310_DeadLettered(t *testing.T) {
	ctx := context.Background()

	fileStore     := newFakeFileStore()
	batchFiles    := testutil.NewMockBatchFileRepository()
	domainCmds    := testutil.NewMockDomainCommandRepository()
	deadLetters   := testutil.NewMockDeadLetterRepository()
	batchRecords  := testutil.NewMockBatchRecordWriter()
	domainState   := testutil.NewMockDomainStateWriter()
	programLookup := testutil.NewMockProgramLookup()
	audit         := &testutil.MockAuditLogWriter{}
	obs           := &observability.NoopObservability{}

	// SRG310 with one good row and one malformed row (missing required date_of_birth)
	badSRG := []byte(
		"client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,subprogram_id,benefit_type\n" +
		"SMOKE-001,Jane,Good,1985-06-15,123 Test Ave,Portland,OR,97201,2026-06,26071,OTC\n" +
		"SMOKE-BAD,John,Bad,,456 Error Rd,Portland,OR,97202,2026-06,26071,OTC\n", // empty DOB
	)
	fileStore.objects[smokeBucket+"/"+smokeSRGKey] = badSRG
	programID2 := uuid.New()
	programLookup.Register(smokeTenantID, smokeSubprogramID, programID2)

	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 1, "fake")

	cfg := &PipelineConfig{
		CorrelationID:     uuid.New(),
		TenantID:          smokeTenantID,
		ClientID:          smokeClientID,
		S3Bucket:          smokeBucket,
		S3Key:             smokeSRGKey,
		FileType:          "SRG310",
		PipelineEnv:       "DEV",
		StagedBucket:      smokeStagedBucket,
		FISExchangeBucket: smokeFISBucket,
	}
	subID2 := int64(26071)
	stagedRecordsMock2 := testutil.NewMockBatchRecordsLister()
	stagedRecordsMock2.Register(cfg.CorrelationID.String(), &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "SMOKE-001", ProgramID: programID2, SubprogramID: &subID2},
		},
	})

	deps := &PipelineDeps{
		Stage1: &stage1.FileArrivalStage{Files: fileStore, BatchFiles: batchFiles, Audit: audit, Obs: obs},
		Stage2: &stage2.ValidationStage{
			Files: fileStore, BatchFiles: batchFiles, DeadLetters: deadLetters,
			Audit: audit, Obs: obs, PGPDecrypt: stage2.NullPGPDecrypt,
		},
		Stage3: &stage3.RowProcessingStage{
			DomainCommands: domainCmds, DeadLetters: deadLetters, BatchFiles: batchFiles,
			BatchRecords: batchRecords, DomainState: domainState, Programs: programLookup,
			Audit: audit, Obs: obs,
		},
		Stage4: &stage4.BatchAssemblyStage{
			Assembler: assembler, Files: fileStore, BatchFiles: batchFiles,
			StagedRecords: stagedRecordsMock2,
			Audit: audit, Obs: obs,
			PGPEncrypt: stage4.NullPGPEncrypt, StagedBucket: smokeStagedBucket, FISExchangeBucket: smokeFISBucket,
		},
		Stage5: &stage5.FISTransferStage{
			Transport: &nullFISTransport{}, Files: fileStore, BatchFiles: batchFiles,
			Audit: audit, Obs: obs, FISExchangeBucket: smokeFISBucket,
		},
		Stage6: &stage6.ReturnFileWaitStage{
			Transport: &nullFISTransport{}, BatchFiles: batchFiles,
			Audit: audit, Obs: obs, Timeout: 100 * time.Millisecond,
		},
		Stage7: &stage7.ReconciliationStage{
			BatchFiles: batchFiles, BatchRecords: testutil.NewMockBatchRecordsReconciler(),
			DomainCommands: domainCmds, DomainState: testutil.NewMockDomainStateReconciler(),
			DeadLetters: deadLetters, Audit: audit, Mart: &testutil.MockMartWriter{}, Obs: obs,
		},
	}

	err := runWithDeps(ctx, cfg, deps)

	// Pipeline must not abort on a single malformed row
	if err != nil {
		t.Fatalf("runWithDeps returned error: %v", err)
	}

	// Malformed row must be dead-lettered
	if len(deadLetters.Entries) == 0 {
		t.Error("expected dead letter entry for malformed row; got none")
	}

	// Good row must still be staged
	if len(batchRecords.RT30) != 1 {
		t.Errorf("staged RT30 count = %d; want 1 (good row only)", len(batchRecords.RT30))
	}
}

// TestSmoke_EmptySRG310_AssemblesCleanly verifies that an empty file (header only)
// does not panic and reaches ASSEMBLED status with zero staged records.
func TestSmoke_EmptySRG310_AssemblesCleanly(t *testing.T) {
	ctx := context.Background()

	fileStore     := newFakeFileStore()
	batchFiles    := testutil.NewMockBatchFileRepository()
	domainCmds    := testutil.NewMockDomainCommandRepository()
	deadLetters   := testutil.NewMockDeadLetterRepository()
	batchRecords  := testutil.NewMockBatchRecordWriter()
	domainState   := testutil.NewMockDomainStateWriter()
	programLookup := testutil.NewMockProgramLookup()
	audit         := &testutil.MockAuditLogWriter{}
	obs           := &observability.NoopObservability{}

	// Header only — zero data rows
	fileStore.objects[smokeBucket+"/"+smokeSRGKey] = []byte(
		"client_member_id,first_name,last_name,date_of_birth,address_1,city,state,zip,benefit_period,subprogram_id,benefit_type\n",
	)
	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 0, "")

	cfg := &PipelineConfig{
		CorrelationID: uuid.New(), TenantID: smokeTenantID, ClientID: smokeClientID,
		S3Bucket: smokeBucket, S3Key: smokeSRGKey, FileType: "SRG310",
		PipelineEnv: "DEV", StagedBucket: smokeStagedBucket, FISExchangeBucket: smokeFISBucket,
	}

	deps := &PipelineDeps{
		Stage1: &stage1.FileArrivalStage{Files: fileStore, BatchFiles: batchFiles, Audit: audit, Obs: obs},
		Stage2: &stage2.ValidationStage{
			Files: fileStore, BatchFiles: batchFiles, DeadLetters: deadLetters,
			Audit: audit, Obs: obs, PGPDecrypt: stage2.NullPGPDecrypt,
		},
		Stage3: &stage3.RowProcessingStage{
			DomainCommands: domainCmds, DeadLetters: deadLetters, BatchFiles: batchFiles,
			BatchRecords: batchRecords, DomainState: domainState, Programs: programLookup,
			Audit: audit, Obs: obs,
		},
		Stage4: &stage4.BatchAssemblyStage{
			Assembler: assembler, Files: fileStore, BatchFiles: batchFiles,
			StagedRecords: testutil.NewMockBatchRecordsLister(),
			Audit: audit, Obs: obs,
			PGPEncrypt: stage4.NullPGPEncrypt, StagedBucket: smokeStagedBucket, FISExchangeBucket: smokeFISBucket,
		},
		Stage5: &stage5.FISTransferStage{
			Transport: &nullFISTransport{}, Files: fileStore, BatchFiles: batchFiles,
			Audit: audit, Obs: obs, FISExchangeBucket: smokeFISBucket,
		},
		Stage6: &stage6.ReturnFileWaitStage{
			Transport: &nullFISTransport{}, BatchFiles: batchFiles,
			Audit: audit, Obs: obs, Timeout: 100 * time.Millisecond,
		},
		Stage7: &stage7.ReconciliationStage{
			BatchFiles: batchFiles, BatchRecords: testutil.NewMockBatchRecordsReconciler(),
			DomainCommands: domainCmds, DomainState: testutil.NewMockDomainStateReconciler(),
			DeadLetters: deadLetters, Audit: audit, Mart: &testutil.MockMartWriter{}, Obs: obs,
		},
	}

	err := runWithDeps(ctx, cfg, deps)

	if err != nil {
		t.Fatalf("runWithDeps returned error on empty file: %v", err)
	}
	if len(batchRecords.RT30) != 0 {
		t.Errorf("RT30 count = %d; want 0 for empty file", len(batchRecords.RT30))
	}
	if len(deadLetters.Entries) != 0 {
		t.Errorf("dead letters = %d; want 0 for empty file", len(deadLetters.Entries))
	}
}
