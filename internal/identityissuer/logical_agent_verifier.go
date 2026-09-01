package identityissuer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// VerifiedLogicalAgent is the complete typed identity returned after a logical-agent
// JWT-SVID profile has passed signature and profile validation.
type VerifiedLogicalAgent struct {
	TrustDomain string
	Subject     string
	Tier        DefinitionTier
	Name        string
	Instance    string
	Tools       []string
	JWTID       string
	Expiry      time.Time
}

// LogicalAgentVerifier independently verifies only the logical-agent/v1 JWT-SVID
// profile. It deliberately does not accept the registered-claims-only canary profile.
type LogicalAgentVerifier struct {
	verifier *Verifier
}

// NewLogicalAgentVerifier constructs an independently configured verifier for the
// logical-agent/v1 profile. It does not fetch a bundle until Refresh succeeds.
func NewLogicalAgentVerifier(cfg VerifierConfig, fetcher BundleFetcher) (*LogicalAgentVerifier, error) {
	verifier, err := NewVerifier(cfg, fetcher)
	if err != nil {
		return nil, err
	}
	return &LogicalAgentVerifier{verifier: verifier}, nil
}

// Refresh replaces the verification bundle only after a complete newer bundle succeeds.
func (v *LogicalAgentVerifier) Refresh(ctx context.Context) error {
	if v == nil || v.verifier == nil {
		return errors.New("logical agent verifier is unavailable")
	}
	return v.verifier.Refresh(ctx)
}

// Verify validates one compact logical-agent/v1 JWT-SVID and returns only its typed
// verified identity. It never returns partially decoded claims on an error.
func (v *LogicalAgentVerifier) Verify(raw string) (VerifiedLogicalAgent, error) {
	if v == nil || v.verifier == nil || len(raw) == 0 || len(raw) > MaxTokenBytes {
		return VerifiedLogicalAgent{}, errors.New("logical agent token size is invalid")
	}
	keys, err := v.verifier.freshKeys()
	if err != nil {
		return VerifiedLogicalAgent{}, err
	}
	header, claims, signing, signature, err := parseLogicalAgentJWT(raw)
	if err != nil {
		return VerifiedLogicalAgent{}, err
	}
	key, ok := keys[header.KID]
	if !ok {
		return VerifiedLogicalAgent{}, errors.New("logical agent token key id is unknown")
	}
	if err := jwt.SigningMethodES256.Verify(signing, signature, key); err != nil {
		return VerifiedLogicalAgent{}, errors.New("logical agent token signature is invalid")
	}
	if err := v.validateClaims(claims); err != nil {
		return VerifiedLogicalAgent{}, err
	}
	identity, err := NewLogicalAgentIdentity(v.verifier.cfg.TrustDomain, claims.LogicalAgent.DefinitionTier, claims.LogicalAgent.DefinitionName)
	if err != nil || identity.Subject != claims.Subject {
		return VerifiedLogicalAgent{}, errors.New("logical agent token subject is invalid")
	}
	return VerifiedLogicalAgent{
		TrustDomain: v.verifier.cfg.TrustDomain,
		Subject:     identity.Subject,
		Tier:        claims.LogicalAgent.DefinitionTier,
		Name:        claims.LogicalAgent.DefinitionName,
		Instance:    claims.LogicalAgent.Instance,
		Tools:       append([]string(nil), claims.LogicalAgent.Tools...),
		JWTID:       claims.ID,
		Expiry:      claims.ExpiresAt.Time.UTC(),
	}, nil
}

type logicalAgentJWTHeader struct {
	Algorithm string
	KID       string
}

type verifiedLogicalAgentClaims struct {
	jwt.RegisteredClaims
	LogicalAgent LogicalAgentClaim
}

func parseLogicalAgentJWT(raw string) (logicalAgentJWTHeader, verifiedLogicalAgentClaims, string, []byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return logicalAgentJWTHeader{}, verifiedLogicalAgentClaims{}, "", nil, errors.New("logical agent token is malformed")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(headerJSON) == 0 {
		return logicalAgentJWTHeader{}, verifiedLogicalAgentClaims{}, "", nil, errors.New("logical agent token header is malformed")
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payloadJSON) == 0 || len(payloadJSON) > MaxTokenBytes {
		return logicalAgentJWTHeader{}, verifiedLogicalAgentClaims{}, "", nil, errors.New("logical agent token payload is malformed")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) == 0 {
		return logicalAgentJWTHeader{}, verifiedLogicalAgentClaims{}, "", nil, errors.New("logical agent token signature is malformed")
	}
	header, err := parseLogicalAgentHeader(headerJSON)
	if err != nil {
		return logicalAgentJWTHeader{}, verifiedLogicalAgentClaims{}, "", nil, err
	}
	claims, err := parseVerifiedLogicalAgentClaims(payloadJSON)
	if err != nil {
		return logicalAgentJWTHeader{}, verifiedLogicalAgentClaims{}, "", nil, err
	}
	return header, claims, parts[0] + "." + parts[1], signature, nil
}

func parseLogicalAgentHeader(raw []byte) (logicalAgentJWTHeader, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return logicalAgentJWTHeader{}, errors.New("logical agent token header is malformed")
	}
	var header logicalAgentJWTHeader
	seen := make(map[string]bool, 3)
	for decoder.More() {
		name, err := strictJSONName(decoder, seen)
		if err != nil {
			return logicalAgentJWTHeader{}, errors.New("logical agent token header is malformed")
		}
		switch name {
		case "alg":
			err = decoder.Decode(&header.Algorithm)
		case "kid":
			err = decoder.Decode(&header.KID)
		case "typ":
			var typ string
			err = decoder.Decode(&typ)
			if err == nil && typ != "JWT" {
				err = errors.New("invalid type")
			}
		default:
			return logicalAgentJWTHeader{}, errors.New("logical agent token header is malformed")
		}
		if err != nil {
			return logicalAgentJWTHeader{}, errors.New("logical agent token header is malformed")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || ensureEOF(decoder) != nil || !seen["alg"] || !seen["kid"] || header.Algorithm != jwt.SigningMethodES256.Alg() || header.KID == "" || len(header.KID) > 128 {
		return logicalAgentJWTHeader{}, errors.New("logical agent token header is malformed")
	}
	return header, nil
}

func parseVerifiedLogicalAgentClaims(raw []byte) (verifiedLogicalAgentClaims, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are malformed")
	}
	var claims verifiedLogicalAgentClaims
	seen := make(map[string]bool, 8)
	for decoder.More() {
		name, err := strictJSONName(decoder, seen)
		if err != nil {
			return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are malformed")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are malformed")
		}
		switch name {
		case "iss":
			err = json.Unmarshal(value, &claims.Issuer)
		case "sub":
			err = json.Unmarshal(value, &claims.Subject)
		case "aud":
			err = json.Unmarshal(value, &claims.Audience)
		case "exp":
			claims.ExpiresAt, err = parseNumericDate(value)
		case "nbf":
			claims.NotBefore, err = parseNumericDate(value)
		case "iat":
			claims.IssuedAt, err = parseNumericDate(value)
		case "jti":
			err = json.Unmarshal(value, &claims.ID)
		case LogicalAgentClaimURI:
			claims.LogicalAgent, err = ParseLogicalAgentClaim(value)
		default:
			if strings.HasPrefix(name, "https://mecatl.dev/claims/logical-agent/") {
				return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are malformed")
			}
		}
		if err != nil {
			return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are malformed")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || ensureEOF(decoder) != nil {
		return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are malformed")
	}
	for _, required := range []string{"iss", "sub", "aud", "exp", "nbf", "iat", "jti", LogicalAgentClaimURI} {
		if !seen[required] {
			return verifiedLogicalAgentClaims{}, errors.New("logical agent token claims are incomplete")
		}
	}
	return claims, nil
}

func strictJSONName(decoder *json.Decoder, seen map[string]bool) (string, error) {
	token, err := decoder.Token()
	name, ok := token.(string)
	if err != nil || !ok || seen[name] {
		return "", errors.New("duplicate or invalid JSON member")
	}
	seen[name] = true
	return name, nil
}

func parseNumericDate(value json.RawMessage) (*jwt.NumericDate, error) {
	var date jwt.NumericDate
	if err := json.Unmarshal(value, &date); err != nil {
		return nil, errors.New("invalid numeric date")
	}
	return &date, nil
}

func (v *LogicalAgentVerifier) validateClaims(claims verifiedLogicalAgentClaims) error {
	if err := v.verifier.validateClaims(&claims.RegisteredClaims); err != nil {
		return err
	}
	if claims.ID == "" || len(claims.ID) != base64.RawURLEncoding.EncodedLen(16) || strings.Contains(claims.ID, "=") {
		return errors.New("logical agent token ID is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(claims.ID)
	if err != nil || len(decoded) != 16 || base64.RawURLEncoding.EncodeToString(decoded) != claims.ID {
		return errors.New("logical agent token ID is invalid")
	}
	now := v.verifier.cfg.Now().UTC()
	if now.After(claims.ExpiresAt.Time.Add(v.verifier.cfg.ClockSkew)) || now.Before(claims.NotBefore.Time.Add(-v.verifier.cfg.ClockSkew)) || now.Before(claims.IssuedAt.Time.Add(-v.verifier.cfg.ClockSkew)) {
		return errors.New("logical agent token time is invalid")
	}
	return nil
}
