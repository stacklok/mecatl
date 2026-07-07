package client

import (
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeEventStream is a scripted, in-memory EventRecver used by the replay tests:
// Recv replays a fixed slice of *mecatlv1.Event (NO ConverseResponse envelope)
// then returns io.EOF (or a configured error). It satisfies EventRecver so the
// real EventStream/ReadLoop run with no gRPC and no network — the offline,
// deterministic replay substrate the brief mandates.
type fakeEventStream struct {
	mu sync.Mutex

	script []*mecatlv1.Event
	idx    int
	endErr error // returned after the script drains (nil ⇒ io.EOF)
}

func newFakeEventStream(script ...*mecatlv1.Event) *fakeEventStream {
	return &fakeEventStream{script: script}
}

func (f *fakeEventStream) Recv() (*mecatlv1.Event, error) {
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

// eventsFromScript builds the *mecatlv1.Event slice the scriptedRun scenario
// carries (the same events, minus the ConverseResponse envelope), so the
// projection-equivalence test can drive BOTH a fakeStream (live) and a
// fakeEventStream (replay) from the SAME event sequence.
func eventsFromScript(resps []*mecatlv1.ConverseResponse) []*mecatlv1.Event {
	out := make([]*mecatlv1.Event, 0, len(resps))
	for _, r := range resps {
		out = append(out, r.GetEvent())
	}
	return out
}

// logOnlyScript is the replay script for TestReplayReadLoopLogOnlyKinds: one of
// each of the three log-only kinds (approval/user_prompt/compaction.archive),
// carrying representative fields the test asserts against.
func logOnlyScript() []*mecatlv1.Event {
	return []*mecatlv1.Event{
		{Type: "user_prompt", UserPrompt: &mecatlv1.UserPrompt{
			Text: "please review the diff",
			Parts: []*mecatlv1.Content{
				{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{0x89, 0x50}},
			},
		}},
		{Type: "approval", Approval: &mecatlv1.Approval{
			AskId: "ask-write-1", Verdict: "allow_always", Tool: "Write", CallId: "call-write-1", AllowAlways: true,
		}},
		{Type: "compaction.archive", CompactionArchive: &mecatlv1.CompactionArchive{
			Replaced: []*mecatlv1.ConversationMessage{
				{Role: "user", Text: "old task"},
				{Role: "assistant", Text: "old answer", Reasoning: "thought", ProviderPhase: "commentary",
					ToolCalls:  []*mecatlv1.ToolCall{{Id: "call-old", Name: "Read", Args: `{"path":"x"}`}},
					ToolResult: &mecatlv1.ToolResult{CallId: "call-old", Content: "ok", IsError: false, StructuredContent: `{"k":1}`}},
			},
		}},
	}
}

// TestReplayReadLoopProjectionEquivalence asserts the replay (EventStream over a
// fakeEventStream) and the live Converse stream (Stream over a fakeStream) yield
// the IDENTICAL tea.Msg sequence for the same event script — the
// projection-equivalence contract of readEventLoop (the single translation path).
//
// This intentionally drives the SHARED kinds only — the ones BOTH the live relay
// and the replay feed emit. The live relay skips the three log-only kinds
// (approval/user_prompt/compaction.archive) by design (they are relay-persisted
// but not sent on the client wire; the replay feed DOES emit them, since a
// transcript viewer wants verdicts + user prompts). That asymmetry is exercised
// separately by TestReplayReadLoopLogOnlyKinds; do not add log-only events here
// or the live/replay counts diverge for a reason unrelated to projection.
func TestReplayReadLoopProjectionEquivalence(t *testing.T) {
	resps := scriptedRunResult()
	live := newFakeStream(resps...)
	replay := newFakeEventStream(eventsFromScript(resps)...)

	liveCh := make(chan tea.Msg, 64)
	replayCh := make(chan tea.Msg, 64)
	go NewStream(live, live).ReadLoop(context.Background(), liveCh)
	go NewEventStream(replay).ReadLoop(context.Background(), replayCh)

	liveMsgs := drain(liveCh)
	replayMsgs := drain(replayCh)

	if len(liveMsgs) != len(replayMsgs) {
		t.Fatalf("len mismatch: live=%d replay=%d\nlive=%#v\nreplay=%#v", len(liveMsgs), len(replayMsgs), liveMsgs, replayMsgs)
	}
	for i, lm := range liveMsgs {
		rm := replayMsgs[i]
		// The terminal StreamClosedMsg is a struct{} — identical. Every other
		// msg is built by the SAME EventToMsg path, so reflect.DeepEqual is the
		// honest equivalence check (msgs carry slices/structs not ==-comparable).
		if !reflect.DeepEqual(lm, rm) {
			t.Errorf("msg %d mismatch:\nlive  = %#v\nreplay= %#v", i, lm, rm)
		}
	}
}

// TestReplayReadLoopLogOnlyKinds asserts the three log-only kinds each map to
// the right msg with the right fields via the replay path.
func TestReplayReadLoopLogOnlyKinds(t *testing.T) {
	es := NewEventStream(newFakeEventStream(logOnlyScript()...))
	ch := make(chan tea.Msg, 64)
	go es.ReadLoop(context.Background(), ch)
	msgs := drain(ch)
	// msgs: UserPromptMsg, ApprovalMsg, CompactionArchiveMsg, StreamClosedMsg.
	if len(msgs) != 4 {
		t.Fatalf("got %d msgs, want 4: %#v", len(msgs), msgs)
	}

	up, ok := msgs[0].(UserPromptMsg)
	if !ok {
		t.Fatalf("msg 0 = %T, want UserPromptMsg", msgs[0])
	}
	if up.Text != "please review the diff" {
		t.Errorf("user prompt text = %q", up.Text)
	}
	if len(up.Parts) != 1 || up.Parts[0].Kind != ContentBlockImage || up.Parts[0].MimeType != "image/png" {
		t.Errorf("user prompt parts = %#v, want one image/png block", up.Parts)
	}
	if string(up.Parts[0].Data) != "\x89\x50" {
		t.Errorf("user prompt part data = %v", up.Parts[0].Data)
	}

	ap, ok := msgs[1].(ApprovalMsg)
	if !ok {
		t.Fatalf("msg 1 = %T, want ApprovalMsg", msgs[1])
	}
	if ap.AskID != "ask-write-1" || ap.Verdict != "allow_always" || ap.Tool != "Write" || ap.CallID != "call-write-1" || !ap.AllowAlways {
		t.Errorf("approval = %#v", ap)
	}

	ca, ok := msgs[2].(CompactionArchiveMsg)
	if !ok {
		t.Fatalf("msg 2 = %T, want CompactionArchiveMsg", msgs[2])
	}
	if len(ca.Replaced) != 2 {
		t.Fatalf("replaced = %d, want 2", len(ca.Replaced))
	}
	if ca.Replaced[0].Role != "user" || ca.Replaced[0].Text != "old task" {
		t.Errorf("replaced[0] = %#v", ca.Replaced[0])
	}
	if ca.Replaced[1].Role != "assistant" || ca.Replaced[1].Text != "old answer" || ca.Replaced[1].Reasoning != "thought" || ca.Replaced[1].ProviderPhase != "commentary" {
		t.Errorf("replaced[1] = %#v", ca.Replaced[1])
	}
	if len(ca.Replaced[1].ToolCalls) != 1 || ca.Replaced[1].ToolCalls[0].ID != "call-old" || ca.Replaced[1].ToolCalls[0].Name != "Read" {
		t.Errorf("replaced[1].ToolCalls = %#v", ca.Replaced[1].ToolCalls)
	}
	if ca.Replaced[1].ToolResult == nil || ca.Replaced[1].ToolResult.CallID != "call-old" || ca.Replaced[1].ToolResult.Content != "ok" || ca.Replaced[1].ToolResult.StructuredContent != `{"k":1}` {
		t.Errorf("replaced[1].ToolResult = %#v", ca.Replaced[1].ToolResult)
	}

	if _, ok := msgs[3].(StreamClosedMsg); !ok {
		t.Errorf("msg 3 = %T, want StreamClosedMsg", msgs[3])
	}
}

// TestReplayReadLoopEOF mirrors TestReadLoopEOF over EventStream: a clean EOF
// yields StreamClosedMsg after the script, then closes the channel.
func TestReplayReadLoopEOF(t *testing.T) {
	es := NewEventStream(newFakeEventStream(eventsFromScript(scriptedRunResult())...))
	ch := make(chan tea.Msg, 64)
	go es.ReadLoop(context.Background(), ch)

	msgs := drain(ch)
	if len(msgs) == 0 {
		t.Fatal("no msgs")
	}
	if _, ok := msgs[0].(SessionInitMsg); !ok {
		t.Errorf("first msg = %T, want SessionInitMsg", msgs[0])
	}
	last := msgs[len(msgs)-1]
	if _, ok := last.(StreamClosedMsg); !ok {
		t.Errorf("last msg = %T, want StreamClosedMsg", last)
	}
	res, ok := msgs[len(msgs)-2].(ResultMsg)
	if !ok || res.Stop != "end_turn" {
		t.Errorf("penultimate msg = %#v, want ResultMsg{end_turn}", msgs[len(msgs)-2])
	}
}

// TestReplayReadLoopError mirrors TestReadLoopError over EventStream: a non-EOF
// Recv error becomes StreamErrMsg.
func TestReplayReadLoopError(t *testing.T) {
	boom := errors.New("boom")
	fs := newFakeEventStream(&mecatlv1.Event{Type: "session.init"})
	fs.endErr = boom
	es := NewEventStream(fs)
	ch := make(chan tea.Msg, 8)
	go es.ReadLoop(context.Background(), ch)

	msgs := drain(ch)
	last := msgs[len(msgs)-1]
	se, ok := last.(StreamErrMsg)
	if !ok {
		t.Fatalf("last msg = %T, want StreamErrMsg", last)
	}
	if !errors.Is(se.Err, boom) {
		t.Errorf("err = %v, want boom", se.Err)
	}
}

// TestReplayReadLoopCancelUnblocks mirrors TestReadLoopCancelUnblocks over
// EventStream: the reader exits when its context is cancelled even though nobody
// is draining the (unbuffered) channel — the no-leak property via the ctx select.
func TestReplayReadLoopCancelUnblocks(t *testing.T) {
	fs := newFakeEventStream(&mecatlv1.Event{Type: "message.delta", Text: "hi"})
	es := NewEventStream(fs)
	ch := make(chan tea.Msg) // unbuffered, never drained

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		es.ReadLoop(ctx, ch)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// reader exited (and closed ch via defer) — good
	case <-time.After(2 * time.Second):
		t.Fatal("EventStream.ReadLoop did not exit after context cancel (goroutine leak)")
	}
}

// fakeStreamSessionEventsClient is a scripted HarnessServiceClient for the
// StreamSessionEvents wrapper test: it overrides only the one server-streaming
// RPC under test, returning a scripted fakeEventStream (an EventRecver, but the
// real wrapper only needs Recv, so it doubles as the ServerStreamingClient via
// the embedded ClientStream no-op below). The proto→plain mapping runs offline.
type fakeStreamSessionEventsClient struct {
	mecatlv1.HarnessServiceClient

	stream *fakeEventStream
	err    error

	lastReq *mecatlv1.StreamSessionEventsRequest
}

// fakeServerStreamingClient is a minimal grpc.ServerStreamingClient[Event] stand-in:
// it implements Recv (via the embedded fakeEventStream) and the four ClientStream
// methods the generated code might call on the open path. The wrapper only
// exercises Recv, so the others are no-ops.
type fakeServerStreamingClient struct {
	*fakeEventStream
}

func (fakeServerStreamingClient) Header() (metadata.MD, error) { return nil, nil }
func (fakeServerStreamingClient) Trailer() metadata.MD         { return nil }
func (fakeServerStreamingClient) CloseSend() error             { return nil }
func (fakeServerStreamingClient) Context() context.Context     { return context.Background() }
func (fakeServerStreamingClient) SendMsg(_ interface{}) error  { return nil }
func (fakeServerStreamingClient) RecvMsg(_ interface{}) error  { return io.EOF }

func (f *fakeStreamSessionEventsClient) StreamSessionEvents(_ context.Context, in *mecatlv1.StreamSessionEventsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mecatlv1.Event], error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return fakeServerStreamingClient{f.stream}, nil
}

// TestStreamSessionEventsCmd asserts the cmd opens the replay stream + runs
// ReadLoop, that the channel yields the scripted msgs then closes, and that
// stop() cancels (idempotent + no panic).
func TestStreamSessionEventsCmd(t *testing.T) {
	// fakeServerStreamingClient's RecvMsg returns io.EOF (unused by the wrapper);
	// its Recv (via embedded fakeEventStream) drives the loop.
	fake := &fakeStreamSessionEventsClient{
		stream: newFakeEventStream(eventsFromScript(scriptedRunResult())...),
	}
	cl := newFakeClient(fake)

	ch, stop := StreamSessionEventsCmd(context.Background(), cl, "sess-replay")
	defer stop()

	msgs := drain(ch)
	if len(msgs) == 0 {
		t.Fatal("no msgs")
	}
	if fake.lastReq.GetSessionId() != "sess-replay" {
		t.Errorf("request session_id = %q, want sess-replay", fake.lastReq.GetSessionId())
	}
	if _, ok := msgs[0].(SessionInitMsg); !ok {
		t.Errorf("first msg = %T, want SessionInitMsg", msgs[0])
	}
	if _, ok := msgs[len(msgs)-1].(StreamClosedMsg); !ok {
		t.Errorf("last msg = %T, want StreamClosedMsg", msgs[len(msgs)-1])
	}
	// stop is idempotent and never panics.
	stop()
	stop()
}

// blockingEventStream is a grpc.ServerStreamingClient[mecatlv1.Event] stand-in
// whose Recv BLOCKS until the context captured at open time is cancelled — it
// never returns on its own. It proves the cmd's own context.WithCancel
// propagates to an IN-FLIGHT Recv (the no-leak guarantee the Cmd owns that the
// EventStream.ReadLoop-level tests don't; a real gRPC Recv respects ctx the same
// way). The non-Recv ClientStream methods are no-ops like fakeServerStreamingClient.
type blockingEventStream struct {
	ctx context.Context // set by the fake client at StreamSessionEvents time
}

func (b *blockingEventStream) Recv() (*mecatlv1.Event, error) {
	<-b.ctx.Done()
	return nil, b.ctx.Err()
}
func (*blockingEventStream) Header() (metadata.MD, error) { return nil, nil }
func (*blockingEventStream) Trailer() metadata.MD         { return nil }
func (*blockingEventStream) CloseSend() error             { return nil }
func (*blockingEventStream) Context() context.Context     { return context.Background() }
func (*blockingEventStream) SendMsg(_ interface{}) error  { return nil }
func (*blockingEventStream) RecvMsg(_ interface{}) error  { return io.EOF }

// blockingStreamSessionEventsClient captures the ctx the cmd derived (via
// StreamSessionEvents) into a blockingEventStream so Recv blocks on the SAME ctx
// the stop() func cancels.
type blockingStreamSessionEventsClient struct {
	mecatlv1.HarnessServiceClient

	stream *blockingEventStream
}

func (f *blockingStreamSessionEventsClient) StreamSessionEvents(ctx context.Context, _ *mecatlv1.StreamSessionEventsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mecatlv1.Event], error) {
	f.stream = &blockingEventStream{ctx: ctx}
	return f.stream, nil
}

// TestStreamSessionEventsCmdStopCancelsInProgress proves the one no-leak
// guarantee the Cmd owns: stop() cancels an IN-FLIGHT ReadLoop blocked in Recv.
// A real gRPC Recv respects ctx; the blockingEventStream fakes that (it blocks
// until the captured ctx is cancelled, never returning on its own). The key
// assertion is the goroutine exits (the channel closes) within a bounded time
// after stop() — a cancel that did NOT propagate to a blocked Recv would hang.
// Mirrors TestReplayReadLoopCancelUnblocks's done/select shape.
func TestStreamSessionEventsCmdStopCancelsInProgress(t *testing.T) {
	fake := &blockingStreamSessionEventsClient{}
	cl := newFakeClient(fake)

	ch, stop := StreamSessionEventsCmd(context.Background(), cl, "sess-blocked")
	defer stop()

	// The reader goroutine: ReadLoop runs on its own goroutine, blocks in Recv,
	// and closes ch when it exits. We range ch to detect the close.
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	// Make sure the goroutine has actually entered the blocking Recv before we
	// cancel: stream is set during the open call, so once it's non-nil the
	// ReadLoop goroutine is in (or heading to) Recv.
	waitFor := func(cond func() bool) {
		deadline := time.Now().Add(2 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for the ReadLoop goroutine to enter the blocked Recv")
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitFor(func() bool { return fake.stream != nil })
	// One more tick so the goroutine is parked in <-ctx.Done() inside Recv, not
	// still between open and Recv.
	time.Sleep(50 * time.Millisecond)

	stop()

	select {
	case <-done:
		// The channel closed — ReadLoop exited because the blocked Recv returned a
		// ctx-cancelled error and readEventLoop terminated (emit may have lost the
		// race to ctx.Done and emitted nothing, or emitted a StreamErrMsg; either
		// way the channel closed = no goroutine leak). Good.
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not unblock the in-flight ReadLoop (goroutine leak: cancel did not propagate to the blocked Recv)")
	}
}

// midStreamErrorStream returns one event, then a non-EOF error on the next Recv.
type midStreamErrorStream struct {
	first  *mecatlv1.Event
	err    error
	called bool
}

func (m *midStreamErrorStream) Recv() (*mecatlv1.Event, error) {
	if !m.called {
		m.called = true
		return m.first, nil
	}
	return nil, m.err
}
func (*midStreamErrorStream) Header() (metadata.MD, error) { return nil, nil }
func (*midStreamErrorStream) Trailer() metadata.MD         { return nil }
func (*midStreamErrorStream) CloseSend() error             { return nil }
func (*midStreamErrorStream) Context() context.Context     { return context.Background() }
func (*midStreamErrorStream) SendMsg(_ interface{}) error  { return nil }
func (*midStreamErrorStream) RecvMsg(_ interface{}) error  { return io.EOF }

// midStreamErrorClient serves a midStreamErrorStream as the server stream so the
// mid-stream-error test can drive the wrapper with a one-event-then-error Recv.
type midStreamErrorClient struct {
	mecatlv1.HarnessServiceClient
	stream *midStreamErrorStream
}

func (m *midStreamErrorClient) StreamSessionEvents(_ context.Context, _ *mecatlv1.StreamSessionEventsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mecatlv1.Event], error) {
	return m.stream, nil
}

// TestStreamSessionEventsCmdMidStreamErrorClosesChannel (S1): a stream that
// returns one event then a non-EOF error emits a StreamErrMsg wrapping the
// error as its LAST msg AND closes the channel (drain returns). The error msg
// must be the terminal one — no trailing StreamClosedMsg (readEventLoop returns
// after the StreamErrMsg, so close(ch) fires but StreamClosedMsg does not).
func TestStreamSessionEventsCmdMidStreamErrorClosesChannel(t *testing.T) {
	boom := errors.New("rpc gone")
	ms := &midStreamErrorStream{
		first: &mecatlv1.Event{Type: "session.init", Seq: 1},
		err:   boom,
	}
	cl := newFakeClient(&midStreamErrorClient{stream: ms})

	ch, stop := StreamSessionEventsCmd(context.Background(), cl, "sess-mid")
	defer stop()

	msgs := drain(ch) // returns (channel closed) — proves readEventLoop terminated
	if len(msgs) < 2 {
		t.Fatalf("got %d msgs, want >=2 (event + error): %#v", len(msgs), msgs)
	}
	last := msgs[len(msgs)-1]
	se, ok := last.(StreamErrMsg)
	if !ok {
		t.Fatalf("last msg = %T, want StreamErrMsg: %#v", last, msgs)
	}
	if !errors.Is(se.Err, boom) {
		t.Errorf("err = %v, want boom", se.Err)
	}
	// The channel closing (drain returned) is itself the assertion that the loop
	// terminated; assert the first msg is the event we scripted.
	if _, ok := msgs[0].(SessionInitMsg); !ok {
		t.Errorf("first msg = %T, want SessionInitMsg", msgs[0])
	}
}

// fakeSessionReplayer is a scripted client.SessionReplayer for the
// ReplayStreamCmd test: it returns a *EventStream over a fakeEventStream (an
// EventRecver) or a configured open error, recording the id it was called with.
// It mirrors the ui package's fakeSessionReplayer but lives here so the client
// test stays self-contained.
type fakeSessionReplayer struct {
	stream *fakeEventStream
	err    error
	calls  int
	lastID string
}

func (f *fakeSessionReplayer) StreamSessionEvents(_ context.Context, id string) (*EventStream, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return nil, f.err
	}
	return NewEventStream(f.stream), nil
}

// TestReplayStreamCmd asserts the interface variant of StreamSessionEventsCmd:
// ReplayStreamCmd opens the replay stream via a SessionReplayer + runs ReadLoop,
// that the channel yields the scripted msgs then closes, and that stop() cancels
// (idempotent + no panic). Mirrors TestStreamSessionEventsCmd but over the
// SessionReplayer interface (the seam the ui's /sessions transcript viewer holds).
func TestReplayStreamCmd(t *testing.T) {
	fr := &fakeSessionReplayer{
		stream: newFakeEventStream(eventsFromScript(scriptedRunResult())...),
	}

	ch, stop := ReplayStreamCmd(context.Background(), fr, "sess-replay")
	defer stop()

	msgs := drain(ch)
	if len(msgs) == 0 {
		t.Fatal("no msgs")
	}
	if fr.calls != 1 {
		t.Errorf("StreamSessionEvents calls = %d, want 1", fr.calls)
	}
	if fr.lastID != "sess-replay" {
		t.Errorf("replayer called with id %q, want sess-replay", fr.lastID)
	}
	if _, ok := msgs[0].(SessionInitMsg); !ok {
		t.Errorf("first msg = %T, want SessionInitMsg", msgs[0])
	}
	if _, ok := msgs[len(msgs)-1].(StreamClosedMsg); !ok {
		t.Errorf("last msg = %T, want StreamClosedMsg", msgs[len(msgs)-1])
	}
	// stop is idempotent and never panics.
	stop()
	stop()
}

// TestReplayStreamCmdOpenError asserts an open-error emits a StreamErrMsg wrapping
// the error as its LAST msg AND closes the channel (so WaitForMsg terminates),
// mirroring the concrete cmd's open-error path.
func TestReplayStreamCmdOpenError(t *testing.T) {
	boom := errors.New("rpc gone")
	fr := &fakeSessionReplayer{err: boom}

	ch, stop := ReplayStreamCmd(context.Background(), fr, "sess-err")
	defer stop()

	msgs := drain(ch)
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want 1 (the open-error StreamErrMsg): %#v", len(msgs), msgs)
	}
	se, ok := msgs[0].(StreamErrMsg)
	if !ok {
		t.Fatalf("msg = %T, want StreamErrMsg: %#v", msgs[0], msgs)
	}
	if !errors.Is(se.Err, boom) {
		t.Errorf("err = %v, want boom", se.Err)
	}
}
