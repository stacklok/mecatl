package identitybroker

import (
	"context"
	"time"
)

// ClockSkew is the clock skew the package's test brokers configure.
const ClockSkew = 30 * time.Second

// MaxRunsPerSession is how many run records, and how many delegation
// records, a session keeps before forgetting the oldest.
const MaxRunsPerSession = maxRunsPerSession

// HostClosedLifetime is how long a close by host session refuses its opens.
const HostClosedLifetime = hostClosedLifetime

// MaxQueuedRecords bounds the record queue, and CloseReserve is how much of
// it only closes and the records queued with one may fill.
const (
	MaxQueuedRecords = maxQueuedRecords
	CloseReserve     = closeReserve
)

// QueueRecords queues recs on b's record queue as one group, as the broker
// queues closes and refusals.
func QueueRecords(b *Broker, recs ...Record) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queueRecordLocked(recs...)
}

// SetClock replaces b's clock. Call it before b is used.
func SetClock(b *Broker, now func() time.Time) { WithClock(now)(b) }

// AS2Bound reports whether b's gateway AS-2 is bound, so a test can tell an
// unbind has started.
func AS2Bound(b *Broker) bool { return b.as2.Load() != nil }

// HostLockRefs returns how many opens hold or wait for host's open lock, and
// whether the lock has an entry at all.
func HostLockRefs(b *Broker, host string) (int, bool) {
	k := &b.sessions.opening
	k.mu.Lock()
	defer k.mu.Unlock()
	l, ok := k.locks[host]
	if !ok {
		return 0, false
	}
	return l.refs, true
}

// HeldGrant is what a broker session keeps about its refresh token.
type HeldGrant struct {
	RefreshToken  Token
	ExpiresAt     time.Time
	SourceJTI     string
	VerifiedOwner Owner
}

// SessionGrant returns the grant session id holds, and whether it holds one.
func SessionGrant(b *Broker, id string) (HeldGrant, bool) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byID[id]
	if !ok || s.grant == nil {
		return HeldGrant{}, false
	}
	g := HeldGrant{RefreshToken: s.grant.refreshToken, ExpiresAt: s.grant.expiresAt, SourceJTI: s.grant.sourceJTI}
	if s.verifiedOwner != nil {
		g.VerifiedOwner = *s.verifiedOwner
	}
	return g, true
}

// VerifiedOwner returns session id's verified owner, and whether it has one.
func VerifiedOwner(b *Broker, id string) (Owner, bool) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.byID[id]
	if !ok || s.verifiedOwner == nil {
		return Owner{}, false
	}
	return *s.verifiedOwner, true
}

// SetVerifiedOwner forces session id's verified owner, for a state the
// public calls cannot reach.
func SetVerifiedOwner(b *Broker, id string, owner Owner) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byID[id].verifiedOwner = &owner
}

// CloseSessionWithReason closes session id with reason, which CloseSession
// does not accept from Mecatl.
func CloseSessionWithReason(b *Broker, id string, reason Reason) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked(t.byID[id], reason, CloseRequested)
}

// ConvertedTokens returns how many converted access tokens b remembers,
// after forgetting expired ones.
func ConvertedTokens(b *Broker) int {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneConvertedLocked()
	return len(t.converted)
}

// PreviousReports returns how many delegations' accepted reports b keeps
// for root host session host.
func PreviousReports(b *Broker, host string) int {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.runs.previous[host]; ok {
		return len(p.byID)
	}
	return 0
}

// WaitRecords returns once b has sent every queued close and refusal record.
func WaitRecords(b *Broker) { _ = b.sessions.queue.wait(context.Background()) }

// SetRecorder replaces b's record sink. Call it while no call is running.
func SetRecorder(b *Broker, r Recorder) { b.recorder = r }

// MaxAttemptsPerSession bounds a session's issued and reported attempts.
const MaxAttemptsPerSession = maxAttemptsPerSession

// IssueReportedAttempt has b remember attemptID, issued in session id for
// operation op by a decision recorded as decision, as AuthorizeCall does for
// an allowed tools/call.
func IssueReportedAttempt(b *Broker, id, attemptID, op, decision string) {
	t := b.sessions
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reports.issueLocked(id, attemptID, op, decision, Subjects{})
}
