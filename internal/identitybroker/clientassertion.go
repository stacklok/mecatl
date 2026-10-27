package identitybroker

import (
	"crypto"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// BrokerClientID is the identity broker's OAuth client at AS-1 and AS-2.
	BrokerClientID = "mecatl"
	// MaxClientAssertionLifetime is the largest exp - iat of a client
	// assertion, and the lifetime used when none is configured.
	MaxClientAssertionLifetime = 60 * time.Second

	// clientAssertionType is the typ draft-ietf-oauth-rfc7523bis gives client
	// authentication JWTs.
	clientAssertionType = "client-authentication+jwt"
)

// ClientAssertionSigner mints the private_key_jwt client assertions (RFC 7523)
// the broker authenticates with as BrokerClientID at every token and
// revocation endpoint request. Its key is the broker client key, separate
// from the agent issuer's. It is safe for concurrent use.
type ClientAssertionSigner struct {
	signer   crypto.Signer
	jwk      JWK
	lifetime time.Duration
}

// NewClientAssertionSigner returns a signer for assertions with the given
// lifetime: zero selects MaxClientAssertionLifetime, and a lifetime shorter
// than a second or longer than MaxClientAssertionLifetime is refused. The
// signer must have an ECDSA P-256 public key; a raw *ecdsa.PrivateKey is
// copied, any other signer, such as a KMS handle, is retained as supplied.
// Errors wrap ErrInvalidSigner or ErrInvalidConfig.
func NewClientAssertionSigner(signer crypto.Signer, lifetime time.Duration) (*ClientAssertionSigner, error) {
	if lifetime == 0 {
		lifetime = MaxClientAssertionLifetime
	}
	if lifetime < time.Second || lifetime > MaxClientAssertionLifetime {
		return nil, fmt.Errorf("%w: the client assertion lifetime %v is not between 1s and %v", ErrInvalidConfig, lifetime, MaxClientAssertionLifetime)
	}
	signer, pub, err := checkSigner(signer)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSigner, err)
	}
	jwk, err := publicJWK(pub)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSigner, err)
	}
	return &ClientAssertionSigner{signer: signer, jwk: jwk, lifetime: lifetime}, nil
}

// ClientAssertion returns a fresh assertion for the authorization server
// whose issuer identifier is audience: header typ client-authentication+jwt,
// alg ES256, and the key's kid; claims iss and sub BrokerClientID, aud
// audience as a single string, iat now, exp the lifetime after iat, and a
// random jti. Each call yields a new jti, so an assertion is used once.
func (s *ClientAssertionSigner) ClientAssertion(audience string, now time.Time) (Token, error) {
	if audience == "" {
		return Token{}, errors.New("identitybroker: a client assertion needs an audience")
	}
	iat := now.Truncate(time.Second)
	token := jwt.NewWithClaims(signerES256{}, jwt.MapClaims{
		"iss": BrokerClientID,
		"sub": BrokerClientID,
		// A plain string: aud carries the issuer as its only value.
		"aud": audience,
		"iat": iat.Unix(),
		"exp": iat.Add(s.lifetime).Unix(),
		"jti": rand.Text(),
	})
	token.Header = map[string]any{"typ": clientAssertionType, "alg": es256, "kid": s.jwk.KeyID}
	signed, err := token.SignedString(s.signer)
	if err != nil {
		return Token{}, err
	}
	return NewToken(signed), nil
}

// JWKS returns the public key set authorization servers register for
// BrokerClientID, inline, as ToolHive accepts no jwks_uri.
func (s *ClientAssertionSigner) JWKS() JWKSet { return JWKSet{Keys: []JWK{s.jwk}} }

// JWK is a public EC signing key as an RFC 7517 JSON Web Key.
type JWK struct {
	KeyType   string `json:"kty"`
	Curve     string `json:"crv"`
	X         string `json:"x"`
	Y         string `json:"y"`
	KeyID     string `json:"kid"`
	Use       string `json:"use"`
	Algorithm string `json:"alg"`
}

// JWKSet is an RFC 7517 JWK Set.
type JWKSet struct {
	Keys []JWK `json:"keys"`
}
