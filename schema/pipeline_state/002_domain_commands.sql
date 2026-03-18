-- domain_commands: idempotency log (§4.1.1)
-- One row per business command attempt.
-- Written in Stage 3 BEFORE any domain state mutation — this sequence is mandatory.
-- A row with status='Accepted' is the idempotency gate:
-- any subsequent attempt with the same composite key returns 'Duplicate'
-- without touching FIS or domain state.
--
-- command_type maps to FIS record types:
--   ENROLL    → RT30
--   UPDATE    → RT37
--   LOAD      → RT60 AT01
--   SWEEP     → RT60 AT30
--   SUSPEND   → RT37 status 6
--   TERMINATE → RT37 status 7

CREATE TABLE public.domain_commands (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    correlation_id   UUID NOT NULL,   -- FK → batch_files.correlation_id
    tenant_id        TEXT NOT NULL,
    client_member_id TEXT NOT NULL,
    command_type     TEXT NOT NULL
                     CHECK (command_type IN ('ENROLL','UPDATE','LOAD','SWEEP','SUSPEND','TERMINATE')),
    benefit_period   TEXT NOT NULL,   -- ISO YYYY-MM e.g. '2026-07'
    status           TEXT NOT NULL DEFAULT 'Accepted'
                     CHECK (status IN ('Accepted','Completed','Failed','Duplicate')),
    batch_file_id    UUID NOT NULL,
    sequence_in_file INTEGER NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at     TIMESTAMPTZ,
    failure_reason   TEXT,
    UNIQUE (correlation_id, client_member_id, command_type, benefit_period)
);

CREATE INDEX ON public.domain_commands (tenant_id, status);
CREATE INDEX ON public.domain_commands (batch_file_id);
CREATE INDEX ON public.domain_commands (client_member_id, tenant_id);

COMMENT ON TABLE public.domain_commands IS
'Idempotency gate. Written before any domain state mutation. Composite UNIQUE prevents
duplicate FIS submissions on file replay.';

COMMENT ON COLUMN public.domain_commands.status IS
'Accepted = idempotency gate is open (write-side CQRS record).
Duplicate = composite key already exists; no processing occurred.';
