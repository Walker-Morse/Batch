// Package ports defines the interface contracts (adapter seams) for the One Fintech platform.
//
// Per ADR-001, these are named seams that preserve a clear migration path to full
// hexagonal architecture. ALL FIS record format knowledge lives in the fis_submission
// package — domain logic and pipeline stages never import that package directly.
// They consume these interfaces.
//
// IObservabilityPort (§7.5): CloudWatch is the substrate; Datadog is the operational layer.
// Swapping requires only a new adapter — no domain changes.
//
// MartWriter (§4.3.12): sole write path from pipeline to reporting schema.
// All methods are idempotent — ON CONFLICT DO UPDATE for dimensions,
// ON CONFLICT DO NOTHING for facts.
package ports

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
)

// ─── File Storage (ADR-006) ──────────────────────────────────────────────────

// FileStore is the port for S3-backed batch file staging and non-repudiation.
// Files are written once and never mutated. The inbound-raw prefix is write-once:
// no service has DeleteObject permission on inbound-raw (§5.4.5).
// Plaintext staged files MUST be deleted after Stage 4 — DeleteObject is only
// valid on the staged/ prefix (§5.4.3).
type FileStore interface {
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	PutObject(ctx context.Context, bucket, key string, body io.Reader) error
	// DeleteObject: only call on staged/ prefix — never on inbound-raw/ (§5.4.3, §5.4.5)
	DeleteObject(ctx context.Context, bucket, key string) error
	HeadObject(ctx context.Context, bucket, key string) (*ObjectMeta, error)
}

// ObjectMeta holds S3 object metadata.
type ObjectMeta struct {
	SHA256       string
	Size         int64
	LastModified time.Time
}

// ─── Secrets (§5.4.5) ────────────────────────────────────────────────────────

// SecretStore is the port for AWS Secrets Manager.
// No credential may appear in ECS task environment variables.
// Datadog API key, PGP keys, FIS credentials, DB passwords all go through here.
type SecretStore interface {
	GetSecret(ctx context.Context, secretARN string) (string, error)
}

// ─── Persistence ─────────────────────────────────────────────────────────────

// BatchFileRepository is the persistence port for batch_files (§4.1).
type BatchFileRepository interface {
	Create(ctx context.Context, f *BatchFile) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status string, updatedAt time.Time) error
	// SetRecordCount is called after parsing to record total row count.
	// Required for Stage 3's stall detection (staged_count vs record_count).
	SetRecordCount(ctx context.Context, id uuid.UUID, count int) error
	IncrementMalformedCount(ctx context.Context, id uuid.UUID) error
	GetByCorrelationID(ctx context.Context, correlationID uuid.UUID) (*BatchFile, error)
}

// DomainCommandRepository enforces the idempotency contract (§4.1.1).
// Written in Stage 3 BEFORE any domain state mutation — this sequence is mandatory.
type DomainCommandRepository interface {
	Insert(ctx context.Context, cmd *DomainCommand) error
	// FindDuplicate returns a non-nil existing command if the composite key
	// (correlation_id, client_member_id, command_type, benefit_period) already exists.
	FindDuplicate(ctx context.Context, tenantID, clientMemberID, commandType, benefitPeriod string, correlationID uuid.UUID) (*DomainCommand, error)
	UpdateStatus(ctx context.Context, id uuid.UUID, status string, failureReason *string) error
}

// DeadLetterRepository is the persistence port for dead_letter_store (§6.5).
// message_body may contain PHI — never log it; access-controlled at DB layer.
type DeadLetterRepository interface {
	Write(ctx context.Context, entry *DeadLetterEntry) error
	ListUnresolved(ctx context.Context, correlationID uuid.UUID) ([]*DeadLetterEntry, error)
	MarkReplayed(ctx context.Context, id uuid.UUID, at time.Time) error
	MarkResolved(ctx context.Context, id uuid.UUID, resolvedBy, notes string) error
}

// AuditLogWriter is the append-only compliance writer for audit_log (§6.2, §4.2.7).
// INSERT only. Role has no UPDATE or DELETE — enforced at PostgreSQL layer.
// Required for HIPAA §164.312(b) and PCI DSS 4.0 Req 10.2.
// Long-term archival to S3 Glacier required before go-live (Open Item #36).
type AuditLogWriter interface {
	Write(ctx context.Context, entry *AuditEntry) error
}

// ─── FIS Batch Processor (ADR-001) ───────────────────────────────────────────

// FISBatchAssembler is the adapter seam for FIS Prepaid Sunrise record construction.
// ALL FIS record format knowledge (field offsets, padding rules, AT codes,
// RT10/RT20/RT80/RT90 wrapper structure) lives in the implementation.
// Domain logic and pipeline stages consume this interface only.
type FISBatchAssembler interface {
	AssembleFile(ctx context.Context, req *AssembleRequest) (*AssembledFile, error)
}

// FISTransport delivers assembled PGP-encrypted files to FIS via Secure File Transfer
// and polls the FIS inbound S3 prefix for return files (Stage 6).
type FISTransport interface {
	Deliver(ctx context.Context, encryptedFile io.Reader, filename string) error
	// PollForReturn blocks until the return file arrives or timeout expires.
	// On timeout: caller transitions batch_files to STALLED and emits dead.letter.alert.
	PollForReturn(ctx context.Context, correlationID uuid.UUID, timeout time.Duration) (io.ReadCloser, error)
}

// ─── Observability (§7.5, ADR-004) ──────────────────────────────────────────

// IObservabilityPort is the Go interface boundary isolating all domain logic
// from the transport implementation.
// Architecture: ingest-task stdout → Datadog Agent ECS sidecar → Datadog
//               simultaneously retained in CloudWatch Logs (6-year HIPAA retention)
// Swapping the backend requires only a new adapter — no domain changes.
//
// HARD CONSTRAINT: log events MUST NEVER contain PHI or PII (§7.2, HIPAA §164.312(b),
// OWASP ASVS 7.1.1). Permitted identifiers: correlation_id, tenant_id, client_id,
// batch_file_id (int), row_sequence_number (int). This is the technical basis for
// the "Datadog receives zero PHI" architectural guarantee.
type IObservabilityPort interface {
	LogEvent(ctx context.Context, event *LogEvent) error
	RecordMetric(ctx context.Context, name string, value float64, dims map[string]string) error
}

// ─── Data Mart (§4.3.12) ─────────────────────────────────────────────────────

// MartWriter is the sole write path from the pipeline to the reporting schema.
// All methods are idempotent:
//   - Dimension writes: ON CONFLICT DO UPDATE
//   - Fact writes: ON CONFLICT DO NOTHING on composite idempotency key
//
// Called during Stage 3 (per member record) and Stage 7 (per batch file).
// The synchronous Aurora implementation is swappable for an async writer
// without modifying pipeline domain logic.
type MartWriter interface {
	// Stage 3 — called per member record
	UpsertMember(ctx context.Context, m *MemberRecord) (int64, error)
	UpsertCard(ctx context.Context, c *CardRecord) (int64, error)
	UpsertPurse(ctx context.Context, p *PurseRecord) (int64, error)
	WriteEnrollmentFact(ctx context.Context, f *EnrollmentFact) error
	WritePurseLifecycleFact(ctx context.Context, f *PurseLifecycleFact) error
	// Stage 7 — called per batch file after return file processing
	WriteReconciliationFact(ctx context.Context, f *ReconciliationFact) error
}

// ─── Supporting types ────────────────────────────────────────────────────────

// BatchFile represents the batch_files pipeline state table row.
type BatchFile struct {
	ID              uuid.UUID
	CorrelationID   uuid.UUID
	TenantID        string
	ClientID        string
	FileType        string
	Status          string
	RecordCount     *int
	MalformedCount  int
	SHA256Encrypted *string
	SHA256Plaintext *string
	ArrivedAt       time.Time
	SubmittedAt     *time.Time
	UpdatedAt       time.Time
}

// DomainCommand represents the domain_commands idempotency log row.
type DomainCommand struct {
	ID             uuid.UUID
	CorrelationID  uuid.UUID
	TenantID       string
	ClientMemberID string
	CommandType    string // ENROLL|UPDATE|LOAD|SWEEP|SUSPEND|TERMINATE
	BenefitPeriod  string // ISO YYYY-MM
	Status         string
	BatchFileID    uuid.UUID
	SequenceInFile int
	CreatedAt      time.Time
	CompletedAt    *time.Time
	FailureReason  *string
}

// DeadLetterEntry represents a dead_letter_store row.
// MessageBody may contain PHI — never log its contents (§6.5.2, §7.2).
type DeadLetterEntry struct {
	ID                uuid.UUID
	CorrelationID     uuid.UUID
	BatchFileID       *uuid.UUID
	RowSequenceNumber *int
	TenantID          string
	ClientMemberID    *string
	FailureStage      string // validation|row_processing|batch_assembly|fis_transfer|return_file_wait|reconciliation
	FailureReason     string // structured error code ONLY — no PHI ever
	MessageBody       []byte // JSONB — PHI present; never log this field
	RetryCount        int
	LastRetryAt       *time.Time
	ReplayedAt        *time.Time
	ResolvedAt        *time.Time
	ResolvedBy        *string
	ResolutionNotes   *string
	CreatedAt         time.Time
}

// AuditEntry represents one append-only row in audit_log.
type AuditEntry struct {
	TenantID       string
	EntityType     string
	EntityID       string  // UUID stored as TEXT
	OldState       *string
	NewState       string
	ChangedBy      string  // service name e.g. "ingest-task"
	CorrelationID  *uuid.UUID
	ClientMemberID *string // denormalised for compliance query patterns
	FISResultCode  *string
	Notes          *string
}

// LogEvent is a structured observability event — zero PHI permitted.
type LogEvent struct {
	EventType         string
	Level             string // INFO|WARN|ERROR
	CorrelationID     *uuid.UUID
	TenantID          *string
	ClientID          *string
	BatchFileID       *uuid.UUID
	RowSequenceNumber *int
	Stage             *string
	Message           string
	Error             *string
}

// AssembleRequest drives the FIS batch assembler for a single correlation ID.
type AssembleRequest struct {
	CorrelationID uuid.UUID
	TenantID      string
	// LogFileIndicator MUST be '0' (return all records) — hardcoded in adapter,
	// not overridable at runtime (§6.6.3). Required for Stage 7 to capture
	// FIS-assigned card IDs and purse numbers for every enrolled member.
	LogFileIndicator byte
	// TestProdIndicator: 'T' for DEV (format test only), 'P' for TST/PRD.
	// Driven by PIPELINE_ENV environment variable — never hardcoded (§6.6.4).
	TestProdIndicator byte
}

// AssembledFile is the result of FIS batch assembly.
type AssembledFile struct {
	Filename    string       // FIS naming convention: ppppppppmmddccyyss.*.txt (§6.6.1)
	RecordCount int
	Body        io.ReadCloser
}

// MartWriter record types (populated by Stage 3 and Stage 7)
type MemberRecord        struct{ ConsumerID uuid.UUID }
type CardRecord          struct{ CardID uuid.UUID }
type PurseRecord         struct{ PurseID uuid.UUID }
type EnrollmentFact      struct{ CorrelationID uuid.UUID; RowSequenceNumber int }
type PurseLifecycleFact  struct{ PurseID uuid.UUID; EventType string }
type ReconciliationFact  struct{ BatchFileID uuid.UUID; RowSequenceNumber int; FISResultCode string }

// SetRecordCount is called by Stage 2 after parsing to record the total row count.
// Required for Stage 3's stall detection (staged_count vs record_count comparison).
// This addition is appended here — the interface block above closes before this.

// ─── Program Lookup (Stage 3) ────────────────────────────────────────────────

// ProgramLookup resolves the programs table UUID from the FIS subprogram
// identifier carried on every SRG310 row. Implemented by DomainStateRepo.
// Stage 3 depends on this narrow interface — not the full DomainStateRepo —
// so it can be mocked in unit tests without a live DB connection.
type ProgramLookup interface {
	GetProgramByTenantAndSubprogram(ctx context.Context, tenantID, fisSubprogramID string) (uuid.UUID, error)
}
