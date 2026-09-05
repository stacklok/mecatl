package identityissuer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/engine/governance"
)

func TestLogicalAgentIdentityProjection_Scenario4_IndependentTypedVerification(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierProject, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity,
		Instance: "session-42",
		Tools:    []string{"Write", "Read"},
		Source:   governance.CapabilitySet{Tools: []string{"Read", "Write"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier := testLogicalAgentVerifier(t, issuer)
	verified, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	want := VerifiedLogicalAgent{
		TrustDomain: issuer.TrustDomain(),
		Subject:     identity.Subject,
		Tier:        DefinitionTierProject,
		Name:        "Code Reviewer",
		Instance:    "session-42",
		Tools:       []string{"Read", "Write"},
		JWTID:       logicalAgentJWTID(t, issuer, token),
		Expiry:      time.Unix(1_700_000_300, 0).UTC(),
	}
	if !reflect.DeepEqual(verified, want) {
		t.Fatalf("Verify() = %#v, want %#v", verified, want)
	}
	verified.Tools[0] = "mutated"
	again, err := verifier.Verify(token)
	if err != nil || !reflect.DeepEqual(again.Tools, want.Tools) {
		t.Fatalf("Verify after output mutation = %#v, %v; want fresh tools %#v", again, err, want.Tools)
	}
}

func TestADR_0301_TypedVerifierFailsClosed(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: []string{"Read"}, Source: governance.CapabilitySet{Tools: []string{"Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	verifier := testLogicalAgentVerifier(t, issuer)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongAlgorithm, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	claims := logicalAgentClaims(t, valid)
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"payload tamper", tamperPayload(valid)},
		{"attacker key", signedLogicalAgentToken(t, other, issuer.ActiveKID(), jwt.SigningMethodES256, claims)},
		{"wrong algorithm", signedLogicalAgentToken(t, wrongAlgorithm, issuer.ActiveKID(), jwt.SigningMethodES384, claims)},
		{"unknown key", signedLogicalAgentToken(t, issuer.key, "unknown", jwt.SigningMethodES256, claims)},
		{"wrong issuer", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "iss", "spiffe://other.example"))},
		{"wrong audience", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "aud", []string{"other"}))},
		{"expired", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "exp", float64(1)))},
		{"missing jti", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWithout(t, claims, "jti"))},
		{"empty jti", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "jti", ""))},
		{"wrong length jti", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "jti", "A"))},
		{"nonbase64url jti", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "jti", strings.Repeat("!", 22)))},
		{"padded jti", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "jti", strings.Repeat("A", 21)+"="))},
		{"noncanonical tools", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentProfileTools(t, claims, []string{"Write", "Read"}))},
		{"subject disagreement", signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, logicalAgentClaimsWith(t, claims, "sub", identity.Subject+"x"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifier.Verify(tc.token)
			if err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
				t.Fatalf("Verify() = %#v, %v; want zero result and error", got, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		claims map[string]any
	}{
		{"missing profile", logicalAgentClaimsWithout(t, claims, LogicalAgentClaimURI)},
		{"malformed profile", logicalAgentClaimsWith(t, claims, LogicalAgentClaimURI, "not an object")},
		{"unknown profile tier", logicalAgentProfileWith(t, claims, "definition_tier", "unknown")},
		{"unknown profile member", logicalAgentProfileWith(t, claims, "version", "v2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := verifier.Verify(signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, tc.claims))
			if err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
				t.Fatalf("Verify() = %#v, %v; want zero result and error", got, err)
			}
		})
	}
	unready, err := NewLogicalAgentVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := unready.Verify(valid); err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
		t.Fatalf("unready verifier = %#v, %v; want zero result and error", got, err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testVerifierConfig()
	cfg.Now = func() time.Time { return now }
	bounded, err := NewLogicalAgentVerifier(cfg, BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := bounded.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bounded.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh accepted regressed bundle")
	}
	now = now.Add(cfg.BundleCacheTTL + time.Second)
	if got, err := bounded.Verify(valid); err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
		t.Fatalf("stale bundle verifier = %#v, %v; want zero result and error", got, err)
	}
	incomplete, err := NewLogicalAgentVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return []byte(`{"sequence":1}`), nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := incomplete.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh accepted incomplete bundle")
	}
}

func TestADR_0301_TypedVerifierUsesOneSecurityRepresentation(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: []string{"Read"}, Source: governance.CapabilitySet{Tools: []string{"Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	verifier := testLogicalAgentVerifier(t, issuer)
	parts := strings.Split(valid, ".")
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		header  []byte
		payload []byte
	}{
		{"duplicate protected header", append(header[:len(header)-1], []byte(`,"kid":"`+issuer.ActiveKID()+`"}`)...), payload},
		{"duplicate registered claim", header, append(payload[:len(payload)-1], []byte(`,"sub":"`+identity.Subject+`"}`)...)},
		{"duplicate profile", header, append(payload[:len(payload)-1], []byte(`,"`+LogicalAgentClaimURI+`":{}}`)...)},
		{"duplicate profile member", header, replaceProfile(t, payload, `{"definition_tier":"managed","definition_tier":"managed","definition_name":"Code Reviewer","tools":["Read"]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := signRawJWT(t, issuer.key, tc.header, tc.payload)
			got, err := verifier.Verify(token)
			if err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
				t.Fatalf("Verify duplicate = %#v, %v; want zero result and error", got, err)
			}
		})
	}
	if got, err := verifier.Verify(valid); err != nil || got.Subject != identity.Subject {
		t.Fatalf("canonical positive control = %#v, %v", got, err)
	}
}

func TestADR_0301_ProfileConfusionMatrix(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	i1, err := issuer.IssueJWTSubject("spiffe://example.org/workload/api", testConfig().Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	i2, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: []string{"Read"}, Source: governance.CapabilitySet{Tools: []string{"Read"}}})
	if err != nil {
		t.Fatal(err)
	}
	typed := testLogicalAgentVerifier(t, issuer)
	canary := testVerifier(t, issuer)
	if got, err := typed.Verify(i1); err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
		t.Fatalf("typed verifier accepted I1 canary: %#v, %v", got, err)
	}
	if got, err := canary.Verify(i2); err == nil || got != (VerifiedIdentity{}) {
		t.Fatalf("canary verifier accepted I2 profile: %#v, %v", got, err)
	}
	claims := logicalAgentClaims(t, i2)
	claims["unrelated"] = map[string]any{"tools": []string{"Write"}}
	unrelated := signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, claims)
	if got, err := typed.Verify(unrelated); err != nil || !reflect.DeepEqual(got.Tools, []string{"Read"}) {
		t.Fatalf("unrelated claim changed typed authority: %#v, %v", got, err)
	}
	claims = logicalAgentClaims(t, i2)
	claims["https://mecatl.dev/claims/logical-agent/v2"] = map[string]any{"tools": []string{"Write"}}
	multipleProfiles := signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, claims)
	if got, err := typed.Verify(multipleProfiles); err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
		t.Fatalf("multiple profiles selected typed authority: %#v, %v", got, err)
	}
}

func testLogicalAgentVerifier(t *testing.T, issuer *Issuer) *LogicalAgentVerifier {
	t.Helper()
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testVerifierConfig()
	cfg.Now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	verifier, err := NewLogicalAgentVerifier(cfg, BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return verifier
}

func testVerifier(t *testing.T, issuer *Issuer) *Verifier {
	t.Helper()
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(testVerifierConfig(), BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return verifier
}

func tamperPayload(token string) string {
	parts := strings.Split(token, ".")
	if parts[1][0] == 'A' {
		parts[1] = "B" + parts[1][1:]
	} else {
		parts[1] = "A" + parts[1][1:]
	}
	return strings.Join(parts, ".")
}

func logicalAgentClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func logicalAgentClaimsWith(t *testing.T, claims map[string]any, name string, value any) map[string]any {
	t.Helper()
	cloned := mapsClone(t, claims)
	cloned[name] = value
	return cloned
}

func logicalAgentClaimsWithout(t *testing.T, claims map[string]any, name string) map[string]any {
	t.Helper()
	cloned := mapsClone(t, claims)
	delete(cloned, name)
	return cloned
}

func logicalAgentProfileWith(t *testing.T, claims map[string]any, name string, value any) map[string]any {
	t.Helper()
	cloned := mapsClone(t, claims)
	profile := mapsClone(t, cloned[LogicalAgentClaimURI].(map[string]any))
	profile[name] = value
	cloned[LogicalAgentClaimURI] = profile
	return cloned
}

func logicalAgentProfileTools(t *testing.T, claims map[string]any, tools []string) map[string]any {
	t.Helper()
	cloned := mapsClone(t, claims)
	profile := mapsClone(t, cloned[LogicalAgentClaimURI].(map[string]any))
	profile["tools"] = tools
	cloned[LogicalAgentClaimURI] = profile
	return cloned
}

func mapsClone(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatal(err)
	}
	return output
}

func signedLogicalAgentToken(t *testing.T, key *ecdsa.PrivateKey, kid string, method jwt.SigningMethod, claims map[string]any) string {
	t.Helper()
	token := jwt.NewWithClaims(method, jwt.MapClaims(claims))
	token.Header["kid"] = kid
	compact, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return compact
}

func signRawJWT(t *testing.T, key *ecdsa.PrivateKey, header, payload []byte) string {
	t.Helper()
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	signature, err := jwt.SigningMethodES256.Sign(signing, key)
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func replaceProfile(t *testing.T, payload []byte, profile string) []byte {
	t.Helper()
	var claims map[string]json.RawMessage
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	needle := `"` + LogicalAgentClaimURI + `":` + string(claims[LogicalAgentClaimURI])
	return []byte(strings.Replace(string(encoded), needle, `"`+LogicalAgentClaimURI+`":`+profile, 1))
}
