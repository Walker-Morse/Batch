package pipeline

// Tests for Stage 3 — Row Processing (stage3_row_processing.go).
//
// Strategy: all dependencies behind the narrow interfaces (BatchRecordWriter,
// DomainStateWriter, ProgramLookup, DomainCommandRepository, etc.) are
// replaced with in-memory mocks from testutil. No DB connection required.
//
// Coverage targets:
//
//	Run() happy path — SRG310 rows staged, status→PROCESSING
//	Run() idempotency — duplicate rows skipped, StagedCount unaffected
//	Run() stall detection — failed rows trigger STALLED transition
//	Run() multi-row-type — 310/315/320 counters all reported correctly
//	processSRG310Row — happy path: command inserted, RT30 written, consumer+card created
//	processSRG310Row — domain command insert failure → dead letter
//	processSRG310Row — RT30 insert failure → dead letter
//	processSRG310Row — consumer upsert failure → dead letter
//	processSRG315Row — unknown event type → dead letter
//	processSRG315Row — consumer not found → dead letter (RT37 requires prior RT30)
//	processSRG315Row — fis_card_id empty → dead letter (Stage 7 not yet run)
//	processSRG320Row — unknown command type → dead letter
//	processSRG320Row — consumer not found → dead letter
//	processSRG320Row — fis_card_id empty → dead letter
//	srgEventToCommandType — all known/unknown event types
//	srg320CommandTypeToCommand — all known/unknown command types
//	commandTypeToCardStatus — all known/unknown command types
//	fisCardIDOrEmpty — always returns empty (pre-Stage-7)
//	strPtrIfNotEmpty / timePtrIfNotZero — edge cases

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
	"github.com/walker-morse/batch/member_enrollment/srg"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newStage3 builds a RowProcessingStage fully wired with mocks.
// All mocks are returned for inspection after the call.
type stage3Mocks struct {
	batchFiles  *testutil.MockBatchFileRepository
	cmds        *testutil.MockDomainCommandRepository
	deadLetters *testutil.MockDeadLetterRepository
	records     *testutil.MockBatchRecordWriter
	state       *testutil.MockDomainStateWriter
	programs    *testutil.MockProgramLookup
	audit       *testutil.MockAuditLogWriter
	obs         *testutil.MockObservability
}

func newStage3WithMocks() (*RowProcessingStage, *stage3Mocks) {
	m := &stage3Mocks{
		batchFiles:  testutil.NewMockBatchFileRepository(),
		cmds:        testutil.NewMockDomainCommandRepository(),
		deadLetters: testutil.NewMockDeadLetterRepository(),
		records:     testutil.NewMockBatchRecordWriter(),
		state:       testutil.NewMockDomainStateWriter(),
		programs:    testutil.NewMockProgramLookup(),
		audit:       &testutil.MockAuditLogWriter{},
		obs:         &testutil.MockObservability{},
	}
	stage := &RowProcessingStage{
		DomainCommands: m.cmds,
		DeadLetters:    m.deadLetters,
		BatchFiles:     m.batchFiles,
		BatchRecords:   m.records,
		DomainState:    m.state,
		Programs:       m.programs,
		Audit:          m.audit,
		Obs:            m.obs,
	}
	return stage, m
}

func seedBatchFile(m *stage3Mocks, tenantID string) *ports.BatchFile {
	bf := &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      tenantID,
		ClientID:      "rfu",
		FileType:      "SRG310",
		Status:        "RECEIVED",
		ArrivedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	m.batchFiles.Files[bf.ID] = bf
	return bf
}

func minimalSRG310(clientMemberID, subprogramID string, seq int) *srg.SRG310Row {
	return &srg.SRG310Row{
		SequenceInFile: seq,
		ClientMemberID: clientMemberID,
		SubprogramID:   subprogramID,
		FirstName:      "Rosa",
		LastName:       "Garcia",
		DOB:            time.Date(1982, 4, 10, 0, 0, 0, 0, time.UTC),
		Address1:       "500 N Michigan Ave",
		City:           "Chicago",
		State:          "IL",
		ZIP:            "60611",
		BenefitPeriod:  "2026-06",
		BenefitType:    "OTC",
		Raw:            map[string]string{"client_member_id": clientMemberID},
	}
}

func minimalSRG315(clientMemberID, eventType string, seq int) *srg.SRG315Row {
	return &srg.SRG315Row{
		SequenceInFile: seq,
		ClientMemberID: clientMemberID,
		EventType:      eventType,
		BenefitPeriod:  "2026-06",
		Raw:            map[string]string{"client_member_id": clientMemberID},
	}
}

func minimalSRG320(clientMemberID, commandType string, seq int) *srg.SRG320Row {
	return &srg.SRG320Row{
		SequenceInFile: seq,
		ClientMemberID: clientMemberID,
		CommandType:    commandType,
		BenefitPeriod:  "2026-06",
		AmountCents:    5000,
		EffectiveDate:  time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		ExpiryDate:     time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		Raw:            map[string]string{"client_member_id": clientMemberID},
	}
}

// ─── Run() — happy paths ──────────────────────────────────────────────────────

// TestRun_SRG310_HappyPath verifies that a single valid SRG310 row produces
// StagedCount=1, status transitions to PROCESSING, and RT30 + consumer + card
// are all written to their respective stores.
func TestRun_SRG310_HappyPath(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	bf := seedBatchFile(m, "rfu-oregon")

	result, err := stage.Run(context.Background(), &RowProcessingInput{
		BatchFile:  bf,
		SRG310Rows: []*srg.SRG310Row{minimalSRG310("MBR-001", "26071", 1)},
	})

	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.StagedCount != 1 {
		t.Errorf("StagedCount = %d; want 1", result.StagedCount)
	}
	if result.FailedCount != 0 {
		t.Errorf("FailedCount = %d; want 0", result.FailedCount)
	}
	if result.Stalled {
		t.Error("Stalled should be false on clean run")
	}
	if len(m.records.RT30) != 1 {
		t.Errorf("expected 1 RT30 record; got %d", len(m.records.RT30))
	}
	if len(m.cmds.Commands) != 1 {
		t.Errorf("expected 1 domain command; got %d", len(m.cmds.Commands))
	}
	// Consumer and card should both have been written
	if len(m.state.Consumers) == 0 {
		t.Error("expected consumer to be upserted")
	}
	if len(m.state.Cards) != 1 {
		t.Errorf("expected 1 card; got %d", len(m.state.Cards))
	}
	// status must have transitioned to PROCESSING
	if bf.Status != "PROCESSING" {
		t.Errorf("batch_file status = %q; want PROCESSING", bf.Status)
	}
}

// TestRun_MultipleRows_AllStaged verifies counts across three rows in the same file.
func TestRun_MultipleRows_AllStaged(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	bf := seedBatchFile(m, "rfu-oregon")

	rows := []*srg.SRG310Row{
		minimalSRG310("MBR-001", "26071", 1),
		minimalSRG310("MBR-002", "26071", 2),
		minimalSRG310("MBR-003", "26071", 3),
	}

	result, err := stage.Run(context.Background(), &RowProcessingInput{BatchFile: bf, SRG310Rows: rows})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.StagedCount != 3 {
		t.Errorf("StagedCount = %d; want 3", result.StagedCount)
	}
	if len(m.records.RT30) != 3 {
		t.Errorf("expected 3 RT30 records; got %d", len(m.records.RT30))
	}
}

// ─── Run() — idempotency ──────────────────────────────────────────────────────

// TestRun_DuplicateRow_Skipped verifies that when a domain command already exists
// for the composite key, the row is counted as a duplicate and NOT re-staged.
// Re-staging would produce duplicate FIS records and a malformed batch file.
func TestRun_DuplicateRow_Skipped(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	bf := seedBatchFile(m, "rfu-oregon")

	row := minimalSRG310("MBR-001", "26071", 1)

	// Pre-seed a duplicate command in the mock
	m.cmds.Commands = append(m.cmds.Commands, &ports.DomainCommand{
		TenantID:       bf.TenantID,
		ClientMemberID: "MBR-001",
		CommandType:    string(domain.CommandEnroll),
		BenefitPeriod:  "2026-06",
		CorrelationID:  bf.CorrelationID,
	})

	result, err := stage.Run(context.Background(), &RowProcessingInput{BatchFile: bf, SRG310Rows: []*srg.SRG310Row{row}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.DuplicateCount != 1 {
		t.Errorf("DuplicateCount = %d; want 1", result.DuplicateCount)
	}
	if result.StagedCount != 0 {
		t.Errorf("StagedCount = %d; want 0 (duplicate must not be re-staged)", result.StagedCount)
	}
	// No new RT30 row should have been written
	if len(m.records.RT30) != 0 {
		t.Errorf("RT30 records written for duplicate = %d; want 0", len(m.records.RT30))
	}
}

// ─── Run() — stall detection ──────────────────────────────────────────────────

// TestRun_FailedRow_TriggersStall verifies that when a row fails and an unresolved
// dead letter exists, Stage 3 transitions the batch to STALLED and sets Stalled=true.
// A STALLED batch must not proceed to Stage 4 (§5.1, ADR-010).
func TestRun_FailedRow_TriggersStall(t *testing.T) {
	stage, m := newStage3WithMocks()
	// No programs registered — all rows will fail with program_lookup_failed
	bf := seedBatchFile(m, "rfu-oregon")

	result, err := stage.Run(context.Background(), &RowProcessingInput{
		BatchFile:  bf,
		SRG310Rows: []*srg.SRG310Row{minimalSRG310("MBR-001", "UNKNOWN", 1)},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.FailedCount != 1 {
		t.Errorf("FailedCount = %d; want 1", result.FailedCount)
	}
	if !result.Stalled {
		t.Error("Stalled should be true when unresolved dead letters exist")
	}
	if bf.Status != "STALLED" {
		t.Errorf("batch_file status = %q; want STALLED", bf.Status)
	}
	if len(m.deadLetters.Entries) != 1 {
		t.Errorf("dead letter entries = %d; want 1", len(m.deadLetters.Entries))
	}
}

// TestRun_MixedRows_PartialSuccess verifies that one failed row does not abort
// processing of subsequent rows — the file continues; only the stall check runs at end.
func TestRun_MixedRows_PartialSuccess(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	bf := seedBatchFile(m, "rfu-oregon")

	rows := []*srg.SRG310Row{
		minimalSRG310("MBR-GOOD", "26071", 1),   // will succeed
		minimalSRG310("MBR-BAD", "UNKNOWN", 2),  // will fail — unknown program
		minimalSRG310("MBR-GOOD2", "26071", 3),  // will succeed
	}

	result, err := stage.Run(context.Background(), &RowProcessingInput{BatchFile: bf, SRG310Rows: rows})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.StagedCount != 2 {
		t.Errorf("StagedCount = %d; want 2", result.StagedCount)
	}
	if result.FailedCount != 1 {
		t.Errorf("FailedCount = %d; want 1", result.FailedCount)
	}
	// Batch should be STALLED (one unresolved dead letter)
	if !result.Stalled {
		t.Error("Stalled should be true")
	}
	if len(m.records.RT30) != 2 {
		t.Errorf("RT30 records = %d; want 2 (failed row must not produce a record)", len(m.records.RT30))
	}
}

// ─── processSRG310Row — failure paths ────────────────────────────────────────

// TestSRG310Row_DomainCommandInsertFails_DeadLettered verifies that a domain_commands
// insert failure dead-letters the row and returns outcomeDeadLettered.
// Without a domain_command row, the row has no idempotency record and must not be staged.
func TestSRG310Row_DomainCommandInsertFails_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	m.cmds.InsertErr = errors.New("db: connection reset")
	bf := seedBatchFile(m, "rfu-oregon")

	outcome := stage.processSRG310Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG310("MBR-001", "26071", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if len(m.deadLetters.Entries) != 1 {
		t.Fatalf("dead letter entries = %d; want 1", len(m.deadLetters.Entries))
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "domain_command_insert_failed") {
		t.Errorf("failure_reason = %q; want to contain domain_command_insert_failed", m.deadLetters.Entries[0].FailureReason)
	}
	if len(m.records.RT30) != 0 {
		t.Error("RT30 must not be written when domain_command insert fails")
	}
}

// TestSRG310Row_RT30InsertFails_DeadLettered verifies that an RT30 insert failure
// dead-letters the row. The domain_command was already written; the row still fails
// because without the RT30 record there is nothing for Stage 4 to assemble.
func TestSRG310Row_RT30InsertFails_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	m.records.InsertRT30Err = errors.New("db: disk full")
	bf := seedBatchFile(m, "rfu-oregon")

	outcome := stage.processSRG310Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG310("MBR-001", "26071", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "rt30_insert_failed") {
		t.Errorf("failure_reason = %q; want rt30_insert_failed", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG310Row_ConsumerUpsertFails_DeadLettered verifies that a consumer upsert
// failure dead-letters the row. Domain state must be consistent — a staged RT30
// without a consumers row leaves the system in a partial state.
func TestSRG310Row_ConsumerUpsertFails_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	programID := uuid.New()
	m.programs.Register("rfu-oregon", "26071", programID)
	m.state.UpsertConsumerErr = errors.New("db: constraint violation")
	bf := seedBatchFile(m, "rfu-oregon")

	outcome := stage.processSRG310Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG310("MBR-001", "26071", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "consumer_upsert_failed") {
		t.Errorf("failure_reason = %q; want consumer_upsert_failed", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG310Row_DeadLetter_PHINotInReason verifies the PHI-in-logs invariant:
// failure_reason must contain only a structured code, never PHI.
// (§6.5.2, §7.2, HIPAA §164.312(b))
func TestSRG310Row_DeadLetter_PHINotInReason(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	row := minimalSRG310("MBR-001", "UNKNOWN", 1)
	// Embed a PHI-like value in the raw record to ensure it doesn't leak
	row.Raw["first_name"] = "Rosa"
	row.Raw["dob"] = "1982-04-10"

	stage.processSRG310Row(context.Background(), &RowProcessingInput{BatchFile: bf}, row)

	if len(m.deadLetters.Entries) == 0 {
		t.Fatal("expected a dead letter entry")
	}
	reason := m.deadLetters.Entries[0].FailureReason
	// PHI values must never appear in failure_reason
	if strings.Contains(reason, "Rosa") || strings.Contains(reason, "1982") {
		t.Errorf("failure_reason contains PHI: %q", reason)
	}
}

// TestSRG310Row_CorrelationTracingIntact verifies that dead letter entries carry
// the correlation_id and row_sequence_number for replay-cli recovery (Open Item #24).
func TestSRG310Row_CorrelationTracingIntact(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")

	stage.processSRG310Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG310("MBR-007", "UNKNOWN", 7),
	)

	if len(m.deadLetters.Entries) == 0 {
		t.Fatal("expected dead letter entry")
	}
	e := m.deadLetters.Entries[0]
	if e.CorrelationID != bf.CorrelationID {
		t.Errorf("correlation_id = %s; want %s", e.CorrelationID, bf.CorrelationID)
	}
	if e.RowSequenceNumber == nil || *e.RowSequenceNumber != 7 {
		t.Errorf("row_sequence_number = %v; want 7", e.RowSequenceNumber)
	}
	if e.TenantID != bf.TenantID {
		t.Errorf("tenant_id = %q; want %q", e.TenantID, bf.TenantID)
	}
}

// ─── processSRG315Row ─────────────────────────────────────────────────────────

// TestSRG315Row_UnknownEventType_DeadLettered verifies that an SRG315 row carrying
// an unrecognised event type is dead-lettered rather than silently dropped or panicked.
func TestSRG315Row_UnknownEventType_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")

	outcome := stage.processSRG315Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG315("MBR-001", "BANANA", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "unknown_srg315_event_type") {
		t.Errorf("failure_reason = %q; want unknown_srg315_event_type", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG315Row_ConsumerNotFound_DeadLettered verifies that an RT37 row for a
// consumer who has no prior RT30 is dead-lettered. RT37 (status change) requires
// the consumer to already exist in domain state.
func TestSRG315Row_ConsumerNotFound_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	// No consumer registered — GetConsumerByNaturalKey will return not-found

	outcome := stage.processSRG315Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG315("MBR-UNKNOWN", "SUSPEND", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "consumer_not_found") {
		t.Errorf("failure_reason = %q; want consumer_not_found", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG315Row_MissingFISCardID_DeadLettered verifies that an RT37 row for a
// consumer whose FIS card ID has not yet been populated (Stage 7 not run) is
// dead-lettered. Submitting an RT37 with empty fis_card_id would be rejected by FIS.
func TestSRG315Row_MissingFISCardID_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	// Consumer exists but has no FIS card ID (fisCardIDOrEmpty returns "")
	m.state.RegisterConsumer(bf.TenantID, "MBR-001", &domain.Consumer{
		ID:             uuid.New(),
		TenantID:       bf.TenantID,
		ClientMemberID: "MBR-001",
	})

	outcome := stage.processSRG315Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG315("MBR-001", "SUSPEND", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "rt37_missing_fis_card_id") {
		t.Errorf("failure_reason = %q; want rt37_missing_fis_card_id", m.deadLetters.Entries[0].FailureReason)
	}
}

// ─── processSRG320Row ─────────────────────────────────────────────────────────

// TestSRG320Row_UnknownCommandType_DeadLettered verifies unrecognised SRG320 command
// types are dead-lettered rather than silently ignored.
func TestSRG320Row_UnknownCommandType_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG320("MBR-001", "REFUND", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "unknown_srg320_command_type") {
		t.Errorf("failure_reason = %q; want unknown_srg320_command_type", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG320Row_ConsumerNotFound_DeadLettered verifies that an RT60 load row
// without a prior consumer is dead-lettered. RT60 requires a resolved fis_card_id.
func TestSRG320Row_ConsumerNotFound_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG320("MBR-NONE", "LOAD", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "consumer_not_found") {
		t.Errorf("failure_reason = %q; want consumer_not_found", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG320Row_MissingFISCardID_DeadLettered verifies that a load row for a consumer
// who has not completed Stage 7 reconciliation (no fis_card_id) is dead-lettered.
// FIS rejects RT60 records without a card ID.
func TestSRG320Row_MissingFISCardID_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	m.state.RegisterConsumer(bf.TenantID, "MBR-001", &domain.Consumer{
		ID:             uuid.New(),
		TenantID:       bf.TenantID,
		ClientMemberID: "MBR-001",
	})

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG320("MBR-001", "LOAD", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "rt60_missing_fis_card_id") {
		t.Errorf("failure_reason = %q; want rt60_missing_fis_card_id", m.deadLetters.Entries[0].FailureReason)
	}
}

// ─── mapping helpers ──────────────────────────────────────────────────────────

// TestSRGEventToCommandType covers all five known event types and the unknown fallback.
func TestSRGEventToCommandType(t *testing.T) {
	cases := []struct {
		event string
		want  string
	}{
		{"ACTIVATE", string(domain.CommandUpdate)},
		{"SUSPEND", string(domain.CommandSuspend)},
		{"TERMINATE", string(domain.CommandTerminate)},
		{"EXPIRE_PURSE", string(domain.CommandSweep)},
		{"WALLET_TRANSFER", string(domain.CommandUpdate)},
		{"UNKNOWN_EVENT", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := srgEventToCommandType(tc.event)
		if got != tc.want {
			t.Errorf("srgEventToCommandType(%q) = %q; want %q", tc.event, got, tc.want)
		}
	}
}

// TestSRG320CommandTypeToCommand covers all known command types and the unknown fallback.
func TestSRG320CommandTypeToCommand(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"LOAD", string(domain.CommandLoad)},
		{"TOPUP", string(domain.CommandLoad)},
		{"RELOAD", string(domain.CommandLoad)},
		{"CASHOUT", string(domain.CommandSweep)},
		{"SWEEP", string(domain.CommandSweep)},
		{"REFUND", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := srg320CommandTypeToCommand(tc.input)
		if got != tc.want {
			t.Errorf("srg320CommandTypeToCommand(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// TestCommandTypeToCardStatus covers known types and verifies the safe default.
func TestCommandTypeToCardStatus(t *testing.T) {
	cases := []struct {
		commandType string
		want        int16
	}{
		{string(domain.CommandUpdate), 2},    // Active
		{string(domain.CommandSuspend), 6},   // Suspended
		{string(domain.CommandTerminate), 7}, // Closed
		{"UNKNOWN", 6},                       // Safe default: Suspended
	}
	for _, tc := range cases {
		got := commandTypeToCardStatus(tc.commandType)
		if got != tc.want {
			t.Errorf("commandTypeToCardStatus(%q) = %d; want %d", tc.commandType, got, tc.want)
		}
	}
}

// TestFISCardIDOrEmpty verifies that fisCardIDOrEmpty always returns ""
// before Stage 7 populates the card's FIS card ID. This is the documented
// pre-Stage-7 behaviour (see TODO in stage3_row_processing.go).
func TestFISCardIDOrEmpty(t *testing.T) {
	c := &domain.Consumer{ID: uuid.New(), TenantID: "rfu-oregon", ClientMemberID: "MBR-001"}
	if got := fisCardIDOrEmpty(c); got != "" {
		t.Errorf("fisCardIDOrEmpty() = %q; want empty string (Stage 7 not yet run)", got)
	}
}

// ─── small helpers ────────────────────────────────────────────────────────────

func TestStrPtrIfNotEmpty(t *testing.T) {
	if strPtrIfNotEmpty("hello") == nil {
		t.Error("expected non-nil for non-empty string")
	}
	if got := *strPtrIfNotEmpty("hello"); got != "hello" {
		t.Errorf("got %q; want hello", got)
	}
	if strPtrIfNotEmpty("") != nil {
		t.Error("expected nil for empty string")
	}
}

func TestTimePtrIfNotZero(t *testing.T) {
	zero := time.Time{}
	if timePtrIfNotZero(zero) != nil {
		t.Error("expected nil for zero time")
	}
	nonZero := time.Now()
	if timePtrIfNotZero(nonZero) == nil {
		t.Error("expected non-nil for non-zero time")
	}
}

// ─── processSRG320Row — purse balance update (§I.2 FBO integrity) ─────────────

// seedConsumerWithCard seeds a consumer with a resolved fis_card_id so the RT60
// path doesn't dead-letter on the card check.
func seedConsumerWithCard(m *stage3Mocks, bf *ports.BatchFile, clientMemberID string) *domain.Consumer {
	c := &domain.Consumer{
		ID:             uuid.New(),
		TenantID:       bf.TenantID,
		ClientMemberID: clientMemberID,
	}
	m.state.RegisterConsumer(bf.TenantID, clientMemberID, c)
	fisCardID := "CARD-001"
	card := &domain.Card{
		ID:         uuid.New(),
		TenantID:   bf.TenantID,
		ConsumerID: c.ID,
		FISCardID:  &fisCardID,
	}
	m.state.RegisterCard(c.ID, card)
	return c
}

// seedPurse seeds a purse for a consumer + benefit period combo.
func seedPurse(m *stage3Mocks, consumerID uuid.UUID, benefitPeriod string, balanceCents int64) *domain.Purse {
	p := &domain.Purse{
		ID:                    uuid.New(),
		ConsumerID:            consumerID,
		BenefitPeriod:         benefitPeriod,
		AvailableBalanceCents: balanceCents,
		Status:                domain.PurseActive,
	}
	m.state.RegisterPurse(consumerID, benefitPeriod, p)
	return p
}

// TestSRG320Row_LoadUpdatesBalance verifies that an AT01 LOAD adds AmountCents
// to the existing purse balance immediately (§I.2).
func TestSRG320Row_LoadUpdatesBalance(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	consumer := seedConsumerWithCard(m, bf, "MBR-LOAD")
	purse := seedPurse(m, consumer.ID, "2026-06", 10000) // existing balance: $100.00

	row := minimalSRG320("MBR-LOAD", "LOAD", 1)
	row.AmountCents = 5000 // load $50.00

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf}, row)

	if outcome != outcomeStagedOK {
		t.Fatalf("outcome = %v; want outcomeStagedOK", outcome)
	}
	if len(m.state.BalanceUpdates) != 1 {
		t.Fatalf("BalanceUpdates count = %d; want 1", len(m.state.BalanceUpdates))
	}
	update := m.state.BalanceUpdates[0]
	if update.PurseID != purse.ID {
		t.Errorf("PurseID = %v; want %v", update.PurseID, purse.ID)
	}
	if update.BalanceCents != 15000 {
		t.Errorf("BalanceCents = %d; want 15000 (10000 + 5000)", update.BalanceCents)
	}
}

// TestSRG320Row_SweepZerosBalance verifies that an AT30 SWEEP sets balance to 0
// regardless of the current balance — contractual period-end expiry (SOW §2.1, §3.3).
func TestSRG320Row_SweepZerosBalance(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	consumer := seedConsumerWithCard(m, bf, "MBR-SWEEP")
	purse := seedPurse(m, consumer.ID, "2026-06", 9500) // balance: $95.00

	row := minimalSRG320("MBR-SWEEP", "SWEEP", 1)
	row.AmountCents = 9500

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf}, row)

	if outcome != outcomeStagedOK {
		t.Fatalf("outcome = %v; want outcomeStagedOK", outcome)
	}
	if len(m.state.BalanceUpdates) != 1 {
		t.Fatalf("BalanceUpdates count = %d; want 1", len(m.state.BalanceUpdates))
	}
	update := m.state.BalanceUpdates[0]
	if update.PurseID != purse.ID {
		t.Errorf("PurseID = %v; want %v", update.PurseID, purse.ID)
	}
	if update.BalanceCents != 0 {
		t.Errorf("BalanceCents = %d; want 0 (sweep zeroes balance)", update.BalanceCents)
	}
}

// TestSRG320Row_PurseNotFound_DeadLettered verifies that a missing purse record
// dead-letters the row — can't update FBO sub-ledger without the purse ID.
func TestSRG320Row_PurseNotFound_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	seedConsumerWithCard(m, bf, "MBR-NOPURSE")
	// No purse seeded — GetPurseByConsumerAndBenefitPeriod will fail

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG320("MBR-NOPURSE", "LOAD", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "purse_lookup_failed") {
		t.Errorf("failure_reason = %q; want purse_lookup_failed", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG320Row_UpdatePurseBalanceFails_DeadLettered verifies that a DB error
// on UpdatePurseBalance dead-letters the row — FBO integrity cannot be guaranteed.
func TestSRG320Row_UpdatePurseBalanceFails_DeadLettered(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	consumer := seedConsumerWithCard(m, bf, "MBR-BALUPDATEERR")
	seedPurse(m, consumer.ID, "2026-06", 5000)
	m.state.UpdatePurseBalanceErr = errors.New("db: connection lost")

	outcome := stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG320("MBR-BALUPDATEERR", "LOAD", 1),
	)

	if outcome != outcomeDeadLettered {
		t.Errorf("outcome = %v; want outcomeDeadLettered", outcome)
	}
	if !strings.Contains(m.deadLetters.Entries[0].FailureReason, "purse_balance_update_failed") {
		t.Errorf("failure_reason = %q; want purse_balance_update_failed", m.deadLetters.Entries[0].FailureReason)
	}
}

// TestSRG320Row_PHINotInBalanceFailureReason verifies PHI (clientMemberID) never
// appears in failure_reason for purse balance failures (§6.5.2, §7.2).
func TestSRG320Row_PHINotInBalanceFailureReason(t *testing.T) {
	stage, m := newStage3WithMocks()
	bf := seedBatchFile(m, "rfu-oregon")
	consumer := seedConsumerWithCard(m, bf, "SSN-123-456")
	seedPurse(m, consumer.ID, "2026-06", 5000)
	m.state.UpdatePurseBalanceErr = errors.New("db error")

	stage.processSRG320Row(context.Background(),
		&RowProcessingInput{BatchFile: bf},
		minimalSRG320("SSN-123-456", "LOAD", 1),
	)

	if len(m.deadLetters.Entries) == 0 {
		t.Fatal("expected dead letter entry")
	}
	reason := m.deadLetters.Entries[0].FailureReason
	if strings.Contains(reason, "SSN-123-456") {
		t.Errorf("failure_reason %q contains PHI — must never log member ID", reason)
	}
}
