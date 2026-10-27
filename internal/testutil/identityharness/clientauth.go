package identityharness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// BrokerClientID is the identity broker's confidential client at AS-1
	// (design step (5)). It authenticates only with private_key_jwt.
	BrokerClientID = "mecatl"
	// DefaultClientAssertionMaxLifetime is the largest exp - iat AS-1 accepts
	// in a client assertion.
	DefaultClientAssertionMaxLifetime = 60 * time.Second
	// DefaultBrokerKeyID is the kid of the broker key the harness generates
	// and registers for BrokerClientID.
	DefaultBrokerKeyID = "broker-1"

	authPrivateKeyJWT      = "private_key_jwt"
	clientAssertionTypeJWT = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	clientAssertionTyp     = "client-authentication+jwt"
	// as1ClockSkew is the clock skew AS-1 allows a client assertion's exp and
	// iat, and so how long past exp it keeps a used jti.
	as1ClockSkew = time.Minute
	// ClientAssertionClockSkew is as1ClockSkew, for tests at the boundary.
	ClientAssertionClockSkew = as1ClockSkew
)

// BrokerKey is a public key registered for BrokerClientID, as from its
// jwks_uri or static configuration. Only ES256 (P-256) keys are accepted.
type BrokerKey struct {
	KeyID  string
	Public *ecdsa.PublicKey
}

// ClientAssertionSpec describes a private_key_jwt client assertion for
// ScriptedBroker.Assertion. Fields left empty produce an assertion AS-1 accepts, so a
// test changes only the defect it checks.
type ClientAssertionSpec struct {
	// Issuer and Subject replace BrokerClientID in iss and sub.
	Issuer, Subject string
	// Audience replaces aud. Nil selects AS-1's issuer; one value is encoded
	// as a string, more as an array.
	Audience []string
	// IssuedAt replaces the current time in iat, which may be in the
	// future; exp follows from it.
	IssuedAt time.Time
	// Lifetime is exp - iat. Zero selects the configured maximum.
	Lifetime time.Duration
	// JTI replaces the fresh jti, for a replayed assertion.
	JTI string
	// OmitJTI, OmitIssuedAt, and OmitExpiry leave jti, iat, and exp out.
	OmitJTI, OmitIssuedAt, OmitExpiry bool
	// KeyID replaces DefaultBrokerKeyID in the kid header; OmitKeyID leaves
	// kid out.
	KeyID     string
	OmitKeyID bool
	// ForeignKey signs with a key that is not registered for the broker.
	ForeignKey bool
	// Type replaces client-authentication+jwt in typ; OmitType leaves typ out.
	Type     string
	OmitType bool
}

func (a *AS1) signAssertion(spec ClientAssertionSpec) string {
	iat := spec.IssuedAt
	if iat.IsZero() {
		iat = a.now()
	}
	lifetime := spec.Lifetime
	if lifetime == 0 {
		lifetime = a.assertionMax
	}
	claims := jwt.MapClaims{
		"iss": firstNonEmpty(spec.Issuer, BrokerClientID),
		"sub": firstNonEmpty(spec.Subject, BrokerClientID),
		"iat": iat.Unix(),
		"exp": iat.Add(lifetime).Unix(),
		"jti": firstNonEmpty(spec.JTI, randomToken("ca-")),
	}
	for name, omit := range map[string]bool{"jti": spec.OmitJTI, "iat": spec.OmitIssuedAt, "exp": spec.OmitExpiry} {
		if omit {
			delete(claims, name)
		}
	}
	switch len(spec.Audience) {
	case 0:
		claims["aud"] = a.Issuer()
	case 1:
		claims["aud"] = spec.Audience[0]
	default:
		claims["aud"] = spec.Audience
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	if !spec.OmitKeyID {
		token.Header["kid"] = firstNonEmpty(spec.KeyID, DefaultBrokerKeyID)
	}
	if spec.OmitType {
		delete(token.Header, "typ")
	} else {
		token.Header["typ"] = firstNonEmpty(spec.Type, clientAssertionTyp)
	}
	key := a.brokerKey
	if spec.ForeignKey {
		key = a.foreign
	}
	signed, err := token.SignedString(key)
	if err != nil {
		panic("identityharness: sign client assertion: " + err.Error())
	}
	return signed
}

// ReceivedClientAssertions returns every client assertion AS-1's token and
// revocation endpoints received, accepted or not, oldest first, so a test can
// inspect what a production client actually sent.
func (a *AS1) ReceivedClientAssertions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.receivedAssertions)
}

// authenticateClient identifies the token or revocation endpoint caller. The
// broker authenticates with private_key_jwt; public clients name themselves
// with client_id and no credential. A client secret, in the form or in Basic
// authentication, is refused for every client, as is mixing methods
// (RFC 6749 §2.3).
func (a *AS1) authenticateClient(r *http.Request) (clientID string, confidential, ok bool) {
	form := r.PostForm
	if form.Has("client_secret") || r.Header.Get("Authorization") != "" {
		return "", false, false
	}
	if form.Has("client_assertion") || form.Has("client_assertion_type") {
		a.mu.Lock()
		a.receivedAssertions = append(a.receivedAssertions, form.Get("client_assertion"))
		a.mu.Unlock()
		if form.Get("client_assertion_type") != clientAssertionTypeJWT ||
			(form.Has(paramClientID) && form.Get(paramClientID) != BrokerClientID) ||
			!a.verifyAssertion(form.Get("client_assertion")) {
			return "", false, false
		}
		return BrokerClientID, true, true
	}
	clientID = form.Get(paramClientID)
	a.mu.Lock()
	_, known := a.clients[clientID]
	a.mu.Unlock()
	return clientID, false, known
}

// verifyAssertion runs design step (5)'s checks on a client assertion.
func (a *AS1) verifyAssertion(assertion string) bool {
	var claims jwt.RegisteredClaims
	token, err := jwt.ParseWithClaims(assertion, &claims, func(t *jwt.Token) (any, error) {
		kid, isString := t.Header["kid"].(string)
		if !isString || kid == "" {
			return nil, jwt.ErrTokenUnverifiable
		}
		a.mu.Lock()
		key, ok := a.brokerKeys[kid]
		a.mu.Unlock()
		if !ok {
			return nil, jwt.ErrTokenUnverifiable
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer(BrokerClientID), jwt.WithSubject(BrokerClientID),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(as1ClockSkew), jwt.WithTimeFunc(a.now))
	if err != nil {
		return false
	}
	// Guess: an explicit typ other than the rfc7523bis type or the generic
	// JWT (golang-jwt's default) is refused; an untyped assertion is
	// accepted, as the design says.
	if typ, present := token.Header["typ"]; present && typ != clientAssertionTyp && typ != "JWT" {
		return false
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != a.Issuer() {
		return false
	}
	if claims.IssuedAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > a.assertionMax || claims.ID == "" {
		return false
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	maps.DeleteFunc(a.assertionJTIs, func(_ string, keep time.Time) bool { return now.After(keep) })
	if _, seen := a.assertionJTIs[claims.ID]; seen {
		return false
	}
	a.assertionJTIs[claims.ID] = claims.ExpiresAt.Add(as1ClockSkew)
	return true
}

// registerBrokerKeys registers the harness's own broker key and keys,
// failing t on a key AS-1 could never use or that shadows another.
func (a *AS1) registerBrokerKeys(t testing.TB, keys []BrokerKey) {
	t.Helper()
	a.brokerKeys[DefaultBrokerKeyID] = &a.brokerKey.PublicKey
	for _, k := range keys {
		if _, dup := a.brokerKeys[k.KeyID]; k.KeyID == "" || dup {
			t.Fatalf("identityharness: broker key ID %q is empty, %s, or repeated", k.KeyID, DefaultBrokerKeyID)
		}
		if k.Public == nil || k.Public.Curve != elliptic.P256() {
			t.Fatalf("identityharness: broker key %q is not a P-256 key", k.KeyID)
		}
		a.brokerKeys[k.KeyID] = k.Public
	}
}
