package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
)

// Authenticator generates and verifies gateway keys using a server-side pepper.
//
// Construct once at startup with NewAuthenticator(cfg.KeyPepper) and share
// across goroutines; methods are safe for concurrent use (no mutable state).
type Authenticator struct {
	pepper []byte
}

// NewAuthenticator constructs an Authenticator with the given pepper.
//
// The pepper should be a high-entropy random value (32+ bytes). It must be
// stable across restarts — rotating the pepper invalidates every existing
// gateway key. See cfg.KeyPepper.
func NewAuthenticator(pepper string) (*Authenticator, error) {
	if len(pepper) < 32 {
		return nil, errors.New("auth: pepper must be at least 32 chars")
	}
	return &Authenticator{pepper: []byte(pepper)}, nil
}

// GenerateKey creates a fresh gateway key and returns it along with the
// HMAC of its secret.
//
// The full key (returned as the first value) is shown to the user once at
// creation and never recoverable. The hash (second value) is what gets
// stored in the database. The keyId is extracted from the key for the
// caller's convenience (it's the indexed lookup column).
func (a *Authenticator) GenerateKey() (key *Key, keyHash []byte, err error) {
	keyID, err := generateKeyID()
	if err != nil {
		return nil, nil, err
	}
	secret, err := generateSecret()
	if err != nil {
		return nil, nil, err
	}

	raw := KeyPrefix + keyID + "_" + secret
	k := &Key{Raw: raw, KeyID: keyID, Secret: secret}

	hash, err := a.hashSecret(secret)
	if err != nil {
		return nil, nil, err
	}

	return k, hash, nil
}

// Verify returns true if the provided secret hashes to the stored hash.
//
// Uses a constant-time comparison; takes the same number of byte operations
// regardless of how early in the comparison a mismatch occurs. This closes
// a timing side channel that would otherwise let an attacker probe one byte
// of the hash at a time.
func (a *Authenticator) Verify(secret string, storedHash []byte) (bool, error) {
	candidateHash, err := a.hashSecret(secret)
	if err != nil {
		return false, err
	}

	return subtle.ConstantTimeCompare(candidateHash, storedHash) == 1, nil
}

// hashSecret computes HMAC-SHA256(secret, pepper).
//
// The secret is provided as a hex string (matching the on-the-wire format);
// we decode to bytes before HMACing because HMAC is conceptually over bytes,
// and decoding once at the boundary avoids subtle bugs from HMACing the
// hex representation in some places and the bytes in others.
func (a *Authenticator) hashSecret(secret string) ([]byte, error) {
	secretBytes, err := hex.DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("decode secret: %w", err)
	}
	mac := hmac.New(sha256.New, a.pepper)
	mac.Write(secretBytes)
	return mac.Sum(nil), nil
}
