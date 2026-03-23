-- test_runs: persistent record of every test execution across all CI runs.
-- Populated by scripts/record-test-results.py after each go test -json run.
-- One row per test per run. Enables Grafana test health dashboard.

CREATE TABLE public.test_runs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id          TEXT        NOT NULL,           -- GitHub Actions run ID or 'local'
    run_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    commit_sha      TEXT        NOT NULL,
    branch          TEXT        NOT NULL,
    package         TEXT        NOT NULL,           -- e.g. github.com/walker-morse/batch/member_enrollment/pipeline
    test_name       TEXT        NOT NULL,           -- e.g. TestStage1_ValidFile
    status          TEXT        NOT NULL
                    CHECK (status IN ('pass','fail','skip')),
    elapsed_s       NUMERIC(10,4),                  -- seconds
    output          TEXT                            -- captured test output on failure
);

CREATE INDEX ON public.test_runs (run_id);
CREATE INDEX ON public.test_runs (commit_sha);
CREATE INDEX ON public.test_runs (package, test_name);
CREATE INDEX ON public.test_runs (run_at DESC);
CREATE INDEX ON public.test_runs (status) WHERE status != 'pass';

COMMENT ON TABLE public.test_runs IS
'One row per test per CI run. Written by scripts/record-test-results.py via go test -json output.';
