package as1server

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// actorValidatorName is the name AS-1 registers the identity broker's
// actor-token validator under, which the delegate client's actor profile
// selects. ToolHive reserves names starting with "toolhive.".
const actorValidatorName = "mecatl"

// The plain errors the validator returns, which AS-1 answers with a
// retryable server_error. ToolHive logs their text, so it is fixed: never the
// broker's error text, a user identifier, or a token.
var (
	errVerifierNotBound = errors.New("as1server: the identity broker is not ready to check actor credentials")
	errGrantSubjectID   = errors.New("as1server: the grant has no iss_sub sub_id")
	errActorCheckFailed = errors.New("as1server: the identity broker could not check the actor credential")
)

// actorValidator is AS-1's actor-token check of the delegate client's ID-JAG
// requests (design step (7), check 6): it asks the identity broker's
// CredentialVerifier whether the actor token is a valid credential of an
// agent in an open session of the grant's owner (check 8). It never falls
// back to another check.
type actorValidator struct {
	// clientID is the delegate client's ID, the only client it accepts.
	clientID string
	// actIssuer is act.iss, the trust domain every agent's act.sub is in.
	actIssuer string
	// verifier is nil until Server.UseCredentialVerifier binds it.
	verifier atomic.Pointer[identitybroker.CredentialVerifier]
}

var _ tokenexchange.ActorTokenValidator = (*actorValidator)(nil)

// ValidateActor implements tokenexchange.ActorTokenValidator. An expired
// credential is actor_token_expired, the broker's denied refusal, or a request
// from another client, is denied, and everything else, including an unbound
// verifier, a grant without an iss_sub sub_id, and any error outside
// CredentialVerifier's contract, is a plain error.
func (a *actorValidator) ValidateActor(ctx context.Context, req tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
	if req.ClientID != a.clientID {
		return tokenexchange.ActorResult{}, &tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonDenied}
	}
	v := a.verifier.Load()
	if v == nil {
		return tokenexchange.ActorResult{}, errVerifierNotBound
	}
	// A grant without one is a configuration fault: a denial would be final
	// for the client and hide it from operators.
	owner, err := identitybroker.OwnerFromSubjectIdentifier([]byte(req.Grant.SubjectIdentifier))
	if err != nil {
		return tokenexchange.ActorResult{}, errGrantSubjectID
	}
	agent, err := (*v).VerifyCredentialForOwner(ctx, identitybroker.VerifyCredentialForOwnerRequest{
		Owner: owner, Credential: identitybroker.NewToken(req.ActorToken),
	})
	var e *identitybroker.Error
	switch {
	case err == nil && (agent.AgentID == "" || !strings.HasPrefix(agent.ActorID, a.actIssuer+"/")):
		// AS-1 never names no agent, or one outside its act.iss: act.iss
		// and the broker's trust domain disagree, a configuration fault.
		return tokenexchange.ActorResult{}, errActorCheckFailed
	case err == nil:
		return tokenexchange.ActorResult{
			AgentID:           agent.ActorID,
			ExpiresAt:         agent.ExpiresAt,
			CredentialSubject: agent.Agent.Subject,
		}, nil
	case errors.As(err, &e) && e.Code == identitybroker.CodeUnauthenticated && e.Reason == identitybroker.ReasonSVIDExpired:
		return tokenexchange.ActorResult{}, &tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonActorTokenExpired}
	case errors.As(err, &e) && e.Code == identitybroker.CodePermissionDenied && e.Reason == identitybroker.ReasonDenied:
		return tokenexchange.ActorResult{}, &tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonDenied}
	default:
		return tokenexchange.ActorResult{}, errActorCheckFailed
	}
}

// UseCredentialVerifier binds v, the identity broker's, as AS-1's check of
// the actor token of every ID-JAG request. AS-1 is built before the broker,
// so the verifier comes later; until then AS-1 answers those requests with a
// retryable server_error. It fails when the delegate client has no IDJAG
// issuance, v is nil, or a verifier is already bound. It is safe to call while
// AS-1 serves requests.
func (s *Server) UseCredentialVerifier(v identitybroker.CredentialVerifier) error {
	switch {
	case s.actors == nil:
		return errors.New("as1server: no delegate client issues ID-JAGs")
	case v == nil:
		return errors.New("as1server: the credential verifier is nil")
	case !s.actors.verifier.CompareAndSwap(nil, &v):
		return errors.New("as1server: a credential verifier is already bound")
	}
	return nil
}
