// Stage 3 — Row Processing (§5.1, ADR-010):
//   Sequential row-by-row domain command execution.
//   Each CSV row is an independent processing unit with its own idempotency key
//   and failure boundary. One row exception does not abort the file.
//
// Processing order per row (MANDATORY — do not reorder):
//   1. Idempotency check: FindDuplicate on domain_commands composite key
//      (correlation_id, client_member_id, command_type, benefit_period)
//      → if Accepted or Completed exists: skip (return Duplicate)
//   2. Insert domain_commands row with status=Accepted
//   3. Write batch_records_rt30/rt37/rt60 with status=STAGED
//   4. Write domain state: consumers, cards, purses (upsert)
//   5. Write audit_log entry for the state transition
//
// On row exception:
//   - Write to dead_letter_store with failure_stage="row_processing"
//   - Increment failure counter
//   - Continue to next row (never abort the file on a single row failure)
//
// On Stage 3 completion:
//   - Compare staged_count to batch_files.record_count
//   - If staged_count < record_count: transition batch_files → STALLED
//   - STALLED: emit "batch.stalled" log event; do NOT proceed to Stage 4
//   - Resolution: replay-cli tool (Open Item #24)
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"strconv"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_adapters/aurora"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/member_enrollment/srg"
)

// BatchRecordWriter is the narrow write interface for Stage 3 batch record staging.
// Implemented by aurora.BatchRecordsRepo. Defined here (not in ports) to avoid
// an import cycle: ports cannot import aurora types.
type BatchRecordWriter interface {
	InsertRT30(ctx context.Context, rec *aurora.BatchRecordRT30) error
	InsertRT37(ctx context.Context, rec *aurora.BatchRecordRT37) error
	InsertRT60(ctx context.Context, rec *aurora.BatchRecordRT60) error
}

// DomainStateWriter is the narrow read/write interface for Stage 3 domain state.
// Implemented by aurora.DomainStateRepo. Defined here to avoid the same import cycle.
type DomainStateWriter interface {
	UpsertConsumer(ctx context.Context, c *domain.Consumer) error
	InsertCard(ctx context.Context, c *domain.Card) error
	GetConsumerByNaturalKey(ctx context.Context, tenantID, clientMemberID string) (*domain.Consumer, error)
	GetCardByConsumerID(ctx context.Context, consumerID uuid.UUID) (*domain.Card, error)
	// Purse sub-ledger — required for FBO integrity (§I.2).
	// Balance updated at RT60 write time, not deferred until FIS return.
	GetPurseByConsumerAndBenefitPeriod(ctx context.Context, consumerID uuid.UUID, benefitPeriod string) (*domain.Purse, error)
	UpdatePurseBalance(ctx context.Context, id uuid.UUID, balanceCents int64) error
}

// RowProcessingStage implements Stage 3.
type RowProcessingStage struct {
	DomainCommands ports.DomainCommandRepository
	DeadLetters    ports.DeadLetterRepository
	BatchFiles     ports.BatchFileRepository
	BatchRecords   BatchRecordWriter
	DomainState    DomainStateWriter
	Programs       ports.ProgramLookup // narrow interface for program resolution (testable)
	Audit          ports.AuditLogWriter
	Obs            ports.IObservabilityPort

	// programCache memoises GetProgramByTenantAndSubprogram results within a
	// single Stage 3 run. Key: tenantID+"|"+fisSubprogramID → programs.id UUID.
	// Avoids N identical DB round-trips when all SRG310 rows share one subprogram.
	programCache map[string]uuid.UUID
}

// RowProcessingInput carries everything Stage 3 needs.
type RowProcessingInput struct {
	BatchFile    *ports.BatchFile
	SRG310Rows   []*srg.SRG310Row
	SRG315Rows   []*srg.SRG315Row
	SRG320Rows   []*srg.SRG320Row
	ProgramID    uuid.UUID
	SubprogramID int64
}

// Result captures the outcome of Stage 3.
type RowProcessingResult struct {
	StagedCount    int
	FailedCount    int
	DuplicateCount int
	// Stalled is true if unresolved dead letters prevent Stage 4 from proceeding.
	Stalled bool
}

// Run processes all rows sequentially.
func (s *RowProcessingStage) Run(ctx context.Context, in *RowProcessingInput) (*RowProcessingResult, error) {
	result := &RowProcessingResult{}

	// Transition batch_files → PROCESSING
	if err := s.BatchFiles.UpdateStatus(ctx, in.BatchFile.ID, "PROCESSING", time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("stage3: transition to PROCESSING: %w", err)
	}

	// Process SRG310 rows (ENROLL)
	for _, row := range in.SRG310Rows {
		outcome := s.processSRG310Row(ctx, in, row)
		switch outcome {
		case outcomeStagedOK:
			result.StagedCount++
		case outcomeDuplicate:
			result.DuplicateCount++
		case outcomeDeadLettered:
			result.FailedCount++
		}
	}

	// Process SRG315 rows (UPDATE/SUSPEND/TERMINATE)
	for _, row := range in.SRG315Rows {
		outcome := s.processSRG315Row(ctx, in, row)
		switch outcome {
		case outcomeStagedOK:
			result.StagedCount++
		case outcomeDuplicate:
			result.DuplicateCount++
		case outcomeDeadLettered:
			result.FailedCount++
		}
	}

	// Process SRG320 rows (LOAD)
	for _, row := range in.SRG320Rows {
		outcome := s.processSRG320Row(ctx, in, row)
		switch outcome {
		case outcomeStagedOK:
			result.StagedCount++
		case outcomeDuplicate:
			result.DuplicateCount++
		case outcomeDeadLettered:
			result.FailedCount++
		}
	}

	// Check for STALLED condition: unresolved dead letters
	totalExpected := len(in.SRG310Rows) + len(in.SRG315Rows) + len(in.SRG320Rows)
	if result.FailedCount > 0 {
		unresolved, _ := s.DeadLetters.ListUnresolved(ctx, in.BatchFile.CorrelationID)
		if len(unresolved) > 0 {
			result.Stalled = true
			_ = s.BatchFiles.UpdateStatus(ctx, in.BatchFile.ID, "STALLED", time.Now().UTC())
			_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
				EventType:     observability.EventBatchStalled,
				Level:         "WARN",
				CorrelationID: &in.BatchFile.CorrelationID,
				TenantID:      &in.BatchFile.TenantID,
				BatchFileID:   &in.BatchFile.ID,
				Stage:         strPtr("stage3_row_processing"),
				Message: fmt.Sprintf(
					"batch STALLED: %d unresolved dead letters of %d total rows",
					len(unresolved), totalExpected,
				),
			})
			return result, nil
		}
	}

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage3.complete",
		Level:         "INFO",
		CorrelationID: &in.BatchFile.CorrelationID,
		TenantID:      &in.BatchFile.TenantID,
		BatchFileID:   &in.BatchFile.ID,
		Stage:         strPtr("stage3_row_processing"),
		Message: fmt.Sprintf("stage3 complete: staged=%d duplicates=%d failed=%d",
			result.StagedCount, result.DuplicateCount, result.FailedCount),
	})

	return result, nil
}

type rowOutcome int

const (
	outcomeStagedOK    rowOutcome = iota
	outcomeDuplicate
	outcomeDeadLettered
)

func (s *RowProcessingStage) processSRG310Row(ctx context.Context, in *RowProcessingInput, row *srg.SRG310Row) rowOutcome {
	// Resolve program UUID from SubprogramID on the SRG row.
	// This is the FK anchor for consumers.program_id and purses.program_id.
	// Must succeed before any write — dead-letter the row if the program is unknown.
	programID, err := s.lookupProgram(ctx, in.BatchFile.TenantID, row.SubprogramID)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("program_lookup_failed(subprogram=%s): %v", row.SubprogramID, err),
			row.Raw)
	}

	// Step 1: Idempotency check — MUST precede all writes (§4.1.1)
	existing, err := s.DomainCommands.FindDuplicate(ctx,
		in.BatchFile.TenantID, row.ClientMemberID,
		string(domain.CommandEnroll), row.BenefitPeriod,
		in.BatchFile.CorrelationID,
	)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("idempotency_check_failed: %v", err),
			row.Raw)
	}
	if existing != nil {
		return outcomeDuplicate // already processed — skip safely
	}

	// Step 2: Insert domain_commands — this is the write-side CQRS record
	cmdID := uuid.New()
	now := time.Now().UTC()
	cmd := &ports.DomainCommand{
		ID:             cmdID,
		CorrelationID:  in.BatchFile.CorrelationID,
		TenantID:       in.BatchFile.TenantID,
		ClientMemberID: row.ClientMemberID,
		CommandType:    string(domain.CommandEnroll),
		BenefitPeriod:  row.BenefitPeriod,
		Status:         string(domain.CommandAccepted),
		BatchFileID:    in.BatchFile.ID,
		SequenceInFile: row.SequenceInFile,
		CreatedAt:      now,
	}
	if err := s.DomainCommands.Insert(ctx, cmd); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("domain_command_insert_failed: %v", err),
			row.Raw)
	}

	// Step 3: Write batch_records_rt30 (STAGED)
	rawJSON, _ := json.Marshal(row.Raw) // PHI — stored for replay; never logged
	// Parse row.SubprogramID string → int64 for the RT30 record (validated via program lookup above)
	var subprogramID *int64
	if row.SubprogramID != "" {
		parsedSub, parseErr := strconv.ParseInt(row.SubprogramID, 10, 64)
		if parseErr == nil {
			subprogramID = &parsedSub
		}
	}
	rt30 := &aurora.BatchRecordRT30{
		ID:             uuid.New(),
		BatchFileID:    in.BatchFile.ID,
		DomainCommandID: cmdID,
		CorrelationID:  in.BatchFile.CorrelationID,
		TenantID:       in.BatchFile.TenantID,
		SequenceInFile: row.SequenceInFile,
		Status:         "STAGED",
		StagedAt:       now,
		ClientMemberID: row.ClientMemberID,
		SubprogramID:   subprogramID,
		FirstName:      strPtrIfNotEmpty(row.FirstName),
		LastName:       strPtrIfNotEmpty(row.LastName),
		DateOfBirth:    timePtrIfNotZero(row.DOB),
		Address1:       strPtrIfNotEmpty(row.Address1),
		Address2:       strPtrIfNotEmpty(row.Address2),
		City:           strPtrIfNotEmpty(row.City),
		State:          strPtrIfNotEmpty(row.State),
		ZIP:            strPtrIfNotEmpty(row.ZIP),
		Email:          strPtrIfNotEmpty(row.Email),
		CardDesignID:   strPtrIfNotEmpty(row.CardDesignID),
		CustomCardID:   strPtrIfNotEmpty(row.CustomCardID),
		PackageID:      strPtrIfNotEmpty(row.PackageID),
		RawPayload:     rawJSON,
	}
	if err := s.BatchRecords.InsertRT30(ctx, rt30); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("rt30_insert_failed: %v", err),
			row.Raw)
	}

	// Step 4: Write domain state — Consumer upsert
	consumer := &domain.Consumer{
		ID:                uuid.New(),
		TenantID:          in.BatchFile.TenantID,
		ClientMemberID:    row.ClientMemberID,
		Status:            domain.ConsumerActive,
		FirstName:         row.FirstName,
		LastName:          row.LastName,
		DOB:               row.DOB,
		Address1:          row.Address1,
		Address2:          &row.Address2,
		City:              row.City,
		State:             row.State,
		ZIP:               row.ZIP,
		Email:             &row.Email,
		ProgramID:         programID, // resolved from row.SubprogramID via programs table
		SubprogramID:      func() int64 { if subprogramID != nil { return *subprogramID }; return 0 }(),
		ContractPBP:       &row.ContractPBP,
		CustomCardID:      &row.CustomCardID,
		SourceBatchFileID: in.BatchFile.ID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.DomainState.UpsertConsumer(ctx, consumer); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("consumer_upsert_failed: %v", err),
			row.Raw)
	}

	// Card starts as Ready (status=1) — FIS assigns card ID after RT30 return (Stage 7)
	card := &domain.Card{
		ID:                uuid.New(),
		TenantID:          in.BatchFile.TenantID,
		ConsumerID:        consumer.ID,
		ClientMemberID:    row.ClientMemberID,
		Status:            domain.CardReady,
		CardDesignID:      &row.CardDesignID,
		PackageID:         &row.PackageID,
		SourceBatchFileID: in.BatchFile.ID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if err := s.DomainState.InsertCard(ctx, card); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("card_insert_failed: %v", err),
			row.Raw)
	}

	// Step 5: Audit log
	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:       in.BatchFile.TenantID,
		EntityType:     "consumers",
		EntityID:       consumer.ID.String(),
		NewState:       "ACTIVE",
		ChangedBy:      "ingest-task:stage3",
		CorrelationID:  &in.BatchFile.CorrelationID,
		ClientMemberID: &row.ClientMemberID,
	})

	return outcomeStagedOK
}

func (s *RowProcessingStage) processSRG315Row(ctx context.Context, in *RowProcessingInput, row *srg.SRG315Row) rowOutcome {
	commandType := srgEventToCommandType(row.EventType)
	if commandType == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("unknown_srg315_event_type: %q", row.EventType),
			row.Raw)
	}

	// Idempotency check
	existing, err := s.DomainCommands.FindDuplicate(ctx,
		in.BatchFile.TenantID, row.ClientMemberID,
		commandType, row.BenefitPeriod,
		in.BatchFile.CorrelationID,
	)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("idempotency_check_failed: %v", err), row.Raw)
	}
	if existing != nil {
		return outcomeDuplicate
	}

	cmdID := uuid.New()
	now := time.Now().UTC()
	if err := s.DomainCommands.Insert(ctx, &ports.DomainCommand{
		ID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, ClientMemberID: row.ClientMemberID,
		CommandType: commandType, BenefitPeriod: row.BenefitPeriod,
		Status: string(domain.CommandAccepted),
		BatchFileID: in.BatchFile.ID, SequenceInFile: row.SequenceInFile,
		CreatedAt: now,
	}); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("domain_command_insert_failed: %v", err), row.Raw)
	}

	// RT37 requires fis_card_id — look it up from consumers → cards
	consumer, err := s.DomainState.GetConsumerByNaturalKey(ctx, in.BatchFile.TenantID, row.ClientMemberID)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"consumer_not_found: RT37 requires prior RT30 completion", row.Raw)
	}
	rt37Card, err := s.DomainState.GetCardByConsumerID(ctx, consumer.ID)
	if err != nil || fisCardIDOrEmpty(rt37Card) == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"rt37_missing_fis_card_id: RT30 not yet reconciled (Stage 7 pending)", row.Raw)
	}

	rawJSON, _ := json.Marshal(row.Raw)
	rt37 := &aurora.BatchRecordRT37{
		ID: uuid.New(), BatchFileID: in.BatchFile.ID,
		DomainCommandID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, SequenceInFile: row.SequenceInFile,
		Status: "STAGED", StagedAt: now,
		ClientMemberID: row.ClientMemberID,
		FISCardID:      fisCardIDOrEmpty(rt37Card),
		CardStatusCode: commandTypeToCardStatus(commandType),
		RawPayload:     rawJSON,
	}
	if err := s.BatchRecords.InsertRT37(ctx, rt37); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("rt37_insert_failed: %v", err), row.Raw)
	}

	return outcomeStagedOK
}

func (s *RowProcessingStage) processSRG320Row(ctx context.Context, in *RowProcessingInput, row *srg.SRG320Row) rowOutcome {
	commandType := srg320CommandTypeToCommand(row.CommandType)
	if commandType == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("unknown_srg320_command_type: %q", row.CommandType), row.Raw)
	}

	// Idempotency check
	existing, err := s.DomainCommands.FindDuplicate(ctx,
		in.BatchFile.TenantID, row.ClientMemberID,
		commandType, row.BenefitPeriod,
		in.BatchFile.CorrelationID,
	)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("idempotency_check_failed: %v", err), row.Raw)
	}
	if existing != nil {
		return outcomeDuplicate
	}

	cmdID := uuid.New()
	now := time.Now().UTC()
	if err := s.DomainCommands.Insert(ctx, &ports.DomainCommand{
		ID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, ClientMemberID: row.ClientMemberID,
		CommandType: commandType, BenefitPeriod: row.BenefitPeriod,
		Status: string(domain.CommandAccepted),
		BatchFileID: in.BatchFile.ID, SequenceInFile: row.SequenceInFile,
		CreatedAt: now,
	}); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("domain_command_insert_failed: %v", err), row.Raw)
	}

	// RT60 requires fis_card_id — look it up from consumers → cards
	consumer, err := s.DomainState.GetConsumerByNaturalKey(ctx, in.BatchFile.TenantID, row.ClientMemberID)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"consumer_not_found: RT60 requires prior RT30 completion", row.Raw)
	}
	rt60Card, err := s.DomainState.GetCardByConsumerID(ctx, consumer.ID)
	if err != nil || fisCardIDOrEmpty(rt60Card) == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"rt60_missing_fis_card_id: RT30 not yet reconciled (Stage 7 pending)", row.Raw)
	}

	atCode := "AT01" // load
	if commandType == string(domain.CommandSweep) {
		atCode = "AT30" // period-end sweep
	}

	rawJSON, _ := json.Marshal(row.Raw)
	expiryDate := row.ExpiryDate
	rt60 := &aurora.BatchRecordRT60{
		ID: uuid.New(), BatchFileID: in.BatchFile.ID,
		DomainCommandID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, SequenceInFile: row.SequenceInFile,
		Status: "STAGED", StagedAt: now,
		ATCode:         &atCode,
		ClientMemberID: row.ClientMemberID,
		FISCardID:      fisCardIDOrEmpty(rt60Card),
		AmountCents:    row.AmountCents,
		EffectiveDate:  row.EffectiveDate,
		ExpiryDate:     &expiryDate,
		RawPayload:     rawJSON,
	}
	if err := s.BatchRecords.InsertRT60(ctx, rt60); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("rt60_insert_failed: %v", err), row.Raw)
	}

	// Update One Fintech sub-ledger balance immediately — not deferred (§I.2).
	// FBO integrity requirement: balance reflects accepted command at write time.
	purse, err := s.DomainState.GetPurseByConsumerAndBenefitPeriod(ctx, consumer.ID, row.BenefitPeriod)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("purse_lookup_failed: %v", err), row.Raw)
	}

	var newBalance int64
	if commandType == string(domain.CommandSweep) {
		// AT30 period-end sweep zeroes the purse — contract deadline (SOW §2.1, §3.3)
		newBalance = 0
	} else {
		// AT01 load: add to current balance
		newBalance = purse.AvailableBalanceCents + row.AmountCents
	}

	if err := s.DomainState.UpdatePurseBalance(ctx, purse.ID, newBalance); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("purse_balance_update_failed: %v", err), row.Raw)
	}

	return outcomeStagedOK
}

// lookupProgram resolves programs.id for a (tenantID, fisSubprogramID) pair.
// Results are cached on RowProcessingStage.programCache for the lifetime of the
// Stage 3 run — so the DB is hit at most once per unique subprogram per file.
func (s *RowProcessingStage) lookupProgram(ctx context.Context, tenantID, fisSubprogramID string) (uuid.UUID, error) {
	if s.programCache == nil {
		s.programCache = make(map[string]uuid.UUID)
	}
	cacheKey := tenantID + "|" + fisSubprogramID
	if id, ok := s.programCache[cacheKey]; ok {
		return id, nil
	}
	id, err := s.Programs.GetProgramByTenantAndSubprogram(ctx, tenantID, fisSubprogramID)
	if err != nil {
		return uuid.Nil, err
	}
	s.programCache[cacheKey] = id
	return id, nil
}

// deadLetter writes a failed row to dead_letter_store and returns outcomeDeadLettered.
// The failure_reason must contain only a structured code — NO PHI ever (§6.5.2, §7.2).
// rawRecord contains PHI and is stored in message_body — never logged.
func (s *RowProcessingStage) deadLetter(
	ctx context.Context,
	in *RowProcessingInput,
	seq int,
	clientMemberID string,
	failureStage string,
	failureReason string, // structured code only — no PHI
	rawRecord map[string]string, // PHI — stored as JSONB; never log
) rowOutcome {
	msgBody, _ := json.Marshal(rawRecord) // PHI — stored but never logged
	batchFileID := in.BatchFile.ID
	_ = s.DeadLetters.Write(ctx, &ports.DeadLetterEntry{
		ID:                uuid.New(),
		CorrelationID:     in.BatchFile.CorrelationID,
		BatchFileID:       &batchFileID,
		RowSequenceNumber: &seq,
		TenantID:          in.BatchFile.TenantID,
		ClientMemberID:    &clientMemberID,
		FailureStage:      failureStage,
		FailureReason:     failureReason,
		MessageBody:       msgBody,
		RetryCount:        0,
		CreatedAt:         time.Now().UTC(),
	})

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:         observability.EventDeadLetterWritten,
		Level:             "WARN",
		CorrelationID:     &in.BatchFile.CorrelationID,
		TenantID:          &in.BatchFile.TenantID,
		BatchFileID:       &in.BatchFile.ID,
		RowSequenceNumber: &seq,
		Stage:             strPtr(failureStage),
		// failure_reason logged here (structured code only — no PHI)
		Message: fmt.Sprintf("dead_letter: seq=%d reason=%s", seq, failureReason),
	})

	return outcomeDeadLettered
}

// ─── mapping helpers ──────────────────────────────────────────────────────────

func srgEventToCommandType(eventType string) string {
	switch eventType {
	case "ACTIVATE":
		return string(domain.CommandUpdate)
	case "SUSPEND":
		return string(domain.CommandSuspend)
	case "TERMINATE":
		return string(domain.CommandTerminate)
	case "EXPIRE_PURSE":
		return string(domain.CommandSweep)
	case "WALLET_TRANSFER":
		return string(domain.CommandUpdate)
	default:
		return ""
	}
}

func srg320CommandTypeToCommand(commandType string) string {
	switch commandType {
	case "LOAD", "TOPUP", "RELOAD":
		return string(domain.CommandLoad)
	case "CASHOUT", "SWEEP":
		return string(domain.CommandSweep)
	default:
		return ""
	}
}

func commandTypeToCardStatus(commandType string) int16 {
	switch commandType {
	case string(domain.CommandUpdate):
		return 2 // Active
	case string(domain.CommandSuspend):
		return 6 // Suspended
	case string(domain.CommandTerminate):
		return 7 // Closed
	default:
		return 6 // Default to suspended for unknown
	}
}

func fisCardIDOrEmpty(c *domain.Card) string {
	if c == nil || c.FISCardID == nil {
		return ""
	}
	return *c.FISCardID
}

// ─── small helpers ────────────────────────────────────────────────────────────

func strPtrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtrIfNotZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
