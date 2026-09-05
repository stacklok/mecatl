package actingaccess

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/internal/identityissuer"
)

func TestADR_0302_ClosedSubjectAssertionProfile(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	key := newP256Key(t)
	presenter, err := NewPresenter("broker-prod")
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewJWTSubjectAssertionVerifier(SubjectVerifierConfig{
		Issuer: "https://issuer.example", Audience: "mecatl-exchange", AuthorizedParty: "broker-prod",
		MaxAge: 2 * time.Minute, ClockSkew: 5 * time.Second, MaxTokenBytes: 4096,
		Now: func() time.Time { return now }, Keys: []SubjectKey{{ID: "subject-key", PublicKey: &key.PublicKey, NotAfter: now.Add(10 * time.Minute)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := mustOwner(t, "https://issuer.example", "alice")
	claims := map[string]any{
		"iss": "https://issuer.example", "sub": "alice", "aud": []string{"mecatl-exchange"},
		"iat": now.Add(-time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(),
		"azp": "broker-prod", "consent": "consent-v7",
	}
	valid := subjectJWT(t, key, "subject-key", jwt.SigningMethodES256, ExchangeSubjectTokenType, claims)
	assertion, err := NewSubjectAssertion(valid)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifySubjectForOwner(verifier, assertion, presenter, owner)
	if err != nil || got.Owner() != owner || got.ConsentProof() != "consent-v7" || !got.NotAfter().Equal(now.Add(time.Minute)) {
		t.Fatalf("VerifySubjectForOwner() = %#v, %v", got, err)
	}
	for _, mismatch := range []Owner{mustOwner(t, "https://other.example", "alice"), mustOwner(t, "https://issuer.example", "bob")} {
		if result, err := VerifySubjectForOwner(verifier, assertion, presenter, mismatch); err == nil || !result.IsZero() {
			t.Fatalf("owner mismatch returned %#v, %v", result, err)
		}
	}

	cases := []struct {
		name   string
		typ    string
		claims map[string]any
		method jwt.SigningMethod
		key    *ecdsa.PrivateKey
	}{
		{"wrong audience", ExchangeSubjectTokenType, withClaim(claims, "aud", []string{"other"}), jwt.SigningMethodES256, key},
		{"multiple audience", ExchangeSubjectTokenType, withClaim(claims, "aud", []string{"mecatl-exchange", "other"}), jwt.SigningMethodES256, key},
		{"login bearer", "at+jwt", claims, jwt.SigningMethodES256, key},
		{"id token", "JWT", claims, jwt.SigningMethodES256, key},
		{"wrong type", "subject+jwt", claims, jwt.SigningMethodES256, key},
		{"wrong authorized party", ExchangeSubjectTokenType, withClaim(claims, "azp", "other"), jwt.SigningMethodES256, key},
		{"missing time", ExchangeSubjectTokenType, withoutClaim(claims, "nbf"), jwt.SigningMethodES256, key},
		{"excessive age", ExchangeSubjectTokenType, withClaim(claims, "iat", now.Add(-3*time.Minute).Unix()), jwt.SigningMethodES256, key},
		{"expiry exceeds max age", ExchangeSubjectTokenType, withClaim(claims, "exp", now.Add(time.Minute+time.Second).Unix()), jwt.SigningMethodES256, key},
	}
	wrongAlgorithmKey := newP384Key(t)
	cases = append(cases, struct {
		name   string
		typ    string
		claims map[string]any
		method jwt.SigningMethod
		key    *ecdsa.PrivateKey
	}{"wrong algorithm", ExchangeSubjectTokenType, claims, jwt.SigningMethodES384, wrongAlgorithmKey})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := subjectJWT(t, tc.key, "subject-key", tc.method, tc.typ, tc.claims)
			credential, err := NewSubjectAssertion(raw)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := verifier.Verify(credential, presenter); err == nil || !result.IsZero() {
				t.Fatalf("Verify() = %#v, %v", result, err)
			}
		})
	}
	duplicate := duplicateJWTClaim(t, key, valid, `,"sub":"alice"`)
	duplicateAssertion, _ := NewSubjectAssertion(duplicate)
	if result, err := verifier.Verify(duplicateAssertion, presenter); err == nil || !result.IsZero() {
		t.Fatalf("duplicate claim = %#v, %v", result, err)
	}
	oversized, _ := NewSubjectAssertion(strings.Repeat("x", 4097))
	if result, err := verifier.Verify(oversized, presenter); err == nil || !result.IsZero() {
		t.Fatalf("oversized assertion = %#v, %v", result, err)
	}
	otherPresenter, err := NewPresenter("broker-other")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := verifier.Verify(assertion, otherPresenter); err == nil || !result.IsZero() {
		t.Fatalf("presenter mismatch = %#v, %v", result, err)
	}
	keyEntry := verifier.keys["subject-key"]
	keyEntry.notAfter = now.Add(-time.Second)
	verifier.keys["subject-key"] = keyEntry
	if result, err := verifier.Verify(assertion, presenter); err == nil || !result.IsZero() {
		t.Fatalf("retired subject key = %#v, %v", result, err)
	}
}

func TestADR_0302_ActorProfileVerificationAndRotation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	oldDER, oldKey := newPKCS8(t)
	newDER, _ := newPKCS8(t)
	keys := map[string][]byte{"old": oldDER, "new": newDER}
	prepublish := loadActorIssuer(t, rotationManifest(41, "prepublish", 7, 0, "old", "new"), keys)
	active := loadActorIssuer(t, rotationManifest(42, "active", 8, 0, "new", "old"), keys)
	oldToken := issueActor(t, prepublish, "mecatl-exchange", []string{"Read"})
	newToken := issueActor(t, active, "mecatl-exchange", []string{"Read", "Write"})
	activeBundle, err := active.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock := now
	verifier := newActorVerifier(t, activeBundle, &clock, 5*time.Minute)
	for _, raw := range []string{oldToken, newToken} {
		actor := verifyActor(t, verifier, raw)
		if actor.Subject() == "" || len(actor.Tools()) == 0 || actor.NotAfter().IsZero() {
			t.Fatalf("incomplete verified actor: %#v", actor)
		}
	}

	wrongAudienceIssuer := loadActorIssuer(t, actorManifest(), map[string][]byte{"old": oldDER}, "other-audience")
	wrongAudience := issueActor(t, wrongAudienceIssuer, "other-audience", []string{"Read"})
	duplicate := duplicateJWTClaim(t, oldKey, oldToken, `,"sub":"duplicate"`)
	for _, tc := range []struct{ name, raw string }{
		{"wrong audience", wrongAudience}, {"malformed", "not-a-jwt"},
		{"oversized", strings.Repeat("x", identityissuer.MaxTokenBytes+1)}, {"duplicate claim", duplicate},
		{"algorithm confused", rewriteJWTHeader(t, oldToken, `{"alg":"HS256","kid":"confused","typ":"JWT"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			credential, _ := NewI2Token(tc.raw)
			if actor, err := verifier.Verify(credential); err == nil || !actor.IsZero() {
				t.Fatalf("Verify() = %#v, %v", actor, err)
			}
		})
	}
	expiringClock := now
	expiringVerifier := newActorVerifier(t, activeBundle, &expiringClock, 5*time.Minute)
	expiringClock = expiringClock.Add(2 * time.Minute)
	if actor, err := expiringVerifier.Verify(mustI2(t, oldToken)); err == nil || !actor.IsZero() {
		t.Fatalf("expired actor = %#v, %v", actor, err)
	}

	overlap := time.Minute + 5*time.Second + 5*time.Minute
	retired := loadActorIssuer(t, rotationManifest(43, "retired", 9, time.Now().Add(-overlap).Unix(), "new"), keys)
	retiredBundle, err := retired.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retiredVerifier := newActorVerifier(t, retiredBundle, &now, 5*time.Minute)
	if actor, err := retiredVerifier.Verify(mustI2(t, oldToken)); err == nil || !actor.IsZero() {
		t.Fatalf("retired old key = %#v, %v", actor, err)
	}
	verifyActor(t, retiredVerifier, newToken)

	staleClock := now
	stale := newActorVerifier(t, activeBundle, &staleClock, time.Minute)
	staleClock = staleClock.Add(time.Minute + time.Second)
	if actor, err := stale.Verify(mustI2(t, newToken)); err == nil || !actor.IsZero() {
		t.Fatalf("stale bundle = %#v, %v", actor, err)
	}
	if err := verifier.Refresh(context.Background()); err == nil {
		t.Fatal("refresh accepted stale/regressed bundle")
	}
}

func TestInvariant_acting_access_verified_actor_copy(t *testing.T) {
	der, _ := newPKCS8(t)
	issuer := loadActorIssuer(t, actorManifest(), map[string][]byte{"old": der})
	raw := issueActor(t, issuer, "mecatl-exchange", []string{"Read"})
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	verifier := newActorVerifier(t, bundle, &now, 5*time.Minute)
	actor := verifyActor(t, verifier, raw)
	tools := actor.Tools()
	tools[0] = "Write"
	if actor.ContainsRequiredTools([]string{"Write"}) || !actor.ContainsRequiredTools([]string{"Read"}) {
		t.Fatal("post-verification mutation widened actor authority")
	}
	if (VerifiedActor{}).ContainsRequiredTools([]string{"Read"}) {
		t.Fatal("caller-constructed actor carries authority")
	}
	again := verifyActor(t, verifier, raw)
	if !reflect.DeepEqual(again.Tools(), []string{"Read"}) {
		t.Fatalf("reverification tools = %#v", again.Tools())
	}
}

func mustOwner(t *testing.T, issuer, subject string) Owner {
	t.Helper()
	owner, err := NewOwner(issuer, subject)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func withClaim(base map[string]any, name string, value any) map[string]any {
	out := make(map[string]any, len(base))
	for key, item := range base {
		out[key] = item
	}
	out[name] = value
	return out
}

func withoutClaim(base map[string]any, name string) map[string]any {
	out := withClaim(base, "", nil)
	delete(out, "")
	delete(out, name)
	return out
}

func newP256Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newP384Key(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func subjectJWT(t *testing.T, key *ecdsa.PrivateKey, kid string, method jwt.SigningMethod, typ string, claims map[string]any) string {
	t.Helper()
	token := jwt.NewWithClaims(method, jwt.MapClaims(claims))
	token.Header["kid"] = kid
	token.Header["typ"] = typ
	raw, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func duplicateJWTClaim(t *testing.T, key *ecdsa.PrivateKey, raw, duplicate string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload[:len(payload)-1], []byte(duplicate+"}")...)
	signing := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload)
	signature, err := jwt.SigningMethodES256.Sign(signing, key)
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func rewriteJWTHeader(t *testing.T, raw, header string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	return base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + parts[1] + "." + parts[2]
}

func newPKCS8(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key := newP256Key(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func actorManifest() []byte {
	return []byte(`{"version":1,"keys":[{"name":"old","active":true}]}`)
}

func rotationManifest(generation uint64, phase string, sequence uint64, lastOld int64, active string, others ...string) []byte {
	items := fmt.Sprintf(`{"name":%q,"active":true}`, active)
	for _, name := range others {
		items += fmt.Sprintf(`,{"name":%q,"active":false}`, name)
	}
	return []byte(fmt.Sprintf(`{"version":2,"generation":%d,"phase":%q,"sequence":%d,"last_old_issuance_unix":%d,"keys":[%s]}`, generation, phase, sequence, lastOld, items))
}

func loadActorIssuer(t *testing.T, manifest []byte, keys map[string][]byte, audience ...string) *identityissuer.Issuer {
	t.Helper()
	aud := "mecatl-exchange"
	if len(audience) > 0 {
		aud = audience[0]
	}
	issuer, err := identityissuer.Load(identityissuer.Config{
		Enabled: true, TrustDomain: "example.org", TokenTTL: time.Minute, ClockSkew: 5 * time.Second,
		BundleCacheTTL: 5 * time.Minute, Audience: aud, HTTPSBootstrapURL: "https://issuer.example/bundle",
	}, manifest, func(name string) ([]byte, error) { return keys[name], nil })
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func issueActor(t *testing.T, issuer *identityissuer.Issuer, audience string, tools []string) string {
	t.Helper()
	_ = audience
	identity, err := identityissuer.NewLogicalAgentIdentity("example.org", identityissuer.DefinitionTierProject, "Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.IssueLogicalAgent(identityissuer.LogicalAgentIssueRequest{
		Identity: identity, Tools: tools, Source: governance.CapabilitySet{Tools: append([]string(nil), tools...)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func newActorVerifier(t *testing.T, bundle []byte, now *time.Time, cacheTTL time.Duration) *LogicalActorVerifier {
	t.Helper()
	underlying, err := identityissuer.NewLogicalAgentVerifier(identityissuer.VerifierConfig{
		TrustDomain: "example.org", Audience: "mecatl-exchange", TokenTTL: time.Minute, ClockSkew: 5 * time.Second,
		BundleCacheTTL: cacheTTL, HTTPSBootstrapURL: "https://issuer.example/bundle", Now: func() time.Time { return *now },
	}, identityissuer.BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewLogicalActorVerifier(underlying)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return verifier
}

func verifyActor(t *testing.T, verifier *LogicalActorVerifier, raw string) VerifiedActor {
	t.Helper()
	actor, err := verifier.Verify(mustI2(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func mustI2(t *testing.T, raw string) I2Token {
	t.Helper()
	credential, err := NewI2Token(raw)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}
