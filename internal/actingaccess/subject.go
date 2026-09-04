package actingaccess

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ExchangeSubjectTokenType is the only protected-header type accepted by the
// exchange-subject profile. Ordinary access and ID tokens use different types.
const ExchangeSubjectTokenType = "mecatl-exchange-subject+jwt"

// SubjectAssertionVerifier is the consumer-owned closed subject credential port.
type SubjectAssertionVerifier interface {
	Verify(SubjectAssertion) (VerifiedSubject, error)
}

// VerifiedSubject is the complete non-secret result of subject profile verification.
type VerifiedSubject struct {
	owner        Owner
	consentProof string
	notAfter     time.Time
}

// Owner returns the verified issuer-qualified identity.
func (v VerifiedSubject) Owner() Owner { return v.owner }

// ConsentProof returns the bounded, non-secret consent reference.
func (v VerifiedSubject) ConsentProof() string { return v.consentProof }

// NotAfter returns the verified assertion expiry.
func (v VerifiedSubject) NotAfter() time.Time { return v.notAfter }

// IsZero reports whether no verified subject facts are present.
func (v VerifiedSubject) IsZero() bool {
	return v.owner == (Owner{}) && v.consentProof == "" && v.notAfter.IsZero()
}

// SubjectKey is one pinned ES256 subject-profile verification key.
type SubjectKey struct {
	ID        string
	PublicKey *ecdsa.PublicKey
}

// SubjectVerifierConfig fixes one bilateral exchange-subject profile.
type SubjectVerifierConfig struct {
	Issuer          string
	Audience        string
	AuthorizedParty string
	MaxAge          time.Duration
	ClockSkew       time.Duration
	MaxTokenBytes   int
	Now             func() time.Time
	Keys            []SubjectKey
}

// JWTSubjectAssertionVerifier verifies only the closed exchange-subject JWT profile.
type JWTSubjectAssertionVerifier struct {
	issuer          string
	audience        string
	authorizedParty string
	maxAge          time.Duration
	clockSkew       time.Duration
	maxTokenBytes   int
	now             func() time.Time
	keys            map[string]*ecdsa.PublicKey
}

// NewJWTSubjectAssertionVerifier validates and copies one pinned subject profile.
func NewJWTSubjectAssertionVerifier(cfg SubjectVerifierConfig) (*JWTSubjectAssertionVerifier, error) {
	if !canonicalIssuer(cfg.Issuer) || !boundedSafe(cfg.Audience) || !canonicalToken(cfg.AuthorizedParty) ||
		cfg.MaxAge <= 0 || cfg.MaxAge > time.Hour || cfg.ClockSkew < 0 || cfg.ClockSkew > 5*time.Minute ||
		cfg.MaxTokenBytes <= 0 || cfg.MaxTokenBytes > maxCredentialBytes || len(cfg.Keys) == 0 || len(cfg.Keys) > 16 {
		return nil, errors.New("acting-access subject verifier configuration is invalid")
	}
	keys := make(map[string]*ecdsa.PublicKey, len(cfg.Keys))
	for _, item := range cfg.Keys {
		if !boundedSafe(item.ID) || item.PublicKey == nil {
			return nil, errors.New("acting-access subject verification key is invalid")
		}
		encoded, err := item.PublicKey.Bytes()
		if err != nil {
			return nil, errors.New("acting-access subject verification key is invalid")
		}
		key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), encoded)
		if err != nil {
			return nil, errors.New("acting-access subject verification key is invalid")
		}
		if _, duplicate := keys[item.ID]; duplicate {
			return nil, errors.New("acting-access subject verification key is duplicated")
		}
		keys[item.ID] = key
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &JWTSubjectAssertionVerifier{issuer: cfg.Issuer, audience: cfg.Audience, authorizedParty: cfg.AuthorizedParty,
		maxAge: cfg.MaxAge, clockSkew: cfg.ClockSkew, maxTokenBytes: cfg.MaxTokenBytes, now: now, keys: keys}, nil
}

// Verify validates signature, header, exact claims, intended use, and bounded time.
func (v *JWTSubjectAssertionVerifier) Verify(assertion SubjectAssertion) (VerifiedSubject, error) {
	claims, err := v.verifyCompact(assertion)
	if err != nil {
		return VerifiedSubject{}, err
	}
	owner, err := NewOwner(claims.issuer, claims.subject)
	if err != nil || claims.issuer != v.issuer || len(claims.audience) != 1 || claims.audience[0] != v.audience ||
		claims.authorizedParty != v.authorizedParty || !boundedSafe(claims.consentProof) {
		return VerifiedSubject{}, errors.New("subject assertion profile is invalid")
	}
	now := v.now().UTC()
	issuedAt, notBefore, expiresAt := time.Unix(claims.issuedAt, 0).UTC(), time.Unix(claims.notBefore, 0).UTC(), time.Unix(claims.expiresAt, 0).UTC()
	if !expiresAt.After(issuedAt) || notBefore.After(expiresAt) || now.Before(issuedAt.Add(-v.clockSkew)) ||
		now.Before(notBefore.Add(-v.clockSkew)) || now.After(expiresAt.Add(v.clockSkew)) || now.Sub(issuedAt) > v.maxAge+v.clockSkew {
		return VerifiedSubject{}, errors.New("subject assertion time is invalid")
	}
	return VerifiedSubject{owner: owner, consentProof: claims.consentProof, notAfter: expiresAt}, nil
}

func (v *JWTSubjectAssertionVerifier) verifyCompact(assertion SubjectAssertion) (subjectClaims, error) {
	if v == nil || assertion.secret.value == nil {
		return subjectClaims{}, errors.New("subject assertion is unavailable")
	}
	raw := assertion.secret.value.raw
	if len(raw) == 0 || len(raw) > v.maxTokenBytes {
		return subjectClaims{}, errors.New("subject assertion size is invalid")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return subjectClaims{}, errors.New("subject assertion is malformed")
	}
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return subjectClaims{}, errors.New("subject assertion header is malformed")
	}
	header, err := parseSubjectHeader(headerRaw)
	if err != nil {
		return subjectClaims{}, err
	}
	key := v.keys[header.kid]
	if key == nil {
		return subjectClaims{}, errors.New("subject assertion key is unknown")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || jwt.SigningMethodES256.Verify(parts[0]+"."+parts[1], signature, key) != nil {
		return subjectClaims{}, errors.New("subject assertion signature is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || len(payload) > v.maxTokenBytes {
		return subjectClaims{}, errors.New("subject assertion claims are malformed")
	}
	return parseSubjectClaims(payload)
}

// VerifySubjectForOwner refuses recombination unless the freshly verified subject
// exactly matches the durable issuer-qualified owner.
func VerifySubjectForOwner(verifier SubjectAssertionVerifier, assertion SubjectAssertion, owner Owner) (VerifiedSubject, error) {
	if verifier == nil || owner == (Owner{}) {
		return VerifiedSubject{}, errors.New("subject verification is unavailable")
	}
	verified, err := verifier.Verify(assertion)
	if err != nil {
		return VerifiedSubject{}, err
	}
	if verified.owner != owner {
		return VerifiedSubject{}, errors.New("verified subject does not match durable owner")
	}
	return verified, nil
}

type subjectHeader struct{ kid string }

func parseSubjectHeader(raw []byte) (subjectHeader, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return subjectHeader{}, errors.New("subject assertion header is malformed")
	}
	seen := make(map[string]bool, 3)
	var header subjectHeader
	for decoder.More() {
		name, err := uniqueName(decoder, seen)
		if err != nil {
			return subjectHeader{}, errors.New("subject assertion header is malformed")
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return subjectHeader{}, errors.New("subject assertion header is malformed")
		}
		switch name {
		case "alg":
			if value != jwt.SigningMethodES256.Alg() {
				return subjectHeader{}, errors.New("subject assertion algorithm is invalid")
			}
		case "kid":
			header.kid = value
		case "typ":
			if value != ExchangeSubjectTokenType {
				return subjectHeader{}, errors.New("subject assertion type is invalid")
			}
		default:
			return subjectHeader{}, errors.New("subject assertion header is malformed")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || ensureJSONEOF(decoder) != nil || !seen["alg"] || !seen["kid"] || !seen["typ"] || !boundedSafe(header.kid) {
		return subjectHeader{}, errors.New("subject assertion header is incomplete")
	}
	return header, nil
}

type subjectClaims struct {
	issuer, subject, authorizedParty, consentProof string
	audience                                       []string
	issuedAt, notBefore, expiresAt                 int64
}

//nolint:goconst // Closed JWT claim names intentionally remain explicit in this parser.
func parseSubjectClaims(raw []byte) (subjectClaims, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return subjectClaims{}, errors.New("subject assertion claims are malformed")
	}
	seen := make(map[string]bool, 8)
	var claims subjectClaims
	for decoder.More() {
		name, err := uniqueName(decoder, seen)
		if err != nil {
			return subjectClaims{}, errors.New("subject assertion claims are malformed")
		}
		switch name {
		case "iss":
			err = decoder.Decode(&claims.issuer)
		case "sub":
			err = decoder.Decode(&claims.subject)
		case "aud":
			err = decoder.Decode(&claims.audience)
		case "iat":
			err = decoder.Decode(&claims.issuedAt)
		case "nbf":
			err = decoder.Decode(&claims.notBefore)
		case "exp":
			err = decoder.Decode(&claims.expiresAt)
		case "azp":
			err = decoder.Decode(&claims.authorizedParty)
		case "consent":
			err = decoder.Decode(&claims.consentProof)
		default:
			return subjectClaims{}, errors.New("subject assertion claims are unprofiled")
		}
		if err != nil {
			return subjectClaims{}, errors.New("subject assertion claims are malformed")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || ensureJSONEOF(decoder) != nil {
		return subjectClaims{}, errors.New("subject assertion claims are malformed")
	}
	for _, required := range []string{"iss", "sub", "aud", "iat", "nbf", "exp", "azp", "consent"} {
		if !seen[required] {
			return subjectClaims{}, errors.New("subject assertion claims are incomplete")
		}
	}
	return claims, nil
}

func uniqueName(decoder *json.Decoder, seen map[string]bool) (string, error) {
	token, err := decoder.Token()
	name, ok := token.(string)
	if err != nil || !ok || seen[name] {
		return "", errors.New("duplicate JSON member")
	}
	seen[name] = true
	return name, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}
