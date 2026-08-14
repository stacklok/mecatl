package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestSkillDraftFactorySystemPromptDescribesLifecyclePolicy(t *testing.T) {
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { captured = request })}, mockllm.TextTurn("ok"))
	const model = "gpt-5"
	assets := catalogAssets{learnedSkills: memskill.New(), skillPartition: learning.SkillPartition{Principal: "p"}, skillOwner: "reflection"}
	factory := sessionEngineFactory(Config{Model: model, SkillsDraftDir: t.TempDir()}, regForTest(provider, providerOpenAI, model), provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, assets, nil)
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	sess := session.New("s", session.ModeDefault, "/ws", session.Limits{MaxTurns: 1}, time.Now())
	run := result.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi"})
	for range run.Events() {
	}
	var description string
	for _, spec := range captured.Tools {
		if spec.Name == "SkillDraft" {
			description = spec.Description
		}
	}
	for _, clause := range []string{"versioned agent-owned DRAFT", "Evidence and evaluation are required", "auto activates", "only an evidence-backed PASS"} {
		if !strings.Contains(description, clause) {
			t.Errorf("SkillDraft description missing %q", clause)
		}
	}
}
