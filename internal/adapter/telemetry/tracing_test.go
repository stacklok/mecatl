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

	"github.com/stacklok/mecatl/engine/port"
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

func attrString(s tracetest.SpanStub, key string) (string, bool) {
	for _, kv := range s.Attributes {
		if string(kv.Key) == key {
			return kv.Value.AsString(), true
		}
	}
	return "", false
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

// TestTracingLLMCallSpan_FullTurn covers the OTel GenAI semantic-convention
// attributes stamped on the additive mecatl.llm_call span: gen_ai.operation.name
// (stamped at open, EvTurnStart), and gen_ai.request.model/.response.model/
// .provider.name/.usage.*/.conversation.id (stamped at close, EvTurnEnd, from
// the enriched TurnEndPayload). It also pins SpanKind CLIENT and that the span
// is a child of mecatl.turn — additive, not a replacement of it.
func TestTracingLLMCallSpan_FullTurn(t *testing.T) {
	tr, exp := newTestTracing(t)

	ctx := port.WithSessionID(context.Background(), session.SessionID("sess-123"))
	tr.Emit(ctx, session.Event{Type: session.EvSessionInit})
	tr.Emit(ctx, session.Event{Type: session.EvTurnStart, Turn: 0})
	tr.Emit(ctx, session.Event{Type: session.EvTurnEnd, Turn: 0, TurnEnd: &session.TurnEndPayload{
		Model:    "claude-sonnet-5",
		Provider: "anthropic",
		Usage: session.Usage{
			InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20, ReasoningTokens: 10,
		},
	}})
	tr.Emit(ctx, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	spans := exp.GetSpans()
	llm, ok := spanByName(spans, "mecatl.llm_call")
	if !ok {
		t.Fatalf("no mecatl.llm_call span; got %d spans", len(spans))
	}
	turn, ok := spanByName(spans, "mecatl.turn")
	if !ok {
		t.Fatal("no mecatl.turn span")
	}
	if llm.Parent.SpanID() != turn.SpanContext.SpanID() {
		t.Errorf("llm_call parent = %v, want turn span %v", llm.Parent.SpanID(), turn.SpanContext.SpanID())
	}
	if llm.SpanKind != trace.SpanKindClient {
		t.Errorf("llm_call SpanKind = %v, want Client", llm.SpanKind)
	}
	if llm.Status.Code != codes.Unset {
		t.Errorf("llm_call status = %v, want Unset (EvTurnEnd carries no failure signal, so success must not be asserted as Ok)", llm.Status.Code)
	}
	wantString := map[string]string{
		"gen_ai.operation.name":  "chat",
		"gen_ai.request.model":   "claude-sonnet-5",
		"gen_ai.provider.name":   "anthropic",
		"gen_ai.conversation.id": "sess-123",
	}
	for key, want := range wantString {
		if got, ok := attrString(llm, key); !ok || got != want {
			t.Errorf("%s = %q (ok=%v), want %q", key, got, ok, want)
		}
	}
	if _, ok := attrString(llm, "gen_ai.response.model"); ok {
		t.Errorf("llm_call carries gen_ai.response.model — no per-turn model-fallback signal exists yet, it must not be fabricated")
	}
	wantInt := map[string]int64{
		"gen_ai.usage.input_tokens":            100,
		"gen_ai.usage.output_tokens":           50,
		"gen_ai.usage.cache_read.input_tokens": 20,
		"gen_ai.usage.reasoning.output_tokens": 10,
	}
	for key, want := range wantInt {
		if got, ok := attrInt(llm, key); !ok || got != want {
			t.Errorf("%s = %v (ok=%v), want %v", key, got, ok, want)
		}
	}
}

// TestTracingLLMCallSpan_DanglingClosedOnRunError covers the case where
// runTurn's stream errors before EvTurnEnd is ever emitted (the real event
// stream skips it entirely on that path): the llm_call span opened at
// EvTurnStart must not be left open forever — endRun's dangling-span cleanup
// (which already closes dangling tool spans) must also close it, with
// codes.Error.
func TestTracingLLMCallSpan_DanglingClosedOnRunError(t *testing.T) {
	tr, exp := newTestTracing(t)

	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	tr.Emit(context.Background(), session.Event{Type: session.EvTurnStart, Turn: 0})
	// No EvTurnEnd — mirrors the stream-error early-return path in loop.go.
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopError}})

	llm, ok := spanByName(exp.GetSpans(), "mecatl.llm_call")
	if !ok {
		t.Fatal("no mecatl.llm_call span — it must be closed defensively at run end, not left open")
	}
	if llm.Status.Code != codes.Error {
		t.Errorf("dangling llm_call status = %v, want Error", llm.Status.Code)
	}
}

// TestTracingRunSpan_ConversationID pins gen_ai.conversation.id on the run
// span itself, resolved from the ctx's bound session id (port.WithSessionID)
// — a GenAI-aware backend that filters purely on the run span, without
// walking down to mecatl.llm_call, still finds the conversation identity.
func TestTracingRunSpan_ConversationID(t *testing.T) {
	tr, exp := newTestTracing(t)

	ctx := port.WithSessionID(context.Background(), session.SessionID("sess-456"))
	tr.Emit(ctx, session.Event{Type: session.EvSessionInit})
	tr.Emit(ctx, session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	run, ok := spanByName(exp.GetSpans(), "mecatl.run")
	if !ok {
		t.Fatal("no mecatl.run span")
	}
	if got, ok := attrString(run, "gen_ai.conversation.id"); !ok || got != "sess-456" {
		t.Errorf("gen_ai.conversation.id = %q (ok=%v), want %q", got, ok, "sess-456")
	}
}

// TestTracingRunSpan_ErrorType pins that error.type mirrors mecatl.run.stop on
// a failing run outcome — the semconv-standard attribute alongside the
// project's own mecatl.run.stop classification, same pairing ai-gateway's
// stacklok.failure_class/error.type already establishes. Covers BOTH
// error-classified stop reasons (StopError, StopMaxConsecutiveFailures — the
// latter previously untested), and pins the negative case: a clean stop
// (StopEndTurn) must carry NO error.type at all, not an empty string.
func TestTracingRunSpan_ErrorType(t *testing.T) {
	tests := []struct {
		stop      session.StopReason
		wantError string
	}{
		{session.StopError, "error"},
		{session.StopMaxConsecutiveFailures, "max_consecutive_failures"},
	}
	for _, tt := range tests {
		t.Run(string(tt.stop), func(t *testing.T) {
			tr, exp := newTestTracing(t)
			tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
			tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: tt.stop}})

			run, ok := spanByName(exp.GetSpans(), "mecatl.run")
			if !ok {
				t.Fatal("no mecatl.run span")
			}
			if got, ok := attrString(run, "error.type"); !ok || got != tt.wantError {
				t.Errorf("error.type = %q (ok=%v), want %q", got, ok, tt.wantError)
			}
		})
	}
}

// TestTracingRunSpan_ErrorTypeAbsentOnCleanStop pins the negative case
// TestTracingRunSpan_ErrorType's table doesn't cover directly: a clean stop
// (StopEndTurn) must carry no error.type attribute at all.
func TestTracingRunSpan_ErrorTypeAbsentOnCleanStop(t *testing.T) {
	tr, exp := newTestTracing(t)
	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	run, ok := spanByName(exp.GetSpans(), "mecatl.run")
	if !ok {
		t.Fatal("no mecatl.run span")
	}
	if got, ok := attrString(run, "error.type"); ok {
		t.Errorf("error.type = %q present on a clean stop, want absent", got)
	}
}

// TestTracingLLMCallSpan_ClosesAtTurnEndNotRunEnd mechanically pins the
// EvTurnStart→EvTurnEnd interval contract: mecatl.llm_call must already be
// EXPORTED (ended) immediately after EvTurnEnd, before the trailing EvResult
// ever fires — proving the span closes at the event its own doc comment
// claims, not merely whenever the run happens to end later.
func TestTracingLLMCallSpan_ClosesAtTurnEndNotRunEnd(t *testing.T) {
	tr, exp := newTestTracing(t)

	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	tr.Emit(context.Background(), session.Event{Type: session.EvTurnStart, Turn: 0})
	if _, ok := spanByName(exp.GetSpans(), "mecatl.llm_call"); ok {
		t.Fatal("mecatl.llm_call already exported at EvTurnStart — it must not close before EvTurnEnd")
	}

	tr.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, Turn: 0, TurnEnd: &session.TurnEndPayload{
		Model: "claude-sonnet-5", Provider: "anthropic",
	}})
	if _, ok := spanByName(exp.GetSpans(), "mecatl.llm_call"); !ok {
		t.Fatal("mecatl.llm_call not yet exported right after EvTurnEnd — endLLMCall must close it synchronously at that event")
	}

	// The trailing EvResult must not change anything about the already-closed span.
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	if got := len(exp.GetSpans()); got != 3 { // run, turn, llm_call — no duplicate/second llm_call export
		t.Errorf("span count after EvResult = %d, want 3 (run, turn, llm_call)", got)
	}
}

// TestTracingLLMCallSpan_TwoTurns_FirstClosesBeforeSecondOpens drives two
// turns in one run and proves the first turn's mecatl.llm_call span is fully
// closed before the second turn's opens — i.e. they are two genuinely
// distinct, sequential spans, not one span silently reused or left open
// across a turn boundary.
func TestTracingLLMCallSpan_TwoTurns_FirstClosesBeforeSecondOpens(t *testing.T) {
	tr, exp := newTestTracing(t)

	tr.Emit(context.Background(), session.Event{Type: session.EvSessionInit})

	tr.Emit(context.Background(), session.Event{Type: session.EvTurnStart, Turn: 0})
	tr.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, Turn: 0, TurnEnd: &session.TurnEndPayload{
		Model: "model-a", Provider: "anthropic",
	}})
	afterFirst := exp.GetSpans()
	firstLLMCalls := 0
	for _, s := range afterFirst {
		if s.Name == "mecatl.llm_call" {
			firstLLMCalls++
		}
	}
	if firstLLMCalls != 1 {
		t.Fatalf("mecatl.llm_call spans after turn 1 = %d, want 1", firstLLMCalls)
	}
	firstSpanID := afterFirst[len(afterFirst)-1].SpanContext.SpanID()

	tr.Emit(context.Background(), session.Event{Type: session.EvTurnStart, Turn: 1})
	tr.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, Turn: 1, TurnEnd: &session.TurnEndPayload{
		Model: "model-b", Provider: "anthropic",
	}})
	tr.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	var llmCallSpanIDs []trace.SpanID
	for _, s := range exp.GetSpans() {
		if s.Name == "mecatl.llm_call" {
			llmCallSpanIDs = append(llmCallSpanIDs, s.SpanContext.SpanID())
		}
	}
	if len(llmCallSpanIDs) != 2 {
		t.Fatalf("total mecatl.llm_call spans = %d, want 2 (one per turn)", len(llmCallSpanIDs))
	}
	if llmCallSpanIDs[0] != firstSpanID {
		t.Errorf("first exported llm_call span id changed after turn 2 — turn 1's span must already be closed and immutable")
	}
	if llmCallSpanIDs[0] == llmCallSpanIDs[1] {
		t.Errorf("both turns produced the SAME span id — expected two distinct spans, one per turn")
	}
}
