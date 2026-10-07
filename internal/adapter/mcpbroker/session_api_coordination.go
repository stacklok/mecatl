package mcpbroker

import (
	"context"
	"math"

	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

type apiOperationKey struct{}
type apiOperation struct {
	state      *apiState
	generation uint64
	control    bool
}

type apiControl struct {
	connection c.ConnectionRef
	enrollment c.EnrollmentRef
	delete     bool
	passive    bool
}

func newAPIState(record apiRecord) *apiState {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &apiState{record: record, snapshot: record, ioGate: gate, parked: make(map[c.AuthorizationRef]*apiParked)}
}

func (s *SessionAPI) validOperation(ctx context.Context) bool {
	op, ok := ctx.Value(apiOperationKey{}).(*apiOperation)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !op.state.deleted && op.generation == op.state.generation && (op.control || !op.state.invalidated)
}

func (s *SessionAPI) invalidateLocked(st *apiState) {
	st.invalidated = true
	if st.generation != math.MaxUint64 {
		st.generation++
	}
	if st.opCancel != nil {
		st.opCancel()
	}
}

// Caller identity is checked before interrupting a cached operation. Cold controls
// validate metadata under the same gate, without recovering native credentials.
func (s *SessionAPI) operation(ctx context.Context, ref c.SessionRef, control apiControl) (context.Context, func(), error) {
	if !validAPIRef(string(ref)) {
		return ctx, nil, c.ErrStateUnavailable
	}
	owner, workload, err := s.partitions(ctx)
	if err != nil {
		return ctx, nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ctx, nil, c.ErrStateUnavailable
	}
	st := s.states[ref]
	if st == nil {
		if len(s.states) >= 32 {
			s.mu.Unlock()
			return ctx, nil, c.ErrCapacity
		}
		st = newAPIState(apiRecord{Ref: ref})
		s.states[ref] = st
	}
	if st.loaded || st.retained {
		r := st.snapshot
		if r.Owner != owner || r.Workload != workload || r.Profile != s.profile() || !s.now().Before(r.ExpiresAt) {
			s.mu.Unlock()
			return ctx, nil, c.ErrStateUnavailable
		}
	}
	invalidating := control.delete || control.connection != "" || control.enrollment != ""
	matches := st.loaded && ((control.delete) ||
		(control.connection != "" && string(control.connection) == st.snapshot.Connection && (st.snapshot.Connected || st.snapshot.Withdrawing)) ||
		(control.enrollment != "" && control.enrollment == st.enrollmentRef))
	if matches {
		s.invalidateLocked(st)
	}
	// Add and the closed check share the lock with Close; this includes gate waits.
	s.workers.Add(1)
	st.users++
	generation := st.generation
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		s.finishOwnership(st)
		return ctx, nil, ctx.Err()
	case <-s.ctx.Done():
		s.finishOwnership(st)
		return ctx, nil, c.ErrStateUnavailable
	case <-st.ioGate:
	}
	s.mu.Lock()
	if s.closed || st.deleted || st.generation == math.MaxUint64 || ((!invalidating && !control.passive) && (st.invalidated || generation != st.generation)) {
		s.mu.Unlock()
		st.ioGate <- struct{}{}
		s.finishOwnership(st)
		return ctx, nil, c.ErrStateUnavailable
	}
	opCtx, cancel := context.WithCancel(ctx)
	st.opCancel = cancel
	op := &apiOperation{state: st, generation: st.generation, control: invalidating || control.passive}
	s.mu.Unlock()
	stop := context.AfterFunc(s.ctx, cancel)
	opCtx = context.WithValue(opCtx, apiOperationKey{}, op)
	release := func() {
		stop()
		cancel()
		s.mu.Lock()
		st.opCancel = nil
		st.snapshot = st.record
		st.retained = st.pendingWrite != nil
		st.enrollmentRef = ""
		if st.enrollment != nil && st.enrollment.status.Kind == c.FlowPending {
			st.enrollmentRef = st.enrollment.ref
		}
		s.mu.Unlock()
		st.ioGate <- struct{}{}
		s.finishOwnership(st)
	}
	return opCtx, release, nil
}

// A cold control calls this only after checking its exact metadata identity.
func (s *SessionAPI) controlGeneration(ctx context.Context, st *apiState) error {
	op := ctx.Value(apiOperationKey{}).(*apiOperation)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || st.deleted || st.generation == math.MaxUint64 {
		return c.ErrStateUnavailable
	}
	if !st.invalidated {
		st.invalidated = true
		st.generation++
		op.generation = st.generation
	}
	return nil
}

func (s *SessionAPI) rearm(ctx context.Context, st *apiState) {
	if st.record.Withdrawing || st.recovering || (st.enrollment != nil && st.enrollment.status.Kind == c.FlowPending) {
		return
	}
	for _, parked := range st.parked {
		if parked.cleanupPending {
			return
		}
	}
	op := ctx.Value(apiOperationKey{}).(*apiOperation)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && !st.deleted && st.generation == op.generation && st.pendingWrite == nil {
		st.invalidated = false
	}
}

func (s *SessionAPI) workerGeneration(ctx context.Context, st *apiState) bool {
	op := ctx.Value(apiOperationKey{}).(*apiOperation)
	return !st.deleted && !st.invalidated && op.generation == st.generation
}

func (s *SessionAPI) finishOwnership(st *apiState) {
	s.mu.Lock()
	st.users--
	if st.users == 0 && (st.deleted || (!st.loaded && !st.retained)) && s.states[st.snapshot.Ref] == st {
		delete(s.states, st.snapshot.Ref)
	}
	s.mu.Unlock()
	s.workers.Done()
}

func receiptFinished(r *apiReceipt) bool {
	if r == nil {
		return true
	}
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}
