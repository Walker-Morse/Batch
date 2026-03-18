-- programs: must be created before consumers, purses, apl_versions (FK dependency)
-- Source of truth: OneFin_DDL_Reference_v1_0.docx
--
-- Program configuration per health plan client. One row per FIS SubprogramId.
-- fis_pack_id and package_id provisioned by Selvi Marappan via FIS ACC (Open Item #1).
-- Must be populated before the first card issuance batch can run.
-- benefit_type: OTC (fruits & vegetables, $95/month RFU), FOD (pantry, $250/month RFU), CMB.

CREATE TABLE public.programs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           TEXT NOT NULL,
    fis_client_id       NUMERIC(10) NOT NULL,
    fis_program_id      NUMERIC(10) NOT NULL,
    fis_subprogram_id   NUMERIC(10) NOT NULL, -- natural key; e.g. 26071 for RFU
    subprogram_name     VARCHAR(80),
    fis_pack_id         TEXT,       -- Selvi Marappan owns provisioning via ACC (Open Item #1)
    package_id          TEXT,       -- populated after ACC upload
    client_name         TEXT NOT NULL, -- e.g. 'Rogue Foods United'
    benefit_type        TEXT NOT NULL
                        CHECK (benefit_type IN ('OTC','FOD','CMB')),
    state               CHAR(2),    -- US state code e.g. OR
    is_active           BOOLEAN NOT NULL DEFAULT TRUE,
    program_start_date  DATE,
    ccx_effective_date  DATE,       -- date CCX feed last updated this row
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, fis_subprogram_id)
);

CREATE INDEX ON public.programs (tenant_id, is_active);

COMMENT ON COLUMN public.programs.fis_pack_id IS
'Provisioned by Selvi Marappan via FIS ACC. Required before first card issuance batch.
Open Item #1: PROD ACC upload target Mar 10, 2026; UAT Mar 11.';
