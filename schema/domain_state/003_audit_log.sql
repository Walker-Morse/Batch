-- audit_log: append-only compliance record (§4.2.7, §6.2)
-- One row per state transition across all domain and pipeline tables.
-- Write-once: records are inserted, NEVER updated or deleted.
-- Required for HIPAA §164.312(b) audit controls and PCI DSS 4.0 Req 10.2.
--
-- Long-term archival to S3 Glacier required before go-live (Open Item #36).
-- Aurora automated backups (max 35 days) are INSUFFICIENT for the 6-year
-- HIPAA retention requirement (45 CFR § 164.316(b)(2)).
-- An S3 export pipeline with Glacier lifecycle and Object Lock must be
-- designed and implemented before go-live.
--
-- BIGSERIAL (not UUID) — write volume is too high for UUID index efficiency.

CREATE TABLE public.audit_log (
    id               BIGSERIAL PRIMARY KEY,  -- sequential; not UUID (volume)
    tenant_id        TEXT NOT NULL,
    entity_type      TEXT NOT NULL,
                     -- batch_files|batch_records_rt30|consumers|cards|purses|domain_commands
    entity_id        TEXT NOT NULL,          -- UUID of affected row stored as TEXT
    old_state        TEXT,                   -- NULL on INSERT events
    new_state        TEXT NOT NULL,
    changed_by       TEXT NOT NULL,          -- service name e.g. 'ingest-task'
    correlation_id   UUID,
    client_member_id TEXT,                   -- denormalised for compliance query patterns
    fis_result_code  TEXT,                   -- populated when change driven by FIS return file
    notes            TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- No UPDATE or DELETE — enforced via PostgreSQL role permissions at DB layer
-- See schema/roles/ingest_task_role.sql

CREATE INDEX ON public.audit_log (tenant_id, entity_type, created_at);
CREATE INDEX ON public.audit_log (entity_id, entity_type);
CREATE INDEX ON public.audit_log (correlation_id) WHERE correlation_id IS NOT NULL;
CREATE INDEX ON public.audit_log (client_member_id) WHERE client_member_id IS NOT NULL;

COMMENT ON TABLE public.audit_log IS
'Append-only compliance record. INSERT only — enforced at DB layer via role permissions.
Required for HIPAA §164.312(b) and PCI DSS 4.0 Req 10.2.
Long-term archival to S3 Glacier required before go-live (Open Item #36).
6-year HIPAA retention (45 CFR § 164.316(b)(2)) exceeds Aurora backup max (35 days).';
