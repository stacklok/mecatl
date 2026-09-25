package app

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestInteractiveWiringMainCarriesCfgChildForcesFalse (T5) proves the composition wiring
// of agent.Deps.Interactive: the MAIN engine's deps carry cfg.Interactive verbatim (so a
// subagent's unresolved ask can be surfaced to the human when a client is attached),
// while a CHILD engine's deps ALWAYS force Interactive=false (subagents cannot recurse,
// so they install no child-ask router — the nested-surfacing fail-safe). A silent break
// in either direction (child surfacing, or main never surfacing) is caught here.
func TestInteractiveWiringMainCarriesCfgChildForcesFalse(t *testing.T) {
	provider := mockllm.New(mockllm.TextTurn("x"))
	policy := permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil)

	// Main deps with Interactive=true must carry it through.
	mainOn := engineDepsForProvider(
		Config{Model: "m", Interactive: true}, provider, testProviderModel("m"), func() int { return defaultContextWindowTokens },
		nil, policy, hookexec.New(nil), nil, nil)
	if !mainOn.Interactive {
		t.Fatalf("main deps with cfg.Interactive=true must carry Interactive=true")
	}

	// Main deps with Interactive=false must carry false (headless daemon / demo).
	mainOff := engineDepsForProvider(
		Config{Model: "m", Interactive: false}, provider, testProviderModel("m"), func() int { return defaultContextWindowTokens },
		nil, policy, hookexec.New(nil), nil, nil)
	if mainOff.Interactive {
		t.Fatalf("main deps with cfg.Interactive=false must carry Interactive=false")
	}

	// CHILD deps must force Interactive=false EVEN when the parent cfg is interactive —
	// a subagent never surfaces a further-nested ask, so it installs no router.
	pc := promptConfig(Config{Model: "m"}, "")
	child := childEngineDepsForProvider(
		Config{Model: "m", Interactive: true}, "task", provider, testProviderModel("m"), func() int { return defaultContextWindowTokens },
		tool.NewCatalog(), pc, hookexec.New(nil))
	if child.Interactive {
		t.Fatalf("child deps must force Interactive=false even when cfg.Interactive=true (nested-surfacing fail-safe)")
	}
	if child.Role != "task" {
		t.Fatalf("child deps Role = %q, want %q", child.Role, "task")
	}
}

// compile-time guard: prompt.Config is the type promptConfig returns (keeps the import
// used even if the helper signature changes).
var _ = prompt.Config{}
