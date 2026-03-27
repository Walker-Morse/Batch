# Smoke Test Option B — Integration against live DEV environment

**Status:** Not yet implemented  
**Owner:** Kyle Walker  

## What this is

Option B exercises the full pipeline against the live DEV Aurora instance and S3 buckets,
using real AWS credentials. It complements Option A (in-process, zero infrastructure) by
catching real SQL behaviour, real S3 I/O, and real PGP decrypt/encrypt.

## Prerequisites

- AWS credentials with access to account 307871782435 (available — cdk-deployer)
- Aurora DEV instance running (running — onefintech-dev-proxy.proxy-cehsk0igwsbz.us-east-1.rds.amazonaws.com)
- S3 buckets provisioned (provisioned — onefintech-dev-inbound-raw-placeholder etc.)

## Why it hasn't been implemented yet

Option A smoke test covers wiring regressions. Integration tests against real infra
require a dedicated test harness that seeds known-good data, runs the pipeline, and
asserts DB state — that work hasn't been prioritised yet.

## Implementation notes

When implementing, use the RDS Data API (boto3) for DB inspection — it avoids VPC
connectivity requirements from the test runner. The cdk-deployer credentials have the
necessary permissions.
