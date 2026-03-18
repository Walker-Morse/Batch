package fis_adapter

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
)

// ─── local mocks (avoid import cycle with testutil) ───────────────────────────

type mockBatchRecordsLister struct {
	records *ports.StagedRecords
	err     error
}

func (m *mockBatchRecordsLister) ListStagedByCorrelationID(_ context.Context, _ uuid.UUID) (*ports.StagedRecords, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.records == nil {
		return &ports.StagedRecords{}, nil
	}
	return m.records, nil
}

type mockSequenceStore struct{ seq int }

func (m *mockSequenceStore) Next(_ context.Context, _ string, _ time.Time) (int, error) {
	m.seq++
	return m.seq, nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func newTestAssembler(records *ports.StagedRecords) *AssemblerImpl {
	return NewAssembler(
		"PLACEHOLDER",
		&mockSequenceStore{},
		&mockBatchRecordsLister{records: records},
	)
}

func ptr[T any](v T) *T { return &v }

func readAll(t *testing.T, rc io.ReadCloser) []byte {
	t.Helper()
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	return b
}

// ─── empty staged records ─────────────────────────────────────────────────────

// An empty correlation ID must still produce a structurally valid FIS file:
// RT10, RT20, RT80, RT90 — no data records.
// Required for assembler idempotency (ADR-007).
func TestAssembleFile_EmptyStagedRecords(t *testing.T) {
	a := newTestAssembler(&ports.StagedRecords{})
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("AssembleFile empty: %v", err)
	}
	body := string(readAll(t, result.Body))
	if !strings.Contains(body, "RT10") {
		t.Error("missing RT10 File Header")
	}
	if !strings.Contains(body, "RT90") {
		t.Error("missing RT90 File Trailer")
	}
	if len(body) == 0 {
		t.Fatal("empty staged records must still produce a non-empty file")
	}
}

// ─── RT30 records appear ──────────────────────────────────────────────────────

func TestAssembleFile_RT30RecordsAppear(t *testing.T) {
	dob := time.Date(1990, 6, 15, 0, 0, 0, 0, time.UTC)
	subID := int64(12345)
	records := &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{
				ID:             uuid.New(),
				SequenceInFile: 1,
				ClientMemberID: "MBR-001",
				SubprogramID:   &subID,
				FirstName:      ptr("Jane"),
				LastName:       ptr("Smith"),
				DateOfBirth:    &dob,
				Address1:       ptr("123 Main St"),
				City:           ptr("Minneapolis"),
				State:          ptr("MN"),
				ZIP:            ptr("55401"),
			},
		},
	}
	a := newTestAssembler(records)
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("AssembleFile RT30: %v", err)
	}
	body := string(readAll(t, result.Body))
	if !strings.Contains(body, "RT30") {
		t.Error("assembled file missing RT30 record")
	}
	if !strings.Contains(body, "MBR-001") {
		t.Error("assembled file missing ClientMemberID in RT30")
	}
}

// ─── RT30 sequence order ──────────────────────────────────────────────────────

// Multiple RT30 records must appear in sequence_in_file ASC order.
func TestAssembleFile_RT30SequenceOrder(t *testing.T) {
	subID := int64(1)
	records := &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "FIRST", SubprogramID: &subID},
			{ID: uuid.New(), SequenceInFile: 2, ClientMemberID: "SECOND", SubprogramID: &subID},
			{ID: uuid.New(), SequenceInFile: 3, ClientMemberID: "THIRD", SubprogramID: &subID},
		},
	}
	a := newTestAssembler(records)
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("sequence order: %v", err)
	}
	body := string(readAll(t, result.Body))
	if !(strings.Index(body, "FIRST") < strings.Index(body, "SECOND") &&
		strings.Index(body, "SECOND") < strings.Index(body, "THIRD")) {
		t.Error("RT30 records not in sequence_in_file ASC order")
	}
}

// ─── RT60 records appear ──────────────────────────────────────────────────────

func TestAssembleFile_RT60RecordsAppear(t *testing.T) {
	eff := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	records := &ports.StagedRecords{
		RT60: []*ports.StagedRT60{
			{
				ID:             uuid.New(),
				SequenceInFile: 1,
				ClientMemberID: "MBR-RT60",
				FISCardID:      "",
				ATCode:         ptr("AT01"),
				AmountCents:    5000,
				EffectiveDate:  eff,
			},
		},
	}
	a := newTestAssembler(records)
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("AssembleFile RT60: %v", err)
	}
	if !strings.Contains(string(readAll(t, result.Body)), "RT60") {
		t.Error("assembled file missing RT60 record")
	}
}

// ─── record count integrity ───────────────────────────────────────────────────

// RT90 total must equal RT10 + RT20 + data records + RT80 + RT90.
// Mismatch triggers FIS RT99 pre-processing halt (§6.5.1).
func TestAssembleFile_RecordCountIntegrity(t *testing.T) {
	subID := int64(1)
	records := &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "A", SubprogramID: &subID},
			{ID: uuid.New(), SequenceInFile: 2, ClientMemberID: "B", SubprogramID: &subID},
		},
	}
	a := newTestAssembler(records)
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("record count: %v", err)
	}
	// 2 data + RT10 + RT20 + RT80 + RT90 = 6
	if result.RecordCount != 6 {
		t.Errorf("RecordCount: got %d, want 6", result.RecordCount)
	}
}

// ─── every record is exactly 400 bytes ───────────────────────────────────────

func TestAssembleFile_EachRecordIs400Bytes(t *testing.T) {
	subID := int64(1)
	records := &ports.StagedRecords{
		RT30: []*ports.StagedRT30{
			{ID: uuid.New(), SequenceInFile: 1, ClientMemberID: "MBR-WIDTH", SubprogramID: &subID},
		},
	}
	a := newTestAssembler(records)
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("record width: %v", err)
	}
	body := readAll(t, result.Body)
	for i, line := range strings.Split(string(body), CRLF) {
		if line == "" {
			continue
		}
		if len(line) != RecordWidth {
			t.Errorf("record %d: %d bytes, want %d (§6.6.2)", i, len(line), RecordWidth)
		}
	}
}

// ─── filename format ──────────────────────────────────────────────────────────

func TestAssembleFile_FilenameFormat(t *testing.T) {
	a := newTestAssembler(&ports.StagedRecords{})
	result, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err != nil {
		t.Fatalf("filename: %v", err)
	}
	if !strings.HasSuffix(result.Filename, ".txt") {
		t.Errorf("filename %q must end with .txt", result.Filename)
	}
	if result.Filename[:8] != "PLACEHOL" {
		t.Errorf("filename first 8 chars should be program ID prefix, got %q", result.Filename[:8])
	}
}

// ─── lister error propagates ──────────────────────────────────────────────────

func TestAssembleFile_ListerErrorPropagates(t *testing.T) {
	a := NewAssembler(
		"PLACEHOLDER",
		&mockSequenceStore{},
		&mockBatchRecordsLister{err: fmt.Errorf("db unavailable")},
	)
	_, err := a.AssembleFile(context.Background(), &ports.AssembleRequest{
		CorrelationID:     uuid.New(),
		TenantID:          "tenant-001",
		LogFileIndicator:  '0',
		TestProdIndicator: 'T',
	})
	if err == nil {
		t.Fatal("expected error when BatchRecordsLister fails")
	}
}
