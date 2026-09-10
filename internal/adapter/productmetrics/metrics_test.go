package productmetrics

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

func newTestRecorder(t *testing.T) (*Recorder, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	r, err := NewRecorder(mp)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	return r, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			out[md.Name] = md.Data
		}
	}
	return out
}

func sumValue(t *testing.T, agg metricdata.Aggregation) int64 {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Sum[int64]", agg)
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	return total
}

func sumPoint(t *testing.T, agg metricdata.Aggregation, key, value string) int64 {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Sum[int64]", agg)
	}
	for _, dp := range sum.DataPoints {
		if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
			return dp.Value
		}
	}
	t.Fatalf("no data point with %s=%q", key, value)
	return 0
}

func TestRecorderEmitSessionsStarted(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})

	agg, ok := collect(t, reader)["mecatl.product.sessions_started"]
	if !ok {
		t.Fatal("mecatl.product.sessions_started missing")
	}
	if got := sumValue(t, agg); got != 2 {
		t.Errorf("sessions_started = %d, want 2", got)
	}
}

func TestRecorderEmitRunsCompletedByStopReason(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{
		Type:   session.EvResult,
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})
	r.Emit(context.Background(), session.Event{
		Type:   session.EvResult,
		Result: &session.ResultPayload{Stop: session.StopError},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, "stop", "end_turn"); got != 1 {
		t.Errorf("runs_completed{stop=end_turn} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, "stop", "error"); got != 1 {
		t.Errorf("runs_completed{stop=error} = %d, want 1", got)
	}
}

func TestRecorderEmitTokensByKind(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult,
		Result: &session.ResultPayload{
			Stop: session.StopEndTurn,
			Usage: session.Usage{
				InputTokens:      100,
				OutputTokens:     50,
				CacheReadTokens:  20,
				CacheWriteTokens: 5,
				ReasoningTokens:  10,
			},
		},
	})

	agg := collect(t, reader)["mecatl.product.tokens"]
	cases := map[string]int64{"input": 100, "output": 50, "cache_read": 20, "cache_write": 5, "reasoning": 10}
	for kind, want := range cases {
		if got := sumPoint(t, agg, "kind", kind); got != want {
			t.Errorf("tokens{kind=%s} = %d, want %d", kind, got, want)
		}
	}
}

func TestRecorderEmitSubagentAndTeamUsed(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart})
	r.Emit(context.Background(), session.Event{Type: session.EvTeamStart})

	collected := collect(t, reader)
	if got := sumValue(t, collected["mecatl.product.subagent_used"]); got != 1 {
		t.Errorf("subagent_used = %d, want 1", got)
	}
	if got := sumValue(t, collected["mecatl.product.team_used"]); got != 1 {
		t.Errorf("team_used = %d, want 1", got)
	}
}

// TestRecorderSubagentUsedCountsOncePerRun pins the documented "at least
// once per run" semantics: a run fanning out several concurrent Subagent
// calls (explicitly supported, e.g. the child concurrency gate) must count
// once, not once per EvSubagentStart.
func TestRecorderSubagentUsedCountsOncePerRun(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: "run-1"})
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: "run-1"})
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: "run-1"})

	if got := sumValue(t, collect(t, reader)["mecatl.product.subagent_used"]); got != 1 {
		t.Errorf("subagent_used = %d, want 1 (deduped within one run)", got)
	}
}

// TestRecorderSubagentUsedCountsEachDistinctRun proves the dedup is scoped
// to a run, not global: two separate runs each using Subagent count twice,
// and a run's dedup state is dropped on EvResult so a later run with the
// same RunID (unlikely in practice, but bounds correctness) still counts.
func TestRecorderSubagentUsedCountsEachDistinctRun(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: "run-1"})
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: "run-2"})

	if got := sumValue(t, collect(t, reader)["mecatl.product.subagent_used"]); got != 2 {
		t.Errorf("subagent_used = %d, want 2 (two distinct runs)", got)
	}

	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-1", Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: "run-1"})

	if got := sumValue(t, collect(t, reader)["mecatl.product.subagent_used"]); got != 3 {
		t.Errorf("subagent_used = %d, want 3 (run-1's dedup entry cleared on its EvResult)", got)
	}
}

func TestRecorderRunsCompletedHadToolCallTrueWhenASuccessfulToolCallOccurred(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{IsError: false}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-1",
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, attrHadToolCall, "true"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=true} = %d, want 1", got)
	}
}

func TestRecorderRunsCompletedHadToolCallFalseWithNoToolCall(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-2",
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, attrHadToolCall, "false"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=false} = %d, want 1", got)
	}
}

// TestRecorderRunsCompletedHadToolCallFalseWhenOnlyToolCallErrored pins the
// product definition: had_tool_call means the run took at least one
// SUCCESSFUL action, so a run whose only tool call errored is false.
func TestRecorderRunsCompletedHadToolCallFalseWhenOnlyToolCallErrored(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-3", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: true}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult, RunID: "run-3",
		Result: &session.ResultPayload{Stop: session.StopError},
	})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, attrHadToolCall, "false"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=false} = %d, want 1 (the only tool call errored)", got)
	}
}

// TestRecorderRunsCompletedHadToolCallOnNilResult pins that a result-less
// EvResult still carries the attribute (the stop-reason arm already defaults
// to StopNone) rather than emitting an attribute-shape that differs from
// every other data point on the instrument.
func TestRecorderRunsCompletedHadToolCallOnNilResult(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-4", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, 0)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-4"})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, attrHadToolCall, "true"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=true} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, attrStop, string(session.StopNone)); got != 1 {
		t.Errorf("runs_completed{stop=none} = %d, want 1", got)
	}
}

// TestRecorderPerRunStateIsIsolatedAcrossConcurrentRuns proves the per-run
// state is scoped to its run: two interleaved runs (a real possibility —
// Team/Parallel fan-out, or two concurrent client sessions on one process)
// must not leak their tool-call facts into each other.
func TestRecorderPerRunStateIsIsolatedAcrossConcurrentRuns(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-a", session.SessionID("s1"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, time.Millisecond)
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-b", Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	r.Emit(context.Background(), session.Event{Type: session.EvResult, RunID: "run-a", Result: &session.ResultPayload{Stop: session.StopEndTurn}})

	agg := collect(t, reader)["mecatl.product.runs_completed"]
	if got := sumPoint(t, agg, attrHadToolCall, "false"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=false} = %d, want 1 (run-a's tool call must not leak into run-b)", got)
	}
	if got := sumPoint(t, agg, attrHadToolCall, "true"); got != 1 {
		t.Errorf("runs_completed{had_tool_call=true} = %d, want 1 (run-a's own tool call)", got)
	}
}
