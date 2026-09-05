package actingaccess

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func outputTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	raw := make([]byte, 32)
	raw[len(raw)-1] = 1
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type outputFixture struct {
	scenario3Fixture
	issuer   *DeterministicOutputIssuer
	verifier *JWTOutputVerifier
}

func newOutputFixture(t *testing.T) outputFixture {
	t.Helper()
	base := newScenario3Fixture(t)
	key := outputTestKey(t)
	issuer, err := NewDeterministicOutputIssuer(OutputIssuerConfig{
		Issuer: "https://as.example", ClientID: "broker-prod", KeyID: "output-1", PrivateKey: key,
		Now: func() time.Time { return base.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewJWTOutputVerifier(OutputVerifierConfig{
		Issuer: "https://as.example", ClientID: "broker-prod", KeyID: "output-1", PublicKey: &key.PublicKey,
		KeyNotAfter: base.now.Add(time.Hour), MaxAge: 2 * time.Minute, Now: func() time.Time { return base.now }, ClockSkew: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	base.gate.cfg.Mechanism = issuer
	base.gate.cfg.OutputVerifier = verifier
	return outputFixture{scenario3Fixture: base, issuer: issuer, verifier: verifier}
}

func (f outputFixture) exchangeOutput(t *testing.T, request Request) (ExchangeResult, VerifiedOutput) {
	t.Helper()
	result, err := f.exchange(t, request, "output-proof")
	if err != nil {
		t.Fatal(err)
	}
	verified := result.VerifiedOutput()
	if verified.IsZero() {
		t.Fatal("Exchange returned no verified output")
	}
	return result, verified
}

func outputPlan(result ExchangeResult) MechanismInput { return result.plan }

func mintedOutput(t *testing.T, issuer *DeterministicOutputIssuer, plan MechanismInput, overrides map[string]any) OutputToken {
	t.Helper()
	token, err := issuer.mintToken(plan, overrides)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func signedOutput(t *testing.T, issuer *DeterministicOutputIssuer, header, claims string) OutputToken {
	t.Helper()
	encodedHeader := base64.RawURLEncoding.EncodeToString([]byte(header))
	encodedClaims := base64.RawURLEncoding.EncodeToString([]byte(claims))
	signature, err := jwt.SigningMethodES256.Sign(encodedHeader+"."+encodedClaims, issuer.privateKey)
	if err != nil {
		t.Fatal(err)
	}
	token, err := NewOutputToken(encodedHeader + "." + encodedClaims + "." + base64.RawURLEncoding.EncodeToString(signature))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func exactOutputClaims(f outputFixture, plan MechanismInput) string {
	return `{"iss":"https://as.example","sub":"` + qualifiedOutputSubject(f.owner) + `","aud":"` + plan.Resource().Value() + `","iat":` +
		"2051319845" + `,"nbf":2051319845,"exp":2051320145,"act":{"sub":"` + plan.ActorSubject() + `"},"client_id":"broker-prod","scope":"` +
		plan.Scopes()[0] + `","authorization_details":"` + plan.Detail().Value() + `"}`
}

func TestActingAccess_Scenario4_VerifiesExactOutputProfile(t *testing.T) {
	f := newOutputFixture(t)
	result, verified := f.exchangeOutput(t, f.request)
	response := result.Response()
	if response.IssuedTokenType() != "urn:ietf:params:oauth:token-type:access_token" || response.TokenType() != "Bearer" ||
		response.ExpiresIn() != 300 || response.Scope() != "repo:read" || response.token.secret.value == nil {
		t.Fatalf("RFC 8693 response = %#v", response)
	}
	if verified.Subject() != qualifiedOutputSubject(f.owner) || verified.ActorSubject() != f.actor.verified.Subject() ||
		verified.ClientID() != f.presenter.Value() || verified.Audience() != f.request.Resource().Value() ||
		!reflect.DeepEqual(verified.Scopes(), f.request.Scopes()) || verified.Detail() != f.request.Detail().Value() ||
		!verified.ExpiresAt().Equal(f.now.Add(5*time.Minute)) {
		t.Fatalf("verified output does not match exact plan: %#v", verified)
	}
	scopes := verified.Scopes()
	scopes[0] = "repo:write"
	if got := result.VerifiedOutput().Scopes(); !reflect.DeepEqual(got, []string{"repo:read"}) {
		t.Fatalf("verified output accessor aliases scopes: %#v", got)
	}
}

func TestADR_0302_OutputLifetimeCeiling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shorten func(*outputFixture)
	}{
		{"subject", func(f *outputFixture) { f.subject.verified.notAfter = f.now.Add(time.Minute) }},
		{"actor", func(f *outputFixture) { f.actor.verified.notAfter = f.now.Add(time.Minute) }},
		{"consent", func(f *outputFixture) {
			f.spies.source(FailureConsent).decision = mustDecision(DecisionPermit, f.now.Add(time.Minute))
		}},
		{"association", func(f *outputFixture) {
			f.spies.source(FailureAssociation).decision = mustDecision(DecisionPermit, f.now.Add(time.Minute))
		}},
		{"target policy", func(f *outputFixture) {
			f.spies.source(FailureTargetPolicy).decision = mustDecision(DecisionPermit, f.now.Add(time.Minute))
		}},
		{"configured", func(f *outputFixture) { f.gate.cfg.MaximumLifetime = time.Minute }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOutputFixture(t)
			tc.shorten(&f)
			_, verified := f.exchangeOutput(t, f.request)
			if !verified.ExpiresAt().Equal(f.now.Add(time.Minute)) {
				t.Fatalf("expiry = %v", verified.ExpiresAt())
			}
		})
	}
	f := newOutputFixture(t)
	f.issuer.omitExpiry = true
	if _, err := f.exchange(t, f.request, "missing-expiry"); !IsFailure(err, FailureOutputVerification) {
		t.Fatalf("missing output bound err = %v", err)
	}
}

func TestADR_0302_OutputProfileConfusionRefused(t *testing.T) {
	f := newOutputFixture(t)
	result, _ := f.exchangeOutput(t, f.request)
	for _, tc := range []struct {
		name   string
		mutate func(*ExchangeResponse)
	}{
		{"missing response scope", func(r *ExchangeResponse) { r.scope = "" }},
		{"refresh token", func(r *ExchangeResponse) { r.refreshToken = "forbidden" }},
		{"wrong issuer", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"iss": "https://other.example"})
		}},
		{"wrong user", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"sub": "sha256:other"})
		}},
		{"wrong actor", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"act": map[string]any{"sub": "spiffe://agents.example/agent/project/other"}})
		}},
		{"wrong client", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"client_id": "other"})
		}},
		{"nested act", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"act": map[string]any{"sub": f.actor.verified.Subject(), "act": map[string]any{"sub": "nested"}}})
		}},
		{"cnf", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"cnf": map[string]any{"jkt": "forbidden"}})
		}},
		{"wrong audience", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"aud": "other"})
		}},
		{"multiple audience", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"aud": []string{f.request.Resource().Value(), "other"}})
		}},
		{"wrong scope", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"scope": "repo:write"})
		}},
		{"wrong detail", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"authorization_details": "repository.write"})
		}},
		{"invalid temporal", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"exp": f.now.Add(-time.Minute).Unix()})
		}},
		{"wrong signature", func(r *ExchangeResponse) {
			token, err := NewOutputToken(r.token.secret.value.raw + "x")
			if err != nil {
				t.Fatal(err)
			}
			r.token = token
		}},
		{"wrong protected type", func(r *ExchangeResponse) {
			r.token = signedOutput(t, f.issuer, `{"alg":"ES256","typ":"other","kid":"output-1"}`, exactOutputClaims(f, result.plan))
		}},
		{"duplicate security claim", func(r *ExchangeResponse) {
			claims := exactOutputClaims(f, result.plan)
			claims = claims[:len(claims)-1] + `,"sub":"duplicate"}`
			r.token = signedOutput(t, f.issuer, `{"alg":"ES256","typ":"mecatl-acting-access+jwt","kid":"output-1"}`, claims)
		}},
		{"missing header field", func(r *ExchangeResponse) {
			r.token = signedOutput(t, f.issuer, `{"alg":"ES256","typ":"mecatl-acting-access+jwt"}`, exactOutputClaims(f, result.plan))
		}},
		{"duplicate protected header", func(r *ExchangeResponse) {
			r.token = signedOutput(t, f.issuer, `{"alg":"ES256","typ":"mecatl-acting-access+jwt","kid":"output-1","kid":"duplicate"}`, exactOutputClaims(f, result.plan))
		}},
		{"missing temporal claim", func(r *ExchangeResponse) {
			claims := strings.Replace(exactOutputClaims(f, result.plan), `,"nbf":2051319845`, "", 1)
			r.token = signedOutput(t, f.issuer, `{"alg":"ES256","typ":"mecatl-acting-access+jwt","kid":"output-1"}`, claims)
		}},
		{"input claim", func(r *ExchangeResponse) {
			r.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"subject_token": "forbidden"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := result.Response()
			tc.mutate(&response)
			if _, err := f.verifier.Verify(response, result.plan); err == nil {
				t.Fatal("confused output profile verified")
			}
		})
	}
}

func TestActingAccess_Scenario4_ReviewerReadDeployerWrite(t *testing.T) {
	f := newOutputFixture(t)
	_, err := f.registry.NewRequest(f.owner, f.presenter, "vmcp-deploy", "write", "deployment.write", []string{"deploy:write"})
	if err == nil {
		t.Fatal("fixture must not register deploy for reviewer")
	}
	if _, err := f.exchange(t, Request{}, "reviewer-deploy"); err == nil || f.issuer.calls != 0 {
		t.Fatal("reviewer deploy reached issuance")
	}

	f = newOutputFixture(t)
	f.registry, err = NewRegistry([]Registration{{Resource: "vmcp-deploy", Operation: "write", Detail: "deployment.write", Scopes: []string{"deploy:write"}, RequiredTools: []string{"Deploy"}}})
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := f.registry.NewRequest(f.owner, f.presenter, "vmcp-deploy", "write", "deployment.write", []string{"deploy:write"})
	if err != nil {
		t.Fatal(err)
	}
	f.actor.verified.tools = []string{"Deploy"}
	f.spies.setExpected(decisionExpectation{consent: "consent-alice-read", presenter: "broker-prod", resource: "vmcp-deploy", operation: "write", detail: "deployment.write", scopes: []string{"deploy:write"}})
	f.spies.registry.requiredTools = []string{"Deploy"}
	if _, verified := f.exchangeOutput(t, deploy); verified.ActorSubject() != f.actor.verified.Subject() {
		t.Fatal("deployer output not verified")
	}
}

func TestADR_0302_OutputBindsAuthenticatedPresenterAndFreshKey(t *testing.T) {
	f := newOutputFixture(t)
	result, _ := f.exchangeOutput(t, f.request)

	other, err := NewPresenter("broker-other")
	if err != nil {
		t.Fatal(err)
	}
	mismatchedPlan := result.plan
	mismatchedPlan.facts.presenter = other
	response := result.Response()
	response.token = mintedOutput(t, f.issuer, mismatchedPlan, nil)
	if _, err := f.verifier.Verify(response, mismatchedPlan); err == nil {
		t.Fatal("configured output client accepted a different authenticated presenter")
	}

	f.verifier.keyNotAfter = f.now.Add(-time.Second)
	if _, err := f.verifier.Verify(result.Response(), result.plan); err == nil {
		t.Fatal("retired output verification key accepted newly minted output")
	}
}

func TestADR_0302_OutputRejectsStaleIssuanceTimes(t *testing.T) {
	f := newOutputFixture(t)
	result, _ := f.exchangeOutput(t, f.request)
	response := result.Response()
	stale := f.now.Add(-f.verifier.maxAge - f.verifier.clockSkew - time.Second).Unix()
	response.token = mintedOutput(t, f.issuer, result.plan, map[string]any{"iat": stale, "nbf": stale})
	if _, err := f.verifier.Verify(response, result.plan); err == nil {
		t.Fatal("arbitrarily stale output iat/nbf verified")
	}
}

type classifiedOutputVerifier struct{ err error }

func (v classifiedOutputVerifier) Verify(ExchangeResponse, MechanismInput) (VerifiedOutput, error) {
	return VerifiedOutput{}, v.err
}

type fixedOutputVerifier struct {
	verified VerifiedOutput
	err      error
}

func (v fixedOutputVerifier) Verify(ExchangeResponse, MechanismInput) (VerifiedOutput, error) {
	return v.verified, v.err
}

func TestGateRejectsEmptyOrMismatchedVerifiedOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output func(MechanismInput) VerifiedOutput
	}{
		{name: "zero", output: func(MechanismInput) VerifiedOutput { return VerifiedOutput{} }},
		{name: "mismatched", output: func(plan MechanismInput) VerifiedOutput {
			output, err := NewVerifiedOutput(qualifiedOutputSubject(plan.Owner()), plan.ActorSubject(), plan.Presenter().Value(),
				"other-resource", plan.Detail().Value(), plan.Scopes(), plan.NotAfter())
			if err != nil {
				t.Fatal(err)
			}
			return output
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			plan := MechanismInput{facts: decisionFacts{owner: f.owner, presenter: f.presenter, actorSubject: f.actor.verified.Subject(),
				resource: f.request.Resource(), operation: f.request.Operation(), detail: f.request.Detail(), scopes: f.request.Scopes()}, notAfter: f.now.Add(5 * time.Minute)}
			f.gate.cfg.OutputVerifier = fixedOutputVerifier{verified: tc.output(plan)}
			result, err := f.exchange(t, f.request, "bad-verifier-output")
			if !IsFailure(err, FailureOutputVerification) || !result.VerifiedOutput().IsZero() {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestGateCanonicalizesOutputDeadlineToWholeSeconds(t *testing.T) {
	f := newOutputFixture(t)
	f.now = f.now.Add(987654321 * time.Nanosecond)
	f.gate.cfg.Now = func() time.Time { return f.now }
	f.issuer.now = func() time.Time { return f.now }
	f.verifier.now = func() time.Time { return f.now }
	f.subject.verified.notAfter = f.now.Add(90*time.Second + 123*time.Nanosecond)
	result, err := f.exchange(t, f.request, "nanosecond-clock")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := outputPlan(result).NotAfter(); got.Nanosecond() != 0 || !result.VerifiedOutput().ExpiresAt().Equal(got) {
		t.Fatalf("deadline=%v verified expiry=%v", got, result.VerifiedOutput().ExpiresAt())
	}

	f = newOutputFixture(t)
	f.now = f.now.Add(900 * time.Millisecond)
	f.gate.cfg.Now = func() time.Time { return f.now }
	f.subject.verified.notAfter = f.now.Add(50 * time.Millisecond)
	if _, err := f.exchange(t, f.request, "truncated-expiry"); !IsFailure(err, FailureOutputVerification) || f.issuer.calls != 0 {
		t.Fatalf("err=%v issuer calls=%d", err, f.issuer.calls)
	}
}

func TestGatePreservesOutputVerifierFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		kind      FailureKind
		retryable bool
	}{
		{"temporarily unavailable", ErrTemporarilyUnavailable, FailureOutputVerification, true},
		{"unsupported profile", ErrUnsupportedProfile, FailureUnsupportedProfile, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			f.gate.cfg.OutputVerifier = classifiedOutputVerifier{err: tc.err}
			_, err := f.exchange(t, f.request, "classification")
			if !IsFailure(err, tc.kind) || FailureRetryable(err) != tc.retryable {
				t.Fatalf("classification = %v retryable=%v", err, FailureRetryable(err))
			}
		})
	}
}
