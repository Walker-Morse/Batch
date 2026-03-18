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

.PHONY: build test vet lint

build:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go build $(ALL_PKGS)

test:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test $(ALL_PKGS)

test-verbose:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go test -v $(ALL_PKGS)

vet:
	GONOSUMDB=$(GONOSUMDB) GOFLAGS=$(GOFLAGS) go vet $(ALL_PKGS)
