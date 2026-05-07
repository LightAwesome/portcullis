package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/LightAwesome/portcullis/internal/auth"
)

const testPepper = "test-pepper-must-be-at-least-32-chars-long"

// === Key parsing ===

func TestParseKey_Valid(t *testing.T) {
	const raw = "pck_0123456789abcdef_" + // 16 chars keyId
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 chars secret

	k, err := auth.ParseKey(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if k.KeyID != "0123456789abcdef" {
		t.Errorf("keyID: got %q", k.KeyID)
	}
	if len(k.Secret) != 64 {
		t.Errorf("secret length: got %d, want 64", len(k.Secret))
	}
	if k.Raw != raw {
		t.Errorf("raw: got %q, want %q", k.Raw, raw)
	}
}

func TestParseKey_WrongPrefix(t *testing.T) {
	_, err := auth.ParseKey("xyz_0123456789abcdef_" + strings.Repeat("a", 64))
	if !errors.Is(err, auth.ErrWrongPrefix) {
		t.Fatalf("expected ErrWrongPrefix, got %v", err)
	}
}

func TestParseKey_NoSeparator(t *testing.T) {
	_, err := auth.ParseKey("pck_garbage")
	if !errors.Is(err, auth.ErrMalformedKey) {
		t.Fatalf("expected ErrMalformedKey, got %v", err)
	}
}

func TestParseKey_WrongKeyIDLength(t *testing.T) {
	_, err := auth.ParseKey("pck_short_" + strings.Repeat("a", 64))
	if !errors.Is(err, auth.ErrMalformedKey) {
		t.Fatalf("expected ErrMalformedKey for short keyID, got %v", err)
	}
}

func TestParseKey_WrongSecretLength(t *testing.T) {
	_, err := auth.ParseKey("pck_0123456789abcdef_short")
	if !errors.Is(err, auth.ErrMalformedKey) {
		t.Fatalf("expected ErrMalformedKey for short secret, got %v", err)
	}
}

func TestParseKey_NonHex(t *testing.T) {
	// 16 chars but contains 'z'
	_, err := auth.ParseKey("pck_0123456789abcdez_" + strings.Repeat("a", 64))
	if !errors.Is(err, auth.ErrMalformedKey) {
		t.Fatalf("expected ErrMalformedKey for non-hex keyID, got %v", err)
	}
}

// === Authenticator construction ===

func TestNewAuthenticator_RejectsShortPepper(t *testing.T) {
	_, err := auth.NewAuthenticator("too-short")
	if err == nil {
		t.Fatal("expected error for short pepper")
	}
}

// === Generate / Verify round-trip ===

func TestGenerateAndVerify(t *testing.T) {
	a, err := auth.NewAuthenticator(testPepper)
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}

	k, hash, err := a.GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Sanity-check the generated key.
	if !strings.HasPrefix(k.Raw, "pck_") {
		t.Errorf("missing prefix: %q", k.Raw)
	}
	if len(k.KeyID) != 16 {
		t.Errorf("keyID length: %d", len(k.KeyID))
	}
	if len(k.Secret) != 64 {
		t.Errorf("secret length: %d", len(k.Secret))
	}
	if len(hash) != 32 {
		t.Errorf("hash length: %d, want 32 (SHA-256)", len(hash))
	}

	// Round-trip: parse the raw key and verify the secret.
	parsed, err := auth.ParseKey(k.Raw)
	if err != nil {
		t.Fatalf("parse generated key: %v", err)
	}
	ok, err := a.Verify(parsed.Secret, hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Error("verification failed for valid key")
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	a, _ := auth.NewAuthenticator(testPepper)

	_, hash, _ := a.GenerateKey()

	// A different valid-format secret should not verify.
	wrongSecret := strings.Repeat("0", 64)
	ok, err := a.Verify(wrongSecret, hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Error("verification succeeded for wrong secret")
	}
}

func TestVerify_DifferentPeppersProduceDifferentHashes(t *testing.T) {
	a1, _ := auth.NewAuthenticator(testPepper)
	a2, _ := auth.NewAuthenticator(testPepper + "x")

	k, hash, _ := a1.GenerateKey()

	// Verifying with a different-pepper authenticator must fail, even
	// though the secret itself is identical. This proves the pepper is
	// actually being used in the HMAC.
	ok, err := a2.Verify(k.Secret, hash)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Error("verification succeeded across different peppers")
	}
}

func TestGenerateKey_ProducesUniqueValues(t *testing.T) {
	a, _ := auth.NewAuthenticator(testPepper)

	const n = 100
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		k, _, err := a.GenerateKey()
		if err != nil {
			t.Fatalf("generate %d: %v", i, err)
		}
		if seen[k.KeyID] {
			t.Fatalf("duplicate keyID after %d: %s", i, k.KeyID)
		}
		seen[k.KeyID] = true
	}
}
