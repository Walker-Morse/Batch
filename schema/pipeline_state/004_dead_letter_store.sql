-- dead_letter_store: failed record capture for triage and replay (§4.1, §6.5)
-- Written directly by the ingest-task container on unrecoverable exception.
-- Written SYNCHRONOUSLY before any retry or stage-skip logic executes.
-- Supports triage, replay, and resolution tracking.
--
-- message_body (JSONB): original SRG row — PHI present.
-- Access controlled at DB layer. NEVER log its contents at any log level.
-- failure_reason: structured error code and stage identifier ONLY — no PHI.

CREATE TABLE public.dead_letter_store (
    id                  BIGSERIAL PRIMARY KEY,
    correlation_id      UUID NOT NULL,
    batch_file_id       BIGINT REFERENCES public.batch_files(id),
    row_sequence_number INTEGER,
    tenant_id           TEXT NOT NULL,
    client_member_id    TEXT,           -- NULL if failure is file-level
    failure_stage       TEXT NOT NULL
                        CHECK (failure_stage IN (
                            'validation','row_processing','batch_assembly',
                            'fis_transfer','return_file_wait','reconciliation')),
    failure_reason      TEXT NOT NULL,  -- structured error code ONLY — no PHI
    message_body        JSONB,          -- original SRG row; PHI; access-controlled
    retry_count         INTEGER NOT NULL DEFAULT 0,
    last_retry_at       TIMESTAMPTZ,
    replayed_at         TIMESTAMPTZ,    -- set by replay-cli on successful replay
    resolved_at         TIMESTAMPTZ,    -- NULL until resolved
    resolved_by         TEXT,           -- operator identity or 'replay-cli'
    resolution_notes    TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON public.dead_letter_store (tenant_id, resolved_at);
CREATE INDEX ON public.dead_letter_store (correlation_id);

COMMENT ON TABLE public.dead_letter_store IS
'Capture table for failed pipeline records. Every dead-lettered record represents
a member not processed. Resolved only via replay-cli (Open Item #24) or manual abandonment.';

COMMENT ON COLUMN public.dead_letter_store.message_body IS
'PHI present — original SRG row as JSONB. Access-controlled at DB layer.
Required for replay. Never log this field at any log level (§6.5.2, §7.2).';

COMMENT ON COLUMN public.dead_letter_store.failure_reason IS
'Structured error code and stage identifier ONLY. Must never contain PHI.
Used in CloudWatch metric filters and on-call triage (§6.5.1).';
