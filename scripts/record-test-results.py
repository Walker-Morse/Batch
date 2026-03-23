#!/usr/bin/env python3
"""
record-test-results.py
======================
Reads `go test -json` output from stdin and writes to the three-table
test observability schema: test_sets, test_runs, test_steps.

Flow:
  1. Parse go test -json events into per-test results
  2. Upsert each test into test_sets (canonical inventory)
  3. Insert one test_runs row (the run header)
  4. Insert one test_steps row per test (the leaf records)
  5. Update test_runs aggregates (total/passed/failed/skipped/duration/status)

Usage (CI):
    go test -json -count=1 -race -timeout 120s ./... 2>&1 | \\
        python3 scripts/record-test-results.py

Usage (smoke):
    go test -json -tags smoke -run TestSmoke ./_cmd/ingest-task/ 2>&1 | \\
        python3 scripts/record-test-results.py --suite smoke

Flags:
    --suite     unit|smoke|integration  (default: unit)
    --dry-run   print rows, no DB write
    --input     file path               (default: stdin)

Environment:
    DB_HOST, DB_PORT, DB_NAME, DB_USER, DB_PASSWORD
    CI_RUN_ID, CI_SHA, CI_BRANCH
"""

import json, os, subprocess, sys, argparse, time
from datetime import datetime, timezone

# ── args ──────────────────────────────────────────────────────────────────────
parser = argparse.ArgumentParser()
parser.add_argument("--suite",   default="unit",
                    choices=["unit","smoke","integration"])
parser.add_argument("--dry-run", action="store_true")
parser.add_argument("--input",   default="-")
args = parser.parse_args()

# ── environment ───────────────────────────────────────────────────────────────
def git(cmd):
    try:
        return subprocess.check_output(cmd, shell=True, text=True).strip()
    except Exception:
        return "unknown"

RUN_ID    = os.environ.get("CI_RUN_ID",  os.environ.get("GITHUB_RUN_ID",
            f"local-{int(time.time())}"))
COMMIT    = os.environ.get("CI_SHA",     os.environ.get("GITHUB_SHA",
            git("git rev-parse HEAD")))
BRANCH    = os.environ.get("CI_BRANCH",  os.environ.get("GITHUB_REF_NAME",
            git("git rev-parse --abbrev-ref HEAD")))
RUN_AT    = datetime.now(timezone.utc).isoformat()

DB_HOST   = os.environ.get("DB_HOST",   "onefintech-dev-proxy.proxy-cehsk0igwsbz.us-east-1.rds.amazonaws.com")
DB_PORT   = os.environ.get("DB_PORT",   "5432")
DB_NAME   = os.environ.get("DB_NAME",   "onefintech")
DB_USER   = os.environ.get("DB_USER",   "ingest_task")
DB_PASS   = os.environ.get("DB_PASSWORD", "")

# ── parse go test -json ───────────────────────────────────────────────────────
steps   = {}   # (package, test_name) -> result dict
outputs = {}   # (package, test_name) -> [output lines]
t_start = None
t_end   = None

src = open(args.input) if args.input != "-" else sys.stdin
for raw in src:
    raw = raw.strip()
    if not raw:
        continue
    try:
        ev = json.loads(raw)
    except json.JSONDecodeError:
        continue

    action  = ev.get("Action", "")
    pkg     = ev.get("Package", "")
    test    = ev.get("Test", "")
    elapsed = ev.get("Elapsed")
    output  = ev.get("Output", "")

    if not test:
        continue

    key = (pkg, test)

    if action == "output":
        outputs.setdefault(key, []).append(output)

    elif action in ("pass", "fail", "skip"):
        steps[key] = {
            "package":   pkg,
            "test_name": test,
            "status":    action,
            "elapsed_s": elapsed,
            "output":    "".join(outputs.get(key, [])) if action == "fail" else None,
        }
        if t_start is None:
            t_start = time.time()
        t_end = time.time()

if args.input != "-":
    src.close()

rows = list(steps.values())
total    = len(rows)
passed   = sum(1 for r in rows if r["status"] == "pass")
failed   = sum(1 for r in rows if r["status"] == "fail")
skipped  = sum(1 for r in rows if r["status"] == "skip")
duration = round(t_end - t_start, 4) if t_start and t_end else None
run_status = "failed" if failed > 0 else "passed"

print(f"Parsed {total} tests  pass={passed} fail={failed} skip={skipped}  "
      f"run={RUN_ID}  sha={COMMIT[:8]}  branch={BRANCH}", file=sys.stderr)

if not rows:
    print("No test events — nothing to record.", file=sys.stderr)
    sys.exit(0)

# ── dry run ───────────────────────────────────────────────────────────────────
if args.dry_run:
    print(f"\n{'PKG':40s}  {'TEST':60s}  STATUS    ELAPSED")
    print("-" * 120)
    for r in sorted(rows, key=lambda x: (x["package"], x["test_name"])):
        icon = "✅" if r["status"] == "pass" else ("⏭" if r["status"] == "skip" else "❌")
        pkg_short = r["package"].split("/")[-1]
        print(f"  {icon}  {pkg_short:38s}  {r['test_name']:60s}  {r['status']:8s}  {r['elapsed_s']}s")
    print(f"\nRun: {RUN_ID}  {run_status.upper()}  {total} tests  {duration}s")
    sys.exit(0)

# ── write to Aurora ───────────────────────────────────────────────────────────
try:
    import psycopg2
except ImportError:
    subprocess.check_call([sys.executable, "-m", "pip", "install",
                           "psycopg2-binary", "-q"])
    import psycopg2

try:
    conn = psycopg2.connect(
        host=DB_HOST, port=DB_PORT, dbname=DB_NAME,
        user=DB_USER, password=DB_PASS,
        sslmode="require", connect_timeout=10
    )
except Exception as e:
    print(f"⚠  DB connection failed ({e}) — skipping record.", file=sys.stderr)
    sys.exit(0)   # graceful — don't fail the build

cur = conn.cursor()

# 1. Upsert test_sets — canonical inventory
UPSERT_SET = """
INSERT INTO public.test_sets (package, test_name, suite, first_seen_at, last_seen_at)
VALUES (%s, %s, %s, NOW(), NOW())
ON CONFLICT (package, test_name) DO UPDATE
  SET last_seen_at = NOW(),
      active       = TRUE,
      suite        = EXCLUDED.suite
RETURNING id
"""

set_ids = {}
for r in rows:
    cur.execute(UPSERT_SET, (r["package"], r["test_name"], args.suite))
    set_ids[(r["package"], r["test_name"])] = cur.fetchone()[0]

# 2. Insert test_runs header
INSERT_RUN = """
INSERT INTO public.test_runs
  (run_id, run_at, commit_sha, branch, suite,
   total, passed, failed, skipped, duration_s, status)
VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
ON CONFLICT (run_id) DO UPDATE
  SET total=EXCLUDED.total, passed=EXCLUDED.passed,
      failed=EXCLUDED.failed, skipped=EXCLUDED.skipped,
      duration_s=EXCLUDED.duration_s, status=EXCLUDED.status
RETURNING id
"""
cur.execute(INSERT_RUN, (
    RUN_ID, RUN_AT, COMMIT, BRANCH, args.suite,
    total, passed, failed, skipped, duration, run_status
))
run_uuid = cur.fetchone()[0]

# 3. Insert test_steps — one row per test
INSERT_STEP = """
INSERT INTO public.test_steps
  (test_run_id, test_set_id, package, test_name, status, elapsed_s, output)
VALUES (%s, %s, %s, %s, %s, %s, %s)
"""
for r in rows:
    cur.execute(INSERT_STEP, (
        run_uuid,
        set_ids.get((r["package"], r["test_name"])),
        r["package"], r["test_name"],
        r["status"], r["elapsed_s"], r["output"]
    ))

conn.commit()
cur.close()
conn.close()

print(f"Recorded: run_id={RUN_ID}  run_uuid={run_uuid}  "
      f"{total} steps  {run_status.upper()}", file=sys.stderr)
