// Package s3 implements ports.FileStore against AWS S3.
//
// Three bucket prefixes with distinct access rules (ADR-006, §5.4.3, §5.4.5):
//   inbound-raw/   write-once; DeleteObject is NOT permitted on this prefix
//   staged/        decrypted plaintext; MUST be deleted after Stage 4
//   fis-exchange/  PGP-encrypted FIS files and inbound return files
//
// All objects use SSE-KMS with customer-managed key (§5.4.3).
// Public access blocked at account level — enforced by CDK.
package s3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/walker-morse/batch/_shared/ports"
)

// Store implements ports.FileStore.
type Store struct {
	client    *s3.Client
	kmsKeyARN string // customer-managed KMS key for SSE-KMS (§5.4.3)
}

func New(client *s3.Client, kmsKeyARN string) *Store {
	return &Store{client: client, kmsKeyARN: kmsKeyARN}
}

func (s *Store) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3.GetObject %s/%s: %w", bucket, key, err)
	}
	return out.Body, nil
}

func (s *Store) PutObject(ctx context.Context, bucket, key string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("s3.PutObject read body: %w", err)
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(data),
		ServerSideEncryption: "aws:kms",
		SSEKMSKeyId:          aws.String(s.kmsKeyARN),
	})
	if err != nil {
		return fmt.Errorf("s3.PutObject %s/%s: %w", bucket, key, err)
	}
	return nil
}

// DeleteObject removes a transient plaintext file.
// ONLY valid on the staged/ prefix — the ingest-task IAM role has no
// DeleteObject permission on inbound-raw/ (§5.4.3, §5.4.5).
func (s *Store) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3.DeleteObject %s/%s: %w", bucket, key, err)
	}
	return nil
}

func (s *Store) HeadObject(ctx context.Context, bucket, key string) (*ports.ObjectMeta, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3.HeadObject %s/%s: %w", bucket, key, err)
	}

	meta := &ports.ObjectMeta{
		Size:         aws.ToInt64(out.ContentLength),
		LastModified: aws.ToTime(out.LastModified),
	}

	// Extract SHA-256 checksum if present (set by AWS when object was uploaded
	// with checksum algorithm). Fall back to computing it ourselves if not.
	if out.ChecksumSHA256 != nil {
		meta.SHA256 = aws.ToString(out.ChecksumSHA256)
	}

	return meta, nil
}

// SHA256OfObject downloads the object and computes its SHA-256 hash.
// Used by Stage 1 to write sha256_encrypted and sha256_plaintext to batch_files.
// Returns the hex-encoded digest.
func (s *Store) SHA256OfObject(ctx context.Context, bucket, key string) (string, error) {
	body, err := s.GetObject(ctx, bucket, key)
	if err != nil {
		return "", err
	}
	defer body.Close()

	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return "", fmt.Errorf("s3.SHA256OfObject %s/%s: %w", bucket, key, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
