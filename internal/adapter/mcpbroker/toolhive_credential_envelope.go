package mcpbroker

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

//nolint:gosec // Stable Redis namespace, not credential material.
const credentialAADNamespace = "mecatl:authserver:"

const (
	credentialEnvelopeVersion = "v1"
	credentialEnvelopeAlg     = "a256gcm"
	credentialKeyBytes        = 32
	credentialSaltBytes       = 32
	credentialNonceBytes      = 12
	maxCredentialEnvelope     = 1 << 20
)

var errCredentialEnvelope = errors.New("mcpbroker: credential envelope unavailable")

type credentialKeyRing struct {
	activeID string
	keys     map[string][]byte
}

func newCredentialKeyRing(activeID string, keys map[string][]byte) (*credentialKeyRing, error) {
	if !validCredentialKeyID(activeID) || len(keys) == 0 {
		return nil, errCredentialEnvelope
	}
	out := &credentialKeyRing{activeID: activeID, keys: make(map[string][]byte, len(keys))}
	for id, key := range keys {
		if !validCredentialKeyID(id) || len(key) != credentialKeyBytes {
			return nil, errCredentialEnvelope
		}
		out.keys[id] = bytes.Clone(key)
	}
	if _, ok := out.keys[activeID]; !ok {
		return nil, errCredentialEnvelope
	}
	return out, nil
}

func validCredentialKeyID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// credentialAAD is canonical length-delimited framing, so distinct logical rows
// cannot produce the same binding value.
func credentialAAD(parts ...string) []byte {
	var b strings.Builder
	for _, part := range parts {
		fmt.Fprintf(&b, "%d:", len(part))
		b.WriteString(part)
	}
	return []byte(b.String())
}

func (k *credentialKeyRing) seal(aad []byte, plaintext string) (string, error) {
	if k == nil || plaintext == "" || len(plaintext) > maxCredentialEnvelope {
		return "", errCredentialEnvelope
	}
	kek := k.keys[k.activeID]
	if len(kek) != credentialKeyBytes {
		return "", errCredentialEnvelope
	}
	salt := make([]byte, credentialSaltBytes)
	payloadNonce := make([]byte, credentialNonceBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", errCredentialEnvelope
	}
	if _, err := rand.Read(payloadNonce); err != nil {
		return "", errCredentialEnvelope
	}
	meta := credentialAAD("mecatl-toolhive-credential", credentialEnvelopeVersion, credentialEnvelopeAlg, k.activeID)
	key, err := hkdf.Key(sha256.New, kek, salt, string(meta), credentialKeyBytes)
	if err != nil {
		return "", errCredentialEnvelope
	}
	defer clear(key)
	payload, err := sealGCM(key, payloadNonce, aadWith(aad, meta), []byte(plaintext))
	if err != nil {
		return "", errCredentialEnvelope
	}
	out := strings.Join([]string{"mecatl", credentialEnvelopeVersion, credentialEnvelopeAlg, k.activeID,
		base64.RawURLEncoding.EncodeToString(salt), base64.RawURLEncoding.EncodeToString(payloadNonce),
		base64.RawURLEncoding.EncodeToString(payload)}, ".")
	if len(out) > maxCredentialEnvelope {
		return "", errCredentialEnvelope
	}
	return out, nil
}

// open validates every untrusted envelope segment before decrypting it.
//
//nolint:gocyclo // Strict envelope parsing deliberately fails at each malformed component.
func (k *credentialKeyRing) open(aad []byte, value string) (string, error) {
	if k == nil || value == "" || len(value) > maxCredentialEnvelope {
		return "", errCredentialEnvelope
	}
	p := strings.Split(value, ".")
	if len(p) != 7 || p[0] != "mecatl" || p[1] != credentialEnvelopeVersion || p[2] != credentialEnvelopeAlg || !validCredentialKeyID(p[3]) {
		return "", errCredentialEnvelope
	}
	kek, ok := k.keys[p[3]]
	if !ok || len(kek) != credentialKeyBytes {
		return "", errCredentialEnvelope
	}
	salt, err := decodeCredentialPart(p[4], credentialSaltBytes)
	if err != nil || len(salt) != credentialSaltBytes {
		return "", errCredentialEnvelope
	}
	payloadNonce, err := decodeCredentialPart(p[5], credentialNonceBytes)
	if err != nil || len(payloadNonce) != credentialNonceBytes {
		return "", errCredentialEnvelope
	}
	payload, err := decodeCredentialPart(p[6], maxCredentialEnvelope)
	if err != nil || len(payload) < aes.BlockSize {
		return "", errCredentialEnvelope
	}
	meta := credentialAAD("mecatl-toolhive-credential", credentialEnvelopeVersion, credentialEnvelopeAlg, p[3])
	key, err := hkdf.Key(sha256.New, kek, salt, string(meta), credentialKeyBytes)
	if err != nil {
		return "", errCredentialEnvelope
	}
	defer clear(key)
	plain, err := openGCM(key, payloadNonce, aadWith(aad, meta), payload)
	if err != nil || len(plain) == 0 || len(plain) > maxCredentialEnvelope {
		return "", errCredentialEnvelope
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
		return nil, errCredentialEnvelope
	}
	out, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(out) > maximum {
		return nil, errCredentialEnvelope
	}
	return out, nil
}
