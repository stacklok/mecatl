package productmetrics

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// DryRunRecorder implements the same two ports as Recorder (port.EventSink +
// port.ToolCallRecorder) but logs every would-be observation via an injected
// port.Diagnostics instead of exporting it over OTLP — the
// --product-metrics-dry-run audit path, so a skeptical operator can see
// exactly what this pipeline would have sent without trusting the docs. It
// logs ONLY the same bounded fields Recorder ever reads (event type, stop
// reason, token counts by kind, feature/provider/mode enum values) — never a
// tool name, session id, or free-text content, mirroring Recorder's own
// privacy discipline exactly.
type DryRunRecorder struct {
	diag port.Diagnostics
}

// Compile-time interface checks.
var (
	_ port.EventSink        = (*DryRunRecorder)(nil)
	_ port.ToolCallRecorder = (*DryRunRecorder)(nil)
)

// NewDryRunRecorder builds a DryRunRecorder over the given Diagnostics sink.
func NewDryRunRecorder(diag port.Diagnostics) *DryRunRecorder {
	return &DryRunRecorder{diag: diag}
}

// Emit logs the bounded event type (and, for EvResult, the stop reason and
// token counts by kind) — the exact same fields Recorder.Emit reads.
func (d *DryRunRecorder) Emit(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EvSessionInit:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record sessions_started+1")
	case session.EvResult:
		d.emitResult(ctx, ev.Result)
	case session.EvSubagentStart:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record subagent_used+1")
	case session.EvTeamStart:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record team_used+1")
	}
}

func (d *DryRunRecorder) emitResult(ctx context.Context, res *session.ResultPayload) {
	if res == nil {
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record runs_completed",
			"stop", string(session.StopNone))
		return
	}
	u := res.Usage
	d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record runs_completed + tokens",
		"stop", string(res.Stop),
		"input_tokens", u.InputTokens,
		"output_tokens", u.OutputTokens,
		"cache_read_tokens", u.CacheReadTokens,
		"cache_write_tokens", u.CacheWriteTokens,
		"reasoning_tokens", u.ReasoningTokens)
}

// ToolCall logs only that a call happened — no name, no session id, no
// content, no duration — matching Recorder.ToolCall's restraint exactly.
func (d *DryRunRecorder) ToolCall(_ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	d.diag.Log(context.Background(), port.LevelInfo, "product metrics (dry-run): would record tool_calls+1")
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
