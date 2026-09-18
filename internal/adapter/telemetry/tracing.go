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
// still cannot distinguish two concurrent runs whose ctx carries no span.
//
// KNOWN LIMITATION — one Tracing instance is shared across every concurrent
// session in mecated (cmd/mecated/main.go constructs exactly one and wires
// it as the shared EventSink), but runSpan/turnSpan/llmSpan are single
// mutable fields: the mutex prevents data RACES on them, not lifecycle
// OVERLAP. Two sessions racing EvSessionInit will have the second silently
// attach to the first session's still-open span tree (startRun's
// `if t.runSpan != nil { return }` guard), and a concurrent EvResult can
// then close or misattribute spans across sessions. This is a real
// production concern, not merely theoretical, given mecated serves
// concurrent sessions with no per-request ctx span anywhere upstream to
// disambiguate them (checked: no otelhttp/otelgrpc/per-request tracer.Start
// exists in cmd/mecated or internal/ outside this adapter). Every value
// this adapter stamps (gen_ai.conversation.id included — see startRun and
// endLLMCall) is resolved fresh from the CALLER's own ctx at the point of
// use rather than cached, so a misattributed span still carries accurate
// data for whichever session most recently touched it — but the span
// NESTING itself can still cross sessions. Fixing that needs per-run state
// keyed by session/run identity (a map keyed by session id, or a per-run
// Tracing-like sink handed out per session); tracked as a follow-up, not
// fixed here.
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
	tools   map[session.ToolCallID]trace.Span
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
		t.endLLMCall(ctx, ev.TurnEnd)
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
// The session id (port.SessionIDFromContext) is stamped as
// gen_ai.conversation.id on the run span here. It is deliberately NOT cached
// on the Tracing struct: this adapter is a single shared instance across
// every concurrent session in mecated (see the struct doc comment's overlap
// caveat), so caching one run's session id and reusing it for a later,
// interleaved run's llmSpan would misattribute one session's conversation
// identity onto another's telemetry (CWE-200) — a materially worse failure
// than the pre-existing span-nesting overlap alone. Resolving fresh from
// each call's own ctx (here, and again in endLLMCall) keeps every stamped
// value correct for whichever call is actually making it, even though the
// broader per-instance overlap (see the struct doc comment) is not fixed by
// this alone and needs its own follow-up (per-run state keyed by session/run
// identity).
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
		t.runSpan.SetAttributes(semconv.GenAIConversationIDKey.String(string(id)))
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

// endLLMCall stamps the GenAI request/usage attributes onto llmSpan from the
// turn's enriched TurnEndPayload and ends it. p is nil only if the caller
// passes a nil TurnEnd payload, which the event stream never does for a
// real EvTurnEnd — guarded defensively all the same.
//
// gen_ai.response.model is deliberately NOT stamped: there is no per-turn
// model-fallback signal yet (the model that answered can never be shown to
// differ from the one requested), and mirroring request.model under a
// distinctly-named attribute would fabricate information this code does not
// actually have — omit until a real signal exists rather than mislead a
// consumer into thinking a fallback was checked.
//
// The span status is deliberately left Unset on this path, not explicitly
// set to codes.Ok: EvTurnEnd carries no failure signal of its own (that
// only arrives later, on EvResult), so "the model call transported
// successfully" is the only fact available here, and asserting Ok would
// overstate it — this mirrors endTool's own pattern (status only ever set
// to Error on a known failure, left Unset otherwise). A stream error skips
// EvTurnEnd entirely (runTurn returns early before emitting it), leaving
// llmSpan open; endRun's dangling-span cleanup closes it with codes.Error
// in that case, mirroring the existing dangling-tool-span cleanup.
//
// The session id is resolved fresh from ctx (not cached — see the struct
// doc comment) so a misattributed llmSpan still carries the CALLER's own
// correct conversation id rather than a stale value cached from whichever
// run last called startRun.
func (t *Tracing) endLLMCall(ctx context.Context, p *session.TurnEndPayload) {
	if t.llmSpan == nil {
		return
	}
	if p != nil {
		t.llmSpan.SetAttributes(
			semconv.GenAIRequestModelKey.String(p.Model),
			semconv.GenAIProviderNameKey.String(p.Provider),
			semconv.GenAIUsageInputTokensKey.Int(p.Usage.InputTokens),
			semconv.GenAIUsageOutputTokensKey.Int(p.Usage.OutputTokens),
			semconv.GenAIUsageCacheReadInputTokensKey.Int(p.Usage.CacheReadTokens),
			semconv.GenAIUsageReasoningOutputTokensKey.Int(p.Usage.ReasoningTokens),
		)
		if id, ok := port.SessionIDFromContext(ctx); ok {
			t.llmSpan.SetAttributes(semconv.GenAIConversationIDKey.String(string(id)))
		}
	}
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
			// error.type deliberately DUPLICATES mecatl.run.stop's value here,
			// not an oversight: many GenAI-aware consumers key error views
			// specifically off the semconv-standard error.type attribute
			// rather than a project-local one, so both are stamped —
			// error.type for semconv alignment, mecatl.run.stop for the
			// project's own richer (non-error) stop-reason vocabulary.
			t.runSpan.SetAttributes(attribute.String("error.type", string(r.Stop)))
		case session.StopNone, session.StopEndTurn, session.StopMaxTurns,
			session.StopMaxToolCalls, session.StopCancelled:
			t.runSpan.SetStatus(codes.Ok, "")
		}
	}
	t.runSpan.End()
	t.runSpan = nil
	t.runCtx = nil
}
