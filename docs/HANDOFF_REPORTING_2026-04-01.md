# One Fintech Platform — Reporting Handoff
**Date:** 2026-04-01  
**HEAD:** `a068909` (branch: `main`)  
**Context:** Continuing to add reports in next thread. Captures exact state at end of session.

---

## What Was Built This Session

### XTRACT ETL Pipeline — Full Bug Chain Resolved
Ten commits fixed a 10-deep chain from S3 upload to Aurora row:

| Commit | Fix |
|---|---|
| `da38ef3` | Scratch container has no `/tmp` — replaced `os.CreateTemp` with in-memory `bytes.Reader` |
| `5f37562` | `filelogger.CreateReceived` swallowed all DB errors as "already processed" — surface real errors |
| `aec0201` | `XtractEcsConstruct` used `(cluster as any).vpc` (undefined on `ICluster`) — pass `vpc` explicitly |
| `1468471` | Xtract-loader SG missing from Aurora Proxy SG ingress whitelist — added rule, codified in CDK |
| `56a29d7` | All 6 loaders: NULL dates, wrong column counts, char(1) overflow, UUID cast failures |
| `24c5352` | STDMON `authorization_code` truncated to `varchar(6)` |
| `82e5cd5` | `response_code`/`item_amount`/`event_type_code` stored as NULL when value=0 (nullInt(0)→nil) |
| `516105c` | STDFSID D-record `item_amount`/`item_quantity` off by one field position |
| `a068909` | STDNONMON all field positions wrong — validated against STDNONMON11012025_MORSE.txt |

**Two-pass loader pattern** (used by all feeds, enforced in `_cmd/xtract-loader/main.go`):
1. Phase 1: noop parse to extract `workOfDate` from H record
2. Phase 2: real loader constructed with `(pool, tenantID, sourceFile, workOfDate)`

**Idempotency:** `xtract_file_log.s3_key` is UNIQUE. Same file = no-op. To reload: `DELETE FROM reporting.xtract_file_log WHERE s3_key='...'` then re-upload.

---

## Current DEV State

### GitHub
- **Repo:** Walker-Morse/Batch — **HEAD:** `a068909` — CI 7/7 ✅
- **PAT:** `ghp_REDACTED_SEE_1PASSWORD`

### AWS (Account 307871782435, us-east-1)
| Resource | Value |
|---|---|
| ECS Cluster | `onefintech-dev` |
| Aurora Proxy | `onefintech-dev-proxy.proxy-cehsk0igwsbz.us-east-1.rds.amazonaws.com` |
| Xtract Bucket | `onefintech-dev-xtract` |
| Grafana ALB | `http://onefintech-dev-grafana-445593983.us-east-1.elb.amazonaws.com` |
| API ALB | `http://onefintech-dev-api-721349829.us-east-1.elb.amazonaws.com` |
| Grafana Dashboards S3 | `onefintech-dev-grafana-dashboards` |
| Migration Lambda | `onefintech-dev-db-migrate` |
| DB User / Secret | `ingest_task` / `onefintech/dev/db/ingest-task` |
| Grafana Admin Secret | `onefintech/dev/grafana/admin-password` |
| Xtract-Loader SG | `sg-08e7832a30ce82cef` (whitelisted in Aurora Proxy SG ingress) |
| Ingest-Task SG | `sg-0ac2b341738976645` |
| Aurora Proxy SG | `sg-0e9124670f971d2a0` |
| Private Subnets | `subnet-0149996d8aceb77b9`, `subnet-02f620aaaab77274a` |

**AWS Credentials (cdk-deployer):**  
Key ID: `AKIA_REDACTED_SEE_1PASSWORD` / Secret: `SECRET_REDACTED_SEE_1PASSWORD`

### Pipeline Domain State (rfu-oregon)
| | Count | Status |
|---|---|---|
| batch_files | 25 | 11 COMPLETE, 14 SUBMITTED |
| batch_records_rt30 | 169 | 169 COMPLETED |
| domain_commands | 172 | 169 Completed, 3 Failed (pre-existing) |
| consumers | 172 | 166 with FIS Person IDs |
| cards | 292 | 286 with FIS Card IDs |

---

## Reporting Schema

### Tables With Live Data
```
reporting.dim_member              172   synced from public.consumers
reporting.dim_card                292   synced from public.cards
reporting.fact_enrollments        169   pipeline Stage 3/7
reporting.fact_reconciliation     172   pipeline Stage 7
reporting.fact_monetary           498   STDMON XTRACT
reporting.fact_authorization      395   STDAUTH XTRACT
reporting.fact_account_balance    498   STDACCTBAL XTRACT
reporting.fact_non_monetary       166   STDNONMON XTRACT (card lifecycle events)
reporting.fact_spend_item          99   STDFSID XTRACT (transaction R records)
reporting.fact_spend_item_detail  393   STDFSID XTRACT (item D records)
reporting.dim_subprogram            1   CCX XTRACT (DSPG — subprogram 26071)
reporting.dim_purse_config          3   CCX XTRACT (DPUR — FOD/OTC/CMB)
reporting.dim_package               1   CCX XTRACT (DPKG)
reporting.xtract_file_log           6   non-repudiation log
```

### Tables With DDL But No Data (available for new reports)
```
reporting.fact_card_loads         future: card load events
reporting.fact_purchases          future: purchase detail
reporting.fact_purse_lifecycle    future: purse state transitions
reporting.dim_purse               future: purse master
reporting.dim_program             future: program master
reporting.dim_txn_type            future: transaction type reference
reporting.dim_client_hierarchy    CLIENTHIERARCHY feed (not yet from FIS)
reporting.dim_date                date dimension (unpopulated)
```

### Pre-Built Views (no dashboards yet — good candidates for next thread)
```sql
reporting.vw_auth_decline_summary      -- decline rate by purse, work_of_date
reporting.vw_daily_monetary_summary    -- daily settled txn totals (time-series ready)
reporting.vw_daily_purse_balance       -- daily purse balance snapshots (time-series ready)
reporting.vw_spend_category_summary    -- spend totals by category
```

### Schema Access
`ingest_task` has `SELECT/INSERT/UPDATE` on all reporting tables (DELETE on XTRACT/fact tables).  
`ALTER DEFAULT PRIVILEGES IN SCHEMA reporting GRANT SELECT ON TABLES TO ingest_task` is set — new tables are automatically accessible to Grafana without additional grants.

---

## Grafana Dashboard Inventory

**URL:** `http://onefintech-dev-grafana-445593983.us-east-1.elb.amazonaws.com`  
**Provisioned from S3:** `s3://onefintech-dev-grafana-dashboards/`  
**Repo:** `infra/grafana/dashboards/`

### Report Dashboards (built this session — 9 reports + landing page)
All follow a standard pattern: (ℹ️) info panel → stat tiles (clickable, time-range preserved) → tables.

| UID | Title | Source | Key Content |
|---|---|---|---|
| `onefintech-reports` | Reports (landing) | — | 9 linked cards with audience + description |
| `onefintech-rpt-card-status` | Card Status | `dim_card`, `dim_member` | FIS IDs, card status, activation |
| `onefintech-rpt-enrollment` | Enrollment | `fact_enrollments` | STAGED→ACCEPTED funnel by batch file |
| `onefintech-rpt-reconciliation` | Reconciliation | `fact_reconciliation` | MATCHED/UNMATCHED, match rate |
| `onefintech-rpt-auth` | Auth & Declines | `fact_authorization` | Approval %, decline reasons, total auth $ |
| `onefintech-rpt-utilization` | Utilization | `fact_account_balance`, `fact_monetary` | Balance/spend by purse, utilization % |
| `onefintech-rpt-spend` | Spend Item Detail | `fact_spend_item_detail` | UPC-level spend, top items, category |
| `onefintech-rpt-monetary` | Monetary Transactions | `fact_monetary` | All txn types, purse breakdown |
| `onefintech-rpt-nonmon` | Non-Monetary Events | `fact_non_monetary` | Card lifecycle events by type |
| `onefintech-rpt-ccx` | Client Configuration | `dim_subprogram/purse_config/package` | Purse limits, spend categories, package |

### Stat Tile Standard
- Every stat tile: `fieldConfig.defaults.links` with `targetBlank: false` + `${__from}/${__to}` time preservation
- Dollar tiles: `unit: "currencyUSD", decimals: 2`
- Percentage tiles: plain `unit: "none"`, value computed as `ROUND(...,1)` from SQL
- Table dollar columns: `'$' || TO_CHAR(ROUND(col::numeric,2), 'FM999,999,990.00')`

### Critical: Provisioned Dashboard Rule
Grafana API (`POST /api/dashboards/db`) returns `{"message":"Cannot save provisioned dashboard"}` for all provisioned UIDs. **Do not attempt API saves.** Always:
1. Write JSON to S3: `s3.put_object(Bucket='onefintech-dev-grafana-dashboards', Key='uid.json', Body=body)`
2. Write to repo: `infra/grafana/dashboards/uid.json`
3. Force ECS redeploy: `ecs.update_service(cluster='onefintech-dev', service='onefintech-dev-grafana', forceNewDeployment=True)`
4. Wait ~70s for the new task to be stable (poll `ecs.describe_services` until `len(deployments)==1` and `runningCount==desiredCount`)

---

## How to Add a New Report Dashboard (next thread)

### Copy-Paste Boilerplate
```python
import json, boto3, time

S3  = boto3.client('s3',  region_name='us-east-1', aws_access_key_id='AKIA_REDACTED_SEE_1PASSWORD', aws_secret_access_key='SECRET_REDACTED_SEE_1PASSWORD')
ECS = boto3.client('ecs', region_name='us-east-1', aws_access_key_id='AKIA_REDACTED_SEE_1PASSWORD', aws_secret_access_key='SECRET_REDACTED_SEE_1PASSWORD')

BUCKET   = 'onefintech-dev-grafana-dashboards'
DASH_DIR = '/home/claude/Batch/infra/grafana/dashboards'   # adjust for your env
PG  = {"type":"grafana-postgresql-datasource","uid":"cfglj9j8ghwqob"}
T   = "rfu-oregon"
BASE = "?orgId=1&from=${__from}&to=${__to}"

NAV = [{"title":t,"url":u,"keepTime":True,"asDropdown":False,"icon":"external link",
        "includeVars":False,"tags":[],"targetBlank":False,"type":"link"}
       for t,u in [("Home","/d/onefintech-home/one-fintech-platform"),
                   ("Reports","/d/onefintech-reports/reports"),
                   ("Ops Health","/d/onefintech-ops-health/ops-health")]]

def tgt(sql): return [{"datasource":PG,"format":"table","rawQuery":True,"rawSql":sql,"refId":"A"}]

def stat(uid,title,sql,color,x,y,w=4,h=4,unit="none",decimals=0,link_url=None,link_title="View detail"):
    links = [{"targetBlank":False,"title":link_title,"url":link_url}] if link_url else []
    return {"id":uid,"type":"stat","title":title,"datasource":PG,
            "gridPos":{"x":x,"y":y,"w":w,"h":h},
            "options":{"colorMode":"background","graphMode":"none","justifyMode":"center",
                       "orientation":"auto","textMode":"auto",
                       "reduceOptions":{"calcs":["lastNotNull"],"fields":"","values":False}},
            "fieldConfig":{"defaults":{"color":{"mode":"fixed","fixedColor":color},
                           "unit":unit,"decimals":decimals,"mappings":[],
                           "thresholds":{"mode":"absolute","steps":[{"color":color,"value":None}]},
                           "links":links},"overrides":[]},
            "pluginVersion":"10.4.3","targets":tgt(sql)}

def tbl(uid,title,sql,x,y,w=24,h=10):
    return {"id":uid,"type":"table","title":title,"datasource":PG,
            "gridPos":{"x":x,"y":y,"w":w,"h":h},
            "options":{"cellHeight":"sm","footer":{"show":False},"showHeader":True},
            "fieldConfig":{"defaults":{"custom":{"filterable":True,"displayMode":"auto"}},"overrides":[]},
            "pluginVersion":"10.4.3","targets":tgt(sql)}

def rowp(uid,title,y): return {"id":uid,"type":"row","title":title,
    "gridPos":{"x":0,"y":y,"w":24,"h":1},"collapsed":False,"panels":[]}

def txt(uid,content,y,h=8): return {"id":uid,"type":"text","title":"",
    "gridPos":{"x":0,"y":y,"w":24,"h":h},
    "options":{"content":content,"mode":"markdown"},"pluginVersion":"10.4.3"}

def usd(e): return f"'$' || TO_CHAR(ROUND(({e})::numeric,2),'FM999,999,990.00')"
def pct(e): return f"ROUND(({e})::numeric,1) || '%'"

def dash(uid,title,panels,tags=None):
    return {"uid":uid,"title":title,"tags":tags or ["reporting"],
            "timezone":"browser","schemaVersion":39,"refresh":"10m",
            "time":{"from":"now-90d","to":"now"},"links":NAV,"panels":panels,
            "templating":{"list":[]},"annotations":{"list":[]}}

def write_and_deploy(uid, d):
    body = json.dumps(d, indent=2).encode()
    S3.put_object(Bucket=BUCKET, Key=f'{uid}.json', Body=body, ContentType='application/json')
    open(f'{DASH_DIR}/{uid}.json', 'wb').write(body)
    print(f"Wrote {uid} ({len(d['panels'])} panels)")
    ECS.update_service(cluster='onefintech-dev', service='onefintech-dev-grafana', forceNewDeployment=True)
    time.sleep(70)
    print("Deployed.")
```

### Updating the Reports Landing Page
After adding a new dashboard, add a card to `onefintech-reports`:
```python
# Read current landing page from S3, append a card, re-write
resp = S3.get_object(Bucket=BUCKET, Key='onefintech-reports.json')
landing = json.loads(resp['Body'].read())
max_id = max(p['id'] for p in landing['panels'])
landing['panels'].append({
    "id": max_id+1, "type": "text", "title": "",
    "gridPos": {"x": col, "y": row, "w": 8, "h": 8},
    "options": {"content": f"## [🆕 Title](/d/uid/slug)\n\n**Audience:** ...\n\n...\n\n**Source tables:** `table_name`", "mode": "markdown"},
    "pluginVersion": "10.4.3"
})
S3.put_object(Bucket=BUCKET, Key='onefintech-reports.json', Body=json.dumps(landing,indent=2).encode())
```

---

## Open Items Not Addressed This Session

### XTRACT / Loader
- **CLIENTHIERARCHY** — loader stub logs "not implemented." No FIS feed delivered yet. When it arrives: implement loader → `dim_client_hierarchy`, add dashboard card to Reports landing.
- **`dim_member.activated_at`** — NULL for all records. Activation events from STDNONMON need matching logic to set this.
- **`fact_non_monetary.pan_proxy_number`** — NULL. Real FIS feed may populate differently from test data.

### Pipeline Codebase (unchanged from previous sessions)
- Wire real Aurora adapters (noopCardRepo/noopCommandRepo stubs)
- FIS Code Connect integration (OI #31, John Stevens)
- Cognito JWT auth (auth.go/jwks.go written, not wired)
- RT60 bug fix (5-file change)
- `apl-uploader` (OI #11, Kendra owns spec)
- Deregister ECS oneshot task definitions 1–11 (DB secret exposed in history)

### HLD
- Diagram 2 redraw (stale hexagonal → ETL pipeline)
- EventBridge diagram gaps (8 rules specified, 3 shown)
- APL section expansion
