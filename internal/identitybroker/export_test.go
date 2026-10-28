package identitybroker

import "time"

// SetClock replaces b's clock. Call it before b is used.
func SetClock(b *Broker, now func() time.Time) { b.sessions.now = now }

// HostLockRefs returns how many opens hold or wait for host's open lock, and
// whether the lock has an entry at all.
func HostLockRefs(b *Broker, host string) (int, bool) {
	t := b.sessions
	t.hostMu.Lock()
	defer t.hostMu.Unlock()
	l, ok := t.hostLocks[host]
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
