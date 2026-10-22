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
// is registered to can be checked. Registering an instance again must keep its
// owner, host session, and parent; its definition is not compared yet.
type agentRecord struct {
	sessionID     string
	hostSessionID string
	owner         Owner
	incarnation   string
	definition    DefinitionRef
	parent        AgentRef
}

// RegisterAgent implements Core. It checks the session, the owner, the root
// host session or parent, and an earlier registration of the instance, issues
// the credential, and only then records the instance in the session, so a
// failure changes nothing. The definition is accepted as reported, without a
// root eligibility check, because the broker loads no definitions yet.
func (b *Broker) RegisterAgent(_ context.Context, workload Workload, req RegisterAgentRequest) (Registration, error) {
	if err := checkCall(workload, req); err != nil {
		return Registration{}, err
	}
	subject := b.issuer.subject(req.Incarnation)
	t := b.sessions
	if _, err := t.checkRegistration(workload, subject, req); err != nil {
		return Registration{}, err
	}

	// Sign outside the lock: a remote signer may be slow.
	credential, expiresAt, err := b.issuer.issue(subject, t.now())
	if err != nil {
		return Registration{}, NewError(CodeInternal, "", fmt.Sprintf("issue the agent credential: %v", err))
	}

	// Check again under the lock that records the instance: the session or
	// the instance may have changed while signing.
	t.mu.Lock()
	defer t.mu.Unlock()
	s, err := t.checkRegistrationLocked(workload, subject, req)
	if err != nil {
		return Registration{}, err
	}
	if rec, ok := t.agents[subject]; ok {
		// Registering again only moves the instance. Its definition stays the
		// first one reported, since a later one is not compared yet.
		rec.sessionID = s.ID
	} else {
		t.agents[subject] = &agentRecord{
			sessionID:     s.ID,
			hostSessionID: req.HostSessionID,
			owner:         req.Owner,
			incarnation:   req.Incarnation,
			definition:    req.Definition,
			parent:        req.Parent,
		}
	}
	return Registration{Agent: AgentRef{Subject: subject}, Credential: credential, ExpiresAt: expiresAt}, nil
}

// checkRegistration is checkRegistrationLocked under t.mu.
func (t *sessionTable) checkRegistration(workload Workload, subject string, req RegisterAgentRequest) (Session, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.checkRegistrationLocked(workload, subject, req)
}

// checkRegistrationLocked returns the usable session for req after the
// owner, root host session, parent, and re-registration checks. An instance
// registered before, in any session, must keep its owner, host session, and
// parent; only its broker session may change. The caller holds t.mu.
func (t *sessionTable) checkRegistrationLocked(workload Workload, subject string, req RegisterAgentRequest) (Session, error) {
	s, err := t.usableLocked(workload, req.SessionID)
	if err != nil {
		return Session{}, err
	}
	if req.Owner != s.Owner {
		return Session{}, NewError(CodePermissionDenied, "", "the registration's owner is not the broker session's owner")
	}
	if req.Parent.IsZero() {
		if req.HostSessionID != s.HostSessionID {
			return Session{}, NewError(CodePermissionDenied, "", "an agent with no parent must run in the broker session's root host session")
		}
	} else if parent, ok := t.agents[req.Parent.Subject]; !ok || parent.sessionID != s.ID {
		return Session{}, NewError(CodePermissionDenied, "", "the parent agent is not registered to the broker session")
	}
	if rec, ok := t.agents[subject]; ok && (rec.owner != req.Owner || rec.hostSessionID != req.HostSessionID || rec.parent != req.Parent) {
		return Session{}, NewError(CodeAlreadyExists, "", "the agent is registered with a different owner, host session, or parent")
	}
	return s, nil
}

// VerifyCredential implements Core. After the credential's own checks (see
// credentialIssuer.verify), its subject must be a registered instance, and
// that instance must be registered to the request's session, and that session
// must be open, so closing a session ends its credentials' use at once rather
// than at their expiry. Per-instance subjects are what bind a credential to
// one session, with no session claim. It does not check which workload opened
// the session: AS-1 checks that from its refresh token's record.
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
	rec, registered := t.agents[cred.subject]
	bound := registered && rec.sessionID == req.SessionID
	open := bound && t.byID[req.SessionID].closed == ""
	t.mu.Unlock()
	switch {
	case !registered:
		return VerifiedAgent{}, credentialDenied("the credential's subject is not a registered agent")
	case !bound:
		return VerifiedAgent{}, credentialDenied("the agent is not registered to the broker session")
	case !open:
		return VerifiedAgent{}, credentialDenied("the broker session is closed")
	}
	return VerifiedAgent{Agent: AgentRef{Subject: cred.subject}, AgentID: b.agentID, ExpiresAt: cred.expiresAt}, nil
}
