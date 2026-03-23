-- Typed batch record tables (§4.1.2, §4.1.3, §4.1.4)
-- Source of truth: OneFin_DDL_Reference_v1_0.docx
-- FK REFERENCES enforced at DB layer (matches DDL reference exactly).
--
-- Three tables replace the generic batch_records:
--   batch_records_rt30 — RT30: new account / card issuance
--   batch_records_rt37 — RT37: card status update
--   batch_records_rt60 — RT60: purse load / fund transfer
--
-- batch_records_common view unions shared columns for pipeline monitoring.

-- ─── RT30: New Account / Card Issuance ──────────────────────────────────────
-- fis_person_id, fis_cuid, fis_card_id NULL until Stage 7 RT30 reconciliation.
-- raw_payload: PHI — original SRG row; required for replay; never log.
CREATE TABLE public.batch_records_rt30 (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_file_id       UUID NOT NULL REFERENCES public.batch_files(id),
    domain_command_id   UUID NOT NULL REFERENCES public.domain_commands(id),
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
    at_code             TEXT,
    -- SRG310 member demographics (PHI — §5.4.1)
    client_member_id    TEXT NOT NULL,
    subprogram_id       NUMERIC(10),
    package_id          TEXT,
    first_name          VARCHAR(50)     NOT NULL, -- PHI; required by FIS RT30
    last_name           VARCHAR(50)     NOT NULL, -- PHI; required by FIS RT30
    date_of_birth       DATE            NOT NULL, -- PHI; required by FIS RT30
    address_1           VARCHAR(100)    NOT NULL, -- PHI; required by FIS RT30
    address_2           VARCHAR(100),
    city                VARCHAR(50)     NOT NULL, -- required by FIS RT30
    state               CHAR(2)         NOT NULL, -- required by FIS RT30
    zip                 VARCHAR(10)     NOT NULL, -- required by FIS RT30
    phone_number        BIGINT          NOT NULL DEFAULT 0, -- PHI; digits only; 0 when not provided
    email               VARCHAR(100),   -- PHI
    card_design_id      TEXT,
    custom_card_id      VARCHAR(50),
    -- FIS return fields (populated Stage 7)
    fis_person_id       VARCHAR(20),    -- NULL until Stage 7
    fis_cuid            VARCHAR(19),    -- NULL until Stage 7
    fis_card_id         VARCHAR(19),    -- NULL until Stage 7; opt-in required (Selvi Marappan)
    raw_payload         JSONB NOT NULL, -- PHI; required for replay; never log
    UNIQUE (batch_file_id, sequence_in_file)
);

CREATE INDEX ON public.batch_records_rt30 (tenant_id, status);
CREATE INDEX ON public.batch_records_rt30 (client_member_id, tenant_id);
CREATE INDEX ON public.batch_records_rt30 (domain_command_id);
CREATE INDEX ON public.batch_records_rt30 (correlation_id);

COMMENT ON COLUMN public.batch_records_rt30.fis_card_id IS
'OPT-IN REQUIRED (Selvi Marappan). NULL until FIS configuration enabled.
Primary XTRACT join key once enabled. Falls back to fis_proxy_number.';

COMMENT ON COLUMN public.batch_records_rt30.raw_payload IS
'PHI present. Required for dead letter replay. Never log at any level (§6.5.2, §7.2).';

COMMENT ON COLUMN public.batch_records_rt30.phone_number IS
'PHI. Digits only, no formatting. 0 when not provided by MCO. Confirm FIS RT30 field offset with Kendra (Open Item #9).';

-- ─── RT37: Card Status Update ────────────────────────────────────────────────
CREATE TABLE public.batch_records_rt37 (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_file_id       UUID NOT NULL REFERENCES public.batch_files(id),
    domain_command_id   UUID NOT NULL REFERENCES public.domain_commands(id),
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
    fis_card_id         VARCHAR(19) NOT NULL,
    card_status_code    SMALLINT NOT NULL,
    reason_code         TEXT,
    raw_payload         JSONB NOT NULL,
    UNIQUE (batch_file_id, sequence_in_file)
);

CREATE INDEX ON public.batch_records_rt37 (tenant_id, status);
CREATE INDEX ON public.batch_records_rt37 (fis_card_id);
CREATE INDEX ON public.batch_records_rt37 (domain_command_id);
CREATE INDEX ON public.batch_records_rt37 (correlation_id);

-- ─── RT60: Purse Load / Fund Transfer ────────────────────────────────────────
CREATE TABLE public.batch_records_rt60 (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_file_id       UUID NOT NULL REFERENCES public.batch_files(id),
    domain_command_id   UUID NOT NULL REFERENCES public.domain_commands(id),
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
    at_code             TEXT,
    client_member_id    TEXT NOT NULL,
    fis_card_id         VARCHAR(19) NOT NULL,
    fis_purse_number    SMALLINT,
    purse_name          VARCHAR(7),
    amount_cents        BIGINT NOT NULL,
    effective_date      DATE NOT NULL,
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
AT30 sweep MUST complete before this date — hard contractual requirement, not a guideline.';

-- ─── batch_records_common view ───────────────────────────────────────────────
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
