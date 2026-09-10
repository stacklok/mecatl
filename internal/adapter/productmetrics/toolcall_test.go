package productmetrics

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

func TestRecorderToolCallCountsWithoutIdentity(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCall(
		session.SessionID("sensitive-session-id"),
		session.ToolCall{Name: "read_secret_file"},
		session.ToolResult{Content: "super secret content", IsError: true},
		10*time.Millisecond, 20*time.Millisecond,
	)
	r.ToolCall(session.SessionID("other"), session.ToolCall{Name: "another_tool"}, session.ToolResult{}, 0, 0)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumValue(t, agg); got != 2 {
		t.Errorf("tool_calls = %d, want 2", got)
	}
}

func TestRecorderToolCallForRunCategorizesBuiltinsByName(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: false}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{IsError: true}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, attrCategory, "Bash"); got != 1 {
		t.Errorf("tool_calls{category=Bash} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, attrCategory, "Read"); got != 1 {
		t.Errorf("tool_calls{category=Read} = %d, want 1", got)
	}
}

// TestRecorderToolCallForRunBucketsUnrecognisedNamesUnderOther pins the
// closed-set discipline: a name that is NOT in mecatl's own fixed catalog is
// not assumed safe to emit verbatim (an agent def, a learned skill, or a
// future extension seam could derive one from operator or model input), so it
// lands on the single literal "other".
func TestRecorderToolCallForRunBucketsUnrecognisedNamesUnderOther(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "some-operator-named-tool"}, session.ToolResult{}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "another_unknown"}, session.ToolResult{}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, attrCategory, categoryOther); got != 2 {
		t.Errorf("tool_calls{category=other} = %d, want 2 (unrecognised names must not be emitted verbatim)", got)
	}
}

func TestRecorderToolCallForRunBucketsMCPToolsUnderOneCategory(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "mcp__github__list_issues"}, session.ToolResult{}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "mcp__slack__post_message"}, session.ToolResult{}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, attrCategory, categoryMCP); got != 2 {
		t.Errorf("tool_calls{category=mcp} = %d, want 2 (both MCP-server tools bucketed together)", got)
	}

	// The real server/tool names must never appear as an attribute value.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			sum, ok := md.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				iter := dp.Attributes.Iter()
				for iter.Next() {
					kv := iter.Attribute()
					for _, leak := range []string{"github", "list_issues", "slack", "post_message"} {
						if kv.Value.AsString() == leak {
							t.Fatalf("MCP server/tool name leaked as an attribute value: %s=%s", kv.Key, kv.Value.AsString())
						}
					}
				}
			}
		}
	}
}

func TestRecorderToolCallForRunRecordsOutcome(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: false}, 0, time.Millisecond)
	r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Bash"}, session.ToolResult{IsError: true}, 0, time.Millisecond)

	agg := collect(t, reader)["mecatl.product.tool_calls"]
	if got := sumPoint(t, agg, attrOutcome, outcomeSuccess); got != 1 {
		t.Errorf("tool_calls{outcome=success} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, attrOutcome, outcomeError); got != 1 {
		t.Errorf("tool_calls{outcome=error} = %d, want 1", got)
	}
}

// TestRecorderPerRunTrackerIsRaceFreeUnderConcurrentUse drives the two
// concurrent entry points into the shared per-run map — ToolCallForRun (per
// tool call, from the dispatcher's read-parallel batches) and Emit (per
// event) — against overlapping run ids, so `-race` exercises the one
// lock-discipline invariant this task introduces.
//
// The live runs are finished SEQUENTIALLY afterwards, deliberately: a
// concurrent EvResult racing its own run's tool calls has no defined
// ordering, so the bounded-map assertion below would be a coin flip rather
// than an invariant. Runs finished concurrently use disjoint ids.
func TestRecorderPerRunTrackerIsRaceFreeUnderConcurrentUse(t *testing.T) {
	r, _ := newTestRecorder(t)
	live := []string{"run-a", "run-b", "run-c"}
	finishing := []string{"run-d", "run-e", "run-f"}

	var wg sync.WaitGroup
	for _, runID := range live {
		for i := 0; i < 8; i++ {
			wg.Add(2)
			go func(runID string) {
				defer wg.Done()
				r.ToolCallForRun(runID, session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, 0)
			}(runID)
			go func(runID string) {
				defer wg.Done()
				r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart, RunID: runID})
			}(runID)
		}
	}
	for _, runID := range finishing {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			r.Emit(context.Background(), session.Event{
				Type: session.EvResult, RunID: runID,
				Result: &session.ResultPayload{Stop: session.StopEndTurn},
			})
		}(runID)
	}
	wg.Wait()

	for _, runID := range live {
		st := r.perRun.finish(runID)
		if st.toolCallCount != 8 {
			t.Errorf("%s toolCallCount = %d, want 8", runID, st.toolCallCount)
		}
		if !st.hadToolCall || !st.subagentSeen {
			t.Errorf("%s state = %+v, want hadToolCall and subagentSeen both true", runID, st)
		}
	}

	// Every run has now been finished, so no state may be left behind.
	r.perRun.mu.Lock()
	left := len(r.perRun.states)
	r.perRun.mu.Unlock()
	if left != 0 {
		t.Errorf("perRun.states holds %d entries after every run's EvResult, want 0 (the map must stay bounded to live runs)", left)
	}
}

// TestRecorderToolCallForRunTalliesPerRunCount pins the per-run tool-call
// count the later tool-calls-per-run task reads. It is tallied but not yet
// published as an instrument, so it is asserted on the state directly.
func TestRecorderToolCallForRunTalliesPerRunCount(t *testing.T) {
	r, _ := newTestRecorder(t)
	for i := 0; i < 3; i++ {
		r.ToolCallForRun("run-1", session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{IsError: i == 0}, 0, 0)
	}
	// A call with no run correlation must not land on any run.
	r.ToolCall(session.SessionID("s"), session.ToolCall{Name: "Read"}, session.ToolResult{}, 0, 0)

	st := r.perRun.finish("run-1")
	if st.toolCallCount != 3 {
		t.Errorf("toolCallCount = %d, want 3", st.toolCallCount)
	}
	if !st.hadToolCall {
		t.Error("hadToolCall = false, want true (two of the three calls succeeded)")
	}
	if got := r.perRun.finish(""); got != (perRunState{}) {
		t.Errorf("finish(\"\") = %+v, want the zero state", got)
	}
}
