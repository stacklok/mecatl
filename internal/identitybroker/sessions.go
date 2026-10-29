package identitybroker

import (
	"context"
	"crypto/rand"
	"strings"
	"sync"
	"time"
)

var _ Core = (*Broker)(nil)

// sessionTable holds broker sessions in memory, so they last only as long as
// the process. Session IDs are bs_ and 26 lowercase base32 characters (128
// random bits), like incarnations, so they cannot be guessed and do not repeat
// after a restart. A closed session's record is kept for the life of
// the process, so it keeps refusing with its closing reason and its host
// session gets a new broker session when opened again. The record keeps the
// session's ID, owner, and closing time, as the design requires.
//
// Opens of one root host session run one at a time, under that host
// session's lock in hostLocks, taken before mu. mu guards everything else and
// is never held while AS-1 is called, so other calls do not wait on an
// exchange.
type sessionTable struct {
	mu   sync.Mutex
	open map[string]string // root host session ID -> open broker session ID
	byID map[string]*brokerSession
	// agents holds registered agents by subject, apart from their sessions.
	agents map[string]*agentRecord
	// converted holds the jti of every access token AS-1 may have converted,
	// for any session, with the token's exp; entries are dropped once exp
	// plus convertedClockSkew has passed.
	converted map[string]time.Time
	now       func() time.Time

	hostMu    sync.Mutex
	hostLocks map[string]*hostLock // root host session ID -> its open lock
}

// brokerSession is a broker session's record. Agents registered to the
// session are kept apart from it, so a replacement session can register them
// again with the same identity.
type brokerSession struct {
	Session
	// workload is the whole Workload that opened the session, Verified
	// included, so a verified and an unverified workload with the same name
	// never share it.
	workload Workload
	// closed is the closing reason; empty while the session is open.
	closed Reason
	// closedAt is when the session was closed; zero while it is open.
	closedAt time.Time
	// verifiedOwner is the company identity of the first access token AS-1
	// accepted for the session; nil until then. Session.Owner is the owner
	// mecak8s reported, which pairs the session and which registrations are
	// checked against; an access token's sub_id must equal both.
	verifiedOwner *Owner
	// grant is the refresh token the session holds; nil when it holds none.
	// It is kept past its expiry, which stays the session's hard expiry.
	grant *heldGrant
}

func newSessionTable() *sessionTable {
	return &sessionTable{
		open: map[string]string{}, byID: map[string]*brokerSession{}, agents: map[string]*agentRecord{},
		converted: map[string]time.Time{}, hostLocks: map[string]*hostLock{}, now: time.Now,
	}
}

// OpenSession implements Core. Opens of one host session run one at a time,
// so concurrent opens for it return the same broker session.
//
// With an AuthServer configured, an open whose request carries an access
// token converts it into the session's refresh token, as
// convert describes. An open without one, or without an AuthServer, only
// pairs the session with the reported owner.
func (b *Broker) OpenSession(ctx context.Context, workload Workload, req OpenSessionRequest) (Session, error) {
	if err := checkCall(workload, req); err != nil {
		return Session{}, err
	}
	t := b.sessions
	unlock, err := t.lockHost(ctx, req.HostSessionID)
	if err != nil {
		return Session{}, err
	}
	defer unlock()
	t.mu.Lock()
	// convert releases t.mu around the AS-1 call and takes it again in a
	// defer, so a panic there still unwinds through this Unlock with t.mu
	// held.
	defer t.mu.Unlock()
	s, err := t.pairLocked(workload, req)
	if err != nil {
		return Session{}, err
	}
	if b.as1 == nil || req.AccessToken.IsZero() {
		return t.viewLocked(s), nil
	}
	// A new session stays paired whatever the conversion's outcome, even when
	// check 1 refuses the token: the session is the reported owner's, and a
	// retry with a fresh token finds it.
	return b.convert(ctx, s, req)
}

// pairLocked returns the open broker session of req's host session after
// the workload, owner, and incarnation checks, or opens a new one paired with
// the reported owner. The caller holds t.mu.
//
// An open session past its hard expiry is closed first, so the host session
// gets a new broker session as after any close.
func (t *sessionTable) pairLocked(workload Workload, req OpenSessionRequest) (*brokerSession, error) {
	if id, ok := t.open[req.HostSessionID]; ok && !t.expireLocked(t.byID[id]) {
		s := t.byID[id]
		switch {
		case s.workload != workload:
			// Refusing necessarily reveals that the host session has a broker
			// session.
			return nil, NewError(CodePermissionDenied, "", "the host session cannot be opened by this workload")
		case s.Owner != req.Owner:
			return nil, ReasonError(ReasonSessionOwned, "the host session's broker session belongs to another owner")
		case s.Incarnation != req.Incarnation:
			return nil, NewError(CodeAlreadyExists, "", "the host session has an open broker session for another incarnation")
		}
		return s, nil
	}
	s := &brokerSession{
		Session: Session{
			ID:            newSessionID(),
			HostSessionID: req.HostSessionID,
			Incarnation:   req.Incarnation,
			Owner:         req.Owner,
		},
		workload: workload,
	}
	t.byID[s.ID] = s
	t.open[s.HostSessionID] = s.ID
	return s, nil
}

// viewLocked returns s as callers see it. The caller holds t.mu.
func (t *sessionTable) viewLocked(s *brokerSession) Session {
	view := s.Session
	view.HoldsRefreshToken = s.closed == "" && s.grant != nil && t.now().Before(s.grant.expiresAt)
	return view
}

// closeLocked closes s with reason, unless it is already closed, and
// discards its refresh token. The caller holds t.mu.
func (t *sessionTable) closeLocked(s *brokerSession, reason Reason) {
	if s.closed != "" {
		return
	}
	s.closed = reason
	s.closedAt = t.now()
	s.grant = nil
	delete(t.open, s.HostSessionID)
}

// expireLocked closes s with ReasonSessionClosed if it is open and its
// refresh token has reached its expiry, the session's hard expiry, and
// reports whether s is closed now. A session that holds no refresh token
// never expires. Every lookup of an open session calls it, so the session
// stops working at its hard expiry. The caller holds t.mu.
func (t *sessionTable) expireLocked(s *brokerSession) bool {
	if s.closed == "" && s.grant != nil && !t.now().Before(s.grant.expiresAt) {
		t.closeLocked(s, ReasonSessionClosed)
	}
	return s.closed != ""
}

// FindSession implements Core.
func (b *Broker) FindSession(_ context.Context, workload Workload, req FindSessionRequest) (Session, bool, error) {
	if err := checkCall(workload, req); err != nil {
		return Session{}, false, err
	}
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	id, ok := t.open[req.RootHostSessionID]
	if !ok {
		return Session{}, false, nil
	}
	s := t.byID[id]
	if s.workload != workload {
		// Answer as if there were none, so another workload cannot learn which
		// host sessions exist.
		return Session{}, false, nil
	}
	if t.expireLocked(s) {
		return Session{}, false, nil
	}
	return t.viewLocked(s), true, nil
}

// CloseSession implements Core. Calls already in progress may finish. It
// discards the session's refresh token; an open converting a token for the
// session meanwhile discards the new one.
func (b *Broker) CloseSession(_ context.Context, workload Workload, req CloseSessionRequest) error {
	if err := checkCall(workload, req); err != nil {
		return err
	}
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byID[req.SessionID]
	if !ok || s.workload != workload {
		return unknownSession()
	}
	t.closeLocked(s, req.Reason)
	return nil
}

// AuthorizeCall implements Core. It applies the session checks, then denies
// every call: no call is authorized until per-call grants exist.
func (b *Broker) AuthorizeCall(_ context.Context, workload Workload, req AuthorizeCallRequest) (Authorization, error) {
	if err := checkCall(workload, req); err != nil {
		return Authorization{}, err
	}
	if _, err := b.usableSession(workload, req.SessionID); err != nil {
		return Authorization{}, err
	}
	return Authorization{}, ReasonError(ReasonDenied, "call authorization is not available yet")
}

// usableSession returns the open session for id, or the error for a session
// that does not exist or belongs to another workload (the same error, so the
// two cannot be told apart), or is closed.
func (b *Broker) usableSession(workload Workload, id string) (Session, error) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usableLocked(workload, id)
}

// usableLocked is usableSession for a caller holding t.mu.
func (t *sessionTable) usableLocked(workload Workload, id string) (Session, error) {
	s, ok := t.byID[id]
	switch {
	case !ok || s.workload != workload:
		return Session{}, unknownSession()
	case t.expireLocked(s):
		return Session{}, ReasonError(s.closed, "the broker session is closed")
	}
	return s.Session, nil
}

// newSessionID returns a random broker session ID in the same lowercase
// base32 form as the design's inc_ and ag_ identifiers.
func newSessionID() string {
	return "bs_" + strings.ToLower(rand.Text())
}

// checkCall validates the workload, then the request.
func checkCall(workload Workload, req interface{ Validate() error }) error {
	if err := workload.Validate(); err != nil {
		return err
	}
	return req.Validate()
}

func unknownSession() error {
	return NewError(CodePermissionDenied, "", "unknown broker session")
}
