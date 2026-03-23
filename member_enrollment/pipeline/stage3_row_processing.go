// Stage 3 — Row Processing (§5.1, ADR-010):
//   Sequential row-by-row domain command execution.
//   Each CSV row is an independent processing unit with its own idempotency key
//   and failure boundary. One row exception does not abort the file.
//
// Processing order per row (MANDATORY — do not reorder):
//   1. Idempotency check: FindDuplicate on domain_commands composite key
//   2. Insert domain_commands row with status=Accepted
//   3. Write batch_records_rt30/rt37/rt60 with status=STAGED
//   4. Write domain state: consumers, cards, purses (upsert)
//   5. Write audit_log entry for the state transition
//
// On row exception:
//   - Write to dead_letter_store with failure_stage="row_processing"
//   - Increment failure counter
//   - Continue to next row (never abort the file on a single row failure)
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_adapters/aurora"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/observability"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/member_enrollment/srg"
)

// BatchRecordWriter is the narrow write interface for Stage 3 batch record staging.
type BatchRecordWriter interface {
	InsertRT30(ctx context.Context, rec *aurora.BatchRecordRT30) error
	InsertRT37(ctx context.Context, rec *aurora.BatchRecordRT37) error
	InsertRT60(ctx context.Context, rec *aurora.BatchRecordRT60) error
}

// DomainStateWriter is the narrow read/write interface for Stage 3 domain state.
type DomainStateWriter interface {
	UpsertConsumer(ctx context.Context, c *domain.Consumer) error
	InsertCard(ctx context.Context, c *domain.Card) error
	GetConsumerByNaturalKey(ctx context.Context, tenantID, clientMemberID string) (*domain.Consumer, error)
	GetCardByConsumerID(ctx context.Context, consumerID uuid.UUID) (*domain.Card, error)
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
	Programs       ports.ProgramLookup
	Audit          ports.AuditLogWriter
	Obs            ports.IObservabilityPort
	programCache   map[string]uuid.UUID
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

// RowProcessingResult captures the outcome of Stage 3.
type RowProcessingResult struct {
	StagedCount    int
	FailedCount    int
	DuplicateCount int
	Stalled        bool
}

// Run processes all rows sequentially.
func (s *RowProcessingStage) Run(ctx context.Context, in *RowProcessingInput) (*RowProcessingResult, error) {
	result := &RowProcessingResult{}

	if err := s.BatchFiles.UpdateStatus(ctx, in.BatchFile.ID, "PROCESSING", time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("stage3: transition to PROCESSING: %w", err)
	}

	for _, row := range in.SRG310Rows {
		switch s.processSRG310Row(ctx, in, row) {
		case outcomeStagedOK:
			result.StagedCount++
		case outcomeDuplicate:
			result.DuplicateCount++
		case outcomeDeadLettered:
			result.FailedCount++
		}
	}

	for _, row := range in.SRG315Rows {
		switch s.processSRG315Row(ctx, in, row) {
		case outcomeStagedOK:
			result.StagedCount++
		case outcomeDuplicate:
			result.DuplicateCount++
		case outcomeDeadLettered:
			result.FailedCount++
		}
	}

	for _, row := range in.SRG320Rows {
		switch s.processSRG320Row(ctx, in, row) {
		case outcomeStagedOK:
			result.StagedCount++
		case outcomeDuplicate:
			result.DuplicateCount++
		case outcomeDeadLettered:
			result.FailedCount++
		}
	}

	totalExpected := len(in.SRG310Rows) + len(in.SRG315Rows) + len(in.SRG320Rows)
	if result.FailedCount > 0 {
		unresolved, _ := s.DeadLetters.ListUnresolved(ctx, in.BatchFile.CorrelationID)
		if len(unresolved) > 0 {
			result.Stalled = true
			_ = s.BatchFiles.UpdateStatus(ctx, in.BatchFile.ID, "STALLED", time.Now().UTC())
			failed := len(unresolved)
			_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
				EventType:     observability.EventBatchStalled,
				Level:         "WARN",
				CorrelationID: in.BatchFile.CorrelationID,
				TenantID:      in.BatchFile.TenantID,
				BatchFileID:   in.BatchFile.ID,
				Stage:         strPtr("stage3_row_processing"),
				Failed:        &failed,
				Total:         &totalExpected,
				Message: fmt.Sprintf(
					"batch STALLED: %d unresolved dead letters of %d total rows",
					len(unresolved), totalExpected,
				),
			})
			return result, nil
		}
	}

	staged := result.StagedCount
	dups := result.DuplicateCount
	failed := result.FailedCount
	rt30 := 0; rt37 := 0; rt60 := 0
	for range in.SRG310Rows { rt30++ }
	for range in.SRG315Rows { rt37++ }
	for range in.SRG320Rows { rt60++ }
	// Adjust for dead-lettered rows (counted in FailedCount, not record type counts)
	dlRate := "0.0%"
	total := staged + dups + failed
	if total > 0 {
		dlRate = fmt.Sprintf("%.1f%%", float64(failed)/float64(total)*100)
	}

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:      "stage3.complete",
		Level:          "INFO",
		CorrelationID:  in.BatchFile.CorrelationID,
		TenantID:       in.BatchFile.TenantID,
		BatchFileID:    in.BatchFile.ID,
		Stage:          strPtr("stage3_row_processing"),
		Staged:         &staged,
		RT30Count:      &rt30,
		RT37Count:      &rt37,
		RT60Count:      &rt60,
		Duplicates:     &dups,
		Failed:         &failed,
		DeadLetterRate: &dlRate,
		Message: fmt.Sprintf("stage3 complete: staged=%d duplicates=%d failed=%d",
			result.StagedCount, result.DuplicateCount, result.FailedCount),
	})

	return result, nil
}

type rowOutcome int

const (
	outcomeStagedOK rowOutcome = iota
	outcomeDuplicate
	outcomeDeadLettered
)

// validateSRG310Row checks all fields required by FIS before any DB write.
// Returns a non-empty reason string if the row should be dead-lettered.
// Validated here so failures are caught before the domain command is created —
// a domain command without a valid RT30 is an orphan that blocks the member.
func validateSRG310Row(row *srg.SRG310Row) string {
	validStates := map[string]bool{
		"AL":"","AK":"","AZ":"","AR":"","CA":"","CO":"","CT":"","DE":"","FL":"","GA":"",
		"HI":"","ID":"","IL":"","IN":"","IA":"","KS":"","KY":"","LA":"","ME":"","MD":"",
		"MA":"","MI":"","MN":"","MS":"","MO":"","MT":"","NE":"","NV":"","NH":"","NJ":"",
		"NM":"","NY":"","NC":"","ND":"","OH":"","OK":"","OR":"","PA":"","RI":"","SC":"",
		"SD":"","TN":"","TX":"","UT":"","VT":"","VA":"","WA":"","WV":"","WI":"","WY":"",
		"DC":"","PR":"","VI":"","GU":"","MP":"","AS":"",
	}

	switch {
	case row.ClientMemberID == "":
		return "missing_required_field: client_member_id"
	case row.FirstName == "":
		return "missing_required_field: first_name"
	case row.LastName == "":
		return "missing_required_field: last_name"
	case row.DOB.IsZero():
		return "missing_required_field: date_of_birth"
	case row.DOB.After(time.Now().UTC()):
		return "invalid_field: date_of_birth is in the future"
	case time.Since(row.DOB).Hours() > 120*365*24:
		return "invalid_field: date_of_birth implies age > 120 years"
	case row.Address1 == "":
		return "missing_required_field: address_1"
	case row.City == "":
		return "missing_required_field: city"
	case row.State == "":
		return "missing_required_field: state"
	case len(row.State) != 2:
		return fmt.Sprintf("invalid_field: state must be 2 chars, got %q", row.State)
	case !func() bool { _, ok := validStates[row.State]; return ok }():
		return fmt.Sprintf("invalid_field: unrecognised state code %q", row.State)
	case row.ZIP == "":
		return "missing_required_field: zip"
	case !func() bool {
		if len(row.ZIP) == 5 { for _, c := range row.ZIP { if c < '0' || c > '9' { return false } }; return true }
		if len(row.ZIP) == 10 && row.ZIP[5] == '-' {
			for i, c := range row.ZIP { if i == 5 { continue }; if c < '0' || c > '9' { return false } }; return true
		}
		return false
	}():
		return fmt.Sprintf("invalid_field: zip must be 5 or 9 digits, got %q", row.ZIP)
	case row.SubprogramID == "":
		return "missing_required_field: subprogram_id"
	case row.BenefitPeriod == "":
		return "missing_required_field: benefit_period"
	case row.PackageID == "":
		return "missing_required_field: package_id (required for FIS card production)"
	}
	return ""
}

func (s *RowProcessingStage) processSRG310Row(ctx context.Context, in *RowProcessingInput, row *srg.SRG310Row) rowOutcome {
	// Pre-insert validation — dead-letter before any DB write if required fields missing.
	if reason := validateSRG310Row(row); reason != "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"validation_failed: "+reason,
			"DATA_GAP",
			row.Raw)
	}

	programID, err := s.lookupProgram(ctx, in.BatchFile.TenantID, row.SubprogramID)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("program_lookup_failed(subprogram=%s): %v", row.SubprogramID, err),
			"DATA_GAP",
			row.Raw)
	}

	existing, err := s.DomainCommands.FindDuplicate(ctx,
		in.BatchFile.TenantID, row.ClientMemberID,
		string(domain.CommandEnroll), row.BenefitPeriod,
	)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("idempotency_check_failed: %v", err),
			"SYSTEM_ERROR",
			row.Raw)
	}
	if existing != nil {
		return outcomeDuplicate
	}

	cmdID := uuid.New()
	now := time.Now().UTC()
	cmd := &ports.DomainCommand{
		ID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, ClientMemberID: row.ClientMemberID,
		CommandType: string(domain.CommandEnroll), BenefitPeriod: row.BenefitPeriod,
		Status: string(domain.CommandAccepted),
		BatchFileID: in.BatchFile.ID, SequenceInFile: row.SequenceInFile,
		CreatedAt: now,
	}
	if err := s.DomainCommands.Insert(ctx, cmd); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("domain_command_insert_failed: %v", err),
			"PROCESSING_ERROR",
			row.Raw)
	}

	rawJSON, _ := json.Marshal(row.Raw)
	var subprogramID *int64
	if row.SubprogramID != "" {
		if parsed, parseErr := strconv.ParseInt(row.SubprogramID, 10, 64); parseErr == nil {
			subprogramID = &parsed
		}
	}
	rt30 := &aurora.BatchRecordRT30{
		ID: uuid.New(), BatchFileID: in.BatchFile.ID,
		DomainCommandID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, SequenceInFile: row.SequenceInFile,
		Status: "STAGED", StagedAt: now,
		ClientMemberID: row.ClientMemberID,
		ProgramID:      programID,
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
			"PROCESSING_ERROR",
			row.Raw)
	}

	consumer := &domain.Consumer{
		ID: uuid.New(), TenantID: in.BatchFile.TenantID,
		ClientMemberID: row.ClientMemberID, Status: domain.ConsumerActive,
		FirstName: row.FirstName, LastName: row.LastName, DOB: row.DOB,
		Address1: row.Address1, Address2: &row.Address2,
		City: row.City, State: row.State, ZIP: row.ZIP, Email: &row.Email,
		ProgramID: programID,
		SubprogramID: func() int64 { if subprogramID != nil { return *subprogramID }; return 0 }(),
		ContractPBP: &row.ContractPBP, CustomCardID: &row.CustomCardID,
		SourceBatchFileID: in.BatchFile.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.DomainState.UpsertConsumer(ctx, consumer); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("consumer_upsert_failed: %v", err),
			"SYSTEM_ERROR",
			row.Raw)
	}

	card := &domain.Card{
		ID: uuid.New(), TenantID: in.BatchFile.TenantID,
		ConsumerID: consumer.ID, ClientMemberID: row.ClientMemberID,
		Status: domain.CardReady, CardDesignID: &row.CardDesignID,
		PackageID: &row.PackageID, SourceBatchFileID: in.BatchFile.ID,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.DomainState.InsertCard(ctx, card); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("card_insert_failed: %v", err),
			"SYSTEM_ERROR",
			row.Raw)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID: in.BatchFile.TenantID, EntityType: "consumers",
		EntityID: consumer.ID.String(), NewState: "ACTIVE",
		ChangedBy: "ingest-task:stage3", CorrelationID: &in.BatchFile.CorrelationID,
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
			"DATA_GAP",
			row.Raw)
	}

	existing, err := s.DomainCommands.FindDuplicate(ctx,
		in.BatchFile.TenantID, row.ClientMemberID,
		commandType, row.BenefitPeriod,
	)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("idempotency_check_failed: %v", err), "SYSTEM_ERROR", row.Raw)
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
			fmt.Sprintf("domain_command_insert_failed: %v", err), "PROCESSING_ERROR", row.Raw)
	}

	consumer, err := s.DomainState.GetConsumerByNaturalKey(ctx, in.BatchFile.TenantID, row.ClientMemberID)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"consumer_not_found: RT37 requires prior RT30 completion", "DATA_GAP", row.Raw)
	}
	rt37Card, err := s.DomainState.GetCardByConsumerID(ctx, consumer.ID)
	if err != nil || fisCardIDOrEmpty(rt37Card) == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"rt37_missing_fis_card_id: RT30 not yet reconciled (Stage 7 pending)", "DATA_GAP", row.Raw)
	}

	rawJSON, _ := json.Marshal(row.Raw)
	if err := s.BatchRecords.InsertRT37(ctx, &aurora.BatchRecordRT37{
		ID: uuid.New(), BatchFileID: in.BatchFile.ID,
		DomainCommandID: cmdID, CorrelationID: in.BatchFile.CorrelationID,
		TenantID: in.BatchFile.TenantID, SequenceInFile: row.SequenceInFile,
		Status: "STAGED", StagedAt: now,
		ClientMemberID: row.ClientMemberID,
		FISCardID:      fisCardIDOrEmpty(rt37Card),
		CardStatusCode: commandTypeToCardStatus(commandType),
		RawPayload:     rawJSON,
	}); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("rt37_insert_failed: %v", err), "PROCESSING_ERROR", row.Raw)
	}

	return outcomeStagedOK
}

func (s *RowProcessingStage) processSRG320Row(ctx context.Context, in *RowProcessingInput, row *srg.SRG320Row) rowOutcome {
	commandType := srg320CommandTypeToCommand(row.CommandType)
	if commandType == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("unknown_srg320_command_type: %q", row.CommandType), "DATA_GAP", row.Raw)
	}

	existing, err := s.DomainCommands.FindDuplicate(ctx,
		in.BatchFile.TenantID, row.ClientMemberID,
		commandType, row.BenefitPeriod,
	)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("idempotency_check_failed: %v", err), "SYSTEM_ERROR", row.Raw)
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
			fmt.Sprintf("domain_command_insert_failed: %v", err), "PROCESSING_ERROR", row.Raw)
	}

	consumer, err := s.DomainState.GetConsumerByNaturalKey(ctx, in.BatchFile.TenantID, row.ClientMemberID)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"consumer_not_found: RT60 requires prior RT30 completion", "DATA_GAP", row.Raw)
	}
	rt60Card, err := s.DomainState.GetCardByConsumerID(ctx, consumer.ID)
	if err != nil || fisCardIDOrEmpty(rt60Card) == "" {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			"rt60_missing_fis_card_id: RT30 not yet reconciled (Stage 7 pending)", "DATA_GAP", row.Raw)
	}

	atCode := "AT01"
	if commandType == string(domain.CommandSweep) {
		atCode = "AT30"
	}

	rawJSON, _ := json.Marshal(row.Raw)
	expiryDate := row.ExpiryDate
	if err := s.BatchRecords.InsertRT60(ctx, &aurora.BatchRecordRT60{
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
	}); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("rt60_insert_failed: %v", err), "PROCESSING_ERROR", row.Raw)
	}

	purse, err := s.DomainState.GetPurseByConsumerAndBenefitPeriod(ctx, consumer.ID, row.BenefitPeriod)
	if err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("purse_lookup_failed: %v", err), "DATA_GAP", row.Raw)
	}

	var newBalance int64
	if commandType == string(domain.CommandSweep) {
		newBalance = 0
	} else {
		newBalance = purse.AvailableBalanceCents + row.AmountCents
	}

	if err := s.DomainState.UpdatePurseBalance(ctx, purse.ID, newBalance); err != nil {
		return s.deadLetter(ctx, in, row.SequenceInFile, row.ClientMemberID,
			string(domain.FailureRowProcessing),
			fmt.Sprintf("purse_balance_update_failed: %v", err), "SYSTEM_ERROR", row.Raw)
	}

	return outcomeStagedOK
}

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

// deadLetter writes a failed row to dead_letter_store.
// failureCategory must be one of: DATA_GAP | DUPLICATE | PROCESSING_ERROR | SYSTEM_ERROR
func (s *RowProcessingStage) deadLetter(
	ctx context.Context,
	in *RowProcessingInput,
	seq int,
	clientMemberID string,
	failureStage string,
	failureReason string,
	failureCategory string,
	rawRecord map[string]string,
) rowOutcome {
	msgBody, _ := json.Marshal(rawRecord)
	batchFileID := in.BatchFile.ID
	_ = s.DeadLetters.Write(ctx, &ports.DeadLetterEntry{
		ID: uuid.New(), CorrelationID: in.BatchFile.CorrelationID,
		BatchFileID: &batchFileID, RowSequenceNumber: &seq,
		TenantID: in.BatchFile.TenantID, ClientMemberID: &clientMemberID,
		FailureStage: failureStage, FailureReason: failureReason,
		MessageBody: msgBody, RetryCount: 0, CreatedAt: time.Now().UTC(),
	})

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:         observability.EventDeadLetterWritten,
		Level:             "WARN",
		CorrelationID:     in.BatchFile.CorrelationID,
		TenantID:          in.BatchFile.TenantID,
		BatchFileID:       in.BatchFile.ID,
		RowSequenceNumber: &seq,
		Stage:             strPtr(failureStage),
		FailureCategory:   &failureCategory,
		Error:             strPtr(failureReason),
		Message:           fmt.Sprintf("dead_letter: seq=%d reason=%s", seq, failureReason),
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
		return 2
	case string(domain.CommandSuspend):
		return 6
	case string(domain.CommandTerminate):
		return 7
	default:
		return 6
	}
}

func fisCardIDOrEmpty(c *domain.Card) string {
	if c == nil || c.FISCardID == nil {
		return ""
	}
	return *c.FISCardID
}

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
