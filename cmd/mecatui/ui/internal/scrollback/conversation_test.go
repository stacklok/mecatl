package scrollback

import (
	"reflect"
	"testing"
)

func TestMecatuiTypedScrollbackModel_Scenario1_StableIdentityAndImmutableSnapshots(t *testing.T) {
	var c Conversation
	userID := c.Messages().AddUser(UserInput{Text: "prompt", Media: []string{"image/png"}})
	toolID := c.Tools().Add(ToolCall{ID: "call", Name: "Read", Arguments: `{"path":"a.go"}`, Artifacts: []Artifact{{Text: "before"}}})
	if userID == toolID || userID == 0 || toolID == 0 {
		t.Fatalf("IDs = %d, %d; want distinct non-zero document-local IDs", userID, toolID)
	}
	in := ToolResult{Body: "done", Artifacts: []Artifact{{Text: "after"}}}
	if !c.Tools().Resolve("call", in) {
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

func TestMecatuiTypedScrollbackModel_Scenario1_PayloadFamiliesAreSpecialized(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "ordinary", Name: "Read"})
	c.Tools().Add(ToolCall{ID: "wrong-sub", Name: "Read"})
	c.Tools().Add(ToolCall{ID: "sub", Name: "Subagent"})
	c.Tools().Add(ToolCall{ID: "wrong-team", Name: "Read"})
	c.Tools().Add(ToolCall{ID: "team", Name: "Team"})

	if c.Subagents().Update("ordinary", SubagentUpdate{Current: "late"}) ||
		c.Teams().Update("ordinary", TeamUpdate{TeamID: "late"}) ||
		c.Subagents().Start("wrong-sub", SubagentStart{}) ||
		c.Teams().Start("wrong-team", TeamStart{}) {
		t.Fatal("non-start or mismatched delegation transition specialized an ordinary tool")
	}
	if !c.Subagents().Start("sub", SubagentStart{ChildID: "child"}) ||
		!c.Teams().Start("team", TeamStart{TeamID: "team-1"}) {
		t.Fatal("matching start did not specialize its expected tool family")
	}
	want := []Kind{KindTool, KindTool, KindSubagent, KindTool, KindTeam}
	for i, kind := range want {
		if got := c.SnapshotAt(i).Payload.Kind(); got != kind {
			t.Fatalf("card %d kind = %v, want %v", i, got, kind)
		}
	}
}

func TestMecatuiTypedScrollbackModel_Scenario1_TransitionsAdvanceVisibleRevision(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "call", Name: "Subagent"})
	initial := c.SnapshotAt(0).Revision
	if !c.Subagents().Start("call", SubagentStart{Goal: "inspect"}) {
		t.Fatal("start subagent")
	}
	started := c.SnapshotAt(0).Revision
	if started != initial+1 {
		t.Fatalf("revision = %d, want %d", started, initial+1)
	}
	if !c.Subagents().Update("call", SubagentUpdate{Current: "Read", Trace: []TraceEntry{{Text: "a"}}}) {
		t.Fatal("update subagent")
	}
	updated := c.SnapshotAt(0).Revision
	if updated != started+1 {
		t.Fatalf("revision = %d, want %d", updated, started+1)
	}
	if !c.Subagents().Update("call", SubagentUpdate{Current: "Read", Trace: []TraceEntry{{Text: "a"}}}) {
		t.Fatal("idempotent update should match")
	}
	if got := c.SnapshotAt(0).Revision; got != updated {
		t.Fatalf("idempotent revision = %d, want %d", got, updated)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario1_InvalidTransitionsAreNoOps(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "ordinary", Name: "Read"})
	before := c.SnapshotAt(0)
	if c.Teams().Update("missing", TeamUpdate{}) || c.Subagents().Start("missing", SubagentStart{}) || c.Teams().Update("ordinary", TeamUpdate{}) {
		t.Fatal("invalid transitions matched")
	}
	after := c.SnapshotAt(0)
	if !reflect.DeepEqual(after, before) || c.Len() != 1 {
		t.Fatal("invalid transition mutated conversation")
	}
}

func TestConversationMetadataAtAndSnapshotForCall(t *testing.T) {
	var c Conversation
	c.Messages().AddUser(UserInput{Text: "hello"})
	c.Tools().Add(ToolCall{ID: "call", Name: "Read", Arguments: "before", Artifacts: []Artifact{{Data: []byte("before")}}})

	if got, want := c.MetadataAt(0), (BlockMetadata{ID: 1, Revision: 0, Kind: KindUser}); got != want {
		t.Fatalf("MetadataAt(0) = %#v, want %#v", got, want)
	}
	snapshot, ok := c.SnapshotForCall("call")
	if !ok || snapshot.ID != 2 || snapshot.Revision != 0 || snapshot.Payload.Kind() != KindTool {
		t.Fatalf("SnapshotForCall(call) = %#v, %t", snapshot, ok)
	}
	tool := snapshot.Payload.(ToolCardSnapshot)
	tool.Call.Artifacts[0].Data[0] = 'X'
	if got := string(c.SnapshotAt(1).Payload.(ToolCardSnapshot).Call.Artifacts[0].Data); got != "before" {
		t.Fatalf("snapshot mutation leaked into conversation: %q", got)
	}
	if got, ok := c.SnapshotForCall(""); ok || got != (BlockSnapshot{}) {
		t.Fatalf("SnapshotForCall(empty) = %#v, %t", got, ok)
	}
}
