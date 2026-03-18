package fis_adapter

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/walker-morse/batch/_shared/ports"
)

// AssemblerImpl implements ports.FISBatchAssembler.
// This is the only place in the codebase that knows FIS record format details (ADR-001).
type AssemblerImpl struct {
	CompanyIDExtended string // Morse LLC FIS Level 1 client identifier
	sequenceStore     SequenceStore
}

// NewAssembler creates a configured FIS batch file assembler.
func NewAssembler(companyIDExtended string, seqStore SequenceStore) *AssemblerImpl {
	return &AssemblerImpl{
		CompanyIDExtended: companyIDExtended,
		sequenceStore:     seqStore,
	}
}

// AssembleFile produces a complete FIS batch file from staged batch records.
//
// File structure (§6.6.2):
//   RT10 (File Header)     — Log File Indicator MUST be '0'; written first
//   RT20 (Batch Header)    — one per client per file (Phase 1: always one)
//   RT30/37/60 data records — 400 bytes each, CRLF-separated
//   RT80 (Batch Trailer)   — exact detail record count
//   RT90 (File Trailer)    — total, batch, and detail counts; written LAST
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
	seq, err := a.sequenceStore.Next(ctx, a.CompanyIDExtended, now)
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

	// Phase 1: one client per file — placeholder SubprogramID from request context
	// TODO: fetch SubprogramID and ClientID from programs table using req.TenantID
	rt20 := BuildRT20(RT20Fields{
		ClientID:     req.TenantID,
		SubprogramID: "26071", // RFU Phase 1 — must come from programs table
	})

	// TODO: query batch_records_rt30/rt37/rt60 WHERE correlation_id = req.CorrelationID AND status = STAGED
	// For now, build an empty but structurally valid file to establish the format
	var dataRecords [][]byte
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
