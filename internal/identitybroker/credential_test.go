package identitybroker

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var credentialTestWorkload = Workload{Name: "system:serviceaccount:mecatl:mecak8s"}

// credentialFixture is a broker on a controlled clock with one registered
// main agent.
type credentialFixture struct {
	broker  *Broker
	key     *ecdsa.PrivateKey
	now     time.Time
	session Session
	reg     Registration
}

func newCredentialFixture(t *testing.T) *credentialFixture {
	t.Helper()
	f := &credentialFixture{key: testKey(t, "credential test key"), now: time.Unix(1_800_000_000, 0)}
	b, err := New(testConfig(), nil, f.key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b.sessions.now = func() time.Time { return f.now }
	f.broker = b
	f.session = f.open(t, "hostS42")
	f.reg = f.register(t, f.session, "inc_main")
	return f
}

func (f *credentialFixture) open(t *testing.T, host string) Session {
	t.Helper()
	s, err := f.broker.OpenSession(context.Background(), credentialTestWorkload, OpenSessionRequest{
		HostSessionID: host, Incarnation: "inc_" + host,
		Owner: Owner{Issuer: "https://login.example.com", Subject: "alice-204"},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	return s
}

func (f *credentialFixture) register(t *testing.T, s Session, incarnation string) Registration {
	t.Helper()
	reg, err := f.broker.RegisterAgent(context.Background(), credentialTestWorkload, RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner,
		Incarnation: incarnation, Definition: DefinitionRef{Name: "main"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	return reg
}

func (f *credentialFixture) verify(sessionID string, credential Token) (VerifiedAgent, error) {
	return f.broker.VerifyCredential(context.Background(), VerifyCredentialRequest{SessionID: sessionID, Credential: credential})
}

// forge signs claims and header with the fixture's own key, so only the
// field under test is wrong.
func (f *credentialFixture) forge(t *testing.T, header map[string]any, claims jwt.MapClaims, key any) Token {
	t.Helper()
	method := jwt.GetSigningMethod(header["alg"].(string))
	token := jwt.NewWithClaims(method, claims)
	token.Header = header
	if key == nil {
		key = f.key
	}
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("forge: %v", err)
	}
	return NewToken(signed)
}

func (f *credentialFixture) validHeader() map[string]any {
	return map[string]any{"alg": "ES256", "kid": f.broker.issuer.kid}
}

func (f *credentialFixture) validClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"sub": f.reg.Agent.Subject,
		"aud": []string{"https://broker.example.com"},
		"exp": f.now.Add(time.Minute).Unix(),
	}
}

func segment(t *testing.T, token, which string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("credential has %d parts, want 3", len(parts))
	}
	i := map[string]int{"header": 0, "claims": 1}[which]
	raw, err := base64.RawURLEncoding.DecodeString(parts[i])
	if err != nil {
		t.Fatalf("decode %s: %v", which, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse %s: %v", which, err)
	}
	return out
}

func TestCredentialHasExactlyTheDesignedHeaderAndClaims(t *testing.T) {
	f := newCredentialFixture(t)
	token := f.reg.Credential.Value()

	header := segment(t, token, "header")
	if len(header) != 2 || header["alg"] != "ES256" || header["kid"] != f.broker.issuer.kid {
		t.Fatalf("header = %v, want exactly alg ES256 and kid %s", header, f.broker.issuer.kid)
	}
	claims := segment(t, token, "claims")
	wantSub := "spiffe://agents.example.com/mecatl/agent/shared/inst/inc_main"
	wantExp := float64(f.now.Add(testConfig().CredentialLifetime).Unix())
	aud, _ := claims["aud"].([]any)
	if len(claims) != 3 || claims["sub"] != wantSub || len(aud) != 1 || aud[0] != "https://broker.example.com" || claims["exp"] != wantExp {
		t.Fatalf("claims = %v, want exactly sub %s, aud [https://broker.example.com], exp %v", claims, wantSub, wantExp)
	}
	if f.reg.Agent.Subject != wantSub || !f.reg.ExpiresAt.Equal(time.Unix(int64(wantExp), 0)) {
		t.Fatalf("registration = %+v, want subject %s expiring at %v", f.reg, wantSub, time.Unix(int64(wantExp), 0))
	}
}

func TestCredentialVerifiesWithTheSignersPublicKey(t *testing.T) {
	f := newCredentialFixture(t)
	// The kid is the RFC 7638 thumbprint of the signer's public key.
	point, err := f.key.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	thumb := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + enc.EncodeToString(point[1:33]) + `","y":"` + enc.EncodeToString(point[33:]) + `"}`))
	kid := enc.EncodeToString(thumb[:])

	// An independent verifier that knows only the public key and its kid
	// accepts the credential.
	parsed, err := jwt.Parse(f.reg.Credential.Value(), func(tok *jwt.Token) (any, error) {
		if tok.Header["kid"] != kid {
			return nil, errors.New("unknown kid")
		}
		return &f.key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithAudience("https://broker.example.com"),
		jwt.WithTimeFunc(func() time.Time { return f.now }))
	if err != nil || !parsed.Valid {
		t.Fatalf("the credential does not verify with the signer's public key: %v", err)
	}
}

func TestVerifyCredentialChecks(t *testing.T) {
	denied := ReasonError(ReasonDenied, "")
	expired := ReasonError(ReasonSVIDExpired, "")

	cases := map[string]struct {
		credential func(t *testing.T, f *credentialFixture) Token
		want       *Error
	}{
		"malformed": {func(*testing.T, *credentialFixture) Token { return NewToken("not.a.jwt") }, denied},
		"subject in another trust domain": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["sub"] = "spiffe://other.example.com/mecatl/agent/shared/inst/inc_main"
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"subject not a SPIFFE ID": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["sub"] = "agents.example.com/mecatl/agent/shared/inst/inc_main"
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"no kid": {func(t *testing.T, f *credentialFixture) Token {
			return f.forge(t, map[string]any{"alg": "ES256"}, f.validClaims(), nil)
		}, denied},
		"unknown kid": {func(t *testing.T, f *credentialFixture) Token {
			return f.forge(t, map[string]any{"alg": "ES256", "kid": "other"}, f.validClaims(), nil)
		}, denied},
		"HMAC algorithm": {func(t *testing.T, f *credentialFixture) Token {
			pub, _ := f.key.PublicKey.Bytes()
			return f.forge(t, map[string]any{"alg": "HS256", "kid": f.broker.issuer.kid}, f.validClaims(), pub)
		}, denied},
		"none algorithm": {func(t *testing.T, f *credentialFixture) Token {
			return f.forge(t, map[string]any{"alg": "none", "kid": f.broker.issuer.kid}, f.validClaims(), jwt.UnsafeAllowNoneSignatureType)
		}, denied},
		"signed by another key": {func(t *testing.T, f *credentialFixture) Token {
			return f.forge(t, f.validHeader(), f.validClaims(), testKey(t, "another key"))
		}, denied},
		"another audience": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["aud"] = []string{"https://other.example.com"}
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"a second audience": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["aud"] = []string{"https://broker.example.com", "https://other.example.com"}
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"no expiry": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			delete(c, "exp")
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"expired": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["exp"] = f.now.Add(-credentialClockSkew).Unix()
			return f.forge(t, f.validHeader(), c, nil)
		}, expired},
		"expiry beyond the credential lifetime": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["exp"] = f.now.Add(testConfig().CredentialLifetime + credentialClockSkew + time.Second).Unix()
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"unregistered subject": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["sub"] = "spiffe://agents.example.com/mecatl/agent/shared/inst/inc_unknown"
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"registered to another session": {func(t *testing.T, f *credentialFixture) Token {
			return f.register(t, f.open(t, "hostS43"), "inc_other").Credential
		}, denied},
		// Order: the audience check comes before expiry, and expiry before
		// registration.
		"expired with another audience": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["aud"] = []string{"https://other.example.com"}
			c["exp"] = f.now.Add(-time.Hour).Unix()
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"expired and unregistered": {func(t *testing.T, f *credentialFixture) Token {
			c := f.validClaims()
			c["sub"] = "spiffe://agents.example.com/mecatl/agent/shared/inst/inc_unknown"
			c["exp"] = f.now.Add(-time.Hour).Unix()
			return f.forge(t, f.validHeader(), c, nil)
		}, expired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newCredentialFixture(t)
			got, err := f.verify(f.session.ID, tc.credential(t, f))
			var brokerErr *Error
			if !errors.As(err, &brokerErr) || brokerErr.Code != tc.want.Code || brokerErr.Reason != tc.want.Reason {
				t.Fatalf("VerifyCredential error = %v, want %s %s", err, tc.want.Code, tc.want.Reason)
			}
			if got != (VerifiedAgent{}) {
				t.Fatalf("a failed VerifyCredential returned %+v", got)
			}
		})
	}
}

func TestVerifyCredentialAllowsClockSkewAfterExpiry(t *testing.T) {
	f := newCredentialFixture(t)
	f.now = f.reg.ExpiresAt.Add(credentialClockSkew - time.Second)
	if _, err := f.verify(f.session.ID, f.reg.Credential); err != nil {
		t.Fatalf("VerifyCredential within the clock skew: %v", err)
	}
	f.now = f.reg.ExpiresAt.Add(credentialClockSkew)
	if _, err := f.verify(f.session.ID, f.reg.Credential); !errors.Is(err, ReasonError(ReasonSVIDExpired, "")) {
		t.Fatalf("VerifyCredential past the clock skew = %v, want svid_expired", err)
	}
}

// keySigner hides the key's type, as a KMS handle would, so signing goes
// through crypto.Signer and the ASN.1 conversion.
type keySigner struct{ key *ecdsa.PrivateKey }

func (s keySigner) Public() crypto.PublicKey { return &s.key.PublicKey }
func (s keySigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return s.key.Sign(r, digest, opts)
}

func TestCredentialFromAnOpaqueSignerVerifies(t *testing.T) {
	key := testKey(t, "opaque signer key")
	b, err := New(testConfig(), nil, keySigner{key: key})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s, err := b.OpenSession(context.Background(), credentialTestWorkload, OpenSessionRequest{
		HostSessionID: "hostS42", Incarnation: "inc_hostS42", Owner: Owner{Issuer: "https://login.example.com", Subject: "alice-204"},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := b.RegisterAgent(context.Background(), credentialTestWorkload, RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: "inc_main", Definition: DefinitionRef{Name: "main"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if _, err := b.VerifyCredential(context.Background(), VerifyCredentialRequest{SessionID: s.ID, Credential: reg.Credential}); err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
}

// malformedSigner returns signatures that are not ASN.1 ECDSA signatures.
type malformedSigner struct {
	keySigner
	sig []byte
}

func (s malformedSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return s.sig, nil
}

func TestSignerES256RejectsMalformedSignatures(t *testing.T) {
	key := testKey(t, "malformed signer key")
	oversized := struct{ R, S *big.Int }{new(big.Int).Lsh(big.NewInt(1), 300), big.NewInt(1)}
	for name, sig := range map[string][]byte{
		"not ASN.1":  []byte("garbage"),
		"oversized":  mustASN1(t, oversized),
		"zero value": mustASN1(t, struct{ R, S *big.Int }{big.NewInt(0), big.NewInt(1)}),
	} {
		if _, err := (signerES256{}).Sign("payload", malformedSigner{keySigner{key}, sig}); err == nil {
			t.Errorf("%s: Sign accepted a malformed signature", name)
		}
	}
	if _, err := (signerES256{}).Sign("payload", "not a signer"); err == nil {
		t.Error("Sign accepted a key that is not a crypto.Signer")
	}
}

func mustASN1(t *testing.T, v any) []byte {
	t.Helper()
	der, err := asn1.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
