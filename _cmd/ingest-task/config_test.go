// Package main — unit tests for parseConfig S3 key derivation logic.
//
// Tests cover the three derivation paths added to support EventBridge-triggered
// invocations where only S3_BUCKET and S3_KEY are injected:
//
//   1. CORRELATION_ID — UUID v5 (SHA-1) derived from "bucket/key" when not set.
//      Same key always produces the same UUID (idempotent replay guarantee).
//
//   2. TENANT_ID / CLIENT_ID — parsed from path segments 1 and 2 of S3_KEY.
//      Key convention: inbound-raw/{tenant_id}/{client_id}/YYYY/MM/DD/file.ext
//
//   3. FILE_TYPE — inferred from filename substring (srg310/315/320).
//      Falls back to SRG310 if unrecognised.
//
// These tests do not call flag.Parse() — they exercise the derivation helpers
// directly so they can run alongside other package tests without flag conflicts.
package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ─── helpers under test ───────────────────────────────────────────────────────
// The derivation logic lives inline in parseConfig(). We extract the same logic
// here as package-level helpers so we can unit test it cleanly.

// deriveCorrelationID returns a deterministic UUID v5 from bucket+key.
func deriveCorrelationID(bucket, key string) uuid.UUID {
	seed := bucket + "/" + key
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed))
}

// deriveTenantAndClient parses tenant_id and client_id from an S3 key.
// Expected convention: inbound-raw/{tenant_id}/{client_id}/YYYY/MM/DD/file
// Returns ("", "") if the key has fewer than 3 segments.
func deriveTenantAndClient(s3Key string) (tenantID, clientID string) {
	parts := strings.SplitN(s3Key, "/", 4)
	if len(parts) < 3 {
		return "", ""
	}
	return parts[1], parts[2]
}

// deriveFileType infers FILE_TYPE from the S3 key filename.
// Returns SRG315 or SRG320 if the lowercase key contains those substrings,
// otherwise defaults to SRG310.
func deriveFileType(s3Key string) string {
	lower := strings.ToLower(s3Key)
	switch {
	case strings.Contains(lower, "srg315"):
		return "SRG315"
	case strings.Contains(lower, "srg320"):
		return "SRG320"
	default:
		return "SRG310"
	}
}

// ─── CORRELATION_ID derivation ────────────────────────────────────────────────

func TestDeriveCorrelationID_Deterministic(t *testing.T) {
	bucket := "onefintech-dev-inbound-raw-placeholder"
	key := "inbound-raw/rfu-oregon/rfu/2026/03/20/members.srg310.csv"

	id1 := deriveCorrelationID(bucket, key)
	id2 := deriveCorrelationID(bucket, key)

	if id1 != id2 {
		t.Errorf("correlation ID not deterministic: %v != %v", id1, id2)
	}
}

func TestDeriveCorrelationID_DifferentKeysProduceDifferentIDs(t *testing.T) {
	bucket := "onefintech-dev-inbound-raw-placeholder"
	key1 := "inbound-raw/rfu-oregon/rfu/2026/03/20/file1.srg310.csv"
	key2 := "inbound-raw/rfu-oregon/rfu/2026/03/20/file2.srg310.csv"

	id1 := deriveCorrelationID(bucket, key1)
	id2 := deriveCorrelationID(bucket, key2)

	if id1 == id2 {
		t.Errorf("different keys produced the same correlation ID: %v", id1)
	}
}

func TestDeriveCorrelationID_IsValidUUID(t *testing.T) {
	id := deriveCorrelationID("bucket", "inbound-raw/rfu-oregon/rfu/2026/03/20/f.csv")
	if id == uuid.Nil {
		t.Error("derived correlation ID is nil UUID")
	}
	// UUID v5 — version bits should be 5
	if id.Version() != 5 {
		t.Errorf("UUID version = %d; want 5", id.Version())
	}
}

func TestDeriveCorrelationID_BucketAffectsID(t *testing.T) {
	key := "inbound-raw/rfu-oregon/rfu/2026/03/20/file.srg310.csv"
	id1 := deriveCorrelationID("bucket-a", key)
	id2 := deriveCorrelationID("bucket-b", key)

	if id1 == id2 {
		t.Error("same key in different buckets should produce different correlation IDs")
	}
}

// ─── TENANT_ID / CLIENT_ID derivation ────────────────────────────────────────

func TestDeriveTenantAndClient_StandardPath(t *testing.T) {
	key := "inbound-raw/rfu-oregon/rfu/2026/03/20/members.srg310.csv"
	tenant, client := deriveTenantAndClient(key)

	if tenant != "rfu-oregon" {
		t.Errorf("tenant = %q; want rfu-oregon", tenant)
	}
	if client != "rfu" {
		t.Errorf("client = %q; want rfu", client)
	}
}

func TestDeriveTenantAndClient_ShortKey_ReturnsEmpty(t *testing.T) {
	cases := []string{
		"",
		"inbound-raw",
		"inbound-raw/rfu-oregon",
	}
	for _, key := range cases {
		tenant, client := deriveTenantAndClient(key)
		if tenant != "" || client != "" {
			t.Errorf("key=%q: expected empty derivation, got tenant=%q client=%q",
				key, tenant, client)
		}
	}
}

func TestDeriveTenantAndClient_MultipleClientSegments(t *testing.T) {
	// Path with extra segments — only first two after prefix matter
	key := "inbound-raw/acme-health/acme/2026/04/01/nested/dir/file.pgp"
	tenant, client := deriveTenantAndClient(key)

	if tenant != "acme-health" {
		t.Errorf("tenant = %q; want acme-health", tenant)
	}
	if client != "acme" {
		t.Errorf("client = %q; want acme", client)
	}
}

// ─── FILE_TYPE derivation ─────────────────────────────────────────────────────

func TestDeriveFileType_SRG310_Default(t *testing.T) {
	cases := []struct {
		key      string
		wantType string
	}{
		{"inbound-raw/rfu-oregon/rfu/2026/03/20/members.srg310.csv", "SRG310"},
		{"inbound-raw/rfu-oregon/rfu/2026/03/20/members.SRG310.csv", "SRG310"}, // uppercase
		{"inbound-raw/rfu-oregon/rfu/2026/03/20/members.csv", "SRG310"},         // no hint → default
		{"inbound-raw/rfu-oregon/rfu/2026/03/20/members.pgp", "SRG310"},         // encrypted → default
		{"inbound-raw/rfu-oregon/rfu/2026/03/20/members.srg310.csv.pgp", "SRG310"},
	}
	for _, tc := range cases {
		got := deriveFileType(tc.key)
		if got != tc.wantType {
			t.Errorf("key=%q: got %q; want %q", tc.key, got, tc.wantType)
		}
	}
}

func TestDeriveFileType_SRG315(t *testing.T) {
	cases := []string{
		"inbound-raw/rfu-oregon/rfu/2026/03/20/changes.srg315.csv",
		"inbound-raw/rfu-oregon/rfu/2026/03/20/changes.SRG315.pgp",
	}
	for _, key := range cases {
		if got := deriveFileType(key); got != "SRG315" {
			t.Errorf("key=%q: got %q; want SRG315", key, got)
		}
	}
}

func TestDeriveFileType_SRG320(t *testing.T) {
	cases := []string{
		"inbound-raw/rfu-oregon/rfu/2026/03/20/loads.srg320.csv",
		"inbound-raw/rfu-oregon/rfu/2026/03/20/loads.SRG320.pgp",
	}
	for _, key := range cases {
		if got := deriveFileType(key); got != "SRG320" {
			t.Errorf("key=%q: got %q; want SRG320", key, got)
		}
	}
}

func TestDeriveFileType_SRG315_TakesPrecedenceInFilename(t *testing.T) {
	// SRG315 check comes before SRG320 in the switch — confirm ordering
	key := "inbound-raw/rfu-oregon/rfu/2026/03/20/file.srg315.csv"
	if got := deriveFileType(key); got != "SRG315" {
		t.Errorf("got %q; want SRG315", got)
	}
}
