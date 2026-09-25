package llmresilience

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

// recoveryWindow has no context deadline: its joined timer is removed before
// semantic commit, leaving the visible continuation governed by caller/idle bounds.
type recoveryWindow struct {
	ctx      context.Context
	cancel   context.CancelFunc
	deadline time.Time
	timer    *time.Timer
	done     chan struct{}
	fired    atomic.Bool
}

func newRecoveryWindow(ctx context.Context) *recoveryWindow {
	ctx, cancel := context.WithCancel(ctx)
	return &recoveryWindow{ctx: ctx, cancel: cancel}
}
func (r *recoveryWindow) start(now time.Time, budget time.Duration) {
	if budget <= 0 || !r.deadline.IsZero() {
		return
	}
	r.deadline = now.Add(budget)
	r.done = make(chan struct{})
	r.timer = time.AfterFunc(budget, func() { defer close(r.done); r.fired.Store(true); r.cancel() })
}
func (r *recoveryWindow) expired(now time.Time) bool {
	return r.fired.Load() || !r.deadline.IsZero() && !now.Before(r.deadline)
}
func (r *recoveryWindow) stop(now time.Time) bool {
	if r.timer != nil {
		if !r.timer.Stop() {
			<-r.done
		}
		r.timer = nil
	}
	return r.expired(now)
}
func (r *recoveryWindow) remaining(now time.Time) time.Duration {
	if r.deadline.IsZero() {
		return 0
	}
	return max(0, r.deadline.Sub(now))
}

var unschedulableRetry = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)

// retryAt compares absolute times before Sub can saturate. The provider's
// sentinel must remain unschedulable even for a centuries-long budget.
func retryAt(err error, now time.Time, backoff time.Duration, r *recoveryWindow) (time.Time, string, bool) {
	at, source := now.Add(backoff), "backoff"
	var hint interface{ RetryNotBefore() (time.Time, bool) }
	if errors.As(err, &hint) {
		if providerAt, valid := hint.RetryNotBefore(); valid {
			if !providerAt.Before(unschedulableRetry) {
				return providerAt, "provider", false
			}
			if providerAt.After(at) {
				at, source = providerAt, "provider"
			}
		}
	}
	if r.deadline.IsZero() {
		return at, source, source != "provider"
	}
	return at, source, !at.After(r.deadline)
}

func (p *resilientProvider) logRecovery(ctx context.Context, model, decision string, calls int, wait time.Duration, r *recoveryWindow, source string) {
	if decision == "wait" && wait < time.Second {
		return
	}
	p.diag().Log(ctx, port.LevelInfo, "llm provider recovery",
		"model", model, "decision", decision, "attempt", calls, "max_attempts", p.cfg.MaxAttempts,
		"wait", wait, "remaining_budget", r.remaining(p.cfg.Clock()), "source", source)
}

// cancelablePull releases a suspended iterator even if the caller cancels before
// starting its returned continuation. Pull2's next and stop must never overlap.
func cancelablePull(ctx context.Context, seq iter.Seq2[port.Chunk, error]) (func() (port.Chunk, error, bool), func()) {
	next, stop := iter.Pull2(seq)
	var mu sync.Mutex
	stopLocked := func() { mu.Lock(); defer mu.Unlock(); stop() }
	done := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() { defer close(done); stopLocked() })
	cleanup := sync.OnceFunc(func() {
		if !stopCancel() {
			<-done
		} else {
			stopLocked()
		}
	})
	return func() (port.Chunk, error, bool) {
		mu.Lock()
		defer mu.Unlock()
		return next()
	}, cleanup
}

// Notifications wake all waiters, never transfer ownership; allow must run again.
func waitRecovery(ctx context.Context, delay time.Duration, changed <-chan struct{}) error {
	if delay <= 0 && changed == nil {
		return ctx.Err()
	}
	var timer *time.Timer
	var tick <-chan time.Time
	if delay > 0 {
		timer = time.NewTimer(delay)
		tick = timer.C
		defer timer.Stop()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	case <-tick:
		return nil
	}
}
