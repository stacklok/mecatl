package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type countObserver struct{ calls atomic.Int64 }

func (o *countObserver) Observe(context.Context, learning.Trajectory) error {
	o.calls.Add(1)
	return nil
}

func TestLearningAdmissionIsGlobalAcrossConcurrentProviderObservers(t *testing.T) {
	admission := newLearningAdmission(3)
	a, b := &countObserver{}, &countObserver{}
	observers := []learning.Observer{newAdmittedObserver(a, admission), newAdmittedObserver(b, admission)}
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := observers[i%len(observers)].Observe(context.Background(), learning.Trajectory{}); err != nil {
				t.Errorf("Observe: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if got := a.calls.Load() + b.calls.Load(); got != 4 {
		t.Fatalf("globally admitted calls = %d, want 4 (calls 1,4,7,10)", got)
	}
}

func learningResolverConfig(t *testing.T, operator, workspace string) Config {
	t.Helper()
	cfg := Config{Workspace: workspace, PermissionsConventional: true}
	if operator != "" {
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := os.WriteFile(path, []byte("learning:\n  mode: "+operator+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.PermissionConfigs = []string{path}
	}
	cfg.permResolver = buildPermResolver(cfg)
	return cfg
}

func TestFoldLearningModeDefaultAndOperator(t *testing.T) {
	got, err := foldLearningMode(learningResolverConfig(t, "", ""))
	if err != nil || got.LearningMode != learning.Off {
		t.Fatalf("default = %s, %v", got.LearningMode, err)
	}
	got, err = foldLearningMode(learningResolverConfig(t, "auto", ""))
	if err != nil || got.LearningMode != learning.Auto {
		t.Fatalf("operator = %s, %v", got.LearningMode, err)
	}
}

func TestFoldLearningModeHeadlessUsesSamePolicy(t *testing.T) {
	cfg := learningResolverConfig(t, "auto", "")
	cfg.Headless = true
	got, err := foldLearningMode(cfg)
	if err != nil || got.LearningMode != learning.Auto {
		t.Fatalf("headless mode = %s, %v", got.LearningMode, err)
	}
}

func TestFoldLearningModeProjectRequiresAdmissionAndCannotRaise(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		for _, tc := range []struct {
			project  string
			admitted learning.Mode
		}{{"review", learning.Review}, {"off", learning.Off}, {"auto", learning.Auto}} {
			t.Run(fmt.Sprintf("trusted=%t/project=%s", trusted, tc.project), func(t *testing.T) {
				root := t.TempDir()
				if err := os.MkdirAll(filepath.Join(root, ".mecatl"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".mecatl", "settings.yaml"), []byte("learning:\n  mode: "+tc.project+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg := learningResolverConfig(t, "auto", root)
				cfg.TrustProject = trusted
				cfg.permResolver = buildPermResolver(cfg)
				got, err := foldLearningMode(cfg)
				want := learning.Auto
				if trusted {
					want = tc.admitted
				}
				if err != nil || got.LearningMode != want {
					t.Fatalf("mode = %s, %v; want %s", got.LearningMode, err, want)
				}
			})
		}
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mecatl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mecatl", "settings.yaml"), []byte("learning:\n  mode: auto\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := learningResolverConfig(t, "review", root)
	cfg.TrustProject = true
	cfg.permResolver = buildPermResolver(cfg)
	got, err := foldLearningMode(cfg)
	if err != nil || got.LearningMode != learning.Review {
		t.Fatalf("raised mode = %s, %v", got.LearningMode, err)
	}
}

func TestLegacyUserModelReviewProjectsToAutoAndConflicts(t *testing.T) {
	cfg := learningResolverConfig(t, "", "")
	cfg.UserModelReview = true
	got, err := foldLearningMode(cfg)
	if err != nil || got.LearningMode != learning.Auto {
		t.Fatalf("legacy = %s, %v", got.LearningMode, err)
	}
	cfg = learningResolverConfig(t, "review", "")
	cfg.UserModelReview = true
	if _, err := foldLearningMode(cfg); err == nil {
		t.Fatal("legacy flag + review should conflict")
	}
	cfg = learningResolverConfig(t, "auto", "")
	cfg.UserModelReview = true
	if got, err := foldLearningMode(cfg); err != nil || got.LearningMode != learning.Auto {
		t.Fatalf("legacy flag + auto = %s, %v", got.LearningMode, err)
	}
}

func TestBuildSharesLearningAdmissionAcrossSharedAndSelectedProviderEngines(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	providers := map[string]*mockllm.Provider{}
	built, err := Build(ctx, Config{
		Workspace:               workspace,
		NoSoul:                  true,
		LearningMode:            learning.Auto,
		UserModelDir:            t.TempDir(),
		UserModelReviewInterval: 2,
		envDetector: fakeEnv(map[string]string{
			"OPENAI_API_KEY":     "test-key",
			"OPENROUTER_API_KEY": "test-key",
		}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			var turns []mockllm.Turn
			switch id {
			case providerOpenAI:
				turns = []mockllm.Turn{mockllm.TextTurn("default completion"), mockllm.TextTurn("default review")}
			case providerOpenRouter:
				turns = []mockllm.Turn{mockllm.TextTurn("selected completion 1"), mockllm.TextTurn("selected completion 2"), mockllm.TextTurn("selected review")}
			default:
				turns = []mockllm.Turn{mockllm.TextTurn("unused")}
			}
			p := mockllm.New(turns...)
			providers[id] = p
			return p
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()

	defaultSession, err := built.Service.CreateSession(ctx, workspace, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatalf("CreateSession(default): %v", err)
	}
	defaultRun, err := built.Service.StartRun(ctx, defaultSession.ID, "first")
	if err != nil {
		t.Fatalf("StartRun(default): %v", err)
	}
	_ = drainRun(defaultRun)
	if got := providers[providerOpenAI].Calls(); got != 2 {
		t.Fatalf("default provider calls after admitted completion = %d, want 2 (run + review)", got)
	}

	runSelected := func(prompt string) {
		t.Helper()
		sess, createErr := built.Service.CreateSessionWithProvider(ctx, workspace, session.ModeDefault, defaultLimits(), server.ProviderSelector{ProviderID: providerOpenRouter})
		if createErr != nil {
			t.Fatalf("CreateSessionWithProvider: %v", createErr)
		}
		run, runErr := built.Service.StartRun(ctx, sess.ID, prompt)
		if runErr != nil {
			t.Fatalf("StartRun(selected): %v", runErr)
		}
		_ = drainRun(run)
	}

	runSelected("second")
	if got := providers[providerOpenRouter].Calls(); got != 1 {
		t.Fatalf("selected provider calls after globally skipped completion = %d, want 1 (no reviewer call)", got)
	}
	runSelected("third")
	if got := providers[providerOpenRouter].Calls(); got != 3 {
		t.Fatalf("selected provider calls after next global admission = %d, want 3 (two runs + one review)", got)
	}
}

func TestLearningObserverUsesSelectedProviderAndModelWindow(t *testing.T) {
	const (
		defaultID     = "default"
		selectedID    = "selected"
		selectedModel = "selected/model"
	)
	defaultProvider := mockllm.New(mockllm.TextTurn("wrong provider"))
	selectedProvider := mockllm.New(mockllm.TextTurn("review done"))
	reg := twoProviderReg(defaultProvider, defaultID, "default/model", selectedProvider, selectedID)
	cfg := Config{
		LearningMode:   learning.Auto,
		Model:          selectedModel,
		contextWindows: map[string]map[string]int{selectedID: {selectedModel: 444_000}},
	}
	userStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	observer := buildLearningObserver(cfg, reg, selectedID, selectedProvider, userStore, newLearningAdmission(1))
	if observer == nil {
		t.Fatal("auto observer is nil")
	}
	if err := observer.Observe(context.Background(), learning.Trajectory{
		SessionID: "s", Workspace: "/ws", Messages: []session.Message{session.NewUserMessage("I prefer concise answers")},
	}); err != nil {
		t.Fatal(err)
	}
	if defaultProvider.Calls() != 0 || selectedProvider.Calls() != 1 {
		t.Fatalf("provider calls: default=%d selected=%d", defaultProvider.Calls(), selectedProvider.Calls())
	}
	eng := buildUserModelReviewEngine(cfg, reg, selectedID, selectedProvider, userStore)
	if got := eng.ContextWindow(); got != 444_000 {
		t.Fatalf("reviewer context window = %d, want 444000", got)
	}
}

func TestLearningCompositionModesAndAutoWrite(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Off, learning.Review} {
		t.Run(mode.String(), func(t *testing.T) {
			provider := mockllm.New(mockllm.TextTurn("must not run"))
			userStore, err := memory.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			cfg := Config{LearningMode: mode, Model: "model"}
			if got := buildLearningObserver(cfg, regForTest(provider, providerMock, cfg.Model), providerMock, provider, userStore, newLearningAdmission(1)); got != nil {
				t.Fatalf("%s observer must be inert in standard composition", mode)
			}
			if provider.Calls() != 0 {
				t.Fatalf("%s made %d provider calls", mode, provider.Calls())
			}
		})
	}

	userStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(map[string]any{
		"key": "communication", "value": "Prefers concise answers", "description": "answer style",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("c1", memory.RememberUserToolName, args)),
		mockllm.TextTurn("done"),
	)
	cfg := Config{LearningMode: learning.Auto, Model: "model"}
	observer := buildLearningObserver(cfg, regForTest(provider, providerMock, cfg.Model), providerMock, provider, userStore, newLearningAdmission(1))
	if err := observer.Observe(context.Background(), learning.Trajectory{
		SessionID: "s", Workspace: "/ws", Messages: []session.Message{session.NewUserMessage("I prefer concise answers")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := userStore.Recall(context.Background(), "user/communication"); err != nil || !ok {
		t.Fatalf("automatic learning write: ok=%v err=%v", ok, err)
	}
}

func TestUserModelReviewEngineCatalogIsReduced(t *testing.T) {
	userStore, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var request port.LLMRequest
	provider := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(func(got port.LLMRequest) { request = got })},
		mockllm.TextTurn("done"),
	)
	cfg := Config{Model: "review-model"}
	engine := buildUserModelReviewEngine(cfg, regForTest(provider, providerMock, cfg.Model), providerMock, provider, userStore)
	reviewer := agent.NewUserModelReviewer(memstore.New(), engine)
	if err := reviewer.Observe(context.Background(), learning.Trajectory{
		SessionID: "s", Messages: []session.Message{session.NewUserMessage("I prefer concise answers")},
	}); err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != 1 || request.Tools[0].Name != memory.RememberUserToolName {
		t.Fatalf("review request tools = %v, want only %s", request.Tools, memory.RememberUserToolName)
	}
}

func TestUserModelConsolidationRequiresIntervalStoreAndProviderNotLearningMode(t *testing.T) {
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(mockllm.TextTurn("unused"))
	if started := startUserModelConsolidation(context.Background(), Config{}, store, provider); started {
		t.Fatal("consolidation started without an interval")
	}
	if started := startUserModelConsolidation(context.Background(), Config{
		UserModelConsolidateInterval: time.Hour,
	}, nil, provider); started {
		t.Fatal("consolidation started without a store")
	}
	if started := startUserModelConsolidation(context.Background(), Config{
		UserModelConsolidateInterval: time.Hour,
	}, store, nil); started {
		t.Fatal("consolidation started without a provider")
	}

	for _, mode := range []learning.Mode{learning.Off, learning.Review, learning.Auto} {
		ctx, cancel := context.WithCancel(context.Background())
		if started := startUserModelConsolidation(ctx, Config{
			LearningMode: mode, UserModelConsolidateInterval: time.Hour,
		}, store, provider); !started {
			t.Errorf("consolidation did not start in %s mode", mode)
		}
		cancel()
	}
}

func TestProjectLearningOffCannotSuppressUserModelConsolidation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mecatl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mecatl", "settings.yaml"), []byte("learning:\n  mode: off\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := learningResolverConfig(t, "auto", root)
	cfg.TrustProject = true
	cfg.UserModelConsolidateInterval = time.Hour
	cfg.permResolver = buildPermResolver(cfg)
	cfg, err := foldLearningMode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LearningMode != learning.Off {
		t.Fatalf("effective project learning mode = %s, want off", cfg.LearningMode)
	}
	store, err := memory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if started := startUserModelConsolidation(ctx, cfg, store, mockllm.New(mockllm.TextTurn("unused"))); !started {
		t.Fatal("trusted project learning.mode: off suppressed the explicitly scheduled process-wide consolidator")
	}
}

func TestDreamAndSkillDraftDoNotRaiseLearningMode(t *testing.T) {
	cfg := learningResolverConfig(t, "off", "")
	cfg.UserModelConsolidateInterval = 1
	cfg.SkillsDraftDir = t.TempDir()
	got, err := foldLearningMode(cfg)
	if err != nil || got.LearningMode != learning.Off {
		t.Fatalf("mode = %s, %v", got.LearningMode, err)
	}
}
