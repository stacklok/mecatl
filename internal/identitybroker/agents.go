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
// Each instance has its own ag_ identifier, given when its incarnation first
// registers and never reassigned, and its SPIFFE ID is
// spiffe://<trust domain>/agent/<ag_ identifier>: it carries no definition
// name, incarnation, or ancestry, which the record keeps. Registering an
// instance again must keep its owner, host session, definition, and parent.
type agentRecord struct {
	agentID       string
	sessionID     string
	hostSessionID string
	owner         Owner
	incarnation   string
	definition    DefinitionRef
	parent        AgentRef
	// ceiling is the instance's effective ceiling: its definition's, narrowed
	// by its parent's effective ceiling, computed when the instance first
	// registers and never recomputed or widened.
	ceiling Ceiling
	// credentials holds the credentials issued since the instance last moved
	// to another broker session, by SHA-256 of the token, with their expiry;
	// at most maxIssuedCredentials. Only these verify, so reopening a session
	// does not revive credentials issued before it closed, however close
	// together the two were issued. Expired entries are dropped when the
	// instance registers again.
	credentials map[[sha256.Size]byte]time.Time
}

// agentIdentity is an instance's ag_ identifier and the SPIFFE ID built from
// it. The zero value is no identity.
type agentIdentity struct {
	id      string
	subject string
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

// registerAttempts bounds RegisterAgent's attempts. An attempt fails only when
// another registration gave the same incarnation its identity while this one
// was signing, and identities are never reassigned, so the second attempt
// signs for the identity the first recorded.
const registerAttempts = 2

// RegisterAgent implements Core. It checks the session, the owner, the root
// host session or the parent and its host session, and the definition or an earlier registration of
// the instance, issues the credential, and only then records the instance in
// the session, so a failure changes nothing. A new instance's definition must
// be one the broker loaded; an instance registered before keeps the
// definition it registered with, whatever the broker loaded since.
//
// Shortcut: a definition the broker did not load is refused, not accepted as reported by mecak8s, and there is no root eligibility or nesting check; removed by #2551.
//
// Shortcut: a registration, and a refused one, records no lineage event; removed by #2541.
func (b *Broker) RegisterAgent(_ context.Context, workload Workload, req RegisterAgentRequest) (Registration, error) {
	if err := checkCall(workload, req); err != nil {
		return Registration{}, err
	}
	t := b.sessions
	for range registerAttempts {
		identity, err := b.registrationIdentity(workload, req)
		if err != nil {
			return Registration{}, err
		}

		// Sign outside the lock: a remote signer may be slow.
		credential, expiresAt, err := b.issuer.issue(identity.subject, t.now())
		if err != nil {
			// The signer's error is not passed on: from a key service it can
			// name keys or endpoints, and the broker has no log of its own.
			return Registration{}, NewError(CodeInternal, "", "the agent credential could not be issued")
		}

		reg, retry, err := b.recordRegistration(workload, req, identity, credential, expiresAt)
		if err != nil || !retry {
			return reg, err
		}
	}
	return Registration{}, NewError(CodeAborted, "", "the agent registration raced another registration of the same agent")
}

// registrationIdentity checks req under t.mu and returns the identity to sign
// for: the instance's own if it registered before, otherwise a new one.
func (b *Broker) registrationIdentity(workload Workload, req RegisterAgentRequest) (agentIdentity, error) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	_, identity, err := b.checkRegistrationLocked(workload, req)
	if err != nil || identity != (agentIdentity{}) {
		return identity, err
	}
	id := newAgentID()
	return agentIdentity{id: id, subject: b.issuer.subject(id)}, nil
}

// recordRegistration checks req again under t.mu, since the session or the
// instance may have changed while signing, and records the registration with
// identity. It reports retry, recording nothing, when identity is no longer
// the one to record: another registration gave the incarnation an identity
// first, or a new identity is already taken.
func (b *Broker) recordRegistration(workload Workload, req RegisterAgentRequest, identity agentIdentity, credential Token, expiresAt time.Time) (reg Registration, retry bool, err error) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	s, recorded, err := b.checkRegistrationLocked(workload, req)
	if err != nil {
		return Registration{}, false, err
	}
	if recorded != identity {
		if recorded != (agentIdentity{}) {
			return Registration{}, true, nil
		}
		if _, taken := t.agents[identity.subject]; taken {
			return Registration{}, true, nil
		}
	}
	t.recordLocked(identity, s.ID, req, b.effectiveCeilingLocked(req), credential, expiresAt)
	return Registration{Agent: AgentRef{Subject: identity.subject}, Credential: credential, ExpiresAt: expiresAt}, false, nil
}

// effectiveCeilingLocked returns the effective ceiling of a new instance
// registering with req: its definition's ceiling, intersected with its
// parent's effective ceiling when it has a parent. The caller holds t.mu and
// has checked req, so the definition is loaded and the parent registered.
func (b *Broker) effectiveCeilingLocked(req RegisterAgentRequest) Ceiling {
	ceiling := b.definitions[req.Definition].Ceiling
	if req.Parent.IsZero() {
		return ceiling
	}
	return ceiling.intersect(b.sessions.agents[req.Parent.Subject].ceiling)
}

// recordLocked records a registration of identity in session sessionID, with
// the credential just issued, which always verifies afterwards. A new
// instance gets ceiling as its effective ceiling; one registered before
// keeps its own. The caller holds t.mu.
func (t *sessionTable) recordLocked(identity agentIdentity, sessionID string, req RegisterAgentRequest, ceiling Ceiling, credential Token, expiresAt time.Time) {
	rec, ok := t.agents[identity.subject]
	switch {
	case !ok:
		rec = &agentRecord{
			agentID:       identity.id,
			hostSessionID: req.HostSessionID,
			owner:         req.Owner,
			incarnation:   req.Incarnation,
			definition:    req.Definition,
			parent:        req.Parent,
			ceiling:       ceiling,
		}
		t.agents[identity.subject] = rec
		t.incarnations[req.Incarnation] = identity.subject
	case rec.sessionID == sessionID:
		// Registering again in the same session keeps the credentials issued
		// there, dropping expired ones and, past maxIssuedCredentials, the
		// earliest to expire.
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

// checkRegistrationLocked returns the usable session for req after the
// owner, root host session or parent and its host session, and definition
// or re-registration checks, and the identity of the instance if its incarnation registered
// before. An instance registered before, in any session, must keep its owner,
// host session, definition, and parent, and is not checked against the
// definitions loaded now (design check 7 replaces check 3); only its broker
// session may change. A new instance's definition must be one the broker
// loaded (check 3). The caller holds t.mu.
func (b *Broker) checkRegistrationLocked(workload Workload, req RegisterAgentRequest) (Session, agentIdentity, error) {
	t := b.sessions
	s, err := t.usableLocked(workload, req.SessionID)
	if err != nil {
		return Session{}, agentIdentity{}, err
	}
	if req.Owner != s.Owner {
		return Session{}, agentIdentity{}, NewError(CodePermissionDenied, "", "the registration's owner is not the broker session's owner")
	}
	if req.Parent.IsZero() {
		if req.HostSessionID != s.HostSessionID {
			return Session{}, agentIdentity{}, NewError(CodePermissionDenied, "", "an agent with no parent must run in the broker session's root host session")
		}
		// The main agent's incarnation is its host session's, so a stale one
		// (from an earlier session under the same host session ID) cannot
		// move that session's agent and identity into this one.
		if req.Incarnation != s.Incarnation {
			return Session{}, agentIdentity{}, NewError(CodePermissionDenied, "", "an agent with no parent must have the root host session's incarnation")
		}
	} else if parent, ok := t.agents[req.Parent.Subject]; !ok || parent.sessionID != s.ID {
		return Session{}, agentIdentity{}, NewError(CodePermissionDenied, "", "the parent agent is not registered to the broker session")
	} else if req.ParentHostSessionID != parent.hostSessionID {
		// Each child is tied to its parent's host session, and the parent to
		// its own, so the chain ends at the root host session checked above.
		return Session{}, agentIdentity{}, NewError(CodePermissionDenied, "", "the reported parent host session is not the parent agent's host session")
	}
	if subject, ok := t.incarnations[req.Incarnation]; ok {
		rec := t.agents[subject]
		if rec.owner != req.Owner || rec.hostSessionID != req.HostSessionID || rec.definition != req.Definition || rec.parent != req.Parent {
			return Session{}, agentIdentity{}, NewError(CodeAlreadyExists, "", "the agent is registered with a different owner, host session, definition, or parent")
		}
		return s, agentIdentity{id: rec.agentID, subject: subject}, nil
	}
	if _, ok := b.definitions[req.Definition]; !ok {
		return Session{}, agentIdentity{}, NewError(CodePermissionDenied, "", "the definition is not one the broker loaded")
	}
	return s, agentIdentity{}, nil
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
// the session; the broker's per-operation checks do.
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
	var agentID string
	if bound {
		_, current = rec.credentials[credentialHash(req.Credential)]
		agentID = rec.agentID
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
		return VerifiedAgent{}, deniedError("the credential's subject is not a registered agent")
	case !bound:
		return VerifiedAgent{}, deniedError("the agent is not registered to the broker session")
	case !current:
		return VerifiedAgent{}, deniedError("the credential was not issued since the agent moved to the broker session")
	case !open && closed != "":
		// The design reports a closed session by its closing reason, so the
		// caller opens a new session on the user's next request.
		return VerifiedAgent{}, ReasonError(closed, "the broker session is closed")
	case !open:
		return VerifiedAgent{}, deniedError("the broker session is closed")
	}
	return verifiedAgent(cred, agentID), nil
}

// verifiedAgent is the agent cred, a verified credential, identifies:
// the registered instance with ag_ identifier agentID, whose SPIFFE ID is
// cred's subject and becomes act.sub.
func verifiedAgent(cred verifiedCredential, agentID string) VerifiedAgent {
	return VerifiedAgent{
		Agent:     AgentRef{Subject: cred.subject},
		AgentID:   agentID,
		ActorID:   cred.subject,
		ExpiresAt: cred.expiresAt,
	}
}

// ActorIssuer is the act.iss naming the agents of trustDomain; changing it
// renames every agent.
func ActorIssuer(trustDomain string) string { return "spiffe://" + trustDomain }

// VerifyCredentialForOwner implements CredentialVerifier. After the
// credential's own checks, its subject must be a registered instance whose
// present session is open and owned by req.Owner, and the credential must
// have been issued since the instance moved to that session, so a credential
// from before a session closed does not verify once the instance registers
// into the session that replaces it. The owner compared is the session's
// Owner, which its registrations are checked against, not its verified
// owner: the broker's per-operation checks put the agent in the session whose
// refresh token AS-1 exchanges, and that session's verified owner equals its
// Owner. Every refusal after the credential's own checks has the same detail,
// so an error passed on whole tells AS-1 nothing about the session's state.
func (b *Broker) VerifyCredentialForOwner(_ context.Context, req VerifyCredentialForOwnerRequest) (VerifiedAgent, error) {
	if req.Credential.IsZero() || req.Owner.validate() != nil {
		return VerifiedAgent{}, deniedError("the request has no credential or no valid owner")
	}
	t := b.sessions
	cred, err := b.issuer.verify(req.Credential, t.now())
	if err != nil {
		return VerifiedAgent{}, err
	}
	t.mu.Lock()
	rec, registered := t.agents[cred.subject]
	var current, open, owned bool
	var agentID string
	if registered {
		agentID = rec.agentID
		_, current = rec.credentials[credentialHash(req.Credential)]
		if s, ok := t.byID[rec.sessionID]; ok {
			t.expireLocked(s)
			open = s.closed == ""
			owned = s.Owner == req.Owner
		}
	}
	t.mu.Unlock()
	if !registered || !owned || !current || !open {
		return VerifiedAgent{}, deniedError("the credential is not current for an open broker session of the owner")
	}
	return verifiedAgent(cred, agentID), nil
}
