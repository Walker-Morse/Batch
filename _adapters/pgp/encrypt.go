package pgp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"golang.org/x/crypto/openpgp"      //nolint:staticcheck
	"golang.org/x/crypto/openpgp/armor" //nolint:staticcheck
)

// Encrypter holds the loaded FIS recipient public key and returns an encrypt func
// compatible with BatchAssemblyStage.PGPEncrypt.
//
// The FIS public key is ASCII-armored and stored in Secrets Manager.
// It is loaded once at task startup — not per-file.
//
// Key format expected: ASCII-armored OpenPGP public key block (exported from
// FIS-provided certificate or key file). FIS key rotation is a manual process —
// update the Secrets Manager secret value and re-deploy the task definition.
// (Open Item #41 — key rotation runbook.)
type Encrypter struct {
	recipient *openpgp.Entity
}

// LoadEncrypter fetches the FIS public key from Secrets Manager and returns an Encrypter.
//
// publicKeySecretARN — ARN of the Secrets Manager secret containing the
//   ASCII-armored OpenPGP public key provided by FIS.
//
// Returns an error if the secret cannot be read or the key cannot be parsed.
// Called once at ingest-task startup — not per-file.
func LoadEncrypter(
	ctx context.Context,
	sm *secretsmanager.Client,
	publicKeySecretARN string,
) (*Encrypter, error) {
	keyPEM, err := getSecret(ctx, sm, publicKeySecretARN)
	if err != nil {
		return nil, fmt.Errorf("pgp.LoadEncrypter: fetch public key: %w", err)
	}

	block, err := armor.Decode(strings.NewReader(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("pgp.LoadEncrypter: armor decode: %w", err)
	}

	entities, err := openpgp.ReadKeyRing(block.Body)
	if err != nil {
		return nil, fmt.Errorf("pgp.LoadEncrypter: read key ring: %w", err)
	}
	if len(entities) == 0 {
		return nil, fmt.Errorf("pgp.LoadEncrypter: public key secret contained no PGP entities")
	}

	return &Encrypter{recipient: entities[0]}, nil
}

// NewEncrypterFromEntity constructs an Encrypter directly from a PGP entity.
// For use in tests only — production code must use LoadEncrypter.
func NewEncrypterFromEntity(e *openpgp.Entity) *Encrypter {
	return &Encrypter{recipient: e}
}

// Encrypt satisfies the func(io.Reader) (io.Reader, error) signature
// consumed by BatchAssemblyStage.PGPEncrypt.
//
// Produces a binary (non-armored) OpenPGP message encrypted to the FIS public key.
// FIS expects binary PGP — do not armor the output unless FIS confirms otherwise.
//
// Uses AES-256 symmetric cipher selected by the recipient key's preferred algorithms.
// No signing — FIS does not require message signing on outbound batch files.
func (e *Encrypter) Encrypt(plaintext io.Reader) (io.Reader, error) {
	var buf bytes.Buffer

	w, err := openpgp.Encrypt(&buf, []*openpgp.Entity{e.recipient}, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("pgp.Encrypt: open encrypt writer: %w", err)
	}

	if _, err := io.Copy(w, plaintext); err != nil {
		return nil, fmt.Errorf("pgp.Encrypt: write plaintext: %w", err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("pgp.Encrypt: close encrypt writer: %w", err)
	}

	return &buf, nil
}
