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
	mu   sync.Mutex
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
	// reports holds the attempts AuthorizeCall issued for tools/calls, per
	// session, and what the MCP client reported of each; those whose report
	// is not settled are kept after the session closes.
	reports *reportLedger
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
	// queue sends the records of closes and refusals, which take effect
	// whether or not their record is accepted. A close takes effect under
	// mu, and closeLocked queues its record there, so every close path,
	// including a lookup that finds a session past its hard expiry, is
	// recorded in the order closes happen, and no caller waits on the sink
	// for it. nil drops them.
	queue *recordQueue
	// newOpens holds, by root host session, the new broker session an open
	// is creating, which is not in the table until its open record is
	// accepted. FindSession of such a host session waits for that open, and
	// a close of the host session closes it, so the open fails when it ends.
	newOpens map[string]*brokerSession
	// hostClosed holds when each host session incarnation was closed by
	// host session, per workload, for hostClosedLifetime, oldest first;
	// pairLocked gives a marked incarnation no new broker session.
	hostClosed hostClosedMarks
}

// hostClosedLifetime is how long a close by host session keeps refusing new
// broker sessions for that host session's closed incarnation. It covers the opens that started
// before the close: one queued behind the open the close ended, or one whose
// caller loaded the host session before it was deleted. An open holds its
// host session's open lock for at most two AS-1 requests (the exchange and its
// one resend, each within MaxAS1RequestTimeout) and one record send
// (recordTimeout), 70 seconds; five minutes covers a few opens queued in turn
// and the caller's own steps before it calls the broker. A longer mark only
// refuses opens of a host session that no longer exists, and costs one entry
// per close.
//
// Shortcut: the mark is in memory, per replica, and time-limited, so an open on a replica that did not serve the close, after a restart, or more than hostClosedLifetime after the close still gets a broker session nothing closes before its hard expiry; removed by #2805 (more than one replica) and #2563 (durable state).
const hostClosedLifetime = 5 * time.Minute

// hostClosedKey is an incarnation of a workload's root host session. The
// incarnation keeps a mark from refusing a later create of the same host
// session ID, which mints a new incarnation.
type hostClosedKey struct {
	workload    Workload
	host        string
	incarnation string
}

// hostClosedMarks holds the hosts closed in the last hostClosedLifetime.
// order lists every mark in the order made, so expired ones are pruned from
// the front; times come from one clock that does not run back, so that order
// is also by time. The caller holds t.mu.
type hostClosedMarks struct {
	at    map[hostClosedKey]time.Time
	order []hostClosedMark
}

// hostClosedMark is one close of a host session.
type hostClosedMark struct {
	key hostClosedKey
	at  time.Time
}

// mark notes key closed at now, after pruning expired marks.
func (m *hostClosedMarks) mark(key hostClosedKey, now time.Time) {
	m.prune(now)
	if m.at == nil {
		m.at = map[hostClosedKey]time.Time{}
	}
	m.at[key] = now
	m.order = append(m.order, hostClosedMark{key: key, at: now})
}

// marked reports whether key was closed less than hostClosedLifetime before
// now.
func (m *hostClosedMarks) marked(key hostClosedKey, now time.Time) bool {
	m.prune(now)
	_, ok := m.at[key]
	return ok
}

// prune drops the marks hostClosedLifetime old at now. A host closed again
// keeps its later mark.
func (m *hostClosedMarks) prune(now time.Time) {
	for len(m.order) > 0 && now.Sub(m.order[0].at) >= hostClosedLifetime {
		first := m.order[0]
		if m.at[first.key].Equal(first.at) {
			delete(m.at, first.key)
		}
		m.order[0] = hostClosedMark{}
		m.order = m.order[1:]
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
	// heldClose is the record of a close made while the session's open was
	// in progress, by that open or, for a new session, by a close of its host
	// session. refuseOpen queues it after the open's refusal record, which it
	// refers to; nil otherwise. t.mu guards it.
	heldClose *SessionCloseRecord
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
		converted: map[string]time.Time{}, attempts: newAttemptLedger(), reports: newReportLedger(), runs: newRunLedger(), now: time.Now,
		opening: keyedLocks{locks: map[string]*keyedLock{}}, registering: keyedLocks{locks: map[string]*keyedLock{}}, reporting: keyedLocks{locks: map[string]*keyedLock{}},
		newOpens: map[string]*brokerSession{},
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
// "Evidence"). A successful open returns only once its record is accepted.
// Otherwise it changes nothing: a session it created is closed, a refresh
// token the session held stays, a refresh token it got is discarded, and the
// broker still remembers the source jti it converted. It then fails with
// ReasonRecordNotAccepted: with CodeUnauthenticated when it converted the
// request's token, which is spent, and CodeUnavailable otherwise. A refused
// open is returned at once, its record queued, and never leaves a session it
// created open.
func (b *Broker) OpenSession(ctx context.Context, workload Workload, req OpenSessionRequest) (Session, error) {
	if err := checkCall(workload, req); err != nil {
		return Session{}, b.refuseInvalid(&SessionOpenRecord{RecordHeader: b.newHeader(TypeSessionOpen), Workload: workload}, err)
	}
	t := b.sessions
	rec := SessionOpenRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeSessionOpen, At: t.now()},
		Workload:     workload, HostSessionID: req.HostSessionID, Incarnation: req.Incarnation,
		ReportedOwner: req.Owner, TokenPresented: !req.AccessToken.IsZero(),
	}
	unlock, err := t.opening.lock(ctx, req.HostSessionID, "the session open was cancelled")
	if err != nil {
		return Session{}, b.refuseOpen(&rec, nil, err)
	}
	defer unlock()
	// A lookup of the host session waits for an open creating a new session
	// until it ends, refused or not.
	defer func() {
		t.mu.Lock()
		delete(t.newOpens, req.HostSessionID)
		t.mu.Unlock()
	}()
	s, pending, err := b.open(ctx, workload, req, &rec)
	if err != nil {
		return Session{}, b.refuseOpen(&rec, s, err)
	}
	// A close of the host session may have closed s since b.open released
	// t.mu. Its record is held for a new s, so the open is refused, not
	// recorded as a success its close would follow.
	t.mu.Lock()
	closed := s.closed
	t.mu.Unlock()
	if closed != "" {
		return Session{}, b.refuseOpen(&rec, s, ReasonError(closed, "the broker session closed during the open"))
	}
	rec.Outcome = OutcomeSuccess
	if err := b.record(ctx, &rec); err != nil {
		// The open is undone: pending is dropped, and refuseOpen closes a
		// session it created. convert already noted a converted jti, so the
		// spent token is never presented to AS-1 again. The sink may have
		// kept the record without confirming it, so the refusal names it.
		//
		// Shortcut: a refresh token the open got is discarded, not revoked; removed by #2568 (refresh-token revocation).
		refused := rec
		refused.RecordHeader = rec.supersede()
		return Session{}, b.refuseOpen(&refused, s, errOpenNotRecorded(rec.Kind == OpenConverted))
	}
	t.mu.Lock()
	// Only a close can have changed s while the record was sent: opens of
	// its host session wait for the open lock this call holds. A new s is
	// not in the table yet, but a close of its host session closes it
	// (newOpens), as when the host session is deleted, and holds its record.
	// The open is then recorded again as refused, so the records show it was
	// not honoured and any refresh token it got was not stored, and the
	// close follows that refusal.
	if s.closed != "" {
		err := ReasonError(s.closed, "the broker session closed during the open")
		t.mu.Unlock()
		refused := rec
		refused.RecordHeader, refused.NewSession = rec.supersede(), false
		return Session{}, b.refuseOpen(&refused, s, err)
	}
	defer t.mu.Unlock()
	if rec.NewSession {
		// Only now, with its open record accepted, can a registration, a
		// report, a call, or a close by session ID reach the session, so no
		// accepted record refers to an open the sink did not accept. A close
		// of its host session meanwhile closed s, which the check above
		// caught. The host session has no other open session: opens of it
		// wait for the open lock this call holds.
		t.byID[s.ID] = s
		t.open[s.HostSessionID] = s.ID
		// From here a close of the host session finds s in the table, so
		// queues its record rather than holding it.
		delete(t.newOpens, s.HostSessionID)
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
	// A refused conversion returns the session, so that refuseOpen closes it
	// if this open created it.
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

// refuseOpen queues rec as the refusal of rec's open of s, nil when the
// open reached no session, and returns err. A refused open never leaves a
// session: one it created is closed, whatever happens to its record, and the
// close, queued after the refusal, names it (audit.md, "Write order and
// failure"). Nothing here waits on the sink.
func (b *Broker) refuseOpen(rec *SessionOpenRecord, s *brokerSession, err error) error {
	rec.refused(err)
	// A refusal stores nothing, so it names no user, expiry, or replaced
	// refresh token.
	rec.Kind, rec.User, rec.UserRecord, rec.HardExpiry, rec.Replaced = "", nil, "", time.Time{}, nil
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	// The refusal and the close that follows it are queued together, whole
	// or not at all, so a close never names a refusal the queue dropped.
	recs := []Record{rec}
	if s != nil {
		recs = append(recs, t.closeRefusedLocked(rec, s)...)
	}
	t.queueRecordLocked(recs...)
	return err
}

// closeRefusedLocked returns the records that follow rec, the refusal of an
// open of s: the close convert or a close of the host session held back
// during the open, and the close of s when the open was creating it. The
// caller holds t.mu.
func (t *sessionTable) closeRefusedLocked(rec *SessionOpenRecord, s *brokerSession) []Record {
	var recs []Record
	if rec.NewSession && t.newOpens[s.HostSessionID] == s {
		// From here a close of the host session finds s closed.
		delete(t.newOpens, s.HostSessionID)
	}
	if held := s.heldClose; held != nil {
		// convert, or a close of the host session, closed s during the open
		// and held its record back, so it follows the refusal. A new
		// session's open record is the refusal, which the close names; no
		// record the sink was not given precedes it.
		s.heldClose = nil
		if rec.NewSession {
			held.SessionRecord = rec.AuditID
		}
		recs = append(recs, held)
	}
	if rec.NewSession {
		if s.closed == "" {
			// The session's open record is the refusal now.
			s.openRecord = rec.AuditID
		}
		if closed := t.closeHeldLocked(s, ReasonSessionClosed, CloseOpenRefused); closed != nil {
			recs = append(recs, closed)
		}
	}
	return recs
}

// pairLocked returns the open broker session of req's host session after
// the workload, owner, and incarnation checks, or a new one paired with the
// reported owner, not yet in the table, and notes which in rec. The caller
// holds t.mu.
//
// An open session past its hard expiry is closed first, so the host session
// gets a new broker session as after any close.
func (t *sessionTable) pairLocked(workload Workload, req OpenSessionRequest, rec *SessionOpenRecord) (*brokerSession, error) {
	if t.hostClosed.marked(hostClosedKey{workload: workload, host: req.HostSessionID, incarnation: req.Incarnation}, t.now()) {
		// This incarnation of the host session was closed by host session,
		// as when it is deleted: an open that started before then gets no
		// new session, so converts nothing and stores nothing.
		return nil, ReasonError(ReasonSessionClosed, "the host session was closed")
	}
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
	// OpenSession adds s to the table once its open record is accepted.
	t.newOpens[req.HostSessionID] = s
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
// and discards its refresh token, issued attempts, settled call reports, and
// run records. The close
// takes effect whether or not its record is accepted; the record is queued on
// t.queue. The caller holds t.mu.
func (t *sessionTable) closeLocked(s *brokerSession, reason Reason, cause CloseCause) {
	if rec := t.closeHeldLocked(s, reason, cause); rec != nil {
		t.queueRecordLocked(rec)
	}
}

// holdCloseLocked closes s as closeLocked does but keeps the record in
// s.heldClose for refuseOpen to queue after the open's refusal. It keeps a
// record already held: s closes once, so that one is s's close. The caller
// holds t.mu.
func (t *sessionTable) holdCloseLocked(s *brokerSession, reason Reason, cause CloseCause) {
	if rec := t.closeHeldLocked(s, reason, cause); rec != nil {
		s.heldClose = rec
	}
}

// queueRecordLocked queues recs on t.queue, after every record queued before
// them, whole or not at all. The caller holds t.mu, which orders them with
// the closes.
func (t *sessionTable) queueRecordLocked(recs ...Record) {
	if t.queue != nil {
		t.queue.push(recs...)
	}
}

// queueRecord is queueRecordLocked for a caller that does not hold t.mu.
func (t *sessionTable) queueRecord(rec Record) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queueRecordLocked(rec)
}

// closeHeldLocked is closeLocked without queueing the record, which it
// returns, nil when s was already closed. The caller holds t.mu.
func (t *sessionTable) closeHeldLocked(s *brokerSession, reason Reason, cause CloseCause) *SessionCloseRecord {
	if s.closed != "" {
		return nil
	}
	s.closed = reason
	s.closedAt = t.now()
	s.grant = nil
	t.attempts.dropLocked(s.ID)
	t.reports.closeLocked(s.ID)
	t.runs.dropLocked(s.ID)
	// A new session an open is still recording is not in the table.
	if t.open[s.HostSessionID] == s.ID {
		delete(t.open, s.HostSessionID)
	}
	userRecord, user := sessionUser(s)
	sessionRecord := s.openRecord
	return &SessionCloseRecord{
		RecordHeader: RecordHeader{AuditID: newAuditID(), Type: TypeSessionClose, At: s.closedAt, Outcome: OutcomeSuccess},
		SessionID:    s.ID, HostSessionID: s.HostSessionID, Workload: s.workload, Owner: s.Owner,
		ClosingReason: reason, Cause: cause, SessionRecord: sessionRecord, UserRecord: userRecord, User: user,
	}
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
func (b *Broker) FindSession(ctx context.Context, workload Workload, req FindSessionRequest) (Session, bool, error) {
	if err := checkCall(workload, req); err != nil {
		return Session{}, false, err
	}
	t := b.sessions
	t.mu.Lock()
	// While an open is creating a new broker session for the host session,
	// which enters the table only once its record is accepted, wait for that
	// open, so a find-then-close closes the session it opens rather than
	// miss it. Another open may start a new session between the wait and the
	// lock, so the check repeats. A lookup of an existing session does not
	// wait.
	for t.newOpens[req.RootHostSessionID] != nil {
		t.mu.Unlock()
		unlock, err := t.opening.lock(ctx, req.RootHostSessionID, "the session lookup was cancelled")
		if err != nil {
			return Session{}, false, err
		}
		unlock()
		t.mu.Lock()
	}
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
// refresh token; an open converting a token for an existing session
// meanwhile discards the new one. A new session is not in the table until
// its open is recorded, so a close by session ID cannot reach it; a close of
// its host session closes it, and the open then fails and stores nothing.
// Neither waits for an open in progress. A close of a host session also
// refuses opens of its closed incarnation for hostClosedLifetime
// (closeHostLocked).
//
// Shortcut: a discarded refresh token is not revoked; removed by #2568 (refresh-token revocation).
func (b *Broker) CloseSession(_ context.Context, workload Workload, req CloseSessionRequest) error {
	if err := checkCall(workload, req); err != nil {
		return err
	}
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	if req.RootHostSessionID != "" {
		t.closeHostLocked(hostClosedKey{workload: workload, host: req.RootHostSessionID, incarnation: req.RootHostIncarnation}, req.Reason)
		return nil
	}
	s, ok := t.byID[req.SessionID]
	if !ok || s.workload != workload {
		return unknownSession()
	}
	current := s.closed == "" && t.open[s.HostSessionID] == s.ID
	t.closeLocked(s, req.Reason, CloseRequested)
	if current {
		// mecak8s closes a root's current broker session itself only when
		// nothing will report its delegations again: it belongs to an earlier
		// incarnation or an unsaved create. A close of an earlier or already
		// closed session, as of a create that lost a race, leaves the
		// current session's history alone.
		t.runs.forgetPreviousLocked(workload, s.HostSessionID)
	}
	return nil
}

// closeHostLocked closes host's open broker session and the new one an
// open of host is creating, when workload's, forgets host's delegation
// history, and marks host's incarnation closed for hostClosedLifetime so
// that no open of that incarnation gives it a new broker session meanwhile: mecak8s closes a host session
// this way when it is deleted or its scheduled run ends, after which nothing
// opens it or reports its delegations again. It does not wait for an open in
// progress, which fails once it ends; the close of the new session it is
// creating is held until that open's refusal is queued. The caller holds
// t.mu.
func (t *sessionTable) closeHostLocked(key hostClosedKey, reason Reason) {
	workload, host := key.workload, key.host
	t.hostClosed.mark(key, t.now())
	if s := t.newOpens[host]; s != nil && s.workload == workload {
		t.holdCloseLocked(s, reason, CloseRequested)
	}
	if id, ok := t.open[host]; ok && t.byID[id].workload == workload {
		t.closeLocked(t.byID[id], reason, CloseRequested)
	}
	t.runs.forgetPreviousLocked(workload, host)
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
