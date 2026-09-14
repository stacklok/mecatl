package productmetrics

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// DryRunRecorder implements the same ports as Recorder (port.EventSink +
// port.ToolCallRecorder + port.RunAwareToolCallRecorder) but logs every
// would-be observation via an injected port.Diagnostics instead of exporting
// it over OTLP — the --product-metrics-dry-run audit path, so a skeptical
// operator can see exactly what this pipeline would have sent without
// trusting the docs. It logs ONLY the same bounded fields Recorder ever reads
// (event type, stop reason, had_tool_call, run_duration, tool_calls_per_run,
// the closed-set tool category and outcome, token counts by kind,
// feature/provider/mode enum values) — never a session id, a raw tool name,
// or free-text content, mirroring Recorder's own privacy discipline exactly.
//
// The audit path must stay in LOCKSTEP with Recorder: an attribute Recorder
// attaches but DryRunRecorder omits makes this surface understate what is
// sent, which is the one thing it exists to rule out.
//
// time_to_first_value is the ONE deliberate divergence, by design: Recorder's
// version is a once-ever, install-scoped, persisted-marker sample (armed via
// EnableFirstValueTracking, composition-only). DryRunRecorder has — and must
// have — NO persistent state (no XDG_STATE_HOME reads/writes; it stays a
// stateless, one-shot audit tool per the ADR), so it cannot reproduce "at
// most once, ever" across process restarts. Instead it logs a would-be
// time_to_first_value observation on EVERY run that qualifies (StopEndTurn +
// at least one successful tool call), not just the first one this process
// happens to see. That is MORE verbose than what Recorder would actually
// send, but it is the honest, simpler choice for a diagnostic surface whose
// job is "show what COULD be sent" — an operator sees every candidate moment
// rather than only whichever one a real install's persisted marker allowed.
type DryRunRecorder struct {
	diag   port.Diagnostics
	perRun *perRunTracker
}

// Compile-time interface checks.
var (
	_ port.EventSink                = (*DryRunRecorder)(nil)
	_ port.ToolCallRecorder         = (*DryRunRecorder)(nil)
	_ port.RunAwareToolCallRecorder = (*DryRunRecorder)(nil)
)

// NewDryRunRecorder builds a DryRunRecorder over the given Diagnostics sink.
func NewDryRunRecorder(diag port.Diagnostics) *DryRunRecorder {
	return &DryRunRecorder{diag: diag, perRun: newPerRunTracker()}
}

// Emit logs the bounded event type (and, for EvResult, the stop reason,
// had_tool_call, run_duration_seconds, tool_calls_per_run, token counts by
// kind, and a separate would-be time_to_first_value line when the run
// qualifies) — the exact same underlying facts Recorder.Emit reads.
func (d *DryRunRecorder) Emit(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EvSessionInit:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record sessions_started+1")
		d.perRun.markStarted(ev.RunID)
	case session.EvResult:
		st, tracked := d.perRun.finish(ev.RunID)
		d.emitResult(ctx, ev.Result, st, tracked)
	case session.EvSubagentStart:
		if d.perRun.markFamilyUsed(ev.RunID, familySubagent) {
			d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record subagent_used+1")
		}
	case session.EvTeamStart:
		if d.perRun.markFamilyUsed(ev.RunID, familyTeam) {
			d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record team_used+1")
		}
	}
}

func (d *DryRunRecorder) emitResult(ctx context.Context, res *session.ResultPayload, st perRunState, tracked bool) {
	stop := session.StopNone
	if res != nil {
		stop = res.Stop
	}

	fields := []any{
		"stop", string(stop),
		attrHadToolCall, st.hadToolCall,
	}
	// tool_calls_per_run mirrors Recorder.recordResult's tracked guard: an
	// untracked run (no RunID, or a RunID this recorder never saw an
	// EvSessionInit/tool call for) would otherwise report a fabricated 0.
	if tracked {
		fields = append(fields, "tool_calls_per_run", st.toolCallCount)
	}
	// run_duration is only meaningful when this recorder actually observed the
	// run's EvSessionInit (mirroring Recorder.recordResult's zero-startedAt
	// guard) — a run whose start this process missed reports no duration
	// rather than a nonsense one measured from time.Time{}.
	if !st.startedAt.IsZero() {
		fields = append(fields, "run_duration_seconds", time.Since(st.startedAt).Seconds())
	}

	msg := "product metrics (dry-run): would record runs_completed"
	if res != nil {
		u := res.Usage
		msg = "product metrics (dry-run): would record runs_completed + tokens"
		fields = append(fields,
			"input_tokens", u.InputTokens,
			"output_tokens", u.OutputTokens,
			"cache_read_tokens", u.CacheReadTokens,
			"cache_write_tokens", u.CacheWriteTokens,
			"reasoning_tokens", u.ReasoningTokens)
	}
	d.diag.Log(ctx, port.LevelInfo, msg, fields...)

	// See the DryRunRecorder doc comment: unlike Recorder's once-ever,
	// persisted-marker time_to_first_value, dry-run logs this on EVERY
	// qualifying run (no state to track "first" against) — the honest
	// simplification for a stateless audit surface.
	if stop == session.StopEndTurn && st.hadToolCall {
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record time_to_first_value")
	}
}

// ToolCall logs the run-less form, matching Recorder.ToolCall's delegation.
func (d *DryRunRecorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	d.ToolCallForRun("", id, call, result, queued, took)
}

// ToolCallForRun logs the two bounded tool-call attributes Recorder attaches —
// the closed-set category (never the raw name for anything outside mecatl's
// own catalog, never an MCP server/tool name) and the outcome — and tallies
// the same per-run state, matching Recorder.ToolCallForRun exactly.
func (d *DryRunRecorder) ToolCallForRun(runID string, _ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	outcome := outcomeSuccess
	if result.IsError {
		outcome = outcomeError
	}
	d.diag.Log(context.Background(), port.LevelInfo, "product metrics (dry-run): would record tool_calls+1",
		attrCategory, toolCategory(call.Name),
		attrOutcome, outcome)
	d.perRun.markToolCall(runID, result.IsError)
}

// Heartbeat logs the closed-enum feature/provider/mode signal, matching
// Recorder.Heartbeat's fields exactly.
func (d *DryRunRecorder) Heartbeat(snap FeatureSnapshot) {
	enabled := make([]string, 0, 4)
	for f, on := range snap.enabled() {
		if on {
			enabled = append(enabled, string(f))
		}
	}
	d.diag.Log(context.Background(), port.LevelInfo, "product metrics (dry-run): would record heartbeat",
		"features_enabled", enabled,
		"provider_family", string(snap.Provider),
		"deployment_mode", string(snap.Mode))
}
