// Package testutil provides mock implementations of port interfaces for unit testing.
// These are minimal in-memory mocks — no real database, S3, or FIS connections.
// Use these in stage tests to isolate business logic from infrastructure.
package testutil

import (
	"context"
	"fmt"
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

// ─── MockProgramLookup ───────────────────────────────────────────────────────

// MockProgramLookup implements ports.ProgramLookup for unit tests.
// Programs maps "tenantID|subprogramID" → uuid.UUID.
// If a key is absent, returns an error simulating an unknown program.
type MockProgramLookup struct {
	mu       sync.Mutex
	Programs map[string]uuid.UUID
	CallLog  []string // each entry is "tenantID|subprogramID"
}

func NewMockProgramLookup() *MockProgramLookup {
	return &MockProgramLookup{Programs: make(map[string]uuid.UUID)}
}

// Register adds a known program mapping for tests.
func (m *MockProgramLookup) Register(tenantID, subprogramID string, id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Programs[tenantID+"|"+subprogramID] = id
}

func (m *MockProgramLookup) GetProgramByTenantAndSubprogram(_ context.Context, tenantID, subprogramID string) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := tenantID + "|" + subprogramID
	m.CallLog = append(m.CallLog, key)
	if id, ok := m.Programs[key]; ok {
		return id, nil
	}
	return uuid.Nil, fmt.Errorf("programs.GetByTenantAndSubprogram: no active program for tenant=%s subprogram=%s", tenantID, subprogramID)
}

// CallCount returns how many times the lookup was called (cache-miss indicator).
func (m *MockProgramLookup) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.CallLog)
}

// ─── MockDeadLetterRepository ────────────────────────────────────────────────

type MockDeadLetterRepository struct {
	mu      sync.Mutex
	Entries []*ports.DeadLetterEntry
	WriteErr error
}

func NewMockDeadLetterRepository() *MockDeadLetterRepository {
	return &MockDeadLetterRepository{}
}

func (m *MockDeadLetterRepository) Write(_ context.Context, e *ports.DeadLetterEntry) error {
	if m.WriteErr != nil {
		return m.WriteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Entries = append(m.Entries, e)
	return nil
}

func (m *MockDeadLetterRepository) ListUnresolved(_ context.Context, correlationID uuid.UUID) ([]*ports.DeadLetterEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*ports.DeadLetterEntry
	for _, e := range m.Entries {
		if e.CorrelationID == correlationID && e.ResolvedAt == nil {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MockDeadLetterRepository) MarkReplayed(_ context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.Entries {
		if e.ID == id {
			e.ReplayedAt = &at
		}
	}
	return nil
}

func (m *MockDeadLetterRepository) MarkResolved(_ context.Context, id uuid.UUID, resolvedBy, notes string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, e := range m.Entries {
		if e.ID == id {
			e.ResolvedAt = &now
			e.ResolvedBy = &resolvedBy
			e.ResolutionNotes = &notes
		}
	}
	return nil
}

// ─── MockDomainCommandRepository ─────────────────────────────────────────────

type MockDomainCommandRepository struct {
	mu       sync.Mutex
	Commands []*ports.DomainCommand
	FindErr  error
	InsertErr error
}

func NewMockDomainCommandRepository() *MockDomainCommandRepository {
	return &MockDomainCommandRepository{}
}

func (m *MockDomainCommandRepository) Insert(_ context.Context, cmd *ports.DomainCommand) error {
	if m.InsertErr != nil {
		return m.InsertErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Commands = append(m.Commands, cmd)
	return nil
}

func (m *MockDomainCommandRepository) FindDuplicate(_ context.Context, tenantID, clientMemberID, commandType, benefitPeriod string, correlationID uuid.UUID) (*ports.DomainCommand, error) {
	if m.FindErr != nil {
		return nil, m.FindErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Commands {
		if c.TenantID == tenantID &&
			c.ClientMemberID == clientMemberID &&
			c.CommandType == commandType &&
			c.BenefitPeriod == benefitPeriod &&
			c.CorrelationID == correlationID {
			return c, nil
		}
	}
	return nil, nil
}

func (m *MockDomainCommandRepository) UpdateStatus(_ context.Context, id uuid.UUID, status string, reason *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Commands {
		if c.ID == id {
			c.Status = status
			c.FailureReason = reason
		}
	}
	return nil
}
