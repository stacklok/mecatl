package server

import (
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

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
// per-session engine registration for id (and its workspace override), WITHOUT
// closing the engine (the process is "gone"). It is the test seam for the
// issue-#102 rehydration assertion: after it, a prompt run must rehydrate. The
// session itself stays in the store (a restart re-reads it from SessionStore).
func (s *Service) DropSessionEngineForTest(id session.SessionID) {
	s.mu.Lock()
	delete(s.sessionEngines, id)
	delete(s.sessionWorkspaces, id)
	s.mu.Unlock()
}

// NeedsRehydrationForTest exposes the (Service) needsRehydration method (the
// issue-#102 widened trigger) to external tests so the worktree/default/cloud
// cases can be pinned directly without driving a full run.
func (s *Service) NeedsRehydrationForTest(sess *session.Session) bool {
	return s.needsRehydration(sess)
}
