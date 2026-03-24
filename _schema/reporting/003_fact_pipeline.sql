-- One Fintech Platform — Pipeline Fact Tables (No XTRACT Dependency)
-- §4.3.6 fact_enrollments, §4.3.7 fact_purse_lifecycle,
-- §4.3.10 fact_reconciliation
--
-- These three tables are buildable entirely from pipeline state
-- (batch_records_*, domain_commands, batch_files) and the RT30/RT60
-- return files. They do NOT depend on FIS Data XTRACT feeds (DM-03).
-- The five pipeline-only reports from §4.4.4 source from these tables
-- plus the dimension tables in 001_dimensions.sql.
--
-- Execution order: 001_dimensions.sql → 002_dim_date_seed.sql → this file.

-- ─── fact_enrollments ────────────────────────────────────────────────────────
-- Written by MartWriter.WriteEnrollmentFact during Stage 3 (Row Processing).
-- Updated by MartWriter.WriteReconciliationFact during Stage 7 after
-- return file reconciliation (processing_status STAGED → ACCEPTED/REJECTED).
-- Grain: one row per (correlation_id, row_sequence_number) — the idempotency
-- key that prevents duplicates on Stage 3 retry.
-- Source: batch_records_rt30/rt37/rt60, domain_commands, batch_files.
--
-- Drives pipeline-only reports:
--   §4.4.4 "Enrollment funnel by client"
--   §4.4.4 "Card issuance status by client"
--   §4.4.4 "FIS failure breakdown"
CREATE TABLE IF NOT EXISTS reporting.fact_enrollments (
    fact_enrollment_sk      BIGSERIAL       PRIMARY KEY,
    -- Dimension foreign keys
    dim_member_sk           BIGINT          NOT NULL
                            REFERENCES reporting.dim_member(member_sk),
    dim_program_sk          BIGINT
                            REFERENCES reporting.dim_program(dim_program_sk),
    date_sk                 INTEGER
                            REFERENCES reporting.dim_date(date_sk),        -- processing date
    -- Degenerate dimensions (no surrogate needed)
    tenant_id               TEXT            NOT NULL,
    correlation_id          UUID            NOT NULL,
    row_sequence_number     INTEGER         NOT NULL,
    -- Event classification
    srg_file_type           TEXT
                            CHECK (srg_file_type IN ('SRG310','SRG315','SRG320')),
    event_type              TEXT            NOT NULL
                            CHECK (event_type IN
                                ('NEW_ENROLLMENT','CHANGE','TERMINATION','RELOAD')),
    fis_record_type         CHAR(4)
                            CHECK (fis_record_type IN ('RT30','RT37','RT60','RT62','RT99')),
    -- FIS result (NULL until Stage 7 reconciliation)
    fis_result_code         TEXT,
    fis_result_message      TEXT,
    -- Pipeline status
    processing_status       TEXT            NOT NULL DEFAULT 'STAGED'
                            CHECK (processing_status IN
                                ('STAGED','ACCEPTED','REJECTED','FAILED')),
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    -- Idempotency key (Stage 3 retry safety)
    UNIQUE (correlation_id, row_sequence_number)
);

CREATE INDEX IF NOT EXISTS fact_enroll_member
    ON reporting.fact_enrollments (dim_member_sk);
CREATE INDEX IF NOT EXISTS fact_enroll_program_date
    ON reporting.fact_enrollments (dim_program_sk, date_sk);
CREATE INDEX IF NOT EXISTS fact_enroll_status
    ON reporting.fact_enrollments (processing_status, fis_record_type);
CREATE INDEX IF NOT EXISTS fact_enroll_corr
    ON reporting.fact_enrollments (correlation_id);

COMMENT ON TABLE reporting.fact_enrollments IS
'Enrollment event fact table. §4.3.6.
Grain: one row per (correlation_id, row_sequence_number).
Written at Stage 3 (status=STAGED); updated at Stage 7 (ACCEPTED/REJECTED).
Source: batch_records_rt30/37/60 via MartWriter.WriteEnrollmentFact.
Powers: enrollment funnel, card issuance status, FIS failure breakdown reports.';

-- ─── fact_purse_lifecycle ────────────────────────────────────────────────────
-- Written by MartWriter.WritePurseLifecycleFact during Stage 3.
-- Tracks initial load, reload, expiry, suspension, and termination of purses.
-- Grain: one row per (correlation_id, row_sequence_number, event_type).
-- balance_before/after_cents sourced from Account Balance XTRACT once
-- DM-03 is resolved — columns present now, NULL until then.
--
-- Drives pipeline-only report:
--   §4.4.4 "Purse lifecycle by benefit period"
CREATE TABLE IF NOT EXISTS reporting.fact_purse_lifecycle (
    fact_purse_sk           BIGSERIAL       PRIMARY KEY,
    -- Dimension foreign keys
    dim_purse_sk            BIGINT
                            REFERENCES reporting.dim_purse(purse_sk),
    dim_member_sk           BIGINT          NOT NULL
                            REFERENCES reporting.dim_member(member_sk),
    date_sk                 INTEGER
                            REFERENCES reporting.dim_date(date_sk),
    -- Degenerate dimensions
    tenant_id               TEXT            NOT NULL,
    correlation_id          UUID            NOT NULL,
    row_sequence_number     INTEGER         NOT NULL,
    -- Event classification
    event_type              TEXT            NOT NULL
                            CHECK (event_type IN
                                ('LOAD','RELOAD','EXPIRE','TERMINATE','SUSPEND','NEW_PERIOD')),
    -- Monetary data
    amount_cents            BIGINT,         -- load/reload amount; NULL for non-monetary events
    -- Balance snapshot (sourced from Account Balance XTRACT — NULL until DM-03 resolved)
    balance_before_cents    BIGINT,         -- closing balance before this event
    balance_after_cents     BIGINT,         -- closing balance after event (MON XTRACT field 32)
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    -- Idempotency key
    UNIQUE (correlation_id, row_sequence_number, event_type)
);

CREATE INDEX IF NOT EXISTS fact_purse_member
    ON reporting.fact_purse_lifecycle (dim_member_sk);
CREATE INDEX IF NOT EXISTS fact_purse_purse_date
    ON reporting.fact_purse_lifecycle (dim_purse_sk, date_sk);
CREATE INDEX IF NOT EXISTS fact_purse_event
    ON reporting.fact_purse_lifecycle (event_type, date_sk);

COMMENT ON TABLE reporting.fact_purse_lifecycle IS
'Purse state transition fact table. §4.3.7.
Grain: one row per (correlation_id, row_sequence_number, event_type).
balance_before/after_cents NULL until DM-03 (XTRACT contract) is resolved.
Powers: purse lifecycle by benefit period report.';

-- ─── fact_reconciliation ─────────────────────────────────────────────────────
-- Written by MartWriter.WriteReconciliationFact during Stage 7.
-- One row per return file record matched against a staged batch_record.
-- Grain: one row per (batch_file_id, row_sequence_number).
-- Primary source for the reconciliation pass-rate dashboard (Kendra Williams
-- MVP requirement, per §4.3.10).
--
-- Drives pipeline-only report:
--   §4.4.4 "Batch pipeline latency"
--   §4.4.4 "FIS failure breakdown" (reconciliation view)
CREATE TABLE IF NOT EXISTS reporting.fact_reconciliation (
    fact_recon_sk           BIGSERIAL       PRIMARY KEY,
    -- Dimension foreign keys
    dim_member_sk           BIGINT
                            REFERENCES reporting.dim_member(member_sk),
    date_sk                 INTEGER
                            REFERENCES reporting.dim_date(date_sk),        -- reconciliation date
    -- Degenerate dimensions
    tenant_id               TEXT            NOT NULL,
    correlation_id          UUID            NOT NULL,
    batch_file_id           UUID            NOT NULL,
    row_sequence_number     INTEGER,
    -- FIS return data
    fis_result_code         TEXT,
    fis_result_message      TEXT,
    fis_record_type         CHAR(4)
                            CHECK (fis_record_type IN ('RT30','RT37','RT60','RT62','RT99')),
    -- Reconciliation outcome
    reconciliation_status   TEXT            NOT NULL
                            CHECK (reconciliation_status IN ('MATCHED','UNMATCHED','ERROR')),
    -- Timing (latency report)
    file_arrived_at         TIMESTAMPTZ,    -- batch_files.received_at (denormalised for latency)
    stage3_completed_at     TIMESTAMPTZ,    -- batch_files.stage3_completed_at
    batch_submitted_at      TIMESTAMPTZ,    -- batch_files.submitted_at
    return_file_received_at TIMESTAMPTZ,    -- batch_files.return_received_at
    reconciled_at           TIMESTAMPTZ     NOT NULL DEFAULT NOW(),
    -- Idempotency key
    UNIQUE (batch_file_id, row_sequence_number)
);

CREATE INDEX IF NOT EXISTS fact_recon_corr
    ON reporting.fact_reconciliation (correlation_id);
CREATE INDEX IF NOT EXISTS fact_recon_status_date
    ON reporting.fact_reconciliation (reconciliation_status, date_sk);
CREATE INDEX IF NOT EXISTS fact_recon_member
    ON reporting.fact_reconciliation (dim_member_sk)
    WHERE dim_member_sk IS NOT NULL;
CREATE INDEX IF NOT EXISTS fact_recon_result_code
    ON reporting.fact_reconciliation (fis_result_code, fis_record_type)
    WHERE reconciliation_status = 'MATCHED';

COMMENT ON TABLE reporting.fact_reconciliation IS
'Return file reconciliation fact table. §4.3.10.
Grain: one row per (batch_file_id, row_sequence_number).
Primary source for reconciliation pass-rate dashboard (Kendra Williams MVP).
Timing columns (file_arrived_at → reconciled_at) power batch latency report.
Source: Stage 7 return file via MartWriter.WriteReconciliationFact.';
