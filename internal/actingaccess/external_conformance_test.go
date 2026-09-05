package actingaccess_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/actingaccess"
	"github.com/stacklok/mecatl/internal/identityissuer"
)

type externalSubjectVerifier struct {
	owner actingaccess.Owner
	until time.Time
}

func (v externalSubjectVerifier) Verify(token actingaccess.SubjectAssertion, _ actingaccess.Presenter) (actingaccess.VerifiedSubject, error) {
	if err := token.Consume(subjectBytesConsumer{}); err != nil {
		return actingaccess.VerifiedSubject{}, err
	}
	return actingaccess.NewVerifiedSubject(v.owner, "consent-proof", v.until)
}

type subjectBytesConsumer struct{}

func (subjectBytesConsumer) ConsumeSubjectAssertion([]byte) error { return nil }

type externalActorVerifier struct{ until time.Time }

func (v externalActorVerifier) Verify(token actingaccess.I2Token) (actingaccess.VerifiedActor, error) {
	if err := token.Consume(actorBytesConsumer{}); err != nil {
		return actingaccess.VerifiedActor{}, err
	}
	identity, err := identityissuer.NewLogicalAgentIdentity("agents.example", identityissuer.DefinitionTierProject, "reviewer")
	if err != nil {
		return actingaccess.VerifiedActor{}, err
	}
	return actingaccess.NewVerifiedActor("agents.example", identity.Subject,
		identityissuer.DefinitionTierProject, "reviewer", "", []string{"Read"}, "actor-id", v.until)
}

type actorBytesConsumer struct{}

func (actorBytesConsumer) ConsumeI2Token([]byte) error { return nil }

type externalMechanism struct{}

func (externalMechanism) Exchange(_ context.Context, in actingaccess.MechanismInput, subject actingaccess.SubjectAssertion, actor actingaccess.I2Token) (actingaccess.ExchangeResponse, error) {
	if err := subject.Consume(subjectBytesConsumer{}); err != nil {
		return actingaccess.ExchangeResponse{}, err
	}
	if err := actor.Consume(actorBytesConsumer{}); err != nil {
		return actingaccess.ExchangeResponse{}, err
	}
	token, err := actingaccess.NewOutputToken("opaque-output")
	if err != nil {
		return actingaccess.ExchangeResponse{}, err
	}
	return actingaccess.NewExchangeResponse(token, 30, in.Scopes())
}

type externalOutputVerifier struct{}

func (externalOutputVerifier) Verify(response actingaccess.ExchangeResponse, in actingaccess.MechanismInput) (actingaccess.VerifiedOutput, error) {
	if err := response.Token().Consume(outputBytesConsumer{}); err != nil {
		return actingaccess.VerifiedOutput{}, err
	}
	return actingaccess.NewVerifiedOutput("sha256:owner", in.ActorSubject(), in.Presenter().Value(), in.Resource().Value(),
		in.Detail().Value(), in.Scopes(), in.NotAfter())
}

type outputBytesConsumer struct{}

func (outputBytesConsumer) ConsumeOutputToken([]byte) error { return nil }

func TestExternalCollaboratorsImplementCredentialBoundaries(t *testing.T) {
	owner, err := actingaccess.NewOwner("https://issuer.example", "alice")
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	var _ actingaccess.SubjectAssertionVerifier = externalSubjectVerifier{owner: owner, until: until}
	var _ actingaccess.ActorVerifier = externalActorVerifier{until: until}
	var _ actingaccess.ExchangeMechanism = externalMechanism{}
	var _ actingaccess.OutputVerifier = externalOutputVerifier{}

	subject, _ := actingaccess.NewSubjectAssertion("subject")
	verified, err := (externalSubjectVerifier{owner: owner, until: until}).Verify(subject, mustExternalPresenter(t))
	if err != nil || verified.Owner() != owner {
		t.Fatalf("external verifier result = %#v, %v", verified, err)
	}
}

func mustExternalPresenter(t *testing.T) actingaccess.Presenter {
	t.Helper()
	presenter, err := actingaccess.NewPresenter("broker-prod")
	if err != nil {
		t.Fatal(err)
	}
	return presenter
}
