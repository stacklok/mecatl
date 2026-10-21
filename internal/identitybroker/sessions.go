package identitybroker

import (
	"context"
	"crypto/rand"
	"sync"
	"time"
)

var _ Core = (*Broker)(nil)

// sessionTable holds broker sessions in memory, so they last only as long as
// the process. Session IDs are random, so they cannot be guessed and do not
// repeat after a restart. A closed session's record is kept for the life of
// the process, so it keeps refusing with its closing reason and its host
// session gets a new broker session when opened again. The record keeps the
// session's ID, owner, and closing time, as the design requires.
type sessionTable struct {
	mu   sync.Mutex
	open map[string]string // root host session ID -> open broker session ID
	byID map[string]*brokerSession
	now  func() time.Time
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
}

func newSessionTable() *sessionTable {
	return &sessionTable{open: map[string]string{}, byID: map[string]*brokerSession{}, now: time.Now}
}

// OpenSession implements Core. Opens run one at a time, so concurrent opens
// for one host session return the same broker session.
func (b *Broker) OpenSession(_ context.Context, workload Workload, req OpenSessionRequest) (Session, error) {
	if err := checkCall(workload, req); err != nil {
		return Session{}, err
	}
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	if id, ok := t.open[req.HostSessionID]; ok {
		s := t.byID[id]
		switch {
		case s.workload != workload:
			// Refusing necessarily reveals that the host session has a broker
			// session.
			return Session{}, NewError(CodePermissionDenied, "", "the host session cannot be opened by this workload")
		case s.Owner != req.Owner:
			return Session{}, ReasonError(ReasonSessionOwned, "the host session's broker session belongs to another owner")
		case s.Incarnation != req.Incarnation:
			return Session{}, NewError(CodeAlreadyExists, "", "the host session has an open broker session for another incarnation")
		}
		return s.Session, nil
	}
	s := &brokerSession{
		Session: Session{
			ID:            "bs_" + rand.Text(),
			HostSessionID: req.HostSessionID,
			Incarnation:   req.Incarnation,
			Owner:         req.Owner,
		},
		workload: workload,
	}
	t.byID[s.ID] = s
	t.open[s.HostSessionID] = s.ID
	return s.Session, nil
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
	return s.Session, true, nil
}

// CloseSession implements Core. Calls already in progress may finish.
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
	if s.closed == "" {
		s.closed = req.Reason
		s.closedAt = t.now()
		delete(t.open, s.HostSessionID)
	}
	return nil
}

// RegisterAgent implements Core. It applies the session and owner checks;
// registration itself is not built yet, so a request that passes them fails.
func (b *Broker) RegisterAgent(_ context.Context, workload Workload, req RegisterAgentRequest) (Registration, error) {
	if err := checkCall(workload, req); err != nil {
		return Registration{}, err
	}
	s, err := b.usableSession(workload, req.SessionID)
	if err != nil {
		return Registration{}, err
	}
	if req.Owner != s.Owner {
		return Registration{}, NewError(CodePermissionDenied, "", "the registration's owner is not the broker session's owner")
	}
	return Registration{}, NewError(CodeInternal, "", "agent registration is not available yet")
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
	s, ok := t.byID[id]
	switch {
	case !ok || s.workload != workload:
		return Session{}, unknownSession()
	case s.closed != "":
		return Session{}, ReasonError(s.closed, "the broker session is closed")
	}
	return s.Session, nil
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
