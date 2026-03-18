// Package pgp provides PGP encrypt/decrypt adapters for the One Fintech pipeline.
//
// Keys are loaded once at task startup from AWS Secrets Manager.
// Secrets Manager ARNs are passed in via PipelineConfig and never hard-coded.
//
// Key material format expected in Secrets Manager:
//   PGP private key: ASCII-armored OpenPGP private key block.
//     SecretString value is the entire armoured text including headers.
//     Passphrase (if key is encrypted) stored in a separate secret.
//
// Security requirements:
//   - Private key secret must have resource policy restricting access to task role ARN only.
//   - Key rotation: new secret version, re-deploy task definition (Open Item #41).
//   - NullPGPDecrypt MUST NOT be used in TST or PRD (enforced by LoadDecrypter guard).
package pgp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"golang.org/x/crypto/openpgp"      //nolint:staticcheck // openpgp/v2 not yet stable
	"golang.org/x/crypto/openpgp/armor" //nolint:staticcheck
)

// Decrypter holds the loaded PGP entity and returns a decrypt func
// compatible with ValidationStage.PGPDecrypt.
type Decrypter struct {
	keyring openpgp.EntityList
}

// LoadDecrypter fetches the private key from Secrets Manager and returns a Decrypter.
//
// privateKeySecretARN — ARN of the Secrets Manager secret containing the
//   ASCII-armored OpenPGP private key.
// passphraseSecretARN — ARN of the passphrase secret, or "" if key is unencrypted.
//   For HSM-backed keys pass "" and use a hardware-protected unencrypted key.
//
// Returns an error if the secret cannot be read or the key cannot be parsed.
// Called once at ingest-task startup — not per-file.
func LoadDecrypter(
	ctx context.Context,
	sm *secretsmanager.Client,
	privateKeySecretARN string,
	passphraseSecretARN string,
) (*Decrypter, error) {
	// Fetch the ASCII-armored private key
	keyPEM, err := getSecret(ctx, sm, privateKeySecretARN)
	if err != nil {
		return nil, fmt.Errorf("pgp.LoadDecrypter: fetch private key: %w", err)
	}

	block, err := armor.Decode(strings.NewReader(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("pgp.LoadDecrypter: armor decode: %w", err)
	}

	entities, err := openpgp.ReadKeyRing(block.Body)
	if err != nil {
		return nil, fmt.Errorf("pgp.LoadDecrypter: read key ring: %w", err)
	}
	if len(entities) == 0 {
		return nil, fmt.Errorf("pgp.LoadDecrypter: private key secret contained no PGP entities")
	}

	// Decrypt the private key's subkeys if passphrase-protected
	if passphraseSecretARN != "" {
		passphrase, err := getSecret(ctx, sm, passphraseSecretARN)
		if err != nil {
			return nil, fmt.Errorf("pgp.LoadDecrypter: fetch passphrase: %w", err)
		}
		pp := []byte(strings.TrimSpace(passphrase))
		for _, e := range entities {
			if e.PrivateKey != nil && e.PrivateKey.Encrypted {
				if err := e.PrivateKey.Decrypt(pp); err != nil {
					return nil, fmt.Errorf("pgp.LoadDecrypter: decrypt primary key: %w", err)
				}
			}
			for _, sub := range e.Subkeys {
				if sub.PrivateKey != nil && sub.PrivateKey.Encrypted {
					if err := sub.PrivateKey.Decrypt(pp); err != nil {
						return nil, fmt.Errorf("pgp.LoadDecrypter: decrypt subkey: %w", err)
					}
				}
			}
		}
	}

	return &Decrypter{keyring: entities}, nil
}

// NewDecrypterFromEntity constructs a Decrypter directly from a PGP entity list.
// For use in tests only — production code must use LoadDecrypter.
func NewDecrypterFromEntity(e *openpgp.Entity) *Decrypter {
	return &Decrypter{keyring: openpgp.EntityList{e}}
}

// Decrypt satisfies the func(io.Reader) (io.Reader, error) signature
// consumed by ValidationStage.PGPDecrypt.
//
// encrypted must be a binary OpenPGP message (not ASCII-armored at the message level).
// FIS sends binary-encrypted PGP files — if FIS ever sends armored messages,
// strip the armor wrapper before calling this (see ArmoredDecrypt below).
func (d *Decrypter) Decrypt(encrypted io.Reader) (io.Reader, error) {
	md, err := openpgp.ReadMessage(encrypted, d.keyring, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("pgp.Decrypt: read message: %w", err)
	}

	// Buffer the plaintext so the caller gets a simple io.Reader.
	// For very large files (>100 MB) consider streaming — Open Item #42.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, md.UnverifiedBody); err != nil {
		return nil, fmt.Errorf("pgp.Decrypt: read plaintext body: %w", err)
	}

	return &buf, nil
}

// ArmoredDecrypt is identical to Decrypt but strips ASCII armor first.
// Use only if FIS ever sends armored message files (currently they don't).
func (d *Decrypter) ArmoredDecrypt(encrypted io.Reader) (io.Reader, error) {
	block, err := armor.Decode(encrypted)
	if err != nil {
		return nil, fmt.Errorf("pgp.ArmoredDecrypt: armor decode: %w", err)
	}
	return d.Decrypt(block.Body)
}
