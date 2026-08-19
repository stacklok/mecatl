package server

import (
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

// TrackedClientKeysForTest returns the post-validation rate-limiter's per-client
// bucket keys (empty when rate limiting is disabled). It lets an external test
// assert WHAT the limiter keys on — the verified (iss, sub) rather than the raw
// token — and that a rejected token creates no post-validation bucket (ADR 0204
// decision 3).
func (a *Authenticator) TrackedClientKeysForTest() []string {
	if a.limiters == nil {
		return nil
	}
	a.limiters.mu.Lock()
	defer a.limiters.mu.Unlock()
	keys := make([]string, 0, len(a.limiters.clients))
	for k := range a.limiters.clients {
		keys = append(keys, k)
	}
	return keys
}

func (a *Authenticator) RejectedTrackedClientCountForTest() int {
	if a.rejectedLimiters == nil {
		return 0
	}
	a.rejectedLimiters.mu.Lock()
	defer a.rejectedLimiters.mu.Unlock()
	return len(a.rejectedLimiters.clients)
}

// SetEngineCloseTimeoutForTest overrides the package-level engineCloseTimeout var
// for the duration of one test; it returns a restore func (defer it). The override
// lets a test shrink the timeout so a bounded-engine-close assertion runs in
// milliseconds, not seconds.
func SetEngineCloseTimeoutForTest(d time.Duration) (restore func()) {
	prev := engineCloseTimeout
	engineCloseTimeout = d
	return func() { engineCloseTimeout = prev }
}

// SessionEngineContextWindowForTest exposes the per-session engine's compaction
// context window (Engine.ContextWindow(), engine/agent/loop.go) for the session
// under id, or (0, false) if no per-session engine is registered (the session is
// riding the shared engine). It lets an EXTERNAL (server_test) test assert the
// ACTUAL compaction window of a rehydrated engine — a DIRECT check that does not
// rely on the ResolvedModel echo as a proxy (issue #66 engine-window fix review).
func (s *Service) SessionEngineContextWindowForTest(id session.SessionID) (int, bool) {
	s.mu.Lock()
	se, ok := s.sessionEngines[id]
	s.mu.Unlock()
	if !ok || se.engine == nil {
		return 0, false
	}
	return se.engine.ContextWindow(), true
}

// HasSessionEngineForTest reports whether a per-session engine is registered for
// id (false when the session rides the shared engine). Used by the issue-#102
// worktree-binding tests to assert a worktree session routed through the
// per-session factory (and that rehydration rebuilt one after a simulated
// restart). Exported via export_test.go so an _external_ test (internal/app) can
// reach across the package boundary through the Service the app.Build returns.
func (s *Service) HasSessionEngineForTest(id session.SessionID) bool {
	s.mu.Lock()
	_, ok := s.sessionEngines[id]
	s.mu.Unlock()
	return ok
}

// DropSessionEngineForTest simulates a process restart by removing the
// per-session engine registration for id (and its environment override), WITHOUT
// closing the engine (the process is "gone"). It is the test seam for the
// issue-#102 rehydration assertion: after it, a prompt run must rehydrate. The
// session itself stays in the store (a restart re-reads it from SessionStore).
func (s *Service) DropSessionEngineForTest(id session.SessionID) {
	s.mu.Lock()
	delete(s.sessionEngines, id)
	delete(s.sessionEnvironments, id)
	s.mu.Unlock()
}

// NeedsRehydrationForTest exposes the (Service) needsRehydration method (the
// issue-#102 widened trigger) to external tests so the worktree/default/cloud
// cases can be pinned directly without driving a full run.
func (s *Service) NeedsRehydrationForTest(sess *session.Session) bool {
	return s.needsRehydration(sess)
}

// TrackSteerMessageIDForTest appends one client-minted id to the session's
// watermark FIFO — the test seam for the steer correlation invariant pin
// (ADR-0232; assert positional, not textual, correlation).
func (s *Service) TrackSteerMessageIDForTest(id session.SessionID, messageID string) {
	s.trackSteerMessageID(id, messageID)
}

// SteerOutcomeToProtoForTest exposes the unexported steerOutcomeToProto mapper
// so the full-matrix test can assert every agent.SteerOutcome arm.
func SteerOutcomeToProtoForTest(o agent.SteerOutcome) mecatlv1.SteerOutcome {
	return steerOutcomeToProto(o)
}
