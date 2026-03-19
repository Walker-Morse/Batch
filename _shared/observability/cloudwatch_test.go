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

func TestCloudWatchAdapter_LogEvent_WritesJSON(t *testing.T) {
	a, buf := pipeAdapter(t)

	corrID := uuid.New()
	tid := "rfu-oregon"
	stage := "stage2_validation"
	msg := "validation complete"

	if err := a.LogEvent(context.Background(), &ports.LogEvent{
		EventType:     "stage2.complete",
		Level:         "INFO",
		CorrelationID: &corrID,
		TenantID:      &tid,
		Stage:         &stage,
		Message:       msg,
	}); err != nil {
		t.Fatalf("LogEvent() error: %v", err)
	}
	a.out.Close()

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("expected output, got empty")
	}

	var entry cloudWatchLogEntry
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, line)
	}

	if entry.EventType != "stage2.complete" {
		t.Errorf("event_type = %q; want stage2.complete", entry.EventType)
	}
	if entry.Level != "INFO" {
		t.Errorf("level = %q; want INFO", entry.Level)
	}
	if entry.Env != "TST" {
		t.Errorf("env = %q; want TST", entry.Env)
	}
	if entry.CorrelationID == nil || *entry.CorrelationID != corrID.String() {
		t.Errorf("correlation_id = %v; want %s", entry.CorrelationID, corrID)
	}
	if entry.TenantID == nil || *entry.TenantID != tid {
		t.Errorf("tenant_id = %v; want %s", entry.TenantID, tid)
	}
	if entry.Stage == nil || *entry.Stage != stage {
		t.Errorf("stage = %v; want %s", entry.Stage, stage)
	}
	if entry.Message != msg {
		t.Errorf("message = %q; want %q", entry.Message, msg)
	}
	if entry.Timestamp == "" {
		t.Error("timestamp must not be empty")
	}
}

func TestCloudWatchAdapter_LogEvent_NilFieldsOmitted(t *testing.T) {
	a, buf := pipeAdapter(t)

	if err := a.LogEvent(context.Background(), &ports.LogEvent{
		EventType: "batch.halt.triggered",
		Level:     "ERROR",
		Message:   "batch halted",
	}); err != nil {
		t.Fatalf("LogEvent() error: %v", err)
	}
	a.out.Close()

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, field := range []string{"correlation_id", "tenant_id", "batch_file_id", "row_sequence_number", "error"} {
		if _, ok := raw[field]; ok {
			t.Errorf("field %q should be omitted when nil, but was present", field)
		}
	}
}

func TestCloudWatchAdapter_LogEvent_ErrorFieldPresent(t *testing.T) {
	a, buf := pipeAdapter(t)

	errMsg := "db: connection timeout"
	if err := a.LogEvent(context.Background(), &ports.LogEvent{
		EventType: "batch.halt.triggered",
		Level:     "ERROR",
		Message:   "pipeline halted",
		Error:     &errMsg,
	}); err != nil {
		t.Fatalf("LogEvent() error: %v", err)
	}
	a.out.Close()

	var entry cloudWatchLogEntry
	json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry)
	if entry.Error == nil || *entry.Error != errMsg {
		t.Errorf("error = %v; want %q", entry.Error, errMsg)
	}
}

func TestCloudWatchAdapter_LogEvent_SingleLineOutput(t *testing.T) {
	a, buf := pipeAdapter(t)

	a.LogEvent(context.Background(), &ports.LogEvent{
		EventType: "stage1.complete",
		Level:     "INFO",
		Message:   "file arrived",
	})
	a.out.Close()

	output := strings.TrimSpace(buf.String())
	if strings.Contains(output, "\n") {
		t.Error("log output must be single-line — newlines break CloudWatch log event boundaries")
	}
}

func TestCloudWatchAdapter_RecordMetric_WritesJSON(t *testing.T) {
	a, buf := pipeAdapter(t)

	if err := a.RecordMetric(context.Background(), "dead_letter_rate", 3.0, map[string]string{
		"tenant_id": "rfu-oregon",
		"stage":     "stage3_row_processing",
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

func TestCloudWatchAdapter_ImplementsInterface(t *testing.T) {
	var _ ports.IObservabilityPort = (*CloudWatchAdapter)(nil)
}
