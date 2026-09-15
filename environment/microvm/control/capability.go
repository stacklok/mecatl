package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

var (
	// ErrUnauthenticatedCapability reports an invalid, stale, replayed, or wrongly bound guest capability.
	ErrUnauthenticatedCapability = errors.New("microvm guest capability is not authenticated")
)

const (
	capabilityVersion   = 1
	maxCapabilityNonces = 4096
)

type capabilityClaims struct {
	Version int     `json:"version"`
	Binding Binding `json:"binding"`
	Nonce   string  `json:"nonce"`
}

// CapabilityIssuer mints transferable, generation-bound guest capabilities.
// It contains only an independently provisioned signing key and shares no registry with the guest.
type CapabilityIssuer struct {
	key []byte
}

// NewCapabilityIssuer constructs an issuer from independently provisioned key material.
func NewCapabilityIssuer(key []byte) (*CapabilityIssuer, error) {
	if len(key) < sha256.Size {
		return nil, errors.New("microvm capability key is too short")
	}
	return &CapabilityIssuer{key: append([]byte(nil), key...)}, nil
}

// Issue mints a signed, single-use capability for one immutable binding.
func (i *CapabilityIssuer) Issue(binding Binding) (string, error) {
	if i == nil || binding.validate() != nil {
		return "", ErrUnauthenticatedCapability
	}
	var nonce [sha256.Size]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", fmt.Errorf("mint microvm guest capability: %w", err)
	}
	claims := capabilityClaims{Version: capabilityVersion, Binding: binding, Nonce: base64.RawURLEncoding.EncodeToString(nonce[:])}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode microvm guest capability: %w", err)
	}
	mac := hmac.New(sha256.New, i.key)
	_, _ = mac.Write(payload)
	signature := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// CapabilityVerifier verifies capabilities using its own key copy and rejects nonce replay locally.
type CapabilityVerifier struct {
	key  []byte
	mu   sync.Mutex
	used map[[sha256.Size]byte]Binding
}

// NewCapabilityVerifier constructs a guest-side verifier from provisioned key material.
func NewCapabilityVerifier(key []byte) (*CapabilityVerifier, error) {
	if len(key) < sha256.Size {
		return nil, errors.New("microvm capability key is too short")
	}
	return &CapabilityVerifier{key: append([]byte(nil), key...), used: make(map[[sha256.Size]byte]Binding)}, nil
}

// Verify authenticates and consumes a capability for expected.
func (v *CapabilityVerifier) Verify(token string, expected Binding) error {
	if v == nil || expected.validate() != nil {
		return ErrUnauthenticatedCapability
	}
	parts := splitCapability(token)
	if len(parts) != 2 {
		return ErrUnauthenticatedCapability
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrUnauthenticatedCapability
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ErrUnauthenticatedCapability
	}
	mac := hmac.New(sha256.New, v.key)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return ErrUnauthenticatedCapability
	}
	var claims capabilityClaims
	decoderErr := json.Unmarshal(payload, &claims)
	if decoderErr != nil || claims.Version != capabilityVersion || claims.Binding != expected || claims.Nonce == "" {
		return ErrUnauthenticatedCapability
	}
	nonce := sha256.Sum256([]byte(claims.Nonce))
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, replayed := v.used[nonce]; replayed {
		return ErrUnauthenticatedCapability
	}
	if len(v.used) >= maxCapabilityNonces {
		return ErrUnauthenticatedCapability
	}
	v.used[nonce] = expected
	return nil
}

// Forget removes replay state belonging to one unregistered logical binding.
// It does not affect capabilities consumed by sibling environments.
func (v *CapabilityVerifier) Forget(binding Binding) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for nonce, usedBinding := range v.used {
		if usedBinding == binding {
			delete(v.used, nonce)
		}
	}
}

func splitCapability(token string) []string {
	for index := range token {
		if token[index] == '.' {
			return []string{token[:index], token[index+1:]}
		}
	}
	return nil
}
