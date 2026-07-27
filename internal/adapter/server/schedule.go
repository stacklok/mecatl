package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/cronparse"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

// Sentinel errors for the schedule surface are defined in errors.go (the
// single error-classification chokepoint): ErrNoScheduleStore,
// ErrSchedulerNotRunning, ErrScheduleDisabled (wraps
// scheduler.ErrFireNowDisabled), ErrFireNowOverlap (wraps
// scheduler.ErrFireNowOverlap). toStatus / writeServiceError map them in the
// one place alongside the team/session sentinels.

// scheduleStoreVal is the memoised result of type-asserting the configured Store
// for a ScheduleStore (the PrunableStore/SessionLease precedent). It is computed
// once on first use and cached so the assertion cost is paid a single time. A nil
// ScheduleStore is cached as a sentinel "no schedule store" so the byte-identical
// no-scheduling path stays cheap. Guarded by scheduleStoreMu.
type scheduleStoreCache struct {
	mu      sync.Mutex
	store   port.ScheduleStore
	checked bool
}

// scheduleStore returns the ScheduleStore discovered by type-assertion on the
// configured Store (the PrunableStore/SessionLease precedent). Returns nil if
// the backend does not implement ScheduleStore (the byte-identical no-schedule
// path). The nil result is memoised so the assertion runs at most once.
func (s *Service) scheduleStore() port.ScheduleStore {
	s.scheduleStoreCache.mu.Lock()
	defer s.scheduleStoreCache.mu.Unlock()
	if s.scheduleStoreCache.checked {
		return s.scheduleStoreCache.store
	}
	s.scheduleStoreCache.checked = true
	if s.cfg.Store == nil {
		return nil
	}
	// The jsonlstore + redisstore expose a ScheduleStore() ACCESSOR returning a
	// port.ScheduleStore (or nil). A store that implements the accessor with a
	// non-nil return is the schedule backend; one that does not expose the
	// accessor (or returns nil) has no schedule support.
	type scheduleStoreProvider interface {
		ScheduleStore() port.ScheduleStore
	}
	if p, ok := s.cfg.Store.(scheduleStoreProvider); ok {
		s.scheduleStoreCache.store = p.ScheduleStore()
	}
	return s.scheduleStoreCache.store
}

// CreateSchedule is the create-seam for a schedule: it validates the spec
// fail-closed, applies the intended defaults (via applyScheduleDefaults — the
// SHARED helper UpdateSchedule also calls, so a PUT omitting singleton does not
// silently disable the guard), computes the first NextFireAt (cron via cronparse;
// one-shot is the OneShot instant), enforces the Mutating/Mode invariant (a
// read-leaning schedule — Mutating=false — must run in plan mode, never a
// write-capable posture), and Saves the schedule. It returns the saved
// schedule.
func (s *Service) CreateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	store := s.scheduleStore()
	if store == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	now := s.cfg.Now()
	cronNextFire, err := s.validateScheduleSpec(ctx, spec, now)
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
	if _, lerr := store.Load(ctx, spec.Name); lerr == nil {
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
	if err := store.Save(ctx, sched); err != nil {
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
// It is a Service METHOD (ADR 0073): the selector validation resolves against
// the projected selectable-model inventory (the same provider+model pairs
// ListModels advertises) and the cadence floor against the composition-
// injected scheduler MinInterval — two deployment-level inputs the spec
// alone cannot carry.
func (s *Service) validateScheduleSpec(ctx context.Context, spec port.ScheduleSpec, now time.Time) (time.Time, error) {
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
	if err := s.validateScheduleOrigin(ctx, spec); err != nil {
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
	if err := s.validateScheduleSelector(spec.Selector); err != nil {
		return time.Time{}, err
	}
	switch spec.Trigger.Kind() {
	case port.TriggerOneShot:
		if !spec.Trigger.OneShot.After(now) {
			return time.Time{}, fmt.Errorf("%w: one-shot trigger time must be in the future", ErrInvalidArgument)
		}
	case port.TriggerCron:
		return s.validateCronTrigger(spec, now)
	}
	return time.Time{}, nil
}

// validateCronTrigger validates the cron arm of the trigger switch: the
// grammar (fail-closed via cronparse.NextFire — a bad expression is rejected
// here so a schedule with a bad cron is never saved) and the cadence floor.
// It returns the first NextFireAt computed by the SAME cronparse.NextFire call
// that validates the grammar — the parse is inherently required to validate a
// cron expression, so the caller (validateScheduleSpec) reuses this return
// value instead of parsing the identical expression a second time.
func (s *Service) validateCronTrigger(spec port.ScheduleSpec, now time.Time) (time.Time, error) {
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
	if floor := s.scheduleMinInterval(); floor > 0 {
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
// names a session not in the configured store. An empty OriginSessionID is always
// valid (delivery is OFF). This is extracted from validateScheduleSpec to keep the
// cyclomatic complexity below the gocyclo threshold of 20.
func (s *Service) validateScheduleOrigin(ctx context.Context, spec port.ScheduleSpec) error {
	if spec.OriginSessionID == "" {
		return nil
	}
	if _, lerr := s.cfg.Store.Load(ctx, spec.OriginSessionID); lerr != nil {
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
func (s *Service) validateScheduleSelector(sel port.ScheduleProviderSelector) error {
	if sel.ProviderID == "" && sel.ModelID == "" {
		return nil
	}
	for _, m := range *s.models.Load() {
		if m.GetProviderId() == sel.ProviderID && m.GetId() == sel.ModelID {
			return nil
		}
	}
	return fmt.Errorf("%w: unknown provider+model selector %q/%q (not in the deployment's configured model inventory; an empty selector uses the deployment default)", ErrInvalidArgument, sel.ProviderID, sel.ModelID)
}

// scheduleMinInterval returns the scheduler cadence floor composition
// injected via SetScheduleMinInterval (0 = no floor — the byte-identical
// pre-floor posture). It is independent of the tick loop: the floor guards
// the create-seam whether or not a scheduler is wired (a --no-scheduler
// deployment still manages schedules manually through this seam).
func (s *Service) scheduleMinInterval() time.Duration {
	return time.Duration(s.scheduleMinIntervalNanos.Load())
}

// GetSchedule loads a schedule by name.
func (s *Service) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	store := s.scheduleStore()
	if store == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	sched, err := store.Load(ctx, name)
	if err != nil {
		return port.Schedule{}, err
	}
	return sched, nil
}

// ListSchedules returns all stored schedules.
func (s *Service) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	store := s.scheduleStore()
	if store == nil {
		return nil, ErrNoScheduleStore
	}
	return store.List(ctx)
}

// UpdateSchedule re-validates the spec (the same create-seam validation) and
// overwrites the Spec half while PRESERVING the State half (firing progress). It
// Loads the existing schedule, validates the new spec, and Saves with the
// existing State.
func (s *Service) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	store := s.scheduleStore()
	if store == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	now := s.cfg.Now()
	// The computed cron next-fire is not needed here (Update preserves the
	// existing State, including NextFireAt); the call is still made for its
	// validation side effect (the shared create-seam checks).
	if _, err := s.validateScheduleSpec(ctx, spec, now); err != nil {
		return port.Schedule{}, err
	}
	applyScheduleDefaults(&spec)
	existing, err := store.Load(ctx, spec.Name)
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
	if err := store.Save(ctx, updated); err != nil {
		return port.Schedule{}, err
	}
	return updated, nil
}

// DeleteSchedule removes a schedule by name. It is idempotent (the store's
// Delete discipline).
func (s *Service) DeleteSchedule(ctx context.Context, name string) error {
	store := s.scheduleStore()
	if store == nil {
		return ErrNoScheduleStore
	}
	return store.Delete(ctx, name)
}

// PauseSchedule disables a schedule (Enabled=false) without deleting it. It
// calls SetEnabled — the dedicated atomic flag update — because Save CANNOT
// mutate Enabled (Save preserves the existing State half on a Spec overwrite).
func (s *Service) PauseSchedule(ctx context.Context, name string) error {
	store := s.scheduleStore()
	if store == nil {
		return ErrNoScheduleStore
	}
	return store.SetEnabled(ctx, name, false)
}

// ResumeSchedule re-enables a paused schedule (Enabled=true). It calls
// SetEnabled — see PauseSchedule's doc.
func (s *Service) ResumeSchedule(ctx context.Context, name string) error {
	store := s.scheduleStore()
	if store == nil {
		return ErrNoScheduleStore
	}
	return store.SetEnabled(ctx, name, true)
}

// GetFire loads a fire record by id.
func (s *Service) GetFire(ctx context.Context, fireID string) (port.ScheduleFire, error) {
	store := s.scheduleStore()
	if store == nil {
		return port.ScheduleFire{}, ErrNoScheduleStore
	}
	return store.LoadFire(ctx, fireID)
}

// ListFires returns the fire records for a schedule.
func (s *Service) ListFires(ctx context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	store := s.scheduleStore()
	if store == nil {
		return nil, ErrNoScheduleStore
	}
	return store.ListFires(ctx, scheduleName)
}

// FireNow manually fires a schedule by name. It delegates to the scheduler's
// FireNow (if wired) and maps the scheduler pkg's sentinels to the server
// sentinels. When no scheduler is wired it distinguishes the two possible
// causes: no ScheduleStore at all (ErrNoScheduleStore — Create/List etc. don't
// work either) vs a ScheduleStore present but no in-process scheduler driving
// it (ErrSchedulerNotRunning — the store works fine, there's just nothing to
// fire a manual request through).
func (s *Service) FireNow(ctx context.Context, name string) (port.ScheduleFire, error) {
	if s.scheduler == nil {
		if s.scheduleStore() == nil {
			return port.ScheduleFire{}, ErrNoScheduleStore
		}
		return port.ScheduleFire{}, ErrSchedulerNotRunning
	}
	fire, err := s.scheduler.FireNow(ctx, name, s.cfg.Now())
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
func (s *Service) EmitScheduleEvent(payload session.SchedulePayload) {
	s.emitScheduleEvent(payload)
}

func (s *Service) emitScheduleEvent(payload session.SchedulePayload) {
	if s.cfg.EventLog == nil {
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
	if err := s.cfg.EventLog.Append(ctx, payload.SessionID, ev); err != nil {
		if s.cfg.Diagnostics != nil {
			s.cfg.Diagnostics.Log(ctx, port.LevelWarn, "schedule event log append failed",
				"session", string(payload.SessionID), "kind", payload.Kind, "err", err.Error())
		}
	}
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
