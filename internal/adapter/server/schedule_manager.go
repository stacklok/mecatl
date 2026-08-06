package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

// schedule_manager.go is the STORE-SHAPED schedule manager (ADR 0076): the
// validated create/read/update/fire seam lives on a standalone value
// constructable from a port.SessionStore + a now-func ALONE, BEFORE the
// *Service exists. The manager holds the ScheduleStore (either an explicit
// override passed via ScheduleManagerConfig.ScheduleStore — the
// --schedule-store-url composition path — or type-asserted off the session
// store via the scheduleStoreProvider accessor), the cadence floor, the
// durable EventLog, diagnostics, the shared model-inventory pointer
// (selector validation reads *models.Load()), and the late-set in-process
// scheduler (FireNow). *server.Service delegates its nine port.ScheduleManager
// methods + EmitScheduleEvent + GetFire to this manager — the RPC surface
// (grpc_schedule.go, the REST /v1/schedules handlers, the mecatui /schedule
// overlay) is byte-identical; the create-seam (validateScheduleSpec +
// applyScheduleDefaults + the origin/selector/cadence checks) moved verbatim
// from *Service, one seam, one truth (ADR 0073).
//
// The manager is OPTIONAL: a store that backs no ScheduleStore (the in-memory
// memstore default) yields a nil/absent manager — the honest no-scheduling
// path, matching ServerCapabilities.Scheduling. The constructor returns nil in
// that case so the capabilities gate (scheduleStore() != nil) and the tool
// registration agree.

// ScheduleManagerConfig is the pre-Service construction input for a
// scheduleManager (ADR 0076): the plain inputs available before buildEngine.
// Store is a port.SessionStore; the ScheduleStore is type-asserted off it via
// the scheduleStoreProvider accessor (the jsonlstore + redisstore expose one).
// ScheduleStore is the OPTIONAL explicit override (the --schedule-store-url
// composition path): when non-nil it WINS over the accessor discovery, so a
// driver-backed registry backs the in-chat Schedule TOOL too — the registry is
// a remote driver, NOT the session store's own accessor, so the tool + tick
// loop + fire path share the ONE resolveScheduleStore resolution (no
// split-brain with an accessor-ful store + the override, and no absent tool
// with an accessor-less store + the override). When nil, behaviour is
// byte-identical to the accessor discovery (the pre-override posture).
// Now is the now-func (the same clock the Service uses). Models is the SHARED
// model-inventory pointer (selector validation reads *models.Load()); the
// Service passes its own pointer so SetModels keeps working with no second
// copy. EventLog + Diagnostics are the durable-log + diagnostic seams
// EmitScheduleEvent rides; nil-safe (a nil EventLog is a no-op, a nil
// Diagnostics tolerates an append failure silently). A store that backs no
// ScheduleStore yields a nil manager (NewScheduleManager returns nil).
type ScheduleManagerConfig struct {
	// Store is the port.SessionStore the schedule's origin validation reads
	// (validateScheduleOrigin). It is also the ScheduleStore discovery source
	// when ScheduleStore is nil (the scheduleStoreProvider accessor).
	Store port.SessionStore
	// ScheduleStore is the OPTIONAL explicit port.ScheduleStore override (the
	// --schedule-store-url composition path). When non-nil it WINS over the
	// Store type-assertion discovery: the registry is a remote driver, not the
	// session store's own accessor, so the in-chat Schedule tool + the tick
	// loop + the fire path all share the ONE resolveScheduleStore resolution.
	// When nil, behaviour is byte-identical to the accessor discovery.
	ScheduleStore port.ScheduleStore
	Now           func() time.Time
	Models        *atomic.Pointer[[]*mecatlv1.ModelInfo]
	EventLog      port.EventLog
	Diagnostics   port.Diagnostics
}

// scheduleManager is the store-shaped schedule create/read/update/fire seam
// (ADR 0076). It is the single truth the *Service delegates to: the nine
// port.ScheduleManager verbs + EmitScheduleEvent + GetFire. It holds the
// ScheduleStore (type-asserted at construction), the session store (origin
// validation reads store.Load), a now-func, the cadence floor, the late-set
// in-process scheduler, the durable EventLog, diagnostics, and the shared
// model-inventory pointer. A nil scheduler (the byte-identical default) means
// FireNow distinguishes ErrNoScheduleStore (no store) from
// ErrSchedulerNotRunning (store present, no tick loop). The cadence floor
// guards the SHARED validateScheduleSpec whether or not the tick loop runs (a
// --no-scheduler deployment still manages schedules manually through this
// seam).
type scheduleManager struct {
	// store is the port.SessionStore the schedule's origin validation reads
	// (validateScheduleOrigin: a non-empty OriginSessionID must name an existing
	// session the fire's terminal result will be delivered to). It is the SAME
	// store the Service was configured with (cfg.Store).
	store port.SessionStore
	// schedStore is the ScheduleStore backing the manager. It is EITHER the
	// explicit ScheduleStore override from ScheduleManagerConfig (the
	// --schedule-store-url composition path — the registry is a remote driver)
	// OR the ScheduleStore type-asserted off store at construction (the
	// scheduleStoreProvider accessor). nil is impossible on a constructed
	// manager — NewScheduleManager returns nil when NEITHER yields a store
	// (the honest no-scheduling path).
	schedStore port.ScheduleStore
	// now is the now-func (the same clock the Service uses). Stamped on
	// create/update and passed to the scheduler's FireNow.
	now func() time.Time
	// scheduleMinIntervalNanos is the scheduler cadence floor (ADR 0073, the
	// create-seam half of scheduler.Config.MinInterval): a schedule whose
	// cadence is tighter is rejected fail-closed by validateScheduleSpec. 0 =
	// no floor (the byte-identical pre-floor posture). An atomic so the
	// composition-time SetScheduleMinInterval is race-free against a create
	// already in flight.
	scheduleMinIntervalNanos atomic.Int64
	// scheduler is the OPTIONAL late-set in-process scheduled-tasks tick loop
	// (issue #189, Phase 1f). SetScheduler late-binds it AFTER the manager is
	// constructed (buildScheduler needs the Service for the FireFunc, so the
	// scheduler is built AFTER NewService and attached here). nil when no
	// scheduler is wired (the byte-identical default — FireNow distinguishes
	// ErrNoScheduleStore from ErrSchedulerNotRunning).
	scheduler atomic.Pointer[scheduler.Scheduler]
	// eventLog is the durable EventLog EmitScheduleEvent appends to. nil-safe:
	// a nil EventLog makes EmitScheduleEvent a no-op (byte-identical to the
	// no-emit path).
	eventLog port.EventLog
	// diag is the operational diagnostics sink EmitScheduleEvent WARNs to on
	// an Append failure. nil-safe: a nil Diagnostics tolerates the failure
	// silently (the durability gap is the only effect).
	diag port.Diagnostics
	// models is the SHARED selectable-model inventory pointer (the SAME
	// atomic.Pointer the Service holds and SetModels swaps). Selector
	// validation reads *models.Load() so a live-catalog swap is reflected on
	// the next create without a second copy. Never nil on a manager built by
	// the Service (it passes its own pointer); nil on a standalone-constructed
	// manager (NewScheduleManager without Models) — selector validation then
	// admits only the empty selector (an empty inventory).
	models *atomic.Pointer[[]*mecatlv1.ModelInfo]
}

// scheduleStoreProvider is the accessor the jsonlstore + redisstore expose:
// the ScheduleStore is discovered by type-asserting the configured store for a
// ScheduleStore() method returning a port.ScheduleStore (or nil). A store that
// does not expose the accessor (or returns nil) has no schedule support — the
// byte-identical no-scheduling path. The SAME accessor buildScheduler uses.
type scheduleStoreProvider interface {
	ScheduleStore() port.ScheduleStore
}

// scheduleStoreFrom type-asserts store for a ScheduleStore via the
// scheduleStoreProvider accessor (the jsonlstore + redisstore expose one).
// Returns nil when the store does not expose the accessor or returns nil — the
// honest no-scheduling path, matching ServerCapabilities.Scheduling. It is the
// FALLBACK discovery when ScheduleManagerConfig.ScheduleStore (the explicit
// override) is nil; the single discovery site (the manager constructor + the
// Service's self-construct fallback share it).
func scheduleStoreFrom(store port.SessionStore) port.ScheduleStore {
	if store == nil {
		return nil
	}
	if p, ok := store.(scheduleStoreProvider); ok {
		return p.ScheduleStore()
	}
	return nil
}

// ScheduleManagerImpl exposes the concrete manager type to the COMPOSITION
// layer (internal/app) only: buildEngine must thread the ONE manager it
// constructs through to server.Config.ScheduleManager, and an interface-typed
// round-trip would re-introduce the typed-nil hazard (a nil *scheduleManager
// boxed in a non-nil port.ScheduleManager defeats the mgr == nil honest-absence
// gate). Consumers still interact with the manager via the port.ScheduleManager
// interface; the alias exists ONLY so the concrete value crosses the adapter
// boundary with its nil-ness intact. It adds no methods to the public surface
// beyond what port.ScheduleManager already declares.
type ScheduleManagerImpl = scheduleManager

// NewScheduleManager constructs a scheduleManager from the plain pre-Service
// inputs (ADR 0076): a port.SessionStore + a now-func ALONE (no *Service value
// required, resolvable before buildEngine). It resolves the ScheduleStore as
// follows: when ScheduleManagerConfig.ScheduleStore is non-nil (the
// --schedule-store-url composition override) it WINS — the registry is a remote
// driver, not the session store's own accessor, so the in-chat Schedule tool +
// the tick loop + the fire path share the ONE resolveScheduleStore resolution;
// otherwise the store is type-asserted for a ScheduleStore via the
// scheduleStoreProvider accessor (the jsonlstore + redisstore expose one). A
// store that backs no ScheduleStore AND carries no override (the in-memory
// memstore) yields a nil/absent manager — the honest no-scheduling path,
// matching ServerCapabilities.Scheduling. The returned manager satisfies
// port.ScheduleManager (all nine verbs) and is the single truth the *Service
// delegates to.
//
// Models is OPTIONAL: a standalone-constructed manager (no Models pointer)
// admits only the empty selector (an empty inventory) — composition passes
// the Service's own pointer so SetModels keeps working with no second copy.
// EventLog + Diagnostics are OPTIONAL and nil-safe.
//
//nolint:revive // intentional unexported return: the manager is an adapter-internal type (ADR 0076); callers consume it via the port.ScheduleManager interface, and the *Service embeds + delegates to it. The unexported type keeps the schedule surface from leaking into the server adapter's public API.
func NewScheduleManager(cfg ScheduleManagerConfig) *scheduleManager {
	schedStore := cfg.ScheduleStore
	if schedStore == nil {
		schedStore = scheduleStoreFrom(cfg.Store)
	}
	if schedStore == nil {
		// No explicit override AND no accessor-discovered store (the in-memory
		// default) yields the honest absent manager — nil, matching the
		// capabilities Scheduling=false gate. NEVER a stub.
		return nil
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	m := &scheduleManager{
		store:      cfg.Store,
		schedStore: schedStore,
		now:        now,
		eventLog:   cfg.EventLog,
		diag:       cfg.Diagnostics,
		models:     cfg.Models,
	}
	// A standalone-constructed manager (no Models pointer) seeds an empty
	// inventory so selector validation reads *models.Load() safely and admits
	// only the empty selector — byte-identical to a deployment with an empty
	// inventory. The Service passes its own pointer (seeded at NewService), so
	// this branch is the standalone/test path only.
	if m.models == nil {
		empty := []*mecatlv1.ModelInfo{}
		var p atomic.Pointer[[]*mecatlv1.ModelInfo]
		p.Store(&empty)
		m.models = &p
	}
	return m
}

// setModelsPointer late-binds the shared model-inventory pointer (ADR 0076:
// the model-inventory is a late-bound atomic field, NOT a construction input —
// the manager is resolvable before buildEngine; the pointer is created at
// NewService when the Service seeds its own atomic from cfg.Models). The
// Service calls this when it adopts a pre-built manager (cfg.ScheduleManager)
// so the manager reads the SAME atomic SetModels swaps — no second copy, so a
// live-catalog refresh reflects on the next create. Safe to call before serving
// starts (composition constructs the manager + the Service back-to-back, both
// before Start); the pointer is read lock-free on every selector validation.
func (m *scheduleManager) setModelsPointer(p *atomic.Pointer[[]*mecatlv1.ModelInfo]) {
	m.models = p
}

// scheduleStore returns the manager's ScheduleStore (the explicit override
// when ScheduleManagerConfig.ScheduleStore was set, else the
// scheduleStoreProvider accessor result). It is the read the Service's
// capabilities() Scheduling gate + the delegating schedule verbs use — kept
// here so the Service no longer self-discovers the store (it consumes the
// manager). Always non-nil on a constructed manager (the constructor returns
// nil otherwise).
func (m *scheduleManager) scheduleStore() port.ScheduleStore {
	return m.schedStore
}

// CreateSchedule is the create-seam for a schedule: it validates the spec
// fail-closed, applies the intended defaults (via applyScheduleDefaults — the
// SHARED helper UpdateSchedule also calls, so a PUT omitting singleton does not
// silently disable the guard), computes the first NextFireAt (cron via cronparse;
// one-shot is the OneShot instant), enforces the Mutating/Mode invariant (a
// read-leaning schedule — Mutating=false — must run in plan mode, never a
// write-capable posture), and Saves the schedule. It returns the saved
// schedule.
func (m *scheduleManager) CreateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	now := m.now()
	cronNextFire, err := m.validateScheduleSpec(ctx, spec, now)
	if err != nil {
		return port.Schedule{}, err
	}
	// Collision guard: ScheduleStore.Save is an UPSERT-by-name, so a Create whose
	// name already exists would SILENTLY CLOBBER the existing schedule's spec. A
	// "Create" must never destroy an existing task — reject a duplicate name here
	// (the edit path is UpdateSchedule, a distinct method). There is a tiny
	// check-then-Save TOCTOU window (the store has no atomic create-if-absent),
	// but Create is a low-frequency human action, so the racing-duplicate risk is
	// acceptable and not worth store-level locking.
	if _, lerr := m.schedStore.Load(ctx, spec.Name); lerr == nil {
		return port.Schedule{}, fmt.Errorf("%w: a schedule named %q already exists", ErrInvalidArgument, spec.Name)
	} else if !errors.Is(lerr, port.ErrScheduleNotFound) {
		return port.Schedule{}, lerr
	}
	applyScheduleDefaults(&spec)
	// Compute the first NextFireAt. A cron trigger's next fire was ALREADY
	// computed by validateScheduleSpec (it must parse the expression to
	// validate the grammar, so that parse is reused here rather than calling
	// cronparse.NextFire a second time for the same expression); a one-shot's
	// first fire is its OneShot instant (already validated as in the future).
	var nextFireAt time.Time
	switch spec.Trigger.Kind() {
	case port.TriggerCron:
		nextFireAt = cronNextFire
	case port.TriggerOneShot:
		nextFireAt = spec.Trigger.OneShot
	}
	spec.CreatedAt = now
	sched := port.Schedule{
		Spec: spec,
		State: port.ScheduleState{
			NextFireAt: nextFireAt,
			Enabled:    true,
		},
	}
	if err := m.schedStore.Save(ctx, sched); err != nil {
		return port.Schedule{}, err
	}
	return sched, nil
}

// applyScheduleDefaults applies the intended create-seam defaults to a spec:
// Singleton defaults to true (overlapping fires of the same schedule are
// suppressed); Misfire defaults to MisfireFireOnceNow (the zero value — a
// missed slot fires once on catch-up — so no code is needed for it). A caller
// may override Singleton by setting it explicitly; a bare bool has no "set"
// marker, so the v1 seam applies the conservative default (true) when the
// caller left it false. It is the SHARED helper both CreateSchedule and
// UpdateSchedule call so a PUT omitting singleton does not silently disable the
// guard.
func applyScheduleDefaults(spec *port.ScheduleSpec) {
	if !spec.Singleton && !scheduleSingletonExplicit(*spec) {
		// The bare bool has no "set" marker; the create-seam convention is that
		// the DEFAULT is true. A wire layer that wants to express "false"
		// explicitly passes Singleton=false, which we honor. There is no way to
		// distinguish "unset" from "explicitly false" on a bare bool, so the
		// create-seam applies the intended default (true) only when the wire
		// layer signals it — for now, the v1 create-seam sets Singleton=true
		// unconditionally (the conservative default), and a future wire field
		// (singleton_optional / a pointer) will carry the explicit-override
		// semantics. Documented honestly here.
		spec.Singleton = true
	}
	// OneShotRetry default: when OneShotRetry is true and OneShotMaxRetries is 0
	// (off), apply a default of 3 (the documented create-seam default). A caller
	// that wants a different budget sets it explicitly. One-shot-only (the
	// validateScheduleSpec gate above already rejected a cron with OneShotRetry).
	if spec.OneShotRetry && spec.OneShotMaxRetries == 0 {
		spec.OneShotMaxRetries = defaultOneShotMaxRetries
	}
}

// defaultOneShotMaxRetries is the create-seam default applied when OneShotRetry
// is true and OneShotMaxRetries is 0 (the "set a sensible default" convention — a
// bare int has no "set" marker, so 0 is treated as "unset" on the opt-in path).
const defaultOneShotMaxRetries = 3

// scheduleSingletonExplicit reports whether the caller explicitly set the
// Singleton field. A bare bool has no "set" marker, so v1 treats false as
// "unset" and applies the intended default (true). This stub exists so a future
// wire field (a *bool or a sentinel) can carry explicit-override semantics
// without reworking the create-seam — for now it always returns false (the
// default is always applied when Singleton is false).
func scheduleSingletonExplicit(_ port.ScheduleSpec) bool { return false }

// validateScheduleSpec validates the trigger XOR, the cron grammar (via
// cronparse), the one-shot future invariant, the Mutating/Mode invariant, the
// provider+model selector, and the scheduler cadence floor. It is fail-closed:
// a bad spec is rejected, never silently saved as a never-fires schedule. For
// a cron trigger it ALSO returns the first NextFireAt computed by the SAME
// cronparse.NextFire call that validates the grammar — the parse is inherently
// required to validate a cron expression, so the caller (CreateSchedule)
// reuses this return value instead of parsing the identical expression a
// second time. For a one-shot trigger, or on any validation error, it returns
// the zero time (the caller already knows a one-shot's first fire is its own
// OneShot instant).
//
// It is a manager METHOD (ADR 0076): the selector validation resolves against
// the projected selectable-model inventory (the same provider+model pairs
// ListModels advertises) and the cadence floor against the composition-
// injected scheduler MinInterval — two deployment-level inputs the spec alone
// cannot carry.
func (m *scheduleManager) validateScheduleSpec(ctx context.Context, spec port.ScheduleSpec, now time.Time) (time.Time, error) {
	if spec.Name == "" {
		return time.Time{}, fmt.Errorf("%w: schedule name is required", ErrInvalidArgument)
	}
	if spec.Prompt == "" && len(spec.Parts) == 0 {
		return time.Time{}, fmt.Errorf("%w: prompt or parts is required", ErrInvalidArgument)
	}
	if err := spec.Trigger.Validate(); err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	// OriginSessionID validation: a non-empty OriginSessionID must name an
	// existing session in the store (the session the fire's terminal result
	// will be delivered to). A non-existent session is rejected fail-closed
	// so a delivery pointer that can never resolve is caught at create time
	// rather than surfacing hours later as a fire-time failure. An empty
	// OriginSessionID is always valid (delivery is OFF — the v1 pre-delivery
	// posture).
	if err := m.validateScheduleOrigin(ctx, spec); err != nil {
		return time.Time{}, err
	}
	// OneShotRetry is one-shot-ONLY: a cron self-heals via misfire already
	// (decision #1), so a retry budget on a cron is a misconfiguration the
	// create-seam rejects fail-closed. CarryContext is allowed on either trigger
	// (a cron carrying its prior fire's context is a valid use case).
	if spec.OneShotRetry && spec.Trigger.Kind() != port.TriggerOneShot {
		return time.Time{}, fmt.Errorf("%w: one_shot_retry is one-shot-only (a cron self-heals via misfire — no retry budget)", ErrInvalidArgument)
	}
	if spec.OneShotMaxRetries < 0 {
		return time.Time{}, fmt.Errorf("%w: one_shot_max_retries must be >= 0 (got %d)", ErrInvalidArgument, spec.OneShotMaxRetries)
	}
	// Reject a read-leaning schedule (Mutating=false) with a write-capable Mode
	// (the scheduler_fire.go:54-55 TODO — a read-leaning schedule must not carry
	// a write-capable posture). ModePlan is read-only; ModeDefault/ModeAccept are
	// write-capable. An empty Mode defaults to ModeDefault at fire time, so an
	// empty Mode under Mutating=false is ALSO rejected (the caller must set
	// Mode=plan for a read-leaning schedule, or leave both empty and accept the
	// fire-time plan enforcement — but the create-seam demands an explicit plan
	// mode to avoid the ambiguity).
	mode := spec.Mode
	if mode == "" {
		mode = session.ModeDefault
	}
	if !spec.Mutating && mode != session.ModePlan {
		return time.Time{}, fmt.Errorf("%w: a non-mutating schedule must use plan mode (got %q)", ErrInvalidArgument, mode)
	}
	// Validate the workspace PROFILE-AWARE, mirroring the session create-seam
	// (service.go createSession): a default-profile schedule REQUIRES a workspace
	// (a fire mints a filesystem session), a no-fs schedule must NOT carry one.
	// Enforcing it HERE is fail-closed — otherwise an empty-workspace default
	// schedule is accepted at create but fails at FIRE time ("workspace is
	// required"), i.e. a schedule that can never fire.
	switch SessionProfile(spec.Profile) {
	case ProfileDefault:
		if spec.Workspace == "" {
			return time.Time{}, fmt.Errorf("%w: a default-profile schedule requires a workspace (the fire mints a filesystem session)", ErrInvalidArgument)
		}
	case ProfileNoFS:
		if spec.Workspace != "" {
			return time.Time{}, fmt.Errorf("%w: a %q schedule must not carry a workspace (a no-FS fire has no filesystem to root); got %q", ErrInvalidArgument, ProfileNoFS, spec.Workspace)
		}
	default:
		return time.Time{}, fmt.Errorf("%w: unknown schedule profile %q (supported: \"\" (default) and %q)", ErrInvalidArgument, spec.Profile, ProfileNoFS)
	}
	// Selector validation (ADR 0073, AC1.2c): a non-empty selector must name a
	// provider+model pair the deployment actually serves — resolved against the
	// SAME projected selectable-model inventory ListModels advertises (the
	// composition-computed snapshot, live-swapped by SetModels). Fail-closed,
	// like an invalid cron: a schedule fire must not silently target a provider
	// the deployment never configured, surfacing hours later as a fire-time
	// failure. An empty selector (the deployment default) is ALWAYS valid.
	if err := m.validateScheduleSelector(spec.Selector); err != nil {
		return time.Time{}, err
	}
	switch spec.Trigger.Kind() {
	case port.TriggerOneShot:
		if !spec.Trigger.OneShot.After(now) {
			return time.Time{}, fmt.Errorf("%w: one-shot trigger time must be in the future", ErrInvalidArgument)
		}
	case port.TriggerCron:
		return m.validateCronTrigger(spec, now)
	}
	return time.Time{}, nil
}

// validateCronTrigger validates the cron arm of the trigger switch: the
// grammar (fail-closed via cronparse.NextFire — a bad expression is rejected
// here so a schedule with a bad cron is never saved) and the cadence floor.
// It returns the first NextFireAt computed by the SAME cronparse.NextFire call
// that validates the grammar — the parse is inherently required to validate
// a cron expression, so the caller (validateScheduleSpec) reuses this return
// value instead of parsing the identical expression a second time.
func (m *scheduleManager) validateCronTrigger(spec port.ScheduleSpec, now time.Time) (time.Time, error) {
	loc := scheduler.LoadLocation(spec.Timezone)
	next, err := cronparse.NextFire(spec.Trigger.Cron, now, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid cron expression %q: %v", ErrInvalidArgument, spec.Trigger.Cron, err)
	}
	// The cadence floor (ADR 0073, AC1.3 — SchedulerMinInterval, no longer
	// inert): two consecutive computed fires are the schedule's true
	// cadence, so a fixed-field cron that fires multiple times within one
	// minute (e.g. "*/30 * * * * *" has no seconds field, but "* * * * *"
	// fires every 60s) is measured honestly. A cadence tighter than the
	// floor is rejected fail-closed. A one-shot has no cadence and never
	// reaches this check.
	if floor := m.scheduleMinInterval(); floor > 0 {
		after, err := cronparse.NextFire(spec.Trigger.Cron, next, loc)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: invalid cron expression %q: %v", ErrInvalidArgument, spec.Trigger.Cron, err)
		}
		if cadence := after.Sub(next); cadence < floor {
			return time.Time{}, fmt.Errorf("%w: schedule cadence %v is tighter than the configured minimum interval %v (--scheduler-min-interval)", ErrInvalidArgument, cadence, floor)
		}
	}
	return next, nil
}

// validateScheduleOrigin rejects (fail-closed) a non-empty OriginSessionID that
// names a session not in the held store. An empty OriginSessionID is always
// valid (delivery is OFF). This is extracted from validateScheduleSpec to keep the
// cyclomatic complexity below the gocyclo threshold of 20.
func (m *scheduleManager) validateScheduleOrigin(ctx context.Context, spec port.ScheduleSpec) error {
	if spec.OriginSessionID == "" {
		return nil
	}
	if _, lerr := m.store.Load(ctx, spec.OriginSessionID); lerr != nil {
		return fmt.Errorf("%w: origin_session_id %q must reference an existing session: %w", ErrInvalidArgument, spec.OriginSessionID, lerr)
	}
	return nil
}

// validateScheduleSelector rejects (fail-closed) a non-empty
// ScheduleProviderSelector that names a provider+model pair the deployment
// does not serve, per the projected selectable-model inventory (the same
// composition-computed snapshot ListModels reads; atomically swapped by
// SetModels when the live catalog refresh lands). An empty selector — the
// deployment default — is always valid, as is any pair the inventory
// advertises. A deployment with an EMPTY inventory (a store-only/no-provider
// child service) admits only the empty selector: a fire there can only ever
// run on the default, so a pinned selector could never resolve.
func (m *scheduleManager) validateScheduleSelector(sel port.ScheduleProviderSelector) error {
	if sel.ProviderID == "" && sel.ModelID == "" {
		return nil
	}
	for _, mod := range *m.models.Load() {
		if mod.GetProviderId() == sel.ProviderID && mod.GetId() == sel.ModelID {
			return nil
		}
	}
	return fmt.Errorf("%w: unknown provider+model selector %q/%q (not in the deployment's configured model inventory; an empty selector uses the deployment default)", ErrInvalidArgument, sel.ProviderID, sel.ModelID)
}

// scheduleMinInterval returns the scheduler cadence floor composition injected
// via SetScheduleMinInterval (0 = no floor — the byte-identical pre-floor
// posture). It is independent of the tick loop: the floor guards the
// create-seam whether or not a scheduler is wired (a --no-scheduler deployment
// still manages schedules manually through this seam).
func (m *scheduleManager) scheduleMinInterval() time.Duration {
	return time.Duration(m.scheduleMinIntervalNanos.Load())
}

// GetSchedule loads a schedule by name.
func (m *scheduleManager) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	return m.schedStore.Load(ctx, name)
}

// ListSchedules returns all stored schedules.
func (m *scheduleManager) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	return m.schedStore.List(ctx)
}

// UpdateSchedule re-validates the spec (the same create-seam validation) and
// overwrites the Spec half while PRESERVING the State half (firing progress). It
// Loads the existing schedule, validates the new spec, and Saves with the
// existing State.
func (m *scheduleManager) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	now := m.now()
	// The computed cron next-fire is not needed here (Update preserves the
	// existing State, including NextFireAt); the call is still made for its
	// validation side effect (the shared create-seam checks).
	if _, err := m.validateScheduleSpec(ctx, spec, now); err != nil {
		return port.Schedule{}, err
	}
	applyScheduleDefaults(&spec)
	existing, err := m.schedStore.Load(ctx, spec.Name)
	if err != nil {
		return port.Schedule{}, err
	}
	// Preserve the existing State (firing progress) AND the creation timestamp —
	// only the operator-authored Spec fields change on an Update. CreatedAt is a
	// store-side timestamp, never operator-authored, so it must not be clobbered
	// to the zero value (which the reconcile update path would otherwise do on
	// every restart, destroying the audit trail).
	spec.CreatedAt = existing.Spec.CreatedAt
	updated := port.Schedule{Spec: spec, State: existing.State}
	if err := m.schedStore.Save(ctx, updated); err != nil {
		return port.Schedule{}, err
	}
	return updated, nil
}

// DeleteSchedule removes a schedule by name. It is idempotent (the store's
// Delete discipline).
func (m *scheduleManager) DeleteSchedule(ctx context.Context, name string) error {
	return m.schedStore.Delete(ctx, name)
}

// PauseSchedule disables a schedule (Enabled=false) without deleting it. It
// calls SetEnabled — the dedicated atomic flag update — because Save CANNOT
// mutate Enabled (Save preserves the existing State half on a Spec overwrite).
func (m *scheduleManager) PauseSchedule(ctx context.Context, name string) error {
	return m.schedStore.SetEnabled(ctx, name, false)
}

// ResumeSchedule re-enables a paused schedule (Enabled=true). It calls
// SetEnabled — see PauseSchedule's doc.
func (m *scheduleManager) ResumeSchedule(ctx context.Context, name string) error {
	return m.schedStore.SetEnabled(ctx, name, true)
}

// GetFire loads a fire record by id. It is the read-side sibling of
// ListFires (NOT part of port.ScheduleManager — the agent tool does not need
// it; the gRPC/REST FireNow surface does). Kept on the manager so the Service
// delegates the whole schedule surface to ONE truth.
func (m *scheduleManager) GetFire(ctx context.Context, fireID string) (port.ScheduleFire, error) {
	return m.schedStore.LoadFire(ctx, fireID)
}

// ListFires returns the fire records for a schedule.
func (m *scheduleManager) ListFires(ctx context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	return m.schedStore.ListFires(ctx, scheduleName)
}

// FireNow manually fires a schedule by name. It delegates to the scheduler's
// FireNow (if wired) and maps the scheduler pkg's sentinels to the server
// sentinels. When no scheduler is wired it distinguishes the two possible
// causes: no ScheduleStore at all (ErrNoScheduleStore — Create/List etc. don't
// work either) vs a ScheduleStore present but no in-process scheduler driving
// it (ErrSchedulerNotRunning — the store works fine, there's just nothing to
// fire a manual request through).
func (m *scheduleManager) FireNow(ctx context.Context, name string) (port.ScheduleFire, error) {
	schedPtr := m.scheduler.Load()
	if schedPtr == nil {
		// The manager is only ever constructed with a non-nil schedStore, so
		// this branch is always ErrSchedulerNotRunning (store present, no tick
		// loop). The ErrNoScheduleStore arm is reachable only through the
		// Service's delegating wrapper when the manager is nil (a store with no
		// ScheduleStore — the Service returns ErrNoScheduleStore before
		// reaching here).
		return port.ScheduleFire{}, ErrSchedulerNotRunning
	}
	fire, err := schedPtr.FireNow(ctx, name, m.now())
	if err != nil {
		switch {
		case errors.Is(err, scheduler.ErrFireNowDisabled):
			return port.ScheduleFire{}, fmt.Errorf("%w: %v", ErrScheduleDisabled, err)
		case errors.Is(err, scheduler.ErrFireNowOverlap):
			return port.ScheduleFire{}, fmt.Errorf("%w: %v", ErrFireNowOverlap, err)
		case errors.Is(err, scheduler.ErrFireNowExhausted):
			return port.ScheduleFire{}, fmt.Errorf("%w: %v", ErrScheduleExhausted, err)
		case errors.Is(err, scheduler.ErrNotLeader):
			return port.ScheduleFire{}, fmt.Errorf("%w: %v", ErrScheduleNotLeader, err)
		default:
			return port.ScheduleFire{}, err
		}
	}
	return fire, nil
}

// EmitScheduleEvent appends a SchedulePayload as an EvSchedule* event to the
// fire session's durable EventLog. It is the composition-injected emit callback
// the scheduler invokes (via Config.EmitScheduleEvent) for fired/failed/skipped
// fires. For v1 delivery is durable-log-only (pull-only via GetFire/ListFires);
// a live broadcast stream is a future phase. A skipped fire (no session id) is
// dropped from the durable log (the log is session-keyed) and surfaces only via
// the operator diagnostic. A nil EventLog is a no-op (byte-identical to the
// no-emit path). An Append failure WARNs, never aborts (a broken durable log
// must not break the fire).
func (m *scheduleManager) EmitScheduleEvent(payload session.SchedulePayload) {
	m.emitScheduleEvent(payload)
}

func (m *scheduleManager) emitScheduleEvent(payload session.SchedulePayload) {
	if m.eventLog == nil {
		return
	}
	if payload.SessionID == "" {
		// A skipped fire has no session to log under; the live client wire (the
		// next chunk's handlers) is the channel for skip events. The durable
		// log is session-keyed, so a sessionless event has nowhere to land.
		return
	}
	ev := session.Event{
		Type:     scheduleEventType(payload.Kind),
		Schedule: &payload,
	}
	// Cancel-detached so a fire's ctx (which may be cancelled when the run
	// ends) cannot abort the durable append (the appendEvent precedent).
	ctx := context.WithoutCancel(context.Background())
	if err := m.eventLog.Append(ctx, payload.SessionID, ev); err != nil {
		if m.diag != nil {
			m.diag.Log(ctx, port.LevelWarn, "schedule event log append failed",
				"session", string(payload.SessionID), "kind", payload.Kind, "err", err.Error())
		}
	}
}

// SetScheduler wires a scheduler onto the manager. It is the late-bind seam for
// the scheduled-tasks tick loop (issue #189, Phase 1f): buildScheduler needs the
// Service for the FireFunc, so the scheduler is built AFTER NewService and
// attached here. Nil-safe.
func (m *scheduleManager) SetScheduler(sch *scheduler.Scheduler) {
	m.scheduler.Store(sch)
}

// SetScheduleMinInterval injects the scheduler cadence floor the create-seam
// enforces (ADR 0073, AC1.3 — the composition half of
// scheduler.Config.MinInterval / app Config.SchedulerMinInterval, previously
// inert while there was no in-band create API). It lives on the manager, NOT
// the scheduler: the floor guards the SHARED validateScheduleSpec — the
// Schedule tool's create AND the REST/gRPC create — whether or not the tick
// loop runs (a --no-scheduler deployment still manages schedules manually).
// 0 disables the floor. Called once by composition before serving; atomic so
// an in-flight create never tears against it.
func (m *scheduleManager) SetScheduleMinInterval(d time.Duration) {
	m.scheduleMinIntervalNanos.Store(int64(d))
}

// HasScheduler reports whether a scheduler was wired into this manager. It is
// the read-side companion to SetScheduler: nil-safe (the byte-identical
// default wires no scheduler).
func (m *scheduleManager) HasScheduler() bool {
	return m.scheduler.Load() != nil
}

// scheduleEventType maps the payload Kind ("fired"/"skipped"/"failed") to the
// session EventType const. An unknown kind falls back to EvScheduleFired (the
// benign default — never silently drops the event).
func scheduleEventType(kind string) session.EventType {
	switch kind {
	case "skipped":
		return session.EvScheduleSkipped
	case "failed":
		return session.EvScheduleFailed
	default:
		return session.EvScheduleFired
	}
}

// Compile-time assertion that *scheduleManager satisfies port.ScheduleManager
// (all nine verbs). The Service delegates to this interface; the agent-layer
// Schedule tool consumes it.
var _ port.ScheduleManager = (*scheduleManager)(nil)
