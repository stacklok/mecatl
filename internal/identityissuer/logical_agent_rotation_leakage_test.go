package identityissuer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/mecatl/engine/governance"
)

func TestLogicalAgentIdentityProjection_Scenario5_RotationPreservesLogicalIdentity(t *testing.T) {
	oldKey := testPKCS8(t)
	newKey := testPKCS8(t)
	keys := map[string][]byte{"old": oldKey, "new": newKey}
	issuedAt := time.Unix(1_700_000_000, 0).UTC()

	prepublished := loadRotation(t, 41, RotationPhasePrepublish, 7, 0, keys, "old")
	prepublished.now = func() time.Time { return issuedAt }
	identity, err := NewLogicalAgentIdentity(prepublished.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	request := LogicalAgentIssueRequest{
		Identity: identity,
		Instance: "session-42",
		Tools:    []string{"Read", "Write"},
		Source:   governance.CapabilitySet{Tools: []string{"Read", "Write"}},
	}
	oldToken, err := prepublished.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}

	activated := loadRotation(t, 42, RotationPhaseActive, 8, 0, keys, "new")
	activated.now = func() time.Time { return issuedAt.Add(time.Second) }
	request.Tools = []string{"Read"}
	newToken, err := activated.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}

	oldVerified := verifyLogicalAgentForBundle(t, prepublished, issuedAt, oldToken)
	newVerified := verifyLogicalAgentForBundle(t, activated, issuedAt.Add(time.Second), newToken)
	if oldVerified.Subject != newVerified.Subject || oldVerified.Tier != newVerified.Tier || oldVerified.Name != newVerified.Name || oldVerified.Instance != newVerified.Instance {
		t.Fatalf("logical identity drifted across remint: before=%#v after=%#v", oldVerified, newVerified)
	}
	if !reflect.DeepEqual(oldVerified.Tools, []string{"Read", "Write"}) || !reflect.DeepEqual(newVerified.Tools, []string{"Read"}) {
		t.Fatalf("remint tools = %#v then %#v; want same-or-narrower exact authority", oldVerified.Tools, newVerified.Tools)
	}
	if oldVerified.JWTID == newVerified.JWTID || !newVerified.Expiry.After(oldVerified.Expiry) {
		t.Fatalf("remint did not refresh credential metadata: before=%#v after=%#v", oldVerified, newVerified)
	}
	if oldToken == newToken || logicalAgentSignature(t, oldToken) == logicalAgentSignature(t, newToken) || logicalAgentKID(t, oldToken) != prepublished.ActiveKID() || logicalAgentKID(t, newToken) != activated.ActiveKID() || logicalAgentKID(t, oldToken) == logicalAgentKID(t, newToken) {
		t.Fatalf("remint did not use fresh active-key credential")
	}
}

func TestADR_0301_LogicalAgentRotationOverlap(t *testing.T) {
	oldKey := testPKCS8(t)
	newKey := testPKCS8(t)
	keys := map[string][]byte{"old": oldKey, "new": newKey}
	issuedAt := time.Unix(1_700_000_000, 0).UTC()
	config := testConfig()
	overlap := config.TokenTTL + config.ClockSkew + config.BundleCacheTTL

	prepublished := loadRotation(t, 41, RotationPhasePrepublish, 7, 0, keys, "old")
	prepublished.now = func() time.Time { return issuedAt }
	identity, err := NewLogicalAgentIdentity(prepublished.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	request := LogicalAgentIssueRequest{Identity: identity, Instance: "session-42", Tools: []string{"Read"}, Source: governance.CapabilitySet{Tools: []string{"Read"}}}
	oldToken, err := prepublished.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}

	activated := loadRotation(t, 42, RotationPhaseActive, 8, 0, keys, "new")
	activated.now = func() time.Time { return issuedAt.Add(time.Second) }
	newToken, err := activated.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := prepublished.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := logicalAgentVerifierForBundle(t, bundle, issuedAt.Add(time.Second))
	for _, token := range []string{oldToken, newToken} {
		if _, err := verifier.Verify(token); err != nil {
			t.Fatalf("overlap verifier rejected unexpired token: %v", err)
		}
	}

	retired := loadAtRotation(t, config, 43, RotationPhaseRetired, 9, issuedAt.Unix(), keys, "new", issuedAt.Add(overlap))
	retired.now = func() time.Time { return issuedAt.Add(overlap) }
	remintedToken, err := retired.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	retiredBundle, err := retired.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	retiredVerifier := logicalAgentVerifierForBundle(t, retiredBundle, issuedAt.Add(overlap))
	if got, err := retiredVerifier.Verify(oldToken); err == nil || !reflect.DeepEqual(got, VerifiedLogicalAgent{}) {
		t.Fatalf("retired verifier accepted old token: %#v, %v", got, err)
	}
	if _, err := retiredVerifier.Verify(remintedToken); err != nil {
		t.Fatalf("retired verifier rejected new-key remint: %v", err)
	}
}

func TestADR_0301_LogicalAgentCredentialCanariesNeverPersistOrLeak(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	const toolCanary = "TOOL-CANARY-5f266d2a"
	const instanceCanary = "INSTANCE-CANARY-d0d73d37"
	const unknownClaimCanary = "UNKNOWN-CLAIM-CANARY-9e45b2f1"
	compact, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity,
		Instance: instanceCanary,
		Tools:    []string{toolCanary},
		Source:   governance.CapabilitySet{Tools: []string{toolCanary}},
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[2] == "" {
		t.Fatal("successful issuance did not return compact credential with signature")
	}
	credentialCanaries := []string{compact, parts[2]}
	for _, canary := range credentialCanaries {
		if !strings.Contains(compact, canary) {
			t.Fatalf("issuance boundary did not contain credential canary %q", canary)
		}
	}

	verifier := testLogicalAgentVerifier(t, issuer)
	verified, err := verifier.Verify(compact)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Instance != instanceCanary || !reflect.DeepEqual(verified.Tools, []string{toolCanary}) {
		t.Fatalf("approved fields lost from verified projection: %#v", verified)
	}
	assertNoCanary(t, "typed verifier result", credentialCanaries, verified)
	assertNoCanary(t, "generation status", credentialCanaries, (&Host{issuer: issuer, verified: true}).Status())

	issueError := issueErrorWithCanary(t, issuer, identity, toolCanary, instanceCanary)
	assertBoundedError(t, "issue", issueError)
	assertNoCanary(t, "bounded issue error", append(credentialCanaries, toolCanary, instanceCanary), issueError)

	claims := logicalAgentClaims(t, compact)
	claims = logicalAgentProfileWith(t, claims, "unknown_"+unknownClaimCanary, unknownClaimCanary)
	rejected := signedLogicalAgentToken(t, issuer.key, issuer.ActiveKID(), jwt.SigningMethodES256, claims)
	result, verifyError := verifier.Verify(rejected)
	if verifyError == nil || !reflect.DeepEqual(result, VerifiedLogicalAgent{}) {
		t.Fatalf("rejected claim = %#v, %v; want zero result and error", result, verifyError)
	}
	assertBoundedError(t, "verify", verifyError)
	assertNoCanary(t, "bounded verify error", append(credentialCanaries, toolCanary, instanceCanary, unknownClaimCanary), verifyError)

	_, hostError := NewHost(HostConfig{
		Issuer:      testConfig(),
		Manifest:    testManifest("active", true),
		RefreshHint: time.Minute,
		LoadKey: func(string) ([]byte, error) {
			return nil, errors.New(toolCanary + " " + instanceCanary + " " + unknownClaimCanary)
		},
	})
	if hostError == nil {
		t.Fatal("host accepted canary-bearing key-loader failure")
	}
	assertBoundedError(t, "host diagnostic", hostError)
	assertNoCanary(t, "host diagnostic/error projection", append(credentialCanaries, toolCanary, instanceCanary, unknownClaimCanary), hostError)

}

func verifyLogicalAgentForBundle(t *testing.T, issuer *Issuer, now time.Time, token string) VerifiedLogicalAgent {
	t.Helper()
	bundle, err := issuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	verifier := logicalAgentVerifierForBundle(t, bundle, now)
	verified, err := verifier.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

func logicalAgentVerifierForBundle(t *testing.T, bundle []byte, now time.Time) *LogicalAgentVerifier {
	t.Helper()
	cfg := testVerifierConfig()
	cfg.Now = func() time.Time { return now }
	verifier, err := NewLogicalAgentVerifier(cfg, BundleFetcherFunc(func(context.Context, string) ([]byte, error) { return bundle, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return verifier
}

func loadAtRotation(t *testing.T, cfg Config, generation uint64, phase RotationPhase, sequence uint64, lastOldIssuance int64, keys map[string][]byte, active string, now time.Time) *Issuer {
	t.Helper()
	issuer, err := loadAt(cfg, rotationManifest(generation, phase, sequence, lastOldIssuance, []rotationTestKey{{Name: active, Active: true}}), func(name string) ([]byte, error) { return keys[name], nil }, now)
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func logicalAgentSignature(t *testing.T, compact string) string {
	t.Helper()
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || parts[2] == "" {
		t.Fatal("compact token has no signature")
	}
	return parts[2]
}

func logicalAgentKID(t *testing.T, compact string) string {
	t.Helper()
	parsed, _, err := jwt.NewParser(jwt.WithoutClaimsValidation()).ParseUnverified(compact, jwt.MapClaims{})
	if err != nil {
		t.Fatal(err)
	}
	kid, ok := parsed.Header["kid"].(string)
	if !ok {
		t.Fatal("compact token has no key ID")
	}
	return kid
}

func issueErrorWithCanary(t *testing.T, issuer *Issuer, identity LogicalAgentIdentity, tool, instance string) error {
	t.Helper()
	_, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity,
		Instance: instance,
		Tools:    []string{tool},
		Source:   governance.CapabilitySet{},
	})
	if err == nil {
		t.Fatal("canary-bearing rejected issue unexpectedly succeeded")
	}
	return err
}

func assertBoundedError(t *testing.T, sink string, err error) {
	t.Helper()
	if len(err.Error()) > 256 {
		t.Fatalf("%s error exceeds bounded projection: %d bytes", sink, len(err.Error()))
	}
}

func assertNoCanary(t *testing.T, sink string, canaries []string, value any) {
	t.Helper()
	projection := toText(value)
	for _, canary := range canaries {
		if strings.Contains(projection, canary) {
			t.Fatalf("credential canary leaked into %s", sink)
		}
	}
}

func toText(value any) string {
	if err, ok := value.(error); ok {
		return err.Error()
	}
	return fmt.Sprintf("%#v", value)
}
