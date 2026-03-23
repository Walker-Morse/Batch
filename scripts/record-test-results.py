#!/usr/bin/env python3
"""
record-test-results.py
======================
Reads `go test -json` output from stdin and writes to the three-table
test observability schema: test_sets, test_runs, test_steps.

Uses the RDS Data API (boto3) — no VPC access required, works from
GitHub Actions runners. Aurora cluster must have enableDataApi: true.

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
    AWS_REGION                  (default: us-east-1)
    RDS_CLUSTER_ARN             Aurora cluster ARN (for Data API)
    RDS_SECRET_ARN              Secrets Manager ARN for DB credentials
    DB_NAME                     database name (default: onefintech)
    CI_RUN_ID, CI_SHA, CI_BRANCH
"""

import json, os, subprocess, sys, argparse, time
from datetime import datetime, timezone

# ── args ──────────────────────────────────────────────────────────────────────
parser = argparse.ArgumentParser()
parser.add_argument("--suite",   default="unit",
                    choices=["unit", "smoke", "integration"])
parser.add_argument("--dry-run", action="store_true")
parser.add_argument("--input",   default="-")
args = parser.parse_args()

# ── environment ───────────────────────────────────────────────────────────────
def git(cmd):
    try:
        return subprocess.check_output(cmd, shell=True, text=True).strip()
    except Exception:
        return "unknown"

RUN_ID  = os.environ.get("CI_RUN_ID",  os.environ.get("GITHUB_RUN_ID",
          f"local-{int(time.time())}"))
COMMIT  = os.environ.get("CI_SHA",     os.environ.get("GITHUB_SHA",
          git("git rev-parse HEAD")))
BRANCH  = os.environ.get("CI_BRANCH",  os.environ.get("GITHUB_REF_NAME",
          git("git rev-parse --abbrev-ref HEAD")))
RUN_AT  = datetime.now(timezone.utc).isoformat()

AWS_REGION   = os.environ.get("AWS_REGION", "us-east-1")
DB_NAME      = os.environ.get("DB_NAME", "onefintech")

# RDS Data API identifiers — set in CI via GitHub Actions secrets / env
# Falls back to known DEV values for local use.
CLUSTER_ARN = os.environ.get(
    "RDS_CLUSTER_ARN",
    "arn:aws:rds:us-east-1:307871782435:cluster:onefintechdev-auroraclusterd4efe71c-aqigxft0mb87"
)
SECRET_ARN = os.environ.get(
    "RDS_SECRET_ARN",
    "arn:aws:secretsmanager:us-east-1:307871782435:secret:AuroraClusterSecretD25348DD-8IIFZe5Ng6w8"
)

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

rows     = list(steps.values())
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

# ── RDS Data API write ────────────────────────────────────────────────────────
try:
    import boto3
except ImportError:
    subprocess.check_call([sys.executable, "-m", "pip", "install", "boto3", "-q"])
    import boto3

rds = boto3.client("rds-data", region_name=AWS_REGION)

def execute(sql, params=None):
    """Execute a single SQL statement via RDS Data API."""
    kwargs = dict(
        resourceArn=CLUSTER_ARN,
        secretArn=SECRET_ARN,
        database=DB_NAME,
        sql=sql,
        includeResultMetadata=True,
    )
    if params:
        kwargs["parameters"] = params
    return rds.execute_statement(**kwargs)

def param(name, value, type_hint=None):
    """Build an RDS Data API parameter."""
    if value is None:
        return {"name": name, "value": {"isNull": True}}
    if isinstance(value, bool):
        return {"name": name, "value": {"booleanValue": value}}
    if isinstance(value, int):
        return {"name": name, "value": {"longValue": value}}
    if isinstance(value, float):
        return {"name": name, "value": {"doubleValue": value}}
    return {"name": name, "value": {"stringValue": str(value)},
            **({"typeHint": type_hint} if type_hint else {})}

try:
    # ── 1. Upsert test_sets ───────────────────────────────────────────────────
    set_ids = {}
    for r in rows:
        res = execute("""
            INSERT INTO public.test_sets (package, test_name, suite, first_seen_at, last_seen_at)
            VALUES (:pkg, :test_name, :suite, NOW(), NOW())
            ON CONFLICT (package, test_name) DO UPDATE
              SET last_seen_at = NOW(),
                  active       = TRUE,
                  suite        = EXCLUDED.suite
            RETURNING id
        """, [
            param("pkg",       r["package"]),
            param("test_name", r["test_name"]),
            param("suite",     args.suite),
        ])
        set_ids[(r["package"], r["test_name"])] = res["records"][0][0]["stringValue"]

    # ── 2. Insert test_runs header ────────────────────────────────────────────
    res = execute("""
        INSERT INTO public.test_runs
          (run_id, run_at, commit_sha, branch, suite,
           total, passed, failed, skipped, duration_s, status)
        VALUES (:run_id, :run_at, :commit, :branch, :suite,
                :total, :passed, :failed, :skipped, :duration, :status)
        ON CONFLICT (run_id) DO UPDATE
          SET total=EXCLUDED.total, passed=EXCLUDED.passed,
              failed=EXCLUDED.failed, skipped=EXCLUDED.skipped,
              duration_s=EXCLUDED.duration_s, status=EXCLUDED.status
        RETURNING id
    """, [
        param("run_id",   RUN_ID),
        param("run_at",   RUN_AT,   "TIMESTAMP"),
        param("commit",   COMMIT),
        param("branch",   BRANCH),
        param("suite",    args.suite),
        param("total",    total),
        param("passed",   passed),
        param("failed",   failed),
        param("skipped",  skipped),
        param("duration", duration),
        param("status",   run_status),
    ])
    run_uuid = res["records"][0][0]["stringValue"]

    # ── 3. Insert test_steps ──────────────────────────────────────────────────
    # Batch in groups of 50 — Data API has a 1000-param limit per call
    BATCH = 50
    for i in range(0, len(rows), BATCH):
        batch = rows[i:i+BATCH]
        # Build a multi-row INSERT for this batch
        value_clauses = []
        params_list = []
        for j, r in enumerate(batch):
            set_id = set_ids.get((r["package"], r["test_name"]))
            value_clauses.append(
                f"(:run_uuid_{j}, :set_id_{j}, :pkg_{j}, :test_{j}, :status_{j}, :elapsed_{j}, :output_{j})"
            )
            params_list += [
                param(f"run_uuid_{j}", run_uuid,    "UUID"),
                param(f"set_id_{j}",  set_id,      "UUID"),
                param(f"pkg_{j}",     r["package"]),
                param(f"test_{j}",    r["test_name"]),
                param(f"status_{j}",  r["status"]),
                param(f"elapsed_{j}", r["elapsed_s"]),
                param(f"output_{j}",  r["output"]),
            ]
        execute(
            f"INSERT INTO public.test_steps "
            f"(test_run_id, test_set_id, package, test_name, status, elapsed_s, output) "
            f"VALUES {', '.join(value_clauses)}",
            params_list
        )

    print(f"Recorded: run_id={RUN_ID}  run_uuid={run_uuid}  "
          f"{total} steps  {run_status.upper()}", file=sys.stderr)

except Exception as e:
    print(f"⚠  RDS Data API write failed ({e}) — skipping record.", file=sys.stderr)
    sys.exit(0)   # graceful — never fail the build
