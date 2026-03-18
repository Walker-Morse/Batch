// Package fis_adapter implements the FIS Prepaid Sunrise adapter seam.
//
// This package is the ONLY place in the codebase that knows about:
//   - FIS 400-byte fixed-width record format
//   - Field byte offsets and padding rules
//   - AT code values (AT01=load, AT30=sweep/expiry)
//   - RT10 / RT20 / RT80 / RT90 wrapper structure
//   - File naming convention (ppppppppmmddccyyss.*.txt)
//   - Log File Indicator semantics
//   - Test/Prod Indicator semantics
//
// Per ADR-001: domain logic and pipeline stages consume the ports.FISBatchAssembler
// interface only. This separation is what makes future hexagonal formalisation
// possible without a domain rewrite.
//
// Authoritative source: FIS Generic Batch Loader Reference Guide v37 and
// FIS File Processing Specification v1.37.
//
// All character fields must be ASCII-transliterated before submission (§B.4).
// No accented characters, no Unicode. Non-ASCII demographic fields require
// confirmed transliteration strategy before Stage 3 implementation (Open Item #9).
package fis_adapter

import (
	"context"
	"fmt"
	"time"

	"github.com/walker-morse/batch/shared/ports"
)

const (
	// RecordWidth is the mandatory FIS fixed-width record size in bytes.
	RecordWidth = 400
	// CRLF is the mandatory record separator per §6.6.2.
	CRLF = "\r\n"
	// LogFileIndicatorAll MUST be hardcoded to '0'.
	// Per §6.6.3: returns all records (successes and failures).
	// Required for Stage 7 to capture FIS-assigned card IDs and purse numbers.
	// NOT overridable at runtime under any circumstances.
	LogFileIndicatorAll byte = '0'
)

// AT codes — all FIS fund operation qualifiers live here (§4.1.4 notes).
const (
	ATCodeLoad  = "AT01" // benefit load to existing purse
	ATCodeSweep = "AT30" // period-end purse expiry / zero-balance close
)

// RT record types (§6.6.2).
const (
	RTFileHeader  = "RT10" // first record in every file
	RTBatchHeader = "RT20" // opens each client batch
	RTNewAccount  = "RT30" // new member enrollment + card issuance
	RTCardUpdate  = "RT37" // card status update (suspend, unsuspend, close)
	RTFundLoad    = "RT60" // purse load or period-end sweep
	RTAccountSettings = "RT62"
	RTPreProcessingHalt = "RT99" // FIS file-level rejection OR individual record error
	RTBatchTrailer = "RT80" // closes each RT20 batch — exact count required
	RTFileTrailer  = "RT90" // last record in every file — total/batch/detail counts required
)

// Assembler implements ports.FISBatchAssembler.
type Assembler struct {
	// programID is the 8-character FIS program identifier for file naming.
	// Open Item #31: obtain from Selvi Marappan before Batch Assembly sprint (Apr 27).
	programID string
	// sequenceStore persists the per-day per-program-ID file sequence counter.
	// Must survive container restarts — stored in batch_files table (§6.6.1).
	sequenceStore SequenceStore
}

// SequenceStore persists the per-day file sequence number (§6.6.1).
type SequenceStore interface {
	Next(ctx context.Context, programID string, date time.Time) (int, error)
}

// AssembleFile produces a complete FIS batch file.
// Structure: RT10 → RT20 → [data records] → RT80 → RT90.
// RT90 is written LAST after all data records are confirmed.
// A count mismatch in RT80 or RT90 causes an RT99 pre-processing halt (§6.5.1).
//
// File splitting is applied if row count exceeds 100,000 (§5.2a, Open Item #28).
// Split files each receive the next sequence number in the series.
func (a *Assembler) AssembleFile(ctx context.Context, req *ports.AssembleRequest) (*ports.AssembledFile, error) {
	_ = req
	return nil, fmt.Errorf("not implemented — see §5.3 Batch Assembly sprint (Apr 27 target)")
}

// BuildFilename constructs the FIS-prescribed filename per §6.6.1.
// Format: ppppppppmmddccyyss.*.txt
// The sequence number increments per file per calendar day; resets at midnight.
// When a large file is split, each split file receives the next sequence number.
func BuildFilename(programID string, date time.Time, seq int, fileKind string) string {
	return fmt.Sprintf("%s%s%02d.%s.txt",
		programID,
		date.Format("01022006"),
		seq,
		fileKind,
	)
}

// IsRT99Halt detects a full-file pre-processing halt from FIS (§6.5.1).
// Returns true if the return file contains exactly one record and it is RT99.
// This is DISTINCT from individual record errors (also RT99 but not the only record).
// Full-file halt: no member records were processed. All members must be dead-lettered.
func IsRT99Halt(returnFileRecordCount int, firstRecordType string) bool {
	return returnFileRecordCount == 1 && firstRecordType == RTPreProcessingHalt
}
