package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// ctxMarkerKey is a private context key used to prove the run forwards its OWN
// ctx (carrying the marker) to the EventSink, rather than a fresh ctx.
type ctxMarkerKey struct{}

// recordingSink is a fake port.EventSink that captures the ctx of every Emit
// call so a test can assert what context the loop forwards.
type recordingSink struct {
	mu        sync.Mutex
	ctxs      []context.Context
	events    []session.Event
	sawMarker atomic.Bool
}

func (s *recordingSink) Emit(ctx context.Context, ev session.Event) {
	if v, _ := ctx.Value(ctxMarkerKey{}).(string); v == "from-request" {
		s.sawMarker.Store(true)
	}
	s.mu.Lock()
	s.ctxs = append(s.ctxs, ctx)
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *recordingSink) snapshotEvents() []session.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]session.Event(nil), s.events...)
}

func (s *recordingSink) snapshot() []context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]context.Context, len(s.ctxs))
	copy(out, s.ctxs)
	return out
}

// TestEngineEmitForwardsRunCtxToSink runs a normal mockllm turn under a ctx that
// carries a marker value and asserts the injected EventSink receives a ctx still
// carrying that marker — i.e. Engine.emit forwards the run's own ctx, not a
// fresh context.Background(). This is the only agent-loop-level coverage of the
// ctx-aware EventSink seam (Engine.emit forwarding r.ctx in dispatch.go); if that
// forwarding were dropped, no sink ctx would carry the marker and this fails.
func TestEngineEmitForwardsRunCtxToSink(t *testing.T) {
	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("done"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 1}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)
	sink := &recordingSink{}
	e := NewEngine(Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Sink:    sink,
		Model:   "test-model",
	})
	sess := session.New("s1", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))

	// Engine.Run wraps ctx in context.WithCancel but PRESERVES values, so the
	// marker must survive into the sink if the run forwards its own ctx.
	ctx := context.WithValue(context.Background(), ctxMarkerKey{}, "from-request")
	r := e.Run(ctx, sess, memEnv("/ws"), RunRequest{Text: "go"})
	for range r.Events() { //nolint:revive // drain to completion
	}

	got := sink.snapshot()
	if len(got) == 0 {
		t.Fatal("sink received no Emit calls; expected the run to mirror its events")
	}
	if !sink.sawMarker.Load() {
		t.Fatal("no sink ctx carried the request marker; Engine.emit dropped the run ctx on the floor")
	}
	// Every forwarded ctx must be non-nil AND carry the marker (the run forwards
	// its single run ctx for every event).
	for i, c := range got {
		if c == nil {
			t.Fatalf("sink ctx[%d] is nil", i)
		}
		if v, _ := c.Value(ctxMarkerKey{}).(string); v != "from-request" {
			t.Fatalf("sink ctx[%d] missing request marker (got %q)", i, v)
		}
	}
}

// TestEngineEmitNilCtxGuard covers the r.ctx == nil → context.Background() guard
// in Engine.emit directly: a Run whose ctx was never set must still emit through
// the sink with a non-nil ctx and without panicking. (No engine entry path leaves
// r.ctx nil today, but the guard exists defensively; this pins it.)
func TestEngineEmitNilCtxGuard(t *testing.T) {
	sink := &recordingSink{}
	e := NewEngine(Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Sink:    sink,
		Model:   "test-model",
	})
	// Construct a Run with a nil ctx and a buffered channel so emit does not block.
	r := &Run{
		events: make(chan session.Event, 4),
		asks:   newAskRegistry(),
		ctx:    nil,
	}

	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("emit panicked on nil run ctx: %v", p)
		}
	}()
	e.emit(r, session.Event{Type: session.EvSessionInit})

	got := sink.snapshot()
	if len(got) != 1 {
		t.Fatalf("sink received %d Emit calls, want 1", len(got))
	}
	if got[0] == nil {
		t.Fatal("emit forwarded a nil ctx to the sink despite the background-ctx guard")
	}
}
