package telemetry

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

// attrsMatch reports whether a data point's attribute set carries every given
// key=value pair.
func attrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, present := set.Value(attribute.Key(k))
		if !present || got.AsString() != v {
			return false
		}
	}
	return true
}

// sumPointWith returns the int64 Sum value of the data point matching ALL the
// given attribute pairs, or -1 when no point matches.
func sumPointWith(t *testing.T, agg metricdata.Aggregation, want map[string]string) int64 {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Sum[int64]", agg)
	}
	for _, dp := range sum.DataPoints {
		if attrsMatch(dp.Attributes, want) {
			return dp.Value
		}
	}
	return -1
}

// TestMetricsRoleAttributeOnInstruments asserts every record path carries the
// role attribute (issue #47): the bare Metrics methods tag role="main", and a
// WithRole view tags its own bounded family value — on the event counter, the
// tool-call counter, the token counter, the per-role active_runs gauge, AND the
// latency histograms.
func TestMetricsRoleAttributeOnInstruments(t *testing.T) {
	m, reader := newTestMetrics(t)
	sub := m.WithRole(RoleSubagent)

	// Main path (role="main" by default).
	m.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	m.ToolCall("s", session.NewToolCall("c1", "bash", nil),
		session.NewToolResult("c1", "ok"), 0, 10*time.Millisecond)

	// Child path through the role-scoped view.
	sub.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	sub.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, TurnEnd: &session.TurnEndPayload{
		DurationMs: 100, TTFTMs: 20, InterTokenMeanMs: 10, InterTokenMaxMs: 15,
	}})
	sub.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 50, OutputTokens: 25},
	}})
	sub.ToolCall("s", session.NewToolCall("c2", "Glob", nil),
		session.NewToolResult("c2", "ok"), 0, 5*time.Millisecond)

	data := collect(t, reader)

	if got := sumPointWith(t, data["mecatl.events"], map[string]string{attrType: "session.init", attrRole: RoleMain}); got != 1 {
		t.Errorf("events{session.init,role=main} = %d, want 1", got)
	}
	if got := sumPointWith(t, data["mecatl.events"], map[string]string{attrType: "session.init", attrRole: RoleSubagent}); got != 1 {
		t.Errorf("events{session.init,role=subagent} = %d, want 1", got)
	}
	if got := sumPointWith(t, data["mecatl.tool.calls"], map[string]string{attrTool: "bash", attrRole: RoleMain}); got != 1 {
		t.Errorf("tool.calls{bash,role=main} = %d, want 1", got)
	}
	if got := sumPointWith(t, data["mecatl.tool.calls"], map[string]string{attrTool: "Glob", attrRole: RoleSubagent}); got != 1 {
		t.Errorf("tool.calls{Glob,role=subagent} = %d, want 1", got)
	}
	if got := sumPointWith(t, data["mecatl.tokens"], map[string]string{attrKind: "input", attrRole: RoleSubagent}); got != 50 {
		t.Errorf("tokens{input,role=subagent} = %d, want 50", got)
	}
	// active_runs is role-tagged: main still in flight (1), subagent init+result (0).
	if got := sumPointWith(t, data["mecatl.active_runs"], map[string]string{attrRole: RoleMain}); got != 1 {
		t.Errorf("active_runs{role=main} = %d, want 1", got)
	}
	if got := sumPointWith(t, data["mecatl.active_runs"], map[string]string{attrRole: RoleSubagent}); got != 0 {
		t.Errorf("active_runs{role=subagent} = %d, want 0", got)
	}

	// The latency histograms carry the role too (classic explicit-bucket).
	hist, ok := data[turnDurationInstrument].(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("turn.duration is %T, want Histogram[float64]", data[turnDurationInstrument])
	}
	var sawSubagent bool
	for _, dp := range hist.DataPoints {
		if attrsMatch(dp.Attributes, map[string]string{attrRole: RoleSubagent}) {
			sawSubagent = true
			if dp.Count != 1 {
				t.Errorf("turn.duration{role=subagent} count = %d, want 1", dp.Count)
			}
		}
	}
	if !sawSubagent {
		t.Errorf("turn.duration has no role=subagent series")
	}
}

// TestMetricsMainAndChildDoNotCrossTag proves a role-scoped view's recordings
// never land on the main series and vice versa: after interleaved main and
// child activity, each role's counters reflect ONLY its own recordings.
func TestMetricsMainAndChildDoNotCrossTag(t *testing.T) {
	m, reader := newTestMetrics(t)
	member := m.WithRole(RoleMember)

	// Main: one result with main-only usage; one tool call.
	m.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
		Stop:  session.StopEndTurn,
		Usage: session.Usage{InputTokens: 7, OutputTokens: 3},
	}})
	m.ToolCall("s", session.NewToolCall("c1", "Edit", nil),
		session.NewToolResult("c1", "ok"), 0, time.Millisecond)

	// Child: two results with child-only usage; two tool calls.
	for i := 0; i < 2; i++ {
		member.Emit(context.Background(), session.Event{Type: session.EvResult, Result: &session.ResultPayload{
			Stop:  session.StopEndTurn,
			Usage: session.Usage{InputTokens: 100, OutputTokens: 40},
		}})
		member.ToolCall("s", session.NewToolCall("c2", "Read", nil),
			session.NewToolResult("c2", "ok"), 0, time.Millisecond)
	}

	data := collect(t, reader)

	// Main counters carry ONLY the main activity.
	if got := sumPointWith(t, data["mecatl.tokens"], map[string]string{attrKind: "input", attrRole: RoleMain}); got != 7 {
		t.Errorf("tokens{input,role=main} = %d, want 7 (child tokens must not fold into main)", got)
	}
	if got := sumPointWith(t, data["mecatl.runs"], map[string]string{attrRole: RoleMain}); got != 1 {
		t.Errorf("runs{role=main} = %d, want 1", got)
	}
	if got := sumPointWith(t, data["mecatl.tool.calls"], map[string]string{attrRole: RoleMain}); got != 1 {
		t.Errorf("tool.calls{role=main} = %d, want 1", got)
	}

	// Child counters carry ONLY the child activity.
	if got := sumPointWith(t, data["mecatl.tokens"], map[string]string{attrKind: "input", attrRole: RoleMember}); got != 200 {
		t.Errorf("tokens{input,role=member} = %d, want 200", got)
	}
	if got := sumPointWith(t, data["mecatl.runs"], map[string]string{attrRole: RoleMember}); got != 2 {
		t.Errorf("runs{role=member} = %d, want 2", got)
	}
	if got := sumPointWith(t, data["mecatl.tool.calls"], map[string]string{attrRole: RoleMember}); got != 2 {
		t.Errorf("tool.calls{role=member} = %d, want 2", got)
	}
}

// TestSlowTurnBufferRoleViews asserts the slow-turn ring's role attribution
// mirrors the metrics adapter: bare Emit records role="main", a WithRole view
// records its own bounded family value, and both land in the SAME shared ring.
func TestSlowTurnBufferRoleViews(t *testing.T) {
	b := NewSlowTurnBuffer(8, func() time.Time { return time.Unix(42, 0) })
	child := b.WithRole(RoleSubagent)

	b.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, Turn: 1, TurnEnd: &session.TurnEndPayload{DurationMs: 100}})
	child.Emit(context.Background(), session.Event{Type: session.EvTurnEnd, Turn: 2, TurnEnd: &session.TurnEndPayload{DurationMs: 200}})

	got := b.Recent(0)
	if len(got) != 2 {
		t.Fatalf("Recent returned %d turns, want 2 (shared ring)", len(got))
	}
	// Newest first: the child turn, then the main turn.
	if got[0].Role != RoleSubagent || got[0].TurnIndex != 2 {
		t.Errorf("newest turn = {role:%q turn:%d}, want {role:%q turn:2}", got[0].Role, got[0].TurnIndex, RoleSubagent)
	}
	if got[1].Role != RoleMain || got[1].TurnIndex != 1 {
		t.Errorf("older turn = {role:%q turn:%d}, want {role:%q turn:1}", got[1].Role, got[1].TurnIndex, RoleMain)
	}
}
