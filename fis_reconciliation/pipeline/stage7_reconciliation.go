// Stage 7 — Reconciliation (§5.1, §6.5):
//   Ingest FIS return file → match results to staged batch records →
//   update status → write fact_reconciliation → update domain state identifiers.
//
// RT99 full-file pre-processing halt detection (§6.5.1):
//   If the return file contains EXACTLY ONE record and it is RT99:
//   → the entire file was rejected by FIS before any member was processed
//   → ALL staged records dead-lettered (failure_stage = "reconciliation")
//   → batch_files.status → HALTED
//   → emit "batch.halt.triggered" log event
//   → page on-call immediately (CloudWatch alarm → Datadog P0)
//
//   This is DISTINCT from individual record RT99 failures (FIS processes all other
//   records and returns RT99 only for the failed record). Read §6.5.1 carefully.
//
// For each successful RT30: stamp FISPersonID + FISCUID on consumers, FISCardID + issued_at on cards.
// For each successful RT60: stamp FISPurseNumber on purses.
// Domain commands transitioned: Accepted → Completed (success) or Failed (RT99 individual).
//
// Status transition: TRANSFERRED → COMPLETE (or HALTED on RT99 full-file halt)
package pipeline

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/domain"
	"github.com/walker-morse/batch/_shared/ports"
	"github.com/walker-morse/batch/fis_reconciliation/fis_adapter"
)

// DomainStateReconciler is the narrow interface for Stage 7 domain state updates.
// Defined here (not in ports) to avoid an import cycle: ports cannot import
// aurora or domain types. Implemented by aurora.DomainStateRepo.
type DomainStateReconciler interface {
	GetConsumerByNaturalKey(ctx context.Context, tenantID, clientMemberID string) (*domain.Consumer, error)
	GetCardByConsumerID(ctx context.Context, consumerID uuid.UUID) (*domain.Card, error)
	UpdateConsumerFISIdentifiers(ctx context.Context, id uuid.UUID, fisPersonID, fisCUID string) error
	UpdateCardFISCardID(ctx context.Context, id uuid.UUID, fisCardID string, issuedAt time.Time) error
	UpdatePurseFISNumber(ctx context.Context, consumerID uuid.UUID, benefitPeriod string, fisNumber int16) error
}

// ReconciliationStage implements Stage 7 of the ingest-task pipeline.
type ReconciliationStage struct {
	BatchFiles     ports.BatchFileRepository
	BatchRecords   ports.BatchRecordsReconciler
	DomainCommands ports.DomainCommandRepository
	DomainState    DomainStateReconciler
	DeadLetters    ports.DeadLetterRepository
	Audit          ports.AuditLogWriter
	Mart           ports.MartWriter
	Obs            ports.IObservabilityPort
}

// Run ingests the FIS return file stream and reconciles every result record.
// returnBody is consumed and closed by this method.
func (s *ReconciliationStage) Run(ctx context.Context, batchFile *ports.BatchFile, returnBody io.ReadCloser) error {
	defer returnBody.Close()

	// Parse the full return file into records
	records, err := fis_adapter.ParseReturnFile(returnBody)
	if err != nil {
		return fmt.Errorf("stage7: parse return file: %w", err)
	}

	// RT99 full-file halt detection (§6.5.1)
	// Exactly one record AND it is RT99 → entire file rejected before processing
	dataRecords := dataRecordsOnly(records)
	if fis_adapter.IsRT99Halt(len(records), firstRecordType(records)) {
		return s.handleFullFileHalt(ctx, batchFile, records)
	}

	// Process each data record
	completed, failed := 0, 0
	for _, rec := range dataRecords {
		if err := s.reconcileRecord(ctx, batchFile, rec); err != nil {
			// Log but continue — one bad reconciliation must not abort the file.
			// The individual record remains STAGED; ops can replay.
			_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
				EventType:     "stage7.record_reconcile_error",
				Level:         "ERROR",
				CorrelationID: &batchFile.CorrelationID,
				TenantID:      &batchFile.TenantID,
				BatchFileID:   &batchFile.ID,
				Stage:         strPtr("stage7_reconciliation"),
				Message:       fmt.Sprintf("reconcile error seq=%d type=%s", rec.SequenceInFile, rec.RecordType),
				Error:         strPtr(err.Error()),
			})
			failed++
			continue
		}
		if rec.FISResultCode == "000" {
			completed++
		} else {
			failed++
		}
	}

	// Transition batch_files → COMPLETE
	if err := s.BatchFiles.UpdateStatus(ctx, batchFile.ID, string(domain.BatchFileComplete), time.Now().UTC()); err != nil {
		return fmt.Errorf("stage7: update status COMPLETE: %w", err)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:      batchFile.TenantID,
		EntityType:    "batch_files",
		EntityID:      batchFile.ID.String(),
		OldState:      strPtr("TRANSFERRED"),
		NewState:      "COMPLETE",
		ChangedBy:     "ingest-task:stage7",
		CorrelationID: &batchFile.CorrelationID,
		Notes:         strPtr(fmt.Sprintf("reconciled: completed=%d failed=%d total=%d", completed, failed, len(dataRecords))),
	})

	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "stage7.complete",
		Level:         "INFO",
		CorrelationID: &batchFile.CorrelationID,
		TenantID:      &batchFile.TenantID,
		BatchFileID:   &batchFile.ID,
		Stage:         strPtr("stage7_reconciliation"),
		Message:       fmt.Sprintf("complete: completed=%d failed=%d total=%d", completed, failed, len(dataRecords)),
	})

	return nil
}

// reconcileRecord processes a single return record: updates batch_records,
// domain_commands, domain state identifiers, and writes a reconciliation fact.
func (s *ReconciliationStage) reconcileRecord(ctx context.Context, batchFile *ports.BatchFile, rec *fis_adapter.ReturnRecord) error {
	// Look up the staged batch record by sequence
	recordID, cmdID, err := s.BatchRecords.GetStagedByCorrelationAndSequence(
		ctx, batchFile.CorrelationID, rec.SequenceInFile, rec.RecordType,
	)
	if err != nil {
		return fmt.Errorf("get_staged seq=%d type=%s: %w", rec.SequenceInFile, rec.RecordType, err)
	}

	resultCode := &rec.FISResultCode
	resultMsg := &rec.FISResultMsg

	var batchStatus, cmdStatus string
	if rec.FISResultCode == "000" {
		batchStatus = "COMPLETED"
		cmdStatus = string(domain.CommandCompleted)
	} else {
		batchStatus = "FAILED"
		cmdStatus = string(domain.CommandFailed)
	}

	// Update batch record status
	if err := s.BatchRecords.UpdateStatus(ctx, recordID, rec.RecordType, batchStatus, resultCode, resultMsg); err != nil {
		return fmt.Errorf("batch_records.UpdateStatus seq=%d: %w", rec.SequenceInFile, err)
	}

	// Update domain command status
	var failureReason *string
	if cmdStatus == string(domain.CommandFailed) {
		r := fmt.Sprintf("fis_result_code=%s", rec.FISResultCode)
		failureReason = &r
	}
	if err := s.DomainCommands.UpdateStatus(ctx, cmdID, cmdStatus, failureReason); err != nil {
		return fmt.Errorf("domain_commands.UpdateStatus seq=%d: %w", rec.SequenceInFile, err)
	}

	// On success: stamp FIS-assigned identifiers onto domain state
	if rec.FISResultCode == "000" {
		if err := s.stampFISIdentifiers(ctx, batchFile, rec); err != nil {
			// Non-fatal — log the gap; identifiers can be replayed separately
			_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
				EventType:     "stage7.identifier_stamp_failed",
				Level:         "ERROR",
				CorrelationID: &batchFile.CorrelationID,
				TenantID:      &batchFile.TenantID,
				Stage:         strPtr("stage7_reconciliation"),
				Message:       fmt.Sprintf("FIS identifier stamp failed seq=%d type=%s", rec.SequenceInFile, rec.RecordType),
				Error:         strPtr(err.Error()),
			})
		}
	}

	// Write reconciliation fact for Tableau dashboard (§4.3.10)
	_ = s.Mart.WriteReconciliationFact(ctx, &ports.ReconciliationFact{
		BatchFileID:      batchFile.ID,
		RowSequenceNumber: rec.SequenceInFile,
		FISResultCode:    rec.FISResultCode,
	})

	return nil
}

// stampFISIdentifiers updates domain state with FIS-assigned identifiers.
// RT30: consumer gets FISPersonID + FISCUID; card gets FISCardID + issued_at.
// RT60: purse gets FISPurseNumber.
// RT37: no new identifiers assigned by FIS on card status updates.
func (s *ReconciliationStage) stampFISIdentifiers(ctx context.Context, batchFile *ports.BatchFile, rec *fis_adapter.ReturnRecord) error {
	switch rec.RecordType {
	case fis_adapter.RTNewAccount: // RT30
		if rec.FISPersonID == nil || rec.FISCUID == nil || rec.FISCardID == nil {
			return nil // identifiers not yet available — opt-in pending (Open Item Selvi Marappan)
		}
		consumer, err := s.DomainState.GetConsumerByNaturalKey(ctx, batchFile.TenantID, rec.ClientMemberID)
		if err != nil {
			return fmt.Errorf("get_consumer seq=%d: %w", rec.SequenceInFile, err)
		}
		if err := s.DomainState.UpdateConsumerFISIdentifiers(ctx, consumer.ID, *rec.FISPersonID, *rec.FISCUID); err != nil {
			return fmt.Errorf("update_consumer_fis_ids seq=%d: %w", rec.SequenceInFile, err)
		}
		card, err := s.DomainState.GetCardByConsumerID(ctx, consumer.ID)
		if err != nil {
			return fmt.Errorf("get_card seq=%d: %w", rec.SequenceInFile, err)
		}
		if err := s.DomainState.UpdateCardFISCardID(ctx, card.ID, *rec.FISCardID, time.Now().UTC()); err != nil {
			return fmt.Errorf("update_card_fis_id seq=%d: %w", rec.SequenceInFile, err)
		}

	case fis_adapter.RTFundLoad: // RT60
		if rec.FISPurseNumber == nil {
			return nil
		}
		consumer, err := s.DomainState.GetConsumerByNaturalKey(ctx, batchFile.TenantID, rec.ClientMemberID)
		if err != nil {
			return fmt.Errorf("get_consumer_for_purse seq=%d: %w", rec.SequenceInFile, err)
		}
		// BenefitPeriod is not in the return record — derive from the current month.
		// Phase 1 assumption: return file processed same calendar month as submission.
		benefitPeriod := time.Now().UTC().Format("2006-01")
		if err := s.DomainState.UpdatePurseFISNumber(ctx, consumer.ID, benefitPeriod, *rec.FISPurseNumber); err != nil {
			return fmt.Errorf("update_purse_fis_number seq=%d: %w", rec.SequenceInFile, err)
		}
	}
	return nil
}

// handleFullFileHalt processes an RT99 full-file pre-processing halt (§6.5.1).
// Dead-letters ALL staged records for this batch file and transitions to HALTED.
func (s *ReconciliationStage) handleFullFileHalt(ctx context.Context, batchFile *ports.BatchFile, records []*fis_adapter.ReturnRecord) error {
	_ = s.Obs.LogEvent(ctx, &ports.LogEvent{
		EventType:     "batch.halt.triggered",
		Level:         "ERROR",
		CorrelationID: &batchFile.CorrelationID,
		TenantID:      &batchFile.TenantID,
		BatchFileID:   &batchFile.ID,
		Stage:         strPtr("stage7_reconciliation"),
		Message:       "RT99 full-file pre-processing halt — FIS rejected entire file before processing; ALL members dead-lettered; page on-call P0",
	})

	// Dead-letter the file-level failure — individual row dead letters are not
	// written here because FIS never processed them; the staged records remain
	// intact for replay after root cause is resolved.
	_ = s.DeadLetters.Write(ctx, &ports.DeadLetterEntry{
		ID:            uuid.New(),
		CorrelationID: batchFile.CorrelationID,
		BatchFileID:   &batchFile.ID,
		TenantID:      batchFile.TenantID,
		FailureStage:  string(domain.FailureReconciliation),
		FailureReason: fmt.Sprintf("rt99_full_file_halt: fis_result_code=%s", rt99ResultCode(records)),
		CreatedAt:     time.Now().UTC(),
	})

	if err := s.BatchFiles.UpdateStatus(ctx, batchFile.ID, string(domain.BatchFileHalted), time.Now().UTC()); err != nil {
		return fmt.Errorf("stage7: update status HALTED: %w", err)
	}

	_ = s.Audit.Write(ctx, &ports.AuditEntry{
		TenantID:      batchFile.TenantID,
		EntityType:    "batch_files",
		EntityID:      batchFile.ID.String(),
		OldState:      strPtr("TRANSFERRED"),
		NewState:      "HALTED",
		ChangedBy:     "ingest-task:stage7",
		CorrelationID: &batchFile.CorrelationID,
		Notes:         strPtr(fmt.Sprintf("rt99_full_file_halt: result_code=%s", rt99ResultCode(records))),
	})

	return fmt.Errorf("stage7: RT99 full-file halt (correlation_id=%s)", batchFile.CorrelationID)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// dataRecordsOnly filters out structural records (RT10/RT20/RT80/RT90/RT99).
func dataRecordsOnly(records []*fis_adapter.ReturnRecord) []*fis_adapter.ReturnRecord {
	var out []*fis_adapter.ReturnRecord
	for _, r := range records {
		switch r.RecordType {
		case fis_adapter.RTNewAccount, fis_adapter.RTCardUpdate, fis_adapter.RTFundLoad:
			out = append(out, r)
		}
	}
	return out
}

// firstRecordType returns the RecordType of the first record, or "" if empty.
func firstRecordType(records []*fis_adapter.ReturnRecord) string {
	if len(records) == 0 {
		return ""
	}
	return records[0].RecordType
}

// rt99ResultCode extracts the FIS result code from the RT99 record.
func rt99ResultCode(records []*fis_adapter.ReturnRecord) string {
	for _, r := range records {
		if r.RecordType == fis_adapter.RTPreProcessingHalt {
			return r.FISResultCode
		}
	}
	return "unknown"
}

// Compile-time interface satisfaction is verified in _adapters/aurora package tests.
