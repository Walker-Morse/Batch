package versioning_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/walker-morse/batch/approved_products/versioning"
)

var ctx = context.Background()

// ── CreateVersion ─────────────────────────────────────────────────────────────

func TestMockAPLRepo_CreateVersion_AssignsVersionNumber(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	programID := uuid.New()

	v := &versioning.APLVersion{
		ID:          uuid.New(),
		ProgramID:   programID,
		BenefitType: "OTC",
		RuleCount:   42,
		S3Key:       "apl/rfu-oregon/OTC/v1.txt",
		UploadedAt:  time.Now().UTC(),
		UploadedBy:  "apl-uploader",
	}

	if err := repo.CreateVersion(ctx, v); err != nil {
		t.Fatalf("CreateVersion: %v", err)
	}

	// Mock assigns VersionNumber = len(existing)+1 = 1
	if v.VersionNumber != 1 {
		t.Errorf("VersionNumber = %d; want 1", v.VersionNumber)
	}
	if len(repo.Versions) != 1 {
		t.Errorf("Versions len = %d; want 1", len(repo.Versions))
	}
}

func TestMockAPLRepo_CreateVersion_SequentialVersionNumbers(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	programID := uuid.New()

	for i := 0; i < 3; i++ {
		v := &versioning.APLVersion{
			ID:          uuid.New(),
			ProgramID:   programID,
			BenefitType: "FOD",
			RuleCount:   10 + i,
			S3Key:       "apl/rfu-oregon/FOD/v.txt",
			UploadedAt:  time.Now().UTC(),
			UploadedBy:  "apl-uploader",
		}
		if err := repo.CreateVersion(ctx, v); err != nil {
			t.Fatalf("CreateVersion iter %d: %v", i, err)
		}
		if v.VersionNumber != i+1 {
			t.Errorf("iter %d: VersionNumber = %d; want %d", i, v.VersionNumber, i+1)
		}
	}
}

func TestMockAPLRepo_CreateVersion_ReturnsInjectedError(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	repo.CreateVersionErr = errors.New("db unavailable")

	err := repo.CreateVersion(ctx, &versioning.APLVersion{ID: uuid.New()})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(repo.Versions) != 0 {
		t.Errorf("Versions should be empty on error, got %d", len(repo.Versions))
	}
}

// ── ActivateVersion ───────────────────────────────────────────────────────────

func TestMockAPLRepo_ActivateVersion_SetsActiveVersionID(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	programID := uuid.New()
	versionID := uuid.New()

	if err := repo.ActivateVersion(ctx, programID, "OTC", versionID); err != nil {
		t.Fatalf("ActivateVersion: %v", err)
	}

	key := programID.String() + "|OTC"
	got, ok := repo.ActiveVersionID[key]
	if !ok {
		t.Fatal("ActiveVersionID not set")
	}
	if got != versionID {
		t.Errorf("ActiveVersionID = %s; want %s", got, versionID)
	}
}

func TestMockAPLRepo_ActivateVersion_ReturnsInjectedError(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	repo.ActivateVersionErr = errors.New("update failed")

	err := repo.ActivateVersion(ctx, uuid.New(), "OTC", uuid.New())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ── GetActiveVersion ──────────────────────────────────────────────────────────

func TestMockAPLRepo_GetActiveVersion_ReturnsSeededVersion(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	programID := uuid.New()

	seeded := &versioning.APLVersion{
		ID:            uuid.New(),
		ProgramID:     programID,
		BenefitType:   "OTC",
		VersionNumber: 3,
		RuleCount:     100,
		S3Key:         "apl/rfu-oregon/OTC/v3.txt",
		UploadedAt:    time.Now().UTC(),
		UploadedBy:    "apl-uploader",
	}
	repo.SeedActive[programID.String()+"|OTC"] = seeded

	got, err := repo.GetActiveVersion(ctx, programID, "OTC")
	if err != nil {
		t.Fatalf("GetActiveVersion: %v", err)
	}
	if got.ID != seeded.ID {
		t.Errorf("ID = %s; want %s", got.ID, seeded.ID)
	}
	if got.VersionNumber != 3 {
		t.Errorf("VersionNumber = %d; want 3", got.VersionNumber)
	}
}

func TestMockAPLRepo_GetActiveVersion_ReturnsErrNoActiveVersion_WhenNotSeeded(t *testing.T) {
	repo := versioning.NewMockAPLRepo()

	_, err := repo.GetActiveVersion(ctx, uuid.New(), "OTC")
	if !errors.Is(err, versioning.ErrNoActiveVersion) {
		t.Errorf("err = %v; want ErrNoActiveVersion", err)
	}
}

func TestMockAPLRepo_GetActiveVersion_ReturnsInjectedError(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	repo.GetActiveVersionErr = errors.New("connection reset")

	_, err := repo.GetActiveVersion(ctx, uuid.New(), "FOD")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Must NOT return ErrNoActiveVersion — injected errors are distinct
	if errors.Is(err, versioning.ErrNoActiveVersion) {
		t.Error("injected error should not equal ErrNoActiveVersion")
	}
}

// ── GetActiveVersion returns a copy (immutability) ───────────────────────────

func TestMockAPLRepo_GetActiveVersion_ReturnsCopy(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	programID := uuid.New()

	seeded := &versioning.APLVersion{
		ID:          uuid.New(),
		ProgramID:   programID,
		BenefitType: "FOD",
		RuleCount:   50,
		UploadedAt:  time.Now().UTC(),
		UploadedBy:  "apl-uploader",
	}
	repo.SeedActive[programID.String()+"|FOD"] = seeded

	got, _ := repo.GetActiveVersion(ctx, programID, "FOD")

	// Mutating the returned value must not affect the seeded value
	got.RuleCount = 999
	if seeded.RuleCount == 999 {
		t.Error("GetActiveVersion returned a pointer to the seeded value, not a copy")
	}
}

// ── BenefitType isolation ─────────────────────────────────────────────────────

func TestMockAPLRepo_BenefitTypes_AreIsolated(t *testing.T) {
	repo := versioning.NewMockAPLRepo()
	programID := uuid.New()

	otcID := uuid.New()
	fodID := uuid.New()

	repo.SeedActive[programID.String()+"|OTC"] = &versioning.APLVersion{
		ID: otcID, ProgramID: programID, BenefitType: "OTC",
		UploadedAt: time.Now(), UploadedBy: "test",
	}
	repo.SeedActive[programID.String()+"|FOD"] = &versioning.APLVersion{
		ID: fodID, ProgramID: programID, BenefitType: "FOD",
		UploadedAt: time.Now(), UploadedBy: "test",
	}

	otc, err := repo.GetActiveVersion(ctx, programID, "OTC")
	if err != nil {
		t.Fatalf("OTC: %v", err)
	}
	fod, err := repo.GetActiveVersion(ctx, programID, "FOD")
	if err != nil {
		t.Fatalf("FOD: %v", err)
	}

	if otc.ID != otcID {
		t.Errorf("OTC ID = %s; want %s", otc.ID, otcID)
	}
	if fod.ID != fodID {
		t.Errorf("FOD ID = %s; want %s", fod.ID, fodID)
	}
	if otc.ID == fod.ID {
		t.Error("OTC and FOD versions should have different IDs")
	}
}
