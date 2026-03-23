#!/usr/bin/env python3
"""
record-test-results.py
======================
Reads `go test -json` output from stdin (or a file), parses it into
per-test rows, and writes them to the public.test_runs table in Aurora.

Usage (CI):
    go test -json -count=1 -race -timeout 120s ./... 2>&1 | \
        python3 scripts/record-test-results.py

Usage (smoke):
    go test -json -tags smoke -run TestSmoke ./_cmd/ingest-task/ 2>&1 | \
        python3 scripts/record-test-results.py --suite smoke

Environment variables (all optional with defaults):
    DB_HOST      Aurora proxy endpoint
    DB_PORT      5432
    DB_NAME      onefintech
    DB_USER      ingest_task
    DB_PASSWORD  (from env or AWS Secrets Manager)
    CI_RUN_ID    GitHub Actions run ID  (default: local)
    CI_SHA       git commit SHA         (default: git rev-parse HEAD)
    CI_BRANCH    git branch             (default: git rev-parse --abbrev-ref HEAD)
"""

import json
import os
import subprocess
import sys
import argparse
from datetime import datetime, timezone
from typing import Optional

# ── parse args ────────────────────────────────────────────────────────────────
parser = argparse.ArgumentParser()
parser.add_argument("--suite", default="unit", help="Suite label prepended to package name")
parser.add_argument("--dry-run", action="store_true", help="Print rows without writing to DB")
parser.add_argument("--input", default="-", help="Input file (default: stdin)")
args = parser.parse_args()

# ── environment ───────────────────────────────────────────────────────────────
def git(cmd):
    try:
        return subprocess.check_output(cmd, shell=True, text=True).strip()
    except Exception:
        return "unknown"

RUN_ID    = os.environ.get("CI_RUN_ID",  os.environ.get("GITHUB_RUN_ID", "local"))
COMMIT    = os.environ.get("CI_SHA",     os.environ.get("GITHUB_SHA",     git("git rev-parse HEAD")))
BRANCH    = os.environ.get("CI_BRANCH",  os.environ.get("GITHUB_REF_NAME", git("git rev-parse --abbrev-ref HEAD")))
RUN_AT    = datetime.now(timezone.utc).isoformat()

DB_HOST   = os.environ.get("DB_HOST",   "onefintech-dev-proxy.proxy-cehsk0igwsbz.us-east-1.rds.amazonaws.com")
DB_PORT   = os.environ.get("DB_PORT",   "5432")
DB_NAME   = os.environ.get("DB_NAME",   "onefintech")
DB_USER   = os.environ.get("DB_USER",   "ingest_task")
DB_PASS   = os.environ.get("DB_PASSWORD", "")

# ── parse go test -json output ────────────────────────────────────────────────
# Each line is a JSON event. We care about Action=pass/fail/skip with Test set.
# We accumulate output lines per test for failure detail.

tests   = {}   # key: (package, test_name)
outputs = {}   # key: (package, test_name) -> list of output lines

f = open(args.input) if args.input != "-" else sys.stdin

for raw in f:
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
        continue  # package-level event, not a test

    key = (pkg, test)

    if action == "output":
        outputs.setdefault(key, []).append(output)

    elif action in ("pass", "fail", "skip"):
        tests[key] = {
            "run_id":    RUN_ID,
            "run_at":    RUN_AT,
            "commit_sha": COMMIT,
            "branch":    BRANCH,
            "package":   pkg,
            "test_name": test,
            "status":    action,
            "elapsed_s": elapsed,
            "output":    "".join(outputs.get(key, [])) if action == "fail" else None,
        }

if args.input != "-":
    f.close()

rows = list(tests.values())
print(f"Parsed {len(rows)} test results  (run_id={RUN_ID}  sha={COMMIT[:8]}  branch={BRANCH})",
      file=sys.stderr)

if not rows:
    print("No test events found — nothing to record.", file=sys.stderr)
    sys.exit(0)

# ── dry run ───────────────────────────────────────────────────────────────────
if args.dry_run:
    for r in rows:
        icon = "✅" if r["status"] == "pass" else ("⏭" if r["status"] == "skip" else "❌")
        print(f"  {icon} {r['package'].split('/')[-1]:40s}  {r['test_name']:60s}  {r['elapsed_s']}s")
    sys.exit(0)

# ── write to Aurora ───────────────────────────────────────────────────────────
try:
    import psycopg2
except ImportError:
    subprocess.check_call([sys.executable, "-m", "pip", "install", "psycopg2-binary", "-q"])
    import psycopg2

conn = psycopg2.connect(
    host=DB_HOST, port=DB_PORT, dbname=DB_NAME, user=DB_USER, password=DB_PASS,
    sslmode="require", connect_timeout=10
)
cur = conn.cursor()

INSERT = """
INSERT INTO public.test_runs
    (run_id, run_at, commit_sha, branch, package, test_name, status, elapsed_s, output)
VALUES
    (%s, %s, %s, %s, %s, %s, %s, %s, %s)
"""

written = 0
failed  = 0
for r in rows:
    try:
        cur.execute(INSERT, (
            r["run_id"], r["run_at"], r["commit_sha"], r["branch"],
            r["package"], r["test_name"], r["status"],
            r["elapsed_s"], r["output"]
        ))
        written += 1
    except Exception as e:
        print(f"  ⚠ failed to insert {r['test_name']}: {e}", file=sys.stderr)
        failed += 1

conn.commit()
cur.close()
conn.close()

print(f"Wrote {written} rows to test_runs  ({failed} failures)", file=sys.stderr)
if failed > 0:
    sys.exit(1)
