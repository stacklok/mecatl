package app

import (
	"context"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestBuildAgentDefRootEngineScopesToDefTools is a direct construction test of
// buildAgentDefRootEngine (ADR 0352 — not yet wired to any session-creation path).
// It asserts the resulting engine's tool surface is EXACTLY the def's scoped allowlist
// (never the ordinary root's full default catalog) and that the engine is otherwise a
// usable, ordinary-shaped engine (per Deps.Model). Deps internals are not inspectable
// from this package (agent.Engine encapsulates them), so — mirroring this file's own
// recordingProvider precedent for asserting Deps.Model — behavioral assertions
// (HasTool, Model) are the established seam, not reflection over private fields.
func TestBuildAgentDefRootEngineScopesToDefTools(t *testing.T) {
	def := agents.AgentDef{
		Name:  "root-specialist",
		Tools: []string{"Read", "Grep"},
	}
	base := baseSubagentTools(Config{}) // no shell configured => no Shell in base
	provider := mockllm.New(mockllm.TextTurn("hi"))
	store := memstore.New()
	policy := permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)
	windowFn := func() int { return 128000 }

	eng, mcpClose, names, resources, skillCount := buildAgentDefRootEngine(
		context.Background(), Config{}, def, "root-specialist", "test",
		provider, "test-model", windowFn,
		base, false /*allowMutating*/, false, /*allowShell*/
		nil /*skillIdx*/, hookexec.New(nil), nil /*runner*/, nil, /*mainMgr*/
		store, policy, nil /*mcpProvider*/, nil, /*instructions*/
		nil /*provReg*/, provider, "test-provider", nil, /*guardrailWaiver*/
	)
	if mcpClose != nil {
		t.Errorf("mcpClose = non-nil for a def with no mcpServers")
	}
	if len(resources) != 0 {
		t.Errorf("resources = %v, want empty", resources)
	}
	if skillCount != 0 {
		t.Errorf("skillCount = %d, want 0", skillCount)
	}

	if eng.Model() != "test-model" {
		t.Errorf("Model() = %q, want %q", eng.Model(), "test-model")
	}
	if !eng.HasTool("Read") || !eng.HasTool("Grep") {
		t.Errorf("engine missing an allow-listed tool: names=%v", names)
	}
	// The ceiling is baseSubagentTools, never the ordinary root's full catalog — a
	// def-restricted root engine must never carry delegation tools regardless of what
	// def.Tools names (callSiteExcluded enforces this at scopedToolNamesMode).
	for _, excluded := range []string{"Subagent", "Parallel", "Team", "ToolSearch"} {
		if eng.HasTool(excluded) {
			t.Errorf("engine unexpectedly has excluded tool %q", excluded)
		}
	}
	for _, unlisted := range []string{"Write", "Edit", "Shell", "WebFetch"} {
		if eng.HasTool(unlisted) {
			t.Errorf("engine has tool %q not in def.Tools", unlisted)
		}
	}
}
