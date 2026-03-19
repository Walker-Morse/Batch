-- 006_rt30_program_id.sql
-- Adds program_id (FK → programs.id) to batch_records_rt30.
--
-- Stage 3 already resolves programs.id via GetProgramByTenantAndSubprogram.
-- Storing it here closes the data-flow gap: Stage 4 / AssemblerImpl reads
-- program_id from the first staged RT30 row and passes it to
-- fis_sequence.Next() — which requires a programs.id UUID (§6.6.1).
--
-- NOT NULL with a sentinel (uuid_nil) default lets us apply this to existing
-- rows without a data backfill; Stage 3 will write real UUIDs going forward.
-- The sentinel value '00000000-0000-0000-0000-000000000000' is detectable
-- at runtime — assembler will error clearly if it encounters it.

ALTER TABLE public.batch_records_rt30
    ADD COLUMN program_id UUID REFERENCES public.programs(id);

COMMENT ON COLUMN public.batch_records_rt30.program_id IS
'FK to programs.id — resolved by Stage 3 via GetProgramByTenantAndSubprogram.
Required by Stage 4 to key the per-day FIS file sequence counter (§6.6.1).
NULL only on rows inserted before migration 006 (pre-existing DEV test data).';

CREATE INDEX ON public.batch_records_rt30 (program_id);
