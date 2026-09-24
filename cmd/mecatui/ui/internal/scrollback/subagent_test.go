package scrollback

import "testing"

func TestMecatuiTypedScrollbackModel_Scenario2_TransitionsAndSnapshotsOwnNestedData(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "sub", Name: "Subagent", Artifacts: []Artifact{{Kind: "image", Data: []byte("call")}}})
	decision := RoutingDecision{Confidence: Float64(0.8), MinimumConfidence: Float64(0.5)}
	if !c.Subagents().Start("sub", SubagentStart{Goal: "goal", Routing: decision}) {
		t.Fatal("start subagent")
	}
	update := SubagentUpdate{
		Trace:     []TraceEntry{{Kind: "tool", ToolName: "Read", Detail: "arg"}},
		Usage:     Usage{InputTokens: 2},
		Artifacts: []Artifact{{Kind: "resource_link", Data: []byte("result")}},
	}
	if !c.Subagents().Update("sub", update) {
		t.Fatal("update subagent")
	}
	update.Trace[0].Detail = "mutated input"
	update.Artifacts[0].Data[0] = 'X'
	decision.Confidence = Float64(0.1)

	card := c.SnapshotAt(0).Payload.(SubagentCardSnapshot)
	card.Start.Routing.Confidence = Float64(0.2)
	card.Update.Trace[0].Detail = "mutated snapshot"
	card.Update.Artifacts[0].Data[0] = 'Y'
	got := c.SnapshotAt(0).Payload.(SubagentCardSnapshot)
	if got.Start.Routing.Confidence == nil || *got.Start.Routing.Confidence != 0.8 {
		t.Fatalf("routing confidence = %v, want 0.8", got.Start.Routing.Confidence)
	}
	if got.Update.Trace[0].Detail != "arg" || string(got.Update.Artifacts[0].Data) != "result" {
		t.Fatalf("subagent snapshot leaked mutation: %#v", got.Update)
	}
}

func TestSubagentTraceRetainsTrailingEntriesInOrder(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "sub", Name: "Subagent"})
	if !c.Subagents().Start("sub", SubagentStart{}) {
		t.Fatal("start subagent")
	}
	trace := make([]TraceEntry, MaxTraceEntries+2)
	for i := range trace {
		trace[i].Text = string(rune('a' + i))
	}
	if !c.Subagents().Update("sub", SubagentUpdate{Trace: trace}) {
		t.Fatal("update subagent")
	}
	got := c.SnapshotAt(0).Payload.(SubagentCardSnapshot).Update.Trace
	if len(got) != MaxTraceEntries || got[0].Text != "c" || got[len(got)-1].Text != string(rune('a'+MaxTraceEntries+1)) {
		t.Fatalf("trace = %#v; want trailing %d entries in order", got, MaxTraceEntries)
	}
}
