package identitybroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// keySetOf is the JSON JWK Set of jwks.
func keySetOf(t *testing.T, jwks ...JWK) []byte {
	t.Helper()
	raw, err := json.Marshal(JWKSet{Keys: jwks})
	if err != nil {
		t.Fatalf("marshal key set: %v", err)
	}
	return raw
}

func jwkOf(t *testing.T, label string) JWK {
	t.Helper()
	jwk, err := publicJWK(&testKey(t, label).PublicKey)
	if err != nil {
		t.Fatalf("publicJWK: %v", err)
	}
	return jwk
}

func p384Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// validKeys are a hosted AS-1 deployment's keys, one per role.
func validKeys(t *testing.T) Keys {
	t.Helper()
	return Keys{
		AgentIssuer:          testKey(t, "agent issuer"),
		AgentIssuerFallbacks: []crypto.PublicKey{testKey(t, "old agent issuer").Public()},
		BrokerClient:         testKey(t, "broker client"),
		BrokerClientKeySet:   keySetOf(t, jwkOf(t, "next broker client"), jwkOf(t, "broker client")),
		AS1Signing:           []crypto.PublicKey{testKey(t, "AS-1").Public(), testKey(t, "old AS-1").Public()},
	}
}

func TestKeysValidateAcceptsOneKeyPerRole(t *testing.T) {
	t.Parallel()
	if err := validKeys(t).Validate(true); err != nil {
		t.Fatalf("Validate(hosted AS-1) = %v", err)
	}
	agentOnly := Keys{AgentIssuer: testKey(t, "agent issuer")}
	if err := agentOnly.Validate(false); err != nil {
		t.Fatalf("Validate(agent issuer key alone, no hosted AS-1) = %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	withRSA := validKeys(t)
	withRSA.AS1Signing = []crypto.PublicKey{rsaKey.Public()}
	if err := withRSA.Validate(true); err != nil {
		t.Fatalf("Validate(RSA AS-1 signing key) = %v", err)
	}
}

func TestKeysValidateRefuses(t *testing.T) {
	t.Parallel()
	shared := testKey(t, "shared")
	tests := map[string]struct {
		edit   func(*Keys)
		hosted bool
		want   string
	}{
		"no agent issuer key": {edit: func(k *Keys) { k.AgentIssuer = nil }, want: "agent issuer key is required"},
		"P-384 agent issuer key": {
			edit: func(k *Keys) { k.AgentIssuer = p384Key(t) }, want: "P-256",
		},
		"no broker client key with a hosted AS-1": {
			edit: func(k *Keys) { k.BrokerClient, k.BrokerClientKeySet = nil, nil }, hosted: true, want: "broker client key is required",
		},
		"no AS-1 signing key with a hosted AS-1": {
			edit: func(k *Keys) { k.AS1Signing = nil }, hosted: true, want: "AS-1's signing key is required",
		},
		"a key set without the broker client key": {
			edit: func(k *Keys) { k.BrokerClientKeySet = nil }, want: "required together",
		},
		"a broker client key set without the key": {
			edit: func(k *Keys) { k.BrokerClient = nil }, want: "required together",
		},
		"an empty key set": {
			edit: func(k *Keys) { k.BrokerClientKeySet = []byte(`{"keys":[]}`) }, want: "holds no key",
		},
		"a key set that is not JSON": {
			edit: func(k *Keys) { k.BrokerClientKeySet = []byte(`not json`) }, want: "not a JSON JWK Set",
		},
		"a key set without the broker client's public key": {
			edit: func(k *Keys) { k.BrokerClientKeySet = keySetOf(t, jwkOf(t, "someone else")) }, want: "does not hold the broker client key's public key",
		},
		"a key set with another key under the broker client's kid": {
			edit: func(k *Keys) {
				other := jwkOf(t, "someone else")
				other.KeyID = jwkOf(t, "broker client").KeyID
				k.BrokerClientKeySet = keySetOf(t, other)
			},
			want: "holds another key under the broker client key's kid",
		},
		"agent issuer key is the broker client key": {
			edit: func(k *Keys) {
				k.AgentIssuer, k.BrokerClient = shared, shared
				k.BrokerClientKeySet = keySetOf(t, jwkOf(t, "shared"))
			},
			want: "agent issuer key is also the broker client key",
		},
		"agent issuer key is AS-1's": {
			edit: func(k *Keys) { k.AgentIssuer, k.AS1Signing = shared, []crypto.PublicKey{shared.Public()} },
			want: "agent issuer key is also the AS-1 signing key",
		},
		"AS-1's fallback is the broker client key": {
			edit: func(k *Keys) { k.AS1Signing = append(k.AS1Signing, testKey(t, "broker client").Public()) },
			want: "broker client key is also the AS-1 signing key",
		},
		"an agent issuer fallback is AS-1's key": {
			edit: func(k *Keys) { k.AgentIssuerFallbacks = []crypto.PublicKey{testKey(t, "AS-1").Public()} },
			want: "agent issuer key is also the AS-1 signing key",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			keys := validKeys(t)
			tc.edit(&keys)
			err := keys.Validate(tc.hosted)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.want)
			}
			if !errors.Is(err, ErrInvalidConfig) && !errors.Is(err, ErrInvalidSigner) {
				t.Errorf("Validate = %v, want ErrInvalidConfig or ErrInvalidSigner", err)
			}
		})
	}
}

func TestAgentIssuerFallbackVerifiesCredentialsOfTheRotatedKey(t *testing.T) {
	t.Parallel()
	f := newCredentialFixture(t) // signs with f.key
	next := testKey(t, "next agent issuer")

	rotated, err := New(testConfig(), nil, next, WithAgentIssuerFallbacks(f.key.Public()))
	if err != nil {
		t.Fatalf("New with a fallback: %v", err)
	}
	if _, err := rotated.issuer.verify(f.reg.Credential, f.now); err != nil {
		t.Errorf("a credential signed by the fallback key = %v, want it to verify", err)
	}

	withoutFallback, err := New(testConfig(), nil, next)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := withoutFallback.issuer.verify(f.reg.Credential, f.now); !errors.Is(err, ReasonError(ReasonDenied, "")) {
		t.Errorf("a credential signed by a key no longer configured = %v, want denied", err)
	}
}

func TestWithAgentIssuerFallbacksRefusesANonP256Key(t *testing.T) {
	t.Parallel()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]crypto.PublicKey{"RSA": rsaKey.Public(), "P-384": p384Key(t).Public(), "nil": nil} {
		if _, err := New(testConfig(), nil, testKey(t, "agent issuer"), WithAgentIssuerFallbacks(key)); !errors.Is(err, ErrInvalidSigner) {
			t.Errorf("%s fallback: New = %v, want ErrInvalidSigner", name, err)
		}
	}
}
