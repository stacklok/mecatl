package actingaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/stacklok/mecatl/internal/identityissuer"
)

// ActorVerifier is the closed I2 credential verification port.
type ActorVerifier interface {
	Verify(I2Token) (VerifiedActor, error)
}

// VerifiedActor is a copied logical-agent identity and exact authority set.
// Its zero value carries no authority and callers cannot construct non-zero facts.
type VerifiedActor struct {
	trustDomain string
	subject     string
	tier        identityissuer.DefinitionTier
	name        string
	instance    string
	tools       []string
	jwtID       string
	notAfter    time.Time
}

// TrustDomain returns the verified Mecatl trust domain.
func (v VerifiedActor) TrustDomain() string { return v.trustDomain }

// Subject returns the canonical logical-definition SPIFFE subject.
func (v VerifiedActor) Subject() string { return v.subject }

// Tier returns the verified definition tier.
func (v VerifiedActor) Tier() identityissuer.DefinitionTier { return v.tier }

// Name returns the verified logical definition name.
func (v VerifiedActor) Name() string { return v.name }

// Instance returns the audit-only verified instance identifier.
func (v VerifiedActor) Instance() string { return v.instance }

// Tools returns a fresh copy of the exact verified tool authority.
func (v VerifiedActor) Tools() []string { return append([]string(nil), v.tools...) }

// JWTID returns the audit-only verified token identifier.
func (v VerifiedActor) JWTID() string { return v.jwtID }

// NotAfter returns the verified actor expiry.
func (v VerifiedActor) NotAfter() time.Time { return v.notAfter }

// IsZero reports whether no verified actor facts are present.
func (v VerifiedActor) IsZero() bool {
	return v.trustDomain == "" && v.subject == "" && v.tier == "" && v.name == "" && v.instance == "" && len(v.tools) == 0 && v.jwtID == "" && v.notAfter.IsZero()
}

// ContainsRequiredTools reports whether this verified copy contains every exact
// registered tool requirement. No identity or name implies tool authority.
func (v VerifiedActor) ContainsRequiredTools(required []string) bool {
	if v.IsZero() || len(required) == 0 {
		return false
	}
	available := make(map[string]struct{}, len(v.tools))
	for _, tool := range v.tools {
		available[tool] = struct{}{}
	}
	for _, tool := range required {
		if _, ok := available[tool]; !ok {
			return false
		}
	}
	return true
}

// LogicalActorVerifier adapts the existing ADR-0252 logical-agent verifier. It
// never parses JWTs itself and copies every returned authority-bearing value.
type LogicalActorVerifier struct {
	verifier *identityissuer.LogicalAgentVerifier
}

// NewLogicalActorVerifier constructs the I3 adapter over the I2 verifier.
func NewLogicalActorVerifier(verifier *identityissuer.LogicalAgentVerifier) (*LogicalActorVerifier, error) {
	if verifier == nil {
		return nil, errors.New("logical actor verifier is required")
	}
	return &LogicalActorVerifier{verifier: verifier}, nil
}

// Refresh atomically refreshes the underlying complete I2 verification bundle.
func (v *LogicalActorVerifier) Refresh(ctx context.Context) error {
	if v == nil || v.verifier == nil {
		return errors.New("logical actor verifier is unavailable")
	}
	return v.verifier.Refresh(ctx)
}

// Verify delegates compact I2 verification to identityissuer and copies the result.
// No partially decoded identity or tools are returned on any error.
func (v *LogicalActorVerifier) Verify(token I2Token) (VerifiedActor, error) {
	if v == nil || v.verifier == nil || token.secret.value == nil {
		return VerifiedActor{}, errors.New("logical actor token is unavailable")
	}
	verified, err := v.verifier.Verify(token.secret.value.raw)
	if err != nil {
		return VerifiedActor{}, err
	}
	return VerifiedActor{
		trustDomain: verified.TrustDomain,
		subject:     verified.Subject,
		tier:        verified.Tier,
		name:        verified.Name,
		instance:    verified.Instance,
		tools:       append([]string(nil), verified.Tools...),
		jwtID:       verified.JWTID,
		notAfter:    verified.Expiry,
	}, nil
}

// DecisionEffect is the closed result vocabulary shared by authority sources.
type DecisionEffect string

// Closed decision effects returned by every authority source.
const (
	DecisionPermit      DecisionEffect = "permit"
	DecisionDeny        DecisionEffect = "deny"
	DecisionUnavailable DecisionEffect = "unavailable"
)

// Decision is a bounded source decision. A validity bound is mandatory for every effect.
type Decision struct {
	effect   DecisionEffect
	notAfter time.Time
}

// NewDecision constructs one closed authority-source decision.
func NewDecision(effect DecisionEffect, notAfter time.Time) (Decision, error) {
	if (effect != DecisionPermit && effect != DecisionDeny && effect != DecisionUnavailable) || notAfter.IsZero() {
		return Decision{}, errors.New("acting-access decision is invalid")
	}
	return Decision{effect: effect, notAfter: notAfter.UTC()}, nil
}

// Effect returns the closed decision result.
func (d Decision) Effect() DecisionEffect { return d.effect }

// NotAfter returns the source validity ceiling.
func (d Decision) NotAfter() time.Time { return d.notAfter }

type decisionFacts struct {
	owner        Owner
	presenter    Presenter
	actorSubject string
	actorTools   []string
	consentProof string
	resource     RegisteredResource
	operation    RegisteredOperation
	detail       RegisteredDetail
	scopes       []string
}

func (f decisionFacts) Owner() Owner                   { return f.owner }
func (f decisionFacts) Presenter() Presenter           { return f.presenter }
func (f decisionFacts) ActorSubject() string           { return f.actorSubject }
func (f decisionFacts) ActorTools() []string           { return append([]string(nil), f.actorTools...) }
func (f decisionFacts) ConsentProof() string           { return f.consentProof }
func (f decisionFacts) Resource() RegisteredResource   { return f.resource }
func (f decisionFacts) Operation() RegisteredOperation { return f.operation }
func (f decisionFacts) Detail() RegisteredDetail       { return f.detail }
func (f decisionFacts) Scopes() []string               { return append([]string(nil), f.scopes...) }

type decisionInputView interface {
	Owner() Owner
	Presenter() Presenter
	ActorSubject() string
	ActorTools() []string
	ConsentProof() string
	Resource() RegisteredResource
	Operation() RegisteredOperation
	Detail() RegisteredDetail
	Scopes() []string
}

// SubjectAuthorityInput is the exact tuple presented only to the subject authority source.
type SubjectAuthorityInput struct{ decisionFacts }

// ConsentInput is the exact tuple and independently authenticated consent reference.
type ConsentInput struct{ decisionFacts }

// AssociationInput is the exact authenticated-presenter to logical-actor tuple.
type AssociationInput struct{ decisionFacts }

// RegistryInput is the exact tuple presented to the current target registry.
type RegistryInput struct{ decisionFacts }

// TargetPolicyInput is the exact tuple presented to target policy.
type TargetPolicyInput struct{ decisionFacts }

// SubjectAuthority decides whether the verified subject may exercise the exact tuple.
type SubjectAuthority interface {
	DecideSubject(context.Context, SubjectAuthorityInput) Decision
}

// ConsentAuthority verifies independently authenticated consent for the exact tuple.
type ConsentAuthority interface {
	DecideConsent(context.Context, ConsentInput) Decision
}

// PresenterAssociation decides the AS-authenticated presenter-to-actor association.
type PresenterAssociation interface {
	DecideAssociation(context.Context, AssociationInput) Decision
}

// RequestRegistry revalidates the exact registered resource/operation/detail/scope tuple.
type RequestRegistry interface {
	DecideRegistration(context.Context, RegistryInput) Decision
}

// TargetPolicy decides current target authority for the exact tuple.
type TargetPolicy interface {
	DecideTarget(context.Context, TargetPolicyInput) Decision
}

// MechanismInput is the immutable validated plan passed to an exchange mechanism.
// Its fields are unexported so an adapter can consume but cannot mint an authorized plan.
type MechanismInput struct {
	facts    decisionFacts
	notAfter time.Time
}

// Owner returns the verified owner.
func (i MechanismInput) Owner() Owner { return i.facts.owner }

// Presenter returns the AS-authenticated presenter.
func (i MechanismInput) Presenter() Presenter { return i.facts.presenter }

// ActorSubject returns the verified logical actor subject.
func (i MechanismInput) ActorSubject() string { return i.facts.actorSubject }

// ActorTools returns a copy of the actor's exact verified tools.
func (i MechanismInput) ActorTools() []string { return append([]string(nil), i.facts.actorTools...) }

// ConsentProof returns the independently verified non-secret consent reference.
func (i MechanismInput) ConsentProof() string { return i.facts.consentProof }

// Resource returns the exact registered resource.
func (i MechanismInput) Resource() RegisteredResource { return i.facts.resource }

// Operation returns the exact registered operation.
func (i MechanismInput) Operation() RegisteredOperation { return i.facts.operation }

// Detail returns the exact registered authorization detail.
func (i MechanismInput) Detail() RegisteredDetail { return i.facts.detail }

// Scopes returns a copy of the exact canonical scopes.
func (i MechanismInput) Scopes() []string { return append([]string(nil), i.facts.scopes...) }

// NotAfter returns the minimum validated issuance ceiling.
func (i MechanismInput) NotAfter() time.Time { return i.notAfter }

// OutputVerifier independently verifies the closed issued output before access returns.
type OutputVerifier interface {
	Verify(ExchangeResponse, MechanismInput) (VerifiedOutput, error)
}

// ExchangeMechanism is the only credential-bearing issuance seam.
type ExchangeMechanism interface {
	Exchange(context.Context, MechanismInput, SubjectAssertion, I2Token) (ExchangeResponse, error)
}

// FailureKind is a stable non-secret refusal category.
type FailureKind string

// Stable failure kinds identify the ceiling that refused without exposing source detail.
const (
	FailureSubjectVerification FailureKind = "subject_verification"
	FailureOwner               FailureKind = "owner"
	FailureActorVerification   FailureKind = "actor_verification"
	FailureSubjectAuthority    FailureKind = "subject_authority"
	FailureConsent             FailureKind = "consent"
	FailureAssociation         FailureKind = "association"
	FailureActorTools          FailureKind = "actor_tools"
	FailureRegistry            FailureKind = "registry"
	FailureTargetPolicy        FailureKind = "target_policy"
	FailureMechanism           FailureKind = "mechanism"
)

type exchangeFailure struct {
	kind      FailureKind
	retryable bool
}

func (e *exchangeFailure) Error() string { return "acting-access exchange refused: " + string(e.kind) }

// IsFailure reports whether err has the stable failure kind.
func IsFailure(err error, kind FailureKind) bool {
	var failure *exchangeFailure
	return errors.As(err, &failure) && failure.kind == kind
}

// FailureRetryable reports whether an unavailable dependency may be retried.
func FailureRetryable(err error) bool {
	var failure *exchangeFailure
	return errors.As(err, &failure) && failure.retryable
}

func refuse(kind FailureKind, retryable bool) error {
	return &exchangeFailure{kind: kind, retryable: retryable}
}

// GateKind identifies one successfully applied authorization ceiling.
type GateKind string

// Successful gate kinds form the complete bounded permit trace.
const (
	GateOwner            GateKind = "owner"
	GateSubjectAuthority GateKind = "subject_authority"
	GateConsent          GateKind = "consent"
	GateAssociation      GateKind = "association"
	GateActorTools       GateKind = "actor_tools"
	GateRegistry         GateKind = "registry"
	GateTargetPolicy     GateKind = "target_policy"
)

// MaxTraceCorrelationBytes is the fixed maximum correlation projection size.
const MaxTraceCorrelationBytes = len("sha256:") + sha256.Size*2

// PermitTrace is a bounded per-call authorization trace. Correlation is audit-only.
type PermitTrace struct {
	gates       []GateKind
	correlation string
}

// Gates returns a copy of every successful required gate in evaluation order.
func (t PermitTrace) Gates() []GateKind { return append([]GateKind(nil), t.gates...) }

// Correlation returns a bounded digest of caller correlation.
func (t PermitTrace) Correlation() string { return t.correlation }

// ExchangeResult is the successful mechanism response and its non-secret permit trace.
type ExchangeResult struct {
	response ExchangeResponse
	trace    PermitTrace
	plan     MechanismInput
}

// Token returns the opaque output credential.
func (r ExchangeResult) Token() OutputToken { return r.response.Token() }

// Response returns the closed RFC 8693 response metadata and opaque token.
func (r ExchangeResult) Response() ExchangeResponse { return r.response }

// Trace returns the immutable permit trace.
func (r ExchangeResult) Trace() PermitTrace {
	return PermitTrace{gates: r.trace.Gates(), correlation: r.trace.correlation}
}

// GateConfig wires the complete acting-access conjunction.
type GateConfig struct {
	SubjectVerifier  SubjectAssertionVerifier
	ActorVerifier    ActorVerifier
	SubjectAuthority SubjectAuthority
	Consent          ConsentAuthority
	Association      PresenterAssociation
	Registry         RequestRegistry
	TargetPolicy     TargetPolicy
	Mechanism        ExchangeMechanism
	OutputVerifier   OutputVerifier
	MaximumLifetime  time.Duration
	Now              func() time.Time
}

// Gate is the production acting-as-user authorization entrypoint.
type Gate struct {
	cfg GateConfig
}

// NewGate refuses partial conjunction wiring.
func NewGate(cfg GateConfig) (*Gate, error) {
	if cfg.SubjectVerifier == nil || cfg.ActorVerifier == nil || cfg.SubjectAuthority == nil || cfg.Consent == nil ||
		cfg.Association == nil || cfg.Registry == nil || cfg.TargetPolicy == nil || cfg.Mechanism == nil || cfg.OutputVerifier == nil ||
		cfg.MaximumLifetime <= 0 || cfg.MaximumLifetime > time.Hour {
		return nil, errors.New("acting-access gate configuration is incomplete")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Gate{cfg: cfg}, nil
}

// Exchange verifies fresh credential facts, applies every independent ceiling, and
// invokes the mechanism only with the resulting immutable minimum-lifetime plan.
//
//nolint:gocyclo // The explicit authorization conjunction must remain auditable in one ordered gate.
func (g *Gate) Exchange(ctx context.Context, request Request, subjectToken SubjectAssertion, actorToken I2Token, correlation string) (ExchangeResult, error) {
	if g == nil {
		return ExchangeResult{}, refuse(FailureSubjectVerification, false)
	}
	now := g.cfg.Now().UTC()
	subject, err := g.cfg.SubjectVerifier.Verify(subjectToken)
	if err != nil || subject.IsZero() || !subject.notAfter.After(now) {
		return ExchangeResult{}, refuse(FailureSubjectVerification, false)
	}
	if request.owner == (Owner{}) || subject.owner != request.owner {
		return ExchangeResult{}, refuse(FailureOwner, false)
	}
	actor, err := g.cfg.ActorVerifier.Verify(actorToken)
	if err != nil || actor.IsZero() || !actor.notAfter.After(now) {
		return ExchangeResult{}, refuse(FailureActorVerification, false)
	}
	facts := decisionFacts{
		owner: request.owner, presenter: request.presenter, actorSubject: actor.subject,
		actorTools: actor.Tools(), consentProof: subject.consentProof, resource: request.resource,
		operation: request.operation, detail: request.detail, scopes: request.Scopes(),
	}
	trace := []GateKind{GateOwner}
	bounds := []time.Time{subject.notAfter, actor.notAfter, now.Add(g.cfg.MaximumLifetime)}

	subjectDecision := g.cfg.SubjectAuthority.DecideSubject(ctx, SubjectAuthorityInput{facts})
	if err := validateDecision(subjectDecision, now, FailureSubjectAuthority); err != nil {
		return ExchangeResult{}, err
	}
	bounds = append(bounds, subjectDecision.notAfter)
	trace = append(trace, GateSubjectAuthority)

	consentDecision := g.cfg.Consent.DecideConsent(ctx, ConsentInput{facts})
	if err := validateDecision(consentDecision, now, FailureConsent); err != nil {
		return ExchangeResult{}, err
	}
	bounds = append(bounds, consentDecision.notAfter)
	trace = append(trace, GateConsent)

	associationDecision := g.cfg.Association.DecideAssociation(ctx, AssociationInput{facts})
	if err := validateDecision(associationDecision, now, FailureAssociation); err != nil {
		return ExchangeResult{}, err
	}
	bounds = append(bounds, associationDecision.notAfter)
	trace = append(trace, GateAssociation)

	if !actor.ContainsRequiredTools(request.requiredTools) {
		return ExchangeResult{}, refuse(FailureActorTools, false)
	}
	trace = append(trace, GateActorTools)

	registryDecision := g.cfg.Registry.DecideRegistration(ctx, RegistryInput{facts})
	if err := validateDecision(registryDecision, now, FailureRegistry); err != nil {
		return ExchangeResult{}, err
	}
	bounds = append(bounds, registryDecision.notAfter)
	trace = append(trace, GateRegistry)

	targetDecision := g.cfg.TargetPolicy.DecideTarget(ctx, TargetPolicyInput{facts})
	if err := validateDecision(targetDecision, now, FailureTargetPolicy); err != nil {
		return ExchangeResult{}, err
	}
	bounds = append(bounds, targetDecision.notAfter)
	trace = append(trace, GateTargetPolicy)
	notAfter := bounds[0]
	for _, bound := range bounds[1:] {
		if bound.Before(notAfter) {
			notAfter = bound
		}
	}
	input := MechanismInput{facts: copyDecisionFacts(facts), notAfter: notAfter}
	response, err := g.cfg.Mechanism.Exchange(ctx, input, subjectToken, actorToken)
	if err != nil || !validExchangeResponse(response, input, now) {
		return ExchangeResult{}, refuse(FailureMechanism, true)
	}
	if _, err := g.cfg.OutputVerifier.Verify(response, input); err != nil {
		return ExchangeResult{}, refuse(FailureMechanism, false)
	}
	digest := sha256.Sum256([]byte(correlation))
	return ExchangeResult{response: response, plan: input, trace: PermitTrace{gates: append([]GateKind(nil), trace...), correlation: "sha256:" + hex.EncodeToString(digest[:])}}, nil
}

func validateDecision(decision Decision, now time.Time, kind FailureKind) error {
	switch decision.effect {
	case DecisionUnavailable:
		return refuse(kind, true)
	case DecisionDeny:
		return refuse(kind, false)
	case DecisionPermit:
		if decision.notAfter.After(now) {
			return nil
		}
	}
	return refuse(kind, false)
}

func copyDecisionFacts(facts decisionFacts) decisionFacts {
	facts.actorTools = append([]string(nil), facts.actorTools...)
	facts.scopes = append([]string(nil), facts.scopes...)
	return facts
}
