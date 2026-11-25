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
// session's lock in opening, taken before mu. mu guards everything else and
// is never held while AS-1 is called, so other calls do not wait on an
// exchange.
type sessionTable struct {
	mu   tableMutex
	open map[string]string // root host session ID -> open broker session ID
	byID map[string]*brokerSession
	// agents holds registered agents by subject, apart from their sessions.
	agents map[string]*agentRecord
	// incarnations holds each registered incarnation's subject. An
	// incarnation keeps its subject, so its identity, for good.
	incarnations map[string]string
	// converted holds the jti of every access token AS-1 may have converted,
	// for any session, with the token's exp; entries are dropped once exp
	// plus skew has passed.
	converted map[string]time.Time
	// skew is Config.ClockSkew.
	skew time.Duration
	// attempts holds the attempts AuthorizeCall issued, per session, until
	// the session closes.
	attempts *attemptLedger
	// runs holds each session's run and delegation records until the session
	// closes.
	runs *runLedger
	now  func() time.Time

	// opening serializes opens of one root host session.
	opening keyedLocks
	// registering serializes registrations of one incarnation, and reporting
	// delegation reports to one broker session, so each check, record, and
	// store runs as one step for them: a record is sent between the check and
	// the store, outside mu.
	registering, reporting keyedLocks
}

// tableMutex is sessionTable's mutex. A close takes effect while it is held;
// closeLocked queues the close's record, and Unlock sends the queued records
// after releasing the lock, so the sink is never called under it and every
// close path, including a lookup that finds a session past its hard expiry,
// is recorded.
type tableMutex struct {
	sync.Mutex
	pending []Record
	// deliver sends a queued record; nil drops them.
	deliver func(Record)
}

// Unlock releases m, then sends the records queued while it was held.
func (m *tableMutex) Unlock() {
	pending := m.pending
	m.pending = nil
	m.Mutex.Unlock()
	if m.deliver == nil {
		return
	}
	for _, rec := range pending {
		m.deliver(rec)
	}
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
	// openRecord is the AuditID of the open that created the session.
	openRecord string
	// user is the user AS-1 accepted for the session, with the open that
	// recorded it; nil until then. It is kept with verifiedOwner, whose
	// owner it holds.
	user *verifiedUser
}

// verifiedUser is a session's verified user and the AuditID of the open
// record that verified them.
type verifiedUser struct {
	User
	openRecord string
}

func newSessionTable(skew time.Duration) *sessionTable {
	return &sessionTable{
		open: map[string]string{}, byID: map[string]*brokerSession{}, agents: map[string]*agentRecord{},
		incarnations: map[string]string{}, skew: skew,
		converted: map[string]time.Time{}, attempts: newAttemptLedger(), runs: newRunLedger(), now: time.Now,
		opening: keyedLocks{locks: map[string]*hostLock{}}, registering: keyedLocks{locks: map[string]*hostLock{}}, reporting: keyedLocks{locks: map[string]*hostLock{}},
	}
}

// OpenSession implements Core. Opens of one host session run one at a time,
// so concurrent opens for it return the same broker session.
//
// With an AuthServer configured, an open whose request carries an access
// token converts it into the session's refresh token, as
// convert describes. An open without one, or without an AuthServer, only
// pairs the session with the reported owner.
//
// Every open is recorded (credentials-and-lifecycle.md step (3),
// "Evidence"). A successful open returns only once its record is accepted;
// otherwise it fails with CodeUnavailable and changes nothing: a session it
// created does not stay open, and a refresh token it got is discarded, not
// stored. A refused open is returned whether or not its record is accepted,
// and a session it created does not stay open without one.
func (b *Broker) OpenSession(ctx context.Context, workload Workload, req OpenSessionRequest) (Session, error) {
	if err := checkCall(workload, req); err != nil {
		return Session{}, err
	}
	t := b.sessions
	rec := SessionOpenRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeSessionOpen, At: t.now()},
		Workload:     workload, HostSessionID: req.HostSessionID, Incarnation: req.Incarnation,
		ReportedOwner: req.Owner, TokenPresented: !req.AccessToken.IsZero(),
	}
	unlock, err := t.lockHost(ctx, req.HostSessionID)
	if err != nil {
		return Session{}, b.refuseOpen(ctx, &rec, nil, err)
	}
	defer unlock()
	s, pending, err := b.open(ctx, workload, req, &rec)
	if err != nil {
		return Session{}, b.refuseOpen(ctx, &rec, s, err)
	}
	rec.Outcome = OutcomeSuccess
	if err := b.record(ctx, &rec); err != nil {
		// Shortcut: a refresh token the open got is discarded, not revoked; removed by #2568 (refresh-token revocation).
		t.mu.Lock()
		if rec.NewSession {
			t.closeLocked(s, ReasonSessionClosed, CloseOpenNotRecorded)
		}
		t.mu.Unlock()
		return Session{}, errNotRecorded("session open")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Only a close can have changed s while the record was sent: opens of
	// its host session wait for the open lock this call holds.
	if s.closed != "" {
		return Session{}, ReasonError(s.closed, "the broker session closed during the open")
	}
	if pending != nil {
		// One live refresh token per session: a new one replaces, and
		// discards, any held before.
		owner := pending.owner
		s.verifiedOwner = &owner
		s.user = &verifiedUser{User: pending.user, openRecord: rec.AuditID}
		s.grant = pending
	}
	return t.viewLocked(s), nil
}

// open pairs the session and, when req carries an access token the broker
// converts, runs the conversion. It fills rec with what it learns and
// returns the session and the refresh token to store once rec is accepted;
// nil when the open stores none. A refusal returns the session when the open
// reached one. It takes t.mu and releases it before returning.
func (b *Broker) open(ctx context.Context, workload Workload, req OpenSessionRequest, rec *SessionOpenRecord) (*brokerSession, *heldGrant, error) {
	t := b.sessions
	t.mu.Lock()
	// convert releases t.mu around the AS-1 call and takes it again in a
	// defer, so a panic there still unwinds through this Unlock with t.mu
	// held.
	defer t.mu.Unlock()
	s, err := t.pairLocked(workload, req, rec)
	if err != nil {
		return nil, nil, err
	}
	if b.as1 == nil || req.AccessToken.IsZero() {
		rec.Kind = OpenTokenless
		rec.setSessionUser(s)
		return s, nil, nil
	}
	// A new session stays paired whatever the conversion's outcome, even when
	// check 1 refuses the token: the session is the reported owner's, and a
	// retry with a fresh token finds it.
	pending, err := b.convert(ctx, s, req, rec)
	return s, pending, err
}

// setSessionUser records s's verified user, from the open that verified it,
// when an earlier open did.
func (r *SessionOpenRecord) setSessionUser(s *brokerSession) {
	if s.user != nil {
		u := s.user.User
		r.User, r.UserRecord = &u, s.user.openRecord
	}
}

// refuseOpen records err as the refusal of rec's open of s, nil when the
// open reached no session, and returns err. A session the open created does
// not stay open when the refusal's record is not accepted.
func (b *Broker) refuseOpen(ctx context.Context, rec *SessionOpenRecord, s *brokerSession, err error) error {
	rec.refused(err)
	rec.Kind = ""
	if b.record(context.WithoutCancel(ctx), rec) != nil && rec.NewSession && s != nil {
		t := b.sessions
		t.mu.Lock()
		t.closeLocked(s, ReasonSessionClosed, CloseOpenNotRecorded)
		t.mu.Unlock()
	}
	return err
}

// pairLocked returns the open broker session of req's host session after
// the workload, owner, and incarnation checks, or opens a new one paired with
// the reported owner, and notes which in rec. The caller holds t.mu.
//
// An open session past its hard expiry is closed first, so the host session
// gets a new broker session as after any close.
func (t *sessionTable) pairLocked(workload Workload, req OpenSessionRequest, rec *SessionOpenRecord) (*brokerSession, error) {
	if id, ok := t.open[req.HostSessionID]; ok && !t.expireLocked(t.byID[id]) {
		s := t.byID[id]
		rec.SessionID, rec.SessionRecord = s.ID, s.openRecord
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
		workload:   workload,
		openRecord: rec.AuditID,
	}
	t.byID[s.ID] = s
	t.open[s.HostSessionID] = s.ID
	rec.SessionID, rec.NewSession = s.ID, true
	return s, nil
}

// viewLocked returns s as callers see it. The caller holds t.mu.
func (t *sessionTable) viewLocked(s *brokerSession) Session {
	view := s.Session
	view.HoldsRefreshToken = s.closed == "" && s.grant != nil && !t.pastHardExpiry(s.grant)
	return view
}

// closeLocked closes s with reason for cause, unless it is already closed,
// and discards its refresh token, issued attempts, and run records. The close
// takes effect whether or not its record is accepted; the record is sent
// when t.mu is released. The caller holds t.mu.
func (t *sessionTable) closeLocked(s *brokerSession, reason Reason, cause CloseCause) {
	if s.closed != "" {
		return
	}
	s.closed = reason
	s.closedAt = t.now()
	s.grant = nil
	t.attempts.dropLocked(s.ID)
	t.runs.dropLocked(s.ID)
	delete(t.open, s.HostSessionID)
	userRecord, user := sessionUser(s)
	t.mu.pending = append(t.mu.pending, &SessionCloseRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeSessionClose, At: s.closedAt, Outcome: OutcomeSuccess},
		SessionID:    s.ID, HostSessionID: s.HostSessionID, Workload: s.workload, Owner: s.Owner,
		ClosingReason: reason, Cause: cause, SessionRecord: s.openRecord, UserRecord: userRecord, User: user,
	})
}

// pastHardExpiry reports whether g's expiry, the session's hard expiry, has
// been reached, counting it one clock skew early so the broker never presents
// a grant AS-1 may already have expired (design step (6), check 2). Skew in
// either direction fails closed: the session ends early instead.
func (t *sessionTable) pastHardExpiry(g *heldGrant) bool {
	return !t.now().Add(t.skew).Before(g.expiresAt)
}

// expireLocked closes s with ReasonSessionClosed if it is open and its
// refresh token has reached the session's hard expiry (see pastHardExpiry),
// and reports whether s is closed now. A session that holds no refresh token
// never expires. Every lookup of an open session calls it, so the session
// stops working at its hard expiry. The caller holds t.mu.
func (t *sessionTable) expireLocked(s *brokerSession) bool {
	if s.closed == "" && s.grant != nil && t.pastHardExpiry(s.grant) {
		t.closeLocked(s, ReasonSessionClosed, CloseHardExpiry)
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

// CloseSession implements Core. A call already in progress is refused with
// the closing reason at its next session check and gets no gateway token,
// even if AS-2 has already issued one. CloseSession discards the session's
// refresh token; an open converting a token for the session meanwhile
// discards the new one.
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
	t.closeLocked(s, req.Reason, CloseRequested)
	return nil
}

// usableLocked returns the open session for id, or the error for a session
// that does not exist or belongs to another workload (the same error, so the
// two cannot be told apart), or is closed. The caller holds t.mu.
func (t *sessionTable) usableLocked(workload Workload, id string) (Session, error) {
	s, err := t.callSessionLocked(workload, id)
	if err != nil {
		return Session{}, err
	}
	return s.Session, nil
}

// callSessionLocked returns workload's session id and nil while it is open.
// It returns the session record with its closing reason as the error when
// the session is closed, so the refusal can be recorded against it, and nil
// with the unknown-session error when no session id exists for workload. The
// caller holds t.mu.
func (t *sessionTable) callSessionLocked(workload Workload, id string) (*brokerSession, error) {
	s, ok := t.byID[id]
	if !ok || s.workload != workload {
		return nil, unknownSession()
	}
	return s, t.closedLocked(s)
}

// closedLocked returns s's closing reason as an error if s is closed, first
// closing s if it has reached its hard expiry (see expireLocked). The caller
// holds t.mu.
func (t *sessionTable) closedLocked(s *brokerSession) error {
	if t.expireLocked(s) {
		return ReasonError(s.closed, "the broker session is closed")
	}
	return nil
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
