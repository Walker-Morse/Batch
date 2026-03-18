package fis_adapter

import (
	"testing"
	"time"
)

// ─── BuildFilename ────────────────────────────────────────────────────────────
// FIS-prescribed filename format (§6.6.1): ppppppppmmddccyyss.*.txt
// The sequence number prevents duplicate filename silent suppression (§6.5.5).
// Duplicate filenames cause FIS to silently discard the second file with no notification.

func TestBuildFilename_Format(t *testing.T) {
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := BuildFilename("ONEFINX1", date, 1, "issuance")
	want := "ONEFINX106012026" + "01.issuance.txt"
	if got != want {
		t.Errorf("BuildFilename: got %q, want %q", got, want)
	}
}

func TestBuildFilename_SequenceZeroPadded(t *testing.T) {
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	got := BuildFilename("ONEFINX1", date, 3, "disbursement")
	// Sequence must be zero-padded to 2 digits (§6.6.1)
	if got[16:18] != "03" {
		t.Errorf("sequence %q is not zero-padded to 2 digits", got[16:18])
	}
}

func TestBuildFilename_SequenceIncrements(t *testing.T) {
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	first := BuildFilename("ONEFINX1", date, 1, "issuance")
	second := BuildFilename("ONEFINX1", date, 2, "issuance")
	if first == second {
		t.Error("sequential files on same day must have different names to prevent silent suppression (§6.5.5)")
	}
}

func TestBuildFilename_DifferentDatesProduceDifferentNames(t *testing.T) {
	d1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	if BuildFilename("ONEFINX1", d1, 1, "issuance") == BuildFilename("ONEFINX1", d2, 1, "issuance") {
		t.Error("different dates must produce different filenames")
	}
}

func TestBuildFilename_AllFileKinds(t *testing.T) {
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	kinds := []string{"issuance", "disbursement", "maintenance", "all"}
	seen := map[string]bool{}
	for _, kind := range kinds {
		name := BuildFilename("ONEFINX1", date, 1, kind)
		if seen[name] {
			t.Errorf("duplicate filename for kind %q", kind)
		}
		seen[name] = true
	}
}

func TestBuildFilename_ProgramIDEightChars(t *testing.T) {
	date := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	name := BuildFilename("ONEFINX1", date, 1, "issuance")
	// First 8 characters must be the program ID (§6.6.1)
	if name[:8] != "ONEFINX1" {
		t.Errorf("first 8 chars should be program ID, got %q", name[:8])
	}
}

// ─── IsRT99Halt ───────────────────────────────────────────────────────────────
// Critical distinction (§6.5.1):
// Full-file halt: return file contains EXACTLY ONE record and it is RT99.
//   → entire file rejected; ALL members must be dead-lettered; batch_files → HALTED
// Individual record failure: RT99 appears among other records.
//   → only that record failed; other records processed normally
// Getting this wrong means either halting a file that only had one failure,
// or missing a full-file rejection and leaving all members unprocessed.

func TestIsRT99Halt_TrueWhenSingleRT99(t *testing.T) {
	// Exactly one record and it's RT99 — full-file pre-processing halt
	if !IsRT99Halt(1, "RT99") {
		t.Error("expected full-file halt when return file contains exactly one RT99 record")
	}
}

func TestIsRT99Halt_FalseWhenMultipleRecordsWithRT99(t *testing.T) {
	// RT99 present but other records too — individual record failure, not full-file halt
	if IsRT99Halt(50, "RT99") {
		t.Error("must not treat as full-file halt when return file has multiple records (individual failure)")
	}
}

func TestIsRT99Halt_FalseWhenSingleNonRT99(t *testing.T) {
	// One record but not RT99 — edge case, not a halt
	if IsRT99Halt(1, "RT30") {
		t.Error("single non-RT99 record is not a halt")
	}
}

func TestIsRT99Halt_FalseWhenEmpty(t *testing.T) {
	if IsRT99Halt(0, "") {
		t.Error("empty return file is not an RT99 halt (different error condition)")
	}
}

func TestIsRT99Halt_FalseWhenZeroCountRT99(t *testing.T) {
	if IsRT99Halt(0, "RT99") {
		t.Error("zero record count with RT99 is not a valid halt condition")
	}
}

// ─── Constants ────────────────────────────────────────────────────────────────
// These constants are load-bearing — wrong values cause FIS to reject files or
// silently misprocess them.

func TestLogFileIndicatorAll_IsZero(t *testing.T) {
	// MUST be '0' — returns all records (§6.6.3)
	// Any other value makes Stage 7 blind to successful enrollments
	if LogFileIndicatorAll != '0' {
		t.Errorf("LogFileIndicatorAll must be '0', got %q — Stage 7 requires all records returned", LogFileIndicatorAll)
	}
}

func TestRecordWidth_Is400(t *testing.T) {
	// FIS fixed-width format: every record is exactly 400 bytes (§6.6.2)
	if RecordWidth != 400 {
		t.Errorf("RecordWidth must be 400, got %d", RecordWidth)
	}
}

func TestCRLF_IsCarriageReturnLineFeed(t *testing.T) {
	// FIS requires CRLF record separator (§6.6.2)
	if CRLF != "\r\n" {
		t.Errorf("CRLF must be \\r\\n, got %q", CRLF)
	}
}

func TestATCodes(t *testing.T) {
	// AT codes are load-bearing — wrong value sends wrong FIS instruction
	if ATCodeLoad != "AT01" {
		t.Errorf("ATCodeLoad must be AT01, got %q", ATCodeLoad)
	}
	if ATCodeSweep != "AT30" {
		t.Errorf("ATCodeSweep must be AT30 (period-end purse expiry), got %q", ATCodeSweep)
	}
}

func TestRTRecordTypes(t *testing.T) {
	tests := []struct{ name, got, want string }{
		{"File Header", RTFileHeader, "RT10"},
		{"Batch Header", RTBatchHeader, "RT20"},
		{"New Account", RTNewAccount, "RT30"},
		{"Card Update", RTCardUpdate, "RT37"},
		{"Fund Load", RTFundLoad, "RT60"},
		{"Batch Trailer", RTBatchTrailer, "RT80"},
		{"File Trailer", RTFileTrailer, "RT90"},
		{"Pre-Processing Halt", RTPreProcessingHalt, "RT99"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("RT record type %s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
