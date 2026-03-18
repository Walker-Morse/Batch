# ADR-005 — AWS CDK as Infrastructure-as-Code Tooling

**Status:** ACCEPTED  
**Date:** 2026-03-10 (resolved)  
**Decider:** Kyle Walker / Morse DevOps team

## Context

AWS CDK is the confirmed IaC standard for the Morse DevOps portfolio. This decision was
made by the Morse DevOps team prior to One Fintech development. One Fintech conforms
to this decision as a portfolio standard.

## Decision

Use AWS CDK for all One Fintech infrastructure provisioning. Construct structure:
`infra/environments/{env}` + `infra/constructs/{networking, ecs, storage, iam, aurora, scheduler}`.

One Fintech partners with the Morse DevOps team on CDK construct patterns.

## Alternatives Considered

- **Terraform** — not selected. Not the Morse DevOps portfolio standard.
- **AWS SAM** — not considered. Scope is broader than serverless-only.

## Consequences

- CDK constructs replace all infrastructure references throughout the codebase.
- Infrastructure changes go through same PR review process as application code.
- **CONSTRAINT**: ACU max for Aurora Serverless v2 must be explicitly set in CDK.
  Do not rely on the default — insufficient for 200K-row sequential ingest workloads
  under multi-client concurrency.
- **CONSTRAINT**: CDK bootstrapping (CloudFormation bootstrap stack, S3 asset bucket,
  ECR repository, IAM roles) must be configured per AWS account before first deploy.
