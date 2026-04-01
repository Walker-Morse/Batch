-- One Fintech Platform — Backfill: dim_program from public.programs
-- Run once after initial pipeline execution to populate dim_program
-- and patch fact_enrollments.dim_program_sk.
--
-- Idempotent: INSERT uses ON CONFLICT DO NOTHING; UPDATE only touches NULL rows.
-- Applied to DEV: 2026-04-01 (1 row inserted, 169 fact_enrollments patched).
--
-- Re-run safe: ON CONFLICT + WHERE dim_program_sk IS NULL make both ops no-ops
-- if already applied.

-- Step 1: populate dim_program from public.programs
INSERT INTO reporting.dim_program (
    tenant_id,
    fis_client_id,
    fis_program_id,
    fis_subprogram_id,
    subprogram_name,
    fis_pack_id,
    is_active,
    benefit_type,
    ccx_effective_date,
    created_at
)
SELECT
    tenant_id,
    NULLIF(fis_client_id, 0),   -- sentinel 0 -> NULL
    NULLIF(fis_program_id, 0),  -- sentinel 0 -> NULL
    fis_subprogram_id,
    subprogram_name,
    fis_pack_id,
    is_active,
    benefit_type,
    ccx_effective_date,
    NOW()
FROM public.programs
ON CONFLICT (tenant_id, fis_subprogram_id) DO NOTHING;

-- Step 2: back-fill dim_program_sk on fact_enrollments
-- Resolves via dim_member.fis_subprogram_id -> dim_program.fis_subprogram_id
UPDATE reporting.fact_enrollments fe
SET dim_program_sk = dp.dim_program_sk
FROM reporting.dim_member dm
JOIN reporting.dim_program dp
    ON  dp.fis_subprogram_id = dm.fis_subprogram_id
    AND dp.tenant_id          = dm.tenant_id
WHERE fe.dim_member_sk  = dm.member_sk
  AND fe.dim_program_sk IS NULL;
