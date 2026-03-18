// Package testutil provides mock implementations of port interfaces for unit testing.
// These are minimal in-memory mocks — no real database, S3, or FIS connections.
// Use these in stage tests to isolate business logic from infrastructure.
package testutil

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_shared/ports"
)

// ─── MockBatchFileRepository ─────────────────────────────────────────────────

type MockBatchFileRepository struct {
	mu      sync.Mutex
	Files   map[uuid.UUID]*ports.BatchFile
	CreateErr error
}

func NewMockBatchFileRepository() *MockBatchFileRepository {
	return &MockBatchFileRepository{Files: make(map[uuid.UUID]*ports.BatchFile)}
}

func (m *MockBatchFileRepository) Create(_ context.Context, f *ports.BatchFile) error {
	if m.CreateErr != nil {
		return m.CreateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Files[f.ID] = f
	return nil
}

func (m *MockBatchFileRepository) UpdateStatus(_ context.Context, id uuid.UUID, status string, updatedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.Files[id]; ok {
		f.Status = status
		f.UpdatedAt = updatedAt
	}
	return nil
}

func (m *MockBatchFileRepository) IncrementMalformedCount(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.Files[id]; ok {
		f.MalformedCount++
	}
	return nil
}

func (m *MockBatchFileRepository) GetByCorrelationID(_ context.Context, correlationID uuid.UUID) (*ports.BatchFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.Files {
		if f.CorrelationID == correlationID {
			return f, nil
		}
	}
	return nil, nil
}

// ─── MockAuditLogWriter ──────────────────────────────────────────────────────

type MockAuditLogWriter struct {
	mu      sync.Mutex
	Entries []*ports.AuditEntry
}

func (m *MockAuditLogWriter) Write(_ context.Context, e *ports.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Entries = append(m.Entries, e)
	return nil
}

// ─── MockObservability ───────────────────────────────────────────────────────

type MockObservability struct {
	mu      sync.Mutex
	Events  []*ports.LogEvent
	Metrics []MetricCall
}

type MetricCall struct {
	Name  string
	Value float64
	Dims  map[string]string
}

func (m *MockObservability) LogEvent(_ context.Context, e *ports.LogEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, e)
	return nil
}

func (m *MockObservability) RecordMetric(_ context.Context, name string, value float64, dims map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Metrics = append(m.Metrics, MetricCall{name, value, dims})
	return nil
}

func (m *MockObservability) EventCount(eventType string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, e := range m.Events {
		if e.EventType == eventType {
			count++
		}
	}
	return count
}

// ─── MockFileStore ───────────────────────────────────────────────────────────

type MockFileStore struct {
	Objects map[string][]byte
	Deleted []string
}

func NewMockFileStore() *MockFileStore {
	return &MockFileStore{Objects: make(map[string][]byte)}
}

func (m *MockFileStore) GetObject(_ context.Context, _, key string) (io.ReadCloser, error) {
	return nil, nil
}

func (m *MockFileStore) PutObject(_ context.Context, _, key string, _ io.Reader) error {
	return nil
}

func (m *MockFileStore) DeleteObject(_ context.Context, _, key string) error {
	m.Deleted = append(m.Deleted, key)
	return nil
}

func (m *MockFileStore) HeadObject(_ context.Context, _, key string) (*ports.ObjectMeta, error) {
	return &ports.ObjectMeta{SHA256: "abc123"}, nil
}

func (m *MockBatchFileRepository) SetRecordCount(_ context.Context, id uuid.UUID, count int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.Files[id]; ok {
		f.RecordCount = &count
	}
	return nil
}

func (m *MockFileStore) SHA256OfObject(_ context.Context, _, key string) (string, error) {
	// Return a deterministic fake hash for testing
	return "abc123def456abc123def456abc123def456abc123def456abc123def456abc1", nil
}
