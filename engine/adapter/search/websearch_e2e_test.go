package search_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/search"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestWebSearchModelFacingE2E is the headline OFFLINE model-facing flow: the model
// emits a WebSearch call, gets bounded FENCED results back, then a second scripted
// turn acts on a URL discovered in those results. It exercises the REAL
// WebSearchTool over a fake provider through the REAL agent loop (mockllm) — no
// network.
//
// MUTATION-VERIFY: replace the provider with search.Unavailable{} (so no real
// results return) — the assertion that the discovered URL reached the loop fails,
// proving the provider call + result formatting are load-bearing for the flow.
func TestWebSearchModelFacingE2E(t *testing.T) {
	const discoveredURL = "https://go.dev/doc/go1.26"

	fake := search.NewFake(
		tool.SearchResult{
			Title:   "Go 1.26 Release Notes",
			URL:     discoveredURL,
			Snippet: "The Go 1.26 release notes.",
			Source:  "go.dev",
		},
	)
	cat := tool.NewCatalog()
	cat.MustRegister(search.NewWebSearchTool(fake))

	llm := mockllm.New(
		// Turn 1: the model decides to search.
		mockllm.ChunksTurn(
			mockllm.TextChunk("searching the web"),
			mockllm.ToolCallChunk(session.NewToolCall("c1", "WebSearch", []byte(`{"query":"go 1.26 release notes"}`))),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		// Turn 2: the model reports the URL it discovered (acting on a result).
		mockllm.ChunksTurn(
			mockllm.TextChunk("I found the release notes at "+discoveredURL),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	policy := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	e := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Clock: wallclock.Clock{}, Policy: policy})
	sess := session.New("s1", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	ws := memfs.NewWorkspace("/ws")

	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, ws, nil)
	r := e.Run(context.Background(), sess, env, agent.RunRequest{Text: "find the go 1.26 release notes"})
	var evs []session.Event
	for ev := range r.Events() {
		evs = append(evs, ev)
	}

	// The tool result must carry the discovered URL inside an untrusted fence.
	var foundResult bool
	for _, ev := range evs {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && strings.Contains(ev.ToolResult.Content, discoveredURL) {
			foundResult = true
			if !strings.Contains(ev.ToolResult.Content, agent.UntrustedFence) {
				t.Fatalf("WebSearch result not fenced:\n%s", ev.ToolResult.Content)
			}
		}
	}
	if !foundResult {
		t.Fatalf("WebSearch tool result with the discovered URL not found in events: %v", typesOf(evs))
	}

	// Fake must have been called exactly once (the search turn).
	if fake.Calls() != 1 {
		t.Fatalf("expected exactly 1 provider call, got %d", fake.Calls())
	}

	// The final turn acts on the discovered URL.
	last := lastResultText(t, evs)
	if !strings.Contains(last, discoveredURL) {
		t.Fatalf("final turn did not act on the discovered URL; final text = %q", last)
	}
}

func typesOf(evs []session.Event) []session.EventType {
	out := make([]session.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func lastResultText(t *testing.T, evs []session.Event) string {
	t.Helper()
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == session.EvResult && evs[i].Result != nil {
			return evs[i].Result.Text
		}
	}
	t.Fatal("no EvResult event found")
	return ""
}
