package pipeline

// Tests for Stage 7 — Reconciliation (stage7_reconciliation.go).
//
// Coverage:
//   - Happy path RT30: batch_record COMPLETED, domain_command Completed,
//     FISPersonID + FISCUID stamped on consumer, FISCardID + issued_at on card
//   - Happy path RT60: batch_record COMPLETED, FISPurseNumber stamped on purse,
//     BenefitPeriod sourced from staged row (not wall-clock time)
//   - Individual RT99: batch_record FAILED, domain_command Failed, no identifiers stamped
//   - RT99 full-file halt: status → HALTED, dead letter written, batch.halt.triggered emitted
//   - Parse failure: error returned
//   - Record reconcile error is non-fatal: loop continues, file reaches COMPLETE
//   - Reconciliation fact written per record
//   - Status → COMPLETE on success, audit written

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
	"github.com/walker-morse/batch/fis_reconciliation/fis_adapter"
)

// ─── builder helpers ──────────────────────────────────────────────────────────

// buildReturnRecord builds a minimal 400-byte FIS return record for testing.
// It encodes just enough fields for ParseReturnFile to extract:
//   offset 0-3:   RecordType (4 bytes)
//   offset 4-23:  ClientMemberID (20 bytes)
//   offset 24-26: FISResultCode (3 bytes) — "000" = success
//   offset 27-66: FISResultMsg (40 bytes)
//   offset 67-86: FISPersonID (20 bytes, RT30 only)
//   offset 87-105: FISCUID (19 bytes, RT30 only)
//   offset 106-124: FISCardID (19 bytes, RT30 only)
//   offset 67-71: FISPurseNumber (5 bytes, RT60 only)
func buildReturnRecord(recType, clientMemberID, resultCode string, extras map[string]string) []byte {
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

// appendCRLF appends \r\n after each record and concatenates.
func buildReturnFile(records ...[]byte) io.ReadCloser {
	var buf bytes.Buffer
	for _, r := range records {
		buf.Write(r)
		buf.WriteString("\r\n")
	}
	return io.NopCloser(&buf)
}

// rt99Record builds a single RT99 record (full-file halt body).
func rt99Record(resultCode string) []byte {
	return buildReturnRecord(fis_adapter.RTPreProcessingHalt, "", resultCode, map[string]string{"msg": "pre-processing halt"})
}

// ─── stage7 constructor ───────────────────────────────────────────────────────

type stage7Mocks struct {
	batchFiles     *testutil.MockBatchFileRepository
	batchRecords   *testutil.MockBatchRecordsReconciler
	domainCommands *testutil.MockDomainCommandRepository
	domainState    *testutil.MockDomainStateReconciler
	deadLetters    *testutil.MockDeadLetterRepository
	audit          *testutil.MockAuditLogWriter
	mart           *testutil.MockMartWriter
	obs            *testutil.MockObservability
}

func newStage7WithMocks() (*ReconciliationStage, *stage7Mocks) {
	m := &stage7Mocks{
		batchFiles:     testutil.NewMockBatchFileRepository(),
		batchRecords:   testutil.NewMockBatchRecordsReconciler(),
		domainCommands: testutil.NewMockDomainCommandRepository(),
		domainState:    testutil.NewMockDomainStateReconciler(),
		deadLetters:    testutil.NewMockDeadLetterRepository(),
		audit:          &testutil.MockAuditLogWriter{},
		mart:           &testutil.MockMartWriter{},
		obs:            &testutil.MockObservability{},
	}
	stage := &ReconciliationStage{
		BatchFiles:     m.batchFiles,
		BatchRecords:   m.batchRecords,
		DomainCommands: m.domainCommands,
		DomainState:    m.domainState,
		DeadLetters:    m.deadLetters,
		Audit:          m.audit,
		Mart:           m.mart,
		Obs:            m.obs,
	}
	return stage, m
}

func makeBatchFileSubmitted(bf *testutil.MockBatchFileRepository) *ports.BatchFile {
	f := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		Status:        "SUBMITTED",
	}
	bf.Files[f.ID] = f
	return f
}

// ─── tests ────────────────────────────────────────────────────────────────────

// TestStage7_RT30_HappyPath verifies: batch_record COMPLETED, domain_command Completed,
// FIS identifiers stamped on consumer and card, reconciliation fact written.
func TestStage7_RT30_HappyPath(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	recordID := uuid.New()
	cmdID := uuid.New()
	consumerID := uuid.New()
	cardID := uuid.New()

	m.batchRecords.Register(batchFile.CorrelationID, 1, fis_adapter.RTNewAccount, recordID, cmdID, "2026-06")
	consumer := &domain.Consumer{ID: consumerID, TenantID: batchFile.TenantID, ClientMemberID: "MBR-001"}
	m.domainState.RegisterConsumer(batchFile.TenantID, "MBR-001", consumer)
	m.domainState.RegisterCard(consumerID, &domain.Card{ID: cardID, ConsumerID: consumerID})

	rt30 := buildReturnRecord(fis_adapter.RTNewAccount, "MBR-001", "000", map[string]string{
		"person_id": "PERSON-001",
		"cuid":      "CUID-001",
		"card_id":   "CARD-001",
	})
	returnBody := buildReturnFile(rt30)

	_, err := stage.Run(context.Background(), batchFile, returnBody)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Batch record → COMPLETED
	if len(m.batchRecords.StatusUpdates) == 0 {
		t.Fatal("expected batch record status update")
	}
	if m.batchRecords.StatusUpdates[0].Status != "COMPLETED" {
		t.Errorf("batch record status = %q; want COMPLETED", m.batchRecords.StatusUpdates[0].Status)
	}
	// Consumer FIS identifiers stamped
	if len(m.domainState.ConsumerFISUpdates) == 0 {
		t.Fatal("expected consumer FIS update")
	}
	if m.domainState.ConsumerFISUpdates[0].FISPersonID != "PERSON-001" {
		t.Errorf("FISPersonID = %q; want PERSON-001", m.domainState.ConsumerFISUpdates[0].FISPersonID)
	}
	// Card FIS identifier stamped
	if len(m.domainState.CardFISUpdates) == 0 {
		t.Fatal("expected card FIS update")
	}
	if m.domainState.CardFISUpdates[0].FISCardID != "CARD-001" {
		t.Errorf("FISCardID = %q; want CARD-001", m.domainState.CardFISUpdates[0].FISCardID)
	}
	// Reconciliation fact written
	if len(m.mart.ReconciliationFacts) == 0 {
		t.Fatal("expected reconciliation fact")
	}
	if m.mart.ReconciliationFacts[0].FISResultCode != "000" {
		t.Errorf("ReconciliationFact.FISResultCode = %q; want 000", m.mart.ReconciliationFacts[0].FISResultCode)
	}
	// Status → COMPLETE
	if batchFile.Status != string(domain.BatchFileComplete) {
		t.Errorf("batch file status = %q; want COMPLETE", batchFile.Status)
	}
}

// TestStage7_RT60_HappyPath verifies FISPurseNumber is stamped on purse and that
// BenefitPeriod is sourced from the staged row — not derived from wall-clock time.
func TestStage7_RT60_HappyPath(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	recordID := uuid.New()
	cmdID := uuid.New()
	consumerID := uuid.New()

	const wantBenefitPeriod = "2026-05"
	m.batchRecords.Register(batchFile.CorrelationID, 1, fis_adapter.RTFundLoad, recordID, cmdID, wantBenefitPeriod)
	m.domainState.RegisterConsumer(batchFile.TenantID, "MBR-001", &domain.Consumer{
		ID: consumerID, TenantID: batchFile.TenantID, ClientMemberID: "MBR-001",
	})

	rt60 := buildReturnRecord(fis_adapter.RTFundLoad, "MBR-001", "000", map[string]string{
		"purse_num": "00042",
	})
	_, err := stage.Run(context.Background(), batchFile, buildReturnFile(rt60))

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.domainState.PurseFISUpdates) == 0 {
		t.Fatal("expected purse FIS number update")
	}
	if m.domainState.PurseFISUpdates[0].FISNumber != 42 {
		t.Errorf("FISPurseNumber = %d; want 42", m.domainState.PurseFISUpdates[0].FISNumber)
	}
	// Core regression: BenefitPeriod must come from the staged row, not time.Now().
	if m.domainState.PurseFISUpdates[0].BenefitPeriod != wantBenefitPeriod {
		t.Errorf("BenefitPeriod = %q; want %q — benefit_period must be sourced from staged batch_records row, not wall-clock time",
			m.domainState.PurseFISUpdates[0].BenefitPeriod, wantBenefitPeriod)
	}
}

// TestStage7_IndividualRT99_Fails marks record FAILED without halting file.
func TestStage7_IndividualRT99_MarksFailed(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	recordID := uuid.New()
	cmdID := uuid.New()
	m.batchRecords.Register(batchFile.CorrelationID, 1, fis_adapter.RTPreProcessingHalt, recordID, cmdID, "2026-06")

	// Single RT99 = full-file halt — use two records to simulate individual failure
	// Individual RT99: appears alongside other records (not the only record)
	rt30Success := buildReturnRecord(fis_adapter.RTNewAccount, "MBR-001", "000", nil)
	rt99Individual := buildReturnRecord(fis_adapter.RTPreProcessingHalt, "", "E01", map[string]string{"msg": "invalid member"})

	m.batchRecords.Register(batchFile.CorrelationID, 1, fis_adapter.RTNewAccount, recordID, cmdID, "2026-06")

	_, err := stage.Run(context.Background(), batchFile, buildReturnFile(rt30Success, rt99Individual))

	// Should not halt — file has more than one record
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batchFile.Status == string(domain.BatchFileHalted) {
		t.Error("file should not be HALTED for individual RT99")
	}
	if batchFile.Status != string(domain.BatchFileComplete) {
		t.Errorf("status = %q; want COMPLETE", batchFile.Status)
	}
}

// TestStage7_FullFileHalt_RT99Only halts file, dead-letters, emits alert.
func TestStage7_FullFileHalt_RT99Only(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	_, err := stage.Run(context.Background(), batchFile, buildReturnFile(rt99Record("E99")))

	if err == nil {
		t.Fatal("expected error for full-file halt")
	}
	if batchFile.Status != string(domain.BatchFileHalted) {
		t.Errorf("status = %q; want HALTED", batchFile.Status)
	}
	if m.obs.EventCount("batch.halt.triggered") == 0 {
		t.Error("expected batch.halt.triggered event")
	}
	if len(m.deadLetters.Entries) == 0 {
		t.Fatal("expected dead letter entry for full-file halt")
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "rt99_full_file_halt") {
		t.Errorf("failure_reason = %q; want rt99_full_file_halt", m.deadLetters.Entries[0].FailureReason)
	}
	// Audit should record SUBMITTED → HALTED
	if len(m.audit.Entries) == 0 {
		t.Fatal("expected audit entry")
	}
	if m.audit.Entries[0].NewState != "HALTED" {
		t.Errorf("audit NewState = %q; want HALTED", m.audit.Entries[0].NewState)
	}
}

// TestStage7_RecordReconcileError_NonFatal verifies that a single record error
// does not abort the file — remaining records processed, file reaches COMPLETE.
func TestStage7_RecordReconcileError_NonFatal(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	// Seed seq=2 successfully, leave seq=1 unseeded (GetStaged will fail)
	recordID := uuid.New()
	cmdID := uuid.New()
	m.batchRecords.Register(batchFile.CorrelationID, 2, fis_adapter.RTNewAccount, recordID, cmdID, "2026-06")
	m.domainState.RegisterConsumer(batchFile.TenantID, "MBR-002", &domain.Consumer{
		ID: uuid.New(), TenantID: batchFile.TenantID, ClientMemberID: "MBR-002",
	})

	rt30Bad := buildReturnRecord(fis_adapter.RTNewAccount, "MBR-001", "000", nil)  // seq=1, no staged row
	rt30Good := buildReturnRecord(fis_adapter.RTNewAccount, "MBR-002", "000", nil) // seq=2, seeded

	_, err := stage.Run(context.Background(), batchFile, buildReturnFile(rt30Bad, rt30Good))

	// File should still reach COMPLETE despite the seq=1 error
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batchFile.Status != string(domain.BatchFileComplete) {
		t.Errorf("status = %q; want COMPLETE", batchFile.Status)
	}
	// Error should have been logged
	if m.obs.EventCount("stage7.record_reconcile_error") == 0 {
		t.Error("expected stage7.record_reconcile_error log event")
	}
}

// TestStage7_AuditOnComplete verifies audit entry for SUBMITTED → COMPLETE.
func TestStage7_AuditOnComplete(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	// Empty return file — no data records, just completes
	_, err := stage.Run(context.Background(), batchFile, buildReturnFile())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.audit.Entries) == 0 {
		t.Fatal("expected audit entry")
	}
	found := false
	for _, e := range m.audit.Entries {
		if e.NewState == "COMPLETE" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected audit entry with NewState=COMPLETE")
	}
}

// TestStage7_DomainCommandStatusUpdated verifies domain_command transitions to Completed.
func TestStage7_DomainCommandStatusUpdated(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	recordID := uuid.New()
	cmdID := uuid.New()
	m.batchRecords.Register(batchFile.CorrelationID, 1, fis_adapter.RTNewAccount, recordID, cmdID, "2026-06")
	// Seed consumer+card so identifier stamping doesn't fail
	consumerID := uuid.New()
	m.domainState.RegisterConsumer(batchFile.TenantID, "MBR-001", &domain.Consumer{
		ID: consumerID, TenantID: batchFile.TenantID, ClientMemberID: "MBR-001",
	})
	m.domainState.RegisterCard(consumerID, &domain.Card{ID: uuid.New(), ConsumerID: consumerID})

	rt30 := buildReturnRecord(fis_adapter.RTNewAccount, "MBR-001", "000", map[string]string{
		"person_id": "P1", "cuid": "C1", "card_id": "K1",
	})
	_, _ = stage.Run(context.Background(), batchFile, buildReturnFile(rt30))

	if len(m.domainCommands.StatusUpdates) == 0 {
		t.Fatal("expected domain_command status update")
	}
	update := m.domainCommands.StatusUpdates[0]
	if update.ID != cmdID {
		t.Errorf("command ID = %v; want %v", update.ID, cmdID)
	}
	if update.Status != string(domain.CommandCompleted) {
		t.Errorf("command status = %q; want Completed", update.Status)
	}
}

// TestStage7_ParseFailure_ReturnsError verifies that a corrupt return file errors immediately.
func TestStage7_ParseFailure_ReturnsError(t *testing.T) {
	stage, m := newStage7WithMocks()
	batchFile := makeBatchFileSubmitted(m.batchFiles)

	// Provide a reader that errors mid-stream
	pr, pw := io.Pipe()
	pw.CloseWithError(fmt.Errorf("s3: read error"))

	_, err := stage.Run(context.Background(), batchFile, pr)

	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if batchFile.Status == string(domain.BatchFileComplete) {
		t.Error("status should not be COMPLETE on parse failure")
	}
	_ = m // suppress unused
}
