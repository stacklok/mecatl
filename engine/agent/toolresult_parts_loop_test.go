package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestLoopEmitsToolResultWithParts proves the agent loop wires a tool's typed
// ToolResult.Parts (BlockResourceLink + BlockText) end-to-end onto the
// EvToolResult event it emits — the loop→EvToolResult contract for typed
// tool-result blocks (issue #223 Phase 1/2). The mcpperf round-trip test drives
// tool.Execute() directly, never through Engine.Run, so it does not exercise
// this wiring; this test drives the full loop on mockllm + a fake tool whose
// Execute returns NewToolResultWithParts, and asserts the emitted EvToolResult
// event's ToolResult.Parts is non-nil and carries the right block kinds.
func TestLoopEmitsToolResultWithParts(t *testing.T) {
	// The tool returns a typed result: a resource_link block + a text block.
	// It is read-only so it runs in the read-parallel batch (no mutation).
	resultTool := &fakeTool{name: "Link", readOnly: true,
		exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			parts := []session.Content{
				session.NewResourceLinkBlock(
					"https://example.com/doc",
					"doc",
					"The Doc",
					"a doc to read",
					"text/plain",
					42,
					nil,
				),
				session.NewTextBlock("summary text"),
			}
			return session.NewToolResultWithParts(in.ID, "summary text", parts), nil
		}}
	cat := catalogWith(t, resultTool)

	llm := mockllm.New(
		mockllm.ChunksTurn(
			mockllm.TextChunk("fetching the link"),
			mockllm.ToolCallChunk(toolCall("c1", "Link", `{"uri":"https://example.com/doc"}`)),
			mockllm.UsageChunk(session.Usage{InputTokens: 10, OutputTokens: 2}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
		mockllm.ChunksTurn(
			mockllm.TextChunk("done with the link"),
			mockllm.UsageChunk(session.Usage{InputTokens: 5, OutputTokens: 3}),
			mockllm.DoneChunk(session.StopEndTurn),
		),
	)

	clk := &fakeClock{t: time.Unix(0, 0)}
	e := newEngine(agent.Deps{LLM: llm, Catalog: cat, Clock: clk})
	sess := newSession(t, session.Limits{})
	ws := memfs.NewWorkspace("/ws")

	r := e.Run(context.Background(), sess, agent.EnvForWS(ws, nil), agent.RunRequest{Text: "fetch the link"})
	evs := drain(r)

	// Find the EvToolResult event for the call and assert Parts carried through.
	var got *session.ToolResult
	for i := range evs {
		if evs[i].Type == session.EvToolResult && evs[i].ToolResult != nil &&
			evs[i].ToolResult.CallID == "c1" {
			got = evs[i].ToolResult
			break
		}
	}
	if got == nil {
		t.Fatalf("no EvToolResult event for call c1 in %v", typesOf(evs))
	}
	if len(got.Parts) != 2 {
		t.Fatalf("ToolResult.Parts len = %d, want 2", len(got.Parts))
	}
	// First block: the resource_link.
	if got.Parts[0].BlockKind != session.BlockResourceLink {
		t.Fatalf("Parts[0].BlockKind = %q, want %q", got.Parts[0].BlockKind, session.BlockResourceLink)
	}
	if got.Parts[0].URL != "https://example.com/doc" {
		t.Fatalf("Parts[0].URL = %q, want https://example.com/doc", got.Parts[0].URL)
	}
	// Second block: the text summary.
	if got.Parts[1].BlockKind != session.BlockText {
		t.Fatalf("Parts[1].BlockKind = %q, want %q", got.Parts[1].BlockKind, session.BlockText)
	}
	if got.Parts[1].Text != "summary text" {
		t.Fatalf("Parts[1].Text = %q, want \"summary text\"", got.Parts[1].Text)
	}
	// The model-facing Content string survives alongside the parts.
	if got.Content != "summary text" {
		t.Fatalf("ToolResult.Content = %q, want \"summary text\"", got.Content)
	}
	// Not an error.
	if got.IsError {
		t.Fatalf("ToolResult.IsError = true, want false")
	}
}
