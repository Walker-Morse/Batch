// Package ports defines the interface contracts (adapter seams) for the One Fintech platform.
//
// Per ADR-001, these are named seams that preserve a clear migration path to full
// hexagonal architecture. ALL SCP record format knowledge lives in the fis_submission
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
// Datadog API key, PGP keys, SCP credentials, DB passwords all go through here.
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
	// FindDuplicate returns a non-nil existing command if the member has already
	// been submitted for the given benefit period — across ALL files, not just the
	// current correlation ID. Scoping to correlation_id was wrong: a member
	// submitted in a previous file for the same benefit period is a duplicate
	// regardless of which file it came from.
	// Only FAILED commands are excluded — a failed submission may be retried.
	FindDuplicate(ctx context.Context, tenantID, clientMemberID, commandType, benefitPeriod string) (*DomainCommand, error)
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

// ─── SCP Batch Processor (ADR-001) ───────────────────────────────────────────

// FISBatchAssembler is the adapter seam for SCP record construction.
// ALL SCP record format knowledge (field offsets, padding rules, AT codes,
// RT10/RT20/RT80/RT90 wrapper structure) lives in the implementation.
// Domain logic and pipeline stages consume this interface only.
type FISBatchAssembler interface {
	AssembleFile(ctx context.Context, req *AssembleRequest) (*AssembledFile, error)
}

// FISTransport delivers assembled PGP-encrypted files to SCP via Secure File Transfer
// and polls the SCP inbound S3 prefix for return files (Stage 6).
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
// batch_file_id (UUID), row_sequence_number (int), domain_command_id (UUID).
// This is the technical basis for the "Datadog receives zero PHI" architectural guarantee.
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
	FailureStage      string // validation|row_processing|batch_assembly|processor_deposit|return_file_wait|reconciliation	FailureReason     string // structured error code ONLY — no PHI ever
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

// LogEvent is a structured observability event emitted by every pipeline stage.
//
// ZERO PHI CONTRACT: The only permitted identifiers are opaque keys —
// correlation_id, tenant_id, client_id, batch_file_id, row_sequence_number,
// domain_command_id. No names, dates of birth, addresses, card numbers, or
// any HIPAA-defined identifier may appear in any field of this struct.
// Basis: §7.2, HIPAA §164.312(b), OWASP ASVS 7.1.1.
//
// Required fields — must be set on every LogEvent without exception:
//   CorrelationID, TenantID, BatchFileID (use uuid.Nil before Stage 1 writes the row).
//
// These three fields are non-pointer values so the compiler enforces their presence.
// All other fields are optional (*T) and are omitted from the JSON output when nil.
type LogEvent struct {
	// ── Required on every event ──────────────────────────────────────────────
	CorrelationID uuid.UUID // non-pointer: always required
	TenantID      string    // non-pointer: always required
	BatchFileID   uuid.UUID // non-pointer: always required (uuid.Nil before Stage 1)

	// ── Event identity ───────────────────────────────────────────────────────
	EventType string // see observability.Event* constants
	Level     string // INFO | WARN | ERROR

	// ── Optional context ─────────────────────────────────────────────────────
	ClientID          *string
	Stage             *string    // snake_case stage name e.g. "stage3_row_processing"
	Message           string

	// ── Per-row fields (set only on row-level events) ─────────────────────
	RowSequenceNumber *int
	DomainCommandID   *uuid.UUID // domain_commands.id — opaque, zero PHI
	CommandType       *string    // ENROLL|UPDATE|LOAD|SWEEP|SUSPEND|TERMINATE
	BenefitPeriod     *string    // ISO YYYY-MM
	SubprogramID      *int64
	FISResultCode     *string    // SCP 3-digit result code e.g. "000"

	// ── Failure fields ───────────────────────────────────────────────────────
	Error           *string // structured error code only — no PHI
	FailureCategory *string // DATA_GAP|DUPLICATE|PROCESSING_ERROR|SYSTEM_ERROR

	// ── File-level fields (set on file/stage events) ─────────────────────
	S3Key     *string
	SizeBytes *int64
	SHA256    *string // first 8 chars only — never full hash in WARN/ERROR contexts

	// ── Aggregate counts (stage-level summary events) ─────────────────────
	Total       *int
	Malformed   *int
	MalformedRate *string // "0.0%" format
	Staged      *int
	Duplicates  *int
	Failed      *int
	RT30Count   *int
	RT37Count   *int
	RT60Count   *int
	DeadLetterRate *string // "0.0%" format

	// ── Assembly / transfer fields (Stage 4, 5, 6) ───────────────────────
	Filename       *string
	RecordCount    *int
	ReturnFilename *string
	WaitMs         *int64

	// ── Reconciliation fields (Stage 7) ──────────────────────────────────
	Completed    *int
	RT30Completed *int
	RT60Completed *int

	// ── Pipeline-level fields ─────────────────────────────────────────────
	FileType   *string
	DurationMs *int64
	Enrolled   *int
	DeadLettered *int
}

// AssembleRequest drives the SCP batch assembler for a single correlation ID.
type AssembleRequest struct {
	CorrelationID uuid.UUID
	TenantID      string
	// ProgramID is the programs.id UUID for this file — sourced from the first
	// staged RT30 row. Required to key fis_sequence.Next (§6.6.1).
	ProgramID uuid.UUID
	// LogFileIndicator MUST be '0' (return all records) — hardcoded in adapter,
	// not overridable at runtime (§6.6.3). Required for Stage 7 to capture
	// SCP-assigned card IDs and purse numbers for every enrolled member.
	LogFileIndicator byte
	// TestProdIndicator: 'T' for DEV (format test only), 'P' for TST/PRD.
	// Driven by PIPELINE_ENV environment variable — never hardcoded (§6.6.4).
	TestProdIndicator byte
}

// AssembledFile is the result of SCP batch assembly.
type AssembledFile struct {
	Filename    string       // SCP naming convention: ppppppppmmddccyyss.*.txt (§6.6.1)
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

// ─── Program Lookup (Stage 3) ────────────────────────────────────────────────

// ProgramLookup resolves the programs table UUID from the SCP subprogram
// identifier carried on every SRG310 row. Implemented by DomainStateRepo.
// Stage 3 depends on this narrow interface — not the full DomainStateRepo —
// so it can be mocked in unit tests without a live DB connection.
type ProgramLookup interface {
	GetProgramByTenantAndSubprogram(ctx context.Context, tenantID, fisSubprogramID string) (uuid.UUID, error)
}

// ─── Batch Records Query (Stage 4) ───────────────────────────────────────────

// StagedRecords holds all STAGED batch records for a single correlation ID,
// grouped by record type. Returned by BatchRecordsLister.ListStagedByCorrelationID.
// Ordered by sequence_in_file ASC within each slice.
type StagedRecords struct {
	RT30 []*StagedRT30
	RT37 []*StagedRT37
	RT60 []*StagedRT60
}

// StagedRT30 is the assembler-visible projection of a staged RT30 row.
// Raw PHI fields are present — assembler uses them only for SCP record construction.
// Never log these values (§7.2, HIPAA §164.312(b)).
type StagedRT30 struct {
	ID             uuid.UUID
	SequenceInFile int
	ClientMemberID string
	ProgramID      uuid.UUID // programs.id — passed to fis_sequence.Next in Stage 4
	SubprogramID   *int64
	PackageID      *string
	FirstName      *string
	LastName       *string
	DateOfBirth    *time.Time
	Address1       *string
	Address2       *string
	City           *string
	State          *string
	ZIP            *string
	PhoneNumber    int64   // PHI; digits only; 0 when not provided
	Email          *string
	CardDesignID   *string
	CustomCardID   *string
}

// StagedRT37 is the assembler-visible projection of a staged RT37 row.
type StagedRT37 struct {
	ID             uuid.UUID
	SequenceInFile int
	ClientMemberID string
	FISCardID      string
	CardStatusCode int16
	ReasonCode     *string
}

// StagedRT60 is the assembler-visible projection of a staged RT60 row.
type StagedRT60 struct {
	ID              uuid.UUID
	SequenceInFile  int
	ATCode          *string
	ClientMemberID  string
	FISCardID       string
	PurseName       *string
	AmountCents     int64
	EffectiveDate   time.Time
	ClientReference *string
}

// BatchRecordsLister is the narrow read interface for Stage 4 batch assembly.
// Implemented by BatchRecordsRepo. Narrow interface allows unit testing of
// AssemblerImpl without a live DB connection.
type BatchRecordsLister interface {
	ListStagedByCorrelationID(ctx context.Context, correlationID uuid.UUID) (*StagedRecords, error)
}

// BatchRecordsReconciler is the narrow interface for Stage 7 return file processing.
// Implemented by aurora.BatchRecordsRepo.
type BatchRecordsReconciler interface {
	// GetStagedByCorrelationAndSequence looks up a staged record by its composite key.
	// Returns recordID, domainCommandID, benefitPeriod, and any error.
	// benefitPeriod is ISO YYYY-MM from the domain_commands row — used by the RT60
	// path to stamp the correct purse without relying on wall-clock time.
	GetStagedByCorrelationAndSequence(ctx context.Context, correlationID uuid.UUID, sequenceInFile int, recordType string) (recordID uuid.UUID, domainCommandID uuid.UUID, benefitPeriod string, err error)
	UpdateStatus(ctx context.Context, id uuid.UUID, recordType string, status string, fisResultCode, fisResultMessage *string) error
}
