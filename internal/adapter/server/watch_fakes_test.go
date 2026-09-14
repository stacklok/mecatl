package server_test

import (
	"context"
	"errors"
	"iter"
	"sync"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// errAppendRejected is the injected backend failure the gap tiers respond to. It
// stands for ADR 0250's LIKELY failure — one rejected or unencodable record —
// rather than a total outage, which by construction cannot record its own
// failure.
var errAppendRejected = errors.New("backend rejected the record")

// countingCursorLog wraps a real port.CursorEventLog to count writes and, on
// demand, fail AppendEvent while leaving AppendGap working.
//
// It delegates to a REAL backend rather than reimplementing one: the invariants
// under test are about the server's write path and the watch's delivery, so a
// hand-rolled log would let a wrong cursor or a wrong record order pass.
type countingCursorLog struct {
	inner port.CursorEventLog

	mu           sync.Mutex
	appendEvents int
	appendGaps   int
	legacyAppend int
	failEvents   bool
}

func newCountingCursorLog(inner port.CursorEventLog) *countingCursorLog {
	return &countingCursorLog{inner: inner}
}

func (l *countingCursorLog) failAppends(fail bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failEvents = fail
}

func (l *countingCursorLog) counts() (appendEvents, appendGaps, legacyAppend int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendEvents, l.appendGaps, l.legacyAppend
}

func (l *countingCursorLog) AppendEvent(ctx context.Context, id session.SessionID, ev session.Event) (port.Cursor, error) {
	l.mu.Lock()
	l.appendEvents++
	fail := l.failEvents
	l.mu.Unlock()
	if fail {
		return "", errAppendRejected
	}
	return l.inner.AppendEvent(ctx, id, ev)
}

func (l *countingCursorLog) AppendGap(ctx context.Context, id session.SessionID, reason string) (port.Cursor, error) {
	l.mu.Lock()
	l.appendGaps++
	l.mu.Unlock()
	return l.inner.AppendGap(ctx, id, reason)
}

// Append is the legacy port.EventLog write. Counting it is the point: with a
// cursor backend wired, the persistence chokepoint must use the cursor seam, so
// this counter staying at zero is what proves there is ONE write path rather
// than two that agree by luck.
func (l *countingCursorLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	l.mu.Lock()
	l.legacyAppend++
	l.mu.Unlock()
	return l.inner.Append(ctx, id, ev)
}

func (l *countingCursorLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return l.inner.Read(ctx, id)
}

func (l *countingCursorLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return l.inner.ReadAfter(ctx, id, after, opts)
}

var _ port.CursorEventLog = (*countingCursorLog)(nil)

// plainEventLog hides a cursor-capable backend behind the legacy port.EventLog
// only, so a watch against it must honestly report the feature as unsupported
// instead of degrading to a full replay.
type plainEventLog struct{ inner port.EventLog }

func (l plainEventLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	return l.inner.Append(ctx, id, ev)
}

func (l plainEventLog) Read(ctx context.Context, id session.SessionID) iter.Seq2[session.Event, error] {
	return l.inner.Read(ctx, id)
}

var _ port.EventLog = plainEventLog{}

// badCursorLog mints a cursor containing invalid UTF-8, standing in for a
// third-party port.CursorEventLog implementation. The four in-tree backends all
// mint ASCII, so nothing else in the tree can exercise the mapper's backstop on
// this field.
type badCursorLog struct{ port.CursorEventLog }

func (l badCursorLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	return func(yield func(port.LogRecord, error) bool) {
		for rec, err := range l.CursorEventLog.ReadAfter(ctx, id, after, opts) {
			if err == nil {
				rec.Cursor = port.Cursor("bad\xe2cursor")
			}
			if !yield(rec, err) {
				return
			}
		}
	}
}

// failingReadLog is a legacy port.EventLog whose Read yields a fault instead of
// events, so the older SSE replay route's mid-stream error frame is reachable.
type failingReadLog struct{ inner port.EventLog }

func (l failingReadLog) Append(ctx context.Context, id session.SessionID, ev session.Event) error {
	return l.inner.Append(ctx, id, ev)
}

func (failingReadLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		yield(session.Event{}, errors.New("backend read failed mid-stream"))
	}
}

var _ port.EventLog = failingReadLog{}

// followCapacityLog delegates replay and persistence to a real cursor log, but
// injects the Redis follower-admission sentinel at the exact Follow read seam.
// This keeps cursor/envelope behavior real while making backend saturation
// deterministic for transport tests.
type followCapacityLog struct{ port.CursorEventLog }

func (l followCapacityLog) ReadAfter(ctx context.Context, id session.SessionID, after port.Cursor, opts port.ReadOptions) iter.Seq2[port.LogRecord, error] {
	if !opts.Follow {
		return l.CursorEventLog.ReadAfter(ctx, id, after, opts)
	}
	return func(yield func(port.LogRecord, error) bool) {
		yield(port.LogRecord{}, port.ErrEventFollowCapacity)
	}
}
