package parser_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/walker-morse/batch/xtract_loader/parser"
)

func TestParseSTDMON(t *testing.T) {
	f, err := os.Open("testdata/STDMON10302025_Morse.txt")
	if err != nil {
		t.Skip("testdata not present — copy UAT sample to xtract_loader/parser/testdata/")
	}
	defer f.Close()

	var rowCount int
	result, err := parser.Parse(context.Background(), f, "STDMON10302025_Morse.txt", "STDFISMON",
		func(_ context.Context, _ int, fields []string) error {
			rowCount++
			if rowCount == 1 {
				if parser.Field(fields, 1) == "" {
					t.Error("issuer_client_id is empty on first row")
				}
				if parser.Field(fields, 80) == "" {
					t.Error("purse_code (field 81) is empty on first row")
				}
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if result.DetailCount == 0 {
		t.Error("no detail rows parsed")
	}
	if result.SHA256 == "" {
		t.Error("SHA-256 not computed")
	}
	if result.CountMismatch {
		t.Errorf("trailer count mismatch: detail=%d trailer=%d", result.DetailCount, result.TrailerCount)
	}
	if result.Header.FeedName != "STDFISMON" {
		t.Errorf("feed name: got %q want STDFISMON", result.Header.FeedName)
	}
	t.Logf("STDMON: %d rows, sha256=%s..., work_of_date=%s",
		result.DetailCount, result.SHA256[:8], result.Header.WorkOfDate.Format("2006-01-02"))
}

func TestParseSTDACCTBAL(t *testing.T) {
	f, err := os.Open("testdata/STDACCTBAL01302025_Morse.txt")
	if err != nil {
		t.Skip("testdata not present")
	}
	defer f.Close()
	result, err := parser.Parse(context.Background(), f, "STDACCTBAL01302025_Morse.txt", "STDFISACCTBAL",
		func(_ context.Context, _ int, _ []string) error { return nil })
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if result.CountMismatch {
		t.Errorf("trailer count mismatch: detail=%d trailer=%d", result.DetailCount, result.TrailerCount)
	}
	t.Logf("STDACCTBAL: %d rows", result.DetailCount)
}

func TestParseSTDNONMON(t *testing.T) {
	f, err := os.Open("testdata/STDNONMON11012025_MORSE.txt")
	if err != nil {
		t.Skip("testdata not present")
	}
	defer f.Close()
	result, err := parser.Parse(context.Background(), f, "STDNONMON11012025_MORSE.txt", "STDFISNONMON",
		func(_ context.Context, _ int, _ []string) error { return nil })
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if result.CountMismatch {
		t.Errorf("trailer count mismatch: detail=%d trailer=%d", result.DetailCount, result.TrailerCount)
	}
	t.Logf("STDNONMON: %d rows", result.DetailCount)
}

func TestParseSTDFSID(t *testing.T) {
	f, err := os.Open("testdata/STDFSID07232025_Test_Client.txt")
	if err != nil {
		t.Skip("testdata not present")
	}
	defer f.Close()
	var rCount, dCount int
	result, err := parser.Parse(context.Background(), f, "STDFSID07232025_Test_Client.txt", "",
		func(_ context.Context, _ int, fields []string) error {
			switch fields[0] {
			case "R":
				rCount++
			case "D":
				dCount++
			}
			return nil
		})
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	t.Logf("STDFSID: R=%d D=%d total=%d trailer=%d", rCount, dCount, rCount+dCount, result.TrailerCount)
}

func TestParseBlankFile(t *testing.T) {
	const blank = "H|FIS PREPAID|STDFISMON|03302026|03292026|\nT|0|\n"
	result, err := parser.Parse(context.Background(), strings.NewReader(blank), "blank_test", "STDFISMON",
		func(_ context.Context, _ int, _ []string) error { return nil })
	if err != nil {
		t.Fatalf("blank file parse error: %v", err)
	}
	if result.DetailCount != 0 {
		t.Errorf("expected 0 detail rows, got %d", result.DetailCount)
	}
	if result.CountMismatch {
		t.Error("blank file should not have count mismatch")
	}
	if result.SHA256 == "" {
		t.Error("SHA-256 not computed for blank file")
	}
}

func TestParseMissingTrailer(t *testing.T) {
	const truncated = "H|FIS PREPAID|STDFISMON|03302026|03292026|\nD|1|x|2\n"
	_, err := parser.Parse(context.Background(), strings.NewReader(truncated), "truncated_test", "STDFISMON",
		func(_ context.Context, _ int, _ []string) error { return nil })
	if err == nil {
		t.Error("expected error for missing T record, got nil")
	}
}

func TestParseFeedNameMismatch(t *testing.T) {
	const wrongFeed = "H|FIS PREPAID|STDFISAUTH|03302026|03292026|\nT|0|\n"
	_, err := parser.Parse(context.Background(), strings.NewReader(wrongFeed), "mismatch_test", "STDFISMON",
		func(_ context.Context, _ int, _ []string) error { return nil })
	if err == nil {
		t.Error("expected error for feed name mismatch, got nil")
	}
}

func TestFieldHelpers(t *testing.T) {
	fields := []string{"D", "1431777", "100.0000", "Null", "", "25.5000", "11032025", "11032025 05:16:37"}

	if got := parser.Field(fields, 1); got != "1431777" {
		t.Errorf("Field[1]: got %q want 1431777", got)
	}
	if got := parser.Field(fields, 3); got != "" {
		t.Errorf("Field[3] Null: got %q want empty", got)
	}
	if got := parser.Field(fields, 4); got != "" {
		t.Errorf("Field[4] empty: got %q want empty", got)
	}
	if got := parser.FieldInt64(fields, 1); got != 1431777 {
		t.Errorf("FieldInt64[1]: got %d want 1431777", got)
	}
	if got := parser.FieldInt64Cents(fields, 2); got != 10000 {
		t.Errorf("FieldInt64Cents 100.0000: got %d want 10000", got)
	}
	if got := parser.FieldInt64Cents(fields, 5); got != 2550 {
		t.Errorf("FieldInt64Cents 25.5000: got %d want 2550", got)
	}
	d := parser.FieldDate(fields, 6)
	if d.Year() != 2025 || int(d.Month()) != 11 || d.Day() != 3 {
		t.Errorf("FieldDate 11032025: got %v", d)
	}
	dt := parser.FieldDateTime(fields, 7)
	if dt.Hour() != 5 || dt.Minute() != 16 {
		t.Errorf("FieldDateTime: got %v", dt)
	}
	if got := parser.DateSK(d); got != 20251103 {
		t.Errorf("DateSK: got %d want 20251103", got)
	}
}
