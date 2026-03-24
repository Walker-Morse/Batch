-- Migration 011: rename batch_files status TRANSFERRED → SUBMITTED
-- TRANSFERRED implied a processor-specific transport mechanism (SFTP).
-- SUBMITTED is processor-agnostic: the file has been deposited for pickup.
-- Matches processor_deposit stage naming (stage5_processor_deposit.go).

ALTER TABLE public.batch_files DROP CONSTRAINT IF EXISTS batch_files_status_check;

ALTER TABLE public.batch_files
    ADD CONSTRAINT batch_files_status_check
    CHECK (status IN (
        'PENDING',
        'PROCESSING',
        'ASSEMBLED',
        'SUBMITTED',     -- formerly TRANSFERRED; renamed for processor-agnosticism
        'COMPLETE',
        'STALLED',
        'HALTED',
        'DEAD_LETTERED'
    ));

-- Migrate any existing rows (idempotent)
UPDATE public.batch_files SET status = 'SUBMITTED' WHERE status = 'TRANSFERRED';

COMMENT ON COLUMN public.batch_files.status IS
'Pipeline lifecycle status.
PENDING→PROCESSING→ASSEMBLED→SUBMITTED→COMPLETE (happy path).
SUBMITTED: file deposited to processor-egress S3 bucket; awaiting return file.
STALLED: return file wait timeout exceeded (Stage 6).
HALTED: processor rejected the entire batch via RT99 (Stage 7).
DEAD_LETTERED: file could not be processed; see dead_letter_store.';
