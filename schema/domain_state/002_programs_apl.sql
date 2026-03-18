-- Program configuration and APL tables (§4.2.4, §4.2.5, §4.2.6)

-- ─── programs ────────────────────────────────────────────────────────────────
-- Program configuration per health plan client. One row per FIS SubprogramId.
-- fis_pack_id and package_id provisioned by Selvi Marappan via ACC upload (Open Item #1).
-- Must be populated before the first card issuance batch can run.
CREATE TABLE public.programs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           TEXT NOT NULL,
    fis_client_id       NUMERIC(10) NOT NULL,
    fis_program_id      NUMERIC(10) NOT NULL,
    fis_subprogram_id   NUMERIC(10) NOT NULL, -- natural key; e.g. 26071 for RFU
    subprogram_name     VARCHAR(80),
    fis_pack_id         TEXT,   -- Selvi Marappan owns provisioning via ACC (Open Item #1)
    package_id          TEXT,   -- populated after ACC upload
    client_name         TEXT NOT NULL, -- e.g. 'Rogue Foods United'
    benefit_type        TEXT NOT NULL
                        CHECK (benefit_type IN ('OTC','FOD','CMB')),
    state               CHAR(2),
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,
    program_start_date  DATE,
    ccx_effective_date  DATE,   -- date CCX XTRACT feed last updated this row
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, fis_subprogram_id)
);

CREATE INDEX ON public.programs (tenant_id, is_active);

COMMENT ON COLUMN public.programs.fis_pack_id IS
'Provisioned by Selvi Marappan via FIS ACC. Required before first card issuance batch.
Open Item #1: PROD ACC upload target March 10, 2026; UAT target March 11, 2026.';

-- ─── apl_versions ────────────────────────────────────────────────────────────
-- Immutable APL snapshot. One row per APL upload event per program per benefit_type.
-- Rows are NEVER updated after creation.
-- Activating a new version means updating apl_rules.active_version_id only.
-- Complete audit trail of every APL uploaded to FIS is preserved.
CREATE TABLE public.apl_versions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id          UUID NOT NULL,   -- FK → programs.id
    benefit_type        TEXT NOT NULL    -- discriminator: OTC|FOD|CMB
                        CHECK (benefit_type IN ('OTC','FOD','CMB')),
    version_number      INTEGER NOT NULL, -- monotonically increasing per (program_id, benefit_type)
    rule_count          INTEGER NOT NULL,
    s3_key              TEXT NOT NULL,   -- fis-exchange S3 object key for the APL file
    uploaded_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_by         TEXT NOT NULL,   -- service name or operator identity
    fis_ack_received_at TIMESTAMPTZ,     -- NULL until FIS confirms receipt
    notes               TEXT,
    UNIQUE (program_id, benefit_type, version_number)
);

CREATE INDEX ON public.apl_versions (program_id, benefit_type);

COMMENT ON TABLE public.apl_versions IS
'Immutable once created. NEVER update or delete rows.
Activating a new APL = update apl_rules.active_version_id only.';

-- ─── apl_rules ───────────────────────────────────────────────────────────────
-- Active APL rule pointer per (program_id, benefit_type).
-- One row per pair — this is the live pointer to the active apl_versions row.
-- Atomically updating active_version_id activates a new APL without deleting the prior version.
--
-- RFU has all three restriction levels simultaneously (BRD 3/6/2026):
--   restriction_levels = '["mcc","merchant_id","upc"]'
-- This is the strictest possible configuration.
-- Do not assume this applies to other clients.
CREATE TABLE public.apl_rules (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id          UUID NOT NULL,   -- FK → programs.id
    benefit_type        TEXT NOT NULL
                        CHECK (benefit_type IN ('OTC','FOD','CMB')),
    active_version_id   UUID NOT NULL,   -- FK → apl_versions.id — live APL pointer
    restriction_levels  JSONB NOT NULL DEFAULT '[]',
                        -- array: ["mcc","merchant_id","upc"]
    mcc_group           INTEGER,         -- FIS MCCGroup restriction ID
    iias_group          INTEGER,         -- FIS IIASGroup; required when upc restriction active
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (program_id, benefit_type)
);

CREATE INDEX ON public.apl_rules (program_id);
CREATE INDEX ON public.apl_rules (active_version_id);

COMMENT ON COLUMN public.apl_rules.active_version_id IS
'Atomic pointer to the currently active APL version.
Update this to activate a new APL. Prior version row in apl_versions is never touched.';

COMMENT ON COLUMN public.apl_rules.restriction_levels IS
'RFU Phase 1: ["mcc","merchant_id","upc"] — strictest configuration (§3.2, BRD 3/6/2026).
Confirm restriction levels per client during program setup — do not assume RFU config applies globally.';
