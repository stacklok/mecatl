package app

import (
	"context"
	"sync"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
)

// userModelReviewHooks is the composition-layer Stop-trigger DECORATOR for the
// Phase-2b background user-model reviewer. It wraps the main engine's
// port.HookRunner: every Run delegates to the inner runner unchanged, and ON a
// PhaseStop event it ADDITIONALLY fires the reviewer in a DETACHED goroutine,
// debounced by a session-count interval.
//
// It is deliberately a composition-layer wrapper, NOT a new domain port: the
// Stop-trigger policy (when to learn, how often) belongs to the composition root,
// and the reviewer's collaborators (SessionStore, child engine) are already wired
// here. The agent loop fires PhaseStop through this HookRunner once per terminal
// run (Engine.fireStop), so this is the natural, existing seam — no new event, no
// loop change.
//
// R10 / reopen-if-completed invariant: the reviewer reads the finished session's
// transcript via the SessionStore and spawns a FRESH single-shot child session. It
// NEVER reopens or re-runs the user's terminal session. The detached goroutine also
// means a Stop is never blocked on the review.
type userModelReviewHooks struct {
	inner    port.HookRunner
	reviewer *agent.UserModelReviewer
	// interval debounces reviews by session-stop count: a review fires on the 1st
	// Stop and then every `interval` Stops thereafter. 0 or 1 means "every Stop".
	interval int
	// diag is the injected operational-logging sink for the (best-effort) review
	// failure line. Never nil — newUserModelReviewHooks defaults it to NopDiagnostics.
	diag port.Diagnostics

	mu    sync.Mutex
	count int // number of PhaseStop events seen
}

// newUserModelReviewHooks wraps inner with the Stop-trigger decorator. interval is
// the session-count debounce (0/1 = every session). A nil diag defaults to
// NopDiagnostics so the decorator never nil-panics on the failure line.
func newUserModelReviewHooks(inner port.HookRunner, reviewer *agent.UserModelReviewer, interval int, diag port.Diagnostics) port.HookRunner {
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &userModelReviewHooks{inner: inner, reviewer: reviewer, interval: interval, diag: diag}
}

// Run delegates to the inner runner, then — only for PhaseStop, and only when the
// debounce admits this stop — fires the reviewer in a detached goroutine. The
// inner outcome is returned unchanged: the review is a side effect that can never
// alter the Stop outcome or block it.
func (h *userModelReviewHooks) Run(ctx context.Context, ev governance.HookEvent) (governance.HookOutcome, error) {
	outcome, err := h.inner.Run(ctx, ev)

	if ev.Phase == governance.PhaseStop && h.admit() {
		sessionID := ev.SessionID
		// Detach cancellation without dropping the originating caller's context
		// values. The review derives user-model facts from that caller's session.
		reviewCtx := context.WithoutCancel(ctx)
		go func() {
			if rerr := h.reviewer.Review(reviewCtx, sessionID); rerr != nil {
				h.diag.Log(reviewCtx, port.LevelWarn, "user-model background review failed", "session", sessionID, "err", rerr)
			}
		}()
	}

	return outcome, err
}

// admit applies the session-count debounce: it returns true on the 1st stop and
// then every `interval` stops. An interval <= 1 admits every stop.
func (h *userModelReviewHooks) admit() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	if h.interval <= 1 {
		return true
	}
	return (h.count-1)%h.interval == 0
}
