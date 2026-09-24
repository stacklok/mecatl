package port

import (
	"context"
	"sync/atomic"

	"github.com/stacklok/mecatl/engine/session"
)

type sessionIDContextKey struct{}
type runSerialContextKey struct{}
type turnIndexContextKey struct{}
type runAttemptCarrierContextKey struct{}
type attemptObserverContextKey struct{}

// AttemptObserver receives producer-controlled provider-attempt evidence. It is
// a run-local bridge: adapters observe, while the agent loop validates the whole
// payload and remains the sole event producer; the server relay remains the sole
// durable-log writer.
type AttemptObserver func(session.NetworkAttemptPayload)

// runAttemptContext belongs to one engine run. Its identity fields are fixed;
// only its turn carrier changes while the run is live.
type runAttemptContext struct {
	context.Context
	sessionID session.SessionID
	runSerial int64
	turnIndex atomic.Int64
	turnSet   atomic.Bool
}

func (c *runAttemptContext) Value(key any) any {
	switch key.(type) {
	case sessionIDContextKey:
		return c.sessionID
	case runSerialContextKey:
		return c.runSerial
	case turnIndexContextKey:
		if c.turnSet.Load() {
			return int(c.turnIndex.Load())
		}
		return nil
	case runAttemptCarrierContextKey:
		return c
	default:
		return c.Context.Value(key)
	}
}

func runAttemptFromContext(ctx context.Context) (*runAttemptContext, bool) {
	if ctx == nil {
		return nil, false
	}
	correlation, ok := ctx.Value(runAttemptCarrierContextKey{}).(*runAttemptContext)
	return correlation, ok
}

// WithAttemptObserver returns a child context carrying a run-local attempt
// observer. The observer grants no authority. Its input is producer-controlled;
// consumers must validate it before constructing an event or durable record.
func WithAttemptObserver(ctx context.Context, observer AttemptObserver) context.Context {
	return context.WithValue(ctx, attemptObserverContextKey{}, observer)
}

// ObserveAttempt sends producer-controlled attempt evidence to the observer on
// ctx, when one is installed. The loop-side observer is responsible for canonical
// validation before emission.
func ObserveAttempt(ctx context.Context, observation session.NetworkAttemptPayload) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(attemptObserverContextKey{}).(AttemptObserver); ok && observer != nil {
		observer(observation)
	}
}

// ObserveAttemptOnce returns a reporter bound to ctx that forwards at most one
// provider-terminal observation to ObserveAttempt: the first call wins and every
// later call is silently dropped. Provider adapters share this so their
// "report the stream's terminal outcome exactly once" contract lives in one
// place instead of being reimplemented per adapter.
func ObserveAttemptOnce(ctx context.Context) func(terminal bool, outcome string) {
	var observed bool
	return func(terminal bool, outcome string) {
		if observed {
			return
		}
		observed = true
		ObserveAttempt(ctx, session.NetworkAttemptPayload{ProviderTerminalObserved: &terminal, StreamOutcome: outcome})
	}
}

// WithSessionID returns a child context carrying the exact identity of the
// session that owns model calls made with that context. It grants no authority.
func WithSessionID(ctx context.Context, id session.SessionID) context.Context {
	return context.WithValue(ctx, sessionIDContextKey{}, id)
}

// SessionIDFromContext returns the session identity carried by ctx.
func SessionIDFromContext(ctx context.Context) (session.SessionID, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(sessionIDContextKey{}).(session.SessionID)
	return id, ok
}

// WithRunSerial returns a child context carrying the process-local serial of the
// run that owns a model call. The value is diagnostic correlation only: it is
// bounded to one int64, grants no authority, and is not durable across restarts.
func WithRunSerial(ctx context.Context, serial int64) context.Context {
	return context.WithValue(ctx, runSerialContextKey{}, serial)
}

// RunSerialFromContext returns the process-local run serial carried by ctx.
func RunSerialFromContext(ctx context.Context) (int64, bool) {
	if ctx == nil {
		return 0, false
	}
	serial, ok := ctx.Value(runSerialContextKey{}).(int64)
	return serial, ok
}

// WithTurnIndex returns a child context carrying the zero-based turn index of
// the model call within its session. It is diagnostic correlation only.
func WithTurnIndex(ctx context.Context, index int) context.Context {
	return context.WithValue(ctx, turnIndexContextKey{}, index)
}

// WithRunAttemptContext returns an independent, run-owned correlation context.
// Session and run identities are immutable for its lifetime. The engine may
// update its turn with SetAttemptTurnIndex until that run ends; the context must
// not be reused for another run. Atomic turn access permits concurrent reads by
// provider iterator and diagnostics goroutines.
func WithRunAttemptContext(ctx context.Context, id session.SessionID, serial int64) context.Context {
	return &runAttemptContext{Context: ctx, sessionID: id, runSerial: serial}
}

// SetAttemptTurnIndex updates the turn on the nearest carrier installed by
// WithRunAttemptContext and reports whether such a carrier exists. One run-loop
// goroutine writes at turn boundaries; concurrent reads are race-safe.
func SetAttemptTurnIndex(ctx context.Context, index int) bool {
	correlation, ok := runAttemptFromContext(ctx)
	if !ok {
		return false
	}
	correlation.turnIndex.Store(int64(index))
	correlation.turnSet.Store(true)
	return true
}

// TurnIndexFromContext returns the zero-based turn index carried by ctx.
func TurnIndexFromContext(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	index, ok := ctx.Value(turnIndexContextKey{}).(int)
	return index, ok
}
