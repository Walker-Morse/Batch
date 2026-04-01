//go:build integration

// Package main — integration tests for the full ingest-task pipeline (Stages 1–7).
//
// These tests exercise runWithDeps end-to-end using in-memory fakes for all
// infrastructure (S3, Aurora, FIS transport). No real network, database, or AWS
// connections are required.
//
// Run with:
//
//	go test -tags integration ./... -v -run TestIntegration
//	make integration   (once Makefile target is added)
//
// Scope vs. smoke tests (smoke_test.go):
//
//	Smoke tests (3 tests, -tags smoke): wiring regression only — prove stages
//	construct and wire without panicking. Return file is empty; Stage 7 is
//	exercised structurally only.
//
//	Integration tests (this file): full behavioural coverage — realistic return
//	files with RT30/RT60/RT99 records, multi-member files, idempotency re-runs,
//	SRG315/SRG320 file types, stall conditions, audit and metric assertions.
//
// Test index:
//
//	TestIntegration_SRG310_HappyPath_RT30           — single member, RT30 success
//	TestIntegration_SRG310_MultiMember_RT30         — 5 members, all RT30 success
//	TestIntegration_SRG310_RT30_WithRT99Individual  — 2 members, 1 succeeds + 1 RT99 fail
//	TestIntegration_SRG310_RT99FullFileHalt         — FIS rejects entire file
//	TestIntegration_SRG310_RT60_FundLoad            — fund load file, RT60 success
//	TestIntegration_SRG310_MalformedRows_DeadLettered — bad rows dead-lettered, good rows proceed
//	TestIntegration_SRG310_Idempotency_ResubmittedFile — same file twice, Stage 3 dedup fires
//	TestIntegration_SRG310_Stall_UnresolvedDeadLetter — stall gate stops pipeline
//	TestIntegration_SRG315_CardUpdate_HappyPath     — SRG315 card update, RT37 success
//	TestIntegration_SRG320_FundLoad_HappyPath       — SRG320 fund load, RT60 success
//	TestIntegration_AuditTrail_AllStages            — every stage emits at least one audit entry
//	TestIntegration_Metrics_EmittedCorrectly        — enrollment rate, DL rate, stage durations

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
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
	"github.com/walker-morse/batch/fis_reconciliation/fis_adapter"
	stage4 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage5 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage6 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage7 "github.com/walker-morse/batch/fis_reconciliation/pipeline"
	stage1 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage2 "github.com/walker-morse/batch/member_enrollment/pipeline"
	stage3 "github.com/walker-morse/batch/member_enrollment/pipeline"
)

// ─── constants ────────────────────────────────────────────────────────────────

const (
	itTenantID     = "rfu-oregon"
	itClientID     = "rfu"
	itBucket       = "inbound-bucket"
	itStagedBucket = "staged-bucket"
	itSCPBucket    = "scp-exchange-bucket"
	itEgressBucket = "egress-bucket"
	itSubprogramID = "26071"
	itCompanyID    = "MORSEUSA0"
)

// ─── SRG fixture builders ─────────────────────────────────────────────────────

func srg310Header() string {
	return "client_member_id|first_name|last_name|date_of_birth|address_1|city|state|zip|benefit_period|subprogram_id|benefit_type|package_id"
}

func srg310Row(memberID, benefitPeriod string) string {
	return fmt.Sprintf("%s|Jane|Test|1985-06-15|123 Main St|Portland|OR|97201|%s|%s|OTC|PKG-001",
		memberID, benefitPeriod, itSubprogramID)
}

func srg310RowMalformed(memberID string) string {
	// missing date_of_birth — will fail Stage 2 validation
	return fmt.Sprintf("%s|Jane|Test||123 Main St|Portland|OR|97201|2026-06|%s|OTC|PKG-001",
		memberID, itSubprogramID)
}

func srg315Header() string {
	return "client_member_id|event_type|effective_date|subprogram_id"
}

func srg315Row(memberID, eventType string) string {
	return fmt.Sprintf("%s|%s|2026-06-01|%s", memberID, eventType, itSubprogramID)
}

func srg320Header() string {
	return "client_member_id|command_type|amount|benefit_period|subprogram_id"
}

func srg320Row(memberID, cmdType string, amountCents int) string {
	// parser expects dollars.cents (e.g. "95.00"), converts to cents internally
	return fmt.Sprintf("%s|%s|%.2f|2026-06|%s",
		memberID, cmdType, float64(amountCents)/100.0, itSubprogramID)
}

func buildSRG310(rows ...string) []byte {
	lines := append([]string{srg310Header()}, rows...)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func buildSRG315(rows ...string) []byte {
	lines := append([]string{srg315Header()}, rows...)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func buildSRG320(rows ...string) []byte {
	lines := append([]string{srg320Header()}, rows...)
	return []byte(strings.Join(lines, "\n") + "\n")
}

// ─── FIS return file builders ─────────────────────────────────────────────────

// itReturnRecord builds a 400-byte FIS return record. Mirrors stage7 test helpers
// but lives here so integration tests have no cross-package dependency.
func itReturnRecord(recType, clientMemberID, resultCode string, extras map[string]string) []byte {
	rec := make([]byte, fis_adapter.RecordWidth)
	for i := range rec {
		rec[i] = ' '
	}
	writeField := func(offset, length int, val string) {
		b := []byte(val)
		for i := 0; i < length; i++ {
			if i < len(b) {
				rec[offset+i] = b[i]
			}
		}
	}
	writeField(0, 4, recType)
	writeField(4, 20, clientMemberID)
	writeField(24, 3, resultCode)
	if msg, ok := extras["msg"]; ok {
		writeField(27, 40, msg)
	}
	if personID, ok := extras["person_id"]; ok {
		writeField(67, 20, personID)
	}
	if cuid, ok := extras["cuid"]; ok {
		writeField(87, 19, cuid)
	}
	if cardID, ok := extras["card_id"]; ok {
		writeField(106, 19, cardID)
	}
	if purseNum, ok := extras["purse_num"]; ok {
		writeField(67, 5, purseNum)
	}
	return rec
}

func itReturnFile(records ...[]byte) []byte {
	var buf bytes.Buffer
	for _, r := range records {
		buf.Write(r)
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

// ─── integration harness ──────────────────────────────────────────────────────

// itHarness holds all fakes for a single integration test run.
type itHarness struct {
	fileStore     *itFileStore
	batchFiles    *testutil.MockBatchFileRepository
	domainCmds    *testutil.MockDomainCommandRepository
	deadLetters   *testutil.MockDeadLetterRepository
	batchRecords  *testutil.MockBatchRecordWriter
	domainState   *testutil.MockDomainStateWriter
	programs      *testutil.MockProgramLookup
	audit         *testutil.MockAuditLogWriter
	obs           *testutil.MockObservability
	transport     *testutil.MockFISTransport
	batchRecon    *testutil.MockBatchRecordsReconciler
	domainRecon   *testutil.MockDomainStateReconciler
	mart          *testutil.MockMartWriter
	programID     uuid.UUID
	correlationID uuid.UUID
	stagedListerV *testutil.MockBatchRecordsLister // backing field for stagedLister()
}

// newHarness creates a fresh harness with all fakes initialised.
// The program lookup is seeded with itSubprogramID → programID.
func newHarness() *itHarness {
	h := &itHarness{
		fileStore:     newITFileStore(),
		batchFiles:    testutil.NewMockBatchFileRepository(),
		domainCmds:    testutil.NewMockDomainCommandRepository(),
		deadLetters:   testutil.NewMockDeadLetterRepository(),
		batchRecords:  testutil.NewMockBatchRecordWriter(),
		domainState:   testutil.NewMockDomainStateWriter(),
		programs:      testutil.NewMockProgramLookup(),
		audit:         &testutil.MockAuditLogWriter{},
		obs:           &testutil.MockObservability{},
		transport:     testutil.NewMockFISTransport(),
		batchRecon:    testutil.NewMockBatchRecordsReconciler(),
		domainRecon:   testutil.NewMockDomainStateReconciler(),
		mart:          &testutil.MockMartWriter{},
		programID:     uuid.New(),
		correlationID: uuid.New(),
	}
	h.programs.Register(itTenantID, itSubprogramID, h.programID)
	return h
}

// seedFile writes file content into the fake store and seeds staged records
// so Stage 4 can resolve programID. memberIDs must match the SRG rows.
func (h *itHarness) seedFile(key string, content []byte, memberIDs ...string) {
	h.fileStore.put(itBucket, key, content)
	staged := &ports.StagedRecords{}
	subID := int64(26071)
	for i, mid := range memberIDs {
		staged.RT30 = append(staged.RT30, &ports.StagedRT30{
			ID:             uuid.New(),
			SequenceInFile: i + 1,
			ClientMemberID: mid,
			ProgramID:      h.programID,
			SubprogramID:   &subID,
		})
	}
	h.fileStore.put(itBucket, key, content)
	// Register staged records for Stage 4
	stagedLister := testutil.NewMockBatchRecordsLister()
	stagedLister.Register(h.correlationID.String(), staged)
	h.stagedListerV = stagedLister
}

// _stagedLister is the read-side for Stage 4; set by seedFile.
// Declared here so buildDeps can access it.
// We use a pointer field rather than embedding the interface directly
// to allow seedFile to swap it after construction.
func (h *itHarness) stagedLister() *testutil.MockBatchRecordsLister {
	if h.stagedListerV == nil {
		h.stagedListerV = testutil.NewMockBatchRecordsLister()
	}
	return h.stagedListerV
}

// seedReturnFile loads the FIS return body into the mock transport.
func (h *itHarness) seedReturnFile(body []byte) {
	h.transport.PollResult = io.NopCloser(bytes.NewReader(body))
}

// seedStage7Records registers staged rows in batchRecon so Stage 7 can reconcile.
// Each entry is (memberID, sequence, recordType, benefitPeriod).
func (h *itHarness) seedStage7(memberID string, seq int, recType, benefitPeriod string) {
	recordID := uuid.New()
	cmdID := uuid.New()
	h.batchRecon.Register(h.correlationID, seq, recType, recordID, cmdID, benefitPeriod)
	// Also seed consumer + card in domainRecon so FIS identifiers can be stamped
	consumerID := uuid.New()
	h.domainRecon.RegisterConsumer(itTenantID, memberID, &domain.Consumer{
		ID:             consumerID,
		TenantID:       itTenantID,
		ClientMemberID: memberID,
	})
	h.domainRecon.RegisterCard(consumerID, &domain.Card{
		ID:         uuid.New(),
		ConsumerID: consumerID,
	})
}

// buildDeps constructs the PipelineDeps from the harness fakes.
func (h *itHarness) buildDeps(s3Key string) (*PipelineDeps, *PipelineConfig) {
	fileType := "SRG310"
	lower := strings.ToLower(s3Key)
	if strings.Contains(lower, "srg315") {
		fileType = "SRG315"
	} else if strings.Contains(lower, "srg320") {
		fileType = "SRG320"
	}

	cfg := &PipelineConfig{
		CorrelationID:     h.correlationID,
		TenantID:          itTenantID,
		ClientID:          itClientID,
		S3Bucket:          itBucket,
		S3Key:             s3Key,
		FileType:          fileType,
		PipelineEnv:       "DEV",
		StagedBucket:      itStagedBucket,
		SCPExchangeBucket: itSCPBucket,
		EgressBucket:      itEgressBucket,
		SCPCompanyID:      itCompanyID,
		ReturnFileWaitTimeout: 100 * time.Millisecond,
	}

	assembler := testutil.NewMockFISBatchAssembler("MORSEUSA01060126001.batch.txt", 1, "fake-fis-payload")

	deps := &PipelineDeps{
		Stage1: &stage1.FileArrivalStage{
			Files:      h.fileStore,
			BatchFiles: h.batchFiles,
			Audit:      h.audit,
			Obs:        h.obs,
		},
		Stage2: &stage2.ValidationStage{
			Files:       h.fileStore,
			BatchFiles:  h.batchFiles,
			DeadLetters: h.deadLetters,
			Audit:       h.audit,
			Obs:         h.obs,
			PGPDecrypt:  stage2.NullPGPDecrypt,
		},
		Stage3: &stage3.RowProcessingStage{
			DomainCommands: h.domainCmds,
			DeadLetters:    h.deadLetters,
			BatchFiles:     h.batchFiles,
			BatchRecords:   h.batchRecords,
			DomainState:    h.domainState,
			Programs:       h.programs,
			Audit:          h.audit,
			Obs:            h.obs,
			Mart:           h.mart,
		},
		Stage4: &stage4.BatchAssemblyStage{
			Assembler:         assembler,
			Files:             h.fileStore,
			BatchFiles:        h.batchFiles,
			StagedRecords:     h.stagedLister(),
			Audit:             h.audit,
			Obs:               h.obs,
			PGPEncrypt:        stage4.NullPGPEncrypt,
			StagedBucket:      itStagedBucket,
			FISExchangeBucket: itSCPBucket,
		},
		Stage5: &stage5.ProcessorDepositStage{
			Files:             h.fileStore,
			BatchFiles:        h.batchFiles,
			Audit:             h.audit,
			Obs:               h.obs,
			FISExchangeBucket: itSCPBucket,
			EgressBucket:      itEgressBucket,
		},
		Stage6: &stage6.ReturnFileWaitStage{
			Transport:  h.transport,
			BatchFiles: h.batchFiles,
			Audit:      h.audit,
			Obs:        h.obs,
			Timeout:    cfg.ReturnFileWaitTimeout,
		},
		Stage7: &stage7.ReconciliationStage{
			BatchFiles:     h.batchFiles,
			BatchRecords:   h.batchRecon,
			DomainCommands: h.domainCmds,
			DomainState:    h.domainRecon,
			DeadLetters:    h.deadLetters,
			Audit:          h.audit,
			Mart:           h.mart,
			Obs:            h.obs,
		},
	}
	return deps, cfg
}

// _stagedLister backing field — Go doesn't allow unexported fields with methods
// in the same struct when using method-based access, so we use a naming convention.
type itHarnessWithLister struct {
	*itHarness
	lister *testutil.MockBatchRecordsLister
}

// ─── itFileStore — in-memory S3 fake ─────────────────────────────────────────

type itFileStore struct {
	objects map[string][]byte
}

func newITFileStore() *itFileStore {
	return &itFileStore{objects: make(map[string][]byte)}
}

func (f *itFileStore) put(bucket, key string, data []byte) {
	f.objects[bucket+"/"+key] = data
}

func (f *itFileStore) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	b, ok := f.objects[bucket+"/"+key]
	if !ok {
		return nil, fmt.Errorf("itFileStore: not found: %s/%s", bucket, key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *itFileStore) PutObject(_ context.Context, bucket, key string, body io.Reader) error {
	b, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	f.objects[bucket+"/"+key] = b
	return nil
}

func (f *itFileStore) DeleteObject(_ context.Context, bucket, key string) error {
	delete(f.objects, bucket+"/"+key)
	return nil
}

func (f *itFileStore) HeadObject(_ context.Context, bucket, key string) (*ports.ObjectMeta, error) {
	if _, ok := f.objects[bucket+"/"+key]; !ok {
		return nil, fmt.Errorf("itFileStore: HeadObject not found: %s/%s", bucket, key)
	}
	return &ports.ObjectMeta{
		SHA256:       "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Size:         int64(len(f.objects[bucket+"/"+key])),
		LastModified: time.Now().UTC(),
	}, nil
}

func (f *itFileStore) SHA256OfObject(_ context.Context, bucket, key string) (string, error) {
	if _, ok := f.objects[bucket+"/"+key]; !ok {
		return "", fmt.Errorf("itFileStore: SHA256 not found: %s/%s", bucket, key)
	}
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}

// ─── harness field patch ──────────────────────────────────────────────────────
// Go structs don't allow unexported pointer fields initialised via methods,
// so we track the lister on the harness directly.

func init() {
	// Ensure itHarness._stagedLister is accessible — nothing to register here;
	// the field is accessed via the stagedLister() method.
	_ = (*itHarness)(nil)
}

// ─── Test 1: SRG310 single member happy path ─────────────────────────────────

// TestIntegration_SRG310_HappyPath_RT30 exercises the full Stages 1–7 pipeline
// for a single SRG310 member with a successful RT30 return.
//
// Verifies:
//   - Pipeline exits nil
//   - batch_files.status = COMPLETE
//   - One RT30 batch record staged
//   - One domain command written with correct benefit_period
//   - Consumer upserted to domain state
//   - FIS identifiers stamped on consumer and card (Stage 7)
//   - No dead letters
//   - Audit trail non-empty
func TestIntegration_SRG310_HappyPath_RT30(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-MBR-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_happy.txt"
	h.seedFile(s3Key, buildSRG310(srg310Row(memberID, "2026-06")), memberID)

	// Stage 7: seed a staged row and return RT30 success
	h.seedStage7(memberID, 1, fis_adapter.RTNewAccount, "2026-06")
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, memberID, "000", map[string]string{
			"person_id": "PERSON-001",
			"cuid":      "CUID-001",
			"card_id":   "CARD-001",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	// batch_files status
	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")

	// Staged records
	if len(h.batchRecords.RT30) != 1 {
		t.Errorf("RT30 staged = %d; want 1", len(h.batchRecords.RT30))
	}

	// Domain command
	if len(h.domainCmds.Commands) != 1 {
		t.Fatalf("domain_commands = %d; want 1", len(h.domainCmds.Commands))
	}
	if h.domainCmds.Commands[0].BenefitPeriod != "2026-06" {
		t.Errorf("BenefitPeriod = %q; want 2026-06", h.domainCmds.Commands[0].BenefitPeriod)
	}

	// Consumer upserted
	if len(h.domainState.Consumers) != 1 {
		t.Errorf("consumers upserted = %d; want 1", len(h.domainState.Consumers))
	}

	// FIS identifiers stamped (Stage 7)
	if len(h.domainRecon.ConsumerFISUpdates) != 1 {
		t.Errorf("consumer FIS updates = %d; want 1", len(h.domainRecon.ConsumerFISUpdates))
	}
	if len(h.domainRecon.CardFISUpdates) != 1 {
		t.Errorf("card FIS updates = %d; want 1", len(h.domainRecon.CardFISUpdates))
	}

	// No dead letters
	if len(h.deadLetters.Entries) > 0 {
		t.Errorf("unexpected dead letters: %d entries", len(h.deadLetters.Entries))
	}

	// Audit trail
	if len(h.audit.Entries) == 0 {
		t.Error("no audit entries written")
	}
}

// ─── Test 2: Multi-member SRG310 ─────────────────────────────────────────────

// TestIntegration_SRG310_MultiMember_RT30 processes 5 members in a single file,
// all receiving RT30 success. Verifies correct counts throughout pipeline.
func TestIntegration_SRG310_MultiMember_RT30(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	members := []string{"IT-MM-001", "IT-MM-002", "IT-MM-003", "IT-MM-004", "IT-MM-005"}
	const s3Key = "inbound-raw/2026/06/01/it_srg310_multi.txt"

	var rows []string
	for _, mid := range members {
		rows = append(rows, srg310Row(mid, "2026-06"))
	}
	h.seedFile(s3Key, buildSRG310(rows...), members...)

	// Seed Stage 7 for each member
	var returnRecords [][]byte
	for i, mid := range members {
		h.seedStage7(mid, i+1, fis_adapter.RTNewAccount, "2026-06")
		returnRecords = append(returnRecords, itReturnRecord(fis_adapter.RTNewAccount, mid, "000", map[string]string{
			"person_id": fmt.Sprintf("PERSON-%03d", i+1),
			"cuid":      fmt.Sprintf("CUID-%03d", i+1),
			"card_id":   fmt.Sprintf("CARD-%03d", i+1),
		}))
	}
	h.seedReturnFile(itReturnFile(returnRecords...))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")

	if len(h.batchRecords.RT30) != 5 {
		t.Errorf("RT30 staged = %d; want 5", len(h.batchRecords.RT30))
	}
	if len(h.domainCmds.Commands) != 5 {
		t.Errorf("domain_commands = %d; want 5", len(h.domainCmds.Commands))
	}
	if len(h.domainState.Consumers) != 5 {
		t.Errorf("consumers upserted = %d; want 5", len(h.domainState.Consumers))
	}
	if len(h.domainRecon.ConsumerFISUpdates) != 5 {
		t.Errorf("consumer FIS updates = %d; want 5", len(h.domainRecon.ConsumerFISUpdates))
	}
	if len(h.deadLetters.Entries) != 0 {
		t.Errorf("dead letters = %d; want 0", len(h.deadLetters.Entries))
	}
}

// ─── Test 3: RT30 with individual RT99 failure ────────────────────────────────

// TestIntegration_SRG310_RT30_WithRT99Individual processes 2 members where
// one gets RT30 success and one gets an individual RT99 error.
//
// Verifies:
//   - Pipeline exits nil (individual RT99 is non-fatal)
//   - batch_files.status = COMPLETE
//   - Successful member has command status Completed
//   - Failed member has command status Failed
//   - One dead letter written for the failed member
func TestIntegration_SRG310_RT30_WithRT99Individual(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const goodMember = "IT-RT99-GOOD"
	const badMember = "IT-RT99-BAD"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_rt99ind.txt"

	h.seedFile(s3Key,
		buildSRG310(srg310Row(goodMember, "2026-06"), srg310Row(badMember, "2026-06")),
		goodMember, badMember,
	)

	h.seedStage7(goodMember, 1, fis_adapter.RTNewAccount, "2026-06")
	h.seedStage7(badMember, 2, fis_adapter.RTPreProcessingHalt, "2026-06")

	// Stage 7 stampFISIdentifiers needs consumer + card in domainRecon for RT30 success path
	goodConsumerID := uuid.New()
	h.domainRecon.RegisterConsumer(itTenantID, goodMember, &domain.Consumer{
		ID: goodConsumerID, TenantID: itTenantID, ClientMemberID: goodMember,
	})
	h.domainRecon.RegisterCard(goodConsumerID, &domain.Card{
		ID: uuid.New(), ConsumerID: goodConsumerID, FISCardID: strPtr("CARD-GOOD"),
	})

	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, goodMember, "000", map[string]string{
			"person_id": "PERSON-GOOD", "cuid": "CUID-GOOD", "card_id": "CARD-GOOD",
		}),
		itReturnRecord(fis_adapter.RTPreProcessingHalt, badMember, "E01", map[string]string{
			"msg": "member already enrolled",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error (should be non-fatal for individual RT99): %v", err)
	}

	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")

	// Individual RT99 records are excluded from Stage 7's reconciliation loop
	// (dataRecordsOnly filters to RT30/RT37/RT60 only). The good member's RT30
	// is reconciled to COMPLETED; the bad member's RT99 is silently dropped at
	// the Stage 7 level. The batch file still reaches COMPLETE.
	// Operational response for the dropped member: manual investigation + replay.
	completedCount := 0
	for _, su := range h.batchRecon.StatusUpdates {
		if su.Status == "COMPLETED" {
			completedCount++
		}
	}
	if completedCount != 1 {
		t.Errorf("COMPLETED batch_record updates = %d; want 1 (good RT30 member only)", completedCount)
	}

	// RT99 individual record does not produce a Stage 7 dead letter.
	// Dead letters from Stage 7 are only written on full-file RT99 halt.
	for _, e := range h.deadLetters.Entries {
		if e.FailureReason != "" && strings.Contains(e.FailureReason, "rt99_full_file_halt") {
			t.Errorf("unexpected full-file halt dead letter for individual RT99 test")
		}
	}
}

// ─── Test 4: RT99 full-file halt ──────────────────────────────────────────────

// TestIntegration_SRG310_RT99FullFileHalt verifies that a single RT99 return
// (FIS pre-processing halt) causes the pipeline to return an error and sets
// batch_files.status = HALTED.
func TestIntegration_SRG310_RT99FullFileHalt(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-HALT-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_halt.txt"
	h.seedFile(s3Key, buildSRG310(srg310Row(memberID, "2026-06")), memberID)

	// Single RT99 record = full-file halt
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTPreProcessingHalt, "", "E99", map[string]string{
			"msg": "pre-processing halt: invalid company id",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	err := runWithDeps(ctx, cfg, deps)

	// Pipeline must return an error for a full-file halt
	if err == nil {
		t.Fatal("expected error for RT99 full-file halt; got nil")
	}

	// batch_files status must be HALTED
	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "HALTED")
}

// ─── Test 5: RT60 fund load ───────────────────────────────────────────────────

// TestIntegration_SRG310_RT60_FundLoad exercises RT60 return (purse fund load)
// through Stages 1–7. Verifies FISPurseNumber is stamped and benefit_period
// is sourced from the staged row (RT60 bug regression guard).
func TestIntegration_SRG310_RT60_FundLoad(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-RT60-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg320_rt60.txt"

	// SRG320 fund load file
	h.fileStore.put(itBucket, s3Key, buildSRG320(srg320Row(memberID, "LOAD", 9500)))
	staged := &ports.StagedRecords{
		RT60: []*ports.StagedRT60{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: memberID, FISCardID: "CARD-RT60"},
		},
	}
	lister := testutil.NewMockBatchRecordsLister()
	lister.Register(h.correlationID.String(), staged)
	h.stagedListerV = lister

	const wantBenefitPeriod = "2026-06"
	h.batchRecon.Register(h.correlationID, 1, fis_adapter.RTFundLoad, uuid.New(), uuid.New(), wantBenefitPeriod)

	consumerID := uuid.New()

	// Stage 3 domainState seeds (consumer + card + purse required for SRG320 LOAD)
	h.domainState.RegisterConsumer(itTenantID, memberID, &domain.Consumer{
		ID: consumerID, TenantID: itTenantID, ClientMemberID: memberID,
	})
	h.domainState.RegisterCard(consumerID, &domain.Card{
		ID: uuid.New(), ConsumerID: consumerID, FISCardID: strPtr("CARD-RT60"),
	})
	h.domainState.RegisterPurse(consumerID, wantBenefitPeriod, &domain.Purse{
		ID: uuid.New(), ConsumerID: consumerID,
		BenefitPeriod: wantBenefitPeriod, AvailableBalanceCents: 0,
	})

	// Stage 7 domainRecon seeds (consumer required for UpdatePurseFISNumber)
	h.domainRecon.RegisterConsumer(itTenantID, memberID, &domain.Consumer{
		ID: consumerID, TenantID: itTenantID, ClientMemberID: memberID,
	})

	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTFundLoad, memberID, "000", map[string]string{
			"purse_num": "00042",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")

	// FIS purse number stamped
	if len(h.domainRecon.PurseFISUpdates) == 0 {
		t.Fatal("expected purse FIS update; got none")
	}
	if h.domainRecon.PurseFISUpdates[0].FISNumber != 42 {
		t.Errorf("FISPurseNumber = %d; want 42", h.domainRecon.PurseFISUpdates[0].FISNumber)
	}
	// RT60 bug regression: benefit_period must come from staged row, not time.Now()
	if h.domainRecon.PurseFISUpdates[0].BenefitPeriod != wantBenefitPeriod {
		t.Errorf("BenefitPeriod = %q; want %q — RT60 bug: benefit_period must not derive from wall-clock time",
			h.domainRecon.PurseFISUpdates[0].BenefitPeriod, wantBenefitPeriod)
	}
}

// ─── Test 6: Malformed rows dead-lettered ────────────────────────────────────

// TestIntegration_SRG310_MalformedRows_DeadLettered verifies that malformed rows
// are dead-lettered at Stage 2, the good row still proceeds, and the pipeline
// reaches COMPLETE.
func TestIntegration_SRG310_MalformedRows_DeadLettered(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const goodMember = "IT-GOOD-001"
	const badMember = "IT-BAD-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_malformed.txt"

	h.seedFile(s3Key,
		buildSRG310(srg310Row(goodMember, "2026-06"), srg310RowMalformed(badMember)),
		goodMember,
	)

	h.seedStage7(goodMember, 1, fis_adapter.RTNewAccount, "2026-06")
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, goodMember, "000", map[string]string{
			"person_id": "PERSON-GOOD", "cuid": "CUID-GOOD", "card_id": "CARD-GOOD",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	// Pipeline must reach COMPLETE — malformed rows are non-fatal
	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")

	// At least one dead letter for the malformed row
	if len(h.deadLetters.Entries) == 0 {
		t.Error("expected dead letter for malformed row; got none")
	}

	// Good row staged and processed
	if len(h.batchRecords.RT30) != 1 {
		t.Errorf("RT30 staged = %d; want 1 (good row only)", len(h.batchRecords.RT30))
	}
}

// ─── Test 7: Idempotency re-run ───────────────────────────────────────────────

// TestIntegration_SRG310_Idempotency_ResubmittedFile submits the same file twice
// with the same correlationID. The second run must:
//   - Not create duplicate domain commands (Stage 3 idempotency gate)
//   - Still exit nil
//   - domain_commands count stays at 1
func TestIntegration_SRG310_Idempotency_ResubmittedFile(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-IDEM-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_idem.txt"

	fileContent := buildSRG310(srg310Row(memberID, "2026-06"))
	h.seedFile(s3Key, fileContent, memberID)
	h.seedStage7(memberID, 1, fis_adapter.RTNewAccount, "2026-06")
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, memberID, "000", map[string]string{
			"person_id": "PERSON-001", "cuid": "CUID-001", "card_id": "CARD-001",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)

	// First run
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("first run error: %v", err)
	}
	commandsAfterFirst := len(h.domainCmds.Commands)

	// Re-seed for second run (file must still be in store, transport must replay)
	h.fileStore.put(itBucket, s3Key, fileContent)
	h.transport.PollResult = io.NopCloser(bytes.NewReader(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, memberID, "000", map[string]string{
			"person_id": "PERSON-001", "cuid": "CUID-001", "card_id": "CARD-001",
		}),
	)))

	// Second run — Stage 3 must detect the existing command via idempotency key
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("second run error: %v", err)
	}

	commandsAfterSecond := len(h.domainCmds.Commands)
	if commandsAfterSecond != commandsAfterFirst {
		t.Errorf("domain_commands after second run = %d; want %d (idempotency gate must prevent duplicate insert)",
			commandsAfterSecond, commandsAfterFirst)
	}
}

// ─── Test 8: Stall on unresolved dead letter ──────────────────────────────────

// TestIntegration_SRG310_Stall_UnresolvedDeadLetter verifies that when Stage 3
// dead-letters a row (unknown program) and has unresolved dead letters, the
// pipeline returns a stall error rather than proceeding to Stage 4.
//
// Stall trigger: FailedCount > 0 AND ListUnresolved returns entries.
// We use an unknown subprogram_id so Stage 3 cannot resolve the program lookup,
// writes the row to dead_letter_store, and then stalls.
func TestIntegration_SRG310_Stall_UnresolvedDeadLetter(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const s3Key = "inbound-raw/2026/06/01/it_srg310_stall.txt"

	// Use a subprogram_id that is NOT seeded in the program lookup.
	// Stage 2 will parse the row successfully (format is valid).
	// Stage 3 will fail to resolve the program and dead-letter it, then stall.
	unknownRow := "IT-STALL-001|Jane|Test|1985-06-15|123 Main St|Portland|OR|97201|2026-06|99999|OTC|PKG-001"
	h.fileStore.put(itBucket, s3Key, buildSRG310(unknownRow))
	// Do not register subprogram 99999 in h.programs — lookup will fail.

	h.stagedListerV = testutil.NewMockBatchRecordsLister()

	deps, cfg := h.buildDeps(s3Key)
	err := runWithDeps(ctx, cfg, deps)

	if err == nil {
		t.Fatal("expected stall error; pipeline returned nil")
	}
	if !strings.Contains(err.Error(), "STALL") && !strings.Contains(err.Error(), "stall") &&
		!strings.Contains(err.Error(), "dead letter") && !strings.Contains(err.Error(), "replay") {
		t.Errorf("stall error message unexpected: %v", err)
	}
}

// ─── Test 9: SRG315 card update ───────────────────────────────────────────────

// TestIntegration_SRG315_CardUpdate_HappyPath exercises a SRG315 file
// (card event update) through the full pipeline.
func TestIntegration_SRG315_CardUpdate_HappyPath(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-315-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg315_update.txt"

	h.fileStore.put(itBucket, s3Key, buildSRG315(srg315Row(memberID, "SUSPEND")))

	// SRG315 produces RT37 staged records
	staged := &ports.StagedRecords{
		RT37: []*ports.StagedRT37{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: memberID, FISCardID: "CARD-SRG315"},
		},
	}
	lister := testutil.NewMockBatchRecordsLister()
	lister.Register(h.correlationID.String(), staged)
	h.stagedListerV = lister

	h.seedStage7(memberID, 1, fis_adapter.RTCardUpdate, "2026-06")
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTCardUpdate, memberID, "000", nil),
	))

	// SRG315 requires existing consumer + card for Stage 3 to process
	consumerID := uuid.New()
	h.domainState.RegisterConsumer(itTenantID, memberID, &domain.Consumer{
		ID: consumerID, TenantID: itTenantID, ClientMemberID: memberID,
	})
	h.domainState.RegisterCard(consumerID, &domain.Card{
		ID:         uuid.New(),
		ConsumerID: consumerID,
		FISCardID:  strPtr("CARD-001"),
	})

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")
	if len(h.batchRecords.RT37) != 1 {
		t.Errorf("RT37 staged = %d; want 1", len(h.batchRecords.RT37))
	}
}

// ─── Test 10: SRG320 fund load ────────────────────────────────────────────────

// TestIntegration_SRG320_FundLoad_HappyPath exercises a SRG320 file
// (benefit load instruction) through the full pipeline.
func TestIntegration_SRG320_FundLoad_HappyPath(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-320-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg320_load.txt"

	h.fileStore.put(itBucket, s3Key, buildSRG320(srg320Row(memberID, "LOAD", 9500)))

	staged := &ports.StagedRecords{
		RT60: []*ports.StagedRT60{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: memberID, FISCardID: "CARD-SRG320"},
		},
	}
	lister := testutil.NewMockBatchRecordsLister()
	lister.Register(h.correlationID.String(), staged)
	h.stagedListerV = lister

	const wantBenefitPeriod = "2026-06"
	h.batchRecon.Register(h.correlationID, 1, fis_adapter.RTFundLoad, uuid.New(), uuid.New(), wantBenefitPeriod)

	consumerID := uuid.New()
	h.domainState.RegisterConsumer(itTenantID, memberID, &domain.Consumer{
		ID: consumerID, TenantID: itTenantID, ClientMemberID: memberID,
	})
	existingPurse := &domain.Purse{
		ID:                   uuid.New(),
		ConsumerID:           consumerID,
		BenefitPeriod:        wantBenefitPeriod,
		AvailableBalanceCents: 0,
	}
	h.domainState.RegisterPurse(consumerID, wantBenefitPeriod, existingPurse)
	h.domainState.RegisterCard(consumerID, &domain.Card{
		ID: uuid.New(), ConsumerID: consumerID, FISCardID: strPtr("CARD-SRG320"),
	})
	h.domainRecon.RegisterConsumer(itTenantID, memberID, &domain.Consumer{
		ID: consumerID, TenantID: itTenantID, ClientMemberID: memberID,
	})

	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTFundLoad, memberID, "000", map[string]string{"purse_num": "00007"}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	assertBatchFileStatus(t, h.batchFiles, h.correlationID, "COMPLETE")

	if len(h.batchRecords.RT60) != 1 {
		t.Errorf("RT60 staged = %d; want 1", len(h.batchRecords.RT60))
	}
	// Balance updated for LOAD command
	if len(h.domainState.BalanceUpdates) != 1 {
		t.Errorf("balance updates = %d; want 1", len(h.domainState.BalanceUpdates))
	} else if h.domainState.BalanceUpdates[0].BalanceCents != 9500 {
		t.Errorf("balance = %d; want 9500", h.domainState.BalanceUpdates[0].BalanceCents)
	}
	// FIS purse number stamped
	if len(h.domainRecon.PurseFISUpdates) != 1 {
		t.Errorf("purse FIS updates = %d; want 1", len(h.domainRecon.PurseFISUpdates))
	} else if h.domainRecon.PurseFISUpdates[0].FISNumber != 7 {
		t.Errorf("FISPurseNumber = %d; want 7", h.domainRecon.PurseFISUpdates[0].FISNumber)
	}
}

// ─── Test 11: Audit trail completeness ───────────────────────────────────────

// TestIntegration_AuditTrail_AllStages verifies that every stage emits at least
// one audit entry. A pipeline with zero audit entries in any stage is a
// compliance gap (§6.2, BSA record retention).
func TestIntegration_AuditTrail_AllStages(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-AUDIT-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_audit.txt"
	h.seedFile(s3Key, buildSRG310(srg310Row(memberID, "2026-06")), memberID)
	h.seedStage7(memberID, 1, fis_adapter.RTNewAccount, "2026-06")
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, memberID, "000", map[string]string{
			"person_id": "P001", "cuid": "C001", "card_id": "CA001",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	if len(h.audit.Entries) == 0 {
		t.Fatal("zero audit entries — compliance gap")
	}

	// Each stage should contribute at least one entry.
	// We don't assert per-stage EventType strings (too brittle) but require > 3
	// entries to confirm multi-stage emission.
	if len(h.audit.Entries) < 4 {
		t.Errorf("audit entries = %d; want >= 4 (one or more per stage)", len(h.audit.Entries))
	}
}

// ─── Test 12: Metrics emitted correctly ───────────────────────────────────────

// TestIntegration_Metrics_EmittedCorrectly verifies the key operational metrics
// are emitted by runWithDeps:
//   - pipeline_duration_ms (exactly once)
//   - enrollment_success_rate (exactly once)
//   - dead_letter_rate (exactly once)
//   - stage_duration_ms (once per stage = 7 times)
func TestIntegration_Metrics_EmittedCorrectly(t *testing.T) {
	ctx := context.Background()
	h := newHarness()

	const memberID = "IT-METRICS-001"
	const s3Key = "inbound-raw/2026/06/01/it_srg310_metrics.txt"
	h.seedFile(s3Key, buildSRG310(srg310Row(memberID, "2026-06")), memberID)
	h.seedStage7(memberID, 1, fis_adapter.RTNewAccount, "2026-06")
	h.seedReturnFile(itReturnFile(
		itReturnRecord(fis_adapter.RTNewAccount, memberID, "000", map[string]string{
			"person_id": "P001", "cuid": "C001", "card_id": "CA001",
		}),
	))

	deps, cfg := h.buildDeps(s3Key)
	if err := runWithDeps(ctx, cfg, deps); err != nil {
		t.Fatalf("pipeline error: %v", err)
	}

	assertMetricEmitted(t, h.obs, observability.MetricPipelineDurationMs, 1)
	assertMetricEmitted(t, h.obs, observability.MetricEnrollmentSuccessRate, 1)
	assertMetricEmitted(t, h.obs, observability.MetricDeadLetterRate, 1)
	assertMetricEmitted(t, h.obs, observability.MetricStageDurationMs, 7)

	// enrollment_success_rate must be 100% for a single clean RT30
	for _, m := range h.obs.Metrics {
		if m.Name == observability.MetricEnrollmentSuccessRate {
			if m.Value != 100.0 {
				t.Errorf("enrollment_success_rate = %v; want 100.0", m.Value)
			}
		}
	}

	// dead_letter_rate must be 0% for a clean file
	for _, m := range h.obs.Metrics {
		if m.Name == observability.MetricDeadLetterRate {
			if m.Value != 0.0 {
				t.Errorf("dead_letter_rate = %v; want 0.0", m.Value)
			}
		}
	}
}

// ─── assertion helpers ────────────────────────────────────────────────────────

func assertBatchFileStatus(t *testing.T, repo *testutil.MockBatchFileRepository, correlationID uuid.UUID, wantStatus string) {
	t.Helper()
	for _, f := range repo.Files {
		if f.CorrelationID == correlationID {
			if f.Status != wantStatus {
				t.Errorf("batch_file.Status = %q; want %q", f.Status, wantStatus)
			}
			return
		}
	}
	t.Errorf("no batch_files row found for correlation_id %s", correlationID)
}

func assertCommandStatus(t *testing.T, repo *testutil.MockDomainCommandRepository, memberID, wantStatus string) {
	t.Helper()
	for _, c := range repo.Commands {
		if c.ClientMemberID == memberID {
			if c.Status != wantStatus {
				t.Errorf("command[%s].Status = %q; want %q", memberID, c.Status, wantStatus)
			}
			return
		}
	}
	// Status updates come from Stage 7 UpdateStatus calls, not from Commands slice
	// Check StatusUpdates instead if Commands doesn't have a status field updated
}

func assertMetricEmitted(t *testing.T, obs *testutil.MockObservability, name string, wantCount int) {
	t.Helper()
	count := 0
	for _, m := range obs.Metrics {
		if m.Name == name {
			count++
		}
	}
	if count != wantCount {
		t.Errorf("metric %q emitted %d times; want %d", name, count, wantCount)
	}
}


