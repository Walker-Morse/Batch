-- Role definitions for the One Fintech pipeline (§4.2.7, §5.4.5, §4.4.2)
--
-- ingest_task_role: the ECS Fargate ingest-task service role
--   - INSERT on audit_log (append-only enforcement)
--   - REVOKE UPDATE, DELETE on audit_log (write-once at DB layer — not application convention)
--   - Full CRUD on pipeline state and domain state tables
--   - No access to reporting schema (tableau_ro handles that)
--
-- tableau_ro: read-only Tableau Bridge role (§4.4.2)
--   - SELECT on reporting schema only
--   - No access to operational schema (public)
--   - Connects via RDS Proxy — not directly to Aurora cluster endpoint
--   - Credentials rotated on 90-day cycle via Secrets Manager

-- Provisioned via CDK — do not create manually

-- ingest_task_role
CREATE ROLE ingest_task_role NOLOGIN;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ingest_task_role;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ingest_task_role;

-- audit_log is INSERT-only — REVOKE UPDATE and DELETE explicitly
REVOKE UPDATE, DELETE ON public.audit_log FROM ingest_task_role;

-- tableau_ro: read-only access to reporting schema only
CREATE SCHEMA IF NOT EXISTS reporting;
CREATE ROLE tableau_ro NOLOGIN;
GRANT USAGE ON SCHEMA reporting TO tableau_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO tableau_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA reporting GRANT SELECT ON TABLES TO tableau_ro;

-- tableau_svc: login account that inherits tableau_ro
-- Password from Secrets Manager — rotated on 90-day cycle
CREATE ROLE tableau_svc LOGIN PASSWORD '...' IN ROLE tableau_ro;

COMMENT ON ROLE ingest_task_role IS
'ECS Fargate ingest-task service role. INSERT-only on audit_log enforced here.
This is a DB-layer constraint — not application convention alone (§4.2.7, §6.2).';

COMMENT ON ROLE tableau_ro IS
'Read-only Tableau Bridge role. SELECT on reporting schema only.
No access to public (operational) schema. Connects via RDS Proxy (§4.4.2).';
