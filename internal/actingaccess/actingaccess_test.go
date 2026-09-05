package actingaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identityissuer"
)

func TestActingAccess_Scenario1_RejectsNonCanonicalRequest(t *testing.T) {
	owner, err := NewOwner("https://issuer.example", "alice")
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := NewPresenter("broker-prod")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry([]Registration{{
		Resource:      "vmcp-repository",
		Operation:     "read",
		Detail:        "repository.read",
		Scopes:        []string{"repo:read", "repo:status"},
		RequiredTools: []string{"Read"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := registry.NewRequest(owner, presenter, "vmcp-repository", "read", "repository.read", []string{"repo:read", "repo:status"})
	if err != nil {
		t.Fatalf("canonical request: %v", err)
	}
	if got := valid.Scopes(); !reflect.DeepEqual(got, []string{"repo:read", "repo:status"}) {
		t.Fatalf("Scopes() = %#v", got)
	}
	got := valid.Scopes()
	got[0] = "repo:write"
	if valid.Scopes()[0] != "repo:read" {
		t.Fatal("request scopes alias caller mutation")
	}

	badOwners := [][2]string{
		{"", "alice"}, {" https://issuer.example", "alice"}, {"https://issuer.example/../tenant", "alice"},
		{"https://issuer.example", ""}, {"https://issuer.example", " alice"}, {"https://issuer.example", "ali\x00ce"},
	}
	for _, parts := range badOwners {
		if _, err := NewOwner(parts[0], parts[1]); err == nil {
			t.Errorf("NewOwner(%q, %q) succeeded", parts[0], parts[1])
		}
	}
	for _, raw := range []string{"", " broker", "broker\nprod", "BROKER"} {
		if _, err := NewPresenter(raw); err == nil {
			t.Errorf("NewPresenter(%q) succeeded", raw)
		}
	}
	for _, tc := range []struct {
		name      string
		resource  string
		operation string
		scopes    []string
	}{
		{"empty resource", "", "read", []string{"repo:read"}},
		{"unknown resource", "other", "read", []string{"repo:read"}},
		{"unknown operation", "vmcp-repository", "write", []string{"repo:read"}},
		{"empty scopes", "vmcp-repository", "read", nil},
		{"duplicate scope", "vmcp-repository", "read", []string{"repo:read", "repo:read"}},
		{"unsorted scopes", "vmcp-repository", "read", []string{"repo:status", "repo:read"}},
		{"unknown scope", "vmcp-repository", "read", []string{"repo:write"}},
		{"control scope", "vmcp-repository", "read", []string{"repo:\nread"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := registry.NewRequest(owner, presenter, tc.resource, tc.operation, "repository.read", tc.scopes); err == nil {
				t.Fatal("NewRequest succeeded")
			}
		})
	}
	oversized := make([]byte, maxValueBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	if _, err := NewPresenter(string(oversized)); err == nil {
		t.Fatal("oversized presenter accepted")
	}
}

func TestInvariant_acting_access_closed_inputs(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(Request{}), reflect.TypeOf(Owner{}), reflect.TypeOf(Presenter{}),
		reflect.TypeOf(RegisteredResource{}), reflect.TypeOf(RegisteredOperation{}), reflect.TypeOf(RegisteredDetail{}),
		reflect.TypeOf(Scope{}), reflect.TypeOf(VerifiedSubject{}), reflect.TypeOf(VerifiedActor{}), reflect.TypeOf(Decision{}),
		reflect.TypeOf(RegistrationDecision{}),
		reflect.TypeOf(SubjectAuthorityInput{}), reflect.TypeOf(ConsentInput{}), reflect.TypeOf(AssociationInput{}),
		reflect.TypeOf(RegistryInput{}), reflect.TypeOf(TargetPolicyInput{}), reflect.TypeOf(MechanismInput{}),
		reflect.TypeOf(PermitTrace{}), reflect.TypeOf(ExchangeResult{}),
	}
	for _, typ := range types {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.IsExported() {
				t.Errorf("%s exposes field %s", typ, field.Name)
			}
			if field.Type.Kind() == reflect.Map {
				t.Errorf("%s exposes generic map field %s", typ, field.Name)
			}
		}
	}
	forbidden := []string{"URL", "Audience", "Authorization", "Credential", "SessionStore", "EventLog", "ToolHive"}
	for _, typ := range types {
		for i := 0; i < typ.NumMethod(); i++ {
			for _, word := range forbidden {
				if typ.Method(i).Name == word {
					t.Errorf("%s exposes forbidden method %s", typ, word)
				}
			}
		}
	}
}

func TestActingAccess_Scenario1_SeparatesCredentialProfiles(t *testing.T) {
	const canary = "secret-canary"
	subject, err := NewSubjectAssertion(canary)
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewI2Token(canary)
	if err != nil {
		t.Fatal(err)
	}
	output, err := NewOutputToken(canary)
	if err != nil {
		t.Fatal(err)
	}
	types := []reflect.Type{reflect.TypeOf(subject), reflect.TypeOf(actor), reflect.TypeOf(output)}
	for i := range types {
		for j := i + 1; j < len(types); j++ {
			if types[i].AssignableTo(types[j]) || types[j].AssignableTo(types[i]) {
				t.Fatalf("credential profiles are assignable: %s and %s", types[i], types[j])
			}
		}
	}
	for name, value := range map[string]any{"subject": subject, "actor": actor, "output": output} {
		if got := fmt.Sprintf("%v %#v", value, value); contains(got, canary) {
			t.Errorf("%s formatted secret: %q", name, got)
		}
		if raw, err := json.Marshal(value); err == nil && (string(raw) != "{}" || contains(string(raw), canary)) {
			t.Errorf("%s serialized credential as %s", name, raw)
		}
		typ := reflect.TypeOf(value)
		for _, method := range []string{"String", "GoString", "MarshalJSON", "MarshalText", "GobEncode"} {
			if _, ok := typ.MethodByName(method); ok {
				t.Errorf("%s exposes %s", name, method)
			}
		}
	}
}

type credentialConsumer struct{ values []string }

func (c *credentialConsumer) ConsumeSubjectAssertion(value []byte) error {
	c.values = append(c.values, string(value))
	return nil
}
func (c *credentialConsumer) ConsumeI2Token(value []byte) error {
	c.values = append(c.values, string(value))
	return nil
}
func (c *credentialConsumer) ConsumeOutputToken(value []byte) error {
	c.values = append(c.values, string(value))
	return nil
}

func TestActingAccess_TrustedCollaboratorBoundaries(t *testing.T) {
	subject, _ := NewSubjectAssertion("subject-secret")
	actor, _ := NewI2Token("actor-secret")
	output, _ := NewOutputToken("output-secret")
	consumer := &credentialConsumer{}
	if err := subject.Consume(consumer); err != nil {
		t.Fatal(err)
	}
	if err := actor.Consume(consumer); err != nil {
		t.Fatal(err)
	}
	if err := output.Consume(consumer); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(consumer.values, []string{"subject-secret", "actor-secret", "output-secret"}) {
		t.Fatalf("consumed values = %#v", consumer.values)
	}
	response, err := NewExchangeResponse(output, 30, []string{"repo:read"})
	if err != nil || response.Token().secret.value == nil || response.ExpiresIn() != 30 || response.Scope() != "repo:read" {
		t.Fatalf("NewExchangeResponse() = %#v, %v", response, err)
	}
}

func TestDecisionValidityBelongsOnlyToPermit(t *testing.T) {
	future := time.Now().Add(time.Minute)
	if _, err := NewDecision(DecisionPermit, future); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDecision(DecisionPermit, time.Time{}); err == nil {
		t.Fatal("permit without validity accepted")
	}
	for _, effect := range []DecisionEffect{DecisionDeny, DecisionUnavailable} {
		decision, err := NewDecision(effect, time.Time{})
		if err != nil || !decision.NotAfter().IsZero() {
			t.Fatalf("NewDecision(%s) = %#v, %v", effect, decision, err)
		}
		if _, err := NewDecision(effect, future); err == nil {
			t.Fatalf("%s accepted a fake validity deadline", effect)
		}
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

type decisionSpy struct {
	decision Decision
	calls    int
	expected decisionExpectation
}

type subjectAuthoritySpy struct{ decisionSpy }
type consentAuthoritySpy struct{ decisionSpy }
type associationAuthoritySpy struct{ decisionSpy }
type registryAuthoritySpy struct {
	decisionSpy
	requiredTools []string
}
type targetAuthoritySpy struct{ decisionSpy }

type authoritySpies struct {
	subject     *subjectAuthoritySpy
	consent     *consentAuthoritySpy
	association *associationAuthoritySpy
	registry    *registryAuthoritySpy
	target      *targetAuthoritySpy
}

type decisionExpectation struct {
	consent, presenter, resource, operation, detail string
	scopes                                          []string
}

func mustDecision(effect DecisionEffect, notAfter time.Time) Decision {
	if effect != DecisionPermit {
		notAfter = time.Time{}
	}
	decision, err := NewDecision(effect, notAfter)
	if err != nil {
		panic(err)
	}
	return decision
}

func newAuthoritySpies(exp decisionExpectation, until time.Time) *authoritySpies {
	permit := mustDecision(DecisionPermit, until)
	return &authoritySpies{
		subject:     &subjectAuthoritySpy{decisionSpy{decision: permit, expected: exp}},
		consent:     &consentAuthoritySpy{decisionSpy{decision: permit, expected: exp}},
		association: &associationAuthoritySpy{decisionSpy{decision: permit, expected: exp}},
		registry:    &registryAuthoritySpy{decisionSpy: decisionSpy{decision: permit, expected: exp}, requiredTools: []string{"Read"}},
		target:      &targetAuthoritySpy{decisionSpy{decision: permit, expected: exp}},
	}
}

func (s *decisionSpy) decide(in decisionInputView) Decision {
	s.calls++
	if in.ConsentProof() != s.expected.consent || in.Presenter().Value() != s.expected.presenter ||
		in.Resource().Value() != s.expected.resource || in.Operation().Value() != s.expected.operation ||
		in.Detail().Value() != s.expected.detail || !reflect.DeepEqual(in.Scopes(), s.expected.scopes) {
		return mustDecision(DecisionDeny, s.decision.NotAfter())
	}
	return s.decision
}

func (s *subjectAuthoritySpy) DecideSubject(_ context.Context, in SubjectAuthorityInput) Decision {
	return s.decide(in)
}
func (s *consentAuthoritySpy) DecideConsent(_ context.Context, in ConsentInput) Decision {
	return s.decide(in)
}
func (s *associationAuthoritySpy) DecideAssociation(_ context.Context, in AssociationInput) Decision {
	return s.decide(in)
}
func (s *registryAuthoritySpy) DecideRegistration(_ context.Context, in RegistryInput) RegistrationDecision {
	decision := s.decide(in)
	tools := s.requiredTools
	if decision.Effect() != DecisionPermit {
		tools = nil
	}
	result, _ := NewRegistrationDecision(decision, tools)
	return result
}
func (s *targetAuthoritySpy) DecideTarget(_ context.Context, in TargetPolicyInput) Decision {
	return s.decide(in)
}

func (s *authoritySpies) source(kind FailureKind) *decisionSpy {
	switch kind {
	case FailureSubjectAuthority:
		return &s.subject.decisionSpy
	case FailureConsent:
		return &s.consent.decisionSpy
	case FailureAssociation:
		return &s.association.decisionSpy
	case FailureRegistry:
		return &s.registry.decisionSpy
	case FailureTargetPolicy:
		return &s.target.decisionSpy
	default:
		panic("unknown decision source")
	}
}

func (s *authoritySpies) setExpected(exp decisionExpectation) {
	for _, source := range []*decisionSpy{&s.subject.decisionSpy, &s.consent.decisionSpy, &s.association.decisionSpy, &s.registry.decisionSpy, &s.target.decisionSpy} {
		source.expected = exp
	}
}

func (s *authoritySpies) callCounts() map[FailureKind]int {
	return map[FailureKind]int{
		FailureSubjectAuthority: s.subject.calls,
		FailureConsent:          s.consent.calls,
		FailureAssociation:      s.association.calls,
		FailureRegistry:         s.registry.calls,
		FailureTargetPolicy:     s.target.calls,
	}
}

type fixedSubjectVerifier struct {
	verified   VerifiedSubject
	err        error
	calls      int
	wantRaw    string
	rawMatches int
}

func (v *fixedSubjectVerifier) Verify(token SubjectAssertion, _ Presenter) (VerifiedSubject, error) {
	v.calls++
	if token.secret.value != nil && token.secret.value.raw == v.wantRaw {
		v.rawMatches++
	}
	return v.verified, v.err
}

type fixedActorVerifier struct {
	verified   VerifiedActor
	err        error
	calls      int
	wantRaw    string
	rawMatches int
}

func (v *fixedActorVerifier) Verify(token I2Token) (VerifiedActor, error) {
	v.calls++
	if token.secret.value != nil && token.secret.value.raw == v.wantRaw {
		v.rawMatches++
	}
	return v.verified, v.err
}

type recordingMechanism struct {
	calls         int
	inputs        []MechanismInput
	err           error
	invalidOutput bool
	wantSubject   string
	wantActor     string
	outputRaw     string
	secretMatches int
}

type recordingOutputVerifier struct{}

func (recordingOutputVerifier) Verify(response ExchangeResponse, plan MechanismInput) (VerifiedOutput, error) {
	if !validExchangeResponse(response, plan, time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)) {
		return VerifiedOutput{}, fmt.Errorf("invalid output")
	}
	return NewVerifiedOutput(qualifiedOutputSubject(plan.Owner()), plan.ActorSubject(), plan.Presenter().Value(),
		plan.Resource().Value(), plan.Detail().Value(), plan.Scopes(), plan.NotAfter())
}

func (m *recordingMechanism) Exchange(_ context.Context, in MechanismInput, subject SubjectAssertion, actor I2Token) (ExchangeResponse, error) {
	m.calls++
	m.inputs = append(m.inputs, in)
	if subject.secret.value != nil && subject.secret.value.raw == m.wantSubject {
		m.secretMatches++
	}
	if actor.secret.value != nil && actor.secret.value.raw == m.wantActor {
		m.secretMatches++
	}
	if m.err != nil {
		return ExchangeResponse{}, m.err
	}
	raw := m.outputRaw
	if raw == "" {
		raw = "issued"
	}
	token, err := NewOutputToken(raw)
	if err != nil {
		return ExchangeResponse{}, err
	}
	response := ExchangeResponse{token: token, issuedTokenType: accessTokenType, tokenType: bearerTokenType, expiresIn: 1, scope: strings.Join(in.Scopes(), " ")}
	if m.invalidOutput {
		response.scope = "broader"
	}
	return response, nil
}

type scenario3Fixture struct {
	gate      *Gate
	registry  *Registry
	request   Request
	owner     Owner
	presenter Presenter
	subject   *fixedSubjectVerifier
	actor     *fixedActorVerifier
	spies     *authoritySpies
	mechanism *recordingMechanism
	now       time.Time
}

func newScenario3Fixture(t *testing.T) scenario3Fixture {
	t.Helper()
	now := time.Date(2035, 1, 2, 3, 4, 5, 0, time.UTC)
	owner, err := NewOwner("https://issuer.example", "alice")
	if err != nil {
		t.Fatal(err)
	}
	presenter, err := NewPresenter("broker-prod")
	if err != nil {
		t.Fatal(err)
	}
	registrations := []Registration{
		{Resource: "vmcp-repository", Operation: "read", Detail: "repository.read", Scopes: []string{"repo:read"}, RequiredTools: []string{"Read"}},
		{Resource: "vmcp-repository", Operation: "status", Detail: "repository.status", Scopes: []string{"repo:status"}, RequiredTools: []string{"Read"}},
		{Resource: "vmcp-deploy", Operation: "read", Detail: "deployment.read", Scopes: []string{"deploy:read"}, RequiredTools: []string{"Read"}},
	}
	registry, err := NewRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	request, err := registry.NewRequest(owner, presenter, "vmcp-repository", "read", "repository.read", []string{"repo:read"})
	if err != nil {
		t.Fatal(err)
	}
	until := now.Add(10 * time.Minute)
	subject := &fixedSubjectVerifier{verified: VerifiedSubject{owner: owner, consentProof: "consent-alice-read", notAfter: until}}
	actorIdentity, err := identityissuer.NewLogicalAgentIdentity("agents.example", identityissuer.DefinitionTierProject, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	verifiedActor, err := NewVerifiedActor("agents.example", actorIdentity.Subject, identityissuer.DefinitionTierProject, "reviewer", "", []string{"Read"}, "actor-1", until)
	if err != nil {
		t.Fatal(err)
	}
	actor := &fixedActorVerifier{verified: verifiedActor}
	expected := decisionExpectation{consent: "consent-alice-read", presenter: "broker-prod", resource: "vmcp-repository", operation: "read", detail: "repository.read", scopes: []string{"repo:read"}}
	spies := newAuthoritySpies(expected, until)
	mechanism := &recordingMechanism{}
	gate, err := NewGate(GateConfig{SubjectVerifier: subject, ActorVerifier: actor, SubjectAuthority: spies.subject, Consent: spies.consent,
		Association: spies.association, Registry: spies.registry, TargetPolicy: spies.target, Mechanism: mechanism, OutputVerifier: recordingOutputVerifier{}, MaximumLifetime: 5 * time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return scenario3Fixture{gate: gate, registry: registry, request: request, owner: owner, presenter: presenter,
		subject: subject, actor: actor, spies: spies, mechanism: mechanism, now: now}
}

func (f scenario3Fixture) exchange(t *testing.T, request Request, correlation string) (ExchangeResult, error) {
	t.Helper()
	subject, err := NewSubjectAssertion("subject-secret")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := NewI2Token("actor-secret")
	if err != nil {
		t.Fatal(err)
	}
	return f.gate.Exchange(context.Background(), request, subject, actor, correlation)
}

func TestActingAccess_Scenario3_AllCeilingsPermit(t *testing.T) {
	f := newScenario3Fixture(t)
	f.spies.source(FailureAssociation).decision = mustDecision(DecisionPermit, f.now.Add(2*time.Minute))
	result, err := f.exchange(t, f.request, "request-123")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	wantGates := []GateKind{GateOwner, GateSubjectAuthority, GateConsent, GateAssociation, GateRegistry, GateActorTools, GateTargetPolicy}
	if got := result.Trace().Gates(); !reflect.DeepEqual(got, wantGates) {
		t.Fatalf("trace gates = %#v, want %#v", got, wantGates)
	}
	if f.mechanism.calls != 1 {
		t.Fatalf("mechanism calls = %d, want 1", f.mechanism.calls)
	}
	input := f.mechanism.inputs[0]
	if input.Owner() != f.owner || input.Presenter() != f.presenter || input.ActorSubject() != f.actor.verified.Subject() ||
		!reflect.DeepEqual(input.ActorTools(), []string{"Read"}) || input.Resource().Value() != "vmcp-repository" ||
		input.Operation().Value() != "read" || input.Detail().Value() != "repository.read" ||
		!reflect.DeepEqual(input.Scopes(), []string{"repo:read"}) || !input.NotAfter().Equal(f.now.Add(2*time.Minute)) {
		t.Fatalf("mechanism input did not preserve exact validated plan: %#v", input)
	}
}

func TestADR_0302_IndependentCeilingRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind FailureKind
	}{
		{"subject authority", FailureSubjectAuthority}, {"consent", FailureConsent}, {"association", FailureAssociation},
		{"actor tools", FailureActorTools}, {"registry", FailureRegistry}, {"target policy", FailureTargetPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			if tc.kind == FailureActorTools {
				f.actor.verified.tools = []string{"Grep"}
			} else {
				f.spies.source(tc.kind).decision = mustDecision(DecisionDeny, f.now.Add(time.Minute))
			}
			_, err := f.exchange(t, f.request, "deny-one")
			if !IsFailure(err, tc.kind) || f.mechanism.calls != 0 {
				t.Fatalf("err = %v, mechanism calls = %d", err, f.mechanism.calls)
			}
			if tc.kind == FailureActorTools {
				f.actor.verified.tools = []string{"Read"}
			} else {
				f.spies.source(tc.kind).decision = mustDecision(DecisionPermit, f.now.Add(time.Minute))
			}
			if _, err := f.exchange(t, f.request, "restored"); err != nil {
				t.Fatalf("restored Exchange: %v", err)
			}
		})
	}

	for _, tc := range []struct {
		name      string
		mutate    func(*scenario3Fixture)
		requestFn func(scenario3Fixture) Request
	}{
		{"consent", func(f *scenario3Fixture) { f.subject.verified.consentProof = "other-consent" }, nil},
		{"presenter", nil, func(f scenario3Fixture) Request {
			p, _ := NewPresenter("broker-other")
			r, _ := f.registry.NewRequest(f.owner, p, "vmcp-repository", "read", "repository.read", []string{"repo:read"})
			return r
		}},
		{"resource", nil, func(f scenario3Fixture) Request {
			r, _ := f.registry.NewRequest(f.owner, f.presenter, "vmcp-deploy", "read", "deployment.read", []string{"deploy:read"})
			return r
		}},
		{"operation", nil, func(f scenario3Fixture) Request {
			r, _ := f.registry.NewRequest(f.owner, f.presenter, "vmcp-repository", "status", "repository.status", []string{"repo:status"})
			return r
		}},
		{"detail", func(f *scenario3Fixture) {
			exp := f.spies.subject.expected
			exp.detail = "other-detail"
			f.spies.setExpected(exp)
		}, nil},
		{"scope", func(f *scenario3Fixture) {
			exp := f.spies.subject.expected
			exp.scopes = []string{"repo:status"}
			f.spies.setExpected(exp)
		}, nil},
	} {
		t.Run("bound "+tc.name, func(t *testing.T) {
			f := newScenario3Fixture(t)
			if tc.mutate != nil {
				tc.mutate(&f)
			}
			r := f.request
			if tc.requestFn != nil {
				r = tc.requestFn(f)
			}
			if _, err := f.exchange(t, r, "bound-mismatch"); err == nil || f.mechanism.calls != 0 {
				t.Fatalf("err = %v, mechanism calls = %d", err, f.mechanism.calls)
			}
		})
	}
}

func TestADR_0302_DenyDominanceAndIndeterminacy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decision  Decision
		retryable bool
	}{
		{"deny", mustDecision(DecisionDeny, time.Date(2035, 1, 2, 3, 5, 5, 0, time.UTC)), false},
		{"unavailable", mustDecision(DecisionUnavailable, time.Date(2035, 1, 2, 3, 5, 5, 0, time.UTC)), true},
		{"indeterminate", Decision{}, false},
	} {
		for _, kind := range []FailureKind{FailureConsent, FailureAssociation, FailureTargetPolicy} {
			t.Run(tc.name+"/"+string(kind), func(t *testing.T) {
				f := newScenario3Fixture(t)
				f.spies.source(kind).decision = tc.decision
				_, err := f.exchange(t, f.request, "deny-dominates")
				if !IsFailure(err, kind) || FailureRetryable(err) != tc.retryable || f.mechanism.calls != 0 {
					t.Fatalf("err = %v retryable=%v mechanism=%d", err, FailureRetryable(err), f.mechanism.calls)
				}
			})
		}
	}
}

func TestRegistryRequirementsAreResolvedAtExchangeTime(t *testing.T) {
	f := newScenario3Fixture(t)
	request := f.request
	f.spies.registry.requiredTools = []string{"Deploy"}
	if _, err := f.exchange(t, request, "changed-requirements"); !IsFailure(err, FailureActorTools) || f.mechanism.calls != 0 {
		t.Fatalf("stale request requirements were used: err=%v mechanism=%d", err, f.mechanism.calls)
	}
	f.actor.verified.tools = []string{"Deploy"}
	if _, err := f.exchange(t, request, "current-requirements"); err != nil {
		t.Fatalf("current registry requirements did not permit: %v", err)
	}
}

func TestNewVerifiedActorRejectsNonCanonicalIdentityAndClaims(t *testing.T) {
	identity, err := identityissuer.NewLogicalAgentIdentity("agents.example", identityissuer.DefinitionTierProject, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	if _, err := NewVerifiedActor("agents.example", identity.Subject, identityissuer.DefinitionTierProject, "reviewer", "", []string{"Read"}, "actor-id", until); err != nil {
		t.Fatalf("canonical actor rejected: %v", err)
	}
	for _, tc := range []struct {
		name, trustDomain, subject, actorName, instance string
		tier                                            identityissuer.DefinitionTier
		tools                                           []string
	}{
		{name: "invalid trust domain", trustDomain: "https://agents.example", subject: identity.Subject, tier: identityissuer.DefinitionTierProject, actorName: "reviewer", tools: []string{"Read"}},
		{name: "subject mismatch", trustDomain: "agents.example", subject: identity.Subject + "-other", tier: identityissuer.DefinitionTierProject, actorName: "reviewer", tools: []string{"Read"}},
		{name: "tier mismatch", trustDomain: "agents.example", subject: identity.Subject, tier: identityissuer.DefinitionTierManaged, actorName: "reviewer", tools: []string{"Read"}},
		{name: "name mismatch", trustDomain: "agents.example", subject: identity.Subject, tier: identityissuer.DefinitionTierProject, actorName: "other", tools: []string{"Read"}},
		{name: "invalid instance", trustDomain: "agents.example", subject: identity.Subject, tier: identityissuer.DefinitionTierProject, actorName: "reviewer", instance: "bad\ninstance", tools: []string{"Read"}},
		{name: "duplicate tools", trustDomain: "agents.example", subject: identity.Subject, tier: identityissuer.DefinitionTierProject, actorName: "reviewer", tools: []string{"Read", "Read"}},
		{name: "unsorted tools", trustDomain: "agents.example", subject: identity.Subject, tier: identityissuer.DefinitionTierProject, actorName: "reviewer", tools: []string{"Write", "Read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewVerifiedActor(tc.trustDomain, tc.subject, tc.tier, tc.actorName, tc.instance, tc.tools, "actor-id", until); err == nil {
				t.Fatal("NewVerifiedActor succeeded")
			}
		})
	}
}

func TestInvariant_acting_access_exact_actor_tools(t *testing.T) {
	f := newScenario3Fixture(t)
	f.actor.verified.tools = []string{"Grep"}
	if _, err := f.exchange(t, f.request, "narrow"); !IsFailure(err, FailureActorTools) {
		t.Fatalf("narrow actor err = %v", err)
	}
	f.actor.verified.tools = []string{"Read"}
	if _, err := f.exchange(t, f.request, "fresh-authority"); err != nil {
		t.Fatalf("fresh exact tools: %v", err)
	}
	if f.actor.calls != 2 || f.mechanism.calls != 1 {
		t.Fatalf("actor verifier calls=%d mechanism calls=%d", f.actor.calls, f.mechanism.calls)
	}
}

func TestActingAccess_Scenario3_RegisteredRequestOnly(t *testing.T) {
	f := newScenario3Fixture(t)
	for _, tc := range []struct {
		name, resource, operation, detail string
		scopes                            []string
	}{
		{"unknown resource", "unknown", "read", "repository.read", []string{"repo:read"}},
		{"resource alias", "vmcp-repo", "read", "repository.read", []string{"repo:read"}},
		{"unknown operation", "vmcp-repository", "inspect", "repository.read", []string{"repo:read"}},
		{"unknown detail", "vmcp-repository", "read", "repository.inspect", []string{"repo:read"}},
		{"omitted scope", "vmcp-repository", "read", "repository.read", nil},
		{"default scope", "vmcp-repository", "read", "repository.read", []string{"default"}},
		{"extra scope", "vmcp-repository", "read", "repository.read", []string{"repo:read", "repo:status"}},
		{"cross-resource scope", "vmcp-deploy", "read", "deployment.read", []string{"repo:read"}},
		{"tool-name inference", "read", "read", "repository.read", []string{"repo:read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.registry.NewRequest(f.owner, f.presenter, tc.resource, tc.operation, tc.detail, tc.scopes); err == nil {
				t.Fatal("unregistered request accepted")
			}
		})
	}
}

func TestInvariant_acting_access_correlation_is_not_authority(t *testing.T) {
	f := newScenario3Fixture(t)
	var traces []PermitTrace
	for _, correlation := range []string{"caller-a", "caller-b", "forged\ncorrelation", "caller-a"} {
		result, err := f.exchange(t, f.request, correlation)
		if err != nil {
			t.Fatalf("Exchange(%q): %v", correlation, err)
		}
		traces = append(traces, result.Trace())
	}
	if traces[0].Correlation() == traces[1].Correlation() || len(traces[2].Correlation()) > MaxTraceCorrelationBytes {
		t.Fatalf("correlations not observably bounded: %q %q %q", traces[0].Correlation(), traces[1].Correlation(), traces[2].Correlation())
	}
	if traces[0].Correlation() != traces[3].Correlation() || f.mechanism.calls != 4 {
		t.Fatalf("duplicate correlation merged decisions: correlations=%q/%q calls=%d", traces[0].Correlation(), traces[3].Correlation(), f.mechanism.calls)
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(SubjectAuthorityInput{}), reflect.TypeOf(ConsentInput{}), reflect.TypeOf(AssociationInput{}), reflect.TypeOf(RegistryInput{}), reflect.TypeOf(TargetPolicyInput{}), reflect.TypeOf(MechanismInput{})} {
		if _, ok := typ.MethodByName("Correlation"); ok {
			t.Errorf("%s exposes correlation as decision/mechanism input", typ)
		}
	}
}
