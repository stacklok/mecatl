package identitybroker

import (
	"context"
	"crypto/sha256"
	"time"
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
	// credentials holds the credentials issued since the instance last moved
	// to another broker session, by SHA-256 of the token, with their expiry;
	// at most maxIssuedCredentials. Only these verify, so reopening a session
	// does not revive credentials issued before it closed, however close
	// together the two were issued. Expired entries are dropped when the
	// instance registers again.
	credentials map[[sha256.Size]byte]time.Time
}

// maxIssuedCredentials bounds agentRecord.credentials, so repeated
// registrations cannot grow an agent's record, or the work done under the
// session lock, without limit. Mecatl registers again only when a credential
// nears expiry or the session changes, so a few cover a lost response or a
// retry racing another registration.
const maxIssuedCredentials = 4

// earliestToExpire returns the credential in credentials that expires first.
func earliestToExpire(credentials map[[sha256.Size]byte]time.Time) [sha256.Size]byte {
	var first [sha256.Size]byte
	var firstExp time.Time
	for h, exp := range credentials {
		if firstExp.IsZero() || exp.Before(firstExp) {
			first, firstExp = h, exp
		}
	}
	return first
}

// credentialHash identifies a credential in agentRecord.credentials.
func credentialHash(credential Token) [sha256.Size]byte {
	return sha256.Sum256([]byte(credential.Value()))
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
		// The signer's error is not passed on: from a key service it can name
		// keys or endpoints, and the broker has no log of its own.
		return Registration{}, NewError(CodeInternal, "", "the agent credential could not be issued")
	}

	// Check again under the lock that records the instance: the session or
	// the instance may have changed while signing.
	t.mu.Lock()
	defer t.mu.Unlock()
	s, err := t.checkRegistrationLocked(workload, subject, req)
	if err != nil {
		return Registration{}, err
	}
	t.recordLocked(subject, s.ID, req, credential, expiresAt)
	return Registration{Agent: AgentRef{Subject: subject}, Credential: credential, ExpiresAt: expiresAt}, nil
}

// recordLocked records a registration of subject in session sessionID, with
// the credential just issued, which always verifies afterwards. The caller
// holds t.mu.
func (t *sessionTable) recordLocked(subject, sessionID string, req RegisterAgentRequest, credential Token, expiresAt time.Time) {
	rec, ok := t.agents[subject]
	switch {
	case !ok:
		rec = &agentRecord{
			hostSessionID: req.HostSessionID,
			owner:         req.Owner,
			incarnation:   req.Incarnation,
			definition:    req.Definition,
			parent:        req.Parent,
		}
		t.agents[subject] = rec
	case rec.sessionID == sessionID:
		// Registering again in the same session keeps the credentials issued
		// there, dropping expired ones and, past maxIssuedCredentials, the
		// earliest to expire. Its definition stays the first one reported,
		// since a later one is not compared yet.
		rec.pruneCredentials(t.now())
	}
	if !ok || rec.sessionID != sessionID {
		// A new instance, or one moving to another session: only the
		// credential issued now verifies.
		rec.sessionID = sessionID
		rec.credentials = map[[sha256.Size]byte]time.Time{}
	}
	rec.credentials[credentialHash(credential)] = expiresAt
}

// pruneCredentials drops expired credentials, then the earliest to expire
// until there is room for one more within maxIssuedCredentials.
func (r *agentRecord) pruneCredentials(now time.Time) {
	for h, exp := range r.credentials {
		if !now.Before(exp.Add(credentialClockSkew)) {
			delete(r.credentials, h)
		}
	}
	for len(r.credentials) >= maxIssuedCredentials {
		delete(r.credentials, earliestToExpire(r.credentials))
	}
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
		// The main agent's incarnation is its host session's, so a stale one
		// (from an earlier session under the same host session ID) cannot
		// move that session's agent and identity into this one.
		if req.Incarnation != s.Incarnation {
			return Session{}, NewError(CodePermissionDenied, "", "an agent with no parent must have the root host session's incarnation")
		}
	} else if parent, ok := t.agents[req.Parent.Subject]; !ok || parent.sessionID != s.ID {
		return Session{}, NewError(CodePermissionDenied, "", "the parent agent is not registered to the broker session")
	}
	if rec, ok := t.agents[subject]; ok && (rec.owner != req.Owner || rec.hostSessionID != req.HostSessionID || rec.parent != req.Parent) {
		return Session{}, NewError(CodeAlreadyExists, "", "the agent is registered with a different owner, host session, or parent")
	}
	return s, nil
}

var _ CredentialVerifier = (*Broker)(nil)

// VerifyCredential implements CredentialVerifier. After the credential's own checks (see
// credentialIssuer.verify), its subject must be a registered instance, and
// that instance must be registered to the request's session, and that session
// must be open, so closing a session ends its credentials' use at once rather
// than at their expiry. Only a credential issued since the instance moved to
// this session verifies, so reopening a session does not revive credentials
// issued before it closed. Per-instance subjects are what bind a credential to
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
	var current bool
	if bound {
		_, current = rec.credentials[credentialHash(req.Credential)]
	}
	// A missing session record counts as closed, with no closing reason.
	var closed Reason
	s, ok := t.byID[req.SessionID]
	if ok {
		t.expireLocked(s)
		closed = s.closed
	}
	open := current && ok && closed == ""
	t.mu.Unlock()
	switch {
	case !registered:
		return VerifiedAgent{}, credentialDenied("the credential's subject is not a registered agent")
	case !bound:
		return VerifiedAgent{}, credentialDenied("the agent is not registered to the broker session")
	case !current:
		return VerifiedAgent{}, credentialDenied("the credential was not issued since the agent moved to the broker session")
	case !open && closed != "":
		// The design reports a closed session by its closing reason, so the
		// caller opens a new session on the user's next request.
		return VerifiedAgent{}, ReasonError(closed, "the broker session is closed")
	case !open:
		return VerifiedAgent{}, credentialDenied("the broker session is closed")
	}
	return VerifiedAgent{Agent: AgentRef{Subject: cred.subject}, AgentID: b.agentID, ExpiresAt: cred.expiresAt}, nil
}
