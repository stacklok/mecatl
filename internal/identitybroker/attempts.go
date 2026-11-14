package identitybroker

import "slices"

// maxAttemptsPerSession bounds how many issued attempts a session's ledger
// keeps; the oldest is forgotten first, so a rejection report for it is
// refused.
const maxAttemptsPerSession = 1024

// callKey is what an attempt was issued for. A rejection report must repeat
// it exactly.
type callKey struct {
	operationID, agent, method, server, tool, argumentsSHA256 string
}

// attemptLedger remembers the attempts AuthorizeCall issued, so a caller can
// report one rejected token and get one retry: per session, the issued
// attempts in issue order, at most maxAttemptsPerSession of them. An attempt
// issued by a retry is never added, so a second rejection fails the call. Its
// methods are guarded by sessionTable.mu.
//
// Shortcut: retry state held in memory, forgetting a session's oldest attempt beyond 1024; removed by #2563 (durable state).
type attemptLedger struct {
	bySession map[string]*sessionAttempts
}

type sessionAttempts struct {
	order []string // attempt IDs, oldest first
	calls map[string]callKey
}

func newAttemptLedger() *attemptLedger {
	return &attemptLedger{bySession: map[string]*sessionAttempts{}}
}

// issueLocked records attemptID as issued in sessionID for call.
func (m *attemptLedger) issueLocked(sessionID, attemptID string, call callKey) {
	s, ok := m.bySession[sessionID]
	if !ok {
		s = &sessionAttempts{calls: map[string]callKey{}}
		m.bySession[sessionID] = s
	}
	if _, ok := s.calls[attemptID]; ok {
		return
	}
	if len(s.order) == maxAttemptsPerSession {
		delete(s.calls, s.order[0])
		s.order = slices.Delete(s.order, 0, 1)
	}
	s.order = append(s.order, attemptID)
	s.calls[attemptID] = call
}

// knownLocked reports whether attemptID was issued in sessionID and not yet
// consumed. It does not compare the call: it runs before the credential
// identifies the agent, so consumeLocked checks the call.
func (m *attemptLedger) knownLocked(sessionID, attemptID string) bool {
	s, ok := m.bySession[sessionID]
	if !ok {
		return false
	}
	_, ok = s.calls[attemptID]
	return ok
}

// consumeLocked deletes attemptID and reports whether it was issued in
// sessionID for call. A mismatched call deletes it too, so an attempt is
// reported at most once whatever the report says.
func (m *attemptLedger) consumeLocked(sessionID, attemptID string, call callKey) bool {
	s, ok := m.bySession[sessionID]
	if !ok {
		return false
	}
	issued, ok := s.calls[attemptID]
	if !ok {
		return false
	}
	delete(s.calls, attemptID)
	s.order = slices.DeleteFunc(s.order, func(id string) bool { return id == attemptID })
	return issued == call
}

// dropLocked forgets every attempt of sessionID.
func (m *attemptLedger) dropLocked(sessionID string) {
	delete(m.bySession, sessionID)
}
