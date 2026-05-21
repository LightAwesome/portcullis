// Package crypto provides AES-256-GCM symmetric encryption for the
// gateway's at-rest secrets.
//
// The primary use case is encrypting upstream API keys before they're
// stored in upstream_routes.upstream_secret_ciphertext. Plaintext keys
// live only in memory; the master key from PORTCULLIS_MASTER_KEY is
// the single secret that protects them.
//
// Threat model and limitations: see PRD §4.5. This package defends
// against database disclosure, not against a compromise of the gateway
// process itself. Key rotation is not supported; rotating PORTCULLIS_MASTER_KEY
// invalidates all previously-encrypted ciphertexts.
//
// All functions in this package are safe for concurrent use.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the required length, in bytes, of the master key.
// AES-256 requires a 256-bit (32-byte) key.
const KeySize = 32

// ErrKeySize is returned when a key of the wrong length is passed to
// Encrypt, Decrypt, or ParseMasterKey.
var ErrKeySize = fmt.Errorf("crypto: key must be exactly %d bytes", KeySize)

// ErrCiphertextTooShort is returned by Decrypt when the input cannot
// contain at minimum a nonce and an authentication tag. A well-formed
// ciphertext is always at least (nonce + tag) = 28 bytes, even for
// empty plaintext.
var ErrCiphertextTooShort = errors.New("crypto: ciphertext too short")

// Encrypt encrypts plaintext with key using AES-256-GCM and returns
// nonce ‖ ciphertext ‖ tag as a single byte slice.
//
// A fresh nonce is generated from crypto/rand for every call; never
// reuse a nonce with the same key.
//
// Returns an error if key is the wrong length or crypto/rand fails.
func Encrypt(plaintext, key []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		// Unreachable given the length check above, but aes.NewCipher's
		// contract permits errors and we honour it.
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("rand.Read: %w", err)
	}

	// Seal appends the ciphertext (with its 16-byte auth tag) to dst.
	// We pass nonce as dst so the returned slice is nonce ‖ ciphertext ‖ tag.
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt. The input must have been produced by Encrypt
// with the same key.
//
// Returns an error if the key is wrong length, the input is malformed,
// or the authentication tag does not verify (tampering or wrong key).
// The error does not distinguish between these cases to avoid leaking
// information about *why* decryption failed.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM: %w", err)
	}

	nonceSize := aead.NonceSize()
	if len(ciphertext) < nonceSize+aead.Overhead() {
		return nil, ErrCiphertextTooShort
	}

	nonce, payload := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Open verifies the auth tag before returning plaintext. If the
	// tag is wrong, payload is corrupt, or key is wrong, this returns
	// an error and the caller learns nothing about the plaintext.
	plaintext, err := aead.Open(nil, nonce, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("aead.Open: %w", err)
	}
	return plaintext, nil
}

// ParseMasterKey decodes a base64-encoded 32-byte master key into raw bytes.
//
// Intended for parsing PORTCULLIS_MASTER_KEY at startup. The expected
// format is standard base64 (RFC 4648) without padding handling
// requirements — base64.StdEncoding handles both with-padding and
// without-padding inputs of length 44 / 43 respectively.
//
// Returns an error if the input is not valid base64 or does not decode
// to exactly 32 bytes.
func ParseMasterKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode base64: %w", err)
	}
	if len(raw) != KeySize {
		return nil, fmt.Errorf("%w: got %d bytes", ErrKeySize, len(raw))
	}
	return raw, nil
}
