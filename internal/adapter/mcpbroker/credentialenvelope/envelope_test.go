package credentialenvelope

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// This fixture was sealed by the pre-extraction implementation using the
// synthetic test key; opening it pins the persisted format and row/field AAD.
func TestCredentialEnvelope_MigrationVector(t *testing.T) {
	const ciphertext = "mecatl.v1.a256gcm.key-a.2I8xQ-Q7qJygTFodc-oXXTohkN8ucMo1BBnUO4z85n8.JsQTJPNrRQUJkPzL.Txh7bDMZ80ZHcN5KDzoH1ByHO-dXWVrzaYCWjwsq0iU"
	aad := AAD("mecatl:authserver:", "upstream", "migration-session", "provider", "access")
	if got, err := testCredentialKeyRing(t).Open(aad, ciphertext); err != nil || got != "migration-canary" {
		t.Fatalf("pre-extraction ciphertext open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_KeyIDsRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyBytes)
	for _, id := range []string{"key-2026", "key_2026", "Key0123", strings.Repeat("a", 128)} {
		t.Run(id, func(t *testing.T) {
			ring, err := NewKeyRing(id, map[string][]byte{id: key})
			if err != nil {
				t.Fatal(err)
			}
			aad := AAD("session", "provider", "access")
			value := sealCredentialForTest(t, ring, aad, "key-id-canary")
			if got, err := ring.Open(aad, value); err != nil || got != "key-id-canary" {
				t.Fatalf("accepted key ID did not round trip: %v", err)
			}
		})
	}
}

func TestCredentialEnvelope_RejectsDottedKeyIDs(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, KeyBytes)
	for _, active := range []string{"key.2026", "key-valid"} {
		t.Run(active, func(t *testing.T) {
			keys := map[string][]byte{"key.2026": key, "key-valid": key}
			if ring, err := NewKeyRing(active, keys); !errors.Is(err, ErrUnavailable) || ring != nil {
				t.Fatal("accepted a dotted active or inactive key ID")
			}
		})
	}
}

func TestCredentialEnvelope_RoundTripsWithPerSealSalt(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "access-token")
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
	got, err := ring.Open(aad, value)
	if err != nil || got != "credential-canary" {
		t.Fatalf("open = %q, %v", got, err)
	}
	again := sealCredentialForTest(t, ring, aad, "credential-canary")
	againParts := strings.Split(again, ".")
	if again == value || againParts[4] == parts[4] || againParts[5] == parts[5] {
		t.Fatal("two seals of the same value reused a salt or nonce")
	}
}

func TestCredentialEnvelope_TamperedCiphertextFailsClosed(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "refresh-token")
	parts := strings.Split(sealCredentialForTest(t, ring, aad, "refresh-canary"), ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[6])
	if err != nil {
		t.Fatal(err)
	}
	payload[0] ^= 0x80
	parts[6] = base64.RawURLEncoding.EncodeToString(payload)
	if got, err := ring.Open(aad, strings.Join(parts, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("tampered envelope open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_UnknownOrRetiredKeyIDFailsClosed(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "id-token")
	sealed := strings.Split(sealCredentialForTest(t, ring, aad, "id-token-canary"), ".")

	for name, mutate := range map[string]func([]string){
		"version":        func(parts []string) { parts[1] = "v2" },
		"algorithm":      func(parts []string) { parts[2] = "a128gcm" },
		"unknown key ID": func(parts []string) { parts[3] = "missing-key" },
	} {
		t.Run(name, func(t *testing.T) {
			parts := append([]string(nil), sealed...)
			mutate(parts)
			if got, err := ring.Open(aad, strings.Join(parts, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
				t.Fatalf("open = %q, %v", got, err)
			}
		})
	}

	retired, err := NewKeyRing("key-b", map[string][]byte{"key-b": bytes.Repeat([]byte("b"), KeyBytes)})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := retired.Open(aad, strings.Join(sealed, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("retired key open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_MalformedSegmentsRejectedBeforeDecrypt(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "subject")
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
		"oversized envelope": func(parts []string) { parts[6] = strings.Repeat("A", MaxEnvelopeBytes) },
		"wrong prefix":       func(parts []string) { parts[0] = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			parts := append([]string(nil), sealed...)
			mutate(parts)
			if got, err := ring.Open(aad, strings.Join(parts, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
				t.Fatalf("open = %q, %v", got, err)
			}
		})
	}
	if got, err := ring.Open(aad, strings.Repeat("x", MaxEnvelopeBytes+1)); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("over-limit envelope open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_AADMismatchFailsClosed(t *testing.T) {
	ring := testCredentialKeyRing(t)
	value := sealCredentialForTest(t, ring, AAD("session", "provider", "access-token"), "access-canary")
	if got, err := ring.Open(AAD("session", "provider", "refresh-token"), value); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("AAD mismatch open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_SaltIsBoundIntoKeyDerivation(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "access-token")
	parts := strings.Split(sealCredentialForTest(t, ring, aad, "access-canary"), ".")
	salt, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	salt[0] ^= 0x01
	parts[4] = base64.RawURLEncoding.EncodeToString(salt)
	if got, err := ring.Open(aad, strings.Join(parts, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("salt-tampered open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_RetiredButPresentKeyStillOpensAfterRotation(t *testing.T) {
	keyA := bytes.Repeat([]byte("a"), KeyBytes)
	keyB := bytes.Repeat([]byte("b"), KeyBytes)
	oldRing, err := NewKeyRing("key-a", map[string][]byte{"key-a": keyA})
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD("session", "provider", "access-token")
	oldEnvelope := sealCredentialForTest(t, oldRing, aad, "access-canary")

	rotatedRing, err := NewKeyRing("key-b", map[string][]byte{"key-a": keyA, "key-b": keyB})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rotatedRing.Open(aad, oldEnvelope); err != nil || got != "access-canary" {
		t.Fatalf("open with retained key = %q, %v", got, err)
	}
	newParts := strings.Split(sealCredentialForTest(t, rotatedRing, aad, "new-access-canary"), ".")
	if newParts[3] != "key-b" {
		t.Fatalf("new envelope key ID = %q, want active key-b", newParts[3])
	}
}

func TestCredentialEnvelope_RejectsNonCanonicalBase64(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "access-token")
	parts := strings.Split(sealCredentialForTest(t, ring, aad, "access-canary"), ".")
	// A 32-byte salt encodes to 43 characters whose final character carries two
	// unused bits. Setting one decodes to the same bytes unless decoding is strict.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, parts[4][len(parts[4])-1])
	parts[4] = parts[4][:len(parts[4])-1] + string(alphabet[last|1])
	if got, err := ring.Open(aad, strings.Join(parts, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("non-canonical salt open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_SwappedKeyIDFailsClosed(t *testing.T) {
	keyA := bytes.Repeat([]byte("a"), KeyBytes)
	keyB := bytes.Repeat([]byte("b"), KeyBytes)
	ring, err := NewKeyRing("key-a", map[string][]byte{"key-a": keyA, "key-b": keyB})
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD("session", "provider", "access-token")
	parts := strings.Split(sealCredentialForTest(t, ring, aad, "access-canary"), ".")
	parts[3] = "key-b"
	if got, err := ring.Open(aad, strings.Join(parts, ".")); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("relabelled key ID open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_AADFramingKeepsPartsDistinct(t *testing.T) {
	if bytes.Equal(AAD("ab", "c"), AAD("a", "bc")) {
		t.Fatal("AAD framing let different part boundaries collide")
	}
	ring := testCredentialKeyRing(t)
	value := sealCredentialForTest(t, ring, AAD("ab", "c"), "canary")
	if got, err := ring.Open(AAD("a", "bc"), value); !errors.Is(err, ErrUnavailable) || got != "" {
		t.Fatalf("shifted AAD boundary open = %q, %v", got, err)
	}
}

func TestCredentialEnvelope_NewKeyRingRejectsInvalidInput(t *testing.T) {
	good := bytes.Repeat([]byte("k"), KeyBytes)
	for name, tc := range map[string]struct {
		active string
		keys   map[string][]byte
	}{
		"no keys":            {active: "key-a", keys: map[string][]byte{}},
		"empty active ID":    {keys: map[string][]byte{"key-a": good}},
		"active key missing": {active: "key-b", keys: map[string][]byte{"key-a": good}},
		"short key":          {active: "key-a", keys: map[string][]byte{"key-a": good[:KeyBytes-1]}},
		"long key":           {active: "key-a", keys: map[string][]byte{"key-a": append(bytes.Clone(good), 0)}},
	} {
		t.Run(name, func(t *testing.T) {
			if ring, err := NewKeyRing(tc.active, tc.keys); !errors.Is(err, ErrUnavailable) || ring != nil {
				t.Fatalf("NewKeyRing = %v, %v", ring, err)
			}
		})
	}
	mutable := bytes.Clone(good)
	ring, err := NewKeyRing("key-a", map[string][]byte{"key-a": mutable})
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD("session")
	value := sealCredentialForTest(t, ring, aad, "canary")
	clear(mutable)
	if got, err := ring.Open(aad, value); err != nil || got != "canary" {
		t.Fatalf("ring shared caller key memory: %q, %v", got, err)
	}
}

func TestCredentialEnvelope_SealAndOpenRejectUnusableInput(t *testing.T) {
	ring := testCredentialKeyRing(t)
	aad := AAD("session", "provider", "access-token")
	value := sealCredentialForTest(t, ring, aad, "canary")
	var nilRing *KeyRing
	for name, fn := range map[string]func() error{
		"nil ring seal":   func() error { _, err := nilRing.Seal(aad, "canary"); return err },
		"nil ring open":   func() error { _, err := nilRing.Open(aad, value); return err },
		"empty plaintext": func() error { _, err := ring.Seal(aad, ""); return err },
		"empty seal AAD":  func() error { _, err := ring.Seal(nil, "canary"); return err },
		"empty open AAD":  func() error { _, err := ring.Open(nil, value); return err },
		"oversized plaintext": func() error {
			_, err := ring.Seal(aad, strings.Repeat("x", MaxPlaintextBytes+1))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := fn(); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v, want ErrUnavailable", err)
			}
		})
	}
}

func TestCredentialEnvelope_MaxPlaintextFitsEnvelopeLimit(t *testing.T) {
	id := strings.Repeat("a", 128)
	ring, err := NewKeyRing(id, map[string][]byte{id: bytes.Repeat([]byte("k"), KeyBytes)})
	if err != nil {
		t.Fatal(err)
	}
	aad := AAD("session")
	plain := strings.Repeat("x", MaxPlaintextBytes)
	value := sealCredentialForTest(t, ring, aad, plain)
	if len(value) > MaxEnvelopeBytes {
		t.Fatalf("envelope is %d bytes, over the %d limit", len(value), MaxEnvelopeBytes)
	}
	if got, err := ring.Open(aad, value); err != nil || got != plain {
		t.Fatalf("maximum plaintext did not round trip: %v", err)
	}
}

func testCredentialKeyRing(t *testing.T) *KeyRing {
	t.Helper()
	ring, err := NewKeyRing("key-a", map[string][]byte{"key-a": []byte(strings.Repeat("k", 32))})
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func sealCredentialForTest(t *testing.T, ring *KeyRing, aad []byte, plaintext string) string {
	t.Helper()
	value, err := ring.Seal(aad, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
