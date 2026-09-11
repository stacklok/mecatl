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
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestADR_0281_EngineSystemPromptContainsTempScopeContract pins the declared
// lifecycle affordance in the factory-built Role layer, rather than merely the
// Shell tool inventory where a duplicated description would make this vacuous.
func TestADR_0281_EngineSystemPromptContainsTempScopeContract(t *testing.T) {
	ctx := context.Background()
	var captured prompt.Layered
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { captured = req.System })}, mockllm.TextTurn("done"))
	cfg := Config{Model: "gpt-5", Shell: "/bin/sh"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	store := memstore.New()
	factory := sessionEngineFactory(cfg, reg, provider, store, permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil, prompt.RootAssembler{}, catalogAssets{}, nil)
	built, err := factory(ctx, server.ProviderSelector{}, nil, server.ProfileDefault, "", session.ModeDefault)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = built.Close() }()
	sess := session.New("scope-prompt", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 1}, time.Now())
	for range built.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "hi"}).Events() {
	}
	for _, clause := range []string{"managed storage is disposable", "temp_scope: system", "not a filesystem sandbox"} {
		if !strings.Contains(captured.StablePrefix, clause) {
			t.Fatalf("StablePrefix missing %q:\n%s", clause, captured.StablePrefix)
		}
	}
}

// TestADR_0281_SystemModeIsRollbackSwitch pins that operator-selected system
// mode supplies the configured system directory and never allocates a managed
// lease, regardless of a per-call managed request.
func TestADR_0281_SystemModeIsRollbackSwitch(t *testing.T) {
	workspace := t.TempDir()
	systemTemp := t.TempDir()
	runner := buildCommandRunnerForRoot(Config{Workspace: workspace, Shell: "/bin/sh", temporaryStorage: temporaryStorageConfig{Mode: temporaryStorageSystem, SystemTempDir: systemTemp}}, workspace)
	if runner == nil {
		t.Fatal("system-mode command runner is unavailable")
	}
	scoped, ok := runner.(tool.CommandTemporaryScopeRunner)
	if !ok {
		t.Fatal("system-mode runner does not support temporary scope selection")
	}
	res, err := scoped.RunWithTemporaryScope(context.Background(), `printf '%s\n%s' "$TMPDIR" "$GOTMPDIR"`, tool.TemporaryScopeManaged)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	if got := strings.TrimSpace(res.Stdout); got != systemTemp+"\n"+systemTemp {
		t.Fatalf("system mode temp overlay = %q, want configured %q", got, systemTemp)
	}
}
