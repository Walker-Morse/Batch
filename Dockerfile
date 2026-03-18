# One Fintech ingest-task — multi-stage build
# Go 1.25.8 (upgraded from 1.22 — 7 CVEs resolved per commit dcf6f44a)
FROM golang:1.25.8-alpine AS builder

RUN apk add --no-cache ca-certificates git

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /ingest-task ./_cmd/ingest-task

# ── Runtime — minimal scratch image ──────────────────────────────────────────
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /ingest-task /ingest-task

# Batch task container — no exposed ports, no HTTP server
ENTRYPOINT ["/ingest-task"]
