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
	./card_member_api/... \
	./_shared/... \
	./_cmd/...

.PHONY: build test test-verbose test-record vet smoke smoke-integration smoke-record

build:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go build $(ALL_PKGS)

test:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -count=1 -race -timeout 120s $(ALL_PKGS)

test-verbose:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -v $(ALL_PKGS)

# Run tests, emit JSON, record every result to test_runs table in Aurora.
# Used by CI on main branch pushes. Falls back gracefully if DB is unreachable.
test-record:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) \
	go test -json -count=1 -race -timeout 120s $(ALL_PKGS) 2>&1 | \
	python3 scripts/record-test-results.py || true
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) \
	go test -count=1 -race -timeout 120s $(ALL_PKGS)

vet:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go vet $(ALL_PKGS)

# Option A smoke test — in-process wiring, no infrastructure required.
# Exercises Stages 1–4 with fake dependencies.
# Run before every deployment to catch wiring regressions.
smoke:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -tags smoke -v -run TestSmoke ./_cmd/ingest-task/

# Smoke test with result recording.
smoke-record:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) \
	go test -json -tags smoke -run TestSmoke ./_cmd/ingest-task/ 2>&1 | \
	python3 scripts/record-test-results.py --suite smoke || true
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) \
	go test -tags smoke -v -run TestSmoke ./_cmd/ingest-task/

# Option B smoke test — integration against live Postgres + S3.
# Requires: docker compose up (see _docs/SMOKE_TEST_OPTION_B.md)
smoke-integration:
	@echo "Option B smoke test requires docker compose. See _docs/SMOKE_TEST_OPTION_B.md"
	@exit 1
