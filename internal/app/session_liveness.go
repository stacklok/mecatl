package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	childLeaseDefaultTTL   = 30 * time.Second
	childLeaseCallTimeout  = 5 * time.Second
	childLeaseRenewDivisor = 2
)

// sessionLiveness is the Build-owned process-wide registry for engine-driven
// child sessions. When a SessionLease is configured it also owns each child's
// distributed hold and renewer for exactly the registered lifecycle.
type sessionLiveness struct {
	mu         sync.RWMutex
	active     map[session.SessionID]*childLeaseHold
	lease      port.SessionLease
	owner      string
	renew      time.Duration
	diag       port.Diagnostics
	capability *server.SessionMutationCapability
	now        func() time.Time
	closed     bool
}

type childLeaseHold struct {
	ready         chan struct{}
	err           error
	refs          uint64
	next          uint64
	cancels       map[uint64]context.CancelFunc
	lease         port.Lease
	acquireCancel context.CancelFunc
	stop          context.CancelFunc
	done          chan struct{}
	lost          bool
}

func newSessionLiveness(lease port.SessionLease, owner string, ttl, renew time.Duration, diag port.Diagnostics, capability *server.SessionMutationCapability) *sessionLiveness {
	if ttl <= 0 {
		ttl = childLeaseDefaultTTL
	}
	if renew <= 0 {
		renew = ttl / 3
	}
	if renew <= 0 {
		renew = ttl
	}
	if diag == nil {
		diag = port.NopDiagnostics{}
	}
	return &sessionLiveness{
		active: make(map[session.SessionID]*childLeaseHold),
		lease:  lease, owner: owner, renew: renew, diag: diag, capability: capability, now: time.Now,
	}
}

func (r *sessionLiveness) Register(ctx context.Context, id session.SessionID, cancel context.CancelFunc) (func(), error) {
	if cancel == nil {
		cancel = func() {}
	}
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, errors.New("app: child-session liveness is closed")
		}
		if h := r.active[id]; h != nil {
			ready := h.ready
			r.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			r.mu.Lock()
			if current := r.active[id]; current == h && h.err == nil {
				h.refs++
				h.next++
				key := h.next
				h.cancels[key] = cancel
				r.mu.Unlock()
				return sync.OnceFunc(func() { r.release(id, h, key) }), nil
			}
			err := h.err
			r.mu.Unlock()
			if err != nil {
				return nil, err
			}
			continue
		}
		h := &childLeaseHold{ready: make(chan struct{}), refs: 1, next: 1, cancels: map[uint64]context.CancelFunc{1: cancel}}
		var acqCtx context.Context
		if r.lease != nil {
			acqCtx, h.acquireCancel = context.WithTimeout(ctx, childLeaseCallTimeout)
		}
		r.active[id] = h
		r.mu.Unlock()

		if r.lease != nil {
			h.lease, h.err = r.lease.Acquire(acqCtx, id, r.owner)
			h.acquireCancel()
			if h.err != nil {
				h.err = fmt.Errorf("app: acquire child session lease %q: %w", id, h.err)
			}
		}

		r.mu.Lock()
		h.acquireCancel = nil
		if r.closed || r.active[id] != h {
			h.err = errors.New("app: child-session liveness is closed")
			close(h.ready)
			r.mu.Unlock()
			return nil, h.err
		}
		if h.err == nil {
			r.activate(id, h)
		}
		close(h.ready)
		if h.err != nil {
			delete(r.active, id)
		}
		r.mu.Unlock()
		if h.err != nil {
			return nil, h.err
		}
		return sync.OnceFunc(func() { r.release(id, h, 1) }), nil
	}
}

func (r *sessionLiveness) activate(id session.SessionID, h *childLeaseHold) {
	if r.capability != nil {
		r.capability.Grant(id)
	}
	if r.lease != nil {
		renewCtx, stop := context.WithCancel(context.Background())
		h.stop = stop
		h.done = make(chan struct{})
		go r.renewLoop(renewCtx, id, h)
	}
}

func (r *sessionLiveness) IsLive(id session.SessionID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h := r.active[id]
	return h != nil && h.err == nil && h.refs > 0
}

func (r *sessionLiveness) renewLoop(ctx context.Context, id session.SessionID, h *childLeaseHold) {
	defer close(h.done)
	ticker := time.NewTicker(r.renew)
	defer ticker.Stop()
	timeout := r.renew / childLeaseRenewDivisor
	if timeout <= 0 {
		timeout = r.renew
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.RLock()
			current := r.active[id]
			lease := h.lease
			r.mu.RUnlock()
			if current != h {
				return
			}
			renewCtx, renewCancel := context.WithTimeout(ctx, timeout)
			refreshed, err := r.lease.Renew(renewCtx, lease)
			renewCancel()
			if errors.Is(err, context.Canceled) {
				return
			}
			if err != nil && !errors.Is(err, port.ErrLeaseHeld) && r.now().Add(r.renew).Before(lease.Expiry) {
				continue
			}
			if err != nil {
				r.lose(id, h, err)
				return
			}
			r.mu.Lock()
			if r.active[id] == h {
				h.lease = refreshed
			}
			r.mu.Unlock()
		}
	}
}

func (r *sessionLiveness) lose(id session.SessionID, h *childLeaseHold, cause error) {
	r.diag.Log(context.Background(), port.LevelWarn, "lost child session lease; cancelling child",
		"session", string(id), "owner", r.owner, "err", cause.Error())
	r.mu.Lock()
	if r.active[id] != h || h.lost {
		r.mu.Unlock()
		return
	}
	h.lost = true
	if r.capability != nil {
		r.capability.Invalidate(id)
	}
	cancels := make([]context.CancelFunc, 0, len(h.cancels))
	for _, cancel := range h.cancels {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (r *sessionLiveness) release(id session.SessionID, h *childLeaseHold, key uint64) {
	r.mu.Lock()
	if r.active[id] != h {
		r.mu.Unlock()
		return
	}
	delete(h.cancels, key)
	if h.refs > 1 {
		h.refs--
		r.mu.Unlock()
		return
	}
	delete(r.active, id)
	stop, done, lease, lost := h.stop, h.done, h.lease, h.lost
	if r.capability != nil && !lost {
		r.capability.Remove(id)
	}
	r.mu.Unlock()
	r.stopAndRelease(stop, done, lease, lost)
}

func (r *sessionLiveness) stopAndRelease(stop context.CancelFunc, done <-chan struct{}, lease port.Lease, lost bool) {
	if stop != nil {
		stop()
		<-done
	}
	if lost || r.lease == nil || lease.SessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), childLeaseCallTimeout)
	err := r.lease.Release(ctx, lease)
	cancel()
	if err != nil {
		r.diag.Log(context.Background(), port.LevelWarn, "release child session lease failed",
			"session", string(lease.SessionID), "owner", r.owner, "err", err.Error())
	}
}

func (r *sessionLiveness) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	type closingHold struct {
		id session.SessionID
		h  *childLeaseHold
	}
	holds := make([]closingHold, 0, len(r.active))
	for id, h := range r.active {
		delete(r.active, id)
		if r.capability != nil {
			r.capability.Invalidate(id)
		}
		if h.acquireCancel != nil {
			h.acquireCancel()
		}
		holds = append(holds, closingHold{id: id, h: h})
	}
	r.mu.Unlock()
	for _, hold := range holds {
		h := hold.h
		<-h.ready
		for _, cancel := range h.cancels {
			cancel()
		}
		r.stopAndRelease(h.stop, h.done, h.lease, h.lost)
		if r.capability != nil && !h.lost {
			r.capability.Remove(hold.id)
		}
	}
}
