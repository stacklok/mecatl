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
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var credentialTestWorkload = Workload{Name: "system:serviceaccount:mecatl:mecak8s"}

// Well-formed minted incarnations for test agents.
const (
	incMain  = "inc_abcdefghijklmnopqrstuvwxya"
	incOther = "inc_bcdefghijklmnopqrstuvwxyza"
)

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
	f.session = f.open(t, "hostS42", incMain)
	f.reg = f.register(t, f.session, incMain)
	return f
}

// open opens host's broker session with incarnation, which its main agent
// must register with.
func (f *credentialFixture) open(t *testing.T, host, incarnation string) Session {
	t.Helper()
	s, err := f.broker.OpenSession(context.Background(), credentialTestWorkload, OpenSessionRequest{
		HostSessionID: host, Incarnation: incarnation,
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

// expiredClaims is validClaims an hour past expiry.
func (f *credentialFixture) expiredClaims() jwt.MapClaims {
	c := f.validClaims()
	c["exp"] = f.now.Add(-time.Hour).Unix()
	return c
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
	wantSub := "spiffe://agents.example.com/mecatl/agent/shared/inst/" + incMain
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
			return f.register(t, f.open(t, "hostS43", incOther), incOther).Credential
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
		// Order: checks 1 to 4 (trust domain, kid, alg, signature) come before
		// expiry, so a forged expired credential is denied rather than told to
		// register again with svid_expired.
		"expired and signed by another key": {func(t *testing.T, f *credentialFixture) Token {
			return f.forge(t, f.validHeader(), f.expiredClaims(), testKey(t, "another key"))
		}, denied},
		"expired with an unknown kid": {func(t *testing.T, f *credentialFixture) Token {
			return f.forge(t, map[string]any{"alg": "ES256", "kid": "other"}, f.expiredClaims(), nil)
		}, denied},
		"expired with the HMAC algorithm": {func(t *testing.T, f *credentialFixture) Token {
			pub, _ := f.key.PublicKey.Bytes()
			return f.forge(t, map[string]any{"alg": "HS256", "kid": f.broker.issuer.kid}, f.expiredClaims(), pub)
		}, denied},
		"expired with a subject in another trust domain": {func(t *testing.T, f *credentialFixture) Token {
			c := f.expiredClaims()
			c["sub"] = "spiffe://other.example.com/mecatl/agent/shared/inst/" + incMain
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
		"expired with a subject that is not a SPIFFE ID": {func(t *testing.T, f *credentialFixture) Token {
			c := f.expiredClaims()
			c["sub"] = "agents.example.com/mecatl/agent/shared/inst/" + incMain
			return f.forge(t, f.validHeader(), c, nil)
		}, denied},
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
		HostSessionID: "hostS42", Incarnation: incMain, Owner: Owner{Issuer: "https://login.example.com", Subject: "alice-204"},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := b.RegisterAgent(context.Background(), credentialTestWorkload, RegisterAgentRequest{
		SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: incMain, Definition: DefinitionRef{Name: "main"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if _, err := b.VerifyCredential(context.Background(), VerifyCredentialRequest{SessionID: s.ID, Credential: reg.Credential}); err != nil {
		t.Fatalf("VerifyCredential: %v", err)
	}
}

// failingSigner fails to sign while fail is set.
type failingSigner struct {
	keySigner
	fail *atomic.Bool
}

func (s failingSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if s.fail.Load() {
		return nil, errors.New("signer unavailable")
	}
	return s.keySigner.Sign(r, digest, opts)
}

func TestFailedSigningLeavesTheAgentWhereItWas(t *testing.T) {
	fail := new(atomic.Bool)
	b, err := New(testConfig(), nil, failingSigner{keySigner{testKey(t, "failing signer key")}, fail})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	owner := Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	open := func() Session {
		s, err := b.OpenSession(ctx, credentialTestWorkload, OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incMain, Owner: owner})
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		return s
	}
	register := func(s Session) (Registration, error) {
		return b.RegisterAgent(ctx, credentialTestWorkload, RegisterAgentRequest{
			SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: owner, Incarnation: incMain, Definition: DefinitionRef{Name: "main"},
		})
	}
	old := open()
	reg, err := register(old)
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if err := b.CloseSession(ctx, credentialTestWorkload, CloseSessionRequest{SessionID: old.ID, Reason: ReasonSessionClosed}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	reopened := open()

	fail.Store(true)
	_, err = register(reopened)
	if !errors.Is(err, NewError(CodeInternal, "", "")) {
		t.Fatalf("RegisterAgent with a failing signer = %v, want INTERNAL", err)
	}
	if strings.Contains(err.Error(), "signer unavailable") {
		t.Fatalf("RegisterAgent passed on the signer's error: %v", err)
	}
	// The agent did not move: it is still recorded in the old session, so its
	// credential does not verify in the new one.
	if got := b.sessions.agents[reg.Agent.Subject].sessionID; got != old.ID {
		t.Fatalf("after a failed move, the agent is recorded in %q, want the old session %q", got, old.ID)
	}
	if _, err := b.VerifyCredential(ctx, VerifyCredentialRequest{SessionID: reopened.ID, Credential: reg.Credential}); err == nil {
		t.Fatal("after a failed move, the old credential verified in the new session")
	}
	fail.Store(false)
	moved, err := register(reopened)
	if err != nil {
		t.Fatalf("RegisterAgent after the signer recovers: %v", err)
	}
	if _, err := b.VerifyCredential(ctx, VerifyCredentialRequest{SessionID: reopened.ID, Credential: moved.Credential}); err != nil {
		t.Fatalf("VerifyCredential in the new session: %v", err)
	}
}

// blockingSigner holds its first Sign call until release is closed, after
// signalling entered, so a test can change broker state while a registration
// is signing. Later calls sign at once.
type blockingSigner struct {
	keySigner
	calls   *atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func newBlockingSigner(t *testing.T) blockingSigner {
	return blockingSigner{keySigner{testKey(t, "blocking signer key")}, new(atomic.Int32), make(chan struct{}), make(chan struct{})}
}

func (s blockingSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return s.keySigner.Sign(r, digest, opts)
}

// TestRegistrationChecksAgainAfterSigning changes broker state while a
// registration is signing, and expects the registration to be refused and
// recorded nowhere.
func TestRegistrationChecksAgainAfterSigning(t *testing.T) {
	ctx := context.Background()
	alice := Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	bob := Owner{Issuer: "https://login.example.com", Subject: "bob-117"}
	setup := func(t *testing.T) (*Broker, blockingSigner, Session) {
		signer := newBlockingSigner(t)
		b, err := New(testConfig(), nil, signer)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		s, err := b.OpenSession(ctx, credentialTestWorkload, OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incMain, Owner: alice})
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		return b, signer, s
	}
	register := func(b *Broker, s Session) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := b.RegisterAgent(ctx, credentialTestWorkload, RegisterAgentRequest{
				SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: s.Owner, Incarnation: incMain, Definition: DefinitionRef{Name: "main"},
			})
			done <- err
		}()
		return done
	}

	t.Run("the session closes while signing", func(t *testing.T) {
		b, signer, s := setup(t)
		done := register(b, s)
		waitSigning(t, signer, done)
		if err := b.CloseSession(ctx, credentialTestWorkload, CloseSessionRequest{SessionID: s.ID, Reason: ReasonSessionClosed}); err != nil {
			t.Fatalf("CloseSession: %v", err)
		}
		close(signer.release)
		if err := <-done; !errors.Is(err, ReasonError(ReasonSessionClosed, "")) {
			t.Fatalf("RegisterAgent = %v, want %s", err, ReasonSessionClosed)
		}
		if n := len(b.sessions.agents); n != 0 {
			t.Fatalf("a refused registration recorded %d agents", n)
		}
	})

	t.Run("another owner registers the instance while signing", func(t *testing.T) {
		b, signer, s := setup(t)
		done := register(b, s)
		waitSigning(t, signer, done)
		bobs, err := b.OpenSession(ctx, credentialTestWorkload, OpenSessionRequest{HostSessionID: "hostS43", Incarnation: incMain, Owner: bob})
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		if err := <-register(b, bobs); err != nil {
			t.Fatalf("RegisterAgent for bob: %v", err)
		}
		close(signer.release)
		if err := <-done; !errors.Is(err, NewError(CodeAlreadyExists, "", "")) {
			t.Fatalf("RegisterAgent = %v, want ALREADY_EXISTS", err)
		}
		subject := b.issuer.subject(incMain)
		if rec := b.sessions.agents[subject]; rec.owner != bob || rec.sessionID != bobs.ID {
			t.Fatalf("agent record = %+v, want bob's in %s", rec, bobs.ID)
		}
	})
}

// waitSigning waits until the registration behind done is held in the signer,
// failing rather than hanging if it returns first.
func waitSigning[T any](t *testing.T, signer blockingSigner, done <-chan T) {
	t.Helper()
	select {
	case <-signer.entered:
	case got := <-done:
		t.Fatalf("the registration returned %v before signing", got)
	}
}

// TestRegistrationsRacingAMoveBothGetUsableCredentials races two registrations
// of one agent into a reopened session: the one that signed first records
// last, after the other has moved the agent. Both credentials must verify.
func TestRegistrationsRacingAMoveBothGetUsableCredentials(t *testing.T) {
	ctx := context.Background()
	owner := Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	signer := newBlockingSigner(t)
	signer.calls.Store(-1) // the first registration signs at once; the next one blocks
	b, err := New(testConfig(), nil, signer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	b.sessions.now = func() time.Time { return now }
	open := func() Session {
		s, err := b.OpenSession(ctx, credentialTestWorkload, OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incMain, Owner: owner})
		if err != nil {
			t.Fatalf("OpenSession: %v", err)
		}
		return s
	}
	type result struct {
		reg Registration
		err error
	}
	register := func(s Session) <-chan result {
		done := make(chan result, 1)
		go func() {
			reg, err := b.RegisterAgent(ctx, credentialTestWorkload, RegisterAgentRequest{
				SessionID: s.ID, HostSessionID: s.HostSessionID, Owner: owner, Incarnation: incMain, Definition: DefinitionRef{Name: "main"},
			})
			done <- result{reg, err}
		}()
		return done
	}
	old := open()
	if r := <-register(old); r.err != nil {
		t.Fatalf("RegisterAgent: %v", r.err)
	}
	if err := b.CloseSession(ctx, credentialTestWorkload, CloseSessionRequest{SessionID: old.ID, Reason: ReasonSessionClosed}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	reopened := open()

	first := register(reopened) // signs first, then waits
	waitSigning(t, signer, first)
	now = now.Add(2 * time.Second)
	second := <-register(reopened) // signs later, and moves the agent
	if second.err != nil {
		t.Fatalf("second RegisterAgent: %v", second.err)
	}
	close(signer.release)
	firstResult := <-first
	if firstResult.err != nil {
		t.Fatalf("first RegisterAgent: %v", firstResult.err)
	}
	for name, reg := range map[string]Registration{"first": firstResult.reg, "second": second.reg} {
		if _, err := b.VerifyCredential(ctx, VerifyCredentialRequest{SessionID: reopened.ID, Credential: reg.Credential}); err != nil {
			t.Errorf("the %s registration's credential does not verify: %v", name, err)
		}
	}
}

func TestReregistrationKeepsAtMostMaxIssuedCredentials(t *testing.T) {
	f := newCredentialFixture(t)
	regs := []Registration{f.reg}
	for range maxIssuedCredentials + 1 {
		f.now = f.now.Add(time.Second) // a distinct expiry, so a distinct token
		regs = append(regs, f.register(t, f.session, incMain))
	}
	if n := len(f.broker.sessions.agents[f.reg.Agent.Subject].credentials); n != maxIssuedCredentials {
		t.Fatalf("the agent keeps %d credentials, want %d", n, maxIssuedCredentials)
	}
	evicted := len(regs) - maxIssuedCredentials
	for i, reg := range regs {
		_, err := f.verify(f.session.ID, reg.Credential)
		if i < evicted && err == nil {
			t.Errorf("credential %d, among the earliest to expire, still verifies", i)
		}
		if i >= evicted && err != nil {
			t.Errorf("credential %d does not verify: %v", i, err)
		}
	}
}

func TestVerifyTreatsAMissingSessionRecordAsClosed(t *testing.T) {
	f := newCredentialFixture(t)
	delete(f.broker.sessions.byID, f.session.ID) // as a later purge would
	if _, err := f.verify(f.session.ID, f.reg.Credential); !errors.Is(err, ReasonError(ReasonDenied, "")) {
		t.Fatalf("VerifyCredential with no session record = %v, want denied", err)
	}
}

func TestOpaqueSignerPublicKeyIsCopied(t *testing.T) {
	pub := testKey(t, "opaque public key").PublicKey
	want := pub
	b, err := New(testConfig(), nil, opaqueSigner{pub: &pub})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pub = testKey(t, "replacement").PublicKey // the caller changes its key in place
	if !b.issuer.publicKey.Equal(&want) {
		t.Fatal("the broker's verification key changed with the caller's key")
	}
}

func TestReregistrationKeepsTheFirstDefinition(t *testing.T) {
	f := newCredentialFixture(t)
	_, err := f.broker.RegisterAgent(context.Background(), credentialTestWorkload, RegisterAgentRequest{
		SessionID: f.session.ID, HostSessionID: f.session.HostSessionID, Owner: f.session.Owner,
		Incarnation: incMain, Definition: DefinitionRef{Name: "deployer"},
	})
	if err != nil {
		t.Fatalf("RegisterAgent again: %v", err)
	}
	if got := f.broker.sessions.agents[f.reg.Agent.Subject].definition; got != (DefinitionRef{Name: "main"}) {
		t.Fatalf("stored definition = %+v, want the first one reported", got)
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
