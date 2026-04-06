//go:build smoke

// Package main — Option A smoke test for the ingest-task pipeline.
//
// This test exercises the full wiring graph (Stages 1–5 ingest mode) using
// in-memory fake dependencies — no AWS, no Aurora, no network calls required.
//
// Architecture change v00.02.11: ingest-task now runs Stages 1–5 only.
// Reconciliation (Stage 6) runs in reconcile mode triggered by return.file.arrived.
// Status terminal for ingest mode is SUBMITTED (set by Stage 5).
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
	fisp "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage1 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage2 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage3 "github.com/walker-morse/batch/member_enrollment/pipeline"
)

// ─── fakeFileStore ────────────────────────────────────────────────────────────

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
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

// ─── minimal SRG310 fixture ───────────────────────────────────────────────────

func minimalSRG310() []byte {
	header := "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id"
	row    := "SMOKE-001|Jane|Smoketest|1985-06-15|123 Test Ave|Portland|OR|97201|2026-06|26071|OTC|PKG-001"
	return []byte(header + "\n" + row + "\n")
}

const (
	smokeSubprogramID = "26071"
	smokeTenantID     = "rfu-oregon"
	smokeClientID     = "rfu"
	smokeBucket       = "inbound-bucket"
	smokeSRGKey       = "inbound-raw/2026/06/01/SMOKE-001.srg310.psv"
	smokeStagedBucket = "staged-bucket"
	smokeSCPBucket    = "scp-exchange-bucket"
)

// ─── helper: build ingest-mode deps ──────────────────────────────────────────

func buildIngestDeps(
	fileStore *fakeFileStore,
	batchFiles *testutil.MockBatchFileRepository,
	domainCmds *testutil.MockDomainCommandRepository,
	deadLetters *testutil.MockDeadLetterRepository,
	batchRecords *testutil.MockBatchRecordWriter,
	domainState *testutil.MockDomainStateWriter,
	programLookup *testutil.MockProgramLookup,
	stagedRecordsMock *testutil.MockBatchRecordsLister,
	assembler *testutil.MockFISBatchAssembler,
	audit *testutil.MockAuditLogWriter,
	obs *observability.NoopObservability,
) *PipelineDeps {
	return &PipelineDeps{
		FileStore: fileStore,
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
		Stage4: &fisp.BatchAssemblyStage{
			Assembler:         assembler,
			Files:             fileStore,
			BatchFiles:        batchFiles,
			StagedRecords:     stagedRecordsMock,
			Audit:             audit,
			Obs:               obs,
			PGPEncrypt:        fisp.NullPGPEncrypt,
			StagedBucket:      smokeStagedBucket,
			FISExchangeBucket: smokeSCPBucket,
		},
		Stage5: &fisp.ProcessorDepositStage{
			Files:             fileStore,
			BatchFiles:        batchFiles,
			Audit:             audit,
			Obs:               obs,
			FISExchangeBucket: smokeSCPBucket,
		},
		Stage7: &fisp.ReconciliationStage{
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
}

// ─── smoke tests ─────────────────────────────────────────────────────────────

// TestSmoke_PipelineStages1Through5_WithFakeDeps exercises the full ingest wiring
// (Stages 1–5) using in-memory fake dependencies.
//
// Terminal status: SUBMITTED (Stage 5). Reconciliation is a separate reconcile-task
// triggered by EventBridge return.file.arrived (v00.02.11 architecture split).
func TestSmoke_PipelineStages1Through5_WithFakeDeps(t *testing.T) {
	ctx := context.Background()

	fileStore      := newFakeFileStore()
	batchFiles     := testutil.NewMockBatchFileRepository()
	domainCmds     := testutil.NewMockDomainCommandRepository()
	deadLetters    := testutil.NewMockDeadLetterRepository()
	batchRecords   := testutil.NewMockBatchRecordWriter()
	domainState    := testutil.NewMockDomainStateWriter()
	programLookup  := testutil.NewMockProgramLookup()
	audit          := &testutil.MockAuditLogWriter{}
	obs            := &observability.NoopObservability{}

	fileStore.objects[smokeBucket+"/"+smokeSRGKey] = minimalSRG310()
	programID := uuid.New()
	programLookup.Register(smokeTenantID, smokeSubprogramID, programID)

	correlationID := uuid.New()
	subID := int64(26071)
	stagedRecordsMock := testutil.NewMockBatchRecordsLister()
	stagedRecordsMock.Register(correlationID.String(), &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "SMOKE-001", ProgramID: programID, SubprogramID: &subID},
		},
	})

	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 1, "fake-fis-record-content")

	cfg := &PipelineConfig{
		CorrelationID:     correlationID,
		TenantID:          smokeTenantID,
		ClientID:          smokeClientID,
		S3Bucket:          smokeBucket,
		S3Key:             smokeSRGKey,
		FileType:          "SRG310",
		PipelineEnv:       "DEV",
		StagedBucket:      smokeStagedBucket,
		SCPExchangeBucket: smokeSCPBucket,
		SCPCompanyID:      "MORSEUSA0",
		// Mode defaults to "" which dispatches to runIngest
	}

	deps := buildIngestDeps(
		fileStore, batchFiles, domainCmds, deadLetters, batchRecords,
		domainState, programLookup, stagedRecordsMock, assembler, audit, obs,
	)

	err := runWithDeps(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("runWithDeps returned error: %v", err)
	}

	if len(batchFiles.Files) != 1 {
		t.Fatalf("batch_files row count = %d; want 1", len(batchFiles.Files))
	}

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
	// Ingest mode terminal status is SUBMITTED (Stage 5).
	// Reconciliation (COMPLETE) happens in reconcile mode via return.file.arrived.
	if batchFile.Status != "SUBMITTED" {
		t.Errorf("batch_file.Status = %q; want SUBMITTED (ingest mode exits after Stage 5)", batchFile.Status)
	}

	if len(deadLetters.Entries) > 0 {
		reasons := make([]string, len(deadLetters.Entries))
		for i, e := range deadLetters.Entries {
			reasons[i] = e.FailureReason
		}
		t.Errorf("unexpected dead letter entries: %s", strings.Join(reasons, "; "))
	}

	if len(batchRecords.RT30) != 1 {
		t.Errorf("staged RT30 count = %d; want 1", len(batchRecords.RT30))
	}
	if len(batchRecords.RT30) > 0 && batchRecords.RT30[0].ClientMemberID != "SMOKE-001" {
		t.Errorf("RT30[0].ClientMemberID = %q; want SMOKE-001", batchRecords.RT30[0].ClientMemberID)
	}

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

	if len(domainState.Consumers) != 1 {
		t.Errorf("consumers upserted = %d; want 1", len(domainState.Consumers))
	}

	foundFISFile := false
	for k := range fileStore.objects {
		if strings.HasPrefix(k, smokeSCPBucket+"/outbound/") {
			foundFISFile = true
			break
		}
	}
	if !foundFISFile {
		t.Error("no assembled SCP file found in scp-exchange bucket")
	}

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

	badSRG := []byte(
		"client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id\n" +
		"SMOKE-001|Jane|Good|1985-06-15|123 Test Ave|Portland|OR|97201|2026-06|26071|OTC|PKG-001\n" +
		"SMOKE-BAD|John|Bad||456 Error Rd|Portland|OR|97202|2026-06|26071|OTC|PKG-001\n",
	)
	fileStore.objects[smokeBucket+"/"+smokeSRGKey] = badSRG
	programID2 := uuid.New()
	programLookup.Register(smokeTenantID, smokeSubprogramID, programID2)

	corrID2 := uuid.New()
	subID2 := int64(26071)
	stagedRecordsMock2 := testutil.NewMockBatchRecordsLister()
	stagedRecordsMock2.Register(corrID2.String(), &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "SMOKE-001", ProgramID: programID2, SubprogramID: &subID2},
		},
	})

	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 1, "fake")

	cfg := &PipelineConfig{
		CorrelationID:     corrID2,
		TenantID:          smokeTenantID,
		ClientID:          smokeClientID,
		S3Bucket:          smokeBucket,
		S3Key:             smokeSRGKey,
		FileType:          "SRG310",
		PipelineEnv:       "DEV",
		StagedBucket:      smokeStagedBucket,
		SCPExchangeBucket: smokeSCPBucket,
	}

	deps := buildIngestDeps(
		fileStore, batchFiles, domainCmds, deadLetters, batchRecords,
		domainState, programLookup, stagedRecordsMock2, assembler, audit, obs,
	)

	err := runWithDeps(ctx, cfg, deps)
	if err != nil {
		t.Fatalf("runWithDeps returned error: %v", err)
	}
	if len(deadLetters.Entries) == 0 {
		t.Error("expected dead letter entry for malformed row; got none")
	}
	if len(batchRecords.RT30) != 1 {
		t.Errorf("staged RT30 count = %d; want 1 (good row only)", len(batchRecords.RT30))
	}
}

// TestSmoke_EmptySRG310_AssemblySkipped verifies that an empty file (header only)
// short-circuits at the staged=0 guard — Stages 4 and 5 are not called.
// This is the correct behaviour: do not assemble or transfer an empty file to FIS.
func TestSmoke_EmptySRG310_AssemblySkipped(t *testing.T) {
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
		"client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type\n",
	)

	// assembler should NOT be called — zero staged records triggers early exit
	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 0, "")

	cfg := &PipelineConfig{
		CorrelationID: uuid.New(), TenantID: smokeTenantID, ClientID: smokeClientID,
		S3Bucket: smokeBucket, S3Key: smokeSRGKey, FileType: "SRG310",
		PipelineEnv: "DEV", StagedBucket: smokeStagedBucket, SCPExchangeBucket: smokeSCPBucket,
	}

	deps := buildIngestDeps(
		fileStore, batchFiles, domainCmds, deadLetters, batchRecords,
		domainState, programLookup, testutil.NewMockBatchRecordsLister(), assembler, audit, obs,
	)

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
	// Assembler must NOT have been called — staged=0 guard exits before Stage 4
	if len(assembler.Calls) > 0 {
		t.Errorf("assembler.AssembleFile called %d times despite staged=0 — Stage 4 guard not working", len(assembler.Calls))
	}
	// No FIS file should appear in the exchange bucket
	for k := range fileStore.objects {
		if strings.HasPrefix(k, smokeSCPBucket+"/") {
			t.Errorf("unexpected file in scp-exchange bucket: %s", k)
		}
	}
}
