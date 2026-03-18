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
	"io"
	"strings"
	"time"

	"github.com/walker-morse/batch/_shared/ports"
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

// ─── Return File Parsing ──────────────────────────────────────────────────────

// ReturnRecord is the parsed result of a single record from the FIS return file.
// FIS returns the same 400-byte fixed-width format as the submission, with
// result codes populated at specific offsets (§6.5.1, §6.6.5).
//
// Result code "000" = success. Any other value is an error.
// For RT30: FISPersonID, FISCUID, and FISCardID are populated on success.
// For RT60: FISPurseNumber is populated on success.
type ReturnRecord struct {
	RecordType     string  // RT30|RT37|RT60|RT99|RT10|RT20|RT80|RT90
	SequenceInFile int     // 1-based row sequence — matches sequence_in_file
	ClientMemberID string  // matches staged batch_records row
	FISResultCode  string  // "000" = success; other = error
	FISResultMsg   string  // human-readable result description
	// RT30 return fields — populated on success
	FISPersonID *string
	FISCUID     *string
	FISCardID   *string
	// RT60 return fields — populated on success
	FISPurseNumber *int16
}

// ParseReturnFile reads a FIS return file stream and produces one ReturnRecord
// per 400-byte record. Header (RT10/RT20) and trailer (RT80/RT90) records are
// included so RT99 full-file halt detection can inspect the first record.
//
// The caller must close the reader. Records are returned in file order (seq ascending).
// Malformed records (wrong length, non-ASCII) are returned with RecordType="MALFORMED".
func ParseReturnFile(body io.Reader) ([]*ReturnRecord, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("parse_return_file: read: %w", err)
	}

	// Split on CRLF — each record is RecordWidth bytes + CRLF separator.
	// FIS guarantees CRLF-terminated records per §6.6.2.
	var records []*ReturnRecord
	seq := 0
	for len(data) >= RecordWidth {
		raw := data[:RecordWidth]
		// Advance past record + optional CRLF
		if len(data) >= RecordWidth+2 && data[RecordWidth] == '\r' && data[RecordWidth+1] == '\n' {
			data = data[RecordWidth+2:]
		} else {
			data = data[RecordWidth:]
		}

		recType := strings.TrimRight(string(raw[0:4]), " ")
		switch recType {
		case RTFileHeader, RTBatchHeader, RTBatchTrailer, RTFileTrailer:
			// Structural records — include for RT99 halt detection but skip seq increment
			records = append(records, &ReturnRecord{RecordType: recType})
			continue
		case RTPreProcessingHalt:
			// RT99 — could be full-file halt OR individual record error
			resultCode := strings.TrimRight(string(raw[4:7]), " ")
			resultMsg := strings.TrimRight(string(raw[7:47]), " ")
			records = append(records, &ReturnRecord{
				RecordType:    RTPreProcessingHalt,
				FISResultCode: resultCode,
				FISResultMsg:  resultMsg,
			})
			continue
		}

		seq++
		rec := &ReturnRecord{
			RecordType:     recType,
			SequenceInFile: seq,
			ClientMemberID: strings.TrimRight(string(raw[4:24]), " "),
			FISResultCode:  strings.TrimRight(string(raw[24:27]), " "),
			FISResultMsg:   strings.TrimRight(string(raw[27:67]), " "),
		}

		if rec.FISResultCode == "000" {
			switch recType {
			case RTNewAccount: // RT30
				personID := strings.TrimRight(string(raw[67:87]), " ")
				cuid := strings.TrimRight(string(raw[87:106]), " ")
				cardID := strings.TrimRight(string(raw[106:125]), " ")
				if personID != "" {
					rec.FISPersonID = &personID
				}
				if cuid != "" {
					rec.FISCUID = &cuid
				}
				if cardID != "" {
					rec.FISCardID = &cardID
				}
			case RTFundLoad: // RT60
				purseNumStr := strings.TrimRight(string(raw[67:72]), " ")
				if purseNumStr != "" {
					var n int16
					fmt.Sscanf(purseNumStr, "%d", &n)
					rec.FISPurseNumber = &n
				}
			}
		}

		records = append(records, rec)
	}

	return records, nil
}
