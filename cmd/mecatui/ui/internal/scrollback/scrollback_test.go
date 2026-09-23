package scrollback

import (
	"reflect"
	"testing"
)

func TestMecatuiTypedScrollbackModel_Scenario1_StableIdentityAndImmutableSnapshots(t *testing.T) {
	var c Conversation
	userID := c.AddUser(UserInput{Text: "prompt", Media: []string{"image/png"}})
	toolID := c.AddToolCall(ToolCall{ID: "call", Name: "Read", Arguments: map[string]string{"path": "a.go"}, Artifacts: []string{"before"}})
	if userID == toolID || userID == 0 || toolID == 0 {
		t.Fatalf("IDs = %d, %d; want distinct non-zero document-local IDs", userID, toolID)
	}
	in := ToolResult{Body: "done", Artifacts: []string{"after"}}
	if !c.ResolveTool("call", in) {
		t.Fatal("resolve tool")
	}
	in.Artifacts[0] = "mutated input"
	snapshot := c.SnapshotAt(1)
	tool, ok := snapshot.Payload.(ToolCardSnapshot)
	if !ok {
		t.Fatalf("payload = %T, want ToolCardSnapshot", snapshot.Payload)
	}
	tool.Call.Arguments["path"] = "mutated snapshot"
	tool.Result.Artifacts[0] = "mutated snapshot"
	again := c.SnapshotAt(1).Payload.(ToolCardSnapshot)
	if got, want := again.Call.Arguments["path"], "a.go"; got != want {
		t.Fatalf("arguments = %q, want %q", got, want)
	}
	if got, want := again.Result.Artifacts, []string{"after"}; !reflect.DeepEqual(got, want) {
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
