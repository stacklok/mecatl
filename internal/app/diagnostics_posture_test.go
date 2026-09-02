package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestDiagnosticsPostureFactoryPaths pins the diagnostics affordance where it is
// model-visible: main-session factory engines only. Child and checker engines
// must not advertise a client command they cannot use.
func TestDiagnosticsPostureFactoryPaths(t *testing.T) {
	ctx := context.Background()
	var systems []prompt.Layered
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		systems = append(systems, req.System)
	})}, mockllm.TextTurn("ok"), mockllm.TextTurn("ok"), mockllm.TextTurn("ok"))
	cfg := Config{Model: "gpt-5", SubagentAskReviewerModel: "gpt-5"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), nil, nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	main, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatalf("main factory: %v", err)
	}
	defer func() { _ = main.Close() }()
	drivePrompt(t, main.Engine, "main")

	child := newChildEngineForProvider(cfg, "subagent", provider, cfg.Model, func() int { return defaultContextWindowTokens }, tool.NewCatalog(), promptConfig(cfg, ""), nil)
	drivePrompt(t, child, "child")
	checkerDeps, ok := askAdjudicatorDeps(cfg, reg, provider, providerOpenAI, cfg.Model)
	if !ok {
		t.Fatal("checker deps were not built")
	}
	drivePrompt(t, agent.NewEngine(checkerDeps), "checker")

	if len(systems) != 3 {
		t.Fatalf("captured systems = %d, want 3", len(systems))
	}
	if !strings.Contains(systems[0].StablePrefix, diagnosticsPostureNote) {
		t.Fatal("main factory StablePrefix omits diagnostics affordance")
	}
	for i, name := range []string{"child", "checker"} {
		if strings.Contains(systems[i+1].StablePrefix, diagnosticsPostureNote) {
			t.Fatalf("%s StablePrefix advertises the main-only diagnostics affordance", name)
		}
	}
}

func drivePrompt(t *testing.T, e *agent.Engine, id string) {
	t.Helper()
	run := e.Run(context.Background(), session.New(session.SessionID(id), session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now()), memEnvironment("/ws"), agent.RunRequest{Text: "hi"})
	for range run.Events() {
	}
}
