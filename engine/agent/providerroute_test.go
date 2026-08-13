package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// TestProviderRouteChunkEmitsEvent pins the issue-#480 loop path: a
// ChunkProviderRoute from the provider is relayed verbatim onto a client-visible
// EvProviderRoute event (Text = the downstream slug), and is NOT recorded into
// the model's conversation history. A turn with no such chunk emits no event.
func TestProviderRouteChunkEmitsEvent(t *testing.T) {
	llm := mockllm.New(
		// Turn 1: text + a provider-route chunk + usage + done.
		mockllm.Turn{Chunks: []port.Chunk{
			{Kind: port.ChunkText, Text: "answer"},
			{Kind: port.ChunkProviderRoute, Text: "anthropic"},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}},
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "hi"}))

	var routes []session.Event
	for _, ev := range evs {
		if ev.Type == session.EvProviderRoute {
			routes = append(routes, ev)
		}
	}
	if len(routes) != 1 {
		t.Fatalf("got %d EvProviderRoute events, want 1 (evs=%v)", len(routes), typesOf(evs))
	}
	if routes[0].Text != "anthropic" {
		t.Errorf("EvProviderRoute.Text = %q, want %q (verbatim slug)", routes[0].Text, "anthropic")
	}

	// The slug is display-only: it must NOT enter the recorded conversation.
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleAssistant && m.Text == "anthropic" {
			t.Errorf("provider-route slug leaked into the assistant message text")
		}
	}
}

// TestProviderRouteAbsentEmitsNoEvent pins the honest degradation: a turn whose
// provider emits no ChunkProviderRoute (a cache hit, or any non-openrouter
// provider) produces no EvProviderRoute event — never a fabricated value.
func TestProviderRouteAbsentEmitsNoEvent(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("plain answer"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	evs := drain(e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "hi"}))
	if containsType(evs, session.EvProviderRoute) {
		t.Fatalf("EvProviderRoute emitted with no ChunkProviderRoute (evs=%v)", typesOf(evs))
	}
}
