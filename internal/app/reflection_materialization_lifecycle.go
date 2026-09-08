package app

import (
	"context"
	"errors"
	"sync"

	"github.com/stacklok/mecatl/internal/adapter/server"
)

var errReflectionMaterializationClosed = errors.New("reflection materialization is closed")

func explicitReflectionServiceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, errReflectionMaterializationClosed), errors.Is(err, errReflectionCoordinatorClosed):
		return server.ErrUnavailable
	default:
		return err
	}
}

// materializationLifecycle owns synchronous pre-admission scans. Callers do the
// scan themselves; the gate only supplies cancellation and bounded active-count
// joining, so admission never creates a goroutine per scan.
type materializationLifecycle struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	mu     sync.Mutex
	cond   *sync.Cond
	active int
	closed bool
}

type materializationOperation struct {
	gate   *materializationLifecycle
	ctx    context.Context
	cancel context.CancelCauseFunc
	stop   func() bool
	once   sync.Once
}

func newMaterializationLifecycle() *materializationLifecycle {
	ctx, cancel := context.WithCancelCause(context.Background())
	g := &materializationLifecycle{ctx: ctx, cancel: cancel}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *materializationLifecycle) enter(caller context.Context) (*materializationOperation, error) {
	if caller == nil {
		caller = context.Background()
	}
	if err := caller.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, errReflectionMaterializationClosed
	}
	ctx, cancel := context.WithCancelCause(caller)
	op := &materializationOperation{gate: g, ctx: ctx, cancel: cancel}
	op.stop = context.AfterFunc(g.ctx, func() { cancel(errReflectionMaterializationClosed) })
	g.active++
	return op, nil
}

func (o *materializationOperation) Context() context.Context { return o.ctx }
func (o *materializationOperation) Done() <-chan struct{}    { return o.ctx.Done() }

func (o *materializationOperation) Err() error {
	if cause := context.Cause(o.gate.ctx); cause != nil {
		return cause
	}
	return context.Cause(o.ctx)
}

func (o *materializationOperation) leave() {
	if o == nil || o.gate == nil {
		return
	}
	o.once.Do(func() {
		o.stop()
		o.cancel(context.Canceled)
		o.gate.mu.Lock()
		o.gate.active--
		if o.gate.active == 0 {
			o.gate.cond.Broadcast()
		}
		o.gate.mu.Unlock()
	})
}

func (g *materializationLifecycle) close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if !g.closed {
		g.closed = true
		g.cancel(errReflectionMaterializationClosed)
	}
	for g.active > 0 {
		g.cond.Wait()
	}
	g.mu.Unlock()
}
