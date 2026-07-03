// Package scheduler is the in-process tick loop for the scheduled-tasks
// feature (issue #189, Phase 1e). It is the composition-layer owner of:
//
//   - the leader-lease on the well-known `port.SchedulerLeaderLeaseID`
//     ("__scheduler__") so that, in a multi-replica deployment, at most one
//     replica ticks the schedule store at a time;
//   - the tick loop that polls `port.ScheduleStore.Due`, applies the misfire
//     policy, claims each due slot via `port.ScheduleStore.Claim` (the
//     at-most-once atomic advance), fires the claimed schedule through the
//     composition-supplied FireFunc seam, and records the outcome via
//     `port.ScheduleStore.RecordFire`.
//
// It is STORAGE-AGNOSTIC: like the run-entry lease renewer on
// `internal/adapter/server.Service`, the loop NEVER imports `engine/agent` or
// `internal/adapter/server`. The FireFunc seam is how composition injects the
// run-entry funnel (Service.CreateSessionWithProfile + Service.StartRunContent
// with subagent-grade RunOptions) in Phase 1f. A unit test supplies a stub
// FireFunc that records fires.
//
// The loop reads `now` from an injected `port.Clock` (deterministic tests) and
// logs through an injected `port.Diagnostics` (NEVER slog — the global-slog
// ban in engine/ and internal/ applies here too; see ADR 0020).
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// FireFunc is the composition-supplied callback a scheduler invokes for each
// claimed fire. It mints a fresh session (the "sched--" top-level session per
// fire, via Service.CreateSessionWithProfile + Service.StartRunContent with
// subagent-grade RunOptions), drives it to terminal, and returns the fire
// record (stop reason + err). The scheduler records the fire outcome via
// ScheduleStore.RecordFire. A non-nil error from FireFunc is recorded as a
// failed fire (StopError); the at-most-once Claim already advanced
// NextFireAt, so a failed fire is NOT retried (the slot is gone — decision
// #1).
type FireFunc func(ctx context.Context, sched port.Schedule, now time.Time) (port.ScheduleFire, error)

// ErrFireNowOverlap is returned by FireNow when the schedule's singleton
// overlap check found a prior fire still running (its session lease is held by
// any replica). The fire was SKIPPED, not claimed — the caller (composition's
// Service.FireNow) maps it to FailedPrecondition / 409 so a client distinguishes
// "overlapping fire rejected" from a genuine error. It mirrors the tick loop's
// silent singleton skip, surfaced as an explicit error on the manual path (a
// manual fire is a client-initiated request that deserves an explicit rejection,
// unlike the tick loop's best-effort skip).
var ErrFireNowOverlap = errors.New("scheduler: fire-now skipped (prior fire still running)")

// ErrFireNowDisabled is returned by FireNow when the schedule is not Enabled
// (paused or done). A paused/done schedule cannot be manually fired. The caller
// maps it to FailedPrecondition.
var ErrFireNowDisabled = errors.New("scheduler: fire-now rejected (schedule disabled)")

// ErrFireNowExhausted is returned by FireNow when a one-shot schedule has
// already fired (FireCount > 0). A one-shot fires once; a manual re-fire of a
// completed one-shot is rejected. The caller maps it to FailedPrecondition.
var ErrFireNowExhausted = errors.New("scheduler: fire-now rejected (one-shot already fired)")

// Config wires the scheduler. All fields are set by composition (Phase 1f);
// the unit tests construct one directly with a stub Fire.
type Config struct {
	// Store is the durable schedule registry. Required.
	Store port.ScheduleStore
	// Lease is the cross-process leader-lease backend for the
	// `port.SchedulerLeaderLeaseID` ("__scheduler__") leader lease. nil means
	// single-replica by affinity: the scheduler runs standalone (no
	// cross-process leader gate) — the byte-identical default when no lease
	// backend is wired, exactly as the run-entry lease defaults off.
	Lease port.SessionLease
	// LeaseOwner is the per-process owner string composition builds once per
	// Build (the same "<hostname>-<pid>-<nonce>" shape the run-entry seam
	// uses). Required when Lease != nil; ignored otherwise.
	LeaseOwner string
	// LeaseTTL is the leader-lease lifetime requested at Acquire. A non-positive
	// value defaults to defaultLeaderLeaseTTL. Ignored when Lease == nil.
	LeaseTTL time.Duration
	// LeaseRenewInterval is how often the leader-lease renewer refreshes the
	// held lease. A non-positive value defaults to LeaseTTL/3. Ignored when
	// Lease == nil.
	LeaseRenewInterval time.Duration
	// Fire is the composition-supplied run-entry callback. Composition wires
	// this in Phase 1f; for Phase 1e's unit tests a stub records fires. Required.
	Fire FireFunc
	// Clock supplies `now` for the tick loop and Claim. Required.
	Clock port.Clock
	// Diagnostics is the operational logging seam. A nil value is treated as
	// port.NopDiagnostics so the scheduler is nil-safe by construction.
	Diagnostics port.Diagnostics
	// TickInterval is how often the tick loop polls ScheduleStore.Due. A
	// non-positive value defaults to defaultTickInterval.
	TickInterval time.Duration
	// MinInterval is the frequency floor the composition create-seam enforces at
	// Save time (a schedule whose cadence is tighter than this is rejected,
	// fail-closed). It is NOT read by the tick loop — it lives on Config so a
	// future self-pushing lookahead can consult it without widening the
	// constructor. Documented here to keep it honest.
	MinInterval time.Duration
	// MaxConcurrentFires bounds the per-tick fire fan-out via an errgroup with
	// SetLimit. A non-positive value defaults to defaultMaxConcurrentFires.
	MaxConcurrentFires int
	// StopFireGrace is how long Stop waits for in-flight fires to drain before
	// abandoning them. A non-positive value defaults to stopFireGrace. It is a
	// Config field (not a flag) so a test can shrink it.
	StopFireGrace time.Duration
	// EmitScheduleEvent is the OPTIONAL composition-injected callback the
	// scheduler invokes to emit an EvScheduleFired/Skipped/Failed event. It is
	// nil-safe (nil = no event emitted — the byte-identical no-emit path).
	// Composition wires it to emit into the fire session's event log / the
	// Service's event sink. The scheduler pkg stays EventSink-free (testable, no
	// engine/agent import): the payload is a plain session.SchedulePayload value
	// object, not an EventSink/port import. The scheduler invokes it from
	// fireClaimed (fired/failed) and fireOne/FireNow (skipped) — the caller
	// decides the kind; the callback decides where it lands.
	EmitScheduleEvent func(payload session.SchedulePayload)
	// ScheduleMetrics is the OPTIONAL composition-injected metrics callback
	// (issue #233, Phase 2b). It is nil-safe (nil = no metrics recorded — the
	// byte-identical no-metrics path). The scheduler invokes it from
	// fireClaimed (fired/failed, with the Claim→terminal duration) and
	// fireOne/FireNow (skipped, duration 0). Composition wires it over the
	// telemetry adapter's Metrics.EmitSchedule — the scheduler pkg stays
	// telemetry-import-free (the metrics seam is a plain callback, mirroring
	// EmitScheduleEvent). The payload's Kind ("fired"/"skipped"/"failed")
	// labels the outcome; duration > 0 only for a fired/failed fire.
	ScheduleMetrics func(payload session.SchedulePayload, duration time.Duration)
}

// Defaults. The leader-lease defaults mirror the run-entry lease defaults
// (defaultLeaseTTL = 30s, renewer at TTL/3, Acquire bounded by
// leaseAcquireTimeout = 5s) so the two lease owners in the process share one
// posture; the tick interval is a conservative 30s poll (the in-memory
// lookahead a future phase may add is DERIVED, the store is ground truth).
const (
	defaultLeaderLeaseTTL     = 30 * time.Second
	defaultTickInterval       = 30 * time.Second
	defaultMaxConcurrentFires = 4
	// leaderLeaseAcquireTimeout bounds a SessionLease.Acquire call for the
	// leader lease so a wedged backend cannot stall Start indefinitely. It
	// mirrors the run-entry seam's leaseAcquireTimeout.
	leaderLeaseAcquireTimeout = 5 * time.Second
	// leaderLeaseRenewFraction bounds a Renew call to a fraction of the renew
	// interval (mirrors the run-entry seam's leaseRenewFraction).
	leaderLeaseRenewFraction = 2
	// Schedule-fire Kind values (the session.SchedulePayload.Kind contract). They
	// are STRING-PASSTHROUGH on the wire (no proto enum — a later value would not
	// silently mis-classify); these unexported constants are the single source of
	// the literals the scheduler constructs, so goconst does not flag the repeated
	// string and a typo can't drift a payload's Kind off the contract.
	scheduleKindFired   = "fired"
	scheduleKindSkipped = "skipped"
	scheduleKindFailed  = "failed"
	// stopFireGrace is how long Stop waits for in-flight fires to drain before
	// cancelling them. A fire mid-run when Stop is called gets this much headroom
	// to complete cleanly; after it, Stop cancels and joins.
	stopFireGrace = 10 * time.Second
	// singletonTrialTimeout bounds the trial-lease acquire in isPriorFireLive.
	// It must be SHORT: the check is a best-effort liveness probe, not a hard
	// gate (the Claim fence is the backstop). A slow backend must not consume an
	// errgroup slot for longer than this. Mirrors the leader-lease acquire
	// timeout's "bounded so a wedged backend cannot stall" discipline.
	singletonTrialTimeout = 5 * time.Second
)

// Scheduler is the composition-layer owner of the scheduled-tasks tick loop.
// Construct one via New, then Start (which acquires the leader lease if a
// backend is wired and launches the tick + renewer goroutines), and Stop it at
// shutdown / drain.
type Scheduler struct {
	cfg  Config
	diag port.Diagnostics

	mu          sync.Mutex // protects the leader-lease state below
	leaderLease *port.Lease

	// tickCtx is the tick loop's ctx; cancelling it stops ticking. It is also
	// cancelled by the renewer on a definitive leader-lease loss (a peer took
	// over — don't double-fire).
	tickCtx       context.Context
	tickCancel    context.CancelFunc
	tickDone      chan struct{} // closed when the tick goroutine exits
	renewerCancel context.CancelFunc
	renewerDone   chan struct{} // closed when the renewer goroutine exits (nil if none started)

	draining atomic.Bool

	// firesWG tracks in-flight fire goroutines so Stop can join them.
	firesWG sync.WaitGroup

	// done is closed when Stop completes (all goroutines joined, lease
	// released). Tests may wait on it to assert clean shutdown.
	done chan struct{}

	// started prevents double-Start / double-Stop.
	started atomic.Bool
	stopped atomic.Bool

	// fireMu is the per-schedule-name serialization gate for FireNow (the
	// TOCTOU close: Load → singleton trial-lease → ClaimNow had no per-name
	// serialization, so two concurrent FireNow RPCs with different `now` values
	// could both pass the singleton fence and both fire — the very overlap the
	// singleton guard prevents). It is shared with the tick loop's fireOne so a
	// tick-driven fire and a manual FireNow on the SAME schedule serialize per
	// name (different schedules stay parallel — the errgroup's SetLimit still
	// bounds fan-out). A sync.Map holds one *sync.Mutex per name, created lazily.
	fireMu sync.Map
}

// lockFireName acquires the per-schedule-name mutex that serializes concurrent
// FireNow calls AND a tick-loop fireOne on the SAME schedule (the TOCTOU close
// for the Load→singleton→Claim fence). Different schedule names stay parallel.
// The caller MUST defer the returned release func. It is safe to call from the
// tick loop (fireOne runs under the errgroup) and from the caller-driven
// FireNow path.
func (s *Scheduler) lockFireName(name string) func() {
	v, _ := s.fireMu.LoadOrStore(name, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// SetFire sets the composition-supplied FireFunc. It MUST be called before
// Start (Start panics if Fire is nil — composition wires the run-entry funnel
// here). It is the late-bind seam: buildScheduler constructs the Scheduler with
// the store/lease/clock but no Fire (the Service does not exist yet), Build
// calls SetFire after NewService, then Start.
func (s *Scheduler) SetFire(f FireFunc) {
	if f == nil {
		panic("scheduler: nil Fire")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.Load() {
		panic("scheduler: SetFire after Start")
	}
	s.cfg.Fire = f
}

// SetEmitScheduleEvent sets the OPTIONAL composition-injected emit callback. It
// MUST be called before Start (the late-bind seam, parallel to SetFire). A nil
// callback is the byte-identical no-emit path (no EvSchedule* events emitted —
// the scheduler is fully functional, just silent on the schedule lifecycle).
// Composition calls it after SetFire (so the FireFunc is bound) and before
// Start (so the callback is in place when the first tick fires).
func (s *Scheduler) SetEmitScheduleEvent(cb func(payload session.SchedulePayload)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.Load() {
		panic("scheduler: SetEmitScheduleEvent after Start")
	}
	s.cfg.EmitScheduleEvent = cb
}

// SetScheduleMetrics sets the OPTIONAL composition-injected metrics callback
// (issue #233, Phase 2b). It is the late-bind seam, parallel to
// SetEmitScheduleEvent: a nil callback is the byte-identical no-metrics path.
// Composition calls it after SetFire (so the FireFunc is bound) and before
// Start (so the callback is in place when the first tick fires).
func (s *Scheduler) SetScheduleMetrics(cb func(payload session.SchedulePayload, duration time.Duration)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.Load() {
		panic("scheduler: SetScheduleMetrics after Start")
	}
	s.cfg.ScheduleMetrics = cb
}

// New constructs a Scheduler. It applies Config defaults (TTLs, intervals,
// fan-out, a NopDiagnostics sink) but does NOT acquire the lease or start any
// goroutine — call Start. A nil Clock or Store is a programming error at the
// only construction site (composition); New panics so it surfaces loudly there
// rather than as a nil-deref in the tick loop. Fire MAY be nil at New (the
// late-bind seam: composition calls SetFire after NewService, before Start); a
// nil Fire at Start panics.
func New(cfg Config) *Scheduler {
	if cfg.Store == nil {
		panic("scheduler: nil Store")
	}
	if cfg.Clock == nil {
		panic("scheduler: nil Clock")
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultLeaderLeaseTTL
	}
	if cfg.LeaseRenewInterval <= 0 {
		cfg.LeaseRenewInterval = cfg.LeaseTTL / 3
	}
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = defaultTickInterval
	}
	if cfg.MaxConcurrentFires <= 0 {
		cfg.MaxConcurrentFires = defaultMaxConcurrentFires
	}
	if cfg.StopFireGrace <= 0 {
		cfg.StopFireGrace = stopFireGrace
	}
	if cfg.Diagnostics == nil {
		cfg.Diagnostics = port.NopDiagnostics{}
	}
	return &Scheduler{
		cfg:  cfg,
		diag: cfg.Diagnostics.With("component", "scheduler"),
		done: make(chan struct{}),
	}
}

// Start acquires the leader lease (if a backend is wired) and launches the
// tick loop (and, on a successful acquire, the renewer). It returns nil on:
//   - success (lease acquired or standalone — ticking either way);
//   - ErrLeaseHeld (a peer is the leader — this replica stands down, NOT
//     ticking; logged INFO);
//   - ErrLeaseUnsupported (the backend can never lease — sticky-disable the
//     leader gate and CONTINUE ticking standalone, single-replica by affinity;
//     logged INFO, the honest posture).
//
// On any other Acquire error it returns the error WITHOUT ticking (a genuine
// infrastructure failure — fail loud rather than silently run two tickers).
// Start is idempotent: a second call is a no-op returning nil.
func (s *Scheduler) Start(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return nil
	}
	if s.cfg.Fire == nil {
		s.started.Store(false)
		panic("scheduler: Start called before SetFire (Fire is nil)")
	}

	if s.cfg.Lease != nil {
		lease, standalone, err := s.acquireLeader(ctx)
		if err != nil {
			// ErrLeaseHeld: a peer is the leader. Stand down cleanly — do NOT
			// tick. The peer's Due/Claim fence still gives at-most-once, but
			// running a second ticker would only waste cycles.
			s.started.Store(false) // allow a later retry
			return err
		}
		if standalone {
			// ErrLeaseUnsupported: the backend can never lease. Tick standalone
			// (single-replica by affinity). The at-most-once Claim fence still
			// holds within this process. No leader-lease state to record.
		} else {
			// Acquired: store the lease and start the renewer.
			renewCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
			renewerDone := make(chan struct{})
			s.mu.Lock()
			s.leaderLease = &lease
			s.renewerCancel = cancel
			s.renewerDone = renewerDone
			s.mu.Unlock()
			go func() {
				defer close(renewerDone)
				s.renewLeader(renewCtx)
			}()
		}
	}

	// Launch the tick loop regardless (standalone or leader). The tick ctx is
	// detached from the caller's ctx so a request-scope cancel does not stop
	// the loop — only Stop or a lost leader does. tickCtx/tickCancel/tickDone
	// are written under s.mu (mirroring RunOnceForTest's existing locked read)
	// because the renewer goroutine started above may already be running and
	// can read tickCancel via declareLeaderLost concurrently with this write
	// (a definitive lease loss racing the tick loop's own startup).
	tickCtx, tickCancel := context.WithCancel(context.WithoutCancel(ctx))
	tickDone := make(chan struct{})
	s.mu.Lock()
	s.tickCtx, s.tickCancel, s.tickDone = tickCtx, tickCancel, tickDone
	s.mu.Unlock()
	go func() {
		defer close(tickDone)
		s.tick(tickCtx)
	}()
	return nil
}

// acquireLeader acquires the __scheduler__ leader lease. It returns:
//   - (lease, false, nil) on a successful Acquire;
//   - (zero, true, nil) on ErrLeaseUnsupported — sticky-disable, tick standalone;
//   - (zero, false, err) on ErrLeaseHeld (stand down) or any other error (fail).
func (s *Scheduler) acquireLeader(ctx context.Context) (port.Lease, bool, error) {
	acqCtx, acqCancel := context.WithTimeout(ctx, leaderLeaseAcquireTimeout)
	defer acqCancel()
	lease, err := s.cfg.Lease.Acquire(acqCtx, port.SchedulerLeaderLeaseID, s.cfg.LeaseOwner)
	switch {
	case errors.Is(err, port.ErrLeaseUnsupported):
		s.diag.Log(ctx, port.LevelInfo, "leader lease unsupported by backend; running standalone (no cross-replica leader gate)",
			"owner", s.cfg.LeaseOwner)
		return port.Lease{}, true, nil
	case errors.Is(err, port.ErrLeaseHeld):
		s.diag.Log(ctx, port.LevelInfo, "another replica is the scheduler leader; standing down",
			"owner", s.cfg.LeaseOwner)
		return port.Lease{}, false, err
	case err != nil:
		return port.Lease{}, false, fmt.Errorf("scheduler: acquire leader lease: %w", err)
	}
	return lease, false, nil
}

// renewLeader refreshes the __scheduler__ leader lease on a ticker until
// renewCtx is cancelled (Stop) or the lease is definitively lost. It mirrors
// service.renewLoop: ErrLeaseHeld is definitive loss (a peer took over → stop
// ticking); a transient fault gets grace until near-expiry; a ctx-cancelled
// error is just shutdown racing a tick.
func (s *Scheduler) renewLeader(renewCtx context.Context) {
	ticker := time.NewTicker(s.cfg.LeaseRenewInterval)
	defer ticker.Stop()
	renewTimeout := s.cfg.LeaseRenewInterval / leaderLeaseRenewFraction
	if renewTimeout <= 0 {
		renewTimeout = s.cfg.LeaseRenewInterval
	}
	for {
		select {
		case <-renewCtx.Done():
			return
		case <-ticker.C:
			// Read the current lease under the lock (single source of truth).
			s.mu.Lock()
			lease := s.leaderLease
			s.mu.Unlock()
			if lease == nil {
				return // released concurrently
			}
			rCtx, rCancel := context.WithTimeout(renewCtx, renewTimeout)
			refreshed, err := s.cfg.Lease.Renew(rCtx, *lease)
			rCancel()
			switch {
			case errors.Is(err, context.Canceled):
				return // shutdown raced the tick
			case errors.Is(err, port.ErrLeaseHeld):
				// Definitive loss: a competitor holds it now. Stop ticking so we
				// do not double-fire against the new leader.
				s.declareLeaderLost(renewCtx, err)
				return
			case err != nil:
				// Transient/infra fault: keep the lease unless we are within one
				// renew interval of expiry (the next tick would land past it).
				if s.cfg.Clock.Now().Add(s.cfg.LeaseRenewInterval).Before(lease.Expiry) {
					continue // still have headroom; retry next tick.
				}
				s.declareLeaderLost(renewCtx, err)
				return
			}
			s.mu.Lock()
			s.leaderLease = &refreshed
			s.mu.Unlock()
		}
	}
}

// declareLeaderLost records the definitive leader-lease loss and cancels the
// tick ctx so the tick loop stops. A peer replica is now the leader; its
// tick loop takes over. In-flight fires are NOT cancelled (a fire mid-run
// completes; the at-most-once Claim already advanced NextFireAt, so the peer
// will not re-fire this slot).
func (s *Scheduler) declareLeaderLost(ctx context.Context, cause error) {
	s.diag.Log(ctx, port.LevelWarn, "lost scheduler leader lease; stopping tick loop",
		"owner", s.cfg.LeaseOwner, "err", cause.Error())
	// The tick-ctx cancel is the actual loss mechanism; no leader-state flag
	// needs bookkeeping (leaderLease is cleared on Stop's release path). Read
	// tickCancel under s.mu: Start's own launch of the tick loop writes it
	// concurrently with this renewer goroutine on a fast definitive loss (a
	// lease lost moments after Start), so an unguarded read here would race.
	s.mu.Lock()
	cancel := s.tickCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// tick is the poll loop. Each tick: read `now` from the clock, poll
// ScheduleStore.Due, and for each due schedule (bounded by
// MaxConcurrentFires via an errgroup) apply the misfire policy, Claim the
// slot (the at-most-once advance), Fire it, and RecordFire the outcome.
func (s *Scheduler) tick(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tickOnce(ctx)
		}
	}
}

// tickOnce runs one tick iteration. It is the unit a test can drive directly
// (via runOnce) to avoid real ticker sleeps.
func (s *Scheduler) tickOnce(ctx context.Context) {
	if s.draining.Load() {
		return // drain gate: no new fires mid-tick
	}
	now := s.cfg.Clock.Now()
	due, err := s.cfg.Store.Due(ctx, now)
	if err != nil {
		if errors.Is(err, port.ErrScheduleUnsupported) {
			// The backend can never store schedules — stop ticking (the seam
			// will never work here). This is sticky: we do not retry.
			s.diag.Log(ctx, port.LevelInfo, "schedule store unsupported by backend; stopping tick loop",
				"err", err.Error())
			if s.tickCancel != nil {
				s.tickCancel()
			}
			return
		}
		s.diag.Log(ctx, port.LevelWarn, "schedule store Due failed", "err", err.Error())
		return
	}
	if len(due) == 0 {
		return
	}
	// Bound the fire fan-out. The errgroup's ctx cancels remaining fires on the
	// first non-nil error, but a fire error is recorded (not propagated to abort
	// siblings) — each fire is independent (one failed fire does not block the
	// others). So we swallow per-fire errors inside fireOne and never return a
	// non-nil error from the errgroup.
	//
	// KNOWN PHASE-1 LIMITATION (issue #189): fireOne drives each fire to a
	// TERMINAL EvResult (a full agent session — potentially minutes), and this
	// g.Wait() blocks the tick goroutine until the whole due batch completes.
	// While blocked the loop cannot re-poll Due (time.Ticker drops intervening
	// ticks), so a long-running fire delays every OTHER schedule by up to its
	// duration. This does NOT affect at-most-once (Claim advances NextFireAt
	// before Fire, so no slot double-fires) — only fire LATENCY under a slow
	// co-scheduled run. Acceptable for Phase 1's small schedule counts; a later
	// phase decouples Claim/advance from the drive (hand fires to a background
	// pool, don't await terminal in the tick). Tracked as a Phase-2 follow-up.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.cfg.MaxConcurrentFires)
	for _, sched := range due {
		sched := sched
		g.Go(func() error {
			s.fireOne(gctx, sched, now)
			return nil
		})
	}
	_ = g.Wait()
}

// fireOne applies the misfire policy, the singleton overlap check, Claims the
// slot, Fires (unless skip), and RecordFires the outcome. It is the per-schedule
// claim-before-fire cycle.
func (s *Scheduler) fireOne(ctx context.Context, sched port.Schedule, now time.Time) {
	// Per-schedule-name serialization: a concurrent FireNow (or a peer tick on
	// the same schedule) must not race the Load→singleton→Claim fence (the
	// TOCTOU the singleton guard closes). Different schedule names stay
	// parallel — the errgroup's SetLimit still bounds fan-out.
	defer s.lockFireName(sched.Spec.Name)()
	// Compute the next fire instant. A cron trigger computes it via cronparse
	// (the store is parser-free); a one-shot fires once (zero nextFire → Claim
	// disables the schedule). A MaxFires-exhausted cron also yields zero (the
	// store enforces the exhaustion in Claim).
	nextFire, err := s.computeNextFire(sched, now)
	if err != nil {
		s.diag.Log(ctx, port.LevelWarn, "scheduler: compute next fire failed; skipping",
			"schedule", sched.Spec.Name, "err", err.Error())
		return
	}

	// Misfire policy: MisfireSkip + a slot missed by MORE THAN the grace window
	// advances NextFireAt WITHOUT firing. The Claim is still the advance (so the
	// slot is not re-returned by a peer's Due); we just skip the Fire.
	//
	// The grace window matters: Due returns any slot with NextFireAt <= now, and
	// with a polling tick a genuinely-due slot's NextFireAt is essentially ALWAYS
	// strictly before `now` (nanosecond equality never happens). Gating skip on a
	// bare `NextFireAt.Before(now)` would therefore skip EVERY fire — a MisfireSkip
	// schedule would never fire at all (review #189). The intended semantics is
	// "skip a slot we missed by a lot (the process was down)", NOT "skip normal
	// poll jitter". So skip only when the slot is late beyond one tick interval;
	// a freshly-due slot (missed by < one tick — ordinary poll cadence) still fires.
	skipFire := sched.Spec.Misfire == port.MisfireSkip && now.Sub(sched.State.NextFireAt) > s.misfireGraceWindow()

	// Singleton overlap check (decision: cross-replica singleton via trial-lease).
	// Before claiming, if Singleton is true and the prior fire's session is still
	// live (its session lease is held by ANY replica), skip this fire. The check
	// is a TRIAL lease acquire on LastFireSessionID: ErrLeaseHeld means the prior
	// fire is still running on some replica → skip (the singleton guard); success
	// means the prior fire finished or crashed (lease released/expired) → fire
	// freely. A stale pointer to a finished fire yields a free acquire (not
	// skipped) — the lease, not the field, is authoritative. The trial lease is
	// released immediately (we don't hold it; the fire's own run-entry acquires
	// its own session lease).
	if !skipFire && sched.Spec.Singleton && s.cfg.Lease != nil && sched.State.LastFireSessionID != "" && sched.State.LastFireSessionID != port.PendingFireSessionID {
		overlap, rel := s.isPriorFireLive(ctx, sched.State.LastFireSessionID)
		if rel != nil {
			defer rel() // release the trial lease when fireOne returns
		}
		if overlap {
			s.emitSchedule(session.SchedulePayload{
				ScheduleName: sched.Spec.Name,
				Kind:         scheduleKindSkipped,
			})
			s.emitScheduleMetrics(session.SchedulePayload{
				ScheduleName: sched.Spec.Name,
				Kind:         scheduleKindSkipped,
			}, 0)
			s.diag.Log(ctx, port.LevelInfo, "scheduler: skipping fire (prior fire still running)",
				"schedule", sched.Spec.Name, "prior_session", sched.State.LastFireSessionID)
			// Do NOT Claim here. A Claim would advance NextFireAt (so the slot
			// isn't re-returned) but would ALSO overwrite LastFireSessionID with
			// port.PendingFireSessionID — destroying the pointer to the
			// still-running prior fire. On the next tick the singleton check is
			// gated on LastFireSessionID != pending, so it would be SKIPPED, and a
			// second fire would launch concurrently with the still-running prior
			// fire, defeating the singleton guarantee (review finding #1).
			//
			// Instead: leave the slot due. The next tick's Due returns it again;
			// the singleton check re-acquires the trial lease (the authoritative
			// liveness oracle) and skips again while the prior fire holds it. When
			// the prior fire finishes (RecordFire + lease release), the next tick's
			// singleton check passes and Claims+fires normally. The repeated
			// trial-lease acquires while the prior fire runs are bounded
			// (singletonTrialTimeout each, every TickInterval) and acceptable.
			return
		}
	}

	// Claim is the at-most-once atomic advance. A peer's Claim between our Due
	// and Claim advanced NextFireAt past `now`; ErrScheduleNotFound is the
	// fail-safe "the slot is gone" — skip (the fence worked).
	claimed, err := s.cfg.Store.Claim(ctx, sched.Spec.Name, now, nextFire)
	if err != nil {
		if errors.Is(err, port.ErrScheduleNotFound) {
			// A peer claimed it between Due and Claim, or it was
			// disabled/deleted. The at-most-once fence worked; not an error.
			return
		}
		s.diag.Log(ctx, port.LevelWarn, "scheduler: Claim failed",
			"schedule", sched.Spec.Name, "err", err.Error())
		return
	}

	if skipFire {
		s.emitSchedule(session.SchedulePayload{
			ScheduleName: sched.Spec.Name,
			Kind:         scheduleKindSkipped,
		})
		s.emitScheduleMetrics(session.SchedulePayload{
			ScheduleName: sched.Spec.Name,
			Kind:         scheduleKindSkipped,
		}, 0)
		s.diag.Log(ctx, port.LevelInfo, "skipped misfire (MisfireSkip)",
			"schedule", sched.Spec.Name)
		return
	}

	// fireClaimed runs the Fire→RecordFire tail (the shared path with FireNow)
	// and emits the fired/failed event via the callback. It does NOT do the
	// singleton check or misfire policy — those are this caller's prelude.
	// firesWG is tracked here so Stop's grace drain sees the in-flight fire.
	s.firesWG.Add(1)
	defer s.firesWG.Done()
	_, _ = s.fireClaimed(ctx, claimed, now)
}

// fireClaimed runs the Fire→RecordFire tail of a fire after the slot is Claimed.
// It is shared by the tick loop (fireOne) and the manual FireNow path. It does
// NOT do the singleton check or misfire policy — those are the caller's prelude.
// It emits the EvScheduleFired/Failed event via the configured emit callback (if
// any): EvScheduleFailed when the FireFunc errored OR the run ended with
// StopError (a run that drained to a terminal EvResult carrying StopError but
// no Go error — makeFireFunc returns (fire, nil) in that case, so keying the
// failed emit off fireErr alone would misclassify it as "fired"); EvScheduleFired
// on a completed fire (any non-Error stop). By the time fireClaimed returns,
// fire.Stop is populated (the FireFunc drives the run to terminal).
func (s *Scheduler) fireClaimed(ctx context.Context, claimed port.Schedule, now time.Time) (port.ScheduleFire, error) {
	fire, fireErr := s.cfg.Fire(ctx, claimed, now)
	if fireErr != nil {
		// A failed fire is recorded as StopError; the at-most-once Claim
		// already advanced NextFireAt, so it is NOT retried (the slot is gone —
		// decision #1).
		fire = port.ScheduleFire{
			ID:           fire.ID,
			ScheduleName: claimed.Spec.Name,
			SessionID:    fire.SessionID,
			FiredAt:      now,
			Stop:         session.StopError,
			Err:          fireErr.Error(),
		}
		s.diag.Log(ctx, port.LevelWarn, "scheduler: fire failed",
			"schedule", claimed.Spec.Name, "err", fireErr.Error())
	}
	// Emit the lifecycle event: "failed" when the FireFunc errored OR the run
	// ended with StopError (even when fireErr is nil — makeFireFunc returns
	// (fire, nil) for a run that drained to terminal EvResult{stop=StopError});
	// "fired" otherwise (a completed fire, any non-Error stop).
	var payload session.SchedulePayload
	if fireErr != nil || fire.Stop == session.StopError {
		payload = session.SchedulePayload{
			ScheduleName: claimed.Spec.Name,
			FireID:       fire.ID,
			SessionID:    fire.SessionID,
			Kind:         scheduleKindFailed,
			Stop:         fire.Stop,
			Err:          fire.Err,
		}
	} else {
		payload = session.SchedulePayload{
			ScheduleName: claimed.Spec.Name,
			FireID:       fire.ID,
			SessionID:    fire.SessionID,
			Kind:         scheduleKindFired,
			Stop:         fire.Stop,
			Err:          fire.Err,
		}
	}
	s.emitSchedule(payload)
	// Record the fire metrics (Claim→terminal duration). now is the Claim time
	// fireClaimed was called with; the run is now terminal, so time.Since(now)
	// is the end-to-end fire cost. Skipped fires (no run) record metrics with a
	// zero duration at their own call sites in fireOne/FireNow.
	s.emitScheduleMetrics(payload, s.cfg.Clock.Now().Sub(now))
	// RecordFire is idempotent per fire id; a transient failure is best-effort
	// (the fire already ran — we lose the outcome record, not the at-most-once
	// guarantee).
	if err := s.cfg.Store.RecordFire(ctx, fire); err != nil && !errors.Is(err, port.ErrScheduleNotFound) {
		s.diag.Log(ctx, port.LevelWarn, "scheduler: RecordFire failed",
			"schedule", claimed.Spec.Name, "fire", fire.ID, "err", err.Error())
	}
	return fire, fireErr
}

// emitSchedule invokes the optional EmitScheduleEvent callback (nil-safe). It is
// the single chokepoint for emitting an EvSchedule* payload — fireClaimed calls
// it for fired/failed, fireOne/FireNow call it for skipped. A nil callback is the
// byte-identical no-emit path.
func (s *Scheduler) emitSchedule(payload session.SchedulePayload) {
	if s.cfg.EmitScheduleEvent != nil {
		s.cfg.EmitScheduleEvent(payload)
	}
}

// emitScheduleMetrics invokes the optional ScheduleMetrics callback (nil-safe).
// It is the single chokepoint for recording schedule fire metrics —
// fireClaimed calls it with the Claim→terminal duration for a fired/failed
// fire, fireOne/FireNow call it with duration 0 for a skipped fire. A nil
// callback is the byte-identical no-metrics path. duration is the fire's
// wall-clock cost (time.Since(now)); a skipped fire passes 0 (no run).
func (s *Scheduler) emitScheduleMetrics(payload session.SchedulePayload, duration time.Duration) {
	if s.cfg.ScheduleMetrics != nil {
		s.cfg.ScheduleMetrics(payload, duration)
	}
}

// FireNow manually fires a schedule by name: it Loads the schedule, applies the
// singleton overlap check (the same trial-lease path fireOne uses), Claims the
// slot, and runs fireClaimed. It is the manual/ad-hoc fire path (a client or
// operator triggers a fire out-of-band from the tick loop). It returns the fire
// record (stop reason + any error) and emits the EvSchedule* event via the
// callback (fired/failed/skipped) exactly as the tick loop does.
//
// FAIL-CLOSED prelude (the caller's job, NOT the at-most-once Claim):
//   - a not-enabled (paused/done) schedule → ErrFireNowDisabled;
//   - an already-fired one-shot (FireCount > 0) → ErrFireNowExhausted;
//   - a singleton schedule whose prior fire is still running →
//     ErrFireNowOverlap (the slot is NOT claimed — the skip path, mirroring the
//     tick loop's singleton skip).
//
// A cron schedule that is due now or in the future is Claimed and fired. A cron
// whose NextFireAt is in the past is ALSO fired (the manual path is an explicit
// request — it does not apply the misfire policy, which is a tick-loop concern
// for polling cadence). The nextFire handed to ClaimNow is computed via
// computeNextFire (the same helper the tick loop uses).
func (s *Scheduler) FireNow(ctx context.Context, name string, now time.Time) (port.ScheduleFire, error) {
	// Per-schedule-name serialization: a concurrent FireNow (or a tick-loop
	// fireOne) on the SAME schedule must not race the Load→singleton→ClaimNow
	// fence (the TOCTOU the singleton guard closes — two concurrent FireNow RPCs
	// with different `now` values would both pass the singleton trial-lease and
	// both fire). Different schedule names stay parallel.
	defer s.lockFireName(name)()
	sched, err := s.cfg.Store.Load(ctx, name)
	if err != nil {
		return port.ScheduleFire{}, err
	}
	if !sched.State.Enabled {
		return port.ScheduleFire{}, ErrFireNowDisabled
	}
	// A one-shot that has already fired is exhausted (a one-shot fires once).
	if sched.Spec.Trigger.Kind() == port.TriggerOneShot && sched.State.FireCount > 0 {
		return port.ScheduleFire{}, ErrFireNowExhausted
	}
	// Singleton overlap check — the same trial-lease path fireOne uses. A held
	// prior-fire lease → skip (ErrFireNowOverlap), NOT claim (a claim would
	// clobber the live pointer, exactly the review-#189 finding the tick loop's
	// skip path avoids). The trial lease is released immediately.
	if sched.Spec.Singleton && s.cfg.Lease != nil && sched.State.LastFireSessionID != "" && sched.State.LastFireSessionID != port.PendingFireSessionID {
		overlap, rel := s.isPriorFireLive(ctx, sched.State.LastFireSessionID)
		if rel != nil {
			defer rel()
		}
		if overlap {
			s.emitSchedule(session.SchedulePayload{
				ScheduleName: sched.Spec.Name,
				Kind:         scheduleKindSkipped,
			})
			s.emitScheduleMetrics(session.SchedulePayload{
				ScheduleName: sched.Spec.Name,
				Kind:         scheduleKindSkipped,
			}, 0)
			return port.ScheduleFire{}, ErrFireNowOverlap
		}
	}
	nextFire, err := s.computeNextFire(sched, now)
	if err != nil {
		return port.ScheduleFire{}, fmt.Errorf("scheduler: fire-now %q: compute next fire: %w", name, err)
	}
	// ClaimNow is Claim WITHOUT the NextFireAt <= now due-check — a manual fire
	// bypasses the cadence but still claims atomically for at-most-once. The tick
	// loop's fireOne KEEPS using Claim (the due-check is correct for the poll
	// loop). ErrScheduleNotFound here means the slot was disabled/exhausted or a
	// concurrent Claim/ClaimNow at the same now already advanced it (the
	// at-most-once fence held).
	claimed, err := s.cfg.Store.ClaimNow(ctx, name, now, nextFire)
	if err != nil {
		return port.ScheduleFire{}, err
	}
	// fireClaimed runs Fire→RecordFire + emits the fired/failed event. It is
	// NOT tracked on firesWG (FireNow is a caller-driven synchronous fire, not
	// a tick-loop fan-out goroutine; Stop does not need to join it).
	return s.fireClaimed(ctx, claimed, now)
}

// computeNextFire returns the next fire instant strictly after `now` for the
// schedule's trigger, or the zero time if there is no further fire (a one-shot,
// or a cron whose MaxFires is exhausted after this Claim). It is parser-bearing
// (the store is parser-free): a cron trigger uses cronparse.NextFire; a
// one-shot yields zero (Claim disables it).
func (*Scheduler) computeNextFire(sched port.Schedule, now time.Time) (time.Time, error) {
	switch sched.Spec.Trigger.Kind() {
	case port.TriggerOneShot:
		// A one-shot fires once; the next fire is zero (Claim disables it).
		return time.Time{}, nil
	case port.TriggerCron:
		// If this Claim exhausts MaxFires, there is no next fire.
		if sched.Spec.MaxFires > 0 && sched.State.FireCount+1 >= sched.Spec.MaxFires {
			return time.Time{}, nil
		}
		loc := scheduleLocation(sched.Spec.Timezone)
		next, err := cronparse.NextFire(sched.Spec.Trigger.Cron, now, loc)
		if err != nil {
			return time.Time{}, err
		}
		return next, nil
	default:
		// TriggerNone or ambiguous: treat as no further fire (Claim will disable
		// it via the zero nextFire). A valid schedule never reaches here — the
		// create-seam Validates the trigger — but fail safe.
		return time.Time{}, nil
	}
}

// Drain arms the drain gate: in-flight fires complete, but no NEW fires start
// mid-tick. It mirrors Service.Drain: a shutting-down replica steers its
// tick-loop work to a survivor. It does NOT cancel in-flight fires (they
// complete or are cancelled by Stop's grace). It returns immediately.
func (s *Scheduler) Drain() {
	s.draining.Store(true)
}

// IsDraining reports whether the drain gate is armed.
func (s *Scheduler) IsDraining() bool {
	return s.draining.Load()
}

// Stop cancels the tick loop, waits for in-flight fires to drain (with a grace
// period), releases the leader lease (if held), and closes done. It is
// idempotent: a second call is a no-op that returns nil. It does NOT return
// until the tick goroutine and any in-flight fire goroutines have joined (or
// the grace elapses).
func (s *Scheduler) Stop() error {
	if !s.stopped.CompareAndSwap(false, true) {
		return nil
	}
	// Read the cancel funcs + done channels under s.mu (Start writes them under
	// the same lock, and declareLeaderLost may concurrently read tickCancel on
	// the renewer goroutine) — copy locally, then act on them OUTSIDE the lock
	// so a channel receive below never blocks while holding s.mu.
	s.mu.Lock()
	tickCancel, renewerCancel := s.tickCancel, s.renewerCancel
	tickDone, renewerDone := s.tickDone, s.renewerDone
	s.mu.Unlock()
	// Cancel the tick loop first so no new fires start.
	if tickCancel != nil {
		tickCancel()
	}
	// Stop the renewer so it does not race the release.
	if renewerCancel != nil {
		renewerCancel()
	}
	// Wait for the tick + renewer goroutines to fully exit so goleak / a
	// NumGoroutine check sees a clean shutdown. Both unwind on their ctx cancel
	// (the tick loop's select returns on ctx.Done(); the renewer likewise). A
	// nil channel (Start not called, or no renewer) skips the wait.
	if tickDone != nil {
		<-tickDone
	}
	if renewerDone != nil {
		<-renewerDone
	}
	// Join in-flight fires with a grace. After the grace, the tickCtx cancel
	// has already propagated to any fire whose ctx derives from it; the
	// firesWG.Wait completes once they unwind.
	waitDone := make(chan struct{})
	go func() {
		s.firesWG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(s.cfg.StopFireGrace):
		// Grace elapsed; in-flight fires whose ctx derives from the tick ctx
		// have been cancelled by tickCancel. Any fire that ignores ctx is
		// abandoned (it will wind down on its own; the at-most-once Claim
		// already advanced NextFireAt, so no double-fire).
		s.diag.Log(context.Background(), port.LevelWarn, "scheduler: Stop grace elapsed; abandoning in-flight fires")
	}
	// Release the leader lease (if held). Best-effort, cancel-detached
	// short-timeout ctx (the appendEvent / releaseLease precedent) so a
	// shutdown-cancelled ctx cannot abort the release.
	s.mu.Lock()
	lease := s.leaderLease
	s.leaderLease = nil
	s.mu.Unlock()
	if s.cfg.Lease != nil && lease != nil {
		relCtx, relCancel := context.WithTimeout(context.Background(), leaderLeaseAcquireTimeout)
		if err := s.cfg.Lease.Release(relCtx, *lease); err != nil {
			s.diag.Log(context.Background(), port.LevelWarn, "scheduler: leader lease release failed",
				"err", err.Error())
		}
		relCancel()
	}
	close(s.done)
	return nil
}

// Done returns a channel closed when Stop completes. Tests may wait on it to
// assert clean shutdown.
func (s *Scheduler) Done() <-chan struct{} {
	return s.done
}

// RunOnceForTest runs a single tick iteration synchronously (no ticker). It is
// the test seam for driving the loop deterministically: a test advances the
// fake clock and calls RunOnceForTest to express "one tick elapsed" without a
// real sleep. Production drives the loop via Start/Stop (the real ticker).
//
// If Start has been called, the iteration runs under the scheduler's OWN tick
// ctx (so a Stop cancels in-flight fires exactly as the real ticker would); if
// Start has NOT been called, it runs under the passed ctx (the deterministic
// at-most-once / misfire tests do not need Start).
func (s *Scheduler) RunOnceForTest(ctx context.Context) {
	eff := ctx
	s.mu.Lock()
	if s.tickCtx != nil {
		eff = s.tickCtx
	}
	s.mu.Unlock()
	s.tickOnce(eff)
}

// misfireGraceWindow is the lateness threshold beyond which a MisfireSkip slot
// is skipped rather than fired. A slot missed by less than this (ordinary poll
// jitter — Due returns a slot the first tick after NextFireAt, so a fresh slot
// is always at least slightly late) still fires; a slot missed by more (the
// process was down across one or more ticks) is the genuine misfire the skip
// policy targets. It is one tick interval — the coarsest grain at which the loop
// can distinguish "just became due" from "missed a scheduled window".
func (s *Scheduler) misfireGraceWindow() time.Duration {
	return s.cfg.TickInterval
}

// scheduleLocation loads the IANA timezone for a schedule's cron expression. An
// empty or invalid timezone falls back to UTC (fail-safe — UTC is the
// recommended default for infra schedules, avoiding the 1–3am DST danger zone).
// An invalid name is logged once (INFO) and the schedule fires in UTC.
//
// Exported as LoadLocation so composition's create-seam (internal/adapter/server)
// can compute the first NextFireAt for a cron schedule via the SAME tz helper the
// tick loop uses (DRY — one tz loader, not a composition-local mirror).
func scheduleLocation(tz string) *time.Location {
	return LoadLocation(tz)
}

// LoadLocation loads the IANA timezone for a schedule's cron expression. An empty
// or invalid timezone falls back to UTC (fail-safe). It is the exported seam for
// composition's create-seam (the first NextFireAt computation) so it shares the
// tick loop's tz loader rather than duplicating it.
func LoadLocation(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// isPriorFireLive does a TRIAL lease acquire on the prior fire's session id.
// ErrLeaseHeld means the prior fire is still running on some replica → the
// singleton guard skips this fire. Success (or ErrLeaseUnsupported / no lease
// backend) means the prior fire finished or crashed → fire freely. The trial
// lease is released immediately; the fire's own run-entry acquires its own
// session lease. Returns (overlap, releaseFunc) where releaseFunc is nil when
// no trial lease was acquired.
//
// The acquire is BOUNDED by singletonTrialTimeout (not the tick ctx) so a slow
// or unresponsive lease backend cannot block the fire path or starve the
// MaxConcurrentFires errgroup. A timeout is treated as a transient infra fault
// → fail-safe (fire; the Claim fence is the backstop).
func (s *Scheduler) isPriorFireLive(ctx context.Context, sessID session.SessionID) (bool, func()) {
	if s.cfg.Lease == nil {
		return false, nil // no lease backend — can't check cross-replica; fire (single-replica by affinity)
	}
	// Bounded ctx: a trial-lease acquire must not block the fire path
	// indefinitely. The tick ctx may be long-lived (only cancelled on Stop);
	// a slow backend would consume an errgroup slot for the whole duration.
	// Cut it short — this is a best-effort liveness check, not a hard gate.
	trialCtx, trialCancel := context.WithTimeout(ctx, singletonTrialTimeout)
	lease, err := s.cfg.Lease.Acquire(trialCtx, sessID, s.cfg.LeaseOwner+"-singleton-trial")
	trialCancel()
	if err != nil {
		if errors.Is(err, port.ErrLeaseHeld) {
			return true, nil // prior fire still running
		}
		if errors.Is(err, port.ErrLeaseUnsupported) {
			return false, nil // backend can't lease; fire (the Claim fence still holds within-process)
		}
		// Infra error or timeout — fail safe: don't skip (a transient fault
		// shouldn't suppress a fire). The at-most-once Claim fence is the
		// backstop.
		s.diag.Log(ctx, port.LevelWarn, "scheduler: singleton trial-lease acquire failed; firing (fail-safe)",
			"session", sessID, "err", err.Error())
		return false, nil
	}
	// Acquired — the prior fire is NOT live. Release immediately; the fire's
	// own run-entry will acquire its own session lease on the new session id.
	return false, func() {
		relCtx, relCancel := context.WithTimeout(context.Background(), leaderLeaseAcquireTimeout)
		defer relCancel()
		_ = s.cfg.Lease.Release(relCtx, lease)
	}
}
