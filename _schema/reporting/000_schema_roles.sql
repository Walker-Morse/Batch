-- One Fintech Platform — Reporting Schema Bootstrap
-- §4.3 Reporting Data Mart / §4.4.2 Read-Only Role
--
-- Execution order: run once before 001_dimensions.sql.
-- Idempotent: CREATE IF NOT EXISTS throughout.
-- DO NOT run in production without a Secrets Manager password rotation
-- for tableau_svc (password placeholder '...' must be replaced by CDK).

-- ─── Schema ──────────────────────────────────────────────────────────────────
CREATE SCHEMA IF NOT EXISTS reporting;

COMMENT ON SCHEMA reporting IS
'Star-schema reporting layer (§4.3). Read path: tableau_svc via RDS Proxy.
Write path: ingest_task via MartWriter interface only. No direct writes
from application code outside the MartWriter contract.';

-- ─── Roles ───────────────────────────────────────────────────────────────────
-- tableau_ro: privilege group (NOLOGIN). Inheritable by any login role.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'tableau_ro') THEN
        CREATE ROLE tableau_ro NOLOGIN;
    END IF;
END
$$;

-- tableau_svc: login account that inherits tableau_ro.
-- Password is managed by Secrets Manager (90-day rotation).
-- CDK provisions this role; password placeholder must not reach production.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'tableau_svc') THEN
        CREATE ROLE tableau_svc LOGIN PASSWORD 'CHANGEME_SECRETS_MANAGER' IN ROLE tableau_ro;
    END IF;
END
$$;

-- ─── Grants ──────────────────────────────────────────────────────────────────
GRANT USAGE ON SCHEMA reporting TO tableau_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO tableau_ro;
-- Default privileges ensure future tables get SELECT automatically.
ALTER DEFAULT PRIVILEGES IN SCHEMA reporting
    GRANT SELECT ON TABLES TO tableau_ro;
-- ingest_task needs INSERT/UPDATE on reporting (MartWriter write path).
GRANT USAGE ON SCHEMA reporting TO ingest_task;
GRANT INSERT, UPDATE ON ALL TABLES IN SCHEMA reporting TO ingest_task;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA reporting TO ingest_task;
ALTER DEFAULT PRIVILEGES IN SCHEMA reporting
    GRANT INSERT, UPDATE ON TABLES TO ingest_task;
ALTER DEFAULT PRIVILEGES IN SCHEMA reporting
    GRANT USAGE, SELECT ON SEQUENCES TO ingest_task;

COMMENT ON ROLE tableau_ro IS
'Read-only Tableau Bridge privilege group. SELECT on reporting schema only.
No access to public (operational) schema. Connects via RDS Proxy (§4.4.2).';

COMMENT ON ROLE tableau_svc IS
'Tableau login account. Inherits tableau_ro. Password rotated 90-day cycle
via Secrets Manager (onefintech/{env}/db/tableau-svc). CDK-provisioned only.';
