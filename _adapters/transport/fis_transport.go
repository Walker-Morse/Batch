// Package transport implements ports.FISTransport for SCP.
//
// SCP file exchange uses two distinct paths (§5.1, §5.4.2):
//
//   Outbound (Deliver): ingest-task → SSH/SCP → SCP Transfer Family SFTP endpoint
//     The PGP-encrypted batch file is delivered to SCP via SSH SCP.
//     SFTP credentials (host, port, user, private key) come from Secrets Manager.
//     Host key pinning is required before TST (Open Item #19 — confirm IP with Kendra Williams).
//
//   Inbound (PollForReturn): SCP → S3 → ingest-task
//     SCP deposits the return file into the scp-exchange S3 bucket via AWS Transfer Family.
//     The ingest-task polls S3 using ListObjectsV2 on the return prefix until the file
//     appears (by LastModified > task start time) or the timeout expires.
//     Return file naming suffix must be confirmed against SCP File Processing Spec v1.37
//     before Stage 6 implementation sprint (Open Item #10).
//
// Timeout: 6 hours default — matches SCP SLA (45–60 min per 50K records).
// Confirm with Kendra Williams before TST provisioning (Open Item #25).
package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const (
	// DefaultTimeout is the Stage 6 return file wait timeout (§5.1, Open Item #25).
	DefaultTimeout = 6 * time.Hour
	// DefaultPollInterval is how often Stage 6 checks S3 for the return file.
	DefaultPollInterval = 30 * time.Second
	// DefaultSFTPPort is the standard SSH port.
	DefaultSFTPPort = 22
)

// FISTransportAdapter implements ports.FISTransport.
type FISTransportAdapter struct {
	// SFTP delivery parameters (Deliver path)
	SFTPHost       string // SCP SFTP host — confirm with Kendra Williams (OI #19)
	SFTPUser       string // SCP-assigned SFTP username
	SFTPPrivateKey []byte // PEM-encoded SSH private key from Secrets Manager
	SFTPPort       int    // defaults to 22

	// S3 polling parameters (PollForReturn path)
	S3Client          S3Lister      // narrow interface, satisfied by *awss3.Client
	FISExchangeBucket string        // scp-exchange S3 bucket name
	ReturnFilePrefix  string        // S3 key prefix for SCP return files (e.g. "return/")
	PollInterval      time.Duration // defaults to 30 seconds
}

// S3Lister is the narrow S3 interface needed by PollForReturn.
// Implemented by *awss3.Client. Defined here to enable test substitution.
type S3Lister interface {
	ListObjectsV2(ctx context.Context, params *awss3.ListObjectsV2Input, optFns ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	GetObject(ctx context.Context, params *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
}

// Deliver sends the PGP-encrypted batch file to SCP via SSH/SCP.
// body is a byte-stream of the encrypted file — consumed once, not closed by this method.
// filename follows SCP naming convention: ppppppppmmddccyyss.issuance.txt (§6.6.1).
func (t *FISTransportAdapter) Deliver(ctx context.Context, body io.Reader, filename string) error {
	signer, err := ssh.ParsePrivateKey(t.SFTPPrivateKey)
	if err != nil {
		return fmt.Errorf("fis_transport.Deliver: parse SFTP private key: %w", err)
	}

	port := t.SFTPPort
	if port == 0 {
		port = DefaultSFTPPort
	}

	// HostKeyCallback: InsecureIgnoreHostKey is acceptable for DEV only.
	// Pin to the SCP Transfer Family endpoint's host key before TST (Open Item #19).
	//nolint:gosec // DEV-only; host key pinning required before TST
	sshCfg := &ssh.ClientConfig{
		User:            t.SFTPUser,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", t.SFTPHost, port)
	conn, err := ssh.Dial("tcp", addr, sshCfg)
	if err != nil {
		return fmt.Errorf("fis_transport.Deliver: SSH dial %s: %w", addr, err)
	}
	defer conn.Close()

	content, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("fis_transport.Deliver: read file body: %w", err)
	}

	if err := scpSend(conn, filename, content); err != nil {
		return fmt.Errorf("fis_transport.Deliver: SCP send %s: %w", filename, err)
	}
	return nil
}

// scpSend uploads content as filename to the SSH connection using SCP protocol.
// SCP is supported by all AWS Transfer Family SFTP endpoints.
func scpSend(conn *ssh.Client, filename string, content []byte) error {
	session, err := conn.NewSession()
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	if err := session.Start("scp -t ."); err != nil {
		return fmt.Errorf("start scp -t: %w", err)
	}

	// SCP file header: C<mode> <size> <filename>\n
	if _, err := fmt.Fprintf(stdin, "C0644 %d %s\n", len(content), filename); err != nil {
		return fmt.Errorf("write SCP header: %w", err)
	}
	if _, err := stdin.Write(content); err != nil {
		return fmt.Errorf("write SCP content: %w", err)
	}
	// SCP null terminator signals end of file
	if _, err := fmt.Fprint(stdin, "\x00"); err != nil {
		return fmt.Errorf("write SCP terminator: %w", err)
	}
	if err := stdin.Close(); err != nil {
		return fmt.Errorf("close stdin: %w", err)
	}
	return session.Wait()
}

// PollForReturn polls the scp-exchange S3 bucket for the SCP return file.
//
// SCP deposits the return file via AWS Transfer Family into the ReturnFilePrefix
// of FISExchangeBucket. The return file is identified by:
//   - LastModified > task start time (eliminates stale files from prior batches)
//   - Key ends with ".txt" (SCP text format per §6.6.2)
//   - Key does not contain "issuance" (excludes the outbound file written by Stage 4)
//
// Return file suffix must be confirmed against SCP File Processing Spec v1.37
// (Open Item #10). The current filter is conservative — correct before format is confirmed.
//
// Returns an io.ReadCloser for the return file body. Caller must close it.
func (t *FISTransportAdapter) PollForReturn(ctx context.Context, correlationID uuid.UUID, timeout time.Duration) (io.ReadCloser, error) {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	interval := t.PollInterval
	if interval == 0 {
		interval = DefaultPollInterval
	}

	deadline := time.Now().Add(timeout)
	startTime := time.Now().UTC()

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("fis_transport.PollForReturn: timeout after %s waiting for return file (correlation_id=%s)",
				timeout, correlationID)
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("fis_transport.PollForReturn: context cancelled (correlation_id=%s): %w",
				correlationID, ctx.Err())
		default:
		}

		out, err := t.S3Client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
			Bucket: &t.FISExchangeBucket,
			Prefix: &t.ReturnFilePrefix,
		})
		if err != nil {
			return nil, fmt.Errorf("fis_transport.PollForReturn: ListObjectsV2: %w", err)
		}

		for _, obj := range out.Contents {
			if obj.LastModified == nil || obj.Key == nil {
				continue
			}
			if obj.LastModified.Before(startTime) {
				continue // stale — from a prior batch run
			}
			key := *obj.Key
			if strings.HasSuffix(key, ".txt") && !strings.Contains(key, "issuance") {
				body, err := t.fetchReturnFile(ctx, key)
				if err != nil {
					return nil, fmt.Errorf("fis_transport.PollForReturn: fetch return file (key=%s): %w", key, err)
				}
				return body, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("fis_transport.PollForReturn: context cancelled during sleep: %w", ctx.Err())
		case <-time.After(interval):
		}
	}
}

// fetchReturnFile gets a return file from S3 and buffers the entire body.
// Buffering releases the S3 HTTP connection before Stage 7 begins parsing.
func (t *FISTransportAdapter) fetchReturnFile(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := t.S3Client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: &t.FISExchangeBucket,
		Key:    &key,
	})
	if err != nil {
		return nil, fmt.Errorf("GetObject (key=%s): %w", key, err)
	}
	defer out.Body.Close()

	buf, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("read body (key=%s): %w", key, err)
	}
	return io.NopCloser(bytes.NewReader(buf)), nil
}
