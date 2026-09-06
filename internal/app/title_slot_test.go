package app

import (
	"context"
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
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func titleEligibilityService(t *testing.T, eligible func(server.ProviderSelector) bool, observe func(port.LLMRequest)) *server.Service {
	t.Helper()
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(observe)}, mockllm.TextTurn("main reply"))
	svc, err := newTestServerService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "main-model",
		}),
		Store:                   memstore.New(),
		PlacementProvider:       appTestPlacementProvider{root: "/ws"},
		PlacementScope:          "test",
		Now:                     func() time.Time { return time.Unix(1, 0) },
		TitleGenerationEligible: eligible,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestSessionTitleGeneration_Scenario2_CreationPersistsEligibility(t *testing.T) {
	var calls int
	provider := mockllm.New()
	eligibleCfg := Config{
		Model:        "main-model",
		ModelSlots:   map[string]string{slotTitle: "title"},
		ModelAliases: map[string]string{"title": "title-model"},
	}
	eligibleSvc := titleEligibilityService(t, titleGenerationEligible(eligibleCfg, regForTest(provider, providerOpenAI, eligibleCfg.Model)), func(port.LLMRequest) { calls++ })

	eligible, err := eligibleSvc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession eligible: %v", err)
	}
	if got, want := eligible.TitleGeneration, session.TitleGenerationPending; got != want {
		t.Fatalf("eligible TitleGeneration = %q, want %q", got, want)
	}

	disabledCfg := Config{Model: "main-model", ModelSlots: map[string]string{slotCheap: "cheap-model"}}
	disabledSvc := titleEligibilityService(t, titleGenerationEligible(disabledCfg, regForTest(provider, providerOpenAI, disabledCfg.Model)), func(port.LLMRequest) { calls++ })
	disabled, err := disabledSvc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession disabled: %v", err)
	}
	if got, want := disabled.TitleGeneration, session.TitleGenerationDisabled; got != want {
		t.Fatalf("disabled TitleGeneration = %q, want %q", got, want)
	}
	if calls != 0 {
		t.Fatalf("session creation made %d provider calls, want 0", calls)
	}
}

func TestSessionTitleGeneration_Scenario3_AbsentTitleSlotDisablesGeneration(t *testing.T) {
	cfg := Config{
		Model:        "main-model",
		ModelSlots:   map[string]string{slotCheap: "cheap-model"},
		ModelAliases: map[string]string{"cheap-model": "resolved-cheap-model"},
	}
	if model, ok := resolveSlotModel(cfg, slotTitle, cfg.Model); ok || model != "" {
		t.Fatalf("resolveSlotModel(title) = (%q, %v), want (\"\", false) without an explicit title binding", model, ok)
	}
}

func TestSessionTitleGeneration_Scenario3_TitleSlotRoutesOnlyGenerator(t *testing.T) {
	const (
		mainModel  = "main-model"
		titleModel = "title-model"
	)
	cfg, _ := projectFoldHarness(t, `
models:
  allowlist:
    - title-model
  aliases:
    operator-title: title-model
  slots:
    title: operator-title
`, `
models:
  aliases:
    project-title: title-model
  slots:
    title: project-title
`, true)
	cfg = foldOperatorModelSlots(cfg)
	cfg = foldProjectModelBindings(cfg, captureCLIModelKeys(Config{}))
	cfg.Model = mainModel
	if got, ok := resolveSlotModel(cfg, slotTitle, mainModel); !ok || got != titleModel {
		t.Fatalf("resolveSlotModel(title) = (%q, %v), want (%q, true)", got, ok, titleModel)
	}
	provider := mockllm.New()
	reg := regForTest(provider, providerOpenAI, mainModel)
	eligible := titleGenerationEligible(cfg, reg)
	if eligible == nil || !eligible(server.ProviderSelector{}) {
		t.Fatal("an alias-resolved title slot must be eligible on the fixed default provider")
	}

	factory := sessionEngineFactory(cfg, reg, provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "/ws", session.ModeDefault)
	if err != nil {
		t.Fatalf("session engine factory: %v", err)
	}
	defer func() { _ = result.Close() }()
	if result.ModelID != mainModel {
		t.Fatalf("title slot changed main session model to %q, want %q", result.ModelID, mainModel)
	}
}
