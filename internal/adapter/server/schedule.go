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

// GetSchedule delegates to the embedded scheduleManager.
func (s *Service) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.Schedule{}, ErrNoScheduleStore
	}
	return mgr.GetSchedule(ctx, name)
}

// ListSchedules delegates to the embedded scheduleManager.
func (s *Service) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return nil, ErrNoScheduleStore
	}
	return mgr.ListSchedules(ctx)
}

// UpdateSchedule delegates to the embedded scheduleManager. The manager
// re-validates the spec (the shared create-seam) and overwrites the Spec half
// while preserving the State half (firing progress) + the CreatedAt timestamp.
func (s *Service) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	mgr := s.schedMgr
	if mgr == nil {
		return port.Schedule{}, ErrNoScheduleStore
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
	return mgr.PauseSchedule(ctx, name)
}

// ResumeSchedule delegates to the embedded scheduleManager. See PauseSchedule.
func (s *Service) ResumeSchedule(ctx context.Context, name string) error {
	mgr := s.schedMgr
	if mgr == nil {
		return ErrNoScheduleStore
	}
	return mgr.ResumeSchedule(ctx, name)
}

// GetFire delegates to the embedded scheduleManager. It is the read-side
// sibling of ListFires (NOT part of port.ScheduleManager — the agent tool does
// not need it; the gRPC/REST FireNow surface does).
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
	return mgr.FireNow(ctx, name)
}

// EmitScheduleEvent delegates to the embedded scheduleManager. The manager
// appends a SchedulePayload as an EvSchedule* event to the fire session's
// durable EventLog (nil EventLog ⇒ a no-op). When no manager is wired it is a
// no-op too (the byte-identical no-emit path — there is no schedule surface to
// emit for).
func (s *Service) EmitScheduleEvent(payload session.SchedulePayload) {
	if m := s.schedMgr; m != nil {
		m.EmitScheduleEvent(payload)
	}
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
