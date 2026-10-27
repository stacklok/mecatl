package identitybroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// credentialClockSkew is how far a verifier's clock may lag the issuer's when
// checking a credential's expiry. The value is an interim choice until
// operator configuration sets it.
const credentialClockSkew = 30 * time.Second

// es256 is the only algorithm agent credentials use.
const es256 = "ES256"

// sharedAgentPath is the SPIFFE ID path prefix of the shared agent identity,
// before /inst/<incarnation>. Its single segment cannot be read as the
// <source>/<name> pair of a per-definition identity.
const sharedAgentPath = "/mecatl/agent/shared"

// credentialIssuer signs and verifies agent credentials: JWT-SVIDs carrying
// only sub, aud, and exp, with alg and kid in the header.
type credentialIssuer struct {
	trustDomain string
	audience    string
	lifetime    time.Duration
	signer      crypto.Signer
	publicKey   *ecdsa.PublicKey
	// kid is the RFC 7638 JWK thumbprint of publicKey.
	kid string
}

func newCredentialIssuer(config Config, signer crypto.Signer, pub *ecdsa.PublicKey) (credentialIssuer, error) {
	jwk, err := publicJWK(pub)
	if err != nil {
		return credentialIssuer{}, err
	}
	return credentialIssuer{
		trustDomain: config.TrustDomain,
		audience:    config.Audience,
		lifetime:    config.CredentialLifetime,
		signer:      signer,
		publicKey:   pub,
		kid:         jwk.KeyID,
	}, nil
}

// subject returns the SPIFFE ID of the shared agent identity's instance with
// incarnation, which the caller has validated as a path segment.
func (c credentialIssuer) subject(incarnation string) string {
	return "spiffe://" + c.trustDomain + sharedAgentPath + "/inst/" + incarnation
}

// issue returns a credential for subject that expires a lifetime after now,
// and its expiry.
func (c credentialIssuer) issue(subject string, now time.Time) (Token, time.Time, error) {
	exp := jwt.NewNumericDate(now.Add(c.lifetime))
	token := jwt.NewWithClaims(signerES256{}, jwt.RegisteredClaims{
		Subject:   subject,
		Audience:  jwt.ClaimStrings{c.audience},
		ExpiresAt: exp,
	})
	// Exactly the header the design specifies: no typ.
	token.Header = map[string]any{"alg": es256, "kid": c.kid}
	signed, err := token.SignedString(c.signer)
	if err != nil {
		return Token{}, time.Time{}, err
	}
	return NewToken(signed), exp.Time, nil
}

// verifiedCredential is a credential that passed the checks needing only the
// credential: its subject and expiry.
type verifiedCredential struct {
	subject   string
	expiresAt time.Time
}

// verify applies the design's credential checks 1 to 6, in order: the subject
// is a SPIFFE ID in the agent trust domain, kid selects the agent issuer key,
// alg is ES256, the signature verifies, aud is exactly the broker audience,
// and exp has not passed (allowing clock skew) and is no further away than
// the credential lifetime. An expired credential fails with
// ReasonSVIDExpired; every other failure with ReasonDenied.
func (c credentialIssuer) verify(credential Token, now time.Time) (verifiedCredential, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(credential.Value(), &claims, func(t *jwt.Token) (any, error) {
		sub, _ := t.Claims.GetSubject()
		if !strings.HasPrefix(sub, "spiffe://"+c.trustDomain+"/") {
			return nil, errors.New("the subject is not a SPIFFE ID in the agent trust domain")
		}
		if kid, _ := t.Header["kid"].(string); kid != c.kid {
			return nil, errors.New("the key ID does not select the agent issuer key")
		}
		if t.Method.Alg() != es256 {
			return nil, errors.New("the algorithm is not allowed")
		}
		return c.publicKey, nil
	}, jwt.WithoutClaimsValidation())
	if err != nil {
		return verifiedCredential{}, credentialDenied("the credential is not valid")
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != c.audience {
		return verifiedCredential{}, credentialDenied("the credential's audience is not the broker's alone")
	}
	if claims.ExpiresAt == nil {
		return verifiedCredential{}, credentialDenied("the credential has no expiry")
	}
	exp := claims.ExpiresAt.Time
	if !now.Before(exp.Add(credentialClockSkew)) {
		return verifiedCredential{}, ReasonError(ReasonSVIDExpired, "the credential has expired")
	}
	if exp.After(now.Add(c.lifetime + credentialClockSkew)) {
		return verifiedCredential{}, credentialDenied("the credential's expiry exceeds the credential lifetime")
	}
	return verifiedCredential{subject: claims.Subject, expiresAt: exp}, nil
}

func credentialDenied(detail string) error {
	return ReasonError(ReasonDenied, detail)
}

// signerES256 signs with any ES256 crypto.Signer, such as a KMS handle, which
// the library's own ES256 method cannot. Only signing uses it; verification
// uses the library's ES256 method with the public key.
type signerES256 struct{}

func (signerES256) Alg() string { return es256 }

func (signerES256) Verify(string, []byte, any) error {
	return errors.New("identitybroker: signerES256 only signs")
}

// Sign returns the JWS ES256 signature: r and s, each as 32 big-endian bytes.
// crypto.Signer returns ECDSA signatures ASN.1-encoded.
func (signerES256) Sign(signingString string, key any) ([]byte, error) {
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("signing key %T is not a crypto.Signer", key)
	}
	digest := sha256.Sum256([]byte(signingString))
	der, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("sign credential: %w", err)
	}
	var sig struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil || len(rest) != 0 || sig.R == nil || sig.S == nil ||
		sig.R.Sign() <= 0 || sig.S.Sign() <= 0 || sig.R.BitLen() > 256 || sig.S.BitLen() > 256 {
		return nil, errors.New("sign credential: the signer returned a malformed ECDSA signature")
	}
	out := make([]byte, 64)
	sig.R.FillBytes(out[:32])
	sig.S.FillBytes(out[32:])
	return out, nil
}

// publicJWK returns a P-256 public key as an ES256 signing JWK (RFC 7518
// §6.2.1) whose kid is its RFC 7638 thumbprint, so the key ID stays the same
// wherever the key is later published.
func publicJWK(pub *ecdsa.PublicKey) (JWK, error) {
	point, err := pub.Bytes()
	if err != nil || len(point) != 65 {
		return JWK{}, errors.New("the signer's public key is not an uncompressed P-256 point")
	}
	enc := base64.RawURLEncoding
	x, y := enc.EncodeToString(point[1:33]), enc.EncodeToString(point[33:])
	// The required members in lexicographic order, no whitespace.
	thumbprint := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + x + `","y":"` + y + `"}`))
	return JWK{
		KeyType: "EC", Curve: "P-256", X: x, Y: y,
		KeyID: enc.EncodeToString(thumbprint[:]), Use: "sig", Algorithm: es256,
	}, nil
}

// newAgentID returns a random ag_ identifier in the same lowercase base32 form
// as broker session IDs.
func newAgentID() string {
	return "ag_" + strings.ToLower(rand.Text())
}
