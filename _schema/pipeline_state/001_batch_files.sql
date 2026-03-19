-- batch_files: non-repudiation anchor for the pipeline (§4.1)
-- One row per inbound or outbound file.
-- Written BEFORE any processing begins.
-- SHA-256 hashes of encrypted and decrypted file written at Stage 1.
-- Status transitions track the file through all seven ingest-task stages.
-- STALLED and HALTED are legal terminal-pending states.

CREATE TABLE public.batch_files (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    correlation_id   UUID NOT NULL UNIQUE,
    tenant_id        TEXT NOT NULL,
    client_id        TEXT NOT NULL,
    file_type        TEXT NOT NULL
                     CHECK (file_type IN ('SRG310','SRG315','SRG320','RETURN')),
    status           TEXT NOT NULL
                     CHECK (status IN (
                         'RECEIVED','VALIDATING','PROCESSING',
                         'STALLED','HALTED','ASSEMBLED',
                         'TRANSFERRED','COMPLETE')),
    record_count     INTEGER,                      -- set at Stage 2
    malformed_count  INTEGER NOT NULL DEFAULT 0,   -- rows that failed validation
    sha256_encrypted TEXT,                         -- hash of PGP-encrypted file (Stage 1)
    sha256_plaintext TEXT,                         -- hash of decrypted file (Stage 1)
    arrived_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at     TIMESTAMPTZ,                  -- Stage 5 FIS transfer timestamp
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ON public.batch_files (correlation_id);
CREATE INDEX ON public.batch_files (tenant_id, status);

COMMENT ON TABLE public.batch_files IS
'Non-repudiation anchor. Root of FK hierarchy — every batch_record, domain_command,
and dead_letter row traces back here via batch_file_id or correlation_id.';
