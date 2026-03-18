-- Domain state tables: consumers, cards, purses (§4.2)
-- These are the DDD aggregates. consumers is the aggregate root.
-- All PHI fields annotated per §5.4.1.

-- ─── consumers ───────────────────────────────────────────────────────────────
-- Consumer aggregate root. One row per enrolled member per tenant.
-- Natural key: (client_member_id, tenant_id) — sourced from SRG310.
-- fis_person_id and fis_cuid are NULL until Stage 7 RT30 reconciliation.
CREATE TABLE public.consumers (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               TEXT NOT NULL,
    client_member_id        TEXT NOT NULL,   -- health plan member ID (PHI)
    status                  TEXT NOT NULL DEFAULT 'ACTIVE'
                            CHECK (status IN ('ACTIVE','SUSPENDED','TERMINATED','TRANSFERRED')),
    -- FIS-assigned (populated Stage 7 RT30 reconciliation)
    fis_person_id           VARCHAR(20),     -- NULL until Stage 7
    fis_cuid                VARCHAR(19),     -- NULL until Stage 7
    -- Demographics (PHI — HIPAA 45 CFR § 164.312)
    first_name              VARCHAR(50) NOT NULL,   -- PHI
    last_name               VARCHAR(50) NOT NULL,   -- PHI
    date_of_birth           DATE NOT NULL,          -- PHI
    address_1               VARCHAR(100) NOT NULL,  -- PHI
    address_2               VARCHAR(100),           -- PHI
    city                    VARCHAR(50) NOT NULL,   -- PHI
    state                   CHAR(2) NOT NULL,       -- PHI
    zip                     VARCHAR(10) NOT NULL,   -- PHI
    email                   VARCHAR(100),           -- PHI
    -- Program linkage
    program_id              UUID NOT NULL,   -- FK → programs.id
    subprogram_id           NUMERIC(10) NOT NULL, -- FIS SubprogramId
    contract_pbp            VARCHAR(20),     -- Plan Benefit Package (SRG310)
    custom_card_id          VARCHAR(50),
    -- Source tracking
    source_batch_file_id    UUID NOT NULL,   -- FK → batch_files.id
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (client_member_id, tenant_id)
);

CREATE INDEX ON public.consumers (tenant_id, status);
CREATE INDEX ON public.consumers (fis_person_id) WHERE fis_person_id IS NOT NULL;
CREATE INDEX ON public.consumers (program_id);

-- ─── cards ───────────────────────────────────────────────────────────────────
-- One row per physical card issued. A consumer may have at most one active card
-- at a time; replacement cards produce a new row with the prior card closed.
--
-- fis_card_id is the preferred XTRACT join key — OPT-IN REQUIRED (Selvi Marappan).
-- Morse UAT is NOT currently opted in. Field is empty on all UAT NONMON rows.
-- Full PAN is NEVER stored. pan_masked = first 6 + last 4 only.
CREATE TABLE public.cards (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               TEXT NOT NULL,
    consumer_id             UUID NOT NULL,   -- FK → consumers.id
    client_member_id        TEXT NOT NULL,   -- PHI — denormalised for query convenience
    -- FIS-assigned (populated Stage 7 RT30)
    -- OPT-IN REQUIRED for fis_card_id (Selvi Marappan)
    fis_card_id             VARCHAR(19),     -- NULL until opt-in enabled
    pan_masked              VARCHAR(19),     -- first 6 + last 4; full PAN NEVER stored
    fis_proxy_number        VARCHAR(30),
    -- Card state
    card_status             SMALLINT NOT NULL DEFAULT 1
                            CHECK (card_status IN (1,2,4,6,7)),
                            -- 1=Ready 2=Active 4=Lost 6=Suspended 7=Closed
    card_design_id          TEXT,
    package_id              TEXT,
    -- Dates
    issued_at               TIMESTAMPTZ,     -- set when RT30 return confirms issuance
    activated_at            TIMESTAMPTZ,
    expired_at              TIMESTAMPTZ,
    closed_at               TIMESTAMPTZ,
    -- Source tracking
    source_batch_file_id    UUID NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON public.cards (tenant_id, card_status);
CREATE INDEX ON public.cards (consumer_id);
CREATE INDEX ON public.cards (fis_card_id) WHERE fis_card_id IS NOT NULL;
CREATE INDEX ON public.cards (client_member_id, tenant_id);

COMMENT ON COLUMN public.cards.fis_card_id IS
'OPT-IN REQUIRED (Selvi Marappan). NULL until FIS configuration is enabled.
Primary XTRACT join key once enabled. Join strategy falls back to pan_masked proxy until then.';

COMMENT ON COLUMN public.cards.pan_masked IS
'First 6 + last 4 digits only. Full PAN is NEVER stored anywhere in this system.';

-- ─── purses ──────────────────────────────────────────────────────────────────
-- One row per card per benefit period per benefit type.
-- fis_purse_number NULL until Stage 7 RT60 return.
-- purse_type is GENERATED: LEFT(fis_purse_name, 3) — primary Tableau filter key.
--
-- CRITICAL: expiry_date = contractual 11:59 PM ET last day of month (SOW §2.1, §3.3).
-- The AT30 period-end sweep MUST complete before this date.
-- Late submission = contract breach. This is NOT an operational guideline.
--
-- available_balance_cents is the One Fintech sub-ledger view (§I.2).
-- Must be reconciled against MVB FBO account balance daily (Open Item #44).
CREATE TABLE public.purses (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id               TEXT NOT NULL,
    card_id                 UUID NOT NULL,   -- FK → cards.id
    consumer_id             UUID NOT NULL,   -- FK → consumers.id (denormalised)
    client_member_id        TEXT NOT NULL,
    -- FIS purse identifiers
    fis_purse_number        SMALLINT,        -- NULL until Stage 7 RT60 return
    fis_purse_name          VARCHAR(7) NOT NULL, -- ACC PurseName e.g. OTC2550, FOD2550
    purse_type              CHAR(3) GENERATED ALWAYS AS (LEFT(fis_purse_name, 3)) STORED,
    -- Purse state
    status                  TEXT NOT NULL DEFAULT 'PENDING'
                            CHECK (status IN ('PENDING','ACTIVE','EXPIRED','TRANSFERRED','CLOSED')),
    available_balance_cents BIGINT NOT NULL DEFAULT 0, -- One Fintech sub-ledger; reconcile vs XTRACT + FBO
    -- Benefit period
    benefit_period          TEXT NOT NULL,   -- ISO YYYY-MM
    effective_date          DATE NOT NULL,
    -- expiry_date: 11:59 PM ET last day of month — hard contractual deadline (SOW §2.1)
    expiry_date             DATE NOT NULL,
    -- APL linkage
    program_id              UUID NOT NULL,   -- FK → programs.id
    benefit_type            TEXT NOT NULL    -- OTC|FOD|CMB; routes APL enforcement
                            CHECK (benefit_type IN ('OTC','FOD','CMB')),
    -- Dates
    activated_at            TIMESTAMPTZ,
    expired_at              TIMESTAMPTZ,
    closed_at               TIMESTAMPTZ,
    source_batch_file_id    UUID NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON public.purses (tenant_id, status);
CREATE INDEX ON public.purses (card_id);
CREATE INDEX ON public.purses (consumer_id);
CREATE INDEX ON public.purses (fis_purse_number, tenant_id) WHERE fis_purse_number IS NOT NULL;
CREATE INDEX ON public.purses (benefit_period, tenant_id);

COMMENT ON COLUMN public.purses.expiry_date IS
'HARD CONTRACTUAL DEADLINE: 11:59 PM Eastern Time on last day of calendar month (SOW §2.1).
AT30 period-end sweep MUST complete before this date.
Late submission is a contract breach, not a processing delay.
Stage 4 derives sweep target from this column — not from any approximate schedule.';

COMMENT ON COLUMN public.purses.available_balance_cents IS
'One Fintech authoritative sub-ledger view. Must equal the FBO-backed balance.
Reconcile daily against MVB FBO account balance (Addendum I, Open Item #44).
Updated at domain_commands write time — not deferred until FIS return file.';
