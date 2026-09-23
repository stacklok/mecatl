package scrollback

import (
	"reflect"
	"testing"
)

func TestMecatuiTypedScrollbackModel_Scenario1_StableIdentityAndImmutableSnapshots(t *testing.T) {
	var c Conversation
	userID := c.AddUser(UserInput{Text: "prompt", Media: []string{"image/png"}})
	toolID := c.AddToolCall(ToolCall{ID: "call", Name: "Read", Arguments: `{"path":"a.go"}`, Artifacts: []Artifact{{Text: "before"}}})
	if userID == toolID || userID == 0 || toolID == 0 {
		t.Fatalf("IDs = %d, %d; want distinct non-zero document-local IDs", userID, toolID)
	}
	in := ToolResult{Body: "done", Artifacts: []Artifact{{Text: "after"}}}
	if !c.ResolveTool("call", in) {
		t.Fatal("resolve tool")
	}
	in.Artifacts[0].Text = "mutated input"
	snapshot := c.SnapshotAt(1)
	tool, ok := snapshot.Payload.(ToolCardSnapshot)
	if !ok {
		t.Fatalf("payload = %T, want ToolCardSnapshot", snapshot.Payload)
	}
	tool.Call.Arguments = "mutated snapshot"
	tool.Result.Artifacts[0].Text = "mutated snapshot"
	again := c.SnapshotAt(1).Payload.(ToolCardSnapshot)
	if got, want := again.Call.Arguments, `{"path":"a.go"}`; got != want {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
	if got, want := again.Result.Artifacts, []Artifact{{Text: "after"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("artifacts = %v, want %v", got, want)
	}
	appendix := c.RecordFileChange("a.go")
	if appendix == 0 || c.RecordFileChange("a.go") != appendix {
		t.Fatal("appendix did not retain its first-observed identity")
	}
}

func TestMecatuiTypedScrollbackModel_Scenario1_TransitionsAdvanceVisibleRevision(t *testing.T) {
	var c Conversation
	c.AddToolCall(ToolCall{ID: "call", Name: "Subagent"})
	initial := c.SnapshotAt(0).Revision
	if !c.StartSubagent("call", SubagentStart{Goal: "inspect"}) {
		t.Fatal("start subagent")
	}
	started := c.SnapshotAt(0).Revision
	if started != initial+1 {
		t.Fatalf("revision = %d, want %d", started, initial+1)
	}
	if !c.UpdateSubagent("call", SubagentUpdate{Current: "Read", Trace: []TraceEntry{{Text: "a"}}}) {
		t.Fatal("update subagent")
	}
	updated := c.SnapshotAt(0).Revision
	if updated != started+1 {
		t.Fatalf("revision = %d, want %d", updated, started+1)
	}
	if !c.UpdateSubagent("call", SubagentUpdate{Current: "Read", Trace: []TraceEntry{{Text: "a"}}}) {
		t.Fatal("idempotent update should match")
	}
	if got := c.SnapshotAt(0).Revision; got != updated {
		t.Fatalf("idempotent revision = %d, want %d", got, updated)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario1_InvalidTransitionsAreNoOps(t *testing.T) {
	var c Conversation
	c.AddToolCall(ToolCall{ID: "ordinary", Name: "Read"})
	before := c.SnapshotAt(0)
	if c.UpdateTeam("missing", TeamUpdate{}) || c.StartSubagent("missing", SubagentStart{}) || c.UpdateTeam("ordinary", TeamUpdate{}) {
		t.Fatal("invalid transitions matched")
	}
	after := c.SnapshotAt(0)
	if !reflect.DeepEqual(after, before) || c.Len() != 1 {
		t.Fatal("invalid transition mutated conversation")
	}
}

func TestMecatuiTypedScrollbackModel_Scenario1_ChangedFilesAppendixIdentityAndFallback(t *testing.T) {
	var c Conversation
	first := c.AddUser(UserInput{Text: "first"})
	appendixID := c.RecordFileChange("first.go")
	if appendixID == first {
		t.Fatalf("appendix ID = %d, must not reuse block ID %d", appendixID, first)
	}
	second := c.AddNotice("later")
	if got := c.RecordFileChange("first.go"); got != appendixID {
		t.Fatalf("duplicate change ID = %d, want %d", got, appendixID)
	}
	appendix, ok := c.AppendixSnapshot()
	if !ok || appendix.ID != appendixID || appendix.PrecedingBlockID != second {
		t.Fatalf("appendix = %#v, want ID %d and fallback %d", appendix, appendixID, second)
	}
	if got, want := appendix.Files, []string{"first.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario2_TransitionsAndSnapshotsOwnNestedData(t *testing.T) {
	var c Conversation
	c.AddToolCall(ToolCall{ID: "sub", Artifacts: []Artifact{{Kind: "image", Data: []byte("call")}}})
	decision := RoutingDecision{Confidence: Float64(0.8), MinimumConfidence: Float64(0.5)}
	if !c.StartSubagent("sub", SubagentStart{Goal: "goal", Routing: decision}) {
		t.Fatal("start subagent")
	}
	update := SubagentUpdate{
		Trace:     []TraceEntry{{Kind: "tool", ToolName: "Read", Detail: "arg"}},
		Usage:     Usage{InputTokens: 2},
		Artifacts: []Artifact{{Kind: "resource_link", Data: []byte("result")}},
	}
	if !c.UpdateSubagent("sub", update) {
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

	c.AddToolCall(ToolCall{ID: "team"})
	if !c.StartTeam("team", TeamStart{TeamID: "team-1"}) {
		t.Fatal("start team")
	}
	team := TeamUpdate{Lanes: []TeamLane{{Name: "worker", Trace: []TraceEntry{{Text: "trace"}}}}, Tasks: []Task{{ID: "task", Dependencies: []string{"dep"}}}, Findings: []Finding{{Member: "worker", Body: "finding"}}}
	if !c.UpdateTeam("team", team) {
		t.Fatal("update team")
	}
	team.Lanes[0].Trace[0].Text = "mutated input"
	team.Tasks[0].Dependencies[0] = "mutated input"
	out := c.SnapshotAt(1).Payload.(TeamCardSnapshot)
	out.Update.Lanes[0].Trace[0].Text = "mutated snapshot"
	out.Update.Tasks[0].Dependencies[0] = "mutated snapshot"
	stored := c.SnapshotAt(1).Payload.(TeamCardSnapshot)
	if stored.Update.Lanes[0].Trace[0].Text != "trace" || stored.Update.Tasks[0].Dependencies[0] != "dep" {
		t.Fatalf("team snapshot leaked mutation: %#v", stored.Update)
	}
}
