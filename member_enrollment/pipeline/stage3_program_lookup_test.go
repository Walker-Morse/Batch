package pipeline

// Tests for the program lookup gap fix.
//
// Scope: lookupProgram cache behaviour and the dead-letter path when a
// subprogram_id is not found in the programs table.
//
// DomainStateRepo and BatchRecordsRepo are concrete Aurora types (not interfaces),
// so processSRG310Row end-to-end tests require a live DB — those are integration
// tests for a future suite. These unit tests isolate the two pieces of new logic
// that ARE behind the ports.ProgramLookup interface:
//
//   TestLookupProgram_ReturnsKnownProgramID       — happy path UUID returned
//   TestLookupProgram_CachesResult                — cache absorbs repeat calls
//   TestLookupProgram_DeadLettersOnUnknownProgram — unknown program → dead letter
//   TestLookupProgram_MultipleSubprograms         — two distinct subprograms, two DB hits

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/_shared/testutil"
	"github.com/walker-morse/batch/member_enrollment/srg"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTestBatchFile(tenantID string) *ports.BatchFile {
	return &ports.BatchFile{
		ID:            uuid.New(),
		CorrelationID: uuid.New(),
		TenantID:      tenantID,
		ClientID:      "rfu",
		FileType:      "SRG310",
		Status:        "RECEIVED",
		ArrivedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
}

func newMinimalSRG310Row(clientMemberID, subprogramID string) *srg.SRG310Row {
	return &srg.SRG310Row{
		SequenceInFile: 1,
		ClientMemberID: clientMemberID,
		SubprogramID:   subprogramID,
		FirstName:      "Rosa",
		LastName:       "Garcia",
		DOB:            time.Date(1980, 3, 15, 0, 0, 0, 0, time.UTC),
		Address1:       "123 Main St",
		City:           "Portland",
		State:          "OR",
		ZIP:            "97201",
		BenefitPeriod:  "2026-06",
		BenefitType:    "OTC",
		Raw:            map[string]string{"client_member_id": clientMemberID},
	}
}

// ─── TestLookupProgram_ReturnsKnownProgramID ──────────────────────────────────

// TestLookupProgram_ReturnsKnownProgramID verifies the happy path:
// lookupProgram returns the UUID registered for the given tenant and subprogram.
func TestLookupProgram_ReturnsKnownProgramID(t *testing.T) {
	programID := uuid.New()
	programs := testutil.NewMockProgramLookup()
	programs.Register("rfu-oregon", "26071", programID)

	stage := &RowProcessingStage{Programs: programs}
	got, err := stage.lookupProgram(context.Background(), "rfu-oregon", "26071")
	if err != nil {
		t.Fatalf("lookupProgram() error: %v", err)
	}
	if got != programID {
		t.Errorf("expected %s, got %s", programID, got)
	}
}

// ─── TestLookupProgram_CachesResult ──────────────────────────────────────────

// TestLookupProgram_CachesResult verifies the in-process memoisation:
// five calls for the same key must produce exactly one DB round-trip.
// RFU files are 100% one subprogram — without the cache that is N DB calls per file.
func TestLookupProgram_CachesResult(t *testing.T) {
	programID := uuid.New()
	programs := testutil.NewMockProgramLookup()
	programs.Register("rfu-oregon", "26071", programID)

	stage := &RowProcessingStage{Programs: programs}

	for i := 0; i < 5; i++ {
		got, err := stage.lookupProgram(context.Background(), "rfu-oregon", "26071")
		if err != nil {
			t.Fatalf("call %d: error: %v", i, err)
		}
		if got != programID {
			t.Errorf("call %d: expected %s, got %s", i, programID, got)
		}
	}

	if programs.CallCount() != 1 {
		t.Errorf("expected exactly 1 DB call; cache should absorb the rest. got %d", programs.CallCount())
	}
}

// ─── TestLookupProgram_DeadLettersOnUnknownProgram ───────────────────────────

// TestLookupProgram_DeadLettersOnUnknownProgram verifies that a row whose
// subprogram_id has no active programs row is dead-lettered with
// failure_stage="row_processing" and failure_reason containing "program_lookup_failed".
// The row must NOT cause Stage 3 to abort — subsequent rows must still be processed.
func TestLookupProgram_DeadLettersOnUnknownProgram(t *testing.T) {
	programs    := testutil.NewMockProgramLookup() // no registrations — all lookups fail
	deadLetters := testutil.NewMockDeadLetterRepository()
	batchFiles  := testutil.NewMockBatchFileRepository()
	obs         := &testutil.MockObservability{}
	bf          := newTestBatchFile("rfu-oregon")
	batchFiles.Files[bf.ID] = bf

	stage := &RowProcessingStage{
		Programs:       programs,
		DomainCommands: testutil.NewMockDomainCommandRepository(),
		DeadLetters:    deadLetters,
		BatchFiles:     batchFiles,
		Audit:          &testutil.MockAuditLogWriter{},
		Obs:            obs,
	}

	outcome := stage.processSRG310Row(
		context.Background(),
		&RowProcessingInput{BatchFile: bf},
		newMinimalSRG310Row("MBR-001", "99999"), // unknown subprogram
	)

	// Row must be dead-lettered, not staged
	if outcome != outcomeDeadLettered {
		t.Errorf("expected outcomeDeadLettered, got %v", outcome)
	}

	// Exactly one dead letter entry
	if len(deadLetters.Entries) != 1 {
		t.Fatalf("expected 1 dead letter entry, got %d", len(deadLetters.Entries))
	}

	entry := deadLetters.Entries[0]

	// failure_stage must identify row_processing
	if entry.FailureStage != string(domain.FailureRowProcessing) {
		t.Errorf("failure_stage = %q; want %q", entry.FailureStage, domain.FailureRowProcessing)
	}

	// failure_reason must be a structured code — never PHI, never empty
	if entry.FailureReason == "" {
		t.Error("failure_reason must not be empty")
	}

	// Correlation and sequence tracing must be intact for replay-cli
	if entry.CorrelationID != bf.CorrelationID {
		t.Errorf("correlation_id mismatch: got %s want %s", entry.CorrelationID, bf.CorrelationID)
	}
	if entry.RowSequenceNumber == nil || *entry.RowSequenceNumber != 1 {
		t.Errorf("row_sequence_number should be 1, got %v", entry.RowSequenceNumber)
	}
}

// ─── TestLookupProgram_MultipleSubprograms ────────────────────────────────────

// TestLookupProgram_MultipleSubprograms verifies that two distinct subprogram IDs
// within one Stage 3 run each get their own DB call, and subsequent hits for each
// are served from cache without additional DB round-trips.
func TestLookupProgram_MultipleSubprograms(t *testing.T) {
	prog1 := uuid.New()
	prog2 := uuid.New()
	programs := testutil.NewMockProgramLookup()
	programs.Register("rfu-oregon", "26071", prog1)
	programs.Register("rfu-oregon", "26072", prog2)

	stage := &RowProcessingStage{Programs: programs}
	ctx := context.Background()

	// First call for each key — must hit DB
	id1, err := stage.lookupProgram(ctx, "rfu-oregon", "26071")
	if err != nil || id1 != prog1 {
		t.Errorf("26071: expected %s err=nil, got %s err=%v", prog1, id1, err)
	}
	id2, err := stage.lookupProgram(ctx, "rfu-oregon", "26072")
	if err != nil || id2 != prog2 {
		t.Errorf("26072: expected %s err=nil, got %s err=%v", prog2, id2, err)
	}
	if programs.CallCount() != 2 {
		t.Errorf("expected 2 DB calls for 2 distinct subprograms, got %d", programs.CallCount())
	}

	// Second call for each key — must hit cache only
	stage.lookupProgram(ctx, "rfu-oregon", "26071")
	stage.lookupProgram(ctx, "rfu-oregon", "26072")
	if programs.CallCount() != 2 {
		t.Errorf("cache not working: expected still 2 DB calls after cache hits, got %d", programs.CallCount())
	}
}
