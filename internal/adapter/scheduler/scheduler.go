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
// with subagent-grade RunRequest) in Phase 1f. A unit test supplies a stub
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
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// FireFunc is the composition-supplied callback a scheduler invokes for each
// claimed fire. It mints a fresh session (the "sched--" top-level session per
// fire, via Service.CreateSessionWithProfile + Service.StartRunContent with
// subagent-grade RunRequest), drives it to terminal, and returns the fire
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

// ErrNotLeader is returned by FireNow when this replica is not the scheduler
// leader (a multi-replica deployment where a peer holds the `__scheduler__`
// lease). A standby replica fails a manual fire fast rather than double-firing
// against the leader's tick loop. The caller maps it to FailedPrecondition and
// SHOULD surface the current leader (Scheduler.LeaderOwner) so a client can
// redirect. Nil-lease (single-replica) schedulers are always the leader.
var ErrNotLeader = errors.New("scheduler: not the leader (a peer replica is; retry against the leader)")

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
	// decides the kind; the callback decides where it lands. The ctx is the
	// firing caller's, so the durable append can attribute the event to whoever
	// acted (a tick fire descends from Start's system-principal root; a manual
	// FireNow keeps its requester) — ADR 0100 decision 5.
	EmitScheduleEvent func(ctx context.Context, payload session.SchedulePayload)
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
	// DeliverFireResult is the OPTIONAL composition-injected callback
	// (ADR 0075, fire-result-delivery) the scheduler invokes from fireClaimed
	// AFTER RecordFire, to route a fire's terminal result back into its origin
	// conversation. It is nil-safe (nil = the byte-identical no-delivery path,
	// matching the pre-ADR-0075 pull-only posture). Composition wires it to
	// deliverFireResult(svc, queue) which: skips an empty OriginSessionID (no
	// delivery), renders the note (renderFireDelivery), enqueues it to the
	// durable DeliveryQueue, and drives a delivery run into an idle/completed/
	// cancelled/failed origin via StartRunContent (loadAndReopen reopens/
	// recovers); a BUSY or AWAITING origin is left queued (the loop's Step 2a
	// drain records it at the next turn boundary); a deleted/child/sched--
	// origin degrades to pull-only with a WARN. A delivery error WARNs and
	// NEVER fails the fire (delivery is decoupled — the fire already recorded).
	// The scheduler invokes it for fired/failed fires alike (a failed fire may
	// still have an origin that should know it errored); the callback decides
	// whether to deliver based on the stop reason (it may skip a StopError
	// fire's delivery, or deliver it — the ADR does not mandate either).
	DeliverFireResult func(ctx context.Context, sched port.Schedule, fire port.ScheduleFire)
	// ReconcileStaleFire is the OPTIONAL composition-injected callback
	// (issue #386 Phase 4b, the stale-fire reconciler) the scheduler invokes
	// from the tick loop's reconcile scan when it DETECTS a stale in-flight
	// fire — a claimed-but-never-terminal fire left behind by a crashed
	// process (acceptance criterion #7). It is nil-safe (nil = the
	// byte-identical no-reconcile path, the pre-Phase-4b posture): detection
	// is store-only (the scheduler CAN do it — it reads ScheduleStore + the
	// leader-lease/isPriorFireLive seam it already has), but the SETTLE
	// (session-load + cancel + RecordFire) needs Service methods the scheduler
	// package must not import (the layering rule: the scheduler is
	// storage-agnostic and must NOT import internal/adapter/server). So the
	// reconcile callback is composition-injected, mirroring Fire/
	// DeliverFireResult/DeliverFireStarted.
	//
	// Two crash cases the detector hands the callback:
	//  1. Crash after Claim, before session creation: LastFireSessionID ==
	//     port.PendingFireSessionID (the sentinel) and stale (LastFireAt older
	//     than the stale window). No session exists. The callback records a
	//     terminal StopError fire ("fire lost: process crashed between claim
	//     and session creation") via RecordFire with a minted id, and clears
	//     the in-flight fields.
	//  2. Crash after session creation, before RecordFire: LastFireSessionID
	//     is a real "sched--" id, the fire record is still in-flight (Stop
	//     empty), the session's lease is released/expired (not live — the
	//     detector's isPriorFireLive trial-lease check acquired freely), and
	//     LastFireStartedAt is older than the stale window. The callback
	//     settles the terminal: marks the session snapshot cancelled
	//     (Interrupt-recoverable) and RecordFire-ing a StopError fire ("fire
	//     lost: process crashed during run").
	//
	// The detector must NOT flag a genuinely-live fire (lease held → skip), and
	// must NOT flag a freshly-claimed fire within the stale window (a fire
	// Claimed moments ago is in flight, not crashed). The callback is
	// idempotent: a fire already terminal is a no-op (RecordFire is idempotent
	// per fire id). Composition wires it over svc.GetSession/svc.Persist/
	// store.RecordFire (reusing settleFireTerminalSnapshot from #388 for the
	// session-settle).
	ReconcileStaleFire func(ctx context.Context, sched port.Schedule)
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
	// leaderStandbyBackoff is how long a non-leader (standby) replica waits
	// between leader-lease acquire attempts. Short enough that a standby takes
	// over soon after the leader's lease lapses (TTL), long enough to avoid
	// hammering the lease backend on contention.
	leaderStandbyBackoff = 2 * time.Second
)

// stopLeadershipJoinTimeout bounds how long Stop waits for the leadership loop
// and the current epoch's tick+renewer goroutines to join after being
// cancelled. It is a `var` (not a const) so a test can shrink it to assert Stop
// is bounded; production keeps the conservative default. A wedged goroutine
// that ignores its cancel ctx is abandoned (best-effort), never allowed to
// stall shutdown unboundedly.
var stopLeadershipJoinTimeout = 5 * time.Second

// staleFireWindow is the staleness threshold the stale-fire reconciler
// (issue #386 Phase 4b) applies to a claimed-but-never-terminal fire when the
// schedule has no explicit FireDeadline. A fire whose LastFireStartedAt (the
// crash-after-session case) or LastFireAt (the crash-after-Claim case, where
// LastFireStartedAt is zero — RecordFireStart never ran) is older than this
// window is a candidate for reconciliation. It is a package var (not a const)
// so an offline test can shrink it to exercise the reconcile path without
// waiting the full window; production keeps the conservative default. It is
// defaultFireTimeout-scale (the same posture as the per-fire wall-clock
// deadline) plus a grace so a genuinely-live slow fire is NOT flagged while
// its lease is still held (the lease check is the authoritative liveness
// oracle; this window is the fallback when there is no explicit deadline).
var staleFireWindow = defaultFireTimeoutScale + staleFireGrace

// defaultFireTimeoutScale mirrors the deployment-default per-fire wall-clock
// deadline (internal/app.defaultFireTimeout, issue #386). It is the scale of
// the stale-fire window so the two share one posture: a fire is "stale" past
// roughly the same horizon it would have been timed out by its watchdog. It is
// a package var so a test can shrink it alongside staleFireWindow.
const defaultFireTimeoutScale = 30 * time.Minute

// staleFireGrace is the headroom added to defaultFireTimeoutScale so a
// fire whose watchdog has NOT yet lapsed (a genuinely-live slow fire whose
// lease is still held) is not flagged by the window-based fallback. The lease
// check is authoritative; this grace only bounds the window-based branch.
const staleFireGrace = 5 * time.Minute

// Scheduler is the composition-layer owner of the scheduled-tasks tick loop.
// Construct one via New, then Start (which acquires the leader lease if a
// backend is wired and launches the tick + renewer goroutines), and Stop it at
// shutdown / drain.
type Scheduler struct {
	cfg  Config
	diag port.Diagnostics

	mu          sync.Mutex // protects the leader-lease + lifecycle state below
	leaderLease *port.Lease

	// isLeader is true while this replica holds the leader lease and is
	// ticking. Read under mu. A non-leader (standby) has it false and is not
	// ticking. Only meaningful when cfg.Lease != nil.
	isLeader bool

	// activeCancel cancels the CURRENT leadership epoch's tick+renewer
	// goroutines; activeDone is closed when BOTH have exited (via epochWg).
	// A demotion cancels the epoch; Stop cancels the current epoch. The
	// leadership loop waits activeDone before starting a new epoch so a
	// demote→promote never overlaps two tick loops.
	activeCancel context.CancelFunc
	activeDone   chan struct{} // nil until the first epoch starts
	epochWg      sync.WaitGroup

	// leadershipCancel drives the standby leadership loop (started by Start,
	// cancelled by Stop). leadershipDone is closed when the loop goroutine exits.
	leadershipCancel context.CancelFunc
	leadershipDone   chan struct{}

	// tickCtx is the current epoch's tick ctx; cancelling it stops ticking. It
	// is also cancelled by the renewer on a definitive leader-lease loss (a peer
	// took over — don't double-fire). Kept for RunOnceForTest + the
	// ErrScheduleUnsupported sticky stop.
	tickCtx    context.Context
	tickCancel context.CancelFunc

	draining atomic.Bool

	// stickyUnsupported is set (once) when the tick loop's Due returns
	// port.ErrScheduleUnsupported — the backend can never store schedules. The
	// leadership loop checks it and stops re-acquiring (without it, an epoch
	// cancelled by the tick loop would just loop back to acquireLeader and
	// churn forever — the sticky-disable must survive the epoch lifecycle).
	stickyUnsupported atomic.Bool

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
func (s *Scheduler) SetEmitScheduleEvent(cb func(ctx context.Context, payload session.SchedulePayload)) {
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

// SetDeliverFireResult wires the OPTIONAL composition-injected fire-result
// delivery callback (ADR 0075). Composition calls it after SetFire (so the
// FireFunc is bound) and before Start. nil is the byte-identical no-delivery
// path (the pre-ADR-0075 pull-only posture). The scheduler invokes it from
// fireClaimed AFTER RecordFire, with the schedule + the fire record.
func (s *Scheduler) SetDeliverFireResult(cb func(ctx context.Context, sched port.Schedule, fire port.ScheduleFire)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.Load() {
		panic("scheduler: SetDeliverFireResult after Start")
	}
	s.cfg.DeliverFireResult = cb
}

// SetReconcileStaleFire wires the OPTIONAL composition-injected stale-fire
// reconcile callback (issue #386 Phase 4b). Composition calls it after
// SetFire (so the FireFunc is bound) and before Start. nil is the
// byte-identical no-reconcile path (the pre-Phase-4b posture): the reconcile
// scan is a nil-safe skip when the callback is unwired. The scheduler invokes
// it from the tick loop's reconcile scan (reconcileStaleFires, called from
// tickOnce) for each detected stale in-flight fire, with the schedule whose
// LastFireSessionID/LastFireStartedAt mark it crashed.
func (s *Scheduler) SetReconcileStaleFire(cb func(ctx context.Context, sched port.Schedule)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.Load() {
		panic("scheduler: SetReconcileStaleFire after Start")
	}
	s.cfg.ReconcileStaleFire = cb
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

// Start launches the scheduler's lifecycle. It is INFALLIBLE-AT-LAUNCH: it
// never returns a leader-lease error. With no lease backend (cfg.Lease == nil,
// single-replica by affinity) it starts the tick loop directly. With a lease
// backend it launches a background LEADERSHIP LOOP that acquires the
// `__scheduler__` leader lease and keeps this replica in one of two states:
//
//   - LEADER: holds the lease, ticks + renews (a leadership EPOCH). On a
//     definitive lease loss (a peer took over) the epoch is torn down and the
//     replica returns to standby.
//   - STANDBY (non-leader): not ticking. Retries the acquire on a backoff
//     ticker and PROMOTES when the current leader's lease lapses (crash/TTL)
//     or is released. This is the multi-replica availability contract: a
//     standby replica must SERVE (report ready, answer RPCs) and take over
//     when the leader dies — it must NOT crash (the pre-ADR-0073 on-by-default
//     bug where a non-leader's Start returned ErrLeaseHeld and the process
//     exited, CrashLooping the replica).
//
// A genuine infrastructure fault on the acquire (NOT ErrLeaseHeld / not
// ErrLeaseUnsupported) is treated like contention: logged and retried on the
// same backoff — a wedged lease backend degrades scheduling to standby rather
// than crashing the process. Start is idempotent: a second call is a no-op.
// FireNow is GATED on leadership (ErrNotLeader) so a standby replica fails a
// manual fire fast rather than double-firing against the leader.
func (s *Scheduler) Start(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return nil
	}
	// The lifecycle root has no caller: every context the tick, fire, delivery
	// and reconcile paths use descends from here (context.WithoutCancel keeps
	// values), so this ONE wrap runs them all as the explicit system principal
	// (ADR 0100 decision 7). FireNow is deliberately NOT wrapped — a manual fire
	// keeps its requester's identity.
	ctx = syscaller.Context(ctx, syscaller.RootScheduler)
	if s.cfg.Fire == nil {
		s.started.Store(false)
		panic("scheduler: Start called before SetFire (Fire is nil)")
	}

	if s.cfg.Lease == nil {
		// No lease backend: single-replica by affinity — tick directly.
		s.startEpoch(context.WithoutCancel(ctx), port.Lease{}, false)
		return nil
	}

	// Lease backend: run the standby leadership loop.
	lCtx, lCancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	s.mu.Lock()
	s.leadershipCancel = lCancel
	s.leadershipDone = done
	s.mu.Unlock()
	go func() {
		defer close(done)
		s.leadershipLoop(lCtx)
	}()
	return nil
}

// leadershipLoop is the standby acquire→epoch→retry cycle. It runs until Stop
// cancels ctx. Each iteration: try to acquire the leader lease; on success run
// a leadership epoch (tick + renew) until the lease is lost or Stop; on
// contention/unsupported/transient fault, wait a backoff and retry. A lost
// epoch (demote) loops straight back to the acquire so a deposed former leader
// can win a later epoch.
func (s *Scheduler) leadershipLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		// Sticky-disable: the tick loop found the store can never store
		// schedules. Do not re-acquire / churn a fresh epoch — stand down for
		// good (the leadership loop exits; Stop still joins cleanly).
		if s.stickyUnsupported.Load() {
			return
		}
		// Wait for any prior epoch's goroutines to fully exit before starting a
		// new one (a demote→promote must never overlap two tick loops).
		s.waitEpoch()

		lease, standalone, err := s.acquireLeader(ctx)
		switch {
		case err == nil:
			// Acquired: run the epoch (blocks until demote or Stop).
			s.runEpoch(ctx, lease)
			continue
		case standalone:
			// ErrLeaseUnsupported: the backend can never lease. Tick standalone
			// (single-replica by affinity) until Stop — no leader gate exists.
			s.diag.Log(ctx, port.LevelInfo, "leader lease unsupported by backend; running standalone (no cross-replica leader gate)",
				"owner", s.cfg.LeaseOwner)
			s.startEpoch(ctx, port.Lease{}, false)
			<-ctx.Done() // standalone runs until Stop; no retry
			return
		default:
			// ErrLeaseHeld (a peer leads) or a transient infra fault: standby.
			if errors.Is(err, port.ErrLeaseHeld) {
				s.diag.Log(ctx, port.LevelInfo, "another replica is the scheduler leader; standing by",
					"owner", s.cfg.LeaseOwner)
			} else {
				s.diag.Log(ctx, port.LevelWarn, "scheduler: leader lease acquire failed; retrying in standby",
					"owner", s.cfg.LeaseOwner, "err", err.Error())
			}
			// Jittered backoff: on a leader's death N standbys would otherwise
			// re-acquire in lockstep (a thundering-herd on the lease backend).
			if !s.sleepOrDone(ctx, jitteredBackoff()) {
				return
			}
		}
	}
}

// jitteredBackoff returns the standby backoff with ±25% uniform jitter so a
// fleet of standby replicas does not retry the leader-lease acquire in
// lockstep after a leader's lease lapses.
func jitteredBackoff() time.Duration {
	// leaderStandbyBackoff in [0.75x, 1.25x]. math/rand/v2 is the right rand
	// here (timing jitter, not a security decision — the llmresilience
	// precedent).
	j := rand.Float64()*0.5 + 0.75 //nolint:gosec // timing jitter, not crypto
	return time.Duration(float64(leaderStandbyBackoff) * j)
}

// runEpoch starts a leadership epoch (tick + renew goroutines) and blocks until
// the epoch ends (a definitive lease loss demotes it, or Stop cancels ctx) and
// its goroutines have exited. It then releases the lease (best-effort) unless
// it was already lost.
func (s *Scheduler) runEpoch(ctx context.Context, lease port.Lease) {
	s.startEpoch(ctx, lease, true)
	// Block until the epoch's tick loop exits (demote cancels it) or Stop.
	s.mu.Lock()
	done := s.activeDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
	// The renewer released nothing on loss; release a still-held lease
	// best-effort (a clean Stop, not a demote).
	s.releaseLeader()
}

// startEpoch launches the tick loop (+ the renewer when renew=true). It is the
// single epoch-start path used by Start (no-lease), leadershipLoop (standalone),
// and runEpoch (leader). The epoch ctx is detached so a request-scope cancel
// does not stop it — only demote/Stop does.
func (s *Scheduler) startEpoch(ctx context.Context, lease port.Lease, renew bool) {
	epochCtx, epochCancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	s.mu.Lock()
	s.tickCtx, s.tickCancel = epochCtx, epochCancel
	s.activeCancel = epochCancel
	s.activeDone = done
	if renew {
		s.leaderLease = &lease
		s.isLeader = true
	}
	s.mu.Unlock()

	// Add(1) per goroutine actually started (the tick always runs; the renewer
	// only on a leader epoch). The watcher closes done once every started
	// goroutine has exited.
	s.epochWg.Add(1)
	go func() {
		defer s.epochWg.Done()
		s.tick(epochCtx)
	}()
	if renew {
		s.epochWg.Add(1)
		go func() {
			defer s.epochWg.Done()
			s.renewLeader(epochCtx)
		}()
	}
	go func() {
		s.epochWg.Wait()
		close(done)
	}()
}

// demote tears down the current epoch on a definitive leader-lease loss: it
// clears leadership and cancels the epoch ctx so the tick + renewer exit. The
// leadership loop's runEpoch observes activeDone and returns to standby.
func (s *Scheduler) demote() {
	s.mu.Lock()
	s.isLeader = false
	s.leaderLease = nil
	cancel := s.activeCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// waitEpoch blocks until the current epoch's goroutines have exited (no-op if
// none started). Called by the leadership loop before a fresh acquire.
func (s *Scheduler) waitEpoch() {
	s.mu.Lock()
	done := s.activeDone
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

// releaseLeader demotes (stopping the current epoch so no new fires start on a
// lease we are about to give up) and releases a still-held leader lease,
// best-effort, on a cancel-detached short-timeout ctx. No-op if not the leader.
// The demote BEFORE the release closes the window where a standby could promote
// and tick while our epoch was still firing (the demote cancels the epoch ctx
// first; the release then frees the lease only after our tick loop is stopping).
func (s *Scheduler) releaseLeader() {
	s.demote()
	s.mu.Lock()
	lease := s.leaderLease
	s.leaderLease = nil
	s.isLeader = false
	s.mu.Unlock()
	if s.cfg.Lease != nil && lease != nil {
		relCtx, relCancel := context.WithTimeout(context.Background(), leaderLeaseAcquireTimeout)
		if err := s.cfg.Lease.Release(relCtx, *lease); err != nil {
			s.diag.Log(context.Background(), port.LevelWarn, "scheduler: leader lease release failed",
				"err", err.Error())
		}
		relCancel()
	}
}

// sleepOrDone waits d or returns false if ctx is done first.
func (*Scheduler) sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// acquireLeader attempts one __scheduler__ leader-lease acquire. It returns:
//   - (lease, false, nil) on a successful Acquire;
//   - (zero, true, nil) on ErrLeaseUnsupported — the backend can never lease
//     (tick standalone);
//   - (zero, false, err) on ErrLeaseHeld (a peer leads — standby) or any other
//     error (a transient infra fault — standby + retry, NOT a fatal start
//     error: a wedged lease backend degrades scheduling, it must not crash the
//     process).
func (s *Scheduler) acquireLeader(ctx context.Context) (port.Lease, bool, error) {
	acqCtx, acqCancel := context.WithTimeout(ctx, leaderLeaseAcquireTimeout)
	defer acqCancel()
	lease, err := s.cfg.Lease.Acquire(acqCtx, port.SchedulerLeaderLeaseID, s.cfg.LeaseOwner)
	switch {
	case errors.Is(err, port.ErrLeaseUnsupported):
		return port.Lease{}, true, nil
	case errors.Is(err, port.ErrLeaseHeld):
		return port.Lease{}, false, err
	case err != nil:
		return port.Lease{}, false, fmt.Errorf("scheduler: acquire leader lease: %w", err)
	}
	return lease, false, nil
}

// LeaderOwner reports whether this replica may fire (it is the scheduler
// leader, or there is no leader gate at all) and, if so, its lease-owner
// identity. It backs the FireNow not-leader redirect surface (a standby
// replica names the leader a client should retry against). A scheduler with no
// lease backend (single-replica by affinity) has NO leader gate, so it always
// reports leader=true. A lease-backed scheduler that has not been Started (the
// unit-test direct-FireNow path) has no standby epoch yet, so it also reports
// leader=true — the gate bites only for a STARTED lease-backed scheduler
// currently in standby.
func (s *Scheduler) LeaderOwner() (owner string, leader bool) {
	if s.cfg.Lease == nil || !s.started.Load() {
		return s.cfg.LeaseOwner, true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isLeader {
		return s.cfg.LeaseOwner, true
	}
	return "", false
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
				// Definitive loss: a competitor holds it now. Demote (stop
				// ticking) so we do not double-fire against the new leader; the
				// leadership loop returns this replica to standby.
				s.diag.Log(renewCtx, port.LevelWarn, "lost scheduler leader lease; demoting to standby",
					"owner", s.cfg.LeaseOwner, "err", err.Error())
				s.demote()
				return
			case err != nil:
				// Transient/infra fault: keep the lease unless we are within one
				// renew interval of expiry (the next tick would land past it).
				if s.cfg.Clock.Now().Add(s.cfg.LeaseRenewInterval).Before(lease.Expiry) {
					continue // still have headroom; retry next tick.
				}
				s.diag.Log(renewCtx, port.LevelWarn, "lost scheduler leader lease; demoting to standby",
					"owner", s.cfg.LeaseOwner, "err", err.Error())
				s.demote()
				return
			}
			s.mu.Lock()
			s.leaderLease = &refreshed
			s.mu.Unlock()
		}
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
	// Leadership belt-and-braces: a demote cancels the epoch ctx, but a tick can
	// already be in flight past the ctx check. A lease-backed scheduler that is
	// not (or no longer) the leader must not poll Due / fire — the standby's
	// only job is to wait for leadership. Nil-lease (single-replica) and
	// not-yet-Started (direct RunOnceForTest unit tests) schedulers have no
	// leader gate, so they always pass.
	if _, leader := s.LeaderOwner(); !leader {
		return
	}
	now := s.cfg.Clock.Now()
	due, err := s.cfg.Store.Due(ctx, now)
	if err != nil {
		if errors.Is(err, port.ErrScheduleUnsupported) {
			// The backend can never store schedules — stop ticking (the seam
			// will never work here) AND stickily disable re-acquisition so the
			// leadership loop does not churn a fresh epoch every TickInterval
			// (the sticky-disable must outlive the epoch lifecycle).
			s.diag.Log(ctx, port.LevelInfo, "schedule store unsupported by backend; stopping scheduler",
				"err", err.Error())
			s.stickyUnsupported.Store(true)
			if s.tickCancel != nil {
				s.tickCancel()
			}
			return
		}
		s.diag.Log(ctx, port.LevelWarn, "schedule store Due failed", "err", err.Error())
		return
	}
	// One-shot crash-loss retry (ADR 0059 Phase 2). A one-shot with
	// OneShotRetry=true that Claim disabled (the at-most-once advance) but never
	// recorded a successful outcome (a crash mid-fire, or a fire that ended
	// StopError) is re-armed up to OneShotMaxRetries times. The re-arm path scans
	// ALL schedules (List) — a disabled one-shot is NOT returned by Due (Due
	// filters Enabled=true), so this is a separate scan. The re-arm check is in
	// the tick loop's scan, NOT in the fire path itself (the fire path knows
	// nothing of re-arm — it only fires what Claim advanced). A store that does
	// not implement ScheduleOneShotReArmer degrades to at-most-once
	// (byte-identical pre-Phase-2).
	//
	// This runs on EVERY tick, BEFORE the len(due)==0 early return — a quiet
	// one-shot-only deployment (no due cron to "spark" the tick past the early
	// return) must still re-arm a crashed one-shot. Without this ordering a
	// crashed one-shot in a quiet deployment stalls indefinitely.
	s.maybeReArmOneShots(ctx, now)
	// Stale-fire reconciliation (issue #386 Phase 4b, acceptance criterion #7):
	// scan for claimed-but-never-terminal fires left behind by a crashed
	// process and settle them via the composition-injected callback. Runs on
	// EVERY tick, BEFORE the len(due)==0 early return — a quiet deployment
	// (no due cron) must still reconcile a crashed fire. Nil callback = the
	// byte-identical no-reconcile path (pre-Phase-4b posture). See
	// reconcileStaleFires.
	s.reconcileStaleFires(ctx, now)
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
			s.emitSchedule(ctx, session.SchedulePayload{
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
		s.emitSchedule(ctx, session.SchedulePayload{
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
	s.emitSchedule(ctx, payload)
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
	// Fire-result delivery (ADR 0075, fire-result-delivery): AFTER RecordFire,
	// route the fire's terminal result back into its origin conversation. The
	// callback is composition-injected (DeliverFireResult); nil is the
	// byte-identical no-delivery path (the pre-ADR-0075 pull-only posture). A
	// delivery error WARNs inside the callback and NEVER fails the fire — the
	// fire is already recorded; delivery is a decoupled side-channel.
	if s.cfg.DeliverFireResult != nil {
		s.cfg.DeliverFireResult(ctx, claimed, fire)
	}
	return fire, fireErr
}

// emitSchedule invokes the optional EmitScheduleEvent callback (nil-safe). It is
// the single chokepoint for emitting an EvSchedule* payload — fireClaimed calls
// it for fired/failed, fireOne/FireNow call it for skipped. A nil callback is the
// byte-identical no-emit path.
func (s *Scheduler) emitSchedule(ctx context.Context, payload session.SchedulePayload) {
	if s.cfg.EmitScheduleEvent != nil {
		s.cfg.EmitScheduleEvent(ctx, payload)
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
	// Leadership gate: in a multi-replica deployment only the leader fires (a
	// standby must not double-fire against the leader's tick loop). Fail fast
	// with ErrNotLeader so the caller can redirect to the leader. A nil-lease
	// (single-replica) scheduler is always the leader.
	if _, leader := s.LeaderOwner(); !leader {
		return port.ScheduleFire{}, ErrNotLeader
	}
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
			s.emitSchedule(ctx, session.SchedulePayload{
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

// oneShotReArmBackoff is the delay applied before a crashed one-shot is re-armed.
// It is a small backoff so a crash-loop does not hammer the provider: the re-arm
// sets NextFireAt to now + this backoff, and Due returns it on the next tick
// after the backoff elapses.
const oneShotReArmBackoff = 1 * time.Minute

// maybeReArmOneShots is the one-shot crash-loss retry path (ADR 0059 Phase 2).
// It scans ALL schedules (List) for a disabled one-shot with OneShotRetry=true
// that did not record a successful outcome, and re-arms it (up to
// OneShotMaxRetries). The re-arm lives in the tick loop's scan, NOT in the fire
// path itself. Two crash sub-cases trigger a re-arm:
//  1. LastFireSessionID == PendingFireSessionID (crash before RecordFire — the
//     session may or may not exist; the sentinel means Claim happened but
//     RecordFire did not).
//  2. LoadFire(LastFireSessionID) returns Stop==StopError (the fire ran but
//     ended in error).
//
// If neither condition (the fire succeeded), the one-shot is NOT re-armed (it was
// a successful one-shot, now done). If OneShotRetryCount >= OneShotMaxRetries the
// re-arm is NOT attempted (the retry budget is exhausted; the one-shot is
// permanently done). A store that does not implement ScheduleOneShotReArmer
// degrades to at-most-once (byte-identical pre-Phase-2) — the scan is skipped.
func (s *Scheduler) maybeReArmOneShots(ctx context.Context, now time.Time) {
	reArmer, ok := s.cfg.Store.(port.ScheduleOneShotReArmer)
	if !ok {
		return // store does not implement the re-arm seam — at-most-once.
	}
	all, err := s.cfg.Store.List(ctx)
	if err != nil {
		if errors.Is(err, port.ErrScheduleUnsupported) {
			return // the same sticky-disable tickOnce already applied.
		}
		s.diag.Log(ctx, port.LevelWarn, "scheduler: List for one-shot re-arm failed", "err", err.Error())
		return
	}
	for _, sched := range all {
		if !s.shouldReArmOneShot(ctx, sched) {
			continue
		}
		// The re-arm backoff. A crashed one-shot is re-armed with NextFireAt =
		// now + backoff so a crash-loop does not hammer the provider.
		nextFire := now.Add(oneShotReArmBackoff)
		if err := reArmer.ReArmOneShot(ctx, sched.Spec.Name, nextFire); err != nil {
			// ErrScheduleNotFound: the schedule was deleted between List and
			// ReArmOneShot — the fence worked, not an error.
			if errors.Is(err, port.ErrScheduleNotFound) {
				continue
			}
			s.diag.Log(ctx, port.LevelWarn, "scheduler: re-arm one-shot failed",
				"schedule", sched.Spec.Name, "err", err.Error())
			continue
		}
		s.diag.Log(ctx, port.LevelInfo, "scheduler: re-armed one-shot (crash-loss retry)",
			"schedule", sched.Spec.Name,
			"retry_count", sched.State.OneShotRetryCount+1,
			"max_retries", sched.Spec.OneShotMaxRetries,
			"next_fire", nextFire)
	}
}

// shouldReArmOneShot reports whether the given schedule is a crashed one-shot
// that should be re-armed. It encodes the two crash sub-cases and the
// retry-budget gate.
func (s *Scheduler) shouldReArmOneShot(ctx context.Context, sched port.Schedule) bool {
	// Only a one-shot with OneShotRetry=true is a candidate.
	if sched.Spec.Trigger.Kind() != port.TriggerOneShot || !sched.Spec.OneShotRetry {
		return false
	}
	// A schedule that is still Enabled was NOT disabled by Claim (it is either
	// pending its first fire, or mid-fire). The re-arm path targets a DISABLED
	// one-shot (Claim disabled it — the at-most-once advance). An Enabled
	// one-shot is Due's concern, not the re-arm path's.
	if sched.State.Enabled {
		return false
	}
	// The retry-budget gate: if OneShotRetryCount already exceeds the budget, the
	// one-shot is permanently done (the retry budget is exhausted).
	if sched.State.OneShotRetryCount >= sched.Spec.OneShotMaxRetries {
		return false
	}
	// Crash sub-case 1: LastFireSessionID is still the pending sentinel (crash
	// before RecordFire). Claim stamped the sentinel; RecordFire never
	// overwrote it with a real session id. Re-arm.
	if sched.State.LastFireSessionID == port.PendingFireSessionID {
		return true
	}
	// Crash sub-case 2: the fire recorded a StopError (the fire ran but ended
	// in error). LoadFire probes the prior fire's outcome. A not-found fire
	// record (RecordFire never ran, but the sentinel was overwritten — an edge
	// case) is treated as a crash → re-arm (fail-safe toward retry, not silent
	// loss). A fire with Stop==StopError → re-arm. A fire with any other stop
	// (the fire succeeded) → do NOT re-arm.
	if sched.State.LastFireSessionID == "" {
		// No prior fire at all (the one-shot was disabled without a Claim —
		// e.g. paused). Not a re-arm candidate.
		return false
	}
	loadCtx, cancel := context.WithTimeout(ctx, singletonTrialTimeout)
	defer cancel()
	fire, err := s.cfg.Store.LoadFire(loadCtx, string(sched.State.LastFireSessionID))
	if err != nil {
		// A not-found fire record: RecordFire never ran. The sentinel was
		// overwritten with a real session id that has no fire record — treat as
		// a crash → re-arm (fail-safe toward retry).
		if errors.Is(err, port.ErrScheduleNotFound) {
			return true
		}
		// An infra error probing the fire: do NOT re-arm (fail-safe toward
		// at-most-once — a transient fault should not trigger a retry). The
		// next tick's List will re-probe.
		return false
	}
	return fire.Stop == session.StopError
}

// reconcileStaleFires is the stale-fire reconciler scan (issue #386 Phase 4b,
// acceptance criterion #7). It scans ALL schedules (List) for a
// claimed-but-never-terminal fire left behind by a crashed process and hands
// each detected stale one to the composition-injected ReconcileStaleFire
// callback, which settles it (RecordFire a terminal StopError fire + settle
// the session for the crash-after-session case). It is the DETECTION layer
// only — store + the leader-lease/isPriorFireLive seam it already has; the
// SETTLE (session-load + cancel) needs Service methods the scheduler package
// must not import (the layering rule), so it delegates to composition.
//
// Two crash cases the detector flags:
//
//  1. Crash after Claim, before session creation: LastFireSessionID ==
//     port.PendingFireSessionID (the sentinel Claim stamps, RecordFireStart
//     overwrites with the real id) and stale — LastFireAt (the Claim instant;
//     LastFireStartedAt is zero here because RecordFireStart never ran) is
//     older than the stale window. No session exists.
//
//  2. Crash after session creation, before RecordFire: LastFireSessionID is a
//     real "sched--" id (not the pending sentinel), the fire is still
//     in-flight (LastFireStartedAt is set — RecordFireStart ran, RecordFire
//     did not clear it), LastFireStartedAt is older than the stale window, AND
//     the prior-fire lease is NOT live (the isPriorFireLive trial-lease
//     acquired freely — the crashed process's session lease lapsed). A
//     genuinely-live fire (lease held by the running process) is NOT flagged.
//
// The stale window: reuse LastFireStartedAt + FireDeadline when FireDeadline
// is set (a fire whose explicit deadline has lapsed is stale), else
// LastFireStartedAt (or LastFireAt for the pending case) + the package-level
// staleFireWindow (defaultFireTimeout-scale + a grace). A freshly-claimed fire
// within the window is NOT flagged (it is in flight, not crashed).
//
// Nil callback = the byte-identical no-reconcile path (pre-Phase-4b posture):
// the scan is a nil-safe skip. Detection must NOT flag a genuinely-live fire
// (lease held → skip) and must NOT race a fire that is concurrently being
// recorded by a peer (the lease check is the authoritative liveness oracle;
// the window is the fallback).
func (s *Scheduler) reconcileStaleFires(ctx context.Context, now time.Time) {
	if s.cfg.ReconcileStaleFire == nil {
		return // byte-identical no-reconcile path (pre-Phase-4b posture).
	}
	all, err := s.cfg.Store.List(ctx)
	if err != nil {
		if errors.Is(err, port.ErrScheduleUnsupported) {
			return // the same sticky-disable tickOnce already applied.
		}
		s.diag.Log(ctx, port.LevelWarn, "scheduler: List for stale-fire reconcile failed", "err", err.Error())
		return
	}
	for _, sched := range all {
		if !s.shouldReconcileStaleFire(ctx, sched, now) {
			continue
		}
		// Hand the stale schedule to composition for the settle. The callback
		// is idempotent (RecordFire is idempotent per fire id); a transient
		// settle failure WARNs inside the callback and never fails the tick.
		s.cfg.ReconcileStaleFire(ctx, sched)
	}
}

// shouldReconcileStaleFire reports whether the given schedule has a
// claimed-but-never-terminal fire that is stale (older than the stale window)
// and whose prior-fire lease is NOT live — i.e. a crashed process left it
// behind. It encodes the two crash sub-cases of reconcileStaleFires and the
// liveness gate (a genuinely-live fire is NOT flagged). It is the per-schedule
// detector; reconcileStaleFires is the scan.
func (s *Scheduler) shouldReconcileStaleFire(ctx context.Context, sched port.Schedule, now time.Time) bool {
	// Crash sub-case 1: pending sentinel (crash after Claim, before session
	// creation). RecordFireStart never ran, so LastFireStartedAt is zero — the
	// staleness anchor is LastFireAt (the Claim instant). The window fallback
	// applies (no FireDeadline for a fire that never started its run).
	if sched.State.LastFireSessionID == port.PendingFireSessionID {
		anchor := sched.State.LastFireAt
		if anchor.IsZero() {
			return false // no Claim recorded — not a crashed fire (a fresh schedule).
		}
		return now.Sub(anchor) > staleFireThreshold(sched, anchor)
	}
	// Crash sub-case 2: a real in-flight fire (crash after session creation,
	// before RecordFire). LastFireSessionID is a real "sched--" id, the fire is
	// still in-flight (LastFireStartedAt set — RecordFireStart ran, RecordFire
	// did not clear it). The lease check is the authoritative liveness oracle:
	// a held lease (the fire is genuinely running) → NOT stale (skip). The
	// window (FireDeadline when set, else staleFireWindow) bounds the fallback.
	if sched.State.LastFireSessionID == "" {
		return false // no prior fire at all.
	}
	// Only an IN-FLIGHT fire is a candidate: LastFireStartedAt set (RecordFireStart
	// ran) — a terminal fire has it cleared by RecordFire.
	if sched.State.LastFireStartedAt.IsZero() {
		return false // the prior fire already recorded terminal (RecordFire cleared it).
	}
	// Staleness: the in-flight fire's start is older than the stale window
	// (or its explicit FireDeadline has lapsed).
	anchor := sched.State.LastFireStartedAt
	if now.Sub(anchor) <= staleFireThreshold(sched, anchor) {
		return false // within the window — in flight, not crashed.
	}
	// Liveness gate: a genuinely-live fire (lease held by the running process)
	// is NOT stale. The isPriorFireLive trial-lease check acquires freely when
	// the crashed process's session lease lapsed; ErrLeaseHeld means the fire
	// is still running → skip. No lease backend (single-replica by affinity) →
	// the window is the only oracle (a stale in-flight fire with no lease
	// backend IS stale — there is no cross-process lease to hold).
	if s.cfg.Lease != nil {
		overlap, rel := s.isPriorFireLive(ctx, sched.State.LastFireSessionID)
		if rel != nil {
			defer rel() // release the trial lease.
		}
		if overlap {
			return false // the fire is genuinely running — not stale.
		}
	}
	return true
}

// staleFireThreshold returns the staleness horizon for a fire: the explicit
// FireDeadline (when set) relative to the anchor, else the package-level
// staleFireWindow. The anchor is LastFireStartedAt for an in-flight fire
// (sub-case 2) or LastFireAt for a pending fire (sub-case 1, where
// LastFireStartedAt is zero). When FireDeadline is set, the threshold is
// FireDeadline - anchor (the remaining time until the explicit deadline); a
// non-positive result means the deadline already lapsed, so the fire is stale
// regardless of the window. The caller compares now.Sub(anchor) > threshold.
func staleFireThreshold(sched port.Schedule, anchor time.Time) time.Duration {
	if !sched.State.FireDeadline.IsZero() {
		// The explicit deadline is the authoritative horizon. A fire past its
		// deadline is stale (the watchdog would have terminated it).
		d := sched.State.FireDeadline.Sub(anchor)
		if d < 0 {
			return 0 // deadline already lapsed — any now past anchor is stale.
		}
		return d
	}
	return staleFireWindow
}

// Stop cancels the leadership loop and the current epoch (tick + renewer),
// waits for in-flight fires to drain (with a grace period), releases the leader
// lease (if held), and closes done. It is idempotent: a second call is a no-op
// that returns nil. The leadership-loop and epoch joins are BOUNDED (a goroutine
// that ignores its cancel ctx is abandoned after stopLeadershipJoinTimeout,
// never allowed to stall shutdown unboundedly); the fire-grace wait and lease
// release are likewise bounded.
func (s *Scheduler) Stop() error {
	if !s.stopped.CompareAndSwap(false, true) {
		return nil
	}
	// Cancel the leadership loop first so no new epoch starts, then the current
	// epoch so the tick + renewer exit. Read the cancel funcs + done channels
	// under s.mu, then act on them OUTSIDE the lock so a channel receive below
	// never blocks while holding s.mu.
	s.mu.Lock()
	leadershipCancel, leadershipDone := s.leadershipCancel, s.leadershipDone
	activeCancel, activeDone := s.activeCancel, s.activeDone
	s.mu.Unlock()
	if leadershipCancel != nil {
		leadershipCancel()
	}
	if activeCancel != nil {
		activeCancel()
	}
	// Wait for the leadership loop and the current epoch's goroutines to fully
	// exit so goleak / a NumGoroutine check sees a clean shutdown. Nil channels
	// (Start not called, or no lease backend) skip the wait. The leadership
	// loop's own runEpoch also waits activeDone, so join it first to avoid a
	// doubly-consumed close (a closed channel receive is safe to repeat, but
	// ordering Stop after the loop keeps the lifecycle linear). Each join is
	// BOUNDED by stopLeadershipJoinTimeout so a goroutine that ignores its
	// cancel ctx (e.g. a store whose Due blocks past ctx cancellation) cannot
	// stall shutdown unboundedly; on timeout the goroutine is abandoned
	// (best-effort) and a WARN is logged through the injected diagnostics.
	if leadershipDone != nil {
		select {
		case <-leadershipDone:
		case <-time.After(stopLeadershipJoinTimeout):
			s.diag.Log(context.Background(), port.LevelWarn, "scheduler: leadership loop join timeout; abandoning")
		}
	}
	if activeDone != nil {
		select {
		case <-activeDone:
		case <-time.After(stopLeadershipJoinTimeout):
			s.diag.Log(context.Background(), port.LevelWarn, "scheduler: active epoch join timeout; abandoning")
		}
	}
	// Join in-flight fires with a grace. After the grace, the epoch-ctx cancel
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
		// Grace elapsed; in-flight fires whose ctx derives from the epoch ctx
		// have been cancelled by activeCancel. Any fire that ignores ctx is
		// abandoned (it will wind down on its own; the at-most-once Claim
		// already advanced NextFireAt, so no double-fire).
		s.diag.Log(context.Background(), port.LevelWarn, "scheduler: Stop grace elapsed; abandoning in-flight fires")
	}
	// Release the leader lease (if still held). Best-effort, cancel-detached
	// short-timeout ctx (the appendEvent / releaseLease precedent) so a
	// shutdown-cancelled ctx cannot abort the release.
	s.releaseLeader()
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
