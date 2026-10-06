package scrollback

import (
	"reflect"
	"testing"
)

func TestADR_0370_Scenario3_ClientConfirmationAndReplacement(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "one", Name: "Read"})
	available := ToolResult{Body: "ready", Artifacts: []Artifact{{Kind: "resource_link", Name: "item"}}}
	if !c.Tools().ResolveAvailable("one", available) {
		t.Fatal("availability did not settle card")
	}
	before := c.SnapshotAt(0)
	if !c.Tools().Resolve("one", available) {
		t.Fatal("canonical confirmation rejected")
	}
	confirmed := c.SnapshotAt(0)
	beforePayload := before.Payload.(ToolCardSnapshot)
	confirmedPayload := confirmed.Payload.(ToolCardSnapshot)
	beforePayload.available, confirmedPayload.available = false, false
	if confirmed.ID != before.ID || !reflect.DeepEqual(confirmedPayload, beforePayload) {
		t.Fatalf("confirmation changed displayed card or identity: %+v", confirmed)
	}
	if !c.Tools().Resolve("one", available) || c.SnapshotAt(0).Revision != confirmed.Revision {
		t.Fatal("canonical replay changed confirmed card")
	}
	cancelled := ToolResult{Body: "cancelled", IsError: true}
	c.Tools().Add(ToolCall{ID: "replace", Name: "Read"})
	if !c.Tools().ResolveAvailable("replace", available) {
		t.Fatal("replacement availability rejected")
	}
	before = c.SnapshotAt(1)
	if !c.Tools().Resolve("replace", cancelled) {
		t.Fatal("canonical replacement rejected")
	}
	after := c.SnapshotAt(1)
	if after.ID != before.ID || after.Revision != before.Revision+1 || !reflect.DeepEqual(after.Payload.(ToolCardSnapshot).Result, cancelled) || c.Len() != 2 {
		t.Fatalf("replacement = %+v", after)
	}
	if c.Tools().Resolve("replace", available) || c.Tools().ResolveAvailable("replace", available) {
		t.Fatal("stale result replay changed canonical card")
	}
	if got := c.SnapshotAt(1); !reflect.DeepEqual(got, after) {
		t.Fatalf("stale replay changed card: %+v", got)
	}
	c.Tools().Add(ToolCall{ID: "two", Name: "Grep"})
	if !c.Tools().Resolve("two", cancelled) {
		t.Fatal("canonical-only stream did not settle call")
	}

	for _, tc := range []struct {
		name  string
		start func(*Conversation, string) bool
	}{
		{"Subagent", func(c *Conversation, id string) bool { return c.Subagents().Start(id, SubagentStart{}) }},
		{"Team", func(c *Conversation, id string) bool { return c.Teams().Start(id, TeamStart{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var specialized Conversation
			specialized.Tools().Add(ToolCall{ID: "delegated", Name: tc.name})
			if !tc.start(&specialized, "delegated") || !specialized.Tools().ResolveAvailable("delegated", available) {
				t.Fatal("specialized card did not settle from availability")
			}
			before := specialized.SnapshotAt(0)
			if !specialized.Tools().Resolve("delegated", cancelled) {
				t.Fatal("specialized card did not accept canonical replacement")
			}
			after := specialized.SnapshotAt(0)
			if after.ID != before.ID || after.Revision != before.Revision+1 || specialized.Len() != 1 {
				t.Fatalf("specialized card duplicated or not replaced: %+v", after)
			}
			switch payload := after.Payload.(type) {
			case SubagentCardSnapshot:
				if !reflect.DeepEqual(payload.Result, cancelled) {
					t.Fatalf("subagent result = %+v, want %+v", payload.Result, cancelled)
				}
			case TeamCardSnapshot:
				if !reflect.DeepEqual(payload.Result, cancelled) {
					t.Fatalf("team result = %+v, want %+v", payload.Result, cancelled)
				}
			default:
				t.Fatalf("unexpected specialized payload %T", after.Payload)
			}
		})
	}
}

func TestToolCallMetadataAtDoesNotExposeResultPayload(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "read", Name: "Read", Arguments: `{"path":"x"}`})
	if !c.Tools().Resolve("read", ToolResult{IsError: true, Artifacts: []Artifact{{Data: []byte("binary")}}}) {
		t.Fatal("resolve tool")
	}

	got, ok := c.ToolCallMetadataAt(0)
	if !ok {
		t.Fatal("tool metadata missing")
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = c.ToolCallMetadataAt(0) }); allocs != 0 {
		t.Fatalf("metadata enumerator cloned result: %.0f allocations", allocs)
	}
	if got.ID != 1 || got.Revision != 1 || got.CallID != "read" || got.Name != "Read" || got.Arguments != `{"path":"x"}` || !got.ResultReceived || got.Provisional || got.Terminal || !got.ResultError || got.LifecycleFailed {
		t.Fatalf("metadata = %#v", got)
	}
}

func TestToolCallMetadataLifecycleSignals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Conversation)
		want  ToolCallMetadata
	}{
		{"provisional tool", func(c *Conversation) {
			c.Tools().Add(ToolCall{ID: "call", Name: "Read"})
			c.Tools().ResolveAvailable("call", ToolResult{})
		}, ToolCallMetadata{ResultReceived: true, Provisional: true}},
		{"failed tool lifecycle", func(c *Conversation) {
			c.Tools().Add(ToolCall{ID: "call", Name: "Read"})
			c.Tools().Finish("call", true)
		}, ToolCallMetadata{Terminal: true, LifecycleFailed: true}},
		{"terminal subagent", func(c *Conversation) {
			c.Tools().Add(ToolCall{ID: "call", Name: "Subagent"})
			c.Subagents().Start("call", SubagentStart{})
			c.Subagents().Update("call", SubagentUpdate{Done: true, Stop: "end_turn"})
		}, ToolCallMetadata{Terminal: true, Stop: "end_turn"}},
		{"terminal team", func(c *Conversation) {
			c.Tools().Add(ToolCall{ID: "call", Name: "Team"})
			c.Teams().Start("call", TeamStart{})
			c.Teams().Update("call", TeamUpdate{Done: true, Stop: "error"})
		}, ToolCallMetadata{Terminal: true, Stop: "error"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var c Conversation
			test.setup(&c)
			got, ok := c.ToolCallMetadataAt(0)
			if !ok {
				t.Fatal("metadata missing")
			}
			if got.ResultReceived != test.want.ResultReceived || got.Provisional != test.want.Provisional || got.Terminal != test.want.Terminal || got.ResultError != test.want.ResultError || got.LifecycleFailed != test.want.LifecycleFailed || got.Stop != test.want.Stop {
				t.Fatalf("signals = %#v, want %#v", got, test.want)
			}
		})
	}
}

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

func TestToolFinishedProjectionAllowsOnlyAuthoritativeResult(t *testing.T) {
	var c Conversation
	call := ToolCall{ID: "parallel", Name: "Parallel", Arguments: `{}`}
	c.Tools().Add(call)
	if !c.Tools().Finish(call.ID, true) {
		t.Fatal("finish tool projection")
	}
	if c.Tools().ReconcileUnresolved(ToolCall{ID: call.ID, Name: call.Name, Arguments: `{"changed":true}`}) {
		t.Fatal("finished card accepted a reconciled call")
	}
	if !c.Tools().Resolve(call.ID, ToolResult{Body: "authoritative"}) {
		t.Fatal("finished card rejected its authoritative result")
	}
	got := c.SnapshotAt(0).Payload.(ToolCardSnapshot)
	if !got.Finished || !got.Failed || !got.Resolved || got.Result.Body != "authoritative" {
		t.Fatalf("tool snapshot = %#v", got)
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
