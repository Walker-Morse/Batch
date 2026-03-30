// Package parser implements the core XTRACT file reader.
//
// All six FIS Data XTRACT feeds share the same file structure:
//   H|<header fields>           — exactly one header record
//   D|<detail fields>...        — zero or more detail records
//   T|<record_count>            — exactly one trailer record
//
// The parser:
//   1. Streams the file line-by-line (never loads fully into memory)
//   2. Computes SHA-256 over raw bytes for non-repudiation
//   3. Validates H record feed name and T record count
//   4. Emits parsed detail rows to a callback for feed-specific processing
//
// Non-repudiation contract (§4.3.20):
//   - The raw file is stored immutably in S3 (versioned, no DeleteObject on xtract bucket)
//   - SHA-256 is recorded in xtract_file_log BEFORE any detail rows are processed
//   - If the loader crashes mid-file, the log row shows status=PROCESSING and the file
//     is still intact in S3 — replay is safe and idempotent via ON CONFLICT DO NOTHING
//     on all fact table inserts.
package parser

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// XtractType identifies which feed a file belongs to.
type XtractType string

const (
	TypeSTDMON    XtractType = "STDMON"
	TypeSTDACCTBAL XtractType = "STDACCTBAL"
	TypeSTDNONMON XtractType = "STDNONMON"
	TypeSTDFSID   XtractType = "STDFSID"
	TypeSTDCBDISP XtractType = "STDCBDISP"
	TypeCCX       XtractType = "CCX"
	TypeSTDHIER   XtractType = "CLIENTHIERARCHY"
	TypeSTDAUTH   XtractType = "STDAUTH"
)

// FileHeader holds the parsed H record fields common to all feeds.
type FileHeader struct {
	ProcessorName  string    // always "FIS PREPAID"
	FeedName       string    // e.g. "STDFISMON", "STDFISAUTH"
	FileDate       time.Time // transmission date MMDDYYYY
	WorkOfDate     time.Time // reporting date MMDDYYYY (field 5 for most feeds)
	RawFields      []string  // all raw header fields for feed-specific access
}

// ParseResult is returned after a complete file parse.
type ParseResult struct {
	Header          FileHeader
	DetailCount     int    // number of D records processed
	TrailerCount    int    // count from T record
	SHA256          string // hex SHA-256 of raw file bytes
	CountMismatch   bool   // true if DetailCount != TrailerCount
	SourceFileKey   string // S3 key of the source file
}

// DetailRowFunc is called once per detail record.
// fields[0] is the record type indicator (D, R, etc).
// Return an error to abort processing — the error is wrapped and returned from Parse.
// A non-fatal row error should be logged by the callback and return nil.
type DetailRowFunc func(ctx context.Context, lineNum int, fields []string) error

// Parse streams an XTRACT file from r, validates structure, computes SHA-256,
// and calls rowFn for each detail record. The source S3 key is recorded for logging.
//
// feedName must match the value in H record field 3 exactly (e.g. "STDFISMON").
// Pass "" to skip feed name validation (use during initial development).
func Parse(ctx context.Context, r io.Reader, s3Key string, feedName string, rowFn DetailRowFunc) (*ParseResult, error) {
	h := sha256.New()
	tee := io.TeeReader(r, h)

	scanner := bufio.NewScanner(tee)
	// STDFSID files can be large (36K+ lines, 9.9MB). Default 64KB buf is fine
	// for pipe-delimited lines but bump to 1MB to handle any wide optional fields.
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, len(buf))

	result := &ParseResult{SourceFileKey: s3Key}
	lineNum := 0
	headerSeen := false
	trailerSeen := false

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		lineNum++
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}

		fields := strings.Split(line, "|")
		if len(fields) == 0 {
			continue
		}

		recType := fields[0]

		switch recType {
		case "H":
			if headerSeen {
				return nil, fmt.Errorf("parser: duplicate H record at line %d in %s", lineNum, s3Key)
			}
			hdr, err := parseHeader(fields)
			if err != nil {
				return nil, fmt.Errorf("parser: invalid H record at line %d in %s: %w", lineNum, s3Key, err)
			}
			if feedName != "" && hdr.FeedName != feedName {
				return nil, fmt.Errorf("parser: expected feed %q got %q in %s", feedName, hdr.FeedName, s3Key)
			}
			result.Header = hdr
			headerSeen = true

		case "T":
			if !headerSeen {
				return nil, fmt.Errorf("parser: T record before H record at line %d in %s", lineNum, s3Key)
			}
			if len(fields) < 2 {
				return nil, fmt.Errorf("parser: malformed T record at line %d in %s", lineNum, s3Key)
			}
			n, err := strconv.Atoi(strings.TrimSpace(fields[1]))
			if err != nil {
				return nil, fmt.Errorf("parser: T record count not numeric at line %d in %s: %w", lineNum, s3Key, err)
			}
			result.TrailerCount = n
			trailerSeen = true
			// T record is always last — stop scanning
			break

		default:
			// D record, R record (STDFSID transaction header), or other detail type
			if !headerSeen {
				return nil, fmt.Errorf("parser: detail record before H record at line %d in %s", lineNum, s3Key)
			}
			if trailerSeen {
				// data after trailer — ignore (some FIS files have trailing newlines)
				continue
			}
			if err := rowFn(ctx, lineNum, fields); err != nil {
				return nil, fmt.Errorf("parser: row callback error at line %d in %s: %w", lineNum, s3Key, err)
			}
			result.DetailCount++
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parser: scan error in %s: %w", s3Key, err)
	}
	if !headerSeen {
		return nil, fmt.Errorf("parser: no H record found in %s", s3Key)
	}
	if !trailerSeen {
		return nil, fmt.Errorf("parser: no T record found in %s (file truncated?)", s3Key)
	}

	// Drain tee so SHA-256 covers the full file including T record
	result.SHA256 = hex.EncodeToString(h.Sum(nil))
	result.CountMismatch = result.DetailCount != result.TrailerCount

	return result, nil
}

// parseHeader extracts common fields from the H record.
//
// Standard feeds (5+ fields):
//   H|ProcessorName|FeedName|FileDate|WorkOfDate|...
//
// STDFSID variant (8 fields — has ClientID and ClientName between FeedName and FileDate):
//   H|FIS PREPAID|FILTEREDSPEND|ClientID|ClientName|BIN|FileDate|WorkOfDate|
func parseHeader(fields []string) (FileHeader, error) {
	if len(fields) < 5 {
		return FileHeader{}, fmt.Errorf("H record has %d fields, expected ≥5", len(fields))
	}
	hdr := FileHeader{
		ProcessorName: strings.TrimSpace(fields[1]),
		FeedName:      strings.TrimSpace(fields[2]),
		RawFields:     fields,
	}

	// STDFSID has ClientID and ClientName between FeedName and FileDate.
	// Detect by checking if fields[3] is numeric (ClientID) vs 8-digit date.
	fileDateIdx := 3
	workDateIdx := 4
	if len(fields) >= 8 && len(strings.TrimSpace(fields[3])) <= 7 {
		// fields[3] is a short numeric ClientID, not an 8-char date
		fileDateIdx = 6
		workDateIdx = 7
	}

	var err error
	hdr.FileDate, err = parseMMDDYYYY(fields[fileDateIdx])
	if err != nil {
		return FileHeader{}, fmt.Errorf("FileDate fields[%d]=%q: %w", fileDateIdx, fields[fileDateIdx], err)
	}
	if workDateIdx < len(fields) {
		hdr.WorkOfDate, _ = parseMMDDYYYY(fields[workDateIdx])
	}
	return hdr, nil
}

// ─── Utility functions used by feed parsers ────────────────────────────────

// Field returns fields[i] trimmed, or "" if out of range or "Null".
func Field(fields []string, i int) string {
	if i >= len(fields) {
		return ""
	}
	v := strings.TrimSpace(fields[i])
	if strings.EqualFold(v, "null") || v == "" {
		return ""
	}
	return v
}

// FieldInt64 returns fields[i] as int64, or 0 on parse error / missing / Null.
func FieldInt64(fields []string, i int) int64 {
	v := Field(fields, i)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// FieldFloat64 returns fields[i] as float64 (e.g. "100.0000"), or 0 on error.
func FieldFloat64(fields []string, i int) float64 {
	v := Field(fields, i)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

// FieldInt64Cents converts a 4-decimal FIS amount field to integer cents.
// "100.0000" → 10000. Rounds to nearest cent.
func FieldInt64Cents(fields []string, i int) int64 {
	f := FieldFloat64(fields, i)
	return int64(f * 100)
}

// FieldDate parses MMDDYYYY → time.Time, returns zero on error or empty.
func FieldDate(fields []string, i int) time.Time {
	v := Field(fields, i)
	if v == "" {
		return time.Time{}
	}
	t, err := parseMMDDYYYY(v)
	if err != nil {
		return time.Time{}
	}
	return t
}

// FieldDateTime parses "MMDDYYYY HH:MM:SS" → time.Time UTC.
func FieldDateTime(fields []string, i int) time.Time {
	v := Field(fields, i)
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse("01022006 15:04:05", v)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseMMDDYYYY(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if len(s) != 8 {
		return time.Time{}, fmt.Errorf("expected 8-char MMDDYYYY, got %q", s)
	}
	return time.Parse("01022006", s)
}

// DateSK converts a time.Time to YYYYMMDD integer surrogate key for dim_date.
func DateSK(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	return t.Year()*10000 + int(t.Month())*100 + t.Day()
}
