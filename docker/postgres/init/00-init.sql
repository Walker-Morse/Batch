-- Local Docker bootstrap for Postgres.
-- Runs once on first container initialization.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

\i /docker-entrypoint-initdb.d/schema/pipeline_state/001_batch_files.sql
\i /docker-entrypoint-initdb.d/schema/pipeline_state/002_domain_commands.sql
\i /docker-entrypoint-initdb.d/schema/pipeline_state/003_batch_records.sql
\i /docker-entrypoint-initdb.d/schema/pipeline_state/004_dead_letter_store.sql
\i /docker-entrypoint-initdb.d/schema/pipeline_state/005_fis_sequence.sql

\i /docker-entrypoint-initdb.d/schema/domain_state/000_programs.sql
\i /docker-entrypoint-initdb.d/schema/domain_state/001_consumers_cards_purses.sql
\i /docker-entrypoint-initdb.d/schema/domain_state/002_programs_apl.sql
\i /docker-entrypoint-initdb.d/schema/domain_state/003_audit_log.sql

\i /docker-entrypoint-initdb.d/schema/pipeline_state/006_rt30_program_id.sql
