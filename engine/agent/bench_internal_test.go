package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// sinkRequest is a package-level sink defeating dead-code elimination for the
// white-box buildRequest benchmark.
var sinkRequest port.LLMRequest

// Compaction-benchmark sinks — every returned value is assigned so the compactor's
// work cannot be elided.
var (
	sinkMessages []session.Message
	sinkSummary  string
	sinkErr      error
)

// BenchmarkBuildRequest measures the unexported Engine.buildRequest — the per-turn
// LLMRequest assembly that combines the layered prompt (cache-stable prefix +
// volatile env suffix), the conversation history, and the mode-filtered tool specs.
// It runs once per turn and is therefore a hot path; allocs/op is the gated KPI.
// White-box because buildRequest is unexported; the Engine + session are built the
// same way the in-package tests do.
func BenchmarkBuildRequest(b *testing.B) {
	e := NewEngine(Deps{
		Catalog: benchInternalCatalog(),
		Model:   "bench-model",
		PromptConfig: prompt.Config{
			Env: prompt.Env{
				OS:        "linux",
				Shell:     "/bin/bash",
				GitStatus: "branch: main\nstatus:\n(clean)",
			},
		},
	})

	sess := session.New("bench", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
	// Prime a small, realistic conversation: user prompt → assistant reply → user
	// follow-up. RecordUserPrompt is legal while idle; BeginTurn moves to running so
	// RecordAssistant is legal.
	_ = sess.RecordUserPrompt("explore the repo", nil)
	_ = sess.BeginTurn()
	_ = sess.RecordAssistant(session.NewAssistantMessage("on it", "", nil))
	_ = sess.RecordUserPrompt("now summarise", nil)

	// A zero-value Run carries an empty RunRequest (no per-run ExtraTools) — the legacy
	// run shape buildRequest sees on the common path. diag is Nop-safe via the engine.
	r := &Run{diag: e.deps.Diagnostics}

	ctx := context.Background()
	ws := memfs.NewWorkspace("/ws")
	env := testEnvironment(ws, nil)
	b.ReportAllocs()
	for b.Loop() {
		sinkRequest = e.buildRequest(ctx, r, sess, env)
	}
}

// BenchmarkHeuristicCompact measures the default network-free compactor over a
// fixed, tool-paired history. Compaction is the largest unmeasured allocation
// surface in the loop and has bricked sessions before (the orphaned-tool-pair bug),
// so its allocs/op is high-value to track. The conversation is built ONCE outside
// the loop; the compactor is pure (no provider, no LLM), so b.Loop just re-runs
// Compact over the same input. The alternating assistant-tool-call / tool-result
// shape ensures snapCutToTurnBoundary + ValidateToolPairing actually run.
func BenchmarkHeuristicCompact(b *testing.B) {
	conv := benchCompactionConversation()
	var c HeuristicCompactor
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		sinkMessages, sinkSummary, sinkErr = c.Compact(ctx, conv)
	}
}

// BenchmarkCascadeCompact measures the tiered cascade compactor with LLM nil, so
// only the deterministic tiers (snip → strip → collapse) run — no model call, fully
// offline. Tier 4 (LLM summary) is deliberately NOT exercised: it needs a live
// provider and would violate the offline-benchmark rule. Same fixed input and pure
// re-run discipline as the heuristic benchmark.
func BenchmarkCascadeCompact(b *testing.B) {
	conv := benchCompactionConversation()
	c := CascadeCompactor{} // LLM nil → tiers 1–3 only, deterministic and offline.
	ctx := context.Background()

	b.ReportAllocs()
	for b.Loop() {
		sinkMessages, sinkSummary, sinkErr = c.Compact(ctx, conv)
	}
}

// benchCompactionConversation builds a fixed ~60-message history: a system prompt,
// the user goal, then alternating assistant-with-tool-call / tool-result turns so
// the compactors' tool-pairing logic (snapCutToTurnBoundary + ValidateToolPairing)
// is genuinely exercised. Deterministic — no clock/random in the path.
func benchCompactionConversation() *session.Conversation {
	conv := &session.Conversation{}
	conv.Append(session.NewSystemMessage("You are a helpful coding assistant."))
	conv.Append(session.NewUserMessage("Refactor the package and keep tests green."))

	// 29 paired turns (assistant tool call + tool result) = 58 messages; with the
	// system + goal that is 60 total.
	for i := 0; i < 29; i++ {
		id := session.ToolCallID(fmt.Sprintf("c%02d", i))
		call := session.NewToolCall(id, "Read", []byte(fmt.Sprintf(`{"file_path":"pkg/file%02d.go"}`, i)))
		conv.Append(session.NewAssistantMessage(fmt.Sprintf("reading file %02d", i), "", []session.ToolCall{call}))
		// A sizeable body so truncateToolBody / strip / collapse have real work.
		body := strings.Repeat("package x; var _ = 1 // line of file content\n", 20)
		conv.Append(session.NewToolMessage(session.NewToolResult(id, body)))
	}
	return conv
}

// benchInternalCatalog builds a small catalog of read/edit/bash-shaped specs so
// buildRequest's tool-spec filtering and the prompt's tool-discipline-hint
// generation run over a representative set.
func benchInternalCatalog() *tool.Catalog {
	c := tool.NewCatalog()
	for _, name := range []string{"Read", "Edit", "Write", "Glob", "Grep", "Shell"} {
		c.MustRegister(&benchTool{name: name})
	}
	return c
}

// benchTool is a minimal read-only tool for catalog population in the white-box
// benchmark. Execute is never called (buildRequest only reads specs).
type benchTool struct{ name string }

func (t *benchTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Description: t.name + ": bench tool."}
}
func (*benchTool) ReadOnly() bool { return true }
func (*benchTool) Execute(_ context.Context, _ session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}
