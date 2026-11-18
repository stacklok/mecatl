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

// credentialClockSkew is how far the broker's clock may differ from another
// party's: a verifier's from the credential issuer's when checking a
// credential's expiry, and the broker's from AS-1's at a session's hard
// expiry. The value is an interim choice until operator configuration sets it
// (one clock-skew setting, #2813).
const credentialClockSkew = 30 * time.Second

// es256 is the only algorithm agent credentials use.
const es256 = "ES256"

// agentPath is the SPIFFE ID path of agent instances, before
// /<ag_ identifier>.
const agentPath = "/agent"

// credentialIssuer signs and verifies agent credentials: JWT-SVIDs carrying
// only sub, aud, and exp, with alg and kid in the header.
type credentialIssuer struct {
	trustDomain string
	audience    string
	lifetime    time.Duration
	signer      crypto.Signer
	// kid is the RFC 7638 JWK thumbprint of the signer's public key.
	kid string
	// keys are the public keys credentials verify with, by kid: the signer's
	// and its fallbacks', earlier agent issuer keys whose credentials still
	// verify during a rotation.
	keys map[string]*ecdsa.PublicKey
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
		kid:         jwk.KeyID,
		keys:        map[string]*ecdsa.PublicKey{jwk.KeyID: pub},
	}, nil
}

// addFallbacks has c also verify credentials signed by the fallback keys,
// each an ECDSA P-256 public key, which it copies.
func (c credentialIssuer) addFallbacks(fallbacks []crypto.PublicKey) error {
	for i, fallback := range fallbacks {
		dup, err := copyP256Public(fallback)
		if err != nil {
			return fmt.Errorf("agent issuer fallback key %d: %w", i, err)
		}
		jwk, err := publicJWK(dup)
		if err != nil {
			return err
		}
		c.keys[jwk.KeyID] = dup
	}
	return nil
}

// subject returns the SPIFFE ID of the agent instance with ag_ identifier
// agentID.
func (c credentialIssuer) subject(agentID string) string {
	return ActorIssuer(c.trustDomain) + agentPath + "/" + agentID
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
// is a SPIFFE ID in the agent trust domain, kid selects the agent issuer key
// or one of its fallbacks, alg is ES256, the signature verifies, aud is exactly the broker audience,
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
		kid, _ := t.Header["kid"].(string)
		key, ok := c.keys[kid]
		if !ok {
			return nil, errors.New("the key ID does not select the agent issuer key")
		}
		if t.Method.Alg() != es256 {
			return nil, errors.New("the algorithm is not allowed")
		}
		return key, nil
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

// isAgentID reports whether s has the form newAgentID returns: ag_ and 26
// lowercase base32 characters.
func isAgentID(s string) bool {
	rest, ok := strings.CutPrefix(s, "ag_")
	if !ok || len(rest) != 26 {
		return false
	}
	for _, c := range rest {
		if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// CheckRegistration reports whether reg has the form of a registration's
// answer: its agent is an agent instance's SPIFFE ID,
// spiffe://<trust domain>/agent/<ag_ identifier>, and its credential's sub
// names that agent. It reads the credential without verifying it: the broker
// that issued it verifies it on use. A caller that holds an earlier
// registration of the same incarnation also checks that reg names the same
// agent, since the ID carries nothing a caller could predict.
func CheckRegistration(reg Registration) error {
	rest, scheme := strings.CutPrefix(reg.Agent.Subject, "spiffe://")
	trustDomain, agentID, cut := strings.Cut(rest, agentPath+"/")
	if !scheme || !cut || ValidateTrustDomain(trustDomain) != nil || !isAgentID(agentID) {
		return fmt.Errorf("the agent %q is not an agent instance's SPIFFE ID", reg.Agent.Subject)
	}
	var claims jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(reg.Credential.Value(), &claims); err != nil {
		return errors.New("the credential is not a JWT")
	}
	if claims.Subject != reg.Agent.Subject {
		return errors.New("the credential names another agent")
	}
	return nil
}
