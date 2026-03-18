package pgp_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/openpgp"      //nolint:staticcheck
	"golang.org/x/crypto/openpgp/armor" //nolint:staticcheck

	pgpadapter "github.com/walker-morse/batch/_adapters/pgp"
)

// generateTestKey creates a throwaway RSA PGP entity for testing.
// Never use test keys outside of unit tests.
func generateTestKey(t *testing.T, name string) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity(name, "test", name+"@test.invalid", nil)
	if err != nil {
		t.Fatalf("generateTestKey: %v", err)
	}
	return e
}

// armorPublicKey returns the ASCII-armored public key for a PGP entity.
func armorPublicKey(t *testing.T, e *openpgp.Entity) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatalf("armorPublicKey: armor encode: %v", err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatalf("armorPublicKey: serialize: %v", err)
	}
	w.Close()
	return buf.String()
}

// armorPrivateKey returns the ASCII-armored private key for a PGP entity.
func armorPrivateKey(t *testing.T, e *openpgp.Entity) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PrivateKeyType, nil)
	if err != nil {
		t.Fatalf("armorPrivateKey: armor encode: %v", err)
	}
	if err := e.SerializePrivate(w, nil); err != nil {
		t.Fatalf("armorPrivateKey: serialize private: %v", err)
	}
	w.Close()
	return buf.String()
}

// encryptToEntity encrypts plaintext to a PGP entity and returns the ciphertext reader.
// Used to produce test ciphertext without going through the full adapter.
func encryptToEntity(t *testing.T, recipient *openpgp.Entity, plaintext string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	w, err := openpgp.Encrypt(&buf, []*openpgp.Entity{recipient}, nil, nil, nil)
	if err != nil {
		t.Fatalf("encryptToEntity: %v", err)
	}
	if _, err := io.WriteString(w, plaintext); err != nil {
		t.Fatalf("encryptToEntity write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("encryptToEntity close: %v", err)
	}
	return &buf
}

// ─── Encrypter tests ──────────────────────────────────────────────────────────

// TestEncrypter_ProducesNonEmptyOutput verifies that Encrypt returns bytes.
func TestEncrypter_ProducesNonEmptyOutput(t *testing.T) {
	e := generateTestKey(t, "fis-recipient")
	enc := pgpadapter.NewEncrypterFromEntity(e)

	ciphertext, err := enc.Encrypt(strings.NewReader("HELLO FIS"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	out, _ := io.ReadAll(ciphertext)
	if len(out) == 0 {
		t.Fatal("Encrypt returned empty ciphertext")
	}
}

// TestEncrypter_OutputIsNotPlaintext verifies the output is not the raw input.
func TestEncrypter_OutputIsNotPlaintext(t *testing.T) {
	e := generateTestKey(t, "fis-recipient")
	enc := pgpadapter.NewEncrypterFromEntity(e)

	plaintext := "SENSITIVE MEMBER DATA"
	ciphertext, err := enc.Encrypt(strings.NewReader(plaintext))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	out, _ := io.ReadAll(ciphertext)
	if strings.Contains(string(out), plaintext) {
		t.Fatal("ciphertext contains plaintext — encryption not applied")
	}
}

// ─── Decrypter tests ──────────────────────────────────────────────────────────

// TestDecrypter_RoundTrip verifies that Decrypt(Encrypt(plaintext)) == plaintext.
func TestDecrypter_RoundTrip(t *testing.T) {
	e := generateTestKey(t, "morse-recipient")
	enc := pgpadapter.NewEncrypterFromEntity(e)
	dec := pgpadapter.NewDecrypterFromEntity(e)

	const want = "MEMBER_ID=12345|SUBPROGRAM=DENTAL|ACTION=ADD"

	ciphertext, err := enc.Encrypt(strings.NewReader(want))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	plainReader, err := dec.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	got, err := io.ReadAll(plainReader)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if string(got) != want {
		t.Errorf("round-trip mismatch\n  want: %q\n   got: %q", want, string(got))
	}
}

// TestDecrypter_WrongKeyReturnsError verifies that decrypting with the wrong key fails.
func TestDecrypter_WrongKeyReturnsError(t *testing.T) {
	sender := generateTestKey(t, "sender")
	wrongKey := generateTestKey(t, "wrong-recipient")

	enc := pgpadapter.NewEncrypterFromEntity(sender)
	dec := pgpadapter.NewDecrypterFromEntity(wrongKey)

	ciphertext, err := enc.Encrypt(strings.NewReader("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	_, err = dec.Decrypt(ciphertext)
	if err == nil {
		t.Fatal("expected error decrypting with wrong key, got nil")
	}
}

// TestDecrypter_EmptyReaderReturnsError verifies that an empty/corrupt input fails gracefully.
func TestDecrypter_EmptyReaderReturnsError(t *testing.T) {
	e := generateTestKey(t, "morse-recipient")
	dec := pgpadapter.NewDecrypterFromEntity(e)

	_, err := dec.Decrypt(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error decrypting empty input, got nil")
	}
}

// TestDecrypter_LargePayload verifies round-trip on a payload comparable to a
// real SRG310 batch file (~400 bytes × 50k rows = ~20 MB).
func TestDecrypter_LargePayload(t *testing.T) {
	e := generateTestKey(t, "morse-large")
	enc := pgpadapter.NewEncrypterFromEntity(e)
	dec := pgpadapter.NewDecrypterFromEntity(e)

	// 400 bytes × 1000 rows = 400 KB — fast enough for unit test
	row := strings.Repeat("X", 398) + "\r\n"
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString(row)
	}
	want := sb.String()

	ciphertext, err := enc.Encrypt(strings.NewReader(want))
	if err != nil {
		t.Fatalf("Encrypt large: %v", err)
	}
	plainReader, err := dec.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt large: %v", err)
	}
	got, err := io.ReadAll(plainReader)
	if err != nil {
		t.Fatalf("ReadAll large: %v", err)
	}
	if string(got) != want {
		t.Errorf("large round-trip: length mismatch want=%d got=%d", len(want), len(got))
	}
}
