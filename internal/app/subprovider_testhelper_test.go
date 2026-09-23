package app

import (
	"context"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// regForTest builds a single-provider *providerRegistry backed by the given
// provider under id, with model as its default model. It is the test-side analogue
// of the build-time registry for the per-sub-agent-provider call sites
// (buildAgentSubagentEngines / buildSubagentTool / buildMemberEngine / buildTeamWiring),
// which now take a *providerRegistry + a parent provider id + a parent model. The
// parent id/model these call sites are exercised with is (id, model).
func regForTest(provider port.LLMProvider, id, model string) *providerRegistry {
	return &providerRegistry{
		entries:      map[string]providerEntry{id: {id: id, provider: provider, available: true}},
		defaultID:    id,
		defaultModel: model,
	}
}

// twoProviderReg builds a *providerRegistry with TWO distinct mock-backed
// providers, default = a's id. It is the per-sub-agent-provider analogue of
// twoProviderFactory's registry, for tests that route a def's pinned provider to a
// non-default entry.
func twoProviderReg(aProvider port.LLMProvider, aID, aModel string, bProvider port.LLMProvider, bID string) *providerRegistry {
	return &providerRegistry{
		entries: map[string]providerEntry{
			aID: {id: aID, provider: aProvider, available: true},
			bID: {id: bID, provider: bProvider, available: true},
		},
		defaultID:    aID,
		defaultModel: aModel,
	}
}

// memberFactoryForTest is the OLD-arity buildMemberEngine wrapper for existing
// tests: it builds a single-provider registry (id=providerMock, model=cfg.Model)
// for `provider` and threads it as the parent. The per-sub-agent-provider feature
// added (provReg, parentProviderID, parentModel) params; the historical
// member-catalog/isolation tests don't exercise a provider switch, so they inherit
// the single mock provider exactly as before.
func memberFactoryForTest(cfg Config, provider port.LLMProvider, teamHooks port.HookRunner, reg *agents.Registry, skillIdx skillIndex, runner tool.CommandRunner, roIsolationAvailable bool, mainMgr *mcp.Manager) server.MemberEngineFactory {
	// The single `runner` doubles as both the read-only (trust-gated) and the
	// mutating (ungated) runner — the historical single-runner shape these tests were
	// written against. The issue-#40 asymmetry (untrusted ⇒ read-only runner nil,
	// mutating runner live) is exercised through the REAL buildTeamWiring in
	// trust_shell_gate_test.go.
	return buildMemberEngine(cfg, regForTest(provider, providerMock, cfg.Model), provider, providerMock, cfg.Model,
		teamHooks, reg, skillIdx, runner, runner, roIsolationAvailable, mainMgr, catalogAssets{}, false)
}

// agentSubagentEnginesForTest is the OLD-arity buildAgentSubagentEngines wrapper for
// existing tests: it builds a single-provider registry (id=providerMock,
// model=cfg.Model) for `provider` and threads it as the parent. The historical
// per-def engine tests don't exercise a provider switch.
func agentSubagentEnginesForTest(ctx context.Context, cfg Config, provider port.LLMProvider, reg *agents.Registry, skillIdx skillIndex, defaultHooks port.HookRunner, runner tool.CommandRunner, mainMgr *mcp.Manager) (map[string]*agent.Engine, []agent.AgentMeta, func() error) {
	return buildAgentSubagentEngines(ctx, cfg, provider, regForTest(provider, providerMock, cfg.Model), providerMock, cfg.Model,
		reg, skillIdx, defaultHooks, runner, mainMgr)
}

// taskToolForTest is the OLD-arity buildSubagentTool wrapper for existing tests: it
// builds a single-provider registry (id=providerMock, model=cfg.Model) for
// `provider` and threads it as the parent.
func taskToolForTest(ctx context.Context, cfg Config, provider port.LLMProvider, hooks port.HookRunner, reg *agents.Registry, mainMgr *mcp.Manager) (tool.Tool, func() error) {
	return buildSubagentTool(ctx, cfg, regForTest(provider, providerMock, cfg.Model), provider, providerMock, cfg.Model,
		hooks, reg, mainMgr, nil, nil, catalogAssets{}, false)
}
