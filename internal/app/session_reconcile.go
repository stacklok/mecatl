package app

// session_reconcile.go wires the composition-level sweep that repairs
// sessions crash-orphaned in StateRunning (issue #475). Step 2
// (internal/adapter/server.Service.SessionStale/SettleIfStale) already owns
// the staleness DECISION and the repair WRITE; this file is the composition
// CALLER that finds candidates and drives them through those two exported
// seams — the childgc.go idiom (startup sweep + ticker, sharing ctx) applied
// to a different inventory.

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// staleSessionSweepInterval is the ticker cadence for the sweep. A
// package-level var (mirroring service.go's staleSessionWindow) so a test can
// shrink it instead of waiting on a real interval.
var staleSessionSweepInterval = 5 * time.Minute

// startStaleSessionReconcile wires the crash-orphaned-running-session sweep
// (issue #475 Step 4) — the fix for the "/sessions shows forever in
// progress" symptom. It complements Step 3's run-entry funnel repair
// (StartRunContent's own StateRunning check): the funnel only fires when a
// caller actually re-opens the orphaned session, so it can never reach a
// subagent-*/parallel-*/team-* child (nothing ever calls StartRunContent on a
// child id) — exactly the population the confirmed real bug came from. This
// sweep finds and settles them even if nobody ever re-opens them.
//
// Unconditional at wiring time: unlike startChildGC there is no operator-facing
// policy to disable, and the candidate enumeration (svc.ListSessions) already
// degrades to an empty slice, nil error, for a store that legitimately can't
// list — a silent no-op sweep, not a posture worth narrating. A GENUINE list
// failure (a real store error, not the degrade) is a different condition and
// gets its own WARN so an operator isn't left with zero diagnostic trail (see
// sweepStaleSessions). Each PASS can still no-op via
// svc.LeaseSweepDisabled() (see sweepStaleSessions) once SessionStale has
// stickily disabled the sweep for a lease backend that doesn't support
// leasing — that is a backend-capability fact, not an operator toggle. One
// startup sweep, then a ticker every staleSessionSweepInterval, on one
// goroutine, until the returned close func cancels it.
//
// Deliberately NOT tied to Build's own ctx (unlike childGC's — childGC is a
// no-op-by-default goroutine, since ChildGCInterval defaults to 0/startup-
// only, so most callers never notice it outlives one Build call; this sweep
// always runs a persistent ticker, so it needs its OWN cancelable lifetime —
// the startLiveModelRefresh idiom). Build folds the returned close into
// Built.Close's closeAll so a caller that never cancels its own ctx (the
// common test-fixture shape) still gets a clean teardown.
func startStaleSessionReconcile(cfg Config, svc *server.Service) func() {
	diag := cfg.diag()
	ctx, cancel := context.WithCancel(syscaller.Context(context.Background(), syscaller.RootStaleSessionReconcile))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweepStaleSessions(ctx, svc, diag)
		ticker := time.NewTicker(staleSessionSweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepStaleSessions(ctx, svc, diag)
			}
		}
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

// sweepStaleSessions runs one pass: list every stored session, narrow to the
// ones persisted StateRunning, exclude "sched--" fire ids (the scheduler owns
// its own stale-fire reconciler — see the note in scheduler_reconcile.go),
// and settle every remaining candidate Service.SessionStale judges to be a
// crash orphan rather than a genuinely in-flight run. ANY other id is
// eligible, top-level or a subagent-*/parallel-*/team-* child alike —
// SessionStale's age-horizon-first test (checked BEFORE any liveness/lease
// signal) is exactly what makes that safe for ids IsLive can never see live
// (a mid-run child, invisible to the top-level run registry).
//
// Best-effort throughout: ListSessions degrading to an empty slice with a nil
// error (an unsupported store) is silently a no-op sweep, matching its own
// degrade-to-empty contract; a GENUINE ListSessions error gets its own WARN
// (distinct from the degrade case — an operator needs to see when the sweep
// couldn't even attempt its job) and the pass aborts. A SettleIfStale failure
// is tallied into one WARN for the whole pass and never aborts the rest of
// the candidates. Stays quiet on an all-clean sweep, matching childgc's
// posture.
func sweepStaleSessions(ctx context.Context, svc *server.Service, diag port.Diagnostics) {
	if svc.LeaseSweepDisabled() {
		// SessionStale already logged the sticky-disable transition once; stay
		// silent here rather than double-logging every tick.
		return
	}
	rows, err := svc.StaleRunningCandidates(ctx)
	if err != nil {
		diag.Log(ctx, port.LevelWarn, "stale-session sweep: list failed; skipping sweep", "err", err.Error())
		return
	}
	if len(rows) == 0 {
		return
	}
	var settled, failed int
	var firstErr string
	for _, meta := range rows {
		if !svc.SessionStale(ctx, meta) {
			continue
		}
		ok, settleErr := svc.SettleIfStale(ctx, meta.ID)
		if settleErr != nil {
			failed++
			if firstErr == "" {
				firstErr = settleErr.Error()
			}
			continue
		}
		if ok {
			settled++
		}
	}
	if failed > 0 {
		diag.Log(ctx, port.LevelWarn, "stale-session sweep: some settles failed",
			"failed", failed, "first_err", firstErr)
	}
	if settled > 0 {
		diag.Log(ctx, port.LevelInfo, "stale-session sweep: settled crash-orphaned running sessions",
			"settled", settled)
	}
}
