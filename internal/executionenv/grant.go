package executionenv

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const grantAlgorithm = "Ed25519"

var (
	// ErrGrantExpired identifies the only grant failure eligible for a read-only refresh.
	ErrGrantExpired = errors.New("grant expired")
	// ErrGrantNotYetValid identifies a grant whose validity window has not started.
	ErrGrantNotYetValid = errors.New("grant not yet valid")
)

type grantHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

// GrantClaims binds a short-lived capability to one client, owner, environment, and epoch.
type GrantClaims struct {
	KeyID           string         `json:"-"`
	Issuer          string         `json:"iss"`
	Audience        string         `json:"aud"`
	Client          string         `json:"client"`
	OwnerHash       string         `json:"owner"`
	BindingID       string         `json:"binding_id"`
	RunID           string         `json:"run_id"`
	ClaimID         string         `json:"claim_id"`
	Environment     EnvironmentRef `json:"environment"`
	Epoch           uint64         `json:"epoch"`
	GrantGeneration uint64         `json:"grant_generation"`
	Operations      []Operation    `json:"operations"`
	NotBefore       time.Time      `json:"not_before"`
	ExpiresAt       time.Time      `json:"expires_at"`
	Nonce           string         `json:"nonce"`
}

// GrantExpectation defines the exact binding and operation required by a request.
type GrantExpectation struct {
	Client          string
	OwnerHash       string
	BindingID       string
	RunID           string
	ClaimID         string
	Environment     EnvironmentRef
	Epoch           uint64
	GrantGeneration uint64
	Operation       Operation
}

// GrantVerifier verifies signed grants against configured trust and revocation state.
type GrantVerifier struct {
	Keys          map[string]ed25519.PublicKey
	RevokedKeys   map[string]struct{}
	RevokedNonces map[string]struct{}
	Issuer        string
	Audience      string
	MaxLifetime   time.Duration
	Now           func() time.Time
}

// SignGrant validates and signs claims with Ed25519.
func SignGrant(key ed25519.PrivateKey, c GrantClaims) (string, error) {
	if len(key) != ed25519.PrivateKeySize {
		return "", errors.New("invalid Ed25519 private key")
	}
	if err := validateClaims(c); err != nil {
		return "", err
	}
	h, _ := json.Marshal(grantHeader{Algorithm: grantAlgorithm, KeyID: c.KeyID, Type: "MECATL-GRANT"})
	p, _ := json.Marshal(c)
	unsigned := rawURL(h) + "." + rawURL(p)
	sig := ed25519.Sign(key, []byte(unsigned))
	return unsigned + "." + rawURL(sig), nil
}

// Verify validates a grant and its exact request binding.
func (v GrantVerifier) Verify(token string, e GrantExpectation) (GrantClaims, error) { //nolint:gocyclo // Fail-closed claim validation is intentionally linear.
	var c GrantClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("malformed grant")
	}
	hb, err := decodeURL(parts[0])
	if err != nil {
		return c, errors.New("malformed grant header")
	}
	var h grantHeader
	if err := DecodeStrict(hb, &h); err != nil {
		return c, errors.New("malformed grant header")
	}
	if h.Algorithm != grantAlgorithm || h.Type != "MECATL-GRANT" || h.KeyID == "" {
		return c, errors.New("unsupported grant header")
	}
	if _, ok := v.RevokedKeys[h.KeyID]; ok {
		return c, errors.New("grant key revoked")
	}
	key, ok := v.Keys[h.KeyID]
	if !ok || len(key) != ed25519.PublicKeySize {
		return c, errors.New("unknown grant key")
	}
	sig, err := decodeURL(parts[2])
	if err != nil || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return c, errors.New("invalid grant signature")
	}
	pb, err := decodeURL(parts[1])
	if err != nil {
		return c, errors.New("malformed grant claims")
	}
	if err := DecodeStrict(pb, &c); err != nil {
		return c, errors.New("malformed grant claims")
	}
	c.KeyID = h.KeyID
	if err := validateClaims(c); err != nil {
		return c, err
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	if c.Issuer != v.Issuer || c.Audience != v.Audience {
		return c, errors.New("grant issuer or audience mismatch")
	}
	if now.Before(c.NotBefore) {
		return c, ErrGrantNotYetValid
	}
	if !now.Before(c.ExpiresAt) {
		return c, ErrGrantExpired
	}
	maxLifetime := v.MaxLifetime
	if maxLifetime <= 0 {
		maxLifetime = 5 * time.Minute
	}
	if c.ExpiresAt.Sub(c.NotBefore) > maxLifetime {
		return c, errors.New("grant lifetime exceeds limit")
	}
	if _, ok := v.RevokedNonces[c.Nonce]; ok {
		return c, errors.New("grant revoked")
	}
	if c.Client != e.Client || c.OwnerHash != e.OwnerHash || c.BindingID != e.BindingID || c.RunID != e.RunID || c.ClaimID != e.ClaimID || c.Environment != e.Environment || c.Epoch != e.Epoch || c.GrantGeneration != e.GrantGeneration {
		return c, errors.New("grant binding mismatch")
	}
	for _, op := range c.Operations {
		if op == e.Operation {
			return c, nil
		}
	}
	return c, errors.New("grant does not authorize operation")
}

//nolint:gocyclo // Complete fail-closed claim validation is deliberately linear.
func validateClaims(c GrantClaims) error {
	if c.KeyID == "" || c.Issuer == "" || c.Audience == "" || c.Client == "" || c.OwnerHash == "" || c.BindingID == "" || c.RunID == "" || c.ClaimID == "" || c.Environment.ID == "" || c.Environment.Revision == "" || c.Epoch == 0 || c.GrantGeneration == 0 || c.Nonce == "" || c.NotBefore.IsZero() || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(c.NotBefore) {
		return errors.New("incomplete grant claims")
	}
	if len(c.Operations) == 0 || len(c.Operations) > 32 {
		return errors.New("invalid grant operations")
	}
	seen := map[Operation]struct{}{}
	for _, op := range c.Operations {
		if !op.Valid() {
			return fmt.Errorf("invalid grant operation %q", op)
		}
		if _, ok := seen[op]; ok {
			return errors.New("duplicate grant operation")
		}
		seen[op] = struct{}{}
	}
	return nil
}
func rawURL(b []byte) string             { return base64.RawURLEncoding.EncodeToString(b) }
func decodeURL(s string) ([]byte, error) { return base64.RawURLEncoding.Strict().DecodeString(s) }
