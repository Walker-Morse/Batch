-- Test observability schema: three-table design
--
-- test_sets   — canonical inventory of all known tests (what SHOULD exist).
--               Populated by `go test -list` on each push. Gap detection:
--               if a test is in test_sets but not in a test_run's steps, it
--               was not executed and must be explained.
--
-- test_runs   — one row per CI execution (run header).
--               Aggregates: total, passed, failed, skipped, duration.
--
-- test_steps  — one row per test per run (the leaf record).
--               FK to test_runs and test_sets. status + elapsed + output.

-- Drop old flat table if present
DROP TABLE IF EXISTS public.test_runs CASCADE;

-- ── test_sets ─────────────────────────────────────────────────────────────────
-- Canonical registry of every known test function in the codebase.
-- Updated on every push via `go test -list '.*' ./...`
-- A test that disappears from test_sets was deleted or renamed.

CREATE TABLE public.test_sets (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    package         TEXT        NOT NULL,
    test_name       TEXT        NOT NULL,
    suite           TEXT        NOT NULL DEFAULT 'unit'
                    CHECK (suite IN ('unit','smoke','integration')),
    first_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    active          BOOLEAN     NOT NULL DEFAULT TRUE,
    UNIQUE (package, test_name)
);

CREATE INDEX ON public.test_sets (package);
CREATE INDEX ON public.test_sets (suite);
CREATE INDEX ON public.test_sets (active);

COMMENT ON TABLE public.test_sets IS
'Canonical inventory of all known tests. Updated from go test -list on every push.
A test missing from a run but present here is a gap and must be explained.';

-- ── test_runs ─────────────────────────────────────────────────────────────────
-- One row per CI execution. Written at the start of the test run.
-- Aggregates filled in at the end.

CREATE TABLE public.test_runs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          TEXT        NOT NULL UNIQUE,    -- GitHub Actions run ID or 'local-<timestamp>'
    run_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    commit_sha      TEXT        NOT NULL,
    branch          TEXT        NOT NULL,
    suite           TEXT        NOT NULL DEFAULT 'unit'
                    CHECK (suite IN ('unit','smoke','integration')),
    total           INTEGER     NOT NULL DEFAULT 0,
    passed          INTEGER     NOT NULL DEFAULT 0,
    failed          INTEGER     NOT NULL DEFAULT 0,
    skipped         INTEGER     NOT NULL DEFAULT 0,
    duration_s      NUMERIC(10,4),
    status          TEXT        NOT NULL DEFAULT 'running'
                    CHECK (status IN ('running','passed','failed'))
);

CREATE INDEX ON public.test_runs (run_at DESC);
CREATE INDEX ON public.test_runs (commit_sha);
CREATE INDEX ON public.test_runs (status);

COMMENT ON TABLE public.test_runs IS
'One row per CI execution. Aggregates filled on completion. Parent of test_steps.';

-- ── test_steps ────────────────────────────────────────────────────────────────
-- One row per test per run. The leaf record.
-- test_set_id may be NULL for the first run before test_sets is populated.

CREATE TABLE public.test_steps (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    test_run_id     UUID        NOT NULL REFERENCES public.test_runs(id) ON DELETE CASCADE,
    test_set_id     UUID        REFERENCES public.test_sets(id),
    package         TEXT        NOT NULL,
    test_name       TEXT        NOT NULL,
    status          TEXT        NOT NULL
                    CHECK (status IN ('pass','fail','skip')),
    elapsed_s       NUMERIC(10,4),
    output          TEXT        -- captured only on failure
);

CREATE INDEX ON public.test_steps (test_run_id);
CREATE INDEX ON public.test_steps (test_set_id);
CREATE INDEX ON public.test_steps (package, test_name);
CREATE INDEX ON public.test_steps (status) WHERE status != 'pass';

COMMENT ON TABLE public.test_steps IS
'One row per test per run. FK to test_runs (header) and test_sets (inventory).
Gaps: tests in test_sets with no corresponding step for a given run were not executed.';
