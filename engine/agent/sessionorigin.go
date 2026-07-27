package agent

import (
	"context"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// OriginBinder is the narrow interface the engine calls in startRun to bind the
// executing session's id so per-run state (e.g. the Schedule tool's origin capture)
// can record which session produced this run's side effects. It is satisfied by
// *SessionOriginScheduleManager. The method is BindSessionOrigin(session.SessionID).
type OriginBinder interface {
	BindSessionOrigin(session.SessionID)
}

// SessionOriginScheduleManager wraps a real port.ScheduleManager, holding the
// current origin session id in a sync/atomic.Pointer[session.SessionID]. Its
// CreateSchedule stamps spec.OriginSessionID = <current id> (empty if unbound)
// before delegating to the wrapped manager. All other methods delegate verbatim.
//
// Why race-free: Schedule tool is ReadOnly()==false → mutate-serial; the engine
// drives ONE session's run at a time → the atomic pointer always names the
// executing session. Works for shared AND per-session engines. NOT a ctx-value,
// NOT a parentCaps widening.
type SessionOriginScheduleManager struct {
	inner port.ScheduleManager
	org   atomic.Pointer[session.SessionID]
}

// NewSessionOriginScheduleManager constructs the wrapper over mgr. mgr must be
// non-nil; NewSessionOriginScheduleManager panics otherwise (a composition-root
// programming error, same contract as the ScheduleTool constructors).
func NewSessionOriginScheduleManager(mgr port.ScheduleManager) *SessionOriginScheduleManager {
	if mgr == nil {
		panic("agent: NewSessionOriginScheduleManager requires a non-nil ScheduleManager")
	}
	return &SessionOriginScheduleManager{inner: mgr}
}

// BindSessionOrigin sets the current origin session id. It is called by the engine
// in startRun before every run, and by nothing else — the id is per-run state
// bound on the same goroutine that later calls the tool.
func (w *SessionOriginScheduleManager) BindSessionOrigin(id session.SessionID) {
	w.org.Store(&id)
}

// CreateSchedule stamps spec.OriginSessionID from the bound id (empty if unbound),
// then delegates to the wrapped manager. The model-supplied args never reach the
// OriginSessionID field — the bound id wins unconditionally.
func (w *SessionOriginScheduleManager) CreateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	spec.OriginSessionID = w.currentID()
	return w.inner.CreateSchedule(ctx, spec)
}

// currentID reads the bound session id, returning "" if unbound.
func (w *SessionOriginScheduleManager) currentID() session.SessionID {
	ptr := w.org.Load()
	if ptr == nil {
		return ""
	}
	return *ptr
}

// GetSchedule delegates verbatim.
func (w *SessionOriginScheduleManager) GetSchedule(ctx context.Context, name string) (port.Schedule, error) {
	return w.inner.GetSchedule(ctx, name)
}

// ListSchedules delegates verbatim.
func (w *SessionOriginScheduleManager) ListSchedules(ctx context.Context) ([]port.Schedule, error) {
	return w.inner.ListSchedules(ctx)
}

// UpdateSchedule delegates verbatim.
func (w *SessionOriginScheduleManager) UpdateSchedule(ctx context.Context, spec port.ScheduleSpec) (port.Schedule, error) {
	return w.inner.UpdateSchedule(ctx, spec)
}

// DeleteSchedule delegates verbatim.
func (w *SessionOriginScheduleManager) DeleteSchedule(ctx context.Context, name string) error {
	return w.inner.DeleteSchedule(ctx, name)
}

// PauseSchedule delegates verbatim.
func (w *SessionOriginScheduleManager) PauseSchedule(ctx context.Context, name string) error {
	return w.inner.PauseSchedule(ctx, name)
}

// ResumeSchedule delegates verbatim.
func (w *SessionOriginScheduleManager) ResumeSchedule(ctx context.Context, name string) error {
	return w.inner.ResumeSchedule(ctx, name)
}

// FireNow delegates verbatim.
func (w *SessionOriginScheduleManager) FireNow(ctx context.Context, name string) (port.ScheduleFire, error) {
	return w.inner.FireNow(ctx, name)
}

// ListFires delegates verbatim.
func (w *SessionOriginScheduleManager) ListFires(ctx context.Context, name string) ([]port.ScheduleFire, error) {
	return w.inner.ListFires(ctx, name)
}

// Compile-time assertion: the wrapper satisfies the port.
var _ port.ScheduleManager = (*SessionOriginScheduleManager)(nil)
