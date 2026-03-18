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
	"time"

	"github.com/google/uuid"
)

// APLVersionRepository is the persistence port for apl_versions and apl_rules.
type APLVersionRepository interface {
	CreateVersion(ctx context.Context, v *APLVersion) error
	ActivateVersion(ctx context.Context, programID uuid.UUID, benefitType string, versionID uuid.UUID) error
	GetActiveVersion(ctx context.Context, programID uuid.UUID, benefitType string) (*APLVersion, error)
}

// APLVersion represents one immutable APL snapshot (apl_versions table, §4.2.5).
type APLVersion struct {
	ID              uuid.UUID
	ProgramID       uuid.UUID
	BenefitType     string    // OTC|FOD|CMB
	VersionNumber   int       // monotonically increasing per (program_id, benefit_type)
	RuleCount       int
	S3Key           string    // fis-exchange S3 object key for the APL file
	UploadedAt      time.Time
	UploadedBy      string    // service name or operator identity
	FISAckReceivedAt *time.Time // nil until FIS confirms receipt
	Notes           *string
}
