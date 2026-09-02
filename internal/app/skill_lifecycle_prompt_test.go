package app

import (
	"context"
	"os"
	"path/filepath"
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
	factory := sessionEngineFactory(Config{Model: model, SkillsDraftDir: t.TempDir(), LearningMode: learning.Auto, SkillActivationPolicy: learning.SkillActivationValidated, operatorLearningMode: learning.Auto, operatorLearningSensitivity: learning.Balanced, operatorSkillActivationPolicy: learning.SkillActivationValidated}, regForTest(provider, providerOpenAI, model), provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, assets, nil)
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	sess := session.New("s", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	run := result.Engine.Run(context.Background(), sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi"})
	for range run.Events() {
	}
	var description string
	for _, spec := range captured.Tools {
		if spec.Name == "SkillDraft" {
			description = spec.Description
		}
	}
	for _, clause := range []string{"versioned agent-owned DRAFT", "completed-trajectory reflection", "direct SkillDraft", "inactive/legacy", "validated/evaluated"} {
		if !strings.Contains(description, clause) {
			t.Errorf("SkillDraft description missing %q", clause)
		}
	}
	for _, clause := range []string{"AUTOMATIC LEARNED-SKILL POLICY", "perform and verify", "completed-trajectory learning materializes", "Do not call SkillDraft as an activation shortcut", "activation=validated"} {
		if !strings.Contains(captured.System.StablePrefix, clause) {
			t.Errorf("learning StablePrefix missing %q", clause)
		}
	}
}

func TestPerSessionFactoryPromptUsesTrustedProjectTightenedSkillActivation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mecatl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mecatl", "settings.yaml"), []byte("learning:\n  skills:\n    activation: evaluated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var captured port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { captured = request })}, mockllm.TextTurn("ok"))
	const model = "gpt-5"
	cfg := Config{Model: model, Workspace: root, TrustProject: true, PermissionsConventional: true, operatorLearningMode: learning.Auto, operatorLearningSensitivity: learning.Balanced, operatorSkillActivationPolicy: learning.SkillActivationValidated, Diagnostics: port.NopDiagnostics{}}
	cfg.permResolver = buildPermResolver(cfg)
	factory := sessionEngineFactory(cfg, regForTest(provider, providerOpenAI, model), provider, memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	result, err := factory(context.Background(), server.ProviderSelector{}, nil, server.ProfileDefault, root, session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	sess := session.New("s-tight", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: root, Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	for range result.Engine.Run(context.Background(), sess, memEnvironment(root), agent.RunRequest{Text: "hi"}).Events() {
	}
	if !strings.Contains(captured.System.StablePrefix, "activation=evaluated") || strings.Contains(captured.System.StablePrefix, "activation=validated") {
		t.Fatalf("tightened StablePrefix missing evaluated contract:\n%s", captured.System.StablePrefix)
	}
}
