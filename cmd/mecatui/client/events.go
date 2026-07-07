package client

import (
	"context"
	"fmt"
	"sync"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The durable-event-log replay surface (issue #245 Phase 2, cloud-native Phase
// 3a read-back): the read-only EventStream wrapper over a server-streaming
// StreamSessionEvents RPC, its ReadLoop entry, and the tea.Cmd constructor the
// ui's transcript viewer calls. As with the rest of this package, NO proto type
// leaks past this file — the ui drains tea.Msgs from the returned channel, the
// SAME fan-in (WaitForMsg) the live Converse stream uses.

// EventStream wraps one open StreamSessionEvents replay: a receive side ONLY
// (read-only, no Send side, unlike the bidi Converse Stream). The generated
// grpc.ServerStreamingClient[mecatlv1.Event] satisfies the EventRecver in
// production; tests supply a scripted fake.
type EventStream struct {
	recv EventRecver
}

// NewEventStream wraps an EventRecver in an EventStream. Pass the generated
// grpc.ServerStreamingClient[mecatlv1.Event] from StreamSessionEvents in
// production; pass a fake EventRecver in tests.
func NewEventStream(recv EventRecver) *EventStream {
	return &EventStream{recv: recv}
}

// ReadLoop runs the receive loop on its OWN goroutine over the replay stream:
// it delegates to readEventLoop (the shared translation path), so the replay
// and a live Converse run project identically for the same event sequence. It
// pushes translated tea.Msgs onto out, then closes out when the replay ends. A
// clean EOF yields StreamClosedMsg; any other error yields StreamErrMsg. Run it
// off the Bubble Tea update goroutine; pass a cancellable context so the ui can
// tear it down when it leaves the transcript view.
func (s *EventStream) ReadLoop(ctx context.Context, out chan<- tea.Msg) {
	readEventLoop(ctx, s.recv.Recv, out)
}

// StreamSessionEvents opens the durable-event-log replay (cloud-native Phase 3a
// read-back) for session id and wraps the returned server stream in an
// EventStream. The replay yields *mecatlv1.Event directly (NO ConverseResponse
// envelope), and INCLUDES the three log-only kinds (approval/user_prompt/
// compaction.archive) — a transcript viewer wants the verdicts and user prompts;
// metadata-only by construction (gauntlet #7). An unknown id yields an EMPTY
// stream (absence is data) → a single StreamClosedMsg; a server with no durable
// EventLog returns gRPC UNIMPLEMENTED → a StreamErrMsg.
func (c *Client) StreamSessionEvents(ctx context.Context, id string) (*EventStream, error) {
	stream, err := c.svc.StreamSessionEvents(ctx, &mecatlv1.StreamSessionEventsRequest{SessionId: id})
	if err != nil {
		return nil, fmt.Errorf("stream session events: %w", err)
	}
	return NewEventStream(stream), nil
}

// StreamSessionEventsCmd opens the replay stream for session id and runs ReadLoop
// on a goroutine, pushing tea.Msgs onto a channel the ui drains via WaitForMsg
// (the SAME fan-in the live Converse stream uses). It mirrors how the ui opens a
// live Converse stream (OpenConverse + ReadLoop + WaitForMsg) but read-only.
// Returns the channel + a teardown func (cancel) the ui calls when it leaves the
// transcript view. The channel is buffered (64). An open error emits a
// StreamErrMsg{Err, Transient: TransientStreamErr(err)} then closes the channel,
// so the ui's WaitForMsg fan-in always terminates. stop is idempotent
// (context.WithCancel + sync.Once).
func StreamSessionEventsCmd(ctx context.Context, c *Client, id string) (ch chan tea.Msg, stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	ch = make(chan tea.Msg, 64)
	var once sync.Once
	stop = func() { once.Do(cancel) }

	es, err := c.StreamSessionEvents(ctx, id)
	if err != nil {
		// open-error path: emit the failure then close, so WaitForMsg terminates.
		go func() {
			defer close(ch)
			emit(ctx, ch, StreamErrMsg{Err: err, Transient: TransientStreamErr(err)})
		}()
		return ch, stop
	}
	go es.ReadLoop(ctx, ch)
	return ch, stop
}

// ReplayStreamCmd is the interface variant of StreamSessionEventsCmd: it opens the
// replay stream for session id via a SessionReplayer (instead of a concrete
// *Client) and runs ReadLoop on a goroutine, pushing tea.Msgs onto a channel the
// ui drains via WaitForMsg (the SAME fan-in the live Converse stream uses). It is
// the open path the /sessions transcript viewer calls — the ui holds a
// SessionReplayer (the interface), not a *Client, so it cannot call the concrete
// StreamSessionEventsCmd. Mirrors StreamSessionEventsCmd exactly but calls
// r.StreamSessionEvents instead of c.svc.StreamSessionEvents. Returns the channel
// + a teardown func (cancel) the ui calls when it leaves the transcript view. The
// channel is buffered (64). An open error emits a StreamErrMsg{Err,
// Transient: TransientStreamErr(err)} then closes the channel, so the ui's
// WaitForMsg fan-in always terminates. stop is idempotent (context.WithCancel +
// sync.Once).
func ReplayStreamCmd(ctx context.Context, r SessionReplayer, id string) (ch chan tea.Msg, stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	ch = make(chan tea.Msg, 64)
	var once sync.Once
	stop = func() { once.Do(cancel) }

	es, err := r.StreamSessionEvents(ctx, id)
	if err != nil {
		// open-error path: emit the failure then close, so WaitForMsg terminates.
		go func() {
			defer close(ch)
			emit(ctx, ch, StreamErrMsg{Err: err, Transient: TransientStreamErr(err)})
		}()
		return ch, stop
	}
	go es.ReadLoop(ctx, ch)
	return ch, stop
}
