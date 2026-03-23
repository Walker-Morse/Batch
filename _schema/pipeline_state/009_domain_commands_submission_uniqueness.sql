-- Fix domain_commands uniqueness constraint.
--
-- The original constraint scoped to (correlation_id, client_member_id,
-- command_type, benefit_period) was wrong. correlation_id is a per-file
-- identifier — scoping to it allowed the same member to be enrolled twice
-- across two different files for the same benefit period.
--
-- The correct boundary is submission to us (the MCO sending us the file).
-- Once we have accepted any file containing a member for a given benefit
-- period, that enrollment is claimed. A second file with the same member
-- and benefit period is a duplicate from the MCO regardless of our
-- internal pipeline state.
--
-- Failed commands are excluded — a member whose command failed may be
-- resubmitted in a corrected file.
--
-- Applied to DEV Aurora 2026-03-23. Existing duplicate test data cleaned
-- up before constraint creation (143 duplicate RT30 rows and domain commands
-- removed, keeping the oldest per member+benefit_period).

ALTER TABLE public.domain_commands
    DROP CONSTRAINT IF EXISTS domain_commands_correlation_id_client_member_id_command_typ_key;

CREATE UNIQUE INDEX IF NOT EXISTS uq_domain_commands_submission
    ON public.domain_commands (tenant_id, client_member_id, command_type, benefit_period)
    WHERE status != 'Failed';
