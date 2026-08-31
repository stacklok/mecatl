package identityissuer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// MaxTokenBytes bounds one compact JWT-SVID before parsing.
const MaxTokenBytes = 16 << 10

// BundleFetcher obtains the complete bundle from the configured HTTPS bootstrap URL.
// It must not return a partial response as a successful bundle.
type BundleFetcher interface {
	FetchBundle(context.Context, string) ([]byte, error)
}

// BundleFetcherFunc adapts a function into a BundleFetcher.
type BundleFetcherFunc func(context.Context, string) ([]byte, error)

// FetchBundle calls f to obtain one complete bundle.
func (f BundleFetcherFunc) FetchBundle(ctx context.Context, url string) ([]byte, error) {
	return f(ctx, url)
}

// VerifierConfig is independently configured verification policy. Its trust domain,
// audience, time bounds, and HTTPS bootstrap URL are never inferred from a bundle.
type VerifierConfig struct {
	TrustDomain       string
	Audience          string
	TokenTTL          time.Duration
	ClockSkew         time.Duration
	BundleCacheTTL    time.Duration
	HTTPSBootstrapURL string
	Now               func() time.Time
}

// VerifiedIdentity is the narrow identity projected from a verified JWT-SVID.
type VerifiedIdentity struct {
	TrustDomain string
	Subject     string
	Audience    string
}

// Verifier validates JWT-SVIDs only against its last complete, fresh bundle snapshot.
type Verifier struct {
	cfg     VerifierConfig
	fetcher BundleFetcher

	mu       sync.RWMutex
	keys     map[string]*ecdsa.PublicKey
	sequence uint64
	fetched  time.Time
}

// NewVerifier constructs an independently configured verifier. It does not fetch or
// trust a bundle until Refresh succeeds.
func NewVerifier(cfg VerifierConfig, fetcher BundleFetcher) (*Verifier, error) {
	if err := validateVerifierConfig(cfg); err != nil {
		return nil, err
	}
	if fetcher == nil {
		return nil, errors.New("identity issuer bundle fetcher is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Verifier{cfg: cfg, fetcher: fetcher}, nil
}

func validateVerifierConfig(cfg VerifierConfig) error {
	if err := ValidateTrustDomain(cfg.TrustDomain); err != nil {
		return fmt.Errorf("invalid identity verifier trust domain: %w", err)
	}
	if cfg.TokenTTL <= 0 || cfg.TokenTTL > maxTokenTTL {
		return errors.New("invalid identity verifier token TTL")
	}
	if cfg.ClockSkew <= 0 || cfg.ClockSkew > maxClockSkew || cfg.ClockSkew > cfg.TokenTTL {
		return errors.New("invalid identity verifier clock skew")
	}
	if cfg.BundleCacheTTL <= 0 || cfg.BundleCacheTTL > maxBundleCache {
		return errors.New("invalid identity verifier bundle cache bound")
	}
	if strings.TrimSpace(cfg.Audience) == "" || len(cfg.Audience) > 256 || strings.IndexFunc(cfg.Audience, func(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }) >= 0 {
		return errors.New("invalid identity verifier audience")
	}
	if err := validateHTTPSBootstrap(cfg.HTTPSBootstrapURL); err != nil {
		return fmt.Errorf("invalid identity verifier HTTPS bootstrap: %w", err)
	}
	return nil
}

// Refresh replaces the current snapshot only after a complete newer valid bundle has
// been fetched and decoded. A refresh failure retains a still-fresh prior snapshot.
func (v *Verifier) Refresh(ctx context.Context) error {
	bundle, err := v.fetcher.FetchBundle(ctx, v.cfg.HTTPSBootstrapURL)
	if err != nil {
		return fmt.Errorf("fetch identity issuer bundle: %w", err)
	}
	keys, sequence, err := parseBundle(bundle)
	if err != nil {
		return err
	}
	now := v.cfg.Now().UTC()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sequence != 0 && sequence <= v.sequence {
		return errors.New("identity issuer bundle sequence regressed")
	}
	v.keys, v.sequence, v.fetched = keys, sequence, now
	return nil
}

// Verify validates one compact ES256 JWT-SVID against a fresh complete snapshot.
func (v *Verifier) Verify(raw string) (VerifiedIdentity, error) {
	if len(raw) == 0 || len(raw) > MaxTokenBytes {
		return VerifiedIdentity{}, errors.New("identity issuer token size is invalid")
	}
	keys, err := v.freshKeys()
	if err != nil {
		return VerifiedIdentity{}, err
	}
	claims, err := strictRegisteredClaims(raw)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	kid, err := tokenKID(raw)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	key, ok := keys[kid]
	if !ok {
		return VerifiedIdentity{}, errors.New("identity issuer token key id is unknown")
	}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}), jwt.WithLeeway(v.cfg.ClockSkew), jwt.WithIssuedAt())
	parsed, err := parser.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodES256 {
			return nil, errors.New("identity issuer token algorithm is invalid")
		}
		return key, nil
	})
	if err != nil || parsed == nil || !parsed.Valid {
		return VerifiedIdentity{}, errors.New("identity issuer token signature or time is invalid")
	}
	if err := v.validateClaims(claims); err != nil {
		return VerifiedIdentity{}, err
	}
	return VerifiedIdentity{TrustDomain: v.cfg.TrustDomain, Subject: claims.Subject, Audience: v.cfg.Audience}, nil
}

func (v *Verifier) freshKeys() (map[string]*ecdsa.PublicKey, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.keys) == 0 || v.fetched.IsZero() || v.cfg.Now().UTC().Sub(v.fetched) > v.cfg.BundleCacheTTL {
		return nil, errors.New("identity issuer bundle is unavailable or stale")
	}
	return v.keys, nil
}

func (v *Verifier) validateClaims(claims *jwt.RegisteredClaims) error {
	if claims.Issuer != "spiffe://"+v.cfg.TrustDomain {
		return errors.New("identity issuer token issuer is invalid")
	}
	if err := validateSPIFFESubject(claims.Subject, v.cfg.TrustDomain); err != nil {
		return err
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != v.cfg.Audience {
		return errors.New("identity issuer token audience is invalid")
	}
	if claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		return errors.New("identity issuer token times are incomplete")
	}
	issuedAt, notBefore, expiresAt := claims.IssuedAt.Time, claims.NotBefore.Time, claims.ExpiresAt.Time
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > v.cfg.TokenTTL || notBefore.After(issuedAt.Add(v.cfg.ClockSkew)) || notBefore.After(expiresAt) {
		return errors.New("identity issuer token lifetime is invalid")
	}
	return nil
}

func tokenKID(raw string) (string, error) {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}))
	token, _, err := parser.ParseUnverified(raw, &jwt.RegisteredClaims{})
	if err != nil || token.Method != jwt.SigningMethodES256 {
		return "", errors.New("identity issuer token algorithm is invalid")
	}
	kid, ok := token.Header["kid"].(string)
	if !ok || kid == "" || len(kid) > 128 {
		return "", errors.New("identity issuer token key id is invalid")
	}
	return kid, nil
}

func strictRegisteredClaims(raw string) (*jwt.RegisteredClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("identity issuer token is malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || len(payload) > MaxTokenBytes {
		return nil, errors.New("identity issuer token payload is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var claims jwt.RegisteredClaims
	if err := decoder.Decode(&claims); err != nil {
		return nil, errors.New("identity issuer token claims are malformed")
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, errors.New("identity issuer token claims have trailing data")
	}
	return &claims, nil
}

func parseBundle(raw []byte) (map[string]*ecdsa.PublicKey, uint64, error) {
	if len(raw) == 0 || len(raw) > MaxBundleBytes {
		return nil, 0, errors.New("identity issuer bundle size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var bundle jwtBundle
	if err := decoder.Decode(&bundle); err != nil || ensureEOF(decoder) != nil {
		return nil, 0, errors.New("identity issuer bundle is malformed")
	}
	if bundle.Sequence == 0 || bundle.RefreshHint <= 0 || bundle.RefreshHint > int64(maxBundleCache/time.Second) || len(bundle.Keys) == 0 || len(bundle.Keys) > maxKeyItems {
		return nil, 0, errors.New("identity issuer bundle shape is invalid")
	}
	keys := make(map[string]*ecdsa.PublicKey, len(bundle.Keys))
	for _, jwk := range bundle.Keys {
		key, err := parsePublicJWK(jwk)
		if err != nil {
			return nil, 0, err
		}
		if _, duplicate := keys[jwk.KID]; duplicate {
			return nil, 0, errors.New("identity issuer bundle has duplicate key id")
		}
		keys[jwk.KID] = key
	}
	return keys, bundle.Sequence, nil
}

func parsePublicJWK(jwk publicJWK) (*ecdsa.PublicKey, error) {
	if jwk.KTY != "EC" || jwk.CRV != "P-256" || jwk.KID == "" || len(jwk.KID) > 128 {
		return nil, errors.New("identity issuer bundle key is invalid")
	}
	x, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil || len(x) != 32 {
		return nil, errors.New("identity issuer bundle key coordinate is invalid")
	}
	y, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil || len(y) != 32 {
		return nil, errors.New("identity issuer bundle key coordinate is invalid")
	}
	encoded := make([]byte, 1, 65)
	encoded[0] = 4
	encoded = append(encoded, x...)
	encoded = append(encoded, y...)
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), encoded)
	if err != nil {
		return nil, errors.New("identity issuer bundle key is not on P-256")
	}
	kid, err := publicJWKThumbprint(key)
	if err != nil || kid != jwk.KID {
		return nil, errors.New("identity issuer bundle key id does not match key")
	}
	return key, nil
}
