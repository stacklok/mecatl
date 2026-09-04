package actingaccess

import (
	"context"
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
