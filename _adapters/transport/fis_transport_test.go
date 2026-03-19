package transport_test

// Tests for FISTransportAdapter.
//
// PollForReturn: tested with a fake S3Lister (no AWS, no httptest required).
// Deliver SFTP path: error paths only (key parse, dial failure).
//   Full SCP round-trip is an integration concern — needs a live SSH server.
//
// Coverage:
//   - PollForReturn: file found immediately → body returned
//   - PollForReturn: stale file skipped; fresh file returned
//   - PollForReturn: issuance file excluded from return candidates
//   - PollForReturn: context cancelled → error
//   - PollForReturn: timeout → timeout error
//   - PollForReturn: ListObjects error → propagated immediately
//   - PollForReturn: GetObject error → propagated
//   - Deliver: malformed PEM → "parse SFTP private key" error
//   - Deliver: dial fails (no server) → "SSH dial" error

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"

	transport "github.com/walker-morse/batch/_adapters/transport"
)

// ─── fake S3Lister ────────────────────────────────────────────────────────────

type fakeS3 struct {
	listOut *awss3.ListObjectsV2Output
	listErr error
	getBody string
	getErr  error
}

func (f *fakeS3) ListObjectsV2(_ context.Context, _ *awss3.ListObjectsV2Input, _ ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	if f.listOut == nil {
		return &awss3.ListObjectsV2Output{}, nil
	}
	return f.listOut, nil
}

func (f *fakeS3) GetObject(_ context.Context, _ *awss3.GetObjectInput, _ ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &awss3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(f.getBody)),
	}, nil
}

func ts(t time.Time) *time.Time { return &t }
func ks(s string) *string       { return &s }

func newAdapter(s3 transport.S3Lister) *transport.FISTransportAdapter {
	return &transport.FISTransportAdapter{
		S3Client:          s3,
		FISExchangeBucket: "fis-exchange",
		ReturnFilePrefix:  "return/",
		PollInterval:      5 * time.Millisecond,
	}
}

// ─── PollForReturn tests ──────────────────────────────────────────────────────

func TestPollForReturn_FileFoundImmediately(t *testing.T) {
	future := time.Now().Add(time.Minute)
	s3 := &fakeS3{
		listOut: &awss3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: ks("return/MORSEUSA01060126001.return.txt"), LastModified: ts(future)},
			},
		},
		getBody: "FAKE FIS RETURN CONTENT\r\n",
	}
	body, err := newAdapter(s3).PollForReturn(context.Background(), uuid.New(), 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer body.Close()
	got, _ := io.ReadAll(body)
	if !strings.Contains(string(got), "FAKE FIS RETURN CONTENT") {
		t.Errorf("body = %q; want content present", got)
	}
}

func TestPollForReturn_StaleFileSkipped(t *testing.T) {
	past := time.Now().Add(-2 * time.Hour)
	future := time.Now().Add(time.Minute)
	s3 := &fakeS3{
		listOut: &awss3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: ks("return/stale.return.txt"), LastModified: ts(past)},
				{Key: ks("return/fresh.return.txt"), LastModified: ts(future)},
			},
		},
		getBody: "FRESH",
	}
	body, err := newAdapter(s3).PollForReturn(context.Background(), uuid.New(), 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	body.Close()
}

func TestPollForReturn_IssuanceFileExcluded(t *testing.T) {
	// Only the outbound issuance file is present — must be skipped
	future := time.Now().Add(time.Minute)
	s3 := &fakeS3{
		listOut: &awss3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: ks("return/MORSEUSA01060126001.issuance.txt"), LastModified: ts(future)},
			},
		},
	}
	_, err := newAdapter(s3).PollForReturn(context.Background(), uuid.New(), 30*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout — issuance file must not satisfy return file condition")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestPollForReturn_ContextCancelledImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newAdapter(&fakeS3{}).PollForReturn(ctx, uuid.New(), 5*time.Second)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestPollForReturn_Timeout(t *testing.T) {
	_, err := newAdapter(&fakeS3{}).PollForReturn(context.Background(), uuid.New(), 20*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error = %q; want 'timeout'", err)
	}
}

func TestPollForReturn_ListErrorPropagated(t *testing.T) {
	s3 := &fakeS3{listErr: fmt.Errorf("S3 unavailable")}
	_, err := newAdapter(s3).PollForReturn(context.Background(), uuid.New(), 5*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ListObjectsV2") {
		t.Errorf("error = %q; want 'ListObjectsV2'", err)
	}
}

func TestPollForReturn_GetObjectErrorPropagated(t *testing.T) {
	future := time.Now().Add(time.Minute)
	s3 := &fakeS3{
		listOut: &awss3.ListObjectsV2Output{
			Contents: []types.Object{
				{Key: ks("return/MORSEUSA.return.txt"), LastModified: ts(future)},
			},
		},
		getErr: fmt.Errorf("access denied"),
	}
	_, err := newAdapter(s3).PollForReturn(context.Background(), uuid.New(), 5*time.Second)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fetch return file") {
		t.Errorf("error = %q; want 'fetch return file'", err)
	}
}

// ─── Deliver error path tests ─────────────────────────────────────────────────

func TestDeliver_MalformedPrivateKey(t *testing.T) {
	a := &transport.FISTransportAdapter{
		SFTPHost:       "localhost",
		SFTPUser:       "fis",
		SFTPPrivateKey: []byte("not a valid PEM key"),
	}
	err := a.Deliver(context.Background(), strings.NewReader("body"), "test.issuance.txt")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "parse SFTP private key") {
		t.Errorf("error = %q; want 'parse SFTP private key'", err)
	}
}

func TestDeliver_DialFails(t *testing.T) {
	pemKey, err := generateRSAPrivateKeyPEM(t)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	a := &transport.FISTransportAdapter{
		SFTPHost:       "127.0.0.1",
		SFTPUser:       "fis",
		SFTPPrivateKey: pemKey,
		SFTPPort:       19922, // nothing listening
	}
	err = a.Deliver(context.Background(), strings.NewReader("body"), "test.issuance.txt")
	if err == nil {
		t.Fatal("expected error when no SSH server listening")
	}
	if !strings.Contains(err.Error(), "SSH dial") {
		t.Errorf("error = %q; want 'SSH dial'", err)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// generateRSAPrivateKeyPEM creates a throwaway RSA-2048 key in PKCS#1 PEM format.
// Only for unit tests — never use outside test code.
func generateRSAPrivateKeyPEM(t *testing.T) ([]byte, error) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("rsa.GenerateKey: %w", err)
	}
	b := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return pem.EncodeToMemory(b), nil
}
