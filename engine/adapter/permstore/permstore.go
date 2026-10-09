// Package permstore is the in-memory, per-session store of LEARNED permission
// rules — the rules an ACP "allow always" verdict records (issue #3).
//
// It is deliberately the SMALLEST possible mutable seam around the otherwise
// immutable, session-free governance.Evaluator: the Evaluator never learns; the
// agent loop, on an allow-always verdict, asks the permission policy to Learn,
// which derives a narrow tool+exact-pattern rule (governance.LearnableRule) and
// Records it here, keyed by session. On every subsequent evaluation the policy
// reads this session's rules back and merges them in at the LOWEST scope.
//
// Scope of the store (by design, conservative):
//
//   - PER-SESSION: a rule learned in session A is invisible to session B.
//   - IN-MEMORY / NON-DURABLE: rules are lost on process restart and on
//     Forget(sessionID) (wired into Service.CloseSession). The store itself is
//     not durable; on resume the server replays logged allow-always verdicts
//     into it (Service.maybeReplayApprovals).
//   - EVICTION runs from Service.CloseSession, reachable over all three surfaces:
//     the ACP adapter (on editor disconnect) and the gRPC CloseSession RPC /
//     HTTP DELETE /v1/sessions/{id} session-end entries (issue #10). A well-behaved
//     client therefore reclaims a session's rules at session end. As a
//     client-independent backstop, the per-session learned-rule slice is also CAPPED
//     (maxRulesPerSession) so a pathological long-lived session that never signals
//     end cannot grow it without bound; each rule still requires a human
//     allow-always approval. There is no TTL/idle eviction.
//
// It implements port.PermissionStore and is safe for concurrent use.
package permstore

import (
	"sync"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// maxRulesPerSession caps a single session's learned-rule slice as a backstop
// against unbounded growth when no session-end signal arrives (issue #10). Each
// rule still requires a human allow-always; this only bounds a pathological
// long-lived session. At the cap a new DISTINCT rule is dropped, so the session
// keeps asking for that call rather than learning it — fail-safe toward asking,
// never toward a silent allow.
const maxRulesPerSession = 256

// Memory is the in-memory per-session learned-rule store. The zero value is NOT
// usable; construct it with New.
type Memory struct {
	mu        sync.Mutex
	bySession map[session.SessionID][]governance.Rule
}

// New constructs an empty Memory store.
func New() *Memory {
	return &Memory{bySession: make(map[session.SessionID][]governance.Rule)}
}

// Record appends rule to sessionID's learned set, deduping identical rules so a
// repeated "allow always" for the same call does not grow the slice unbounded.
// Distinct rules are capped at maxRulesPerSession: at the cap a new distinct rule
// is dropped (the session keeps asking for it). It is concurrency-safe.
func (m *Memory) Record(sessionID session.SessionID, rule governance.Rule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.bySession[sessionID] {
		if existing == rule {
			return // idempotent: identical rule already learned
		}
	}
	// Dedup runs first (above), so an identical re-learn at the cap stays a no-op;
	// only a new DISTINCT rule is dropped here. Fail-safe toward asking.
	if len(m.bySession[sessionID]) >= maxRulesPerSession {
		return
	}
	m.bySession[sessionID] = append(m.bySession[sessionID], rule)
}

// Rules returns a COPY of sessionID's learned rules, so the caller can read it
// without holding the lock and a later Record cannot mutate the returned slice.
// An unknown session yields nil.
func (m *Memory) Rules(sessionID session.SessionID) []governance.Rule {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.bySession[sessionID]
	if len(src) == 0 {
		return nil
	}
	out := make([]governance.Rule, len(src))
	copy(out, src)
	return out
}

// Forget evicts all learned rules for sessionID. The composition layer wires it
// into Service.CloseSession so a session's learned rules do not outlive it (they
// are non-durable by design). It is idempotent: an unknown session is a no-op.
func (m *Memory) Forget(sessionID session.SessionID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bySession, sessionID)
}
