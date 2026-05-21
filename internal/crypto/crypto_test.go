package crypto_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/LightAwesome/portcullis/internal/crypto"
)

// freshKey generates a new random 32-byte key for tests. Tests that
// need determinism use a hardcoded key instead.
func freshKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, crypto.KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return k
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := freshKey(t)

	cases := []struct {
		name      string
		plaintext []byte
	}{
		{"empty", []byte{}},
		{"short", []byte("sk-test-1234567890")},
		{"openai-like", []byte("sk-proj-abc123XYZdef456GHIjkl789MNOpqr012STUvwx345YZ")},
		{"binary", []byte{0x00, 0xff, 0x01, 0xfe, 0x02, 0xfd}},
		{"long", bytes.Repeat([]byte("A"), 4096)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ct, err := crypto.Encrypt(tc.plaintext, key)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}

			pt, err := crypto.Decrypt(ct, key)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}

			if !bytes.Equal(pt, tc.plaintext) {
				t.Errorf("plaintext mismatch:\n got %q\nwant %q", pt, tc.plaintext)
			}
		})
	}
}

// TestEncrypt_FreshNonce verifies that two calls to Encrypt with the
// same plaintext and key produce different ciphertexts. This is the
// core security property — same input, different output, because the
// nonce is fresh every time.
//
// If this test ever fails, AES-GCM's confidentiality is broken for
// our use case.
func TestEncrypt_FreshNonce(t *testing.T) {
	key := freshKey(t)
	plaintext := []byte("the same plaintext every time")

	ct1, err := crypto.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt #1: %v", err)
	}
	ct2, err := crypto.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt #2: %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext (nonce reused?)")
	}
}

// TestDecrypt_TamperDetection flips a single bit in the ciphertext
// and verifies decryption fails. This is what authenticated
// encryption buys us over plain AES-CTR.
func TestDecrypt_TamperDetection(t *testing.T) {
	key := freshKey(t)
	plaintext := []byte("trusted upstream secret")

	ct, err := crypto.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// Flip a bit in the middle of the ciphertext (avoiding the nonce
	// at the start — we want to prove the tag catches payload tampering).
	tampered := append([]byte(nil), ct...)
	mid := len(tampered) / 2
	tampered[mid] ^= 0x01

	if _, err := crypto.Decrypt(tampered, key); err == nil {
		t.Fatal("decryption succeeded on tampered ciphertext; auth tag did not catch it")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	key1 := freshKey(t)
	key2 := freshKey(t)

	ct, err := crypto.Encrypt([]byte("secret"), key1)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if _, err := crypto.Decrypt(ct, key2); err == nil {
		t.Fatal("decryption succeeded with wrong key")
	}
}

func TestEncrypt_RejectsBadKeyLength(t *testing.T) {
	for _, size := range []int{0, 1, 16, 31, 33, 64} {
		t.Run("", func(t *testing.T) {
			key := make([]byte, size)
			_, err := crypto.Encrypt([]byte("x"), key)
			if !errors.Is(err, crypto.ErrKeySize) {
				t.Errorf("size=%d: want ErrKeySize, got %v", size, err)
			}
		})
	}
}

func TestDecrypt_RejectsShortCiphertext(t *testing.T) {
	key := freshKey(t)

	// Anything shorter than nonce (12) + tag (16) = 28 bytes is malformed.
	for _, size := range []int{0, 1, 12, 27} {
		t.Run("", func(t *testing.T) {
			ct := make([]byte, size)
			_, err := crypto.Decrypt(ct, key)
			if !errors.Is(err, crypto.ErrCiphertextTooShort) {
				t.Errorf("size=%d: want ErrCiphertextTooShort, got %v", size, err)
			}
		})
	}
}

func TestParseMasterKey_Valid(t *testing.T) {
	raw := freshKey(t)
	encoded := base64.StdEncoding.EncodeToString(raw)

	parsed, err := crypto.ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	if !bytes.Equal(parsed, raw) {
		t.Errorf("ParseMasterKey: got %x, want %x", parsed, raw)
	}
}

func TestParseMasterKey_Errors(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"not-base64", "this is not base64 !!!"},
		{"too-short", base64.StdEncoding.EncodeToString([]byte("short"))},
		{"too-long", base64.StdEncoding.EncodeToString(make([]byte, 64))},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := crypto.ParseMasterKey(tc.in); err == nil {
				t.Errorf("ParseMasterKey(%q): want error, got nil", tc.in)
			}
		})
	}
}
