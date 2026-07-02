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

	// RecordFire records the outcome of a fire (f) and updates the schedule's
	// LastFireSessionID to f.SessionID (overwriting the port.PendingFireSessionID
	// value Claim set). It is IDEMPOTENT per fire id: recording the same f.ID twice is
	// a no-op (the second call returns nil without mutating state), so a caller
	// may safely retry after a transient infrastructure failure. The not-found
	// case (the schedule was deleted between Claim and RecordFire) wraps
	// ErrScheduleNotFound.
	RecordFire(ctx context.Context, f ScheduleFire) error

	// LoadFire returns the fire record stored under fireID. The not-found case
	// wraps ErrScheduleNotFound; any other error is an infrastructure failure. It
	// is the pull-only result-delivery read path for v1.
	LoadFire(ctx context.Context, fireID string) (ScheduleFire, error)
}
