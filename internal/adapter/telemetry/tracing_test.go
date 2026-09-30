package telemetry

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/mecatl/engine/session"
)

func newTestTracing(t *testing.T) (*Tracing, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	return NewTracing(tp), exp
}

func spanByName(spans tracetest.SpanStubs, name string) (tracetest.SpanStub, bool) {
	for _, s := range spans {
		if s.Name == name {
			return s, true
		}
	}
	return tracetest.SpanStub{}, false
}

func attrInt(s tracetest.SpanStub, key string) (int64, bool) {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsInt64(), true
		}
	}
	return 0, false
}

func TestADR_0370_Scenario1_AvailabilityLatency(t *testing.T) {
	tr, exp := newTestTracing(t)
	ctx := context.Background()
	call := session.NewToolCall("availability-call", "read", nil)
	available := session.NewToolResult("availability-call", "ready")
	canonical := session.NewToolError("availability-call", "cancelled after availability")

	tr.Emit(ctx, session.Event{Type: session.EvSessionInit})
	tr.Emit(ctx, session.Event{Type: session.EvToolCall, ToolCall: &call})

	if spans := exp.GetSpans(); len(spans) != 0 {
		t.Fatalf("tool span ended before safe result availability; got %d exported spans", len(spans))
	}

	tr.Emit(ctx, session.Event{Type: session.EvToolResultAvailable, ToolResult: &available})

	spans := exp.GetSpans()
	tool, ok := spanByName(spans, "mecatl.tool")
	if !ok {
		t.Fatalf("tool span did not end at safe result availability; got %d exported spans", len(spans))
	}
	if tool.Status.Code != codes.Unset {
		t.Errorf("availability span status = %v, want Unset", tool.Status.Code)
	}

	tr.Emit(ctx, session.Event{Type: session.EvToolResult, ToolResult: &canonical})

	spans = exp.GetSpans()
	if got := len(spans); got != 1 {
		t.Errorf("canonical confirmation exported %d tool spans, want exactly 1", got)
	}
	tool, ok = spanByName(spans, "mecatl.tool")
	if !ok {
		t.Fatal("tool span disappeared after canonical confirmation")
	}
	if tool.Status.Code != codes.Unset {
		t.Errorf("canonical confirmation changed availability span status to %v, want Unset", tool.Status.Code)
	}
}

func TestTracingRunSpan(t *testing.T) {
	tr, exp := newTestTracing(t)

	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20, CacheWriteTokens: 5},
	}})

	spans := exp.GetSpans()
	run, ok := spanByName(spans, "mecatl.run")
	if !ok {
		t.Fatalf("no mecatl.run span; got %d spans", len(spans))
	}
	if run.Status.Code != codes.Ok {
		t.Errorf("run status = %v, want Ok", run.Status.Code)
	}
	if v, ok := attrInt(run, "mecatl.tokens.input"); !ok || v != 100 {
		t.Errorf("mecatl.tokens.input = %v (ok=%v), want 100", v, ok)
	}
	if v, ok := attrInt(run, "mecatl.tokens.cache_read"); !ok || v != 20 {
		t.Errorf("mecatl.tokens.cache_read = %v (ok=%v), want 20", v, ok)
	}
	var hasStop bool
	for _, kv := range run.Attributes {
		if kv.Key == attribute.Key("mecatl.run.stop") && kv.Value.AsString() == "end_turn" {
			hasStop = true
		}
	}
	if !hasStop {
		t.Errorf("run span missing mecatl.run.stop=end_turn")
	}
}

func TestTracingToolChildSpan(t *testing.T) {
	tr, exp := newTestTracing(t)

	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	tr.Emit(context.Background(), session.Event{Type: session.EvTurnStart, Turn: 0})
	call := session.NewToolCall("c1", "bash", nil)
	tr.Emit(context.Background(), session.Event{Type: session.EvToolCall, ToolCall: &call})
	res := session.NewToolError("c1", "boom")
	tr.Emit(context.Background(), session.Event{Type: session.EvToolResult, ToolResult: &res})
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	spans := exp.GetSpans()
	tool, ok := spanByName(spans, "mecatl.tool")
	if !ok {
		t.Fatalf("no mecatl.tool span; got %d spans", len(spans))
	}
	run, ok := spanByName(spans, "mecatl.run")
	if !ok {
		t.Fatal("no mecatl.run span")
	}
	turn, ok := spanByName(spans, "mecatl.turn")
	if !ok {
		t.Fatal("no mecatl.turn span")
	}
	// Tool span's parent chain should root at the run span.
	if tool.Parent.SpanID() != turn.SpanContext.SpanID() {
		t.Errorf("tool parent = %v, want turn span %v", tool.Parent.SpanID(), turn.SpanContext.SpanID())
	}
	if turn.Parent.SpanID() != run.SpanContext.SpanID() {
		t.Errorf("turn parent = %v, want run span %v", turn.Parent.SpanID(), run.SpanContext.SpanID())
	}
	if tool.Status.Code != codes.Error {
		t.Errorf("errored tool span status = %v, want Error", tool.Status.Code)
	}
	var nameOK bool
	for _, kv := range tool.Attributes {
		if kv.Key == attribute.Key("mecatl.tool.name") && kv.Value.AsString() == "bash" {
			nameOK = true
		}
	}
	if !nameOK {
		t.Errorf("tool span missing mecatl.tool.name=bash")
	}
}

func TestTracingErrorStopReason(t *testing.T) {
	tr, exp := newTestTracing(t)

	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopError}})

	run, ok := spanByName(exp.GetSpans(), "mecatl.run")
	if !ok {
		t.Fatal("no mecatl.run span")
	}
	if run.Status.Code != codes.Error {
		t.Errorf("run status = %v, want Error", run.Status.Code)
	}
}

// TestTracingRunSpanParentsToCtxSpan asserts that when the Emit ctx carries a
// trace span, the run span is parented to it (so concurrent runs correlate to
// their originating request, the issue the ctx-aware seam unlocks).
func TestTracingRunSpanParentsToCtxSpan(t *testing.T) {
	tr, exp := newTestTracing(t)

	// Open a parent span on the SAME provider and put it in the ctx.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	ctx, parent := tp.Tracer("test").Start(context.Background(), "inbound.request")

	tr.Emit(ctx, session.Event{Type: session.EvSessionInit})
	tr.Emit(ctx, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	parent.End()

	run, ok := spanByName(exp.GetSpans(), "mecatl.run")
	if !ok {
		t.Fatal("no mecatl.run span")
	}
	if run.Parent.SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("run parent = %v, want ctx span %v", run.Parent.SpanID(), parent.SpanContext().SpanID())
	}
}

// TestTracingRunSpanRootsWhenCtxHasNoSpan asserts the fallback: a ctx with no
// span still yields a (root) run span, preserving the single-root behaviour.
func TestTracingRunSpanRootsWhenCtxHasNoSpan(t *testing.T) {
	tr, exp := newTestTracing(t)

	ctx := context.Background() // no span
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Fatal("precondition: background ctx must carry no valid span")
	}

	tr.Emit(ctx, session.Event{Type: session.EvSessionInit})
	tr.Emit(ctx, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	run, ok := spanByName(exp.GetSpans(), "mecatl.run")
	if !ok {
		t.Fatal("no mecatl.run span")
	}
	if run.Parent.IsValid() {
		t.Errorf("run span parent = %v, want no parent (root) when ctx has no span", run.Parent.SpanID())
	}
}

// TestTracingConcurrentEmitParentsToOwnCtxSpan drives two independent Tracing
// instances from two goroutines, each under its OWN inbound ctx span, and
// asserts each run span parents to its own ctx span. This exercises the Emit
// mutex + per-run ctx parenting under -race and backs the "concurrent runs
// correlate to their originating request" docstring claim. (Each run uses its
// own Tracing because a single Tracing instance multiplexes one run span at a
// time; the per-run ctx span is what distinguishes concurrent originating
// requests.)
func TestTracingConcurrentEmitParentsToOwnCtxSpan(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	// Two distinct inbound request spans.
	ctxA, parentA := tp.Tracer("test").Start(context.Background(), "inbound.A")
	ctxB, parentB := tp.Tracer("test").Start(context.Background(), "inbound.B")

	trA := NewTracing(tp)
	trB := NewTracing(tp)

	var wg sync.WaitGroup
	wg.Add(2)
	run := func(tr *Tracing, ctx context.Context) {
		defer wg.Done()
		tr.Emit(ctx, session.Event{Type: session.EvSessionInit})
		tr.Emit(ctx, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	}
	go run(trA, ctxA)
	go run(trB, ctxB)
	wg.Wait()
	parentA.End()
	parentB.End()

	spans := exp.GetSpans()
	// Collect run spans by parent span id.
	parentOfRun := map[trace.SpanID]bool{}
	var runCount int
	for _, s := range spans {
		if s.Name != "mecatl.run" {
			continue
		}
		runCount++
		parentOfRun[s.Parent.SpanID()] = true
	}
	if runCount != 2 {
		t.Fatalf("got %d mecatl.run spans, want 2", runCount)
	}
	if !parentOfRun[parentA.SpanContext().SpanID()] {
		t.Errorf("no run span parented to inbound.A (%v)", parentA.SpanContext().SpanID())
	}
	if !parentOfRun[parentB.SpanContext().SpanID()] {
		t.Errorf("no run span parented to inbound.B (%v)", parentB.SpanContext().SpanID())
	}
}
