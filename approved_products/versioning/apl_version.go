// Package versioning manages APL version lifecycle and the active version pointer.
//
// apl_versions is immutable once created (§4.2.5).
// apl_rules.active_version_id is the atomic pointer to the live version (§4.2.6).
// Activating a new APL version requires only updating active_version_id —
// the prior version row is never modified or deleted.
//
// RFU has all three restriction levels active simultaneously (§3.2, BRD 3/6/2026):
//   restriction_levels = ['mcc', 'merchant_id', 'upc']
// This is the strictest possible configuration.
// Do not assume this applies to other clients — restriction levels are client-configurable.
package versioning

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrNoActiveVersion is returned by GetActiveVersion when no apl_rules row exists
// for the requested (programID, benefitType), or when active_version_id is not yet set.
// Callers should treat this as a program setup gap, not a transient error.
var ErrNoActiveVersion = errors.New("no active APL version found — program setup may be incomplete")

// APLVersionRepository is the persistence port for apl_versions and apl_rules.
type APLVersionRepository interface {
	CreateVersion(ctx context.Context, v *APLVersion) error
	ActivateVersion(ctx context.Context, programID uuid.UUID, benefitType string, versionID uuid.UUID) error
	GetActiveVersion(ctx context.Context, programID uuid.UUID, benefitType string) (*APLVersion, error)
}

// APLVersion represents one immutable APL snapshot (apl_versions table, §4.2.5).
type APLVersion struct {
	ID               uuid.UUID
	ProgramID        uuid.UUID
	BenefitType      string     // OTC|FOD|CMB
	VersionNumber    int        // monotonically increasing per (program_id, benefit_type)
	RuleCount        int
	S3Key            string     // fis-exchange S3 object key for the APL file
	UploadedAt       time.Time
	UploadedBy       string     // service name or operator identity
	FISAckReceivedAt *time.Time // nil until FIS confirms receipt
	Notes            *string
}

// ── MockAPLRepo ────────────────────────────────────────────────────────────────
// In-memory mock for unit testing. Thread-safe.

// MockAPLRepo is a test double for APLVersionRepository.
// Seeded versions are returned by GetActiveVersion; CreateVersion appends to Versions;
// ActivateVersion updates ActiveVersionID.
type MockAPLRepo struct {
	mu sync.Mutex

	// Versions holds all versions written via CreateVersion, in insertion order.
	Versions []*APLVersion

	// ActiveVersionID is the version activated by the most recent ActivateVersion call.
	// Keyed by "programID|benefitType".
	ActiveVersionID map[string]uuid.UUID

	// SeedActive seeds a version to be returned by GetActiveVersion.
	// Keyed by "programID|benefitType".
	SeedActive map[string]*APLVersion

	// Errors optionally returns errors for specific operations.
	CreateVersionErr  error
	ActivateVersionErr error
	GetActiveVersionErr error
}

func NewMockAPLRepo() *MockAPLRepo {
	return &MockAPLRepo{
		ActiveVersionID: make(map[string]uuid.UUID),
		SeedActive:      make(map[string]*APLVersion),
	}
}

func (m *MockAPLRepo) CreateVersion(_ context.Context, v *APLVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CreateVersionErr != nil {
		return m.CreateVersionErr
	}
	if v.VersionNumber == 0 {
		v.VersionNumber = len(m.Versions) + 1
	}
	cp := *v
	m.Versions = append(m.Versions, &cp)
	return nil
}

func (m *MockAPLRepo) ActivateVersion(_ context.Context, programID uuid.UUID, benefitType string, versionID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ActivateVersionErr != nil {
		return m.ActivateVersionErr
	}
	key := fmt.Sprintf("%s|%s", programID, benefitType)
	m.ActiveVersionID[key] = versionID
	return nil
}

func (m *MockAPLRepo) GetActiveVersion(_ context.Context, programID uuid.UUID, benefitType string) (*APLVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.GetActiveVersionErr != nil {
		return nil, m.GetActiveVersionErr
	}
	key := fmt.Sprintf("%s|%s", programID, benefitType)
	v, ok := m.SeedActive[key]
	if !ok {
		return nil, ErrNoActiveVersion
	}
	cp := *v
	return &cp, nil
}
