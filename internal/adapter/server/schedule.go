package server

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
)

// schedule.go is the *Service's THIN DELEGATING schedule surface (ADR 0076):
// the validated create/read/update/fire seam moved OFF *Service onto the
// store-shaped scheduleManager (schedule_manager.go) — one seam, one truth.
// The Service keeps delegating wrappers so the RPC surface (grpc_schedule.go,
// the REST /v1/schedules handlers, the mecatui /schedule overlay) is
// byte-identical: every verb forwards to the embedded manager (s.schedMgr),
// and the no-schedule path (no ScheduleStore → a nil manager) returns the
// SAME sentinels the manager path distinguishes. The cadence-floor +
// scheduler late-setters delegate to the manager too.
//
// Sentinel errors for the schedule surface are defined in errors.go (the
// single error-classification chokepoint): ErrNoScheduleStore,
// ErrSchedulerNotRunning, ErrScheduleDisabled (wraps
// scheduler.ErrFireNowDisabled), ErrFireNowOverlap (wraps
// scheduler.ErrFireNowOverlap). toStatus / writeServiceError map them in the
// one place alongside the team/session sentinels.

// CreateSchedule delegates to the embedded scheduleManager (the single
// schedule truth, ADR 0076). The manager validates the spec fail-closed,
// applies the create-seam defaults, computes the first NextFireAt, and Saves.
// When no manager is wired (the store backs no ScheduleStore) it returns
// ErrNoScheduleStore — the byte-identical no-scheduling path the gRPC/REST
// handlers map to Unimplemented.
func (s *Service) CreateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	return mgr.CreateSchedule(ctx, spec)
}

// GetSchedule returns an owned schedule by name. A mismatch deliberately has
// the same port-level not-found result as an absent schedule.
func (s *Service) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	sched, err := mgr.GetSchedule(ctx, name)
	if err != nil {
		return port.Schedule{}, err
	}
	if err := s.authorizeSchedule(ctx, sched.Spec.Owner); err != nil {
		return port.Schedule{}, err
	}
	return sched, nil
}

// ListSchedules filters before returning the collection so an ownerless or
// foreign record cannot affect caller-visible list metadata.
func (s *Service) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return nil, ErrNoScheduleStore
	}
	schedules, err := mgr.ListSchedules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]port.Schedule, 0, len(schedules))
	for _, sched := range schedules {
		if s.ownsResource(ctx, sched.Spec.Owner) {
			out = append(out, sched)
		}
	}
	return out, nil
}

// UpdateSchedule delegates to the embedded scheduleManager. The manager
// re-validates the spec (the shared create-seam) and overwrites the Spec half
// while preserving the State half (firing progress) + the CreatedAt timestamp.
func (s *Service) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSchedule(ctx, spec.Name); err != nil {
			return port.Schedule{}, err
		}
	}
	return mgr.UpdateSchedule(ctx, spec)
}

// DeleteSchedule delegates to the embedded scheduleManager. Idempotent (the
// store's Delete discipline).
func (s *Service) DeleteSchedule(ctx context.Context, name string) error {
	mgr := s.schedMgr
	if mgr == nil {
		return ErrNoScheduleStore
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSchedule(ctx, name); err != nil {
			return err
		}
	}
	return mgr.DeleteSchedule(ctx, name)
}

// PauseSchedule delegates to the embedded scheduleManager. The manager calls
// SetEnabled — the dedicated atomic flag update — because Save CANNOT mutate
// Enabled (Save preserves the existing State half on a Spec overwrite).
func (s *Service) PauseSchedule(ctx context.Context, name string) error {
	mgr := s.schedMgr
	if mgr == nil {
		return ErrNoScheduleStore
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSchedule(ctx, name); err != nil {
			return err
		}
	}
	return mgr.PauseSchedule(ctx, name)
}

// ResumeSchedule delegates to the embedded scheduleManager. See PauseSchedule.
func (s *Service) ResumeSchedule(ctx context.Context, name string) error {
	mgr := s.schedMgr
	if mgr == nil {
		return ErrNoScheduleStore
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSchedule(ctx, name); err != nil {
			return err
		}
	}
	return mgr.ResumeSchedule(ctx, name)
}

// GetFire delegates to the embedded scheduleManager, which resolves and
// authorizes the stored physical parent key before returning the fire.
func (s *Service) GetFire(ctx context.Context, fireID string) (port.ScheduleFire, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.ScheduleFire{}, ErrNoScheduleStore
	}
	return mgr.GetFire(ctx, fireID)
}

// ListFires delegates to the embedded scheduleManager.
func (s *Service) ListFires(ctx context.Context, scheduleName string) ([]port.ScheduleFire, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return nil, ErrNoScheduleStore
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSchedule(ctx, scheduleName); err != nil {
			return nil, err
		}
	}
	return mgr.ListFires(ctx, scheduleName)
}

// FireNow delegates to the embedded scheduleManager. The manager distinguishes
// the no-scheduler states: a nil manager (no ScheduleStore at all) →
// ErrNoScheduleStore here; a non-nil manager with no scheduler wired →
// ErrSchedulerNotRunning (the manager's own FireNow). A wired scheduler's
// sentinels (ErrScheduleDisabled / ErrFireNowOverlap / ErrScheduleExhausted /
// ErrScheduleNotLeader) are mapped by the manager.
func (s *Service) FireNow(ctx context.Context, name string) (port.ScheduleFire, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.ScheduleFire{}, ErrNoScheduleStore
	}
	if s.cfg.OwnershipEnforced {
		if _, err := s.GetSchedule(ctx, name); err != nil {
			return port.ScheduleFire{}, err
		}
	}
	return mgr.FireNow(ctx, name)
}

// EmitScheduleEvent appends a SchedulePayload as an EvSchedule* event to the fire
// session's durable EventLog. It is the composition-injected emit callback the
// scheduler invokes (via scheduler.Config.EmitScheduleEvent) for fired/failed/
// skipped fires. For v1 delivery is durable-log-only (pull-only via
// GetFire/ListFires); a live broadcast stream is a future phase. A skipped fire
// (no session id) is dropped from the durable log (the log is session-keyed) and
// surfaces only via the operator diagnostic.
//
// It routes through the ONE appendEvent chokepoint so the lifecycle events are
// stamped with the acting caller exactly like the fire's run events — the
// scheduler's system principal for a tick fire, the requester for a manual
// FireNow (ADR 0100 decision 5: every durable append path stamps). ctx is the
// caller's; it is cancel-detached here so a fire's finished/cancelled ctx cannot
// abort the durable append, while its VALUES (the principal) survive.
func (s *Service) EmitScheduleEvent(ctx context.Context, payload session.SchedulePayload) {
	if payload.SessionID == "" {
		return
	}
	s.appendEvent(context.WithoutCancel(ctx), payload.SessionID, session.Event{
		Type:     scheduleEventType(payload.Kind),
		Schedule: &payload,
	})
}

// scheduleStore returns the ScheduleStore the capabilities gate (Scheduling)
// reads — now off the embedded manager (the Service no longer self-discovers
// the store; it consumes the manager, ADR 0076). Returns nil when the store
// backs no ScheduleStore (the byte-identical no-schedule path), so
// ServerCapabilities.Scheduling stays false honestly.
func (s *Service) scheduleStore() port.ScheduleStore {
	if m := s.schedMgr; m != nil {
		return m.scheduleStore()
	}
	return nil
}

// SetScheduler wires a scheduler onto the embedded scheduleManager (the
// late-bind seam, ADR 0076). It is the delegated setter: composition builds the
// scheduler AFTER NewService (the FireFunc closes over the Service) and
// attaches it here; the manager holds the atomic scheduler pointer FireNow
// reads. Nil-safe (no manager wired → no-op, the byte-identical no-scheduling
// path). The Service's own s.mu is no longer involved (the manager's atomic
// pointer is the single truth); Close/Drain read s.schedMgr.HasScheduler()
// instead of a Service-held scheduler field.
func (s *Service) SetScheduler(sch *scheduler.Scheduler) {
	if m := s.schedMgr; m != nil {
		m.SetScheduler(sch)
	}
}

// SetScheduleMinInterval injects the scheduler cadence floor the create-seam
// enforces (ADR 0073, AC1.3). Delegated to the embedded manager: the floor
// guards the SHARED validateScheduleSpec — the Schedule tool's create AND the
// REST/gRPC create — whether or not the tick loop runs (a --no-scheduler
// deployment still manages schedules manually). 0 disables the floor. Called
// once by composition before serving. No-op when no manager is wired (no
// schedule create-seam to guard).
func (s *Service) SetScheduleMinInterval(d time.Duration) {
	if m := s.schedMgr; m != nil {
		m.SetScheduleMinInterval(d)
	}
}

// HasScheduler reports whether a scheduler was wired into the embedded
// scheduleManager. It is the read-side companion to SetScheduler: nil-safe (the
// byte-identical default wires no scheduler, and a store with no ScheduleStore
// has no manager at all).
func (s *Service) HasScheduler() bool {
	if m := s.schedMgr; m != nil {
		return m.HasScheduler()
	}
	return false
}

// ScheduleManager returns the consumer-local port.ScheduleManager the
// model-facing Schedule tool (ADR 0073) drives: the embedded scheduleManager
// (the single truth, ADR 0076) — NOT the Service itself. It returns nil UNLESS
// the configured Store backs a port.ScheduleStore (the manager constructor
// returns nil in that case) — the SAME conditional gate the capabilities echo
// (Scheduling) uses, so the tool registration and the capability bit agree and
// a store-less deployment gets the honest absent-tool path, never a stub.
// Composition calls it AFTER NewService (the manager is constructed before
// buildEngine and handed to the Service via Config.ScheduleManager, or
// self-constructed by the Service from cfg.Store for the legacy test path) and
// injects the result into the catalog assets for the Schedule tool's
// registration.
func (s *Service) ScheduleManager() port.ScheduleManager {
	if s.schedMgr == nil {
		return nil
	}
	return s.schedMgr
}
