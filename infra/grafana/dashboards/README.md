# Grafana Dashboard Definitions

Dashboard JSON files exported from the live Grafana instance.
These are the source of truth — changes should be made here and deployed,
not edited directly in the Grafana UI.

## Dashboards

| File | UID | Title |
|------|-----|-------|
| `onefintech-home.json`   | `onefintech-home`   | One Fintech Platform — Overview |
| `onefintech-ops.json`    | `onefintech-ops`    | Pipeline Operations             |
| `onefintech-uat.json`    | `onefintech-uat`    | UAT Test Validation             |
| `onefintech-files.json`  | `onefintech-files`  | File Manifest                   |
| `onefintech-tests.json`  | `onefintech-tests`  | Test Runs                       |
| `onefintech-detail.json` | `onefintech-detail` | Detail View (drill-through)     |
| `onefintech-file.json`    | `onefintech-file`    | File Detail (per-file viewer)   |

## How provisioning works

At container startup Grafana reads `/etc/grafana/provisioning/dashboards/`.
The CDK construct writes a `dashboards.yaml` provider config and an S3 bucket
holds the dashboard JSON files. The Grafana task downloads them via an
init container (`aws s3 sync`) into the provisioning directory before
Grafana starts.

## Updating a dashboard

1. Make changes in the Grafana UI
2. Export: Dashboard settings → JSON Model → copy
3. Replace the relevant `.json` file here
4. Commit and push — CI deploys automatically
