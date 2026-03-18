-- Typed batch record tables (§4.1.2, §4.1.3, §4.1.4)
-- Three tables replace the generic batch_records:
--   batch_records_rt30 — RT30: new account / card issuance (SRG310 → ENROLL)
--   batch_records_rt37 — RT37: card status update (SRG315 → UPDATE/SUSPEND/TERMINATE)
--   batch_records_rt60 — RT60: purse load / fund transfer (SRG320 → LOAD/SWEEP)
--
-- Each stores structured FIS field content for its record type plus shared status lifecycle.
-- batch_records_common view unions shared columns for pipeline monitoring queries.

-- ─── RT30: New Account / Card Issuance ──────────────────────────────────────
-- fis_person_id, fis_cuid, fis_card_id are NULL until Stage 7 RT30 reconciliation.
-- All demographic fields are PHI per §5.4.1.
CREATE TABLE public.batch_records_rt30 (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_file_id       UUID NOT NULL,   -- FK → batch_files.id
    domain_command_id   UUID NOT NULL,   -- FK → domain_commands.id
    correlation_id      UUID NOT NULL,
    tenant_id           TEXT NOT NULL,
    sequence_in_file    INTEGER NOT NULL,
    status              TEXT NOT NULL DEFAULT 'STAGED'
                        CHECK (status IN ('STAGED','SUBMITTED','COMPLETED','FAILED')),
    staged_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at        TIMESTAMPTZ,     -- set when batch file delivered to FIS (Stage 5)
    completed_at        TIMESTAMPTZ,     -- set on return file reconciliation (Stage 7)
    fis_result_code     TEXT,            -- populated Stage 7
    fis_result_message  TEXT,
    at_code             TEXT,            -- FIS action type code
    -- SRG310 member demographics (PHI — classified per §5.4.1)
    client_member_id    TEXT NOT NULL,
    subprogram_id       NUMERIC(10),     -- FIS SubprogramId e.g. 26071
    package_id          TEXT,
    first_name          VARCHAR(50),     -- PHI
    last_name           VARCHAR(50),     -- PHI
    date_of_birth       DATE,            -- PHI
    address_1           VARCHAR(100),    -- PHI
    address_2           VARCHAR(100),    -- PHI
    city                VARCHAR(50),     -- PHI
    state               CHAR(2),         -- PHI
    zip                 VARCHAR(10),     -- PHI
    email               VARCHAR(100),    -- PHI
    card_design_id      TEXT,
    custom_card_id      VARCHAR(50),
    -- FIS return fields (populated Stage 7)
    fis_person_id       VARCHAR(20),     -- NULL until Stage 7
    fis_cuid            VARCHAR(19),     -- NULL until Stage 7
    fis_card_id         VARCHAR(19),     -- NULL until Stage 7; opt-in required (Selvi Marappan)
    -- Raw record for replay
    raw_payload         JSONB NOT NULL,  -- original SRG row; PHI; required for replay
    UNIQUE (batch_file_id, sequence_in_file)
);

CREATE INDEX ON public.batch_records_rt30 (tenant_id, status);
CREATE INDEX ON public.batch_records_rt30 (client_member_id, tenant_id);
CREATE INDEX ON public.batch_records_rt30 (domain_command_id);
CREATE INDEX ON public.batch_records_rt30 (correlation_id);

COMMENT ON COLUMN public.batch_records_rt30.fis_card_id IS
'OPT-IN REQUIRED: FIS opt-in field. NULL until Selvi Marappan enables fis_card_id
in FIS configuration. Primary XTRACT join key once enabled.';

COMMENT ON COLUMN public.batch_records_rt30.raw_payload IS
'PHI present. Required for dead letter replay. Never log contents.';

-- ─── RT37: Card Status Update ────────────────────────────────────────────────
-- fis_card_id is NOT NULL on this table — required for staging (§4.1.3 notes).
-- A dead-lettered RT37 with NULL fis_card_id means the prior RT30 has not yet
-- completed Stage 7 reconciliation.
CREATE TABLE public.batch_records_rt37 (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_file_id       UUID NOT NULL,
    domain_command_id   UUID NOT NULL,
    correlation_id      UUID NOT NULL,
    tenant_id           TEXT NOT NULL,
    sequence_in_file    INTEGER NOT NULL,
    status              TEXT NOT NULL DEFAULT 'STAGED'
                        CHECK (status IN ('STAGED','SUBMITTED','COMPLETED','FAILED')),
    staged_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at        TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    fis_result_code     TEXT,
    fis_result_message  TEXT,
    client_member_id    TEXT NOT NULL,
    fis_card_id         VARCHAR(19) NOT NULL,  -- required; NULL blocks staging
    card_status_code    SMALLINT NOT NULL,     -- 2=Active 4=Lost 6=Suspended 7=Closed
    reason_code         TEXT,
    raw_payload         JSONB NOT NULL,
    UNIQUE (batch_file_id, sequence_in_file)
);

CREATE INDEX ON public.batch_records_rt37 (tenant_id, status);
CREATE INDEX ON public.batch_records_rt37 (fis_card_id);
CREATE INDEX ON public.batch_records_rt37 (domain_command_id);
CREATE INDEX ON public.batch_records_rt37 (correlation_id);

-- ─── RT60: Purse Load / Fund Transfer ────────────────────────────────────────
-- AT30 (period-end sweep) and AT01 (new period load) are always issued as a
-- correlated pair. Both rows share the same domain_command_id.
-- AT30 MUST complete (Stage 7 COMPLETED) before the paired AT01 is submitted.
-- amount_cents: positive = load, negative = period-end sweep.
CREATE TABLE public.batch_records_rt60 (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_file_id       UUID NOT NULL,
    domain_command_id   UUID NOT NULL,
    correlation_id      UUID NOT NULL,
    tenant_id           TEXT NOT NULL,
    sequence_in_file    INTEGER NOT NULL,
    status              TEXT NOT NULL DEFAULT 'STAGED'
                        CHECK (status IN ('STAGED','SUBMITTED','COMPLETED','FAILED')),
    staged_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at        TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    fis_result_code     TEXT,
    fis_result_message  TEXT,
    at_code             TEXT,           -- AT01=load, AT30=sweep
    client_member_id    TEXT NOT NULL,
    fis_card_id         VARCHAR(19) NOT NULL,
    fis_purse_number    SMALLINT,       -- NULL until Stage 7 RT60 return
    purse_name          VARCHAR(7),     -- ACC PurseName e.g. OTC2550, FOD2550
    amount_cents        BIGINT NOT NULL, -- positive=load, negative=sweep
    effective_date      DATE NOT NULL,
    -- expiry_date: contractual 11:59 PM ET last day of month (SOW §2.1, §3.3)
    expiry_date         DATE,
    client_reference    TEXT,
    raw_payload         JSONB NOT NULL,
    UNIQUE (batch_file_id, sequence_in_file)
);

CREATE INDEX ON public.batch_records_rt60 (tenant_id, status);
CREATE INDEX ON public.batch_records_rt60 (fis_card_id);
CREATE INDEX ON public.batch_records_rt60 (domain_command_id);
CREATE INDEX ON public.batch_records_rt60 (correlation_id);

COMMENT ON COLUMN public.batch_records_rt60.expiry_date IS
'Contractual 11:59 PM ET last day of month (SOW §2.1).
AT30 period-end sweep MUST complete before this date — hard contractual requirement.
Late submission = contract breach.';

-- ─── batch_records_common view ───────────────────────────────────────────────
-- Operators should query this view rather than joining the three tables directly.
-- Used by pipeline monitoring queries, enrollment funnel dashboard, FIS failure report.
CREATE OR REPLACE VIEW public.batch_records_common AS
SELECT id, batch_file_id, domain_command_id, correlation_id, tenant_id,
       sequence_in_file, status, staged_at, submitted_at, completed_at,
       fis_result_code, fis_result_message, client_member_id, 'RT30' AS record_type
FROM   public.batch_records_rt30
UNION ALL
SELECT id, batch_file_id, domain_command_id, correlation_id, tenant_id,
       sequence_in_file, status, staged_at, submitted_at, completed_at,
       fis_result_code, fis_result_message, client_member_id, 'RT37' AS record_type
FROM   public.batch_records_rt37
UNION ALL
SELECT id, batch_file_id, domain_command_id, correlation_id, tenant_id,
       sequence_in_file, status, staged_at, submitted_at, completed_at,
       fis_result_code, fis_result_message, client_member_id, 'RT60' AS record_type
FROM   public.batch_records_rt60;
