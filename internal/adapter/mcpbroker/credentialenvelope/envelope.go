// Package credentialenvelope implements the persisted credential envelope format.
package credentialenvelope

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const (
	credentialEnvelopeVersion = "v1"
	credentialEnvelopeAlg     = "a256gcm"
	// KeyBytes is the required size of each envelope key.
	KeyBytes             = 32
	credentialSaltBytes  = 32
	credentialNonceBytes = 12
	// MaxEnvelopeBytes bounds untrusted persisted envelopes.
	MaxEnvelopeBytes = 1 << 20
)

// ErrUnavailable identifies rejected key material or an unreadable envelope.
var ErrUnavailable = errors.New("mcpbroker: credential envelope unavailable")

// KeyRing is an immutable set of envelope keys with one active write key.
type KeyRing struct {
	activeID string
	keys     map[string][]byte
}

// NewKeyRing copies and validates the key material.
func NewKeyRing(activeID string, keys map[string][]byte) (*KeyRing, error) {
	if !validCredentialKeyID(activeID) || len(keys) == 0 {
		return nil, ErrUnavailable
	}
	out := &KeyRing{activeID: activeID, keys: make(map[string][]byte, len(keys))}
	for id, key := range keys {
		if !validCredentialKeyID(id) || len(key) != KeyBytes {
			return nil, ErrUnavailable
		}
		out.keys[id] = bytes.Clone(key)
	}
	if _, ok := out.keys[activeID]; !ok {
		return nil, ErrUnavailable
	}
	return out, nil
}

func validCredentialKeyID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// AAD is canonical length-delimited framing, so distinct logical rows
// cannot produce the same binding value.
func AAD(parts ...string) []byte {
	var b strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&b, "%d:", len(part))
		b.WriteString(part)
	}
	return []byte(b.String())
}

// Seal encrypts a nonempty value using a fresh per-envelope salt and nonce.
func (k *KeyRing) Seal(aad []byte, plaintext string) (string, error) {
	if k == nil || plaintext == "" || len(plaintext) > MaxEnvelopeBytes {
		return "", ErrUnavailable
	}
	kek := k.keys[k.activeID]
	if len(kek) != KeyBytes {
		return "", ErrUnavailable
	}
	salt := make([]byte, credentialSaltBytes)
	payloadNonce := make([]byte, credentialNonceBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", ErrUnavailable
	}
	if _, err := rand.Read(payloadNonce); err != nil {
		return "", ErrUnavailable
	}
	meta := AAD("mecatl-toolhive-credential", credentialEnvelopeVersion, credentialEnvelopeAlg, k.activeID)
	key, err := hkdf.Key(sha256.New, kek, salt, string(meta), KeyBytes)
	if err != nil {
		return "", ErrUnavailable
	}
	defer clear(key)
	payload, err := sealGCM(key, payloadNonce, aadWith(aad, meta), []byte(plaintext))
	if err != nil {
		return "", ErrUnavailable
	}
	out := strings.Join([]string{"mecatl", credentialEnvelopeVersion, credentialEnvelopeAlg, k.activeID,
		base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(payloadNonce),
		base64.RawURLEncoding.EncodeToString(payload)}, ".")
	if len(out) > MaxEnvelopeBytes {
		return "", ErrUnavailable
	}
	return out, nil
}

// Open validates every untrusted envelope segment before decrypting it.
//
//nolint:gocyclo // Strict envelope parsing deliberately fails at each malformed component.
func (k *KeyRing) Open(aad []byte, value string) (string, error) {
	if k == nil || value == "" || len(value) > MaxEnvelopeBytes {
		return "", ErrUnavailable
	}
	p := strings.Split(value, ".")
	if len(p) != 7 || p[0] != "mecatl" || p[1] != credentialEnvelopeVersion || p[2] != credentialEnvelopeAlg || !validCredentialKeyID(p[3]) {
		return "", ErrUnavailable
	}
	kek, ok := k.keys[p[3]]
	if !ok || len(kek) != KeyBytes {
		return "", ErrUnavailable
	}
	salt, err := decodeCredentialPart(p[4], credentialSaltBytes)
	if err != nil || len(salt) != credentialSaltBytes {
		return "", ErrUnavailable
	}
	payloadNonce, err := decodeCredentialPart(p[5], credentialNonceBytes)
	if err != nil || len(payloadNonce) != credentialNonceBytes {
		return "", ErrUnavailable
	}
	payload, err := decodeCredentialPart(p[6], MaxEnvelopeBytes)
	if err != nil || len(payload) < aes.BlockSize {
		return "", ErrUnavailable
	}
	meta := AAD("mecatl-toolhive-credential", credentialEnvelopeVersion, credentialEnvelopeAlg, p[3])
	key, err := hkdf.Key(sha256.New, kek, salt, string(meta), KeyBytes)
	if err != nil {
		return "", ErrUnavailable
	}
	defer clear(key)
	plain, err := openGCM(key, payloadNonce, aadWith(aad, meta), payload)
	if err != nil || len(plain) == 0 || len(plain) > MaxEnvelopeBytes {
		return "", ErrUnavailable
	}
	return string(plain), nil
}

func aadWith(first, second []byte) []byte { return append(append([]byte(nil), first...), second...) }

func sealGCM(key, nonce, aad, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plain, aad), nil
}

func openGCM(key, nonce, aad, cipherText []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, cipherText, aad)
}

func decodeCredentialPart(value string, maximum int) ([]byte, error) {
	if value == "" || len(value) > base64.RawURLEncoding.EncodedLen(maximum) {
		return nil, ErrUnavailable
	}
	out, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(out) > maximum {
		return nil, ErrUnavailable
	}
	return out, nil
}
