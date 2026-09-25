package llmresilience

import (
	"context"
	"sync"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

type breakerOutcome uint8

const (
	breakerNeutral breakerOutcome = iota
	breakerSuccess
	breakerFailure
)

// A lease belongs to one actual call, including its returned iterator.
// Cancellation may release it without racing health accounting at completion.
type breakerLease struct {
	p           *resilientProvider
	generation  uint64
	probe       bool
	once        sync.Once
	releaseOnce sync.Once
	stopCancel  func() bool
	cancelDone  chan struct{}
}

func (l *breakerLease) finish(outcome breakerOutcome) {
	if l == nil {
		return
	}
	l.once.Do(func() { l.p.recordOutcome(l.generation, l.probe, outcome) })
}

func (l *breakerLease) release() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		if l.stopCancel != nil && !l.stopCancel() {
			<-l.cancelDone
		}
		l.finish(breakerNeutral)
	})
}

// allow returns either an exclusive lease or a snapshot of the current rejection
// and notification channel. Callers must recheck on every notification.
func (p *resilientProvider) allow(ctx context.Context, now time.Time) (*breakerLease, *BreakerError, <-chan struct{}) {
	if p.cfg.BreakerThreshold < 1 {
		return nil, nil, nil
	}
	b := &p.breaker
	b.mu.Lock()
	if b.changed == nil {
		b.changed = make(chan struct{})
	}
	if b.open {
		remaining := b.openedAt.Add(p.cfg.BreakerCooldown).Sub(now)
		if b.halfOpen || remaining > 0 {
			changed := b.changed
			b.mu.Unlock()
			return nil, &BreakerError{RetryAfter: max(remaining, 0)}, changed
		}
		b.halfOpen = true
		close(b.changed)
		b.changed = make(chan struct{})
	}
	lease := &breakerLease{p: p, generation: b.generation, probe: b.halfOpen}
	b.mu.Unlock()
	if lease.probe {
		lease.cancelDone = make(chan struct{})
		lease.stopCancel = context.AfterFunc(ctx, func() { defer close(lease.cancelDone); lease.finish(breakerNeutral) })
		p.diag().Log(ctx, port.LevelInfo, "llm circuit breaker half-open; admitting a trial")
	}
	return lease, nil, nil
}

func (p *resilientProvider) recordOutcome(generation uint64, probe bool, outcome breakerOutcome) {
	b := &p.breaker
	b.mu.Lock()
	if generation != b.generation {
		b.mu.Unlock()
		return
	}
	opened, closed := false, false
	switch outcome {
	case breakerSuccess:
		closed = b.open
		b.consecutiveFailures = 0
		b.open = false
	case breakerFailure:
		b.consecutiveFailures++
		if probe || b.consecutiveFailures >= p.cfg.BreakerThreshold {
			b.open = true
			b.openedAt = p.cfg.Clock()
			opened = true
		}
	case breakerNeutral:
	}
	if probe || opened || closed {
		b.halfOpen = false
		b.generation++
		if b.changed != nil {
			close(b.changed)
		}
		b.changed = make(chan struct{})
	}
	failures := b.consecutiveFailures
	b.mu.Unlock()
	if opened {
		p.diag().Log(context.Background(), port.LevelInfo, "llm circuit breaker opened", "consecutive_failures", failures, "cooldown", p.cfg.BreakerCooldown)
	}
	if closed {
		p.diag().Log(context.Background(), port.LevelInfo, "llm circuit breaker closed (recovered)")
	}
}
