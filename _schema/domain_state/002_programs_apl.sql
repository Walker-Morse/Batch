-- APL tables: apl_versions and apl_rules (§4.2.5, §4.2.6)
-- Source of truth: OneFin_DDL_Reference_v1_0.docx
-- programs table is in 000_programs.sql (must execute first).

-- ─── apl_versions ────────────────────────────────────────────────────────────
-- Immutable once created. NEVER update or delete rows.
-- Activating a new APL = update apl_rules.active_version_id only.
CREATE TABLE public.apl_versions (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id          UUID NOT NULL REFERENCES public.programs(id),
    benefit_type        TEXT NOT NULL
                        CHECK (benefit_type IN ('OTC','FOD','CMB')),
    version_number      INTEGER NOT NULL,
    rule_count          INTEGER NOT NULL,
    s3_key              TEXT NOT NULL,       -- fis-exchange S3 object key for the APL file
    uploaded_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    uploaded_by         TEXT NOT NULL,       -- service name or operator identity
    fis_ack_received_at TIMESTAMPTZ,         -- NULL until FIS confirms receipt
    notes               TEXT,
    UNIQUE (program_id, benefit_type, version_number)
);

CREATE INDEX ON public.apl_versions (program_id, benefit_type);

COMMENT ON TABLE public.apl_versions IS
'Immutable once created. Never update or delete rows.
Complete audit trail of every APL version uploaded to FIS.';

-- ─── apl_rules ───────────────────────────────────────────────────────────────
-- Active APL rule pointer per (program_id, benefit_type).
-- Atomically updating active_version_id activates a new APL without touching prior version.
-- RFU: restriction_levels = ["mcc","merchant_id","upc"] — strictest configuration (§3.2).
CREATE TABLE public.apl_rules (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    program_id          UUID NOT NULL REFERENCES public.programs(id),
    benefit_type        TEXT NOT NULL
                        CHECK (benefit_type IN ('OTC','FOD','CMB')),
    active_version_id   UUID NOT NULL REFERENCES public.apl_versions(id),
    restriction_levels  JSONB NOT NULL DEFAULT '[]',
                        -- array: ["mcc","merchant_id","upc"]
    mcc_group           INTEGER,  -- FIS MCCGroup restriction ID
    iias_group          INTEGER,  -- FIS IIASGroup; required when upc restriction active
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (program_id, benefit_type)
);

CREATE INDEX ON public.apl_rules (program_id);
CREATE INDEX ON public.apl_rules (active_version_id);

COMMENT ON COLUMN public.apl_rules.active_version_id IS
'Atomic pointer to the currently active APL version.
Update this to activate a new APL. Prior apl_versions row is never touched.';

COMMENT ON COLUMN public.apl_rules.restriction_levels IS
'RFU Phase 1: ["mcc","merchant_id","upc"] — all three levels (§3.2, BRD 3/6/2026).
Confirm per client during program setup — do not assume RFU config applies globally.';
