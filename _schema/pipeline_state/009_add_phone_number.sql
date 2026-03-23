-- Migration 009: add phone_number to batch_records_rt30
-- Corresponds to SRG310 schema change: pipe-delimited file now includes phone_number column.
-- BIGINT NOT NULL DEFAULT 0 — 0 when MCO does not provide a value (per spec).
-- PHI: digits only, no formatting characters stored.
--
-- This migration is idempotent: IF NOT EXISTS guard prevents re-run failures.
-- Run against DEV immediately; TST/PRD before UAT (May 18, 2026).
--
-- Confirm FIS RT30 fixed-width field offset for phone with Kendra (Open Item #9).

ALTER TABLE public.batch_records_rt30
    ADD COLUMN IF NOT EXISTS phone_number BIGINT NOT NULL DEFAULT 0;

COMMENT ON COLUMN public.batch_records_rt30.phone_number IS
'PHI. Digits only, no formatting. 0 when not provided by MCO. Confirm FIS RT30 field offset with Kendra (Open Item #9).';
