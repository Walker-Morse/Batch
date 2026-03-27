-- domain_commands: idempotency log (§4.1.1)
-- One row per business command attempt.
-- Written BEFORE any domain state mutation — this sequence is mandatory.
--
-- TWO-LAYER IDEMPOTENCY (Option C):
--
-- Layer 1 — UUID short-circuit (idempotency_key column):
--   Caller-supplied UUID, stable across retries of the same HTTP request.
--   Looked up first. If Completed → return cached result. If Accepted → 409.
--   If Failed → fall through to Layer 2. NULL for batch pipeline rows.
--
-- Layer 2 — Composite business-rule dedup:
--   (tenant_id, client_member_id, command_type, benefit_period)
--   Catches cross-caller duplicates: if a different caller with a different
--   idempotency_key attempts the same business operation for the same member
--   in the same period, Layer 2 catches it.
--   Only FAILED commands are excluded — they may be retried.
--
-- command_type values:
--   Batch pipeline:  ENROLL | UPDATE | LOAD | SWEEP | SUSPEND | TERMINATE
--   REST API:        ENROLL_MEMBER | CANCEL_CARD | REPLACE_CARD | LOAD_FUNDS
--
-- NOTE: The original UNIQUE constraint incorrectly included correlation_id,
-- which scoped uniqueness to a single batch file. Corrected here to use
-- tenant_id instead, enforcing cross-file uniqueness at the DB layer.

CREATE TABLE public.domain_commands (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    correlation_id   UUID NOT NULL,
    idempotency_key  UUID,                -- Layer 1: caller-supplied, NULL for batch rows
    tenant_id        TEXT NOT NULL,
    client_member_id TEXT NOT NULL,
    command_type     TEXT NOT NULL
                     CHECK (command_type IN (
                         'ENROLL','UPDATE','LOAD','SWEEP','SUSPEND','TERMINATE',
                         'ENROLL_MEMBER','CANCEL_CARD','REPLACE_CARD','LOAD_FUNDS'
                     )),
    benefit_period   TEXT NOT NULL,       -- ISO YYYY-MM; empty string for non-period commands
    status           TEXT NOT NULL DEFAULT 'Accepted'
                     CHECK (status IN ('Accepted','Completed','Failed','Duplicate')),
    batch_file_id    UUID NOT NULL,
    sequence_in_file INTEGER NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at     TIMESTAMPTZ,
    failure_reason   TEXT,

    -- Layer 1: UUID uniqueness (partial — only non-null values)
    CONSTRAINT domain_commands_idempotency_key_unique
        UNIQUE (idempotency_key)
        WHERE idempotency_key IS NOT NULL,

    -- Layer 2: business-rule uniqueness (fixed — correlation_id removed)
    CONSTRAINT domain_commands_business_key_unique
        UNIQUE (tenant_id, client_member_id, command_type, benefit_period)
);

CREATE INDEX ON public.domain_commands (tenant_id, status);
CREATE INDEX ON public.domain_commands (batch_file_id);
CREATE INDEX ON public.domain_commands (client_member_id, tenant_id);
CREATE INDEX ON public.domain_commands (idempotency_key) WHERE idempotency_key IS NOT NULL;

COMMENT ON TABLE public.domain_commands IS
'Two-layer idempotency gate. Layer 1: UUID short-circuit for HTTP retry safety.
Layer 2: composite business-rule dedup for cross-caller/cross-file protection.';

COMMENT ON COLUMN public.domain_commands.idempotency_key IS
'Caller-supplied UUID from X-Idempotency-Key header. NULL for batch pipeline rows.
Partial unique index enforces uniqueness only on non-null values.';

COMMENT ON COLUMN public.domain_commands.correlation_id IS
'Per-request trace ID from X-Correlation-ID. Used for observability only.
Not part of any uniqueness constraint.';
