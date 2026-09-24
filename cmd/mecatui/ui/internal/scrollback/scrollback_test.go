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

func TestMecatuiTypedScrollbackModel_Scenario1_ChangedFilesAppendixIdentityAndFallback(t *testing.T) {
	var c Conversation
	first := c.Messages().AddUser(UserInput{Text: "first"})
	appendixID := c.RecordFileChange("first.go")
	if appendixID == first {
		t.Fatalf("appendix ID = %d, must not reuse block ID %d", appendixID, first)
	}
	second := c.Notices().AddNotice("later")
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

	c.Tools().Add(ToolCall{ID: "team", Name: "Team"})
	if !c.Teams().Start("team", TeamStart{TeamID: "team-1"}) {
		t.Fatal("start team")
	}
	team := TeamUpdate{Lanes: []TeamLane{{Name: "worker", Trace: []TraceEntry{{Text: "trace"}}}}, Tasks: []Task{{ID: "task", Dependencies: []string{"dep"}}}, Findings: []Finding{{Member: "worker", Body: "finding"}}}
	if !c.Teams().Update("team", team) {
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

func TestComponentFacadesAdvanceRevisionsAndOwnNestedData(t *testing.T) {
	var c Conversation
	messages := c.Messages()
	messages.AddAssistant(AssistantInput{Reasoning: "thinking", ReasoningStreaming: true})
	if !messages.AppendAssistant("answer") {
		t.Fatal("append assistant")
	}
	if got := c.SnapshotAt(0).Revision; got != 1 {
		t.Fatalf("assistant revision = %d, want 1", got)
	}

	call := ToolCall{ID: "sub", Name: "Subagent", Artifacts: []Artifact{{Data: []byte("call")}}}
	c.Tools().Add(call)
	call.Artifacts[0].Data[0] = 'X'
	if !c.Subagents().Start("sub", SubagentStart{Routing: RoutingDecision{Confidence: Float64(0.8)}}) {
		t.Fatal("start subagent")
	}
	update := SubagentUpdate{
		Trace:     []TraceEntry{{Text: "trace"}},
		Artifacts: []Artifact{{Data: []byte("result")}},
	}
	if !c.Subagents().Update("sub", update) {
		t.Fatal("update subagent")
	}
	update.Trace[0].Text = "mutated"
	update.Artifacts[0].Data[0] = 'X'

	snapshot := c.SnapshotAt(1)
	if snapshot.Revision != 2 {
		t.Fatalf("subagent revision = %d, want 2", snapshot.Revision)
	}
	subagent := snapshot.Payload.(SubagentCardSnapshot)
	if string(subagent.Call.Artifacts[0].Data) != "call" || subagent.Update.Trace[0].Text != "trace" || string(subagent.Update.Artifacts[0].Data) != "result" {
		t.Fatalf("component input mutation leaked into snapshot: %#v", subagent)
	}
	subagent.Update.Trace[0].Text = "snapshot mutation"
	if got := c.SnapshotAt(1).Payload.(SubagentCardSnapshot).Update.Trace[0].Text; got != "trace" {
		t.Fatalf("snapshot mutation leaked into conversation: %q", got)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario1_TerminalTransitionsAreNoOps(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "sub", Name: "Subagent"})
	terminalSubagent := SubagentUpdate{Done: true, Stop: "end_turn"}
	if !c.Subagents().Start("sub", SubagentStart{}) || !c.Subagents().Update("sub", terminalSubagent) {
		t.Fatal("complete subagent")
	}
	before := c.SnapshotAt(0)
	if !c.Subagents().Update("sub", terminalSubagent) {
		t.Fatal("identical terminal subagent replay should be accepted")
	}
	if c.Subagents().Update("sub", SubagentUpdate{Current: "late"}) {
		t.Fatal("conflicting terminal subagent update should be rejected")
	}
	if got := c.SnapshotAt(0); !reflect.DeepEqual(got, before) {
		t.Fatalf("terminal subagent update changed card: %#v", got)
	}

	c.Tools().Add(ToolCall{ID: "team", Name: "Team"})
	terminalTeam := TeamUpdate{Done: true, Tasks: []Task{{ID: "final"}}, Findings: []Finding{{Body: "final"}}}
	if !c.Teams().Start("team", TeamStart{}) || !c.Teams().Update("team", terminalTeam) {
		t.Fatal("complete team")
	}
	before = c.SnapshotAt(1)
	if !c.Teams().Update("team", terminalTeam) {
		t.Fatal("identical terminal team replay should be accepted")
	}
	if c.Teams().Update("team", TeamUpdate{Tasks: []Task{{ID: "late"}}}) {
		t.Fatal("conflicting terminal team update should be rejected")
	}
	if got := c.SnapshotAt(1); !reflect.DeepEqual(got, before) {
		t.Fatalf("terminal team update changed card: %#v", got)
	}

	c.Tools().Add(ToolCall{ID: "tool"})
	result := ToolResult{Body: "first"}
	if !c.Tools().Resolve("tool", result) {
		t.Fatal("resolve tool")
	}
	before = c.SnapshotAt(2)
	if !c.Tools().Resolve("tool", result) {
		t.Fatal("identical terminal tool result replay should be accepted")
	}
	if c.Tools().Resolve("tool", ToolResult{Body: "late"}) {
		t.Fatal("conflicting terminal tool result should be rejected")
	}
	if got := c.SnapshotAt(2); !reflect.DeepEqual(got, before) {
		t.Fatalf("terminal tool resolution changed card: %#v", got)
	}
}

func TestMecatuiTypedScrollbackModel_Scenario2_EmptySlicesRemainNonNil(t *testing.T) {
	var c Conversation
	c.Messages().AddUser(UserInput{Media: []string{}})
	user := c.SnapshotAt(0).Payload.(UserCardSnapshot)
	if user.Media == nil {
		t.Fatal("non-nil empty media lost during snapshot")
	}
	c.Tools().Add(ToolCall{ID: "call", Artifacts: []Artifact{}})
	tool := c.SnapshotAt(1).Payload.(ToolCardSnapshot)
	if tool.Call.Artifacts == nil {
		t.Fatal("non-nil empty artifacts lost during snapshot")
	}
}

func TestConversationMetadataAtDoesNotExposePayload(t *testing.T) {
	var c Conversation
	c.Messages().AddUser(UserInput{Text: "hello"})

	got := c.MetadataAt(0)
	if got.ID != 1 || got.Revision != 0 || got.Kind != KindUser {
		t.Fatalf("MetadataAt(0) = %#v", got)
	}
}
