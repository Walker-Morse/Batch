// Package testutil provides mock implementations of port interfaces for unit testing.
// These are minimal in-memory mocks — no real database, S3, or FIS connections.
// Use these in stage tests to isolate business logic from infrastructure.
package testutil

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/_adapters/aurora"
	"github.com/walker-morse/batch/_shared/domain"
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
	Objects     map[string][]byte
	Deleted     []string
	GetObjectFn func(ctx context.Context, bucket, key string) (io.ReadCloser, error)
}

func NewMockFileStore() *MockFileStore {
	return &MockFileStore{Objects: make(map[string][]byte)}
}

func (m *MockFileStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	if m.GetObjectFn != nil {
		return m.GetObjectFn(ctx, bucket, key)
	}
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
	mu        sync.Mutex
	Commands  []*ports.DomainCommand
	FindErr   error
	InsertErr error
	// StatusUpdates captures every UpdateStatus call for Stage 7 assertions.
	StatusUpdates []DomainCommandStatusUpdate
}

// DomainCommandStatusUpdate records a single UpdateStatus call.
type DomainCommandStatusUpdate struct {
	ID     uuid.UUID
	Status string
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
	m.StatusUpdates = append(m.StatusUpdates, DomainCommandStatusUpdate{ID: id, Status: status})
	return nil
}

// ─── MockBatchRecordWriter ────────────────────────────────────────────────────

// MockBatchRecordWriter implements pipeline.BatchRecordWriter for unit tests.
// Captures inserted records in-memory; no DB required.
type MockBatchRecordWriter struct {
	mu      sync.Mutex
	RT30    []*aurora.BatchRecordRT30
	RT37    []*aurora.BatchRecordRT37
	RT60    []*aurora.BatchRecordRT60
	InsertRT30Err error
	InsertRT37Err error
	InsertRT60Err error
}

func NewMockBatchRecordWriter() *MockBatchRecordWriter {
	return &MockBatchRecordWriter{}
}

func (m *MockBatchRecordWriter) InsertRT30(_ context.Context, rec *aurora.BatchRecordRT30) error {
	if m.InsertRT30Err != nil {
		return m.InsertRT30Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RT30 = append(m.RT30, rec)
	return nil
}

func (m *MockBatchRecordWriter) InsertRT37(_ context.Context, rec *aurora.BatchRecordRT37) error {
	if m.InsertRT37Err != nil {
		return m.InsertRT37Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RT37 = append(m.RT37, rec)
	return nil
}

func (m *MockBatchRecordWriter) InsertRT60(_ context.Context, rec *aurora.BatchRecordRT60) error {
	if m.InsertRT60Err != nil {
		return m.InsertRT60Err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.RT60 = append(m.RT60, rec)
	return nil
}

// ─── MockDomainStateWriter ────────────────────────────────────────────────────

// MockDomainStateWriter implements pipeline.DomainStateWriter for unit tests.
// Consumers maps tenantID+"|"+clientMemberID → *domain.Consumer (seeded via Register).
// Purses maps consumerID+"|"+benefitPeriod → *domain.Purse (seeded via RegisterPurse).
// BalanceUpdates records every (purseID, balanceCents) pair written via UpdatePurseBalance.
type MockDomainStateWriter struct {
	mu        sync.Mutex
	Consumers map[string]*domain.Consumer
	Cards     []*domain.Card
	Purses    map[string]*domain.Purse
	// Captured writes
	BalanceUpdates []PurseBalanceUpdate
	// Error injection
	UpsertConsumerErr        error
	InsertCardErr            error
	GetConsumerErr           error // if set, all GetConsumerByNaturalKey calls return this
	GetPurseErr              error // if set, all GetPurseByConsumerAndBenefitPeriod calls return this
	UpdatePurseBalanceErr    error
}

// PurseBalanceUpdate records a single UpdatePurseBalance call for assertion in tests.
type PurseBalanceUpdate struct {
	PurseID      uuid.UUID
	BalanceCents int64
}

func NewMockDomainStateWriter() *MockDomainStateWriter {
	return &MockDomainStateWriter{
		Consumers: make(map[string]*domain.Consumer),
		Purses:    make(map[string]*domain.Purse),
	}
}

// RegisterConsumer seeds a known consumer for GetConsumerByNaturalKey lookups.
func (m *MockDomainStateWriter) RegisterConsumer(tenantID, clientMemberID string, c *domain.Consumer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Consumers[tenantID+"|"+clientMemberID] = c
}

func (m *MockDomainStateWriter) UpsertConsumer(_ context.Context, c *domain.Consumer) error {
	if m.UpsertConsumerErr != nil {
		return m.UpsertConsumerErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Consumers[c.TenantID+"|"+c.ClientMemberID] = c
	return nil
}

func (m *MockDomainStateWriter) InsertCard(_ context.Context, c *domain.Card) error {
	if m.InsertCardErr != nil {
		return m.InsertCardErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Cards = append(m.Cards, c)
	return nil
}

func (m *MockDomainStateWriter) GetConsumerByNaturalKey(_ context.Context, tenantID, clientMemberID string) (*domain.Consumer, error) {
	if m.GetConsumerErr != nil {
		return nil, m.GetConsumerErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.Consumers[tenantID+"|"+clientMemberID]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("consumer not found: tenant=%s client_member_id=%s", tenantID, clientMemberID)
}

// RegisterPurse seeds a known purse for GetPurseByConsumerAndBenefitPeriod lookups.
func (m *MockDomainStateWriter) RegisterPurse(consumerID uuid.UUID, benefitPeriod string, p *domain.Purse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Purses[consumerID.String()+"|"+benefitPeriod] = p
}

func (m *MockDomainStateWriter) GetPurseByConsumerAndBenefitPeriod(_ context.Context, consumerID uuid.UUID, benefitPeriod string) (*domain.Purse, error) {
	if m.GetPurseErr != nil {
		return nil, m.GetPurseErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.Purses[consumerID.String()+"|"+benefitPeriod]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("purse not found: consumer=%s benefit_period=%s", consumerID, benefitPeriod)
}

func (m *MockDomainStateWriter) UpdatePurseBalance(_ context.Context, id uuid.UUID, balanceCents int64) error {
	if m.UpdatePurseBalanceErr != nil {
		return m.UpdatePurseBalanceErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.BalanceUpdates = append(m.BalanceUpdates, PurseBalanceUpdate{PurseID: id, BalanceCents: balanceCents})
	return nil
}

// ─── MockFISBatchAssembler ────────────────────────────────────────────────────

// MockFISBatchAssembler implements ports.FISBatchAssembler for unit tests.
// AssembleResult is returned on success. AssembleErr is returned instead if set.
type MockFISBatchAssembler struct {
	AssembleResult *ports.AssembledFile
	AssembleErr    error
	Calls          []*ports.AssembleRequest
}

func NewMockFISBatchAssembler(filename string, recordCount int, body string) *MockFISBatchAssembler {
	return &MockFISBatchAssembler{
		AssembleResult: &ports.AssembledFile{
			Filename:    filename,
			RecordCount: recordCount,
			Body:        io.NopCloser(strings.NewReader(body)),
		},
	}
}

func (m *MockFISBatchAssembler) AssembleFile(_ context.Context, req *ports.AssembleRequest) (*ports.AssembledFile, error) {
	m.Calls = append(m.Calls, req)
	if m.AssembleErr != nil {
		return nil, m.AssembleErr
	}
	if m.AssembleResult == nil {
		return nil, nil
	}
	// Return a fresh reader each call so tests that call Run() multiple times work.
	result := &ports.AssembledFile{
		Filename:    m.AssembleResult.Filename,
		RecordCount: m.AssembleResult.RecordCount,
		Body:        io.NopCloser(strings.NewReader(m.AssembleResult.Filename)),
	}
	return result, nil
}

// ─── MockBatchRecordsLister ───────────────────────────────────────────────────

// MockBatchRecordsLister implements ports.BatchRecordsLister for unit tests.
// Staged maps correlationID.String() → *ports.StagedRecords.
// Returns empty StagedRecords (not error) for unknown correlation IDs —
// matches production behaviour of ListStagedByCorrelationID.
type MockBatchRecordsLister struct {
	mu      sync.Mutex
	Staged  map[string]*ports.StagedRecords
	ListErr error
}

func NewMockBatchRecordsLister() *MockBatchRecordsLister {
	return &MockBatchRecordsLister{Staged: make(map[string]*ports.StagedRecords)}
}

// Register seeds staged records for a given correlation ID.
func (m *MockBatchRecordsLister) Register(correlationID string, records *ports.StagedRecords) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Staged[correlationID] = records
}

func (m *MockBatchRecordsLister) ListStagedByCorrelationID(_ context.Context, correlationID uuid.UUID) (*ports.StagedRecords, error) {
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.Staged[correlationID.String()]; ok {
		return r, nil
	}
	return &ports.StagedRecords{}, nil // empty — structurally valid empty file
}

// ─── MockFISTransport ─────────────────────────────────────────────────────────

// MockFISTransport implements ports.FISTransport for unit tests.
type MockFISTransport struct {
	mu                sync.Mutex
	DeliveredFilename string
	DeliveredBody     []byte
	DeliverErr        error
	PollResult        io.ReadCloser // returned by PollForReturn on success
	PollErr           error
}

func NewMockFISTransport() *MockFISTransport {
	return &MockFISTransport{}
}

func (m *MockFISTransport) Deliver(_ context.Context, body io.Reader, filename string) error {
	if m.DeliverErr != nil {
		return m.DeliverErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DeliveredFilename = filename
	data, _ := io.ReadAll(body)
	m.DeliveredBody = data
	return nil
}

func (m *MockFISTransport) PollForReturn(_ context.Context, _ uuid.UUID, _ time.Duration) (io.ReadCloser, error) {
	if m.PollErr != nil {
		return nil, m.PollErr
	}
	if m.PollResult != nil {
		return m.PollResult, nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

// ─── MockBatchRecordsReconciler ───────────────────────────────────────────────

// MockBatchRecordsReconciler implements ports.BatchRecordsReconciler for Stage 7 tests.
// StagedRows maps "correlationID|seq|type" → (recordID, domainCommandID).
// StatusUpdates captures all UpdateStatus calls for assertion.
type MockBatchRecordsReconciler struct {
	mu            sync.Mutex
	StagedRows    map[string][2]uuid.UUID // key → [recordID, cmdID]
	StatusUpdates []BatchRecordStatusUpdate
	GetStagedErr  error
	UpdateErr     error
}

type BatchRecordStatusUpdate struct {
	ID            uuid.UUID
	RecordType    string
	Status        string
	FISResultCode *string
}

func NewMockBatchRecordsReconciler() *MockBatchRecordsReconciler {
	return &MockBatchRecordsReconciler{StagedRows: make(map[string][2]uuid.UUID)}
}

// Register seeds a (recordID, domainCommandID) pair for lookup.
func (m *MockBatchRecordsReconciler) Register(correlationID uuid.UUID, seq int, recordType string, recordID, cmdID uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s|%d|%s", correlationID, seq, recordType)
	m.StagedRows[key] = [2]uuid.UUID{recordID, cmdID}
}

func (m *MockBatchRecordsReconciler) GetStagedByCorrelationAndSequence(_ context.Context, correlationID uuid.UUID, seq int, recordType string) (uuid.UUID, uuid.UUID, error) {
	if m.GetStagedErr != nil {
		return uuid.Nil, uuid.Nil, m.GetStagedErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := fmt.Sprintf("%s|%d|%s", correlationID, seq, recordType)
	if ids, ok := m.StagedRows[key]; ok {
		return ids[0], ids[1], nil
	}
	return uuid.Nil, uuid.Nil, fmt.Errorf("staged row not found: %s", key)
}

func (m *MockBatchRecordsReconciler) UpdateStatus(_ context.Context, id uuid.UUID, recordType, status string, fisResultCode, _ *string) error {
	if m.UpdateErr != nil {
		return m.UpdateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.StatusUpdates = append(m.StatusUpdates, BatchRecordStatusUpdate{
		ID: id, RecordType: recordType, Status: status, FISResultCode: fisResultCode,
	})
	return nil
}

// ─── MockDomainStateReconciler ────────────────────────────────────────────────

// MockDomainStateReconciler implements pipeline.DomainStateReconciler for Stage 7 tests.
type MockDomainStateReconciler struct {
	mu sync.Mutex
	// Seed data
	Consumers map[string]*domain.Consumer // tenantID|clientMemberID → Consumer
	Cards     map[uuid.UUID]*domain.Card  // consumerID → Card
	// Captured writes
	ConsumerFISUpdates  []ConsumerFISUpdate
	CardFISUpdates      []CardFISUpdate
	PurseFISUpdates     []PurseFISUpdate
	// Error injection
	GetConsumerErr              error
	GetCardErr                  error
	UpdateConsumerFISIDsErr     error
	UpdateCardFISIDErr          error
	UpdatePurseFISNumberErr     error
}

type ConsumerFISUpdate struct {
	ID          uuid.UUID
	FISPersonID string
	FISCUID     string
}

type CardFISUpdate struct {
	ID        uuid.UUID
	FISCardID string
}

type PurseFISUpdate struct {
	ConsumerID    uuid.UUID
	BenefitPeriod string
	FISNumber     int16
}

func NewMockDomainStateReconciler() *MockDomainStateReconciler {
	return &MockDomainStateReconciler{
		Consumers: make(map[string]*domain.Consumer),
		Cards:     make(map[uuid.UUID]*domain.Card),
	}
}

func (m *MockDomainStateReconciler) RegisterConsumer(tenantID, clientMemberID string, c *domain.Consumer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Consumers[tenantID+"|"+clientMemberID] = c
}

func (m *MockDomainStateReconciler) RegisterCard(consumerID uuid.UUID, c *domain.Card) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Cards[consumerID] = c
}

func (m *MockDomainStateReconciler) GetConsumerByNaturalKey(_ context.Context, tenantID, clientMemberID string) (*domain.Consumer, error) {
	if m.GetConsumerErr != nil {
		return nil, m.GetConsumerErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.Consumers[tenantID+"|"+clientMemberID]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("consumer not found: %s|%s", tenantID, clientMemberID)
}

func (m *MockDomainStateReconciler) GetCardByConsumerID(_ context.Context, consumerID uuid.UUID) (*domain.Card, error) {
	if m.GetCardErr != nil {
		return nil, m.GetCardErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.Cards[consumerID]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("card not found for consumer: %s", consumerID)
}

func (m *MockDomainStateReconciler) UpdateConsumerFISIdentifiers(_ context.Context, id uuid.UUID, fisPersonID, fisCUID string) error {
	if m.UpdateConsumerFISIDsErr != nil {
		return m.UpdateConsumerFISIDsErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ConsumerFISUpdates = append(m.ConsumerFISUpdates, ConsumerFISUpdate{ID: id, FISPersonID: fisPersonID, FISCUID: fisCUID})
	return nil
}

func (m *MockDomainStateReconciler) UpdateCardFISCardID(_ context.Context, id uuid.UUID, fisCardID string, _ time.Time) error {
	if m.UpdateCardFISIDErr != nil {
		return m.UpdateCardFISIDErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CardFISUpdates = append(m.CardFISUpdates, CardFISUpdate{ID: id, FISCardID: fisCardID})
	return nil
}

func (m *MockDomainStateReconciler) UpdatePurseFISNumber(_ context.Context, consumerID uuid.UUID, benefitPeriod string, fisNumber int16) error {
	if m.UpdatePurseFISNumberErr != nil {
		return m.UpdatePurseFISNumberErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PurseFISUpdates = append(m.PurseFISUpdates, PurseFISUpdate{ConsumerID: consumerID, BenefitPeriod: benefitPeriod, FISNumber: fisNumber})
	return nil
}

// ─── MockMartWriter ───────────────────────────────────────────────────────────

// MockMartWriter implements ports.MartWriter for unit tests.
// ReconciliationFacts captures all WriteReconciliationFact calls.
type MockMartWriter struct {
	mu                  sync.Mutex
	ReconciliationFacts []*ports.ReconciliationFact
}

func (m *MockMartWriter) UpsertMember(_ context.Context, _ *ports.MemberRecord) (int64, error) {
	return 0, nil
}
func (m *MockMartWriter) UpsertCard(_ context.Context, _ *ports.CardRecord) (int64, error) {
	return 0, nil
}
func (m *MockMartWriter) UpsertPurse(_ context.Context, _ *ports.PurseRecord) (int64, error) {
	return 0, nil
}
func (m *MockMartWriter) WriteEnrollmentFact(_ context.Context, _ *ports.EnrollmentFact) error {
	return nil
}
func (m *MockMartWriter) WritePurseLifecycleFact(_ context.Context, _ *ports.PurseLifecycleFact) error {
	return nil
}
func (m *MockMartWriter) WriteReconciliationFact(_ context.Context, f *ports.ReconciliationFact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ReconciliationFacts = append(m.ReconciliationFacts, f)
	return nil
}
