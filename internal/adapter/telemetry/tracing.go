package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// tracerName is the instrumentation scope name for spans this adapter creates.
const tracerName = "github.com/stacklok/mecatl/internal/adapter/telemetry"

// Tracing is an OpenTelemetry-backed telemetry adapter. It implements
// port.EventSink and turns the event stream into spans:
//
//   - a run span opened on the first event of a run (session.init, or the first
//     event seen if init is missing) and ended on the terminal result event,
//     with the stop reason mapped to span status and token usage recorded as
//     attributes;
//   - a child span per tool, opened on tool.call and ended on the matching
//     tool.result, keyed by ToolCallID.
//
// # Context
//
// EventSink.Emit carries the run's context.Context. When that ctx carries a
// trace span (the run goroutine was started under an inbound request span),
// Tracing parents the run span to it, so concurrent runs each link to their
// originating request. When the ctx carries no span, Tracing falls back to a
// single "current run" span per Tracing instance: a result event closes the
// open run span, and the next session.init opens a fresh one. Tool spans are
// correlated by ToolCallID, which is unique within a run. The span map is
// mutex-guarded so concurrent Emit calls are safe.
//
// Note: because session.Event has no session id, the single-instance fallback
// still cannot distinguish two concurrent runs whose ctx carries no span; that
// case relies on each run having its own ctx span (or its own Tracing/EventSink)
// for correct correlation.
type Tracing struct {
	tracer trace.Tracer

	mu       sync.Mutex
	runCtx   context.Context //nolint:containedctx // per-run span-carrier fallback when Emit's request ctx carries no span
	runSpan  trace.Span
	turnSpan trace.Span
	// llmSpan brackets exactly the inference operation within the current
	// turn — a child of turnSpan, additive to it (turnSpan's own boundary and
	// its tool-span nesting are unchanged; see startLLMCall's doc comment for
	// why this is a separate span rather than attributes on turnSpan itself).
	llmSpan trace.Span
	// sessionID is resolved once per run (port.SessionIDFromContext, at
	// startRun) and cached so every llmSpan in the run can stamp
	// gen_ai.conversation.id without re-resolving from context each turn.
	sessionID string
	tools     map[session.ToolCallID]trace.Span
}

// Compile-time interface check.
var _ port.EventSink = (*Tracing)(nil)

// NewTracing constructs a Tracing adapter that creates spans from tp. It
// implements port.EventSink.
func NewTracing(tp trace.TracerProvider) *Tracing {
	return &Tracing{
		tracer: tp.Tracer(tracerName),
		tools:  make(map[session.ToolCallID]trace.Span),
	}
}

// Emit maintains run, turn, and tool spans from the event stream. When ctx
// carries a trace span, a newly opened run span is parented to it.
func (t *Tracing) Emit(ctx context.Context, ev session.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()

	switch ev.Type {
	case session.EvSessionInit:
		t.startRun(ctx)
	case session.EvTurnStart:
		t.ensureRun(ctx)
		t.startTurn(ev.Turn)
		t.startLLMCall()
	case session.EvToolCall:
		t.ensureRun(ctx)
		t.startTool(ev.ToolCall)
	case session.EvToolResult:
		t.endTool(ev.ToolResult)
	case session.EvTurnEnd:
		t.endLLMCall(ev.TurnEnd)
	case session.EvResult:
		t.endRun(ev.Result)
	case session.EvMessageDelta, session.EvPermissionAsk, session.EvHook, session.EvCompaction, session.EvToolProgress:
		// EvToolProgress is a transient advisory line with no span of its own; it
		// only ensures the run span exists, like the other in-run lifecycle events.
		t.ensureRun(ctx)
	}
}

// ensureRun opens a run span lazily if the first event of a run was not a
// session.init (defensive: the run span must exist before any child span).
func (t *Tracing) ensureRun(ctx context.Context) {
	if t.runSpan == nil {
		t.startRun(ctx)
	}
}

// startRun opens the per-run root span. When ctx carries a span (a remote or
// in-process parent), the run span is parented to it so concurrent runs
// correlate to their originating request; otherwise it starts a fresh root.
// The session id (port.SessionIDFromContext) is resolved once here and
// cached on t.sessionID — reused for every llmSpan the run opens, and
// stamped as gen_ai.conversation.id on the run span itself.
func (t *Tracing) startRun(ctx context.Context) {
	if t.runSpan != nil {
		return
	}
	parent := context.Background()
	if trace.SpanContextFromContext(ctx).IsValid() {
		parent = ctx
	}
	t.runCtx, t.runSpan = t.tracer.Start(parent, "mecatl.run")
	if id, ok := port.SessionIDFromContext(ctx); ok {
		t.sessionID = string(id)
		t.runSpan.SetAttributes(semconv.GenAIConversationIDKey.String(t.sessionID))
	}
}

// startTurn opens a per-turn child span, ending any previous turn span first.
func (t *Tracing) startTurn(turn int) {
	if t.turnSpan != nil {
		t.turnSpan.End()
	}
	_, t.turnSpan = t.tracer.Start(t.runCtx, "mecatl.turn",
		trace.WithAttributes(attribute.Int("mecatl.turn", turn)))
}

// startLLMCall opens the GenAI inference span for the turn that just started.
// It is deliberately a NEW, additive child span rather than attributes on
// turnSpan itself: turnSpan stays open past the model call (it's only ended
// lazily on the next EvTurnStart or at run end, so tool spans — its
// children — nest inside it, and its own wall-clock duration covers turn +
// tool execution). Redefining turnSpan's own boundary to match "just the LLM
// call" would re-parent every tool span and change existing trace shape for
// any consumer already depending on it. llmSpan instead brackets exactly
// EvTurnStart→EvTurnEnd — build/compact/stream, no tool execution — which is
// the only interval that matches the OTel GenAI semconv definition of an
// inference operation span.
//
// Model/provider identity isn't known yet at EvTurnStart (session.TurnEndPayload
// carries it, not the start-of-turn signal), so only gen_ai.operation.name is
// stamped here; the rest lands in endLLMCall.
func (t *Tracing) startLLMCall() {
	if t.turnSpan == nil {
		return
	}
	_, t.llmSpan = t.tracer.Start(
		trace.ContextWithSpan(t.runCtx, t.turnSpan),
		"mecatl.llm_call",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(semconv.GenAIOperationNameKey.String(string(genaiconv.OperationNameChat))),
	)
}

// endLLMCall stamps the GenAI request/response/usage attributes onto llmSpan
// from the turn's enriched TurnEndPayload and ends it with codes.Ok. p is nil
// only if the caller passes a nil TurnEnd payload, which the event stream
// never does for a real EvTurnEnd — guarded defensively all the same.
//
// A stream error skips EvTurnEnd entirely (runTurn returns early before
// emitting it), leaving llmSpan open; endRun's dangling-span cleanup closes
// it with codes.Error in that case, mirroring the existing dangling-tool-span
// cleanup.
func (t *Tracing) endLLMCall(p *session.TurnEndPayload) {
	if t.llmSpan == nil {
		return
	}
	if p != nil {
		t.llmSpan.SetAttributes(
			semconv.GenAIRequestModelKey.String(p.Model),
			// No per-turn model-fallback signal exists yet — response.model
			// mirrors request.model until one does.
			semconv.GenAIResponseModelKey.String(p.Model),
			semconv.GenAIProviderNameKey.String(p.Provider),
			semconv.GenAIUsageInputTokensKey.Int(p.Usage.InputTokens),
			semconv.GenAIUsageOutputTokensKey.Int(p.Usage.OutputTokens),
			semconv.GenAIUsageCacheReadInputTokensKey.Int(p.Usage.CacheReadTokens),
			semconv.GenAIUsageReasoningOutputTokensKey.Int(p.Usage.ReasoningTokens),
		)
		if t.sessionID != "" {
			t.llmSpan.SetAttributes(semconv.GenAIConversationIDKey.String(t.sessionID))
		}
	}
	t.llmSpan.SetStatus(codes.Ok, "")
	t.llmSpan.End()
	t.llmSpan = nil
}

// parent returns the most specific open parent context for a child span.
func (t *Tracing) parent() context.Context {
	if t.turnSpan != nil {
		return trace.ContextWithSpan(t.runCtx, t.turnSpan)
	}
	return t.runCtx
}

// startTool opens a tool child span keyed by ToolCallID.
func (t *Tracing) startTool(call *session.ToolCall) {
	if call == nil {
		return
	}
	_, span := t.tracer.Start(t.parent(), "mecatl.tool",
		trace.WithAttributes(
			attribute.String("mecatl.tool.name", call.Name),
			attribute.String("mecatl.tool.call_id", string(call.ID)),
		))
	t.tools[call.ID] = span
}

// endTool ends the tool span matching the result's CallID.
func (t *Tracing) endTool(res *session.ToolResult) {
	if res == nil {
		return
	}
	span, ok := t.tools[res.CallID]
	if !ok {
		return
	}
	span.SetAttributes(attribute.Bool("mecatl.tool.error", res.IsError))
	if res.IsError {
		span.SetStatus(codes.Error, "tool returned error")
	}
	span.End()
	delete(t.tools, res.CallID)
}

// endRun ends the run span (and any open turn/tool/llm-call spans), recording
// the stop reason as status and token usage as attributes.
func (t *Tracing) endRun(r *session.ResultPayload) {
	// Close any dangling tool spans first.
	for id, span := range t.tools {
		span.End()
		delete(t.tools, id)
	}
	if t.llmSpan != nil {
		// A dangling llmSpan at run-end always means the turn's stream errored
		// or was cancelled before EvTurnEnd could fire (endLLMCall).
		t.llmSpan.SetStatus(codes.Error, "turn ended without a completed model response")
		t.llmSpan.End()
		t.llmSpan = nil
	}
	if t.turnSpan != nil {
		t.turnSpan.End()
		t.turnSpan = nil
	}
	if t.runSpan == nil {
		return
	}
	if r != nil {
		t.runSpan.SetAttributes(
			attribute.String("mecatl.run.stop", string(r.Stop)),
			attribute.Int("mecatl.tokens.input", r.Usage.InputTokens),
			attribute.Int("mecatl.tokens.output", r.Usage.OutputTokens),
			attribute.Int("mecatl.tokens.cache_read", r.Usage.CacheReadTokens),
			attribute.Int("mecatl.tokens.cache_write", r.Usage.CacheWriteTokens),
		)
		switch r.Stop {
		case session.StopError, session.StopMaxConsecutiveFailures:
			t.runSpan.SetStatus(codes.Error, string(r.Stop))
			t.runSpan.SetAttributes(attribute.String("error.type", string(r.Stop)))
		case session.StopNone, session.StopEndTurn, session.StopMaxTurns,
			session.StopMaxToolCalls, session.StopCancelled:
			t.runSpan.SetStatus(codes.Ok, "")
		}
	}
	t.runSpan.End()
	t.runSpan = nil
	t.runCtx = nil
	t.sessionID = ""
}
