# Smoke Test — Option B: Integration Against Live Dependencies

**Status:** Blocked — awaiting DEV environment confirmation (John Stevens)  
**Prerequisite:** `docker-compose.yml` with Postgres + S3 stub  
**Owner:** Kyle Walker / John Stevens  

---

## What Option B covers that Option A does not

Option A (in-process wiring test) proves the binary wires correctly with fake dependencies.  
Option B proves the binary works against **real SQL** and **real S3 I/O**:

| Concern | Option A | Option B |
|---|---|---|
| Interface wiring correct | ✅ | ✅ |
| No nil pointer panics | ✅ | ✅ |
| Stage sequencing correct | ✅ | ✅ |
| Real SQL constraint violations | ❌ | ✅ |
| Real transaction semantics | ❌ | ✅ |
| Real S3 multipart / SSE | ❌ | ✅ |
| Schema migrations applied correctly | ❌ | ✅ |
| PGP round-trip (decrypt + encrypt) | ❌ | ✅ |
| FIS record byte offsets on real data | ❌ | ✅ |

## Required `docker-compose.yml` services

```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: onefintech_dev
      POSTGRES_USER: ingest_task
      POSTGRES_PASSWORD: dev_password_not_secret
    ports:
      - "5432:5432"
    volumes:
      - ./_schema:/docker-entrypoint-initdb.d:ro

  # Option 1: localstack (full AWS emulation — heavier)
  localstack:
    image: localstack/localstack:3
    environment:
      SERVICES: s3,secretsmanager,kms
    ports:
      - "4566:4566"

  # Option 2: httptest S3 stub (lighter — only needs GetObject/PutObject/HeadObject/DeleteObject)
  # Implemented as a Go httptest.Server in the test file itself — no docker service needed.
  # See _cmd/ingest-task/smoke_integration_test.go (to be written).
```

## Questions to answer before implementing

1. **Which S3 approach?**  
   Localstack is full-featured but adds Docker pull time to CI.  
   An `httptest.NewServer` implementing the S3 subset we use (~80 lines) needs no Docker and runs in the same process. Recommended unless Secrets Manager emulation is also needed.

2. **Schema migration runner?**  
   `_schema/` contains SQL files. Does Postgres container init pick them up via volume mount, or do we need a `migrate` tool call in test setup?  
   Recommended: `testcontainers-go` with `MigrateWithDir("../../_schema")` — handles ordering automatically.

3. **PGP test keys?**  
   Option B can use throwaway RSA keys generated at test time (same pattern as `_adapters/pgp/pgp_test.go`).  
   Keys never touch Secrets Manager in Option B — injected directly into config.

4. **Is there a `testcontainers-go` policy?**  
   This adds a test dependency. Confirm with John Stevens before adding.

## Proposed test structure once unblocked

```
_cmd/ingest-task/
  main.go              ← existing (refactored with PipelineDeps/runWithDeps)
  smoke_test.go        ← Option A (this PR, build tag: smoke)
  smoke_integration_test.go  ← Option B (build tag: smoke_integration)
```

Run Option B:
```bash
# Requires docker compose up
go test -tags smoke_integration ./... -v -run TestSmokeIntegration
# or:
make smoke-integration
```

## Makefile targets to add

```makefile
smoke:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -tags smoke -v -run TestSmoke ./_cmd/ingest-task/

smoke-integration:
	docker compose up -d postgres
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -tags smoke_integration -v -run TestSmokeIntegration ./_cmd/ingest-task/
	docker compose down
```
