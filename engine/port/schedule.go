package port

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// ErrScheduleNotFound is the port-level sentinel a ScheduleStore wraps (with %w)
// in Load/Delete/Claim/RecordFire/LoadFire when no schedule (or fire) exists under
// the requested name/id — distinct from a genuine infrastructure failure (I/O error,
// decode failure). It mirrors ErrSessionNotFound: a consumer in a layer that may NOT
// import the store adapters distinguishes "no such schedule" from "the store is
// broken" via errors.Is. Every ScheduleStore adapter MUST wrap this for the
// not-found case.
var ErrScheduleNotFound = errors.New("port: schedule not found")

// ErrScheduleUnsupported is the sentinel a ScheduleStore returns (wrapped with %w)
// when the backend cannot store schedules at all — e.g. a remote driver answering
// UNIMPLEMENTED, or an in-memory default that opts out. It is the "this seam will
// never work here" signal, distinct from a transient infrastructure failure (I/O
// error, timeout): a consumer that sees it via errors.Is should stop consulting the
// seam (composition logs one INFO and stickily disables scheduling for the process,
// degrading to the byte-identical no-schedule path). This mirrors
// ErrLeaseUnsupported / ErrPruneUnsupported's sticky-disable contract.
var ErrScheduleUnsupported = errors.New("port: scheduled tasks not supported by this backend")

// ErrFireNowOverlap is the port-level sentinel a ScheduleManager's FireNow
// returns (wrapped with %w) when the schedule's singleton guard found a prior
// fire still running — the manual fire is REJECTED, not run concurrently with
// the in-flight one (the create-seam's default-true Singleton holds through
// every surface: the REST/gRPC FireNow AND the model-facing Schedule tool). A
// consumer in a layer that may NOT import the scheduler adapter (e.g.
// engine/agent, a test asserting the tool surfaces the rejection) distinguishes
// "overlapping fire, try again later" from a genuine failure via errors.Is.
var ErrFireNowOverlap = errors.New("port: fire-now skipped (prior fire still running)")

// PendingFireSessionID is the single-source sentinel Claim stamps on
// ScheduleState.LastFireSessionID — the placeholder the caller overwrites via
// RecordFire with the real fire's session id. It is non-empty so the singleton
// check has a recognisable "in-flight" marker (a Claim has happened but
// RecordFire has not).
//
// Singleton interaction: the singleton overlap check is a TRIAL lease acquire on
// LastFireSessionID (the authoritative cross-replica liveness oracle). The
// pending sentinel is NOT a real session id, so it cannot be probed by the trial
// lease. The scheduler therefore does NOT perform the overlap check when
// LastFireSessionID == PendingFireSessionID: it PROCEEDS with the fire. This is
// deliberate. The alternative — treat pending as "in-flight, so SKIP the fire" —
// would wedge a schedule FOREVER after a hard crash between Claim and RecordFire:
// the sentinel would never clear (a skip does not Claim, so it never advances),
// and the schedule would be skipped on every subsequent tick. Proceeding instead
// accepts a NARROW double-fire window (a fire genuinely still inside its short
// Claim→RecordFire window when the slot next becomes due — normally impossible,
// since Claim advances NextFireAt past `now`) in exchange for crash-recoverability.
// A real (non-pending) LastFireSessionID IS probed and the overlap check applies.
//
// Promoting it to one port constant (the same single-source discipline as
// SchedulerLeaderLeaseID / MemberSessionID) means the three store adapters and
// the scheduler agree on the exact string by importing it, not by independently
// declaring a byte-for-byte mirror.
const PendingFireSessionID session.SessionID = "pending"

// SchedulerLeaderLeaseID is the well-known session id the scheduler acquires a
// leader lease on (a SessionLease keyed by this id) so that, in a multi-replica
// deployment, at most one replica ticks the schedule store at a time (decision
// #10). It is the `id` argument to SessionLease.Acquire — the lease key — NOT the
// owner (the owner is the per-process identity string composition builds, exactly
// as the run-entry seam does for session leases).
//
// It is hygiene, NOT correctness: SessionID is an unvalidated string (the aggregate
// never inspects its value), the k8slease adapter hashes arbitrary ids, and real
// session ids are hex — so this sentinel cannot collide with a genuine session. A
// deployment that prefers a distinct leader id passes its own id through composition
// (a Build field), never by mutating this constant.
const SchedulerLeaderLeaseID session.SessionID = "__scheduler__"

// ScheduleProviderSelector is the provider+model pair a schedule's fires run on.
// It mirrors port.LLMRequest.Model's opaque-string discipline: the domain stores these
// two bare strings and never interprets them — the ProviderSelector type and all
// resolution (live catalog, model alias, capability intersection) live in
// composition, exactly as they do for a per-session engine. A zero value (both
// empty) means "use the deployment default" (the server's default provider/model),
// the same posture as a session created with no selector.
//
// It deliberately omits a reasoning-effort field (the third field on the internal
// ProviderSelector, ADR 0055). Reasoning-effort is an ADAPTER OPTION (a
// per-provider construction knob), NOT a port.LLMRequest field (the DTO-neutrality
// discipline), so it does not cross the port boundary. A v1 schedule runs on the
// operator's configured default effort for the selected model; a per-schedule
// effort override is a later-phase composition knob, not a port concept.
type ScheduleProviderSelector struct {
	// ProviderID is the opaque provider identifier (the same inert label a session
	// stores on ProviderID). "" means the deployment default.
	ProviderID string
	// ModelID is the opaque model identifier (the same inert label a session stores
	// on ModelID). "" means the deployment default for the provider.
	ModelID string
}

// TriggerKind discriminates how a schedule fires.
type TriggerKind int

const (
	// TriggerNone is the zero value: no trigger set. A TriggerSpec with Kind()
	// TriggerNone is invalid (Validate rejects it).
	TriggerNone TriggerKind = iota
	// TriggerCron means the schedule fires on a cron expression (TriggerSpec.Cron).
	TriggerCron
	// TriggerOneShot means the schedule fires once at a wall-clock instant
	// (TriggerSpec.OneShot).
	TriggerOneShot
)

// TriggerSpec is the sum type for a schedule's firing trigger. Exactly ONE of
// Cron / OneShot is set: Cron is a 5-field cron expression OR a macro
// (@every <duration>, @daily, @hourly, …) the composition layer parses via its
// cronparse dependency; OneShot is a single future wall-clock instant. The store is
// parser-free — it stores the raw expression verbatim and never interprets it; the
// CALLER (composition) computes the next fire and hands it to Claim.
//
// The zero value is invalid (neither field set); Validate enforces the
// exactly-one invariant fail-closed.
type TriggerSpec struct {
	// Cron is the cron expression (5-field or @-macro). Mutually exclusive with
	// OneShot. Empty unless this is a cron trigger.
	Cron string
	// OneShot is the single wall-clock instant to fire at. Mutually exclusive with
	// Cron. The zero time means "not set". It MUST be in the future at schedule
	// creation; the store does not re-validate this on save.
	OneShot time.Time
}

// Kind reports which trigger arm is set, or TriggerNone if neither (or, defensively,
// both — Kind never lies about a single arm when both are set; Validate is the
// authoritative gate). It is a convenience discriminator for callers that have
// already validated the spec.
func (t TriggerSpec) Kind() TriggerKind {
	switch {
	case t.Cron != "" && !t.OneShot.IsZero():
		// Both set — ambiguous; report None so callers don't branch on a lie.
		// Validate is the gate that rejects this.
		return TriggerNone
	case t.Cron != "":
		return TriggerCron
	case !t.OneShot.IsZero():
		return TriggerOneShot
	default:
		return TriggerNone
	}
}

// Validate enforces the exactly-one-of(Cron, OneShot) invariant: it returns an
// error (wrapping no sentinel — it is a value-object structural check, not a store
// error) if both are set or neither is set. It does NOT validate the cron
// expression's grammar (that is composition's job, which has the cronparse dep) —
// only the structural XOR.
func (t TriggerSpec) Validate() error {
	hasCron := t.Cron != ""
	hasOneShot := !t.OneShot.IsZero()
	switch {
	case hasCron && hasOneShot:
		return fmt.Errorf("port: trigger spec sets both cron and one-shot (exactly one required)")
	case !hasCron && !hasOneShot:
		return fmt.Errorf("port: trigger spec sets neither cron nor one-shot (exactly one required)")
	default:
		return nil
	}
}

// MisfirePolicy is what to do when a schedule's NextFireAt is in the past at tick
// time — i.e. the scheduler wakes up (or a replica takes over) and finds a due slot
// it missed. It is a per-schedule knob; the default (the zero value) is
// MisfireFireOnceNow.
type MisfirePolicy int

const (
	// MisfireFireOnceNow fires the schedule a single time immediately for the missed
	// slot, then resumes the normal cadence. This is the DEFAULT (zero value): a
	// missed run is not silently dropped — the schedule gets one catch-up fire. It
	// does NOT cascade (a slot missed by an hour fires once, not sixty times).
	MisfireFireOnceNow MisfirePolicy = iota
	// MisfireSkip skips the missed slot entirely and waits for the next due fire.
	// Use for schedules where a stale fire is worthless (e.g. a heartbeat that must
	// reflect current state) and a catch-up would be misleading.
	MisfireSkip
)

// ScheduleSpec is the immutable definition of a schedule — the "what to run and
// when" half, set at creation and not mutated by firing. The durable FIRING state
// (next fire, counts, last session) lives in ScheduleState. The two halves together
// form a Schedule, which is what Load/List return.
//
// Field-by-field contract:
//
//   - Name is the schedule's unique key (Save is an upsert by Name). It is a stable
//     caller-chosen identifier; the store does not generate it.
//   - Prompt is the free-text user prompt the fire runs with. Parts is an OPTIONAL
//     multimodal extension (image/audio content parts), validated by the same
//     session.ValidateMediaParts the wire path uses — there is no second validation
//     path. Either or both may be set; a schedule with neither is invalid (caught at
//     composition's create-seam, not here — the store is structure-blind).
//   - Trigger is the firing trigger (a TriggerSpec: Cron XOR OneShot). The store is
//     parser-free — it stores the raw expression verbatim and never interprets it;
//     the CALLER (composition) computes the next fire and hands it to Claim. Validate
//     enforces the exactly-one-of(Cron, OneShot) invariant; the store does not
//     re-Validate on Save (the create-seam does, fail-closed).
//   - Selector selects the provider+model the fires run on. A zero value means the
//     deployment default (the same opaque-string discipline as
//     port.LLMRequest.Model).
//   - Profile is the session tool-surface profile ("" default, "no-fs" file-less).
//     The store stores it inertly; composition interprets it at fire time.
//   - Workspace is the session cwd. "" means the deployment default.
//   - Mode is the session permission posture (the same session.PermissionMode a
//     created session carries).
//   - Limits are the bounded budgets for each fire — subagent-grade caps
//     (MaxTurns/MaxToolCalls/MaxConsecutiveFailures). They are PER-FIRE: each fire
//     gets a fresh session with these limits, so a runaway fire is bounded exactly
//     as a subagent is. A zero value disables that cap (the caller's responsibility
//     to set sane defaults).
//   - Mutating is the explicit write opt-in. The DEFAULT is false (read-leaning): a
//     schedule that does not opt in is treated as read-only for posture purposes,
//     the same conservative default as a subagent. A schedule that will write
//     (Edit/Write/Bash mutations) MUST set this true; composition's posture ladder
//     applies.
//   - MaxFires bounds the TOTAL number of fires for a cron schedule (0 = forever).
//     It is cron-only: a one-shot fires once by definition and MaxFires is ignored
//     for it. Once FireCount reaches MaxFires the schedule is DONE (Enabled=false,
//     NextFireAt zeroed) — the store enforces this in Claim.
//   - Misfire is the misfire policy (see MisfirePolicy). Default
//     MisfireFireOnceNow.
//   - Singleton is whether to skip the next fire if a prior fire is still running
//     (the singleton / skip-overlap guard). The intended default is true (overlapping
//     fires of the same schedule are suppressed, so a slow run does not pile up
//     concurrent fires); it is a bare `bool` whose zero value is false, and the
//     Phase-2 create-seam is what sets it to true by default (Phase 1 has no create
//     API, so a schedule's Singleton is whatever its Save carried). The authoritative
//     cross-replica liveness oracle for the "prior still running" check is the
//     per-session LEASE on ScheduleState.LastFireSessionID: a stale pointer to a
//     finished fire (lease released/expired) yields a free trial-acquire, so the
//     next fire is NOT skipped — a crashed fire self-heals by being treated as done.
//   - CreatedAt is the schedule's creation timestamp.
//   - OneShotRetry is the opt-in at-least-once retry for a one-shot (ADR 0059
//     Phase 2). The DEFAULT is false: a one-shot is at-most-once (a crash
//     mid-fire SKIPS the slot — the claim-before-fire advance already happened,
//     so a retry does not re-fire). A one-shot that cannot tolerate crash-loss
//     sets this true: the tick loop's re-arm path re-enables the schedule (up
//     to OneShotMaxRetries times) when it observes the prior fire crashed before
//     recording an outcome (LastFireSessionID still pending) or recorded a
//     StopError. It is one-shot-ONLY: setting it on a cron trigger is rejected
//     at the create-seam (a cron self-heals via misfire already). A re-armed
//     one-shot starts FRESH (the crashed fire's context is untrusted AND
//     incomplete — the re-arm path ignores CarryContext).
//   - OneShotMaxRetries bounds the re-arm budget when OneShotRetry is true. The
//     DEFAULT is 0 (off); the create-seam applies a default of 3 when
//     OneShotRetry is true and OneShotMaxRetries is 0. OneShotRetryCount on the
//     State is incremented on each re-arm; when it exceeds OneShotMaxRetries
//     the schedule stays disabled (the one-shot is permanently done).
//   - CarryContext is the opt-in carried-context toggle (ADR 0059 Phase 2).
//     The DEFAULT is false: each fire is a FRESH context (no prior fire's
//     history is carried). When true, the fire path loads the prior fire's
//     session and renders its conversation as a FENCED UNTRUSTED PREAMBLE
//     prepended to the fire's prompt — NOT as seeded history. The carried
//     context is UNTRUSTED (model-authored + tool-result-laden; a prior fire
//     may have been prompt-injected), so it MUST NOT become replayable
//     Conversation.Messages (which would carry injection forward as live
//     instructions). The fence (agent.FenceUntrusted + NeutraliseFraming)
//     quarantines it so a forged closing marker or harness section header in
//     the prior content cannot break out of its block. On prior-session-load
//     failure (not found, decode error) the fire degrades to fresh-context
//     (WARN, never fails the fire). A re-armed one-shot does NOT carry context
//     on the retry.
//   - FireTimeout is the per-fire wall-clock deadline (issue #386, the in-flight
//     scheduled-fire state). Zero means "use the deployment default" (a zero here
//     is NOT "no timeout" — it defers to the operator-tier deployment default,
//     which may itself be zero for "no explicit deadline"). When non-zero,
//     RecordFireStart stamps ScheduleState.FireDeadline = start + FireTimeout,
//     and a watchdog terminates the in-flight run with session.StopTimeout (a
//     CLEAN, recoverable terminal, like StopBudget) when it lapses. It bounds a
//     single fire's RUN, not the schedule's lifetime (MaxFires bounds the count).
//     The store stores it inertly (the store never interprets it); composition
//     reads it at fire-start.
type ScheduleSpec struct {
	Name      string
	Prompt    string
	Parts     []session.Content
	Trigger   TriggerSpec
	Selector  ScheduleProviderSelector
	Profile   string
	Workspace string
	Mode      session.PermissionMode
	Limits    session.Limits
	Mutating  bool
	MaxFires  int
	Misfire   MisfirePolicy
	Singleton bool
	// Timezone is the IANA timezone name (e.g. "America/New_York") the cron
	// expression fires in. Empty means UTC (the recommended default for infra
	// schedules — avoids the 1–3am DST danger zone). The store stores it
	// verbatim (it never interprets it); composition's cronparse call loads
	// it and passes it to NextFire. A one-shot trigger ignores it (a
	// one-shot is an absolute wall-clock instant, already tz-aware via
	// time.Time).
	Timezone  string
	CreatedAt time.Time
	// OneShotRetry is the opt-in at-least-once retry for a one-shot (see the
	// field-by-field contract above). Default false (at-most-once).
	OneShotRetry bool
	// OneShotMaxRetries bounds the re-arm budget when OneShotRetry is true. 0
	// means off (the create-seam applies a default of 3 when OneShotRetry is
	// true and this is 0). One-shot-only; ignored for cron.
	OneShotMaxRetries int
	// CarryContext renders the prior fire's conversation as a fenced untrusted
	// preamble (NOT seeded history — carried context is untrusted). See the
	// field-by-field contract above.
	CarryContext bool
	// OriginSessionID is the session whose terminal result delivery should
	// receive the fire's outcome. Empty means no delivery — the fire's result
	// is discoverable only through the pull-only GetFire/ListFires channel
	// (the v1 pre-delivery posture). A non-empty value names the session the
	// fire's terminal EvResult is delivered to (per ADR 0075, fire-result-
	// delivery). The field is METADATA-ONLY: it is NEVER rendered into a
	// prompt, NEVER surfaced to the model, and NEVER appears in any
	// model-visible surface. It is an infrastructure-level routing key the
	// fire path reads to route the outcome; the model has no access to it.
	//
	// The create-seam validates it: a non-empty OriginSessionID that names a
	// non-existent session is rejected fail-closed (the same ErrInvalidArgument
	// class as the other spec rejections). An empty OriginSessionID is always
	// valid (delivery is OFF — the byte-identical pre-delivery posture).
	OriginSessionID session.SessionID
	// FireTimeout is the per-fire wall-clock deadline (issue #386, the in-flight
	// scheduled-fire state). Zero means "use the deployment default" (the operator-
	// tier default applied by composition; a zero here is NOT "no timeout" — it
	// defers to the deployment default, which may itself be zero for "no explicit
	// deadline"). When non-zero, RecordFireStart stamps FireDeadline = start +
	// FireTimeout on the ScheduleState, and a watchdog reads FireDeadline to
	// terminate the in-flight run with session.StopTimeout when it lapses (a CLEAN,
	// recoverable terminal, like StopBudget). It bounds a single fire's RUN, not
	// the schedule's lifetime (MaxFires bounds the count). Per-fire: each fire gets
	// a fresh deadline from its own start instant. The store stores it inertly
	// (the store never interprets it); composition reads it at fire-start.
	FireTimeout time.Duration
}

// ScheduleState is the durable FIRING state of a schedule — the mutable half that
// advances as the schedule fires. It is updated atomically by Claim (the
// claim-before-fire advance) and RecordFire (the post-fire outcome), and persisted
// by Save. The store is the ground truth; an in-memory timer in composition is a
// DERIVED lookahead over this state.
//
// The claim-before-fire discipline: NextFireAt is advanced BEFORE the fire runs, as
// the atomic claim that gives at-most-once semantics — a peer replica's Due MUST
// NOT re-return a slot after Claim has advanced it. A crash mid-fire therefore
// SKIPS the slot (the advance already happened); a recurring schedule self-heals via
// the MisfireFireOnceNow policy on the next tick, but a one-shot can be LOST
// (decision #1 — the documented trade-off for exactly-once without distributed TX).
type ScheduleState struct {
	// NextFireAt is the ground-truth next fire instant. It is advanced by Claim
	// BEFORE the fire runs (claim-before-fire) and is the field Due compares against.
	// The zero time means "no next fire" (a one-shot that fired, or a cron whose
	// MaxFires is exhausted) — the schedule is effectively done.
	NextFireAt time.Time
	// LastFireAt is the instant of the most recent Claim (the start of the most
	// recent fire), not its completion. Updated atomically in Claim.
	LastFireAt time.Time
	// FireCount is the total number of fires that have been Claimed (a Claim
	// increments it). It is the counter MaxFires is checked against.
	FireCount int
	// Enabled is whether the schedule is active. A schedule may be disabled without
	// deletion (pause/resume). Disabled schedules are excluded from Due even if
	// NextFireAt is in the past. Claim sets Enabled=false when a cron exhausts
	// MaxFires or a one-shot fires.
	Enabled bool
	// LastFireSessionID is the session id of the prior fire. The per-session LEASE
	// on it is the authoritative cross-replica liveness oracle for the singleton
	// check: a still-held lease means the prior fire is running (skip the next
	// fire); a released/expired lease means it finished or crashed (fire freely).
	// Claim sets this to port.PendingFireSessionID; RecordFire overwrites it with
	// the real fire's session id.
	LastFireSessionID session.SessionID
	// OneShotRetryCount is the durable counter of one-shot re-arms (ADR 0059
	// Phase 2). It is incremented atomically by ScheduleOneShotReArmer.ReArmOneShot
	// on each re-arm. When it exceeds ScheduleSpec.OneShotMaxRetries the schedule
	// stays disabled (the one-shot is permanently done — the retry budget is
	// exhausted). The DEFAULT is 0 (no re-arms yet). It is one-shot-only: a cron
	// schedule never re-arms (a cron self-heals via misfire) so the counter stays
	// 0 for cron.
	OneShotRetryCount int
	// LastFireStartedAt is when the current fire's RUN actually began — the instant
	// the fire's session was driven (RecordFireStart), DISTINCT from LastFireAt
	// which is the Claim instant (a claim-before-fire advance happens BEFORE the
	// run starts, so LastFireStartedAt >= LastFireAt). It is the in-flight liveness
	// marker: zero means "the current fire has not started its run yet" (the
	// crash-after-Claim state — Claim happened, RecordFireStart did not). Set by
	// RecordFireStart, cleared by RecordFire (a terminal fire has no in-flight run).
	// Issue #386.
	LastFireStartedAt time.Time
	// LastFireProgressAt is the last observed progress instant for the current
	// fire (RecordFireProgress), advanced as the fire's run produces events. Zero
	// means "no progress observed yet" (the run started but has not emitted, or
	// RecordFireProgress was never called). It is best-effort liveness: a stale
	// value (far behind the wall-clock) is a stuck-fire signal a watchdog may act
	// on. Set by RecordFireStart (seeded to the start instant) and RecordFireProgress;
	// cleared by RecordFire. Issue #386.
	LastFireProgressAt time.Time
	// FireDeadline is the current fire's wall-clock deadline — the instant at
	// which the fire is considered to have exceeded its per-fire timeout
	// (ScheduleSpec.FireTimeout, the deployment default when zero). It is set by
	// RecordFireStart (start + FireTimeout, or zero when FireTimeout is zero / the
	// deployment default applies) and cleared by RecordFire. A watchdog reads it to
	// decide whether to terminate the in-flight run with StopTimeout. Zero means
	// "no explicit deadline (default or not set)". Issue #386.
	FireDeadline time.Time
}

// Schedule is the aggregate value object a ScheduleStore returns from Load/List:
// the immutable Spec plus the durable State. The two halves are separate so a caller
// can hold a Spec without firing state (e.g. a create/update payload) and so the
// store can advance State in place without touching the definition.
type Schedule struct {
	Spec  ScheduleSpec
	State ScheduleState
}

// ScheduleFire is one fire record: the outcome of a single Claim→run→RecordFire
// cycle. It is the pull-only result-delivery channel for v1 — a caller polls
// LoadFire (or List, future) to discover what a fire produced, rather than the store
// pushing results. The fire's SESSION (the conversation, usage, tool calls) lives
// in the SessionStore under SessionID; this record is the schedule-indexed pointer
// to it plus the terminal stop reason and any error string.
type ScheduleFire struct {
	// ID is the fire's unique identifier (caller-assigned at RecordFire time).
	ID string
	// ScheduleName is the schedule this fire belongs to (the foreign key back to
	// ScheduleSpec.Name).
	ScheduleName string
	// SessionID is the session the fire ran as. The fire's full conversation/state
	// is loaded from the SessionStore under this id.
	SessionID session.SessionID
	// FiredAt is when the fire was Claimed (its start instant). It is the same
	// instant recorded as LastFireAt on the schedule.
	FiredAt time.Time
	// StartedAt is when the fire's run actually began (RecordFireStart). It is
	// distinct from FiredAt (the Claim instant): a fire is Claimed BEFORE its run
	// starts, so StartedAt >= FiredAt. A fire written by RecordFireStart is
	// IN-FLIGHT (Stop empty, StartedAt set); a fire written by RecordFire is
	// terminal. Zero on a terminal-only fire (one never observed in-flight by the
	// store, e.g. a legacy record). Issue #386.
	StartedAt time.Time
	// ProgressAt is the last observed progress instant for the fire
	// (RecordFireProgress). Zero means "no progress observed". Issue #386.
	ProgressAt time.Time
	// Deadline is the fire's wall-clock deadline (RecordFireStart): start +
	// ScheduleSpec.FireTimeout (or zero when FireTimeout is zero / the deployment
	// default applies). Zero means "no explicit deadline". Issue #386.
	Deadline time.Time
	// Stop is the terminal stop reason of the fire's run (the same
	// session.StopReason EvResult carries). Empty if the fire has not yet
	// completed.
	Stop session.StopReason
	// Err is the error string if the fire's run failed (Stop == StopError), empty
	// otherwise. It is a flat string (no structured error crosses the store) so a
	// consumer can render it without importing the run's error types.
	Err string
}

// ScheduleStore is the OPTIONAL durable schedule registry port (scheduled-tasks
// Phase 1a) — a peer of port.SessionLease / port.EventLog. It is discovered by type
// assertion exactly like PrunableStore / SessionLease: a store/backend that does not
// implement it is simply never consulted, and composition wires a scheduler ONLY
// when an operator selects a backend by flag — the default path is byte-identical
// with no scheduling.
//
// The loop is storage-agnostic: engine/agent NEVER imports this port. The tick
// loop, cron parsing, misfire policy application, and the leader-lease acquisition
// all live in COMPOSITION (internal/app), exactly as the run-entry lease and the
// event-log persist live in composition. The store is the durable ground truth the
// tick loop polls; an in-memory timer is a DERIVED lookahead over Due, never the
// source of truth.
//
// AT-MOST-ONCE (the core contract): Claim is the atomic advance that gives
// exactly-once firing across replicas. It advances NextFireAt and LastFireAt,
// increments FireCount, and sets LastFireSessionID to port.PendingFireSessionID —
// all BEFORE the fire runs (claim-before-fire). A peer replica's Due MUST NOT
// re-return a slot after Claim has advanced it. A crash mid-fire SKIPS the slot
// (the advance already happened); a recurring schedule self-heals via the
// MisfireFireOnceNow policy on the next tick; a one-shot can be LOST (decision #1).
// The caller computes the next cron fire (the store is parser-free) and passes it
// to Claim.
//
// MISFIRE: when a schedule's NextFireAt is in the past at tick time, the misfire
// policy applies (see MisfirePolicy). The default MisfireFireOnceNow fires once
// immediately for the missed slot; MisfireSkip skips it. The policy is read from
// ScheduleSpec.Misfire by composition at tick time, not by the store.
//
// CONCURRENCY: implementations MUST be safe for concurrent calls across DISTINCT
// schedule names — one process ticks many schedules at once, and two replicas may
// tick the same store. Calls for the SAME name from one process are serialised by
// the caller (the tick loop is single-threaded per schedule). Claim MUST be atomic
// with respect to other Claims on the same name (the at-most-once guarantee).
type ScheduleStore interface {
	// Save upserts the schedule by Spec.Name. A schedule with the same name is
	// overwritten; the State half is preserved on overwrite (a Save with a fresh
	// State does not reset firing progress — call Delete + Save to reset). An
	// implementation that cannot store schedules returns ErrScheduleUnsupported
	// (wrapped).
	Save(ctx context.Context, s Schedule) error

	// Load returns the schedule stored under name. The not-found case MUST wrap
	// ErrScheduleNotFound; any other error is an infrastructure failure (or
	// ErrScheduleUnsupported if the backend cannot store schedules at all).
	Load(ctx context.Context, name string) (Schedule, error)

	// Delete removes the schedule stored under name. It is IDEMPOTENT: deleting an
	// unknown name is success (the PrunableStore.Delete discipline), so callers
	// tolerate List/Delete races by construction. Any returned error is an
	// infrastructure failure (or ErrScheduleUnsupported).
	Delete(ctx context.Context, name string) error

	// List returns ALL stored schedules, in no guaranteed order. It applies NO
	// filtering — retention policy (which schedules are prunable, age thresholds)
	// is entirely the CALLER's business (the PrunableStore.List discipline).
	// An implementation that cannot enumerate returns ErrScheduleUnsupported
	// (wrapped).
	List(ctx context.Context) ([]Schedule, error)

	// Due returns the schedules whose NextFireAt is <= now AND Enabled AND (when
	// MaxFires > 0) FireCount < MaxFires. This is the poll-the-store pattern: the
	// in-memory timer in composition is a DERIVED lookahead, the store is ground
	// truth. A schedule returned by Due is NOT yet claimed — the caller must Claim
	// it to win the slot. Due is idempotent and side-effect-free; it does not
	// advance state.
	Due(ctx context.Context, now time.Time) ([]Schedule, error)

	// Claim is the AT-MOST-ONCE atomic advance. It atomically: sets LastFireAt=now,
	// advances NextFireAt to nextFire (the caller-computed next cron fire, or the
	// zero time for a one-shot / a MaxFires-exhausted cron), increments FireCount,
	// sets LastFireSessionID to port.PendingFireSessionID (the caller overwrites it
	// via RecordFire with the real fire's session id), and (for a one-shot or an
	// exhausted cron) sets Enabled=false and zeroes NextFireAt. It returns the
	// claimed schedule (with the advanced State).
	//
	// nextFire is computed by the CALLER (composition, which has the cronparse
	// dependency) — the store is parser-free and never interprets the cron
	// expression. A zero nextFire means "no further fire" (one-shot done, or cron
	// exhausted): Claim sets Enabled=false and zeroes NextFireAt.
	//
	// The at-most-once fence is the durable NextFireAt advance itself — there is no
	// owner/claim-holder field (unlike SessionLease, whose Owner fences concurrent
	// writers on the SAME id). A schedule's fire slot is claimed by advancing
	// NextFireAt past `now`; a peer replica's Due then no longer returns it, so a
	// second Claim on the same slot is structurally impossible (the slot is no
	// longer due). Cross-replica overlap of a STILL-RUNNING prior fire is a
	// SEPARATE concern, handled by the caller's trial-lease on
	// ScheduleState.LastFireSessionID (the singleton check), not by Claim.
	//
	// A crash mid-fire SKIPS the slot — the advance already happened, so a retry
	// does not re-fire. The not-found case wraps ErrScheduleNotFound (a Claim on a
	// deleted schedule is an error, not a silent no-op).
	Claim(ctx context.Context, name string, now, nextFire time.Time) (Schedule, error)

	// ClaimNow is the manual-trigger variant of Claim: it performs the SAME atomic
	// advance (LastFireAt=now, NextFireAt=nextFire, FireCount++,
	// LastFireSessionID=PendingFireSessionID, disable on zero nextFire) but does
	// NOT enforce the NextFireAt <= now due-check — it claims the slot regardless of
	// whether it is due. It is the FireNow primitive (an explicit manual trigger
	// bypasses the cadence but still claims atomically for at-most-once). The
	// Enabled + MaxFires checks STILL apply (a disabled or exhausted schedule
	// cannot be force-fired). The not-found case wraps ErrScheduleNotFound.
	//
	// At-most-once WITHOUT the due-check: Claim's fence is "NextFireAt is past now
	// (a peer's Claim advanced it)", which a not-yet-due slot fails. ClaimNow cannot
	// use that fence (a future slot would pass), so it fences on LastFireAt: a
	// ClaimNow at the SAME now as a prior ClaimNow is rejected (LastFireAt == now
	// ⇒ the advance already happened). This mirrors Claim's discipline — the durable
	// advance IS the fence, there is no owner/claim-holder field. It is
	// crash-recoverable by design (unlike a pending-sentinel fence, which would
	// wedge a schedule forever after a hard crash between ClaimNow and RecordFire —
	// the exact wedge the singleton check's doc warns against): a crash leaves
	// LastFireAt == now, but a later ClaimNow at a new now sees a stale LastFireAt
	// != now and proceeds, so the schedule self-heals instead of wedging. The
	// tick-loop Claim is UNAFFECTED — it keeps its due-check (a due slot is the only
	// thing the tick loop should fire).
	ClaimNow(ctx context.Context, name string, now, nextFire time.Time) (Schedule, error)

	// SetEnabled atomically sets the schedule's Enabled flag WITHOUT touching
	// any other State field (unlike Save, which preserves State on a Spec
	// overwrite — Save CANNOT mutate Enabled because it preserves the existing
	// State half). It is the pause/resume primitive: PauseSchedule sets
	// Enabled=false; ResumeSchedule sets Enabled=true. The not-found case wraps
	// ErrScheduleNotFound. An implementation that cannot store schedules returns
	// ErrScheduleUnsupported (wrapped).
	SetEnabled(ctx context.Context, name string, enabled bool) error

	// RecordFire records the outcome of a fire (f) and updates the schedule's
	// LastFireSessionID to f.SessionID (overwriting the port.PendingFireSessionID
	// value Claim set). It is IDEMPOTENT per fire id: recording the same f.ID twice is
	// a no-op (the second call returns nil without mutating state), so a caller
	// may safely retry after a transient infrastructure failure. The not-found
	// case (the schedule was deleted between Claim and RecordFire) wraps
	// ErrScheduleNotFound.
	//
	// It FLIPS the fire terminal and clears the in-flight ScheduleState fields
	// (LastFireStartedAt/LastFireProgressAt/FireDeadline) — a recorded (terminal)
	// fire has no in-flight run. It overwrites any in-flight fire record the same
	// f.ID had under RecordFireStart with the terminal one (Stop/Err set). Issue #386.
	RecordFire(ctx context.Context, f ScheduleFire) error

	// RecordFireStart persists the IN-FLIGHT fire (issue #386, the in-flight
	// scheduled-fire state) — the fire's run has begun but not yet produced a
	// terminal outcome. It persists the fire record f (ID, SessionID, StartedAt,
	// Deadline) as IN-FLIGHT: Stop empty, StartedAt set. It also sets
	// ScheduleState.LastFireSessionID to the REAL session id f.SessionID
	// (overwriting the port.PendingFireSessionID sentinel Claim stamped) and
	// ScheduleState.LastFireStartedAt to f.StartedAt (and seeds
	// LastFireProgressAt to f.StartedAt when the caller passed a zero ProgressAt).
	// The FireDeadline field on the state is set to f.Deadline (zero when no
	// explicit deadline applies). The not-found case (the schedule was deleted
	// between Claim and RecordFireStart) wraps ErrScheduleNotFound.
	//
	// It is IDEMPOTENT per fire id: recording the same f.ID twice (with the same
	// StartedAt) is a no-op for the in-flight record (it does not re-stamp or
	// double-advance), so a caller may safely retry after a transient
	// infrastructure failure. A RecordFireStart for a fire id that is ALREADY
	// terminal (a prior RecordFire recorded it) is a no-op too (a terminal fire
	// is not re-opened) — the caller should not interleave RecordFireStart after
	// RecordFire, but the store is honest about it. Issue #386.
	RecordFireStart(ctx context.Context, name string, fire ScheduleFire) error

	// RecordFireProgress advances the in-flight fire's last-observed-progress
	// instant (issue #386). It updates ScheduleState.LastFireProgressAt to `at`
	// (when `at` is after the stored value; an earlier `at` is ignored so a
	// reordered/delayed update cannot rewind progress) and the in-flight fire
	// record's ProgressAt to `at`.
	//
	// fireID is the id of the in-flight fire record the caller's RecordFireStart
	// wrote (the same `fire.ID` RecordFireStart took). It targets the SINGLE fire
	// record by its known key directly — there is NO directory/keyspace scan to
	// locate the schedule's in-flight fire (review finding M1: scanning every fire
	// record under the store mutex on every turn boundary is O(N) in the fire
	// population, up to ~10k with 7d retention, and blocks Claim/RecordFire/List/
	// Due). The caller (the fire loop) has fireID in scope.
	//
	// It is BEST-EFFORT and IDEMPOTENT: a missing in-flight fire record (no prior
	// RecordFireStart, or it was already flipped terminal) is a no-op success (the
	// progress is recorded on the state alone), and a not-found SCHEDULE wraps
	// ErrScheduleNotFound. It never re-opens a terminal fire: a progress write to
	// an already-TERMINAL fire record is a no-op for the record (it MUST NOT
	// revert the record from terminal back to in-flight — review finding M2, the
	// cross-replica race a non-atomic GET-then-SET had where a concurrent terminal
	// RecordFire's SET landing between the GET and SET reverted the record). Issue
	// #386.
	RecordFireProgress(ctx context.Context, name string, fireID string, at time.Time) error

	// LoadFire returns the fire record stored under fireID. The not-found case
	// wraps ErrScheduleNotFound; any other error is an infrastructure failure. It
	// is the pull-only result-delivery read path for v1.
	LoadFire(ctx context.Context, fireID string) (ScheduleFire, error)

	// ListFires returns the fire records for a schedule, in no guaranteed order.
	// It is the list companion to LoadFire. The not-found case for the SCHEDULE
	// wraps ErrScheduleNotFound; an empty fire list for an existing schedule is a
	// successful empty slice (not an error). An implementation that cannot store
	// schedules returns ErrScheduleUnsupported (wrapped).
	ListFires(ctx context.Context, scheduleName string) ([]ScheduleFire, error)
}

// ScheduleManager is the narrow CONSUMER-LOCAL interface the agent-layer
// Schedule tool (engine/agent) consumes to manage scheduled tasks. It exposes
// exactly the verbs the tool needs — create/inspect/list/update/pause/resume/
// delete/fire/list-fires — and is satisfied by composition with the existing
// schedule surface of the server service (internal/adapter/server.Service),
// whose methods have these EXACT signatures. The injection precedent is the
// Subagent tool's WithSubagentStore(port.SessionStore): a consumer-defined
// port satisfied in composition, so engine/agent NEVER imports
// internal/adapter/server, an adapter, proto, or gRPC. It is NOT
// port.ScheduleStore (the durable registry port the tick loop polls) — the
// manager is the validated create-seam + fire-seam SURFACE (CreateSchedule
// validates fail-closed and computes the first fire; FireNow claims + runs a
// fire synchronously-to-terminal), which the store alone does not provide.
//
// Error contract: a ScheduleManager MUST wrap the port-level sentinels a
// consumer distinguishes via errors.Is — port.ErrScheduleNotFound for an
// unknown schedule/fire name, and the create-seam's argument-rejection class
// for an invalid spec. A FireNow on a schedule whose prior fire is still
// running returns the singleton-overlap error the REST FireNow surface
// returns (a caller may map it to a "try again later" model message); it is
// NOT a second concurrent fire.
type ScheduleManager interface {
	// CreateSchedule validates the spec fail-closed (trigger XOR, cron grammar,
	// the Mutating/Mode invariant, the profile-aware workspace check), applies
	// the create-seam defaults (Singleton=true), computes the first NextFireAt,
	// and saves the schedule. A duplicate name is rejected (create never
	// clobbers an existing schedule).
	CreateSchedule(ctx context.Context, spec ScheduleSpec) (Schedule, error)
	// GetSchedule loads a schedule by name; the not-found case wraps
	// ErrScheduleNotFound.
	GetSchedule(ctx context.Context, name string) (Schedule, error)
	// ListSchedules returns all stored schedules (no guaranteed order).
	ListSchedules(ctx context.Context) ([]Schedule, error)
	// UpdateSchedule re-validates and overwrites the Spec half while preserving
	// the firing State (progress). The not-found case wraps ErrScheduleNotFound.
	UpdateSchedule(ctx context.Context, spec ScheduleSpec) (Schedule, error)
	// DeleteSchedule removes a schedule by name. It is idempotent (deleting an
	// unknown name is success).
	DeleteSchedule(ctx context.Context, name string) error
	// PauseSchedule disables a schedule (Enabled=false) without deleting it.
	PauseSchedule(ctx context.Context, name string) error
	// ResumeSchedule re-enables a paused schedule (Enabled=true).
	ResumeSchedule(ctx context.Context, name string) error
	// FireNow manually fires a schedule by name, SYNCHRONOUSLY-TO-TERMINAL: it
	// claims the slot, mints the fire's session (a "sched--"-prefixed id), runs
	// it, and returns the fire record (ID == SessionID, plus the terminal Stop
	// reason). A paused/done schedule, an exhausted one-shot, or a singleton
	// overlap is rejected (the same sentinels the REST FireNow maps).
	FireNow(ctx context.Context, name string) (ScheduleFire, error)
	// ListFires returns the fire records for a schedule (no guaranteed order).
	ListFires(ctx context.Context, scheduleName string) ([]ScheduleFire, error)
}

// ScheduleOneShotReArmer is the OPTIONAL at-least-once re-arm seam for one-shot
// schedules (ADR 0059 Phase 2). It is discovered by type assertion on a
// ScheduleStore exactly like PrunableStore / SessionLease / MetaLister are on a
// SessionStore: a store that does not implement it is simply never consulted, and
// the tick loop's one-shot re-arm path degrades to at-most-once (byte-identical to
// the pre-Phase-2 path) — a one-shot that crashed mid-fire stays lost, the
// documented pre-Phase-2 trade-off.
//
// Why an OPTIONAL interface, NOT a method on ScheduleStore: adding a method to an
// existing interface is BREAKING for external implementers (a ScheduleStore
// implementation outside this repo would fail to compile). The optional-interface
// type-assertion pattern (PrunableStore / SessionLease / MetaLister) adds the seam
// without widening the required surface — a store opts in by implementing the
// method, and the caller type-asserts before calling.
//
// ReArmOneShot atomically: re-enables the schedule (Enabled=true), sets NextFireAt
// to nextFire, and increments OneShotRetryCount. It is the re-arm primitive the
// tick loop calls when it observes a one-shot that crashed mid-fire (Claim
// disabled it; the fire never recorded a successful outcome). The atomicity is the
// at-most-once fence for the RE-ARM: two concurrent re-arms must not
// double-increment OneShotRetryCount or double-enable. The not-found case wraps
// ErrScheduleNotFound. A re-arm past OneShotMaxRetries is the CALLER's gate (the
// tick loop checks the budget before calling); the store does NOT enforce the
// budget — it only atomically advances the counter.
//
// It is one-shot-ONLY: a cron schedule never re-arms (a cron self-heals via
// misfire). The caller never calls ReArmOneShot on a cron schedule.
type ScheduleOneShotReArmer interface {
	// ReArmOneShot re-enables the named one-shot schedule, sets its NextFireAt to
	// nextFire, and increments OneShotRetryCount — atomically. It is the re-arm
	// primitive the tick loop calls for a crashed one-shot retry. The not-found
	// case wraps ErrScheduleNotFound.
	ReArmOneShot(ctx context.Context, name string, nextFire time.Time) error
}
