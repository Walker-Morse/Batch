# One Fintech — Batch ETL Pipeline
# Go's ./... excludes directories with leading underscores (_shared, _schema, etc.)
# Use these targets instead of bare `go test ./...`

GOFLAGS := -mod=mod
GONOSUMDB := *

# All packages including _-prefixed support dirs
ALL_PKGS := \
	./member_enrollment/... \
	./benefit_loading/... \
	./card_management/... \
	./approved_products/... \
	./fis_reconciliation/... \
	./dead_letter/... \
	./sanctions_screening/... \
	./fbo_reconciliation/... \
	./_shared/... \
	./_cmd/...

.PHONY: build test test-verbose vet smoke smoke-integration

build:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go build $(ALL_PKGS)

test:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -count=1 -race -timeout 120s $(ALL_PKGS)

test-verbose:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -v $(ALL_PKGS)

vet:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go vet $(ALL_PKGS)

# Option A smoke test — in-process wiring, no infrastructure required.
# Exercises Stages 1–4 with fake dependencies.
# Run before every deployment to catch wiring regressions.
smoke:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -tags smoke -v -run TestSmoke ./_cmd/ingest-task/

# Option B smoke test — integration against live Postgres + S3.
# Requires: docker compose up (see _docs/SMOKE_TEST_OPTION_B.md)
# Status: blocked — awaiting DEV environment confirmation (John Stevens).
smoke-integration:
	@echo "Option B smoke test requires docker compose. See _docs/SMOKE_TEST_OPTION_B.md"
	@echo "Status: blocked — awaiting DEV environment (John Stevens)"
	@exit 1
