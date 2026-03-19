package fis_adapter

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/walker-morse/batch/_shared/ports"
)

// AssemblerImpl implements ports.FISBatchAssembler.
// This is the only place in the codebase that knows FIS record format details (ADR-001).
type AssemblerImpl struct {
	CompanyIDExtended string // Morse LLC FIS Level 1 client identifier
	sequenceStore     SequenceStore
	records           ports.BatchRecordsLister
}

// NewAssembler creates a configured FIS batch file assembler.
func NewAssembler(companyIDExtended string, seqStore SequenceStore, records ports.BatchRecordsLister) *AssemblerImpl {
	return &AssemblerImpl{
		CompanyIDExtended: companyIDExtended,
		sequenceStore:     seqStore,
		records:           records,
	}
}

// AssembleFile produces a complete FIS batch file from staged batch records.
//
// File structure (§6.6.2):
//
//	RT10 (File Header)     — Log File Indicator MUST be '0'; written first
//	RT20 (Batch Header)    — one per client per file (Phase 1: always one)
//	RT30/37/60 data records — 400 bytes each, CRLF-separated
//	RT80 (Batch Trailer)   — exact detail record count
//	RT90 (File Trailer)    — total, batch, and detail counts; written LAST
//
// The assembler enforces:
//   - Log File Indicator = '0' hardcoded (§6.6.3)
//   - Test/Prod Indicator from request (§6.6.4)
//   - RT90 written after all data records with exact counts
//   - Filename follows FIS convention with incrementing sequence number (§6.6.1)
func (a *AssemblerImpl) AssembleFile(ctx context.Context, req *ports.AssembleRequest) (*ports.AssembledFile, error) {
	now := time.Now().UTC()

	// Get the next sequence number for today (§6.6.1)
	// Prevents duplicate filename silent suppression (§6.5.5)
	// ProgramID comes from the first staged RT30 row — resolved in Stage 3.
	if req.ProgramID == (uuid.UUID{}) {
		return nil, fmt.Errorf("assembler: ProgramID is nil UUID — Stage 3 must resolve programs.id before assembly")
	}
	seq, err := a.sequenceStore.Next(ctx, req.ProgramID.String(), now)
	if err != nil {
		return nil, fmt.Errorf("assembler: get sequence number: %w", err)
	}

	filename := BuildFilename(a.CompanyIDExtended[:8], now, seq, "issuance")

	// Build RT10 File Header
	rt10, err := BuildRT10(RT10Fields{
		CompanyIDExtended: a.CompanyIDExtended,
		FileCreationDate:  now,
		LogFileIndicator:  LogFileIndicatorAll, // hardcoded '0' per §6.6.3
		TestProdIndicator: req.TestProdIndicator,
	})
	if err != nil {
		return nil, fmt.Errorf("assembler: build RT10: %w", err)
	}

	// Query all STAGED records for this correlation ID
	staged, err := a.records.ListStagedByCorrelationID(ctx, req.CorrelationID)
	if err != nil {
		return nil, fmt.Errorf("assembler: list staged records: %w", err)
	}

	// Derive SubprogramID for RT20 from the first RT30 record.
	// Phase 1: all rows in a file share a single subprogram.
	// If no RT30 records exist, fall back to empty string — structurally valid file.
	subprogramID := ""
	if len(staged.RT30) > 0 && staged.RT30[0].SubprogramID != nil {
		subprogramID = strconv.FormatInt(*staged.RT30[0].SubprogramID, 10)
	}

	rt20 := BuildRT20(RT20Fields{
		ClientID:     req.TenantID,
		SubprogramID: subprogramID,
	})

	// Build data records in sequence_in_file order.
	// All three slices are already sorted by sequence_in_file ASC from the query.
	var dataRecords [][]byte

	for _, rec := range staged.RT30 {
		fields := RT30Fields{
			ClientMemberID: rec.ClientMemberID,
		}
		if rec.SubprogramID != nil {
			fields.SubprogramID = strconv.FormatInt(*rec.SubprogramID, 10)
		}
		if rec.PackageID != nil {
			fields.PackageID = *rec.PackageID
		}
		if rec.FirstName != nil {
			fields.FirstName = *rec.FirstName
		}
		if rec.LastName != nil {
			fields.LastName = *rec.LastName
		}
		if rec.DateOfBirth != nil {
			fields.DOB = *rec.DateOfBirth
		}
		if rec.Address1 != nil {
			fields.Address1 = *rec.Address1
		}
		if rec.Address2 != nil {
			fields.Address2 = *rec.Address2
		}
		if rec.City != nil {
			fields.City = *rec.City
		}
		if rec.State != nil {
			fields.State = *rec.State
		}
		if rec.ZIP != nil {
			fields.ZIP = *rec.ZIP
		}
		if rec.Email != nil {
			fields.Email = *rec.Email
		}
		if rec.CardDesignID != nil {
			fields.CardDesignID = *rec.CardDesignID
		}
		if rec.CustomCardID != nil {
			fields.CustomCardID = *rec.CustomCardID
		}

		built, err := BuildRT30(fields)
		if err != nil {
			return nil, fmt.Errorf("assembler: build RT30 (seq=%d member=%s): %w",
				rec.SequenceInFile, rec.ClientMemberID, err)
		}
		dataRecords = append(dataRecords, built)
	}

	for _, rec := range staged.RT37 {
		reasonCode := ""
		if rec.ReasonCode != nil {
			reasonCode = *rec.ReasonCode
		}
		built, err := BuildRT37(RT37Fields{
			ClientMemberID: rec.ClientMemberID,
			FISCardID:      rec.FISCardID,
			CardStatusCode: strconv.Itoa(int(rec.CardStatusCode)),
			ReasonCode:     reasonCode,
		})
		if err != nil {
			return nil, fmt.Errorf("assembler: build RT37 (seq=%d member=%s): %w",
				rec.SequenceInFile, rec.ClientMemberID, err)
		}
		dataRecords = append(dataRecords, built)
	}

	for _, rec := range staged.RT60 {
		atCode := ""
		if rec.ATCode != nil {
			atCode = *rec.ATCode
		}
		purseName := ""
		if rec.PurseName != nil {
			purseName = *rec.PurseName
		}
		clientRef := ""
		if rec.ClientReference != nil {
			clientRef = *rec.ClientReference
		}
		built, err := BuildRT60(RT60Fields{
			ATCode:          atCode,
			ClientMemberID:  rec.ClientMemberID,
			FISCardID:       rec.FISCardID,
			PurseName:       purseName,
			AmountCents:     rec.AmountCents,
			EffectiveDate:   rec.EffectiveDate,
			ClientReference: clientRef,
		})
		if err != nil {
			return nil, fmt.Errorf("assembler: build RT60 (seq=%d member=%s): %w",
				rec.SequenceInFile, rec.ClientMemberID, err)
		}
		dataRecords = append(dataRecords, built)
	}

	detailCount := len(dataRecords)

	// RT80 Batch Trailer — must match exact detail record count
	rt80 := BuildRT80(detailCount)

	// RT90 File Trailer — written LAST with all counts exact (§6.6.2)
	// totalRecords = RT10 + RT20 + data records + RT80 + RT90
	totalRecords := 1 + 1 + detailCount + 1 + 1
	rt90 := BuildRT90(totalRecords, 1, detailCount)

	// Assemble into a single buffer with CRLF separators
	var buf bytes.Buffer
	writeRecord := func(rec []byte) {
		buf.Write(rec)
		buf.WriteString(CRLF)
	}

	writeRecord(rt10)
	writeRecord(rt20)
	for _, rec := range dataRecords {
		writeRecord(rec)
	}
	writeRecord(rt80)
	writeRecord(rt90) // LAST — §6.6.2

	assembled := buf.Bytes()

	return &ports.AssembledFile{
		Filename:    filename,
		RecordCount: totalRecords,
		Body:        newReadCloser(assembled),
	}, nil
}

// readCloser wraps a byte slice as an io.ReadCloser.
type readCloser struct {
	*bytes.Reader
}

func newReadCloser(b []byte) *readCloser {
	return &readCloser{bytes.NewReader(b)}
}

func (r *readCloser) Close() error { return nil }
