package client

import (
	"context"
	"io"
	"sync"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// eventstream_testing.go exports the scripted, in-memory EventRecver used by the
// replay tests so a DIFFERENT package's test (cmd/mecatui/ui) can build an
// offline replay substrate WITHOUT importing the proto package — keeping the ui
// package proto-free even in tests. It is a regular (non-_test.go) file because
// test files cannot be imported cross-package; it carries no production code
// (only the test fake the brief's cross-package helper rule requires) and stays
// in the client package alongside the EventRecver interface it satisfies.
//
// The exported FakeEventStream is the same scripted EventRecver the client's own
// events_test.go uses internally (unexported fakeEventStream): Recv replays a
// fixed slice of *mecatlv1.Event then returns io.EOF (or a configured error). It
// satisfies EventRecver so the real EventStream/ReadLoop run with no gRPC and no
// network — the offline, deterministic replay substrate. Callers that must stay
// proto-free pass no script (an empty stream yields a single StreamClosedMsg via
// the clean-EOF path); callers in this package may pass proto events directly.

// FakeEventStream is a scripted, in-memory EventRecver for replay tests. It is
// the exported twin of the unexported fakeEventStream in events_test.go; keep
// their Recv semantics byte-identical (the projection-equivalence test depends
// on the same drain-then-EOF/error contract).
type FakeEventStream struct {
	mu sync.Mutex

	script []*mecatlv1.Event
	idx    int
	endErr error // returned after the script drains (nil ⇒ io.EOF)
}

// NewFakeEventStream returns a scripted EventRecver over the given event slice.
// A zero-arg call yields an empty stream (Recv returns io.EOF immediately) so a
// proto-free caller can build an offline replay without naming the proto type.
func NewFakeEventStream(script ...*mecatlv1.Event) *FakeEventStream {
	return &FakeEventStream{script: script}
}

// WithEndErr configures the error returned once the script drains (nil ⇒ io.EOF).
func (f *FakeEventStream) WithEndErr(err error) *FakeEventStream {
	f.endErr = err
	return f
}

// Recv implements EventRecver: it yields the next scripted event, then io.EOF
// (or the configured endErr) once the script is exhausted.
func (f *FakeEventStream) Recv() (*mecatlv1.Event, error) {
	f.mu.Lock()
	if f.idx >= len(f.script) {
		err := f.endErr
		f.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	ev := f.script[f.idx]
	f.idx++
	f.mu.Unlock()
	return ev, nil
}

var _ EventRecver = (*FakeEventStream)(nil)

// ctxBlockedRecver models a stalled real transport that never delivers an
// event and never closes on its own — e.g. a dropped stream-close frame over
// a flaky tunnel/port-forward. Unlike FakeEventStream (whose Recv returns
// io.EOF immediately once its script drains and so cannot model a hang), Recv
// here blocks until ctx is done, mirroring how a real gRPC stream's Recv is
// bound to its call context.
type ctxBlockedRecver struct{ ctx context.Context }

func (r ctxBlockedRecver) Recv() (*mecatlv1.Event, error) {
	<-r.ctx.Done()
	return nil, r.ctx.Err()
}

var _ EventRecver = ctxBlockedRecver{}

// NewBlockedEventStream returns an EventStream whose Recv blocks until ctx is
// cancelled, for tests exercising a caller's OWN timeout/watchdog over a
// stalled stream (see ctxBlockedRecver).
func NewBlockedEventStream(ctx context.Context) *EventStream {
	return NewEventStream(ctxBlockedRecver{ctx: ctx})
}
