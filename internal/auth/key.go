// Package auth handles gateway-key generation and verification.
//
// Keys take the form pck_<keyId>_<secret>:
//
//   - "pck_" — fixed prefix (Portcullis Key) for grep-ability and secret-scanning
//   - keyId — 16 hex chars (8 random bytes); plaintext in Postgres, indexed for lookup
//   - secret — 64 hex chars (32 random bytes); never stored, only its HMAC
//
// The HMAC uses SHA-256 keyed with a server-side pepper (config.KeyPepper).
// On every authenticated request, the gateway looks up the client by keyId,
// recomputes HMAC-SHA256(secret, pepper), and constant-time compares to the
// stored hash. See PRD §4.2 for the full design rationale.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Constants for the key format. Exported so handlers and tests can reference them.
const (
	KeyPrefix    = "pck_"
	KeyIDLength  = 16 // hex chars
	SecretLength = 64 // hex chars
	keyIDBytes   = 8  // = KeyIDLength / 2
	secretBytes  = 32 // = SecretLength / 2
)

// Key is a parsed gateway key with its components separated.
//
// The Raw field preserves the original string form for logging and error
// messages where reconstructing it from KeyID + Secret would obscure
// formatting issues.
type Key struct {
	Raw    string
	KeyID  string // 16 hex chars
	Secret string // 64 hex chars
}

// Errors returned by ParseKey. Sentinel values; compare with errors.Is.
var (
	ErrMalformedKey = errors.New("auth: malformed gateway key")
	ErrWrongPrefix  = errors.New("auth: gateway key has wrong prefix")
)

// ParseKey extracts the keyId and secret from a string of the form
// pck_<keyId>_<secret>.
//
// Returns ErrMalformedKey if the structure is wrong (missing parts, wrong
// lengths, non-hex characters). The error type doesn't differentiate between
// these cases — callers shouldn't behave differently based on which way the
// key is malformed; they all map to "401 unauthorized."
func ParseKey(raw string) (*Key, error) {
	if !strings.HasPrefix(raw, KeyPrefix) {
		return nil, ErrWrongPrefix
	}

	rest := raw[len(KeyPrefix):]

	// Expect: <keyId>_<secret>. Exactly one underscore.
	parts := strings.Split(rest, "_")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: expected one separator", ErrMalformedKey)
	}

	keyID, secret := parts[0], parts[1]

	if len(keyID) != KeyIDLength {
		return nil, fmt.Errorf("%w: keyId is %d chars, want %d", ErrMalformedKey, len(keyID), KeyIDLength)
	}
	if len(secret) != SecretLength {
		return nil, fmt.Errorf("%w: secret is %d chars, want %d", ErrMalformedKey, len(secret), SecretLength)
	}

	// Verify both halves are valid hex. We don't actually need the decoded
	// bytes here — we just want to reject inputs that have non-hex chars.
	if _, err := hex.DecodeString(keyID); err != nil {
		return nil, fmt.Errorf("%w: keyId is not hex", ErrMalformedKey)
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return nil, fmt.Errorf("%w: secret is not hex", ErrMalformedKey)
	}

	return &Key{Raw: raw, KeyID: keyID, Secret: secret}, nil
}

// generateKeyID returns a freshly generated 16-char hex key ID.
//
// Uses crypto/rand so the IDs are unpredictable; keyIDs are public-ish (they
// appear in logs and admin output) but generating them with secure entropy
// avoids accidentally encoding patterns that leak information.
func generateKeyID() (string, error) {
	b := make([]byte, keyIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate keyID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// generateSecret returns a freshly generated 64-char hex secret (32 random bytes).
//
// Uses crypto/rand for cryptographic-grade entropy.
// math/rand is deterministic from its seed and an attacker who can guess the
// seed can predict every secret your gateway issues.
func generateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}
