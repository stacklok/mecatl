package mcpbroker

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCredentialEnvelope_RoundTripsWithPerSealSalt(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := credentialAAD("session", "provider", "access-token")
	value := sealCredentialForTest(t, ring, aad, "credential-canary")
	parts := strings.Split(value, ".")
	if len(parts) != 7 {
		t.Fatalf("envelope has %d segments, want 7", len(parts))
	}
	if parts[0] != "mecatl" || parts[1] != credentialEnvelopeVersion || parts[2] != credentialEnvelopeAlg || parts[3] != "key-a" {
		t.Fatalf("envelope prefix/key ID = %v", parts[:4])
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != credentialSaltBytes {
		t.Fatalf("salt length = %d, decode error = %v", len(salt), err)
	}
	got, err := ring.open(aad, value)
	if err != nil || got != "credential-canary" {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_TamperedCiphertextFailsClosed(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := credentialAAD("session", "provider", "refresh-token")
	parts := strings.Split(sealCredentialForTest(t, ring, aad, "refresh-canary"), ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[6])
	if err != nil {
		t.Fatal(err)
	}
	payload[0] ^= 0x80
	parts[6] = base64.RawURLEncoding.EncodeToString(payload)
	if got, err := ring.open(aad, strings.Join(parts, ".")); !errors.Is(err, errCredentialEnvelope) || got != "" {
		t.Fatalf("tampered envelope open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_UnknownOrRetiredKeyIDFailsClosed(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := credentialAAD("session", "provider", "id-token")
	sealed := strings.Split(sealCredentialForTest(t, ring, aad, "id-token-canary"), ".")

	for name, mutate := range map[string]func([]string){
		"version":        func(parts []string) { parts[1] = "v2" },
		"algorithm":      func(parts []string) { parts[2] = "a128gcm" },
		"unknown key ID": func(parts []string) { parts[3] = "missing-key" },
	} {
		t.Run(name, func(t *testing.T) {
			parts := append([]string(nil), sealed...)
			mutate(parts)
			if got, err := ring.open(aad, strings.Join(parts, ".")); !errors.Is(err, errCredentialEnvelope) || got != "" {
				t.Fatalf("open = %q, %v", got, err)
			}
		})
	}

	retired, err := newCredentialKeyRing("key-b", map[string][]byte{"key-b": bytes.Repeat([]byte("b"), credentialKeyBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := retired.open(aad, strings.Join(sealed, ".")); !errors.Is(err, errCredentialEnvelope) || got != "" {
		t.Fatalf("retired key open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_MalformedSegmentsRejectedBeforeDecrypt(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := credentialAAD("session", "provider", "subject")
	sealed := strings.Split(sealCredentialForTest(t, ring, aad, "subject-canary"), ".")

	shortPayload := make([]byte, 15)
	longSalt := make([]byte, credentialSaltBytes+1)
	shortNonce := make([]byte, credentialNonceBytes-1)
	longNonce := make([]byte, credentialNonceBytes+1)
	cases := map[string]func([]string){
		"segment count":   func(parts []string) { parts[6] = "extra.segment" },
		"non-base64 salt": func(parts []string) { parts[4] = "%%%" },
		"truncated salt": func(parts []string) {
			parts[4] = base64.RawURLEncoding.EncodeToString(longSalt[:credentialSaltBytes-1])
		},
		"overlong salt":      func(parts []string) { parts[4] = base64.RawURLEncoding.EncodeToString(longSalt) },
		"non-base64 nonce":   func(parts []string) { parts[5] = "%%%" },
		"truncated nonce":    func(parts []string) { parts[5] = base64.RawURLEncoding.EncodeToString(shortNonce) },
		"overlong nonce":     func(parts []string) { parts[5] = base64.RawURLEncoding.EncodeToString(longNonce) },
		"non-base64 payload": func(parts []string) { parts[6] = "%%%" },
		"truncated payload":  func(parts []string) { parts[6] = base64.RawURLEncoding.EncodeToString(shortPayload) },
		"oversized envelope": func(parts []string) { parts[6] = strings.Repeat("A", maxCredentialEnvelope) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			parts := append([]string(nil), sealed...)
			mutate(parts)
			if got, err := ring.open(aad, strings.Join(parts, ".")); !errors.Is(err, errCredentialEnvelope) || got != "" {
				t.Fatalf("open = %q, %v", got, err)
			}
		})
	}
	if got, err := ring.open(aad, strings.Repeat("x", maxCredentialEnvelope+1)); !errors.Is(err, errCredentialEnvelope) || got != "" {
		t.Fatalf("over-limit envelope open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_AADMismatchFailsClosed(t *testing.T) {
	ring := testCredentialKeyRing(t)
	value := sealCredentialForTest(t, ring, credentialAAD("session", "provider", "access-token"), "access-canary")
	if got, err := ring.open(credentialAAD("session", "provider", "refresh-token"), value); !errors.Is(err, errCredentialEnvelope) || got != "" {
		t.Fatalf("AAD mismatch open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_SaltIsBoundIntoKeyDerivation(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := credentialAAD("session", "provider", "access-token")
	parts := strings.Split(sealCredentialForTest(t, ring, aad, "access-canary"), ".")
	salt, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	salt[0] ^= 0x01
	parts[4] = base64.RawURLEncoding.EncodeToString(salt)
	if got, err := ring.open(aad, strings.Join(parts, ".")); !errors.Is(err, errCredentialEnvelope) || got != "" {
		t.Fatalf("salt-tampered open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_RetiredButPresentKeyStillOpensAfterRotation(t *testing.T) {
	keyA := bytes.Repeat([]byte("a"), credentialKeyBytes)
	keyB := bytes.Repeat([]byte("b"), credentialKeyBytes)
	oldRing, err := newCredentialKeyRing("key-a", map[string][]byte{"key-a": keyA})
	if err != nil {
		t.Fatal(err)
	}
	aad := credentialAAD("session", "provider", "access-token")
	oldEnvelope := sealCredentialForTest(t, oldRing, aad, "access-canary")

	rotatedRing, err := newCredentialKeyRing("key-b", map[string][]byte{"key-a": keyA, "key-b": keyB})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotatedRing.open(aad, oldEnvelope); err != nil || got != "access-canary" {
		t.Fatalf("open with retained key = %q, %v", got, err)
	}
	newParts := strings.Split(sealCredentialForTest(t, rotatedRing, aad, "new-access-canary"), ".")
	if newParts[3] != "key-b" {
		t.Fatalf("new envelope key ID = %q, want active key-b", newParts[3])
	}
}

func testCredentialKeyRing(t *testing.T) *credentialKeyRing {
	t.Helper()
	ring, err := newCredentialKeyRing("key-a", map[string][]byte{"key-a": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func sealCredentialForTest(t *testing.T, ring *credentialKeyRing, aad []byte, plaintext string) string {
	t.Helper()
	value, err := ring.seal(aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
