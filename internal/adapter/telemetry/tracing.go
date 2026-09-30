package telemetry

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
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
//     tool.result.available, keyed by ToolCallID. A canonical tool.result ends
//     only spans from older or missed-availability streams.
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
	tools    map[session.ToolCallID]trace.Span
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
	case session.EvToolCall:
		t.ensureRun(ctx)
		t.startTool(ev.ToolCall)
	case session.EvToolResultAvailable, session.EvToolResult:
		t.endTool(ev.ToolResult)
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
func (t *Tracing) startRun(ctx context.Context) {
	if t.runSpan != nil {
		return
	}
	parent := context.Background()
	if trace.SpanContextFromContext(ctx).IsValid() {
		parent = ctx
	}
	t.runCtx, t.runSpan = t.tracer.Start(parent, "mecatl.run")
}

// startTurn opens a per-turn child span, ending any previous turn span first.
func (t *Tracing) startTurn(turn int) {
	if t.turnSpan != nil {
		t.turnSpan.End()
	}
	_, t.turnSpan = t.tracer.Start(t.runCtx, "mecatl.turn",
		trace.WithAttributes(attribute.Int("mecatl.turn", turn)))
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

// endRun ends the run span (and any open turn/tool spans), recording the stop
// reason as status and token usage as attributes.
func (t *Tracing) endRun(r *session.ResultPayload) {
	// Close any dangling tool spans first.
	for id, span := range t.tools {
		span.End()
		delete(t.tools, id)
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
		case session.StopNone, session.StopEndTurn, session.StopMaxTurns,
			session.StopMaxToolCalls, session.StopCancelled:
			t.runSpan.SetStatus(codes.Ok, "")
		}
	}
	t.runSpan.End()
	t.runSpan = nil
	t.runCtx = nil
}
