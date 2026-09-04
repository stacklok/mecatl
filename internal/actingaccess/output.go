package actingaccess

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// ActingAccessTokenType is the sole JWT protected-header type for issued access.
	ActingAccessTokenType = "mecatl-acting-access+jwt"                      //nolint:gosec // JWT type, not a credential.
	accessTokenType       = "urn:ietf:params:oauth:token-type:access_token" //nolint:gosec // OAuth token type, not a credential.
	bearerTokenType       = "Bearer"
	claimIssuer           = "iss"
	claimSubject          = "sub"
	claimIssuedAt         = "iat"
	claimNotBefore        = "nbf"
	claimAudience         = "aud"
	claimExpiresAt        = "exp"
)

// ExchangeResponse is the closed RFC 8693 access-token response.
type ExchangeResponse struct {
	token           OutputToken
	issuedTokenType string
	tokenType       string
	expiresIn       int64
	scope           string
	refreshToken    string // parser/verifier guard: issuance never sets this.
}

// Token returns the issued opaque access token.
func (r ExchangeResponse) Token() OutputToken { return r.token }

// IssuedTokenType returns the RFC 8693 issued-token type.
func (r ExchangeResponse) IssuedTokenType() string { return r.issuedTokenType }

// TokenType returns the output bearer token type.
func (r ExchangeResponse) TokenType() string { return r.tokenType }

// ExpiresIn returns the response lifetime in seconds.
func (r ExchangeResponse) ExpiresIn() int64 { return r.expiresIn }

// Scope returns the canonical effective response scopes.
func (r ExchangeResponse) Scope() string { return r.scope }

func validExchangeResponse(r ExchangeResponse, plan MechanismInput, now time.Time) bool {
	return r.token.secret.value != nil && r.issuedTokenType == accessTokenType && r.tokenType == bearerTokenType &&
		r.expiresIn > 0 && r.refreshToken == "" && r.scope == strings.Join(plan.Scopes(), " ") &&
		r.expiresIn <= int64(plan.NotAfter().Sub(now)/time.Second)
}

// OutputIssuerConfig fixes deterministic offline ES256 output issuance.
type OutputIssuerConfig struct {
	Issuer     string
	ClientID   string
	KeyID      string
	PrivateKey *ecdsa.PrivateKey
	Now        func() time.Time
}

// DeterministicOutputIssuer is an offline-only compact RFC 8693 mechanism.
type DeterministicOutputIssuer struct {
	issuer, clientID, keyID string
	privateKey              *ecdsa.PrivateKey
	now                     func() time.Time
	calls                   int
	omitExpiry              bool
}

// NewDeterministicOutputIssuer constructs the test-only deterministic issuer.
func NewDeterministicOutputIssuer(cfg OutputIssuerConfig) (*DeterministicOutputIssuer, error) {
	if !canonicalIssuer(cfg.Issuer) || !canonicalToken(cfg.ClientID) || !boundedSafe(cfg.KeyID) || cfg.PrivateKey == nil ||
		cfg.PrivateKey.Curve != elliptic.P256() {
		return nil, errors.New("acting-access output issuer configuration is invalid")
	}
	if _, err := cfg.PrivateKey.Bytes(); err != nil {
		return nil, errors.New("acting-access output issuer configuration is invalid")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &DeterministicOutputIssuer{issuer: cfg.Issuer, clientID: cfg.ClientID, keyID: cfg.KeyID, privateKey: cfg.PrivateKey, now: now}, nil
}

// Exchange issues only the already-authorized exact plan; credentials are deliberately unused.
func (i *DeterministicOutputIssuer) Exchange(_ context.Context, plan MechanismInput, _ SubjectAssertion, _ I2Token) (ExchangeResponse, error) {
	if i == nil || plan.NotAfter().IsZero() {
		return ExchangeResponse{}, errors.New("acting-access output plan is invalid")
	}
	i.calls++
	now := i.now().UTC()
	if !plan.NotAfter().After(now) {
		return ExchangeResponse{}, errors.New("acting-access output lifetime is invalid")
	}
	token := i.mintClaims(plan, nil)
	expires := int64(plan.NotAfter().Sub(now) / time.Second)
	if i.omitExpiry {
		expires = 0
	}
	return ExchangeResponse{token: token, issuedTokenType: accessTokenType, tokenType: bearerTokenType, expiresIn: expires, scope: strings.Join(plan.Scopes(), " ")}, nil
}

func (i *DeterministicOutputIssuer) mintClaims(plan MechanismInput, overrides map[string]any) OutputToken {
	token, err := i.mintToken(plan, overrides)
	if err != nil {
		return OutputToken{}
	}
	return token
}

func (i *DeterministicOutputIssuer) mintToken(plan MechanismInput, overrides map[string]any) (OutputToken, error) {
	now := i.now().UTC()
	claims := map[string]any{
		claimIssuer: i.issuer, claimSubject: qualifiedOutputSubject(plan.Owner()), claimAudience: plan.Resource().Value(),
		claimIssuedAt: now.Unix(), claimNotBefore: now.Unix(), claimExpiresAt: plan.NotAfter().Unix(),
		"act": map[string]any{claimSubject: plan.ActorSubject()}, "client_id": i.clientID,
		"scope": strings.Join(plan.Scopes(), " "), "authorization_details": plan.Detail().Value(),
	}
	for key, value := range overrides {
		claims[key] = value
	}
	raw := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims(claims))
	raw.Header["typ"] = ActingAccessTokenType
	raw.Header["kid"] = i.keyID
	signed, err := raw.SignedString(i.privateKey)
	if err != nil {
		return OutputToken{}, err
	}
	return NewOutputToken(signed)
}

// OutputVerifierConfig fixes the independent closed output profile verifier.
type OutputVerifierConfig struct {
	Issuer    string
	ClientID  string
	KeyID     string
	PublicKey *ecdsa.PublicKey
	Now       func() time.Time
	ClockSkew time.Duration
}

// JWTOutputVerifier independently verifies compact issued output with public material.
type JWTOutputVerifier struct {
	issuer, clientID, keyID string
	publicKey               *ecdsa.PublicKey
	now                     func() time.Time
	clockSkew               time.Duration
}

// NewJWTOutputVerifier constructs an independent verifier from public output-key material.
func NewJWTOutputVerifier(cfg OutputVerifierConfig) (*JWTOutputVerifier, error) {
	if !canonicalIssuer(cfg.Issuer) || !canonicalToken(cfg.ClientID) || !boundedSafe(cfg.KeyID) || cfg.PublicKey == nil ||
		cfg.PublicKey.Curve != elliptic.P256() || cfg.ClockSkew < 0 || cfg.ClockSkew > 5*time.Minute {
		return nil, errors.New("acting-access output verifier configuration is invalid")
	}
	encoded, err := cfg.PublicKey.Bytes()
	if err != nil {
		return nil, errors.New("acting-access output verifier configuration is invalid")
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), encoded)
	if err != nil {
		return nil, errors.New("acting-access output verifier configuration is invalid")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &JWTOutputVerifier{issuer: cfg.Issuer, clientID: cfg.ClientID, keyID: cfg.KeyID, publicKey: key, now: now, clockSkew: cfg.ClockSkew}, nil
}

// VerifiedOutput is the complete non-secret, exact verified issued authority.
type VerifiedOutput struct {
	subject, actorSubject, clientID, audience, detail string
	scopes                                            []string
	expiresAt                                         time.Time
}

// Subject returns the domain-separated owner identifier.
func (v VerifiedOutput) Subject() string { return v.subject }

// ActorSubject returns the exact logical actor subject.
func (v VerifiedOutput) ActorSubject() string { return v.actorSubject }

// ClientID returns the AS-derived client attribution.
func (v VerifiedOutput) ClientID() string { return v.clientID }

// Audience returns the exact registered resource audience.
func (v VerifiedOutput) Audience() string { return v.audience }

// Detail returns the exact registered operation detail.
func (v VerifiedOutput) Detail() string { return v.detail }

// Scopes returns a copy of canonical granted scopes.
func (v VerifiedOutput) Scopes() []string { return append([]string(nil), v.scopes...) }

// ExpiresAt returns the verified output expiry.
func (v VerifiedOutput) ExpiresAt() time.Time { return v.expiresAt }

// Verify accepts only a correctly signed, closed output profile matching the exact plan.
func (v *JWTOutputVerifier) Verify(response ExchangeResponse, plan MechanismInput) (VerifiedOutput, error) {
	if v == nil || !validExchangeResponse(response, plan, v.now().UTC()) || response.token.secret.value == nil {
		return VerifiedOutput{}, errors.New("acting-access output response is invalid")
	}
	claims, err := v.verify(response.token.secret.value.raw)
	if err != nil {
		return VerifiedOutput{}, err
	}
	now := v.now().UTC()
	expires := time.Unix(claims.exp, 0).UTC()
	if claims.issuer != v.issuer || claims.subject != qualifiedOutputSubject(plan.Owner()) || claims.clientID != v.clientID ||
		claims.actor != plan.ActorSubject() || claims.audience != plan.Resource().Value() || claims.detail != plan.Detail().Value() ||
		!equalStrings(claims.scopes, plan.Scopes()) || !expires.Equal(plan.NotAfter()) || !expires.After(now.Add(-v.clockSkew)) ||
		claims.iat > claims.nbf || claims.nbf > claims.exp || now.Before(time.Unix(claims.nbf, 0).Add(-v.clockSkew)) || now.Before(time.Unix(claims.iat, 0).Add(-v.clockSkew)) {
		return VerifiedOutput{}, errors.New("acting-access output claims are invalid")
	}
	return VerifiedOutput{subject: claims.subject, actorSubject: claims.actor, clientID: claims.clientID, audience: claims.audience, detail: claims.detail, scopes: append([]string(nil), claims.scopes...), expiresAt: expires}, nil
}

func qualifiedOutputSubject(owner Owner) string {
	sum := sha256.Sum256([]byte("mecatl:acting-access:owner:v1\x00" + owner.Issuer() + "\x00" + owner.Subject()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func equalStrings(a, b []string) bool {
	return len(a) == len(b) && strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

type outputHeader struct{ kid string }

func (v *JWTOutputVerifier) verify(raw string) (outputClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return outputClaims{}, errors.New("acting-access output is malformed")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return outputClaims{}, errors.New("acting-access output header is malformed")
	}
	header, err := parseOutputHeader(headerRaw)
	if err != nil || header.kid != v.keyID {
		return outputClaims{}, errors.New("acting-access output header is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || jwt.SigningMethodES256.Verify(parts[0]+"."+parts[1], signature, v.publicKey) != nil {
		return outputClaims{}, errors.New("acting-access output signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return outputClaims{}, errors.New("acting-access output claims are malformed")
	}
	return parseOutputClaims(payload)
}
func parseOutputHeader(raw []byte) (outputHeader, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return outputHeader{}, errors.New("header")
	}
	seen := map[string]bool{}
	var header outputHeader
	for decoder.More() {
		name, err := uniqueName(decoder, seen)
		if err != nil {
			return outputHeader{}, err
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return outputHeader{}, err
		}
		switch name {
		case "alg":
			if value != jwt.SigningMethodES256.Alg() {
				return outputHeader{}, errors.New("alg")
			}
		case "typ":
			if value != ActingAccessTokenType {
				return outputHeader{}, errors.New("typ")
			}
		case "kid":
			header.kid = value
		default:
			return outputHeader{}, errors.New("header")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || ensureJSONEOF(decoder) != nil || !seen["alg"] || !seen["typ"] || !seen["kid"] || !boundedSafe(header.kid) {
		return outputHeader{}, errors.New("header")
	}
	return header, nil
}

type outputClaims struct {
	issuer, subject, actor, clientID, audience, detail string
	scopes                                             []string
	iat, nbf, exp                                      int64
}

//nolint:gocyclo // Closed claim parsing enumerates every accepted member and rejects all others.
func parseOutputClaims(raw []byte) (outputClaims, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return outputClaims{}, errors.New("claims")
	}
	seen := map[string]bool{}
	var claims outputClaims
	for decoder.More() {
		name, err := uniqueName(decoder, seen)
		if err != nil {
			return outputClaims{}, err
		}
		switch name {
		case "iss":
			err = decoder.Decode(&claims.issuer)
		case claimSubject:
			err = decoder.Decode(&claims.subject)
		case "aud":
			err = decoder.Decode(&claims.audience)
		case "iat":
			err = decoder.Decode(&claims.iat)
		case "nbf":
			err = decoder.Decode(&claims.nbf)
		case "exp":
			err = decoder.Decode(&claims.exp)
		case "client_id":
			err = decoder.Decode(&claims.clientID)
		case "scope":
			var scope string
			err = decoder.Decode(&scope)
			claims.scopes = strings.Fields(scope)
			if scope != strings.Join(claims.scopes, " ") {
				return outputClaims{}, errors.New("scope")
			}
		case "authorization_details":
			err = decoder.Decode(&claims.detail)
		case "act":
			err = parseOutputAct(decoder, &claims.actor)
		default:
			return outputClaims{}, errors.New("unprofiled")
		}
		if err != nil {
			return outputClaims{}, errors.New("claims")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || ensureJSONEOF(decoder) != nil {
		return outputClaims{}, errors.New("claims")
	}
	for _, required := range []string{"iss", "sub", "aud", "iat", "nbf", "exp", "client_id", "scope", "authorization_details", "act"} {
		if !seen[required] {
			return outputClaims{}, errors.New("incomplete")
		}
	}
	if !boundedSafe(claims.subject) || !canonicalToken(claims.clientID) || !canonicalToken(claims.audience) || !canonicalToken(claims.detail) || !canonicalStrings(claims.scopes, canonicalToken) || !boundedSafe(claims.actor) {
		return outputClaims{}, errors.New("claims")
	}
	return claims, nil
}
func parseOutputAct(decoder *json.Decoder, actor *string) error {
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return errors.New("act")
	}
	seen := map[string]bool{}
	for decoder.More() {
		name, err := uniqueName(decoder, seen)
		if err != nil {
			return err
		}
		if name != "sub" {
			return errors.New("act")
		}
		if err := decoder.Decode(actor); err != nil {
			return err
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || !seen["sub"] {
		return errors.New("act")
	}
	return nil
}
