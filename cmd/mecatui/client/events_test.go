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
				{Role: "assistant", Text: "old answer", Reasoning: "thought", ProviderPhase: "commentary", ReasoningItemId: "rs_old",
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
	if ca.Replaced[1].Role != "assistant" || ca.Replaced[1].Text != "old answer" || ca.Replaced[1].Reasoning != "thought" || ca.Replaced[1].ProviderPhase != "commentary" || ca.Replaced[1].ReasoningItemID != "rs_old" {
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
// StreamSessionEvents and StreamSessionLive wrapper tests. It returns a scripted
// fakeEventStream (an EventRecver, but the real wrappers only need Recv, so it
// doubles as the ServerStreamingClient via the embedded ClientStream no-op
// below). The proto→plain mapping runs offline.
type fakeStreamSessionEventsClient struct {
	mecatlv1.HarnessServiceClient

	stream *fakeEventStream
	err    error

	lastReq     *mecatlv1.StreamSessionEventsRequest
	liveStream  *fakeEventStream
	liveErr     error
	lastLiveReq *mecatlv1.StreamSessionLiveRequest
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

func (f *fakeStreamSessionEventsClient) StreamSessionLive(_ context.Context, in *mecatlv1.StreamSessionLiveRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[mecatlv1.Event], error) {
	f.lastLiveReq = in
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	return fakeServerStreamingClient{f.liveStream}, nil
}

type liveCmdResult struct {
	ch   chan tea.Msg
	stop func()
}

type gatedLiveStreamer struct {
	entered chan struct{}
	release chan struct{}
}

func (s *gatedLiveStreamer) StreamSessionLive(_ context.Context, _ string) (*EventStream, error) {
	close(s.entered)
	<-s.release
	return NewEventStream(newFakeEventStream()), nil
}

func TestLiveStreamCmdOpensSynchronously(t *testing.T) {
	live := &gatedLiveStreamer{entered: make(chan struct{}), release: make(chan struct{})}
	returned := make(chan liveCmdResult, 1)

	go func() {
		ch, stop := LiveStreamCmd(context.Background(), live, "sess-live")
		returned <- liveCmdResult{ch: ch, stop: stop}
	}()

	<-live.entered
	select {
	case <-returned:
		t.Fatal("LiveStreamCmd returned before its opener was released")
	default:
	}

	close(live.release)
	select {
	case result := <-returned:
		result.stop()
		for range result.ch {
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LiveStreamCmd did not return after its opener was released")
	}
}

type contextEventStream struct {
	ctx     context.Context
	entered chan struct{}
}

func (s *contextEventStream) Recv() (*mecatlv1.Event, error) {
	close(s.entered)
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

type blockingLiveStreamer struct {
	entered chan struct{}
}

func (s *blockingLiveStreamer) StreamSessionLive(ctx context.Context, _ string) (*EventStream, error) {
	return NewEventStream(&contextEventStream{ctx: ctx, entered: s.entered}), nil
}

func TestLiveStreamCmdStopCancelsReadLoop(t *testing.T) {
	live := &blockingLiveStreamer{entered: make(chan struct{})}
	ch, stop := LiveStreamCmd(context.Background(), live, "sess-live")
	<-live.entered

	stop()
	stop()
	select {
	case _, ok := <-ch:
		for ok {
			_, ok = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after stop")
	}
}

func TestLiveStreamCmdOpenError(t *testing.T) {
	boom := errors.New("live RPC unavailable")
	live := &fakeLiveStreamer{err: boom}

	ch, stop := LiveStreamCmd(context.Background(), live, "sess-live-error")
	msgs := drain(ch)
	if len(msgs) != 1 {
		t.Fatalf("got %d msgs, want one StreamErrMsg: %#v", len(msgs), msgs)
	}
	se, ok := msgs[0].(StreamErrMsg)
	if !ok {
		t.Fatalf("msg = %T, want StreamErrMsg: %#v", msgs[0], msgs)
	}
	if !errors.Is(se.Err, boom) {
		t.Errorf("error = %v, want wrapping %v", se.Err, boom)
	}
	if se.Transient != TransientStreamErr(boom) {
		t.Errorf("transient = %t, want %t", se.Transient, TransientStreamErr(boom))
	}
	stop()
	stop()
}

func TestClientStreamSessionLive(t *testing.T) {
	fake := &fakeStreamSessionEventsClient{liveStream: newFakeEventStream()}
	es, err := newFakeClient(fake).StreamSessionLive(context.Background(), "sess-live")
	if err != nil {
		t.Fatal(err)
	}
	if es == nil || fake.lastLiveReq.GetSessionId() != "sess-live" {
		t.Fatalf("stream/request = %v/%v", es, fake.lastLiveReq)
	}

	boom := errors.New("live RPC unavailable")
	_, err = newFakeClient(&fakeStreamSessionEventsClient{liveErr: boom}).StreamSessionLive(context.Background(), "sess-live")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapping %v", err, boom)
	}
}

func TestClientStreamSessionEvents(t *testing.T) {
	fake := &fakeStreamSessionEventsClient{stream: newFakeEventStream()}
	es, err := newFakeClient(fake).StreamSessionEvents(context.Background(), "sess-replay")
	if err != nil {
		t.Fatal(err)
	}
	if es == nil || fake.lastReq.GetSessionId() != "sess-replay" {
		t.Fatalf("stream/request = %v/%v", es, fake.lastReq)
	}

	boom := errors.New("replay RPC unavailable")
	_, err = newFakeClient(&fakeStreamSessionEventsClient{err: boom}).StreamSessionEvents(context.Background(), "sess-replay")
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want wrapping %v", err, boom)
	}
}

type fakeLiveStreamer struct {
	stream *fakeEventStream
	err    error
	calls  int
	lastID string
}

func (f *fakeLiveStreamer) StreamSessionLive(_ context.Context, id string) (*EventStream, error) {
	f.calls++
	f.lastID = id
	if f.err != nil {
		return nil, f.err
	}
	return NewEventStream(f.stream), nil
}

// TestLiveStreamCmd asserts the interface live wrapper routes to the
// LiveStreamer, yields the scripted messages, closes its channel, and has an
// idempotent stop.
func TestLiveStreamCmd(t *testing.T) {
	live := &fakeLiveStreamer{
		stream: newFakeEventStream(eventsFromScript(scriptedRunResult())...),
	}

	ch, stop := LiveStreamCmd(context.Background(), live, "sess-live")
	defer stop()

	if cap(ch) != 64 {
		t.Errorf("channel capacity = %d, want 64", cap(ch))
	}
	msgs := drain(ch)
	if live.calls != 1 || live.lastID != "sess-live" {
		t.Errorf("StreamSessionLive calls/id = %d/%q, want 1/sess-live", live.calls, live.lastID)
	}
	if len(msgs) == 0 {
		t.Fatal("no msgs")
	}
	if _, ok := msgs[0].(SessionInitMsg); !ok {
		t.Errorf("first msg = %T, want SessionInitMsg", msgs[0])
	}
	if _, ok := msgs[len(msgs)-1].(StreamClosedMsg); !ok {
		t.Errorf("last msg = %T, want StreamClosedMsg", msgs[len(msgs)-1])
	}
	stop()
	stop()
}
