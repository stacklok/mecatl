package agent

import (
	"context"
	"reflect"
	"strconv"
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

func TestToolResultPublisherConcurrentOrdering(t *testing.T) {
	for i := range 100 {
		sink := &recordingSink{}
		e := &Engine{deps: Deps{Sink: sink}}
		r := &Run{events: make(chan session.Event, 4)}
		early := session.NewToolResult("call", "early")
		canonical := session.NewToolError("call", "cancelled")
		ready := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-ready
			e.publishToolResult(r, session.Event{Type: session.EvToolResultAvailable, ToolResult: ptr(early)})
		}()
		go func() {
			defer wg.Done()
			<-ready
			e.publishToolResult(r, session.Event{Type: session.EvToolResult, ToolResult: ptr(canonical)})
		}()
		close(ready)
		wg.Wait()
		var events []session.Event
		for len(r.events) > 0 {
			events = append(events, <-r.events)
		}
		if len(events) != 2 || events[0].Type != session.EvToolResultAvailable || events[1].Type != session.EvToolResult ||
			events[0].Seq != 1 || events[1].Seq != 2 || !reflect.DeepEqual(events[1].ToolResult, &canonical) ||
			(!reflect.DeepEqual(events[0].ToolResult, &early) && !reflect.DeepEqual(events[0].ToolResult, &canonical)) ||
			!reflect.DeepEqual(events, sink.snapshotEvents()) {
			t.Fatalf("iteration %d: stream=%+v sink=%+v", i, events, sink.snapshotEvents())
		}
	}
}

func TestToolResultPublisherCancellationReplacement(t *testing.T) {
	sink := &recordingSink{}
	e := &Engine{deps: Deps{Sink: sink}}
	r := &Run{events: make(chan session.Event, 4)}
	early := session.NewToolResult("call", "early")
	cancelled := session.NewToolError("call", "cancelled")
	e.publishToolResult(r, session.Event{Type: session.EvToolResultAvailable, ToolResult: ptr(early)})
	e.publishToolResult(r, session.Event{Type: session.EvToolResultAvailable, ToolResult: ptr(early)})
	e.publishToolResult(r, session.Event{Type: session.EvToolResult, ToolResult: ptr(cancelled)})
	first, second := <-r.events, <-r.events
	if len(r.events) != 0 || first.Type != session.EvToolResultAvailable || second.Type != session.EvToolResult ||
		!reflect.DeepEqual(first.ToolResult, &early) || !reflect.DeepEqual(second.ToolResult, &cancelled) ||
		first.Seq >= second.Seq || !reflect.DeepEqual([]session.Event{first, second}, sink.snapshotEvents()) {
		t.Fatalf("available=%+v canonical=%+v sink=%+v", first, second, sink.snapshotEvents())
	}
}

func TestToolResultPublisherCanonicalBeforeLateAvailability(t *testing.T) {
	sink := &recordingSink{}
	e := &Engine{deps: Deps{Sink: sink}}
	r := &Run{events: make(chan session.Event, 4)}
	canonical := session.NewToolError("call", "cancelled")
	stale := session.NewToolResult("call", "early")
	e.publishToolResult(r, session.Event{Type: session.EvToolResult, ToolResult: ptr(canonical)})
	e.publishToolResult(r, session.Event{Type: session.EvToolResultAvailable, ToolResult: ptr(stale)})
	first, second := <-r.events, <-r.events
	if len(r.events) != 0 || first.Type != session.EvToolResultAvailable || second.Type != session.EvToolResult ||
		!reflect.DeepEqual(first.ToolResult, &canonical) || !reflect.DeepEqual(second.ToolResult, &canonical) ||
		!reflect.DeepEqual([]session.Event{first, second}, sink.snapshotEvents()) {
		t.Fatalf("late availability changed canonical publication: stream=%+v sink=%+v", []session.Event{first, second}, sink.snapshotEvents())
	}
}

func TestToolResultPublisherManyCalls(t *testing.T) {
	for _, count := range []int{1, 8, 9, 32} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			// Reusing IDs in a new run must not retain the old run's publication state.
			e := &Engine{}
			for range 2 {
				sink := &recordingSink{}
				e.deps.Sink = sink
				r := &Run{events: make(chan session.Event, 6*count)}
				var want []session.Event
				for i := range count {
					id := session.ToolCallID(strconv.Itoa(i))
					if i == 0 {
						id = "" // An empty ID must not match unused inline storage.
					}
					result := session.NewToolResult(id, "early")
					kind := session.EvToolResultAvailable
					if i%2 == 0 {
						result = session.NewToolError(id, "cancelled")
						kind = session.EvToolResult
					}
					e.publishToolResult(r, session.Event{Type: kind, Turn: i, ToolResult: ptr(result)})
					want = append(want, session.Event{Type: session.EvToolResultAvailable, Turn: i, ToolResult: ptr(result)})
					if kind == session.EvToolResult {
						want = append(want, session.Event{Type: kind, Turn: i, ToolResult: ptr(result)})
					}
				}
				// Replay every ID after the set has grown, including early IDs and
				// canonical-first calls. Neither may gain another availability event.
				for _, ev := range want {
					if ev.Type != session.EvToolResultAvailable {
						continue
					}
					e.publishToolResult(r, ev)
					if ev.Turn%2 != 0 {
						canonical := session.NewToolError(ev.ToolResult.CallID, "cancelled")
						result := session.Event{Type: session.EvToolResult, Turn: ev.Turn, ToolResult: ptr(canonical)}
						e.publishToolResult(r, result)
						want = append(want, result)
						e.publishToolResult(r, ev)
					}
				}
				var got []session.Event
				for len(r.events) > 0 {
					got = append(got, <-r.events)
				}
				for i := range want {
					want[i].Seq = int64(i + 1)
				}
				if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(got, sink.snapshotEvents()) {
					t.Fatalf("publication changed after tracking %d calls: stream=%+v want=%+v sink=%+v", count, got, want, sink.snapshotEvents())
				}
			}
		})
	}
}

func TestGenericEmitDoesNotPublishToolAvailability(t *testing.T) {
	e := &Engine{}
	r := &Run{events: make(chan session.Event, 2)}
	result := session.NewToolResult("call", "result")
	e.emit(r, session.Event{Type: session.EvToolResult, ToolResult: ptr(result)})
	if len(r.events) != 1 || (<-r.events).Type != session.EvToolResult {
		t.Fatal("generic event emission changed tool-result availability")
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
