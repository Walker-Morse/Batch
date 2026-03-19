package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
)

// pipeAdapter wires the adapter to write into a bytes.Buffer via an OS pipe.
func pipeAdapter(t *testing.T) (*CloudWatchAdapter, *bytes.Buffer) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	a := &CloudWatchAdapter{env: "TST", out: w}
	buf := &bytes.Buffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 65536)
		n, _ := r.Read(b)
		buf.Write(b[:n])
		r.Close()
	}()
	t.Cleanup(func() { w.Close(); <-done })
	return a, buf
}

// ─── Required fields always present ──────────────────────────────────────────

func TestLogEvent_RequiredFieldsAlwaysPresent(t *testing.T) {
	a, buf := pipeAdapter(t)

	corrID  := uuid.New()
	batchID := uuid.New()

	if err := a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "stage2.complete",
		Level:         "INFO",
		CorrelationID: corrID,
		TenantID:      "rfu-oregon",
		BatchFileID:   batchID,
		Message:       "validation complete",
	}); err != nil {
		t.Fatalf("LogEvent() error: %v", err)
	}
	a.out.Close()

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, field := range []string{"timestamp", "level", "env", "event_type", "correlation_id", "tenant_id", "batch_file_id", "message"} {
		if v, ok := raw[field]; !ok || v == "" {
			t.Errorf("required field %q missing or empty", field)
		}
	}

	if raw["correlation_id"] != corrID.String() {
		t.Errorf("correlation_id = %v; want %s", raw["correlation_id"], corrID)
	}
	if raw["tenant_id"] != "rfu-oregon" {
		t.Errorf("tenant_id = %v; want rfu-oregon", raw["tenant_id"])
	}
	if raw["batch_file_id"] != batchID.String() {
		t.Errorf("batch_file_id = %v; want %s", raw["batch_file_id"], batchID)
	}
}

func TestLogEvent_UuidNilBatchFileID_StillPresent(t *testing.T) {
	// Before Stage 1 writes the batch_files row, BatchFileID is uuid.Nil.
	// It must still appear in the JSON — not be omitted.
	a, buf := pipeAdapter(t)

	if err := a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "pipeline.startup",
		Level:         "INFO",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.Nil,
		Message:       "pipeline starting",
	}); err != nil {
		t.Fatalf("LogEvent() error: %v", err)
	}
	a.out.Close()

	var raw map[string]interface{}
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &raw)
	if _, ok := raw["batch_file_id"]; !ok {
		t.Error("batch_file_id must be present even when uuid.Nil")
	}
}

// ─── Optional fields omitted when nil ────────────────────────────────────────

func TestLogEvent_OptionalFieldsOmittedWhenNil(t *testing.T) {
	a, buf := pipeAdapter(t)

	if err := a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "batch.halt.triggered",
		Level:         "ERROR",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.New(),
		Message:       "batch halted",
	}); err != nil {
		t.Fatalf("LogEvent() error: %v", err)
	}
	a.out.Close()

	var raw map[string]interface{}
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &raw)

	optional := []string{
		"stage", "client_id", "row_sequence_number", "domain_command_id",
		"command_type", "benefit_period", "subprogram_id", "fis_result_code",
		"error", "failure_category", "s3_key", "size_bytes", "sha256",
		"total", "malformed", "malformed_rate", "staged", "duplicates", "failed",
		"rt30_count", "rt37_count", "rt60_count", "dead_letter_rate",
		"filename", "record_count", "return_filename", "wait_ms",
		"completed", "rt30_completed", "rt60_completed",
		"file_type", "duration_ms", "enrolled", "dead_lettered",
	}
	for _, f := range optional {
		if _, ok := raw[f]; ok {
			t.Errorf("optional field %q should be omitted when nil, but was present", f)
		}
	}
}

// ─── Per-row event fields ─────────────────────────────────────────────────────

func TestLogEvent_RowStagedFields(t *testing.T) {
	a, buf := pipeAdapter(t)

	corrID  := uuid.New()
	batchID := uuid.New()
	cmdID   := uuid.New()
	seq     := 2
	sub     := int64(26071)
	ct      := "ENROLL"
	bp      := "2026-06"

	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:         "row.staged",
		Level:             "INFO",
		CorrelationID:     corrID,
		TenantID:          "rfu-oregon",
		BatchFileID:       batchID,
		Stage:             strPtr("stage3_row_processing"),
		RowSequenceNumber: &seq,
		DomainCommandID:   &cmdID,
		CommandType:       &ct,
		BenefitPeriod:     &bp,
		SubprogramID:      &sub,
		Message:           "row staged: seq=2 command=ENROLL period=2026-06",
	})
	a.out.Close()

	var entry cloudWatchLogEntry
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry)

	if entry.RowSequenceNumber == nil || *entry.RowSequenceNumber != 2 {
		t.Errorf("row_sequence_number = %v; want 2", entry.RowSequenceNumber)
	}
	if entry.DomainCommandID == nil || *entry.DomainCommandID != cmdID.String() {
		t.Errorf("domain_command_id = %v; want %s", entry.DomainCommandID, cmdID)
	}
	if entry.CommandType == nil || *entry.CommandType != "ENROLL" {
		t.Errorf("command_type = %v; want ENROLL", entry.CommandType)
	}
	if entry.BenefitPeriod == nil || *entry.BenefitPeriod != "2026-06" {
		t.Errorf("benefit_period = %v; want 2026-06", entry.BenefitPeriod)
	}
	if entry.SubprogramID == nil || *entry.SubprogramID != 26071 {
		t.Errorf("subprogram_id = %v; want 26071", entry.SubprogramID)
	}
}

// ─── Stage3.complete aggregate fields ────────────────────────────────────────

func TestLogEvent_Stage3CompleteFields(t *testing.T) {
	a, buf := pipeAdapter(t)

	staged := 3; rt30 := 3; rt37 := 0; rt60 := 0; dups := 0; failed := 0

	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:      "stage3.complete",
		Level:          "INFO",
		CorrelationID:  uuid.New(),
		TenantID:       "rfu-oregon",
		BatchFileID:    uuid.New(),
		Stage:          strPtr("stage3_row_processing"),
		Staged:         &staged,
		RT30Count:      &rt30,
		RT37Count:      &rt37,
		RT60Count:      &rt60,
		Duplicates:     &dups,
		Failed:         &failed,
		DeadLetterRate: strPtr("0.0%"),
		Message:        "stage3 complete: staged=3 duplicates=0 failed=0",
	})
	a.out.Close()

	var entry cloudWatchLogEntry
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry)

	if entry.Staged == nil || *entry.Staged != 3 {
		t.Errorf("staged = %v; want 3", entry.Staged)
	}
	if entry.RT30Count == nil || *entry.RT30Count != 3 {
		t.Errorf("rt30_count = %v; want 3", entry.RT30Count)
	}
	if entry.DeadLetterRate == nil || *entry.DeadLetterRate != "0.0%" {
		t.Errorf("dead_letter_rate = %v; want 0.0%%", entry.DeadLetterRate)
	}
}

// ─── Dead letter failure_category ────────────────────────────────────────────

func TestLogEvent_DeadLetterFailureCategory(t *testing.T) {
	a, buf := pipeAdapter(t)

	seq := 3
	ct  := "ENROLL"
	fc  := "DATA_GAP"
	er  := "program_lookup_failed(subprogram=99999): not found"

	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:         "dead.letter.written",
		Level:             "WARN",
		CorrelationID:     uuid.New(),
		TenantID:          "rfu-oregon",
		BatchFileID:       uuid.New(),
		Stage:             strPtr("row_processing"),
		RowSequenceNumber: &seq,
		CommandType:       &ct,
		FailureCategory:   &fc,
		Error:             &er,
		Message:           "dead_letter: seq=3 reason=program_lookup_failed(subprogram=99999)",
	})
	a.out.Close()

	var entry cloudWatchLogEntry
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry)

	if entry.FailureCategory == nil || *entry.FailureCategory != "DATA_GAP" {
		t.Errorf("failure_category = %v; want DATA_GAP", entry.FailureCategory)
	}
	if entry.Error == nil {
		t.Error("error must be present on dead.letter.written")
	}
}

// ─── pipeline.complete ────────────────────────────────────────────────────────

func TestLogEvent_PipelineComplete(t *testing.T) {
	a, buf := pipeAdapter(t)

	dur := int64(1112)
	staged := 3; enrolled := 3; dl := 0; dups := 0

	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "pipeline.complete",
		Level:         "INFO",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.New(),
		Stage:         strPtr("pipeline"),
		DurationMs:    &dur,
		Staged:        &staged,
		Enrolled:      &enrolled,
		DeadLettered:  &dl,
		Duplicates:    &dups,
		Message:       "pipeline complete: duration=1112ms staged=3 enrolled=3 dead_lettered=0 duplicates=0",
	})
	a.out.Close()

	var entry cloudWatchLogEntry
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry)

	if entry.DurationMs == nil || *entry.DurationMs != 1112 {
		t.Errorf("duration_ms = %v; want 1112", entry.DurationMs)
	}
	if entry.Enrolled == nil || *entry.Enrolled != 3 {
		t.Errorf("enrolled = %v; want 3", entry.Enrolled)
	}
	if entry.DeadLettered == nil {
		t.Error("dead_lettered must be present on pipeline.complete")
	}
}

// ─── Single-line output ───────────────────────────────────────────────────────

func TestLogEvent_SingleLineOutput(t *testing.T) {
	a, buf := pipeAdapter(t)

	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "file.arrived",
		Level:         "INFO",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.New(),
		Message:       "file arrived",
	})
	a.out.Close()

	output := strings.TrimSpace(buf.String())
	if strings.Contains(output, "\n") {
		t.Error("log output must be single-line — newlines break CloudWatch log event boundaries")
	}
}

// ─── Error field ──────────────────────────────────────────────────────────────

func TestLogEvent_ErrorFieldPresent(t *testing.T) {
	a, buf := pipeAdapter(t)

	errMsg := "db: connection timeout"
	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "batch.halt.triggered",
		Level:         "ERROR",
		CorrelationID: uuid.New(),
		TenantID:      "rfu-oregon",
		BatchFileID:   uuid.New(),
		Message:       "pipeline halted",
		Error:         &errMsg,
	})
	a.out.Close()

	var entry cloudWatchLogEntry
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry)
	if entry.Error == nil || *entry.Error != errMsg {
		t.Errorf("error = %v; want %q", entry.Error, errMsg)
	}
}

// ─── RecordMetric ─────────────────────────────────────────────────────────────

func TestRecordMetric_WritesJSON(t *testing.T) {
	a, buf := pipeAdapter(t)

	if err := a.RecordMetric(context.Background(), "dead_letter_rate", 3.0, map[string]string{
		"tenant_id": "rfu-oregon",
		"env":       "DEV",
	}); err != nil {
		t.Fatalf("RecordMetric() error: %v", err)
	}
	a.out.Close()

	var entry cloudWatchMetricEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if entry.MetricName != "dead_letter_rate" {
		t.Errorf("metric_name = %q; want dead_letter_rate", entry.MetricName)
	}
	if entry.MetricValue != 3.0 {
		t.Errorf("metric_value = %v; want 3.0", entry.MetricValue)
	}
	if entry.Dimensions["tenant_id"] != "rfu-oregon" {
		t.Errorf("dimensions[tenant_id] = %q; want rfu-oregon", entry.Dimensions["tenant_id"])
	}
	if entry.EventType != "metric" {
		t.Errorf("event_type = %q; want metric", entry.EventType)
	}
}

// ─── Interface compliance ─────────────────────────────────────────────────────

func TestCloudWatchAdapter_ImplementsInterface(t *testing.T) {
	var _ ports.IObservabilityPort = (*CloudWatchAdapter)(nil)
}
