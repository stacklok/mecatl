package identitybroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Keys are the identity broker's operator-supplied keys, one per role. A
// deployment never generates them: a restart keeps every key, so the
// credentials, assertions, and tokens they sign keep verifying.
type Keys struct {
	// AgentIssuer signs agent credentials. It is always required.
	AgentIssuer crypto.Signer
	// AgentIssuerFallbacks are earlier agent issuer public keys, each ECDSA
	// P-256: credentials they signed still verify while they are configured
	// (WithAgentIssuerFallbacks).
	AgentIssuerFallbacks []crypto.PublicKey
	// BrokerClient signs the broker's client assertions at AS-1 and AS-2
	// (NewClientAssertionSigner). It is required with a hosted AS-1.
	BrokerClient crypto.Signer
	// BrokerClientKeySet is the broker client's public JWK Set that AS-1 and
	// AS-2 register for BrokerClientID. It must hold BrokerClient's public key
	// under its kid, and may hold another key during a rotation. It is
	// required with BrokerClient and only with it.
	BrokerClientKeySet []byte
	// AS1Signing are AS-1's signing key's public key, then its fallbacks'.
	// AS-1 reads the keys itself; the broker holds them only to refuse a key
	// used for two roles. The signing key is required with a hosted AS-1 and
	// refused without one.
	AS1Signing []crypto.PublicKey
}

// Host is what the process runs beside the broker, which decides the keys
// Keys.Validate requires and accepts.
type Host struct {
	// AS1 reports that the process hosts AS-1, which requires the broker
	// client key and AS-1's signing key; only a hosted AS-1 takes AS-1's
	// signing key.
	AS1 bool
}

// Validate checks k for a broker beside host. It refuses a missing required
// key, an AS-1 signing key without a hosted AS-1, an agent issuer fallback
// that is not ECDSA P-256, one key used for two roles, including any P-256
// key in the broker client's key set, and a broker client key set that holds
// no key or not the broker client key's public key under its kid. Errors wrap
// ErrInvalidConfig or ErrInvalidSigner and never contain key material.
//
//nolint:gocyclo // One ordered list of the file's key rules, kept together so each refusal reads in the order an operator meets it.
func (k Keys) Validate(host Host) error {
	if k.AgentIssuer == nil {
		return fmt.Errorf("%w: the agent issuer key is required", ErrInvalidConfig)
	}
	_, issuerPub, err := checkSigner(k.AgentIssuer)
	if err != nil {
		return fmt.Errorf("%w: the agent issuer key: %v", ErrInvalidSigner, err)
	}
	switch {
	case host.AS1 && k.BrokerClient == nil:
		return fmt.Errorf("%w: the broker client key is required with a hosted AS-1", ErrInvalidConfig)
	case host.AS1 && len(k.AS1Signing) == 0:
		return fmt.Errorf("%w: AS-1's signing key is required with a hosted AS-1", ErrInvalidConfig)
	case !host.AS1 && len(k.AS1Signing) != 0:
		return fmt.Errorf("%w: AS-1's signing key requires a hosted AS-1, which this process does not run", ErrInvalidConfig)
	case (k.BrokerClient == nil) != (len(k.BrokerClientKeySet) == 0):
		return fmt.Errorf("%w: the broker client key and the broker client's key set are required together", ErrInvalidConfig)
	}
	type roleKey struct {
		role string
		pub  crypto.PublicKey
	}
	keys := []roleKey{{"agent issuer", issuerPub}}
	for i, pub := range k.AgentIssuerFallbacks {
		// New refuses the same keys; checking here refuses them at load.
		if _, err := copyP256Public(pub); err != nil {
			return fmt.Errorf("%w: agent issuer fallback key %d: %v", ErrInvalidSigner, i, err)
		}
		keys = append(keys, roleKey{"agent issuer", pub})
	}
	if k.BrokerClient != nil {
		_, clientPub, err := checkSigner(k.BrokerClient)
		if err != nil {
			return fmt.Errorf("%w: the broker client key: %v", ErrInvalidSigner, err)
		}
		jwk, err := publicJWK(clientPub)
		if err != nil {
			return fmt.Errorf("%w: the broker client key: %v", ErrInvalidSigner, err)
		}
		// AS-1 and AS-2 accept assertions signed by any key in the set, so
		// each is a broker client key; the set holds clientPub.
		set, err := checkClientKeySet(k.BrokerClientKeySet, jwk)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
		for _, pub := range set {
			keys = append(keys, roleKey{"broker client", pub})
		}
	}
	for _, pub := range k.AS1Signing {
		keys = append(keys, roleKey{"AS-1 signing", pub})
	}
	for i, a := range keys {
		eq, ok := a.pub.(interface{ Equal(crypto.PublicKey) bool })
		if !ok {
			continue
		}
		if j := slices.IndexFunc(keys[i+1:], func(b roleKey) bool { return b.role != a.role && eq.Equal(b.pub) }); j >= 0 {
			return fmt.Errorf("%w: the %s key is also the %s key; each role needs its own key", ErrInvalidConfig, a.role, keys[i+1+j].role)
		}
	}
	return nil
}

// checkClientKeySet checks that raw, the broker client's JWK Set, holds at
// least one key and client under client's kid, and returns the set's ECDSA
// P-256 keys.
func checkClientKeySet(raw []byte, client JWK) ([]crypto.PublicKey, error) {
	var set JWKSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, errors.New("the broker client's key set is not a JSON JWK Set")
	}
	if len(set.Keys) == 0 {
		return nil, errors.New("the broker client's key set holds no key")
	}
	found := false
	var keys []crypto.PublicKey
	for _, key := range set.Keys {
		if key.KeyID == client.KeyID {
			if key.KeyType != client.KeyType || key.Curve != client.Curve || key.X != client.X || key.Y != client.Y {
				return nil, fmt.Errorf("the broker client's key set holds another key under the broker client key's kid %q", client.KeyID)
			}
			found = true
		}
		if pub, ok := p256JWKKey(key); ok {
			keys = append(keys, pub)
		}
	}
	if !found {
		return nil, fmt.Errorf("the broker client's key set does not hold the broker client key's public key under its kid %q (its RFC 7638 thumbprint)", client.KeyID)
	}
	return keys, nil
}

// p256JWKKey is jwk's public key when jwk is a valid ECDSA P-256 key.
func p256JWKKey(jwk JWK) (*ecdsa.PublicKey, bool) {
	if jwk.KeyType != "EC" || jwk.Curve != "P-256" {
		return nil, false
	}
	x, errX := base64.RawURLEncoding.DecodeString(jwk.X)
	y, errY := base64.RawURLEncoding.DecodeString(jwk.Y)
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, false
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
	return pub, err == nil
}

// WithAgentIssuerFallbacks has the broker also verify agent credentials
// signed by keys, earlier agent issuer keys, so credentials issued before a
// rotation keep verifying. Each must be an ECDSA P-256 public key; New
// copies them and refuses any other with ErrInvalidSigner.
func WithAgentIssuerFallbacks(keys ...crypto.PublicKey) Option {
	return func(b *Broker) { b.issuerFallbacks = append(b.issuerFallbacks, keys...) }
}
