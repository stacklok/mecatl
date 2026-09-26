package scrollback

import (
	"reflect"
	"testing"
)

func TestToolLifecycle(t *testing.T) {
	var c Conversation
	call := ToolCall{ID: "read", Name: "Read", Artifacts: []Artifact{{Data: []byte("request")}}}
	c.Tools().Add(call)
	call.Artifacts[0].Data[0] = 'X'
	result := ToolResult{Body: "done", IsError: true, Artifacts: []Artifact{{Data: []byte("response")}}}
	if !c.Tools().Resolve("read", result) {
		t.Fatal("resolve tool")
	}
	result.Artifacts[0].Data[0] = 'X'
	got := c.SnapshotAt(0).Payload.(ToolCardSnapshot)
	if !got.Resolved || string(got.Call.Artifacts[0].Data) != "request" || string(got.Result.Artifacts[0].Data) != "response" {
		t.Fatalf("tool snapshot = %#v", got)
	}
	if !c.Tools().Resolve("read", ToolResult{Body: "done", IsError: true, Artifacts: []Artifact{{Data: []byte("response")}}}) {
		t.Fatal("identical terminal result replay")
	}
	if c.Tools().Resolve("read", ToolResult{Body: "different"}) || c.Tools().Resolve("missing", ToolResult{}) {
		t.Fatal("accepted conflicting or missing result")
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
