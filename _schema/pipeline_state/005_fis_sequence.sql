-- fis_file_sequence: per-day per-program sequence counter for FIS batch filenames (§6.6.1).
-- File naming format: ppppppppmmddccyyss.*.txt
-- Sequence number ss increments per file per calendar day, resets at midnight.
-- FIS SILENTLY DISCARDS duplicate filenames (§6.5.5) — the counter must be durable.
--
-- Usage: call NextFISSequence(program_id, today) to atomically claim the next number.
-- This is safe under concurrent ingest-task containers (ADR-003: currently one per file,
-- but the function is idempotent-safe if that changes).

CREATE TABLE public.fis_file_sequence (
    program_id       UUID NOT NULL REFERENCES public.programs(id),
    sequence_date    DATE NOT NULL,
    sequence_number  INTEGER NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (program_id, sequence_date)
);

COMMENT ON TABLE public.fis_file_sequence IS
'Per-day file sequence counter for FIS batch filenames.
FIS silently discards duplicate filenames (§6.5.5) — this counter prevents collisions.
Sequence resets implicitly by calendar date (new row per day, DEFAULT 0).';

COMMENT ON COLUMN public.fis_file_sequence.sequence_number IS
'Counter starts at 0; NextFISSequence increments THEN returns the new value (1-based).
FIS supports up to 99 files per day per program (2-digit field in filename).
Max value: 99. Callers must reject sequence > 99 and halt the pipeline.';

-- NextFISSequence atomically increments and returns the sequence number for a given
-- program and date. Safe under concurrent callers (FOR UPDATE + single transaction).
-- Returns the new value (1 on first call for a given program+date).
CREATE OR REPLACE FUNCTION public.NextFISSequence(p_program_id UUID, p_date DATE)
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_seq INTEGER;
BEGIN
    INSERT INTO public.fis_file_sequence (program_id, sequence_date, sequence_number, updated_at)
    VALUES (p_program_id, p_date, 1, NOW())
    ON CONFLICT (program_id, sequence_date) DO UPDATE
        SET sequence_number = fis_file_sequence.sequence_number + 1,
            updated_at = NOW()
    RETURNING sequence_number INTO v_seq;
    RETURN v_seq;
END;
$$;

COMMENT ON FUNCTION public.NextFISSequence IS
'Atomic sequence claim for FIS batch filenames.
Uses INSERT ... ON CONFLICT DO UPDATE to avoid lost updates.
Returns the claimed sequence number (1-based, max 99).';
