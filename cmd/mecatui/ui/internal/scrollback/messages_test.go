package scrollback

import "testing"

func TestAssistantLifecycle(t *testing.T) {
	var c Conversation
	messages := c.Messages()
	if messages.EndReasoningStream() {
		t.Fatal("ended reasoning on an empty conversation")
	}
	if !messages.AppendReasoning("thinking") {
		t.Fatal("append reasoning")
	}
	if got, want := c.SnapshotAt(0).Payload, (AssistantCardSnapshot{Reasoning: "thinking", ReasoningStreaming: true}); got != want {
		t.Fatalf("reasoning payload = %#v, want %#v", got, want)
	}
	if !messages.AppendAssistant("answer") {
		t.Fatal("append assistant")
	}
	if got, want := c.SnapshotAt(0).Payload, (AssistantCardSnapshot{Text: "answer", Reasoning: "thinking"}); got != want {
		t.Fatalf("assistant payload = %#v, want %#v", got, want)
	}
	if !messages.ReviseAssistant("revised") || !messages.EndReasoningStream() {
		t.Fatal("complete assistant lifecycle")
	}
	if got, want := c.SnapshotAt(0).Payload, (AssistantCardSnapshot{Text: "revised", Reasoning: "thinking"}); got != want {
		t.Fatalf("revised payload = %#v, want %#v", got, want)
	}

	c.Notices().AddNotice("boundary")
	if !messages.AppendAssistant("new") || !messages.ReviseAssistant("newer") {
		t.Fatal("append and revise assistant after non-assistant card")
	}
	if got, want := c.SnapshotAt(2).Payload, (AssistantCardSnapshot{Text: "newer"}); got != want {
		t.Fatalf("new assistant payload = %#v, want %#v", got, want)
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
