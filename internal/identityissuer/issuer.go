// Package identityissuer provides the broker-host-only JWT-SVID signing substrate.
package identityissuer

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// MaxManifestBytes bounds one immutable keyring generation manifest.
	MaxManifestBytes = 64 << 10
	maxKeyItems      = 16
	maxKeyBytes      = 16 << 10
	maxTokenTTL      = time.Hour
	maxClockSkew     = 5 * time.Minute
	maxBundleCache   = 10 * time.Minute
)

var errInvalidConfig = errors.New("invalid identity issuer configuration")

// Config is the complete local issuer configuration. TrustDomain is never derived
// from a host, origin, bundle, or Kubernetes metadata source.
type Config struct {
	Enabled           bool
	TrustDomain       string
	TokenTTL          time.Duration
	ClockSkew         time.Duration
	BundleCacheTTL    time.Duration
	Audience          string
	HTTPSBootstrapURL string
}

// KeyLoader reads one named PKCS#8 item from the already-selected immutable generation.
type KeyLoader func(name string) ([]byte, error)

type manifest struct {
	Version int           `json:"version"`
	Keys    []manifestKey `json:"keys"`
}

type manifestKey struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// Issuer contains a fully validated immutable key generation.
type Issuer struct {
	trustDomain    string
	audience       string
	tokenTTL       time.Duration
	clockSkew      time.Duration
	key            *ecdsa.PrivateKey
	kid            string
	bundleMu       sync.Mutex
	bundleSequence uint64
}

// Load validates configuration before reading any key item, then constructs an issuer
// only after every manifest and key validation succeeds.
func Load(cfg Config, manifestBytes []byte, loader KeyLoader) (*Issuer, error) {
	if !cfg.Enabled {
		return nil, fmt.Errorf("%w: issuer is disabled", errInvalidConfig)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if loader == nil {
		return nil, errors.New("identity issuer key loader is required")
	}
	entries, err := parseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}

	var active *ecdsa.PrivateKey
	for _, entry := range entries {
		der, err := loader(entry.Name)
		if err != nil {
			return nil, fmt.Errorf("load key %q: %w", entry.Name, err)
		}
		key, err := parseP256PKCS8(der)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", entry.Name, err)
		}
		if entry.Active {
			active = key
		}
	}
	if active == nil {
		return nil, errors.New("identity issuer manifest has no active signer")
	}
	kid, err := publicJWKThumbprint(&active.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("derive active key id: %w", err)
	}
	return &Issuer{
		trustDomain: cfg.TrustDomain,
		audience:    cfg.Audience,
		tokenTTL:    cfg.TokenTTL,
		clockSkew:   cfg.ClockSkew,
		key:         active,
		kid:         kid,
	}, nil
}

func validateConfig(cfg Config) error {
	if err := ValidateTrustDomain(cfg.TrustDomain); err != nil {
		return fmt.Errorf("%w: trust domain: %v", errInvalidConfig, err)
	}
	if cfg.TokenTTL <= 0 || cfg.TokenTTL > maxTokenTTL {
		return fmt.Errorf("%w: token TTL must be within (0,%s]", errInvalidConfig, maxTokenTTL)
	}
	if cfg.ClockSkew <= 0 || cfg.ClockSkew > maxClockSkew || cfg.ClockSkew > cfg.TokenTTL {
		return fmt.Errorf("%w: clock skew must be within (0,min(%s,T)]", errInvalidConfig, maxClockSkew)
	}
	if cfg.BundleCacheTTL <= 0 || cfg.BundleCacheTTL > maxBundleCache {
		return fmt.Errorf("%w: bundle cache bound must be within (0,%s]", errInvalidConfig, maxBundleCache)
	}
	if strings.TrimSpace(cfg.Audience) == "" || len(cfg.Audience) > 256 || strings.IndexFunc(cfg.Audience, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%w: registered audience is invalid", errInvalidConfig)
	}
	if err := validateHTTPSBootstrap(cfg.HTTPSBootstrapURL); err != nil {
		return fmt.Errorf("%w: HTTPS bootstrap: %v", errInvalidConfig, err)
	}
	return nil
}

func validateHTTPSBootstrap(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" {
		return errors.New("must be a complete HTTPS URL")
	}
	if u.Port() != "" && u.Port() != "443" {
		return errors.New("must not use a non-default port")
	}
	return nil
}

// ValidateTrustDomain validates the SPIFFE DNS trust-domain grammar.
func ValidateTrustDomain(trustDomain string) error {
	if trustDomain == "" || len(trustDomain) > 253 || net.ParseIP(trustDomain) != nil || strings.Contains(trustDomain, ".") && strings.HasSuffix(trustDomain, ".") {
		return errors.New("must be a DNS trust domain")
	}
	for _, label := range strings.Split(trustDomain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("must be a DNS trust domain")
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return errors.New("must be a DNS trust domain")
			}
		}
	}
	return nil
}

func parseManifest(data []byte) ([]manifestKey, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return nil, errors.New("identity issuer manifest size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var parsed manifest
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode identity issuer manifest: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	if parsed.Version != 1 || len(parsed.Keys) == 0 || len(parsed.Keys) > maxKeyItems {
		return nil, errors.New("identity issuer manifest shape is invalid")
	}
	names := make(map[string]struct{}, len(parsed.Keys))
	active := 0
	for _, key := range parsed.Keys {
		if !validKeyName(key.Name) {
			return nil, errors.New("identity issuer key name is invalid")
		}
		if _, duplicate := names[key.Name]; duplicate {
			return nil, errors.New("identity issuer manifest has duplicate key names")
		}
		names[key.Name] = struct{}{}
		if key.Active {
			active++
		}
	}
	if active != 1 {
		return nil, errors.New("identity issuer manifest must contain exactly one active signer")
	}
	return parsed.Keys, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("identity issuer manifest has trailing data")
		}
		return fmt.Errorf("decode identity issuer manifest trailing data: %w", err)
	}
	return nil
}

func validKeyName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') || (i == 0 && (r == '-' || r == '_')) {
			return false
		}
	}
	return true
}

func parseP256PKCS8(der []byte) (*ecdsa.PrivateKey, error) {
	if len(der) == 0 || len(der) > maxKeyBytes {
		return nil, errors.New("PKCS#8 key size is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, errors.New("must be a PKCS#8 private key")
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() || key.D == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
		return nil, errors.New("must be a P-256 private key")
	}
	return key, nil
}

func publicJWKThumbprint(key *ecdsa.PublicKey) (string, error) {
	if key == nil || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
		return "", errors.New("invalid P-256 public key")
	}
	coordinateSize := (key.Curve.Params().BitSize + 7) / 8
	x := key.X.FillBytes(make([]byte, coordinateSize))
	y := key.Y.FillBytes(make([]byte, coordinateSize))
	canonical := `{"crv":"P-256","kty":"EC","x":"` + base64.RawURLEncoding.EncodeToString(x) + `","y":"` + base64.RawURLEncoding.EncodeToString(y) + `"}`
	digest := sha256.Sum256([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// TrustDomain returns the configured explicit SPIFFE trust domain.
func (i *Issuer) TrustDomain() string { return i.trustDomain }

// ActiveKID returns the RFC 7638 public-JWK thumbprint of the active signer.
func (i *Issuer) ActiveKID() string { return i.kid }

// Algorithm returns the only supported signing algorithm.
func (i *Issuer) Algorithm() string { return jwt.SigningMethodES256.Alg() }

// PublicKey returns a copy of the active public verification key.
func (i *Issuer) PublicKey() *ecdsa.PublicKey {
	return &ecdsa.PublicKey{Curve: i.key.Curve, X: new(big.Int).Set(i.key.X), Y: new(big.Int).Set(i.key.Y)}
}

// IssueJWTSubject mints a JWT-SVID only for the configured local subject, audience,
// and bounded lifetime. The issuer owns the headers, claims, signing key, and timestamps.
func (i *Issuer) IssueJWTSubject(subject, audience string, ttl time.Duration) (string, error) {
	if err := validateSPIFFESubject(subject, i.trustDomain); err != nil {
		return "", err
	}
	if audience != i.audience {
		return "", errors.New("identity issuer audience is not registered")
	}
	if ttl <= 0 || ttl > i.tokenTTL {
		return "", errors.New("identity issuer TTL is outside configured bound")
	}
	now := time.Now().UTC()
	claims := jwt.RegisteredClaims{
		Issuer:    "spiffe://" + i.trustDomain,
		Subject:   subject,
		Audience:  jwt.ClaimStrings{i.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-i.clockSkew)),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = i.kid
	return token.SignedString(i.key)
}

func validateSPIFFESubject(subject, trustDomain string) error {
	u, err := url.Parse(subject)
	if err != nil || u.Scheme != "spiffe" || u.Host != trustDomain || u.User != nil || u.Port() != "" || u.Path == "" || !strings.HasPrefix(u.Path, "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("identity issuer subject must be a local SPIFFE ID")
	}
	return nil
}
