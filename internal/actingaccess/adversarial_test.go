package actingaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestActingAccess_Scenario5_FailureTaxonomy(t *testing.T) {
	cases := []struct {
		name      string
		kind      FailureKind
		retryable bool
		mutate    func(*scenario3Fixture)
	}{
		{"invalid subject", FailureSubjectVerification, false, func(f *scenario3Fixture) { f.subject.verified = VerifiedSubject{} }},
		{"owner mismatch", FailureOwner, false, func(f *scenario3Fixture) {
			f.subject.verified.owner = mustOwner(t, "https://issuer.example", "mallory")
		}},
		{"invalid actor", FailureActorVerification, false, func(f *scenario3Fixture) { f.actor.verified = VerifiedActor{} }},
		{"presenter denial", FailureAssociation, false, func(f *scenario3Fixture) {
			f.spies.decisions[FailureAssociation] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"subject authority denial", FailureSubjectAuthority, false, func(f *scenario3Fixture) {
			f.spies.decisions[FailureSubjectAuthority] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"actor authority failure", FailureActorTools, false, func(f *scenario3Fixture) { f.actor.verified.tools = []string{"Grep"} }},
		{"target scope denial", FailureRegistry, false, func(f *scenario3Fixture) {
			f.spies.decisions[FailureRegistry] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"target policy denial", FailureTargetPolicy, false, func(f *scenario3Fixture) {
			f.spies.decisions[FailureTargetPolicy] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"consent denial", FailureConsent, false, func(f *scenario3Fixture) {
			f.spies.decisions[FailureConsent] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"unsupported profile", FailureUnsupportedProfile, false, func(f *scenario3Fixture) { f.mechanism.err = ErrUnsupportedProfile }},
		{"mechanism refusal", FailureMechanism, false, func(f *scenario3Fixture) { f.mechanism.err = errors.New("refused") }},
		{"temporary unavailability", FailureMechanism, true, func(f *scenario3Fixture) { f.mechanism.err = ErrTemporarilyUnavailable }},
		{"invalid output", FailureOutputVerification, false, func(f *scenario3Fixture) { f.mechanism.invalidOutput = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			tc.mutate(&f)
			result, err := f.exchange(t, f.request, "taxonomy")
			if !IsFailure(err, tc.kind) || FailureRetryable(err) != tc.retryable {
				t.Fatalf("Exchange() result=%#v err=%v, want kind=%s retryable=%v", result, err, tc.kind, tc.retryable)
			}
			if !outputTokenIsZero(result.Token()) {
				t.Fatal("refusal returned an output token")
			}
		})
	}
	for _, kind := range []FailureKind{FailureSubjectAuthority, FailureConsent, FailureAssociation, FailureRegistry, FailureTargetPolicy} {
		t.Run("unavailable/"+string(kind), func(t *testing.T) {
			f := newScenario3Fixture(t)
			f.spies.decisions[kind] = mustDecision(DecisionUnavailable, f.now.Add(time.Minute))
			result, err := f.exchange(t, f.request, "taxonomy-unavailable")
			if !IsFailure(err, kind) || !FailureRetryable(err) || !outputTokenIsZero(result.Token()) {
				t.Fatalf("result=%#v err=%v, want retryable %s", result, err, kind)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		kind   FailureKind
		mutate func(*scenario3Fixture)
	}{
		{"subject verifier", FailureSubjectVerification, func(f *scenario3Fixture) { f.subject.err = ErrTemporarilyUnavailable }},
		{"actor verifier", FailureActorVerification, func(f *scenario3Fixture) { f.actor.err = ErrTemporarilyUnavailable }},
	} {
		t.Run("unavailable/"+tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			tc.mutate(&f)
			result, err := f.exchange(t, f.request, "taxonomy-unavailable")
			if !IsFailure(err, tc.kind) || !FailureRetryable(err) || !outputTokenIsZero(result.Token()) {
				t.Fatalf("result=%#v err=%v, want retryable %s", result, err, tc.kind)
			}
		})
	}
}

func TestInvariant_acting_access_secret_sink_inventory(t *testing.T) {
	const subjectCanary = "SUBJECT-CANARY-9f64"
	const actorCanary = "ACTOR-CANARY-7a31"
	const outputCanary = "OUTPUT-CANARY-2c88"
	subject, _ := NewSubjectAssertion(subjectCanary)
	actor, _ := NewI2Token(actorCanary)
	output, _ := NewOutputToken(outputCanary)

	for name, value := range map[string]any{"subject": subject, "actor": actor, "output": output} {
		typ := reflect.TypeOf(value)
		for _, method := range []string{"String", "GoString", "MarshalJSON", "MarshalText", "MarshalBinary", "GobEncode", "Raw", "Value", "Bytes"} {
			if _, ok := typ.MethodByName(method); ok {
				t.Errorf("%s exposes secret method %s", name, method)
			}
			if _, ok := reflect.PointerTo(typ).MethodByName(method); ok {
				t.Errorf("*%s exposes secret method %s", name, method)
			}
		}
		assertNoCanary(t, name+" formatter", fmt.Sprintf("%v %#v %+v", value, value, value), subjectCanary, actorCanary, outputCanary)
		if raw, err := json.Marshal(value); err == nil {
			assertNoCanary(t, name+" JSON", string(raw), subjectCanary, actorCanary, outputCanary)
		}
	}

	f := newScenario3Fixture(t)
	f.subject.wantRaw = subjectCanary
	f.actor.wantRaw = actorCanary
	f.mechanism.wantSubject = subjectCanary
	f.mechanism.wantActor = actorCanary
	f.mechanism.outputRaw = outputCanary
	f.gate.cfg.OutputVerifier = &canaryOutputVerifier{wantRaw: outputCanary}
	result, err := f.gate.Exchange(context.Background(), f.request, subject, actor, subjectCanary+actorCanary)
	if err != nil {
		t.Fatal(err)
	}
	if f.subject.rawMatches != 1 || f.actor.rawMatches != 1 || f.mechanism.secretMatches != 2 {
		t.Fatalf("secrets did not reach only credential seams: subject=%d actor=%d mechanism=%d", f.subject.rawMatches, f.actor.rawMatches, f.mechanism.secretMatches)
	}
	for name, value := range map[string]any{
		"result": result, "response": result.Response(), "trace": result.Trace(), "plan": result.plan,
		"error": fmt.Errorf("wrapped: %w", refuse(FailureConsent, false)),
	} {
		text := fmt.Sprintf("%v %#v %+v", value, value, value)
		assertNoCanary(t, name+" formatter", text, subjectCanary, actorCanary, outputCanary)
		raw, marshalErr := json.Marshal(value)
		if marshalErr == nil {
			assertNoCanary(t, name+" JSON", string(raw), subjectCanary, actorCanary, outputCanary)
		}
	}

	failures := []struct {
		name   string
		mutate func(*scenario3Fixture)
	}{
		{"subject", func(f *scenario3Fixture) { f.subject.err = errors.New(subjectCanary) }},
		{"owner", func(f *scenario3Fixture) { f.subject.verified.owner = mustOwner(t, "https://issuer.example", "other") }},
		{"actor", func(f *scenario3Fixture) { f.actor.err = errors.New(actorCanary) }},
		{"subject authority", func(f *scenario3Fixture) {
			f.spies.decisions[FailureSubjectAuthority] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"consent", func(f *scenario3Fixture) {
			f.spies.decisions[FailureConsent] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"association", func(f *scenario3Fixture) {
			f.spies.decisions[FailureAssociation] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"actor tools", func(f *scenario3Fixture) { f.actor.verified.tools = []string{"Write"} }},
		{"registry", func(f *scenario3Fixture) {
			f.spies.decisions[FailureRegistry] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"target", func(f *scenario3Fixture) {
			f.spies.decisions[FailureTargetPolicy] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"unsupported", func(f *scenario3Fixture) { f.mechanism.err = fmt.Errorf("%s: %w", outputCanary, ErrUnsupportedProfile) }},
		{"unavailable", func(f *scenario3Fixture) {
			f.mechanism.err = fmt.Errorf("%s: %w", outputCanary, ErrTemporarilyUnavailable)
		}},
		{"invalid output", func(f *scenario3Fixture) { f.mechanism.invalidOutput = true; f.mechanism.outputRaw = outputCanary }},
	}
	for _, tc := range failures {
		failed := newScenario3Fixture(t)
		tc.mutate(&failed)
		_, failure := failed.gate.Exchange(context.Background(), failed.request, subject, actor, subjectCanary+actorCanary)
		if failure == nil {
			t.Errorf("%s unexpectedly succeeded", tc.name)
			continue
		}
		assertNoCanary(t, tc.name+" failure", fmt.Sprintf("%v %#v", failure, failure), subjectCanary, actorCanary, outputCanary)
	}
}

func TestADR_0253_NoFallbackAuthorityAndStaleFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   FailureKind
		mutate func(*scenario3Fixture)
	}{
		{"expired subject", FailureSubjectVerification, func(f *scenario3Fixture) { f.subject.verified.notAfter = f.now }},
		{"expired actor", FailureActorVerification, func(f *scenario3Fixture) { f.actor.verified.notAfter = f.now }},
		{"expired consent", FailureConsent, func(f *scenario3Fixture) { f.spies.decisions[FailureConsent] = mustDecision(DecisionPermit, f.now) }},
		{"expired association", FailureAssociation, func(f *scenario3Fixture) { f.spies.decisions[FailureAssociation] = mustDecision(DecisionPermit, f.now) }},
		{"expired policy", FailureTargetPolicy, func(f *scenario3Fixture) {
			f.spies.decisions[FailureTargetPolicy] = mustDecision(DecisionPermit, f.now)
		}},
		{"denied presenter", FailureAssociation, func(f *scenario3Fixture) {
			f.spies.decisions[FailureAssociation] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"unavailable target", FailureTargetPolicy, func(f *scenario3Fixture) {
			f.spies.decisions[FailureTargetPolicy] = mustDecision(DecisionUnavailable, f.now.Add(time.Minute))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			tc.mutate(&f)
			result, err := f.exchange(t, f.request, "no-fallback")
			if !IsFailure(err, tc.kind) || f.mechanism.calls != 0 || !outputTokenIsZero(result.Token()) {
				t.Fatalf("err=%v calls=%d result=%#v", err, f.mechanism.calls, result)
			}
		})
	}

	mechanismType := reflect.TypeOf((*ExchangeMechanism)(nil)).Elem()
	configType := reflect.TypeOf(GateConfig{})
	mechanismFields := 0
	for i := 0; i < configType.NumField(); i++ {
		if configType.Field(i).Type.Implements(mechanismType) {
			mechanismFields++
		}
	}
	if mechanismFields != 1 {
		t.Fatalf("GateConfig has %d exchange mechanisms, want exactly one with no fallback", mechanismFields)
	}
	inputType := reflect.TypeOf(MechanismInput{})
	for _, name := range []string{"ServiceIdentity", "OwnerlessIdentity", "AmbientAuthority", "ProviderCredential", "BroaderActor"} {
		if _, ok := inputType.MethodByName(name); ok {
			t.Errorf("MechanismInput exposes fallback authority %s", name)
		}
	}
	// Unknown subject keys are refused by the real closed JWT verifier at the entrypoint.
	f := newScenario3Fixture(t)
	trustedSubjectKey := newP256Key(t)
	subjectVerifier, err := NewJWTSubjectAssertionVerifier(SubjectVerifierConfig{
		Issuer: "https://issuer.example", Audience: "mecatl-exchange", AuthorizedParty: "broker-prod",
		MaxAge: time.Minute, MaxTokenBytes: 4096, Now: func() time.Time { return f.now },
		Keys: []SubjectKey{{ID: "current-subject", PublicKey: &trustedSubjectKey.PublicKey}},
	})
	if err != nil {
		t.Fatal(err)
	}
	untrustedSubjectKey := newP256Key(t)
	claims := map[string]any{
		"iss": "https://issuer.example", "sub": "alice", "aud": []string{"mecatl-exchange"},
		"iat": f.now.Unix(), "nbf": f.now.Unix(), "exp": f.now.Add(time.Minute).Unix(), "azp": "broker-prod", "consent": "consent-alice-read",
	}
	unknownSubject, _ := NewSubjectAssertion(subjectJWT(t, untrustedSubjectKey, "retired-subject", jwt.SigningMethodES256, ExchangeSubjectTokenType, claims))
	f.gate.cfg.SubjectVerifier = subjectVerifier
	actorToken, _ := NewI2Token("actor")
	if result, err := f.gate.Exchange(context.Background(), f.request, unknownSubject, actorToken, "unknown-subject-key"); !IsFailure(err, FailureSubjectVerification) || !outputTokenIsZero(result.Token()) {
		t.Fatalf("unknown subject key result=%#v err=%v", result, err)
	}

	// A token from a removed I2 key is refused by the real ADR-0252 verifier.
	trustedDER, _ := newPKCS8(t)
	retiredDER, _ := newPKCS8(t)
	trustedIssuer := loadActorIssuer(t, actorManifest(), map[string][]byte{"old": trustedDER})
	retiredIssuer := loadActorIssuer(t, actorManifest(), map[string][]byte{"old": retiredDER})
	retiredActor := issueActor(t, retiredIssuer, "mecatl-exchange", []string{"Read"})
	bundle, err := trustedIssuer.Bundle(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	actorClock := time.Now().UTC()
	f = newScenario3Fixture(t)
	f.gate.cfg.ActorVerifier = newActorVerifier(t, bundle, &actorClock, time.Minute)
	subjectToken, _ := NewSubjectAssertion("subject")
	if result, err := f.gate.Exchange(context.Background(), f.request, subjectToken, mustI2(t, retiredActor), "retired-actor-key"); !IsFailure(err, FailureActorVerification) || !outputTokenIsZero(result.Token()) {
		t.Fatalf("retired actor key result=%#v err=%v", result, err)
	}

	// Unknown output verification keys are refused by the real independent verifier.
	out := newOutputFixture(t)
	other := outputTestKey(t)
	wrongVerifier, err := NewJWTOutputVerifier(OutputVerifierConfig{Issuer: "https://as.example", ClientID: "broker-prod", KeyID: "retired-output", PublicKey: &other.PublicKey, Now: func() time.Time { return out.now }})
	if err != nil {
		t.Fatal(err)
	}
	out.gate.cfg.OutputVerifier = wrongVerifier
	if result, err := out.exchange(t, out.request, "unknown-output-key"); !IsFailure(err, FailureOutputVerification) || !outputTokenIsZero(result.Token()) {
		t.Fatalf("unknown output key result=%#v err=%v", result, err)
	}
}

func TestInvariant_acting_access_predicate_mutation_resistance(t *testing.T) {
	mutations := []struct {
		name   string
		kind   FailureKind
		mutate func(*scenario3Fixture)
	}{
		{"owner", FailureOwner, func(f *scenario3Fixture) { f.subject.verified.owner = mustOwner(t, "https://issuer.example", "other") }},
		{"subject authority", FailureSubjectAuthority, func(f *scenario3Fixture) {
			f.spies.decisions[FailureSubjectAuthority] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"consent", FailureConsent, func(f *scenario3Fixture) {
			f.spies.decisions[FailureConsent] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"association", FailureAssociation, func(f *scenario3Fixture) {
			f.spies.decisions[FailureAssociation] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"actor tools", FailureActorTools, func(f *scenario3Fixture) { f.actor.verified.tools = []string{"Write"} }},
		{"registry", FailureRegistry, func(f *scenario3Fixture) {
			f.spies.decisions[FailureRegistry] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
		{"target policy", FailureTargetPolicy, func(f *scenario3Fixture) {
			f.spies.decisions[FailureTargetPolicy] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			mutation.mutate(&f)
			_, err := f.exchange(t, f.request, "mutation")
			if !IsFailure(err, mutation.kind) || f.mechanism.calls != 0 {
				t.Fatalf("production entrypoint survived predicate mutation: err=%v mechanism=%d source-calls=%v", err, f.mechanism.calls, f.spies.calls)
			}
		})
	}
}

func TestActingAccess_Scenario6_RefusalPrecedesIssuance(t *testing.T) {
	for _, kind := range []FailureKind{FailureSubjectAuthority, FailureConsent, FailureAssociation, FailureActorTools, FailureRegistry, FailureTargetPolicy} {
		f := newOutputFixture(t)
		if kind == FailureActorTools {
			f.actor.verified.tools = []string{"Write"}
		} else {
			f.spies.decisions[kind] = mustDecision(DecisionDeny, f.now.Add(time.Minute))
		}
		if _, err := f.exchange(t, f.request, "pre-issue"); !IsFailure(err, kind) || f.issuer.calls != 0 {
			t.Errorf("%s: err=%v issuer calls=%d", kind, err, f.issuer.calls)
		}
	}

	f := newOutputFixture(t)
	spy := &compactOutputVerifier{delegate: f.verifier}
	f.gate.cfg.OutputVerifier = spy
	if _, err := f.exchange(t, f.request, "compact-proof"); err != nil {
		t.Fatal(err)
	}
	if spy.calls != 1 || spy.compactParts != 3 || spy.publicKey == nil {
		t.Fatalf("independent verifier did not receive compact bytes/public material: %#v", spy)
	}
}

func TestActingAccess_Scenario6_NoPersistentCacheAndReplayBound(t *testing.T) {
	f := newOutputFixture(t)
	originalBound := f.now.Add(45 * time.Second)
	f.subject.verified.notAfter = originalBound
	first, err := f.exchange(t, f.request, "replay")
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(30 * time.Second)
	f.gate.cfg.Now = func() time.Time { return f.now }
	f.issuer.now = func() time.Time { return f.now }
	f.verifier.now = func() time.Time { return f.now }
	second, err := f.exchange(t, f.request, "replay")
	if err != nil {
		t.Fatal(err)
	}
	if f.issuer.calls != 2 || second.Response().ExpiresIn() > 15 || !outputPlan(second).NotAfter().Equal(originalBound) || !outputPlan(first).NotAfter().Equal(originalBound) {
		t.Fatalf("replay widened/cached output: calls=%d first=%v second=%v expires=%d", f.issuer.calls, outputPlan(first).NotAfter(), outputPlan(second).NotAfter(), second.Response().ExpiresIn())
	}
	f.now = originalBound
	if result, err := f.exchange(t, f.request, "expired-replay"); !IsFailure(err, FailureSubjectVerification) || !outputTokenIsZero(result.Token()) || f.issuer.calls != 2 {
		t.Fatalf("expired replay minted output: result=%#v err=%v calls=%d", result, err, f.issuer.calls)
	}
	f.subject.verified.notAfter = f.now.Add(time.Minute)
	switcher := &switchableMechanism{delegate: f.issuer, err: ErrTemporarilyUnavailable}
	f.gate.cfg.Mechanism = switcher
	if _, err := f.exchange(t, f.request, "outage"); !IsFailure(err, FailureMechanism) || !FailureRetryable(err) {
		t.Fatalf("outage err=%v", err)
	}
	switcher.err = nil
	if _, err := f.exchange(t, f.request, "recovered"); err != nil {
		t.Fatalf("outage contaminated later request: %v", err)
	}

	for _, typ := range []reflect.Type{reflect.TypeOf(Gate{}), reflect.TypeOf(ExchangeResult{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "cache") || strings.Contains(name, "ledger") || strings.Contains(name, "replay") {
				t.Errorf("%s contains persistent replay state %s", typ, typ.Field(i).Name)
			}
		}
	}
}

func TestActingAccess_Scenario6_LocalOnlyOutageIsolation(t *testing.T) {
	f := newScenario3Fixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.subject.block = func() { close(entered); <-release }
	f.subject.err = ErrTemporarilyUnavailable

	var wg sync.WaitGroup
	wg.Add(1)
	var externalResult ExchangeResult
	var externalErr error
	go func() {
		defer wg.Done()
		externalResult, externalErr = f.exchange(t, f.request, "blocked-external")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("external exchange did not block in subject verifier")
	}

	localDone := make(chan string, 1)
	go func() { localDone <- "local-complete" }()
	select {
	case got := <-localDone:
		if got != "local-complete" || f.mechanism.calls != 0 || f.subject.calls != 1 {
			t.Fatalf("local work crossed acting-access boundary: got=%q subject=%d mechanism=%d", got, f.subject.calls, f.mechanism.calls)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("local-only work was coupled to external exchange outage")
	}
	close(release)
	wg.Wait()
	if !IsFailure(externalErr, FailureSubjectVerification) || !FailureRetryable(externalErr) || !outputTokenIsZero(externalResult.Token()) || f.mechanism.calls != 0 {
		t.Fatalf("external result=%#v err=%v mechanism=%d", externalResult, externalErr, f.mechanism.calls)
	}
}

func outputTokenIsZero(token OutputToken) bool { return token.secret.value == nil }

func assertNoCanary(t *testing.T, surface, value string, canaries ...string) {
	t.Helper()
	for _, canary := range canaries {
		if strings.Contains(value, canary) {
			t.Errorf("%s leaked canary %q in %q", surface, canary, value)
		}
	}
}

type switchableMechanism struct {
	delegate ExchangeMechanism
	err      error
}

func (m *switchableMechanism) Exchange(ctx context.Context, in MechanismInput, subject SubjectAssertion, actor I2Token) (ExchangeResponse, error) {
	if m.err != nil {
		return ExchangeResponse{}, m.err
	}
	return m.delegate.Exchange(ctx, in, subject, actor)
}

type canaryOutputVerifier struct {
	wantRaw string
	calls   int
}

func (v *canaryOutputVerifier) Verify(response ExchangeResponse, _ MechanismInput) (VerifiedOutput, error) {
	v.calls++
	if response.token.secret.value == nil || response.token.secret.value.raw != v.wantRaw {
		return VerifiedOutput{}, errors.New("invalid compact output")
	}
	return VerifiedOutput{}, nil
}

type compactOutputVerifier struct {
	delegate     *JWTOutputVerifier
	calls        int
	compactParts int
	publicKey    any
}

func (v *compactOutputVerifier) Verify(response ExchangeResponse, plan MechanismInput) (VerifiedOutput, error) {
	v.calls++
	if response.token.secret.value != nil {
		v.compactParts = len(strings.Split(response.token.secret.value.raw, "."))
	}
	v.publicKey = v.delegate.publicKey
	return v.delegate.Verify(response, plan)
}
