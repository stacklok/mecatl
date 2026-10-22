package identitybroker

import (
	"context"
	"fmt"
)

// agentRecord is a registered agent instance. Records are kept apart from
// broker sessions: registering an instance into a new session moves its
// record there, and the record outlives the session it was registered to.
//
// Every instance has the shared agent identity: the same ag_ identifier, and
// a SPIFFE ID under one shared path that differs only by incarnation, so a
// credential still names the one instance it was issued to and the session it
// is registered to can be checked. Registering an instance again with
// different facts replaces them; detecting that is left to per-agent
// identities.
type agentRecord struct {
	sessionID     string
	hostSessionID string
	owner         Owner
	incarnation   string
	definition    DefinitionRef
	parent        AgentRef
}

// RegisterAgent implements Core. It checks the session, the owner, and the
// root host session or parent, records the instance in the session, and
// issues its credential. The definition is accepted as reported, without a
// root eligibility check, because the broker loads no definitions yet.
func (b *Broker) RegisterAgent(_ context.Context, workload Workload, req RegisterAgentRequest) (Registration, error) {
	if err := checkCall(workload, req); err != nil {
		return Registration{}, err
	}
	subject := b.issuer.subject(req.Incarnation)
	t := b.sessions
	t.mu.Lock()
	s, err := t.usableLocked(workload, req.SessionID)
	if err == nil {
		err = checkRegistration(t, s, req)
	}
	if err != nil {
		t.mu.Unlock()
		return Registration{}, err
	}
	t.agents[subject] = &agentRecord{
		sessionID:     s.ID,
		hostSessionID: req.HostSessionID,
		owner:         req.Owner,
		incarnation:   req.Incarnation,
		definition:    req.Definition,
		parent:        req.Parent,
	}
	t.mu.Unlock()

	// Sign outside the lock: a remote signer may be slow. A failure leaves the
	// instance registered, so registering again retries.
	credential, expiresAt, err := b.issuer.issue(subject, t.now())
	if err != nil {
		return Registration{}, NewError(CodeInternal, "", fmt.Sprintf("issue the agent credential: %v", err))
	}
	return Registration{Agent: AgentRef{Subject: subject}, Credential: credential, ExpiresAt: expiresAt}, nil
}

// checkRegistration applies the owner, root host session, and parent checks.
// The caller holds t.mu.
func checkRegistration(t *sessionTable, s Session, req RegisterAgentRequest) error {
	if req.Owner != s.Owner {
		return NewError(CodePermissionDenied, "", "the registration's owner is not the broker session's owner")
	}
	if req.Parent.IsZero() {
		if req.HostSessionID != s.HostSessionID {
			return NewError(CodePermissionDenied, "", "an agent with no parent must run in the broker session's root host session")
		}
		return nil
	}
	if parent, ok := t.agents[req.Parent.Subject]; !ok || parent.sessionID != s.ID {
		return NewError(CodePermissionDenied, "", "the parent agent is not registered to the broker session")
	}
	return nil
}

// VerifyCredential checks an agent credential with the design's checks, in
// order: the credential itself (see credentialIssuer.verify), then that its
// subject is a registered instance, then that the instance is registered to
// the request's session. It returns the instance, its ag_ identifier, and the
// credential's expiry. An expired credential fails with CodeUnauthenticated
// and ReasonSVIDExpired; every other failure with CodePermissionDenied and
// ReasonDenied.
//
// It does not check that the session is open or which workload opened it:
// callers check the session's state themselves, the broker before a call and
// AS-1 from its refresh token's record.
func (b *Broker) VerifyCredential(_ context.Context, req VerifyCredentialRequest) (VerifiedAgent, error) {
	if err := req.Validate(); err != nil {
		return VerifiedAgent{}, err
	}
	t := b.sessions
	cred, err := b.issuer.verify(req.Credential, t.now())
	if err != nil {
		return VerifiedAgent{}, err
	}
	t.mu.Lock()
	rec, ok := t.agents[cred.subject]
	var sessionID string
	if ok {
		sessionID = rec.sessionID
	}
	t.mu.Unlock()
	if !ok {
		return VerifiedAgent{}, credentialDenied("the credential's subject is not a registered agent")
	}
	if sessionID != req.SessionID {
		return VerifiedAgent{}, credentialDenied("the agent is not registered to the broker session")
	}
	return VerifiedAgent{Agent: AgentRef{Subject: cred.subject}, AgentID: b.agentID, ExpiresAt: cred.expiresAt}, nil
}

// TrustBundle returns the SPIFFE trust bundle for agent credentials: a JWK
// set holding the agent issuer's public key, with use "jwt-svid" and its key
// ID. The returned slice is a copy.
func (b *Broker) TrustBundle() []byte {
	return append([]byte(nil), b.issuer.bundle...)
}
