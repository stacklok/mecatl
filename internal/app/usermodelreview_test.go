package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type countObserver struct{ calls atomic.Int64 }

func drainRunToLearningLog(ctx context.Context, t *testing.T, svc *server.Service, id session.SessionID, run interface {
	Events() <-chan session.Event
	Approve(string, session.ApprovalVerdict) error
}) string {
	t.Helper()
	recorder := server.NewRunEventRecorder(ctx, svc, id)
	defer recorder.Close()
	var final string
	for event := range run.Events() {
		recorder.Observe(event)
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
		}
		if event.Type == session.EvResult && event.Result != nil {
			final = event.Result.Text
		}
	}
	return final
}

func (o *countObserver) Observe(context.Context, learning.Trajectory) error {
	o.calls.Add(1)
	return nil
}

func TestLearningAdmissionIsGlobalAcrossConcurrentProviderObservers(t *testing.T) {
	admission := newLearningAdmissionGate(3)
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
	cfg := Config{Workspace: workspace, PermissionsConventional: workspace != ""}
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
	if err != nil || got.LearningMode != learning.Off || got.SkillActivationPolicy != learning.SkillActivationEvaluated {
		t.Fatalf("default = %s/%s, %v", got.LearningMode, got.SkillActivationPolicy, err)
	}
	got, err = foldLearningMode(learningResolverConfig(t, "auto", ""))
	if err != nil || got.LearningMode != learning.Auto || got.SkillActivationPolicy != learning.SkillActivationValidated {
		t.Fatalf("operator = %s/%s, %v", got.LearningMode, got.SkillActivationPolicy, err)
	}
}

func TestFoldLearningSkillActivationExplicitAndProjectTightening(t *testing.T) {
	operatorPath := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(operatorPath, []byte("learning:\n  mode: auto\n  skills:\n    activation: evaluated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{PermissionConfigs: []string{operatorPath}, Diagnostics: port.NopDiagnostics{}}
	cfg.permResolver = buildPermResolver(cfg)
	got, err := foldLearningMode(cfg)
	if err != nil || got.SkillActivationPolicy != learning.SkillActivationEvaluated {
		t.Fatalf("explicit evaluated = %s, %v", got.SkillActivationPolicy, err)
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".mecatl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mecatl", "settings.yaml"), []byte("learning:\n  skills:\n    activation: evaluated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = learningResolverConfig(t, "auto", root)
	cfg.TrustProject = true
	cfg.permResolver = buildPermResolver(cfg)
	got, err = foldLearningMode(cfg)
	if err != nil || got.SkillActivationPolicy != learning.SkillActivationEvaluated {
		t.Fatalf("project tightened = %s, %v", got.SkillActivationPolicy, err)
	}
	cfg.TrustProject = false
	got, err = foldLearningMode(cfg)
	if err != nil || got.SkillActivationPolicy != learning.SkillActivationValidated {
		t.Fatalf("untrusted project = %s, %v", got.SkillActivationPolicy, err)
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

func TestLearningAdmissionIntervalPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := os.WriteFile(path, []byte("learning:\n  admission_interval: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{PermissionConfigs: []string{path}, LearningAdmissionInterval: 1}
	cfg.permResolver = buildPermResolver(cfg)
	got, err := foldLearningMode(cfg)
	if err != nil || got.LearningAdmissionInterval != 7 {
		t.Fatalf("settings interval = %d, %v; want 7", got.LearningAdmissionInterval, err)
	}

	cfg.LearningAdmissionInterval = 0
	cfg.LearningAdmissionIntervalSet = true
	got, err = foldLearningMode(cfg)
	if err != nil || got.LearningAdmissionInterval != 0 {
		t.Fatalf("explicit CLI zero = %d, %v; want 0", got.LearningAdmissionInterval, err)
	}
}

func TestBuildSharesLearningAdmissionAcrossSharedAndSelectedProviderEngines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		workspace := t.TempDir()
		providers := map[string]*mockllm.Provider{}
		built, err := buildIsolated(t, ctx, Config{
			Model:                     "test-model",
			ContextWindowOverride:     defaultContextWindowTokens,
			Workspace:                 workspace,
			NoSoul:                    true,
			LearningMode:              learning.Auto,
			UserModelDir:              t.TempDir(),
			LearningAdmissionInterval: 2,
			envDetector: fakeEnv(map[string]string{
				"OPENAI_API_KEY":     "test-key",
				"OPENROUTER_API_KEY": "test-key",
			}),
			liveModelHTTPClient: offlineHTTPClient(),
			providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
				var turns []mockllm.Turn
				switch id {
				case providerOpenAI:
					turns = []mockllm.Turn{mockllm.TextTurn("default completion"), mockllm.TextTurn(`{"kind":"abstained"}`)}
				case providerOpenRouter:
					turns = []mockllm.Turn{mockllm.TextTurn("selected completion 1"), mockllm.TextTurn(`{"kind":"abstained"}`), mockllm.TextTurn("selected completion 2"), mockllm.TextTurn(`{"kind":"abstained"}`)}
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

		defaultSession, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
		if err != nil {
			t.Fatalf("CreateSession(default): %v", err)
		}
		defaultRun, err := built.Service.StartRun(ctx, defaultSession.ID, "Remember that I prefer concise answers")
		if err != nil {
			t.Fatalf("StartRun(default): %v", err)
		}
		_ = drainRunToLearningLog(ctx, t, built.Service, defaultSession.ID, defaultRun)
		waitCalls := func(provider *mockllm.Provider, want int) int {
			t.Helper()
			deadline := time.Now().Add(2 * time.Second)
			for provider.Calls() < want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			return provider.Calls()
		}
		if got := waitCalls(providers[providerOpenAI], 2); got != 2 {
			t.Fatalf("default provider calls after admitted completion = %d, want 2 (run + review)", got)
		}

		runSelected := func(prompt string) {
			t.Helper()
			sess, createErr := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(), server.ProviderSelector{ProviderID: providerOpenRouter, ModelID: "test-model"})
			if createErr != nil {
				t.Fatalf("CreateSessionWithProvider: %v", createErr)
			}
			run, runErr := built.Service.StartRun(ctx, sess.ID, prompt)
			if runErr != nil {
				t.Fatalf("StartRun(selected): %v", runErr)
			}
			_ = drainRunToLearningLog(ctx, t, built.Service, sess.ID, run)
		}

		runSelected("Remember that I prefer short examples")
		if got := waitCalls(providers[providerOpenRouter], 2); got != 2 {
			t.Fatalf("selected provider calls after hard trigger = %d, want 2 (run + review)", got)
		}
		runSelected("Remember that I prefer Go examples")
		if got := waitCalls(providers[providerOpenRouter], 4); got != 4 {
			t.Fatalf("selected provider calls after second hard trigger = %d, want 4 (two runs + two reviews)", got)
		}
	})
}

func TestStartupProjectOffKeepsAlternateRootAutomaticAssets(t *testing.T) {
	for _, tc := range []struct {
		name string
		max  int
		want int
	}{{"finite", 2, 2}, {"zero disables", 0, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			startupRoot, alternateRoot := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(startupRoot, ".mecatl"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(startupRoot, ".mecatl", "settings.yaml"), []byte("learning:\n  mode: off\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			operator := filepath.Join(t.TempDir(), "settings.yaml")
			body := fmt.Sprintf("models:\n  default_provider: openai\nlearning:\n  mode: auto\n  automatic:\n    cooldown: 0s\n    max_reflections: %d\n", tc.max)
			if err := os.WriteFile(operator, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			provider := mockllm.New(mockllm.TextTurn("completed"), mockllm.TextTurn(`{"kind":"abstained","candidates":[]}`))
			built, err := buildIsolated(t, context.Background(), Config{
				Model: "model", DefaultProvider: providerOpenAI, Workspace: alternateRoot, TrustProject: true, NoSoul: true,
				PermissionConfigs: []string{operator}, PermissionsConventional: true, permConfigEnv: isolatedPermConfigEnv(t),
				UserModelDir: t.TempDir(), envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "test"}), liveModelHTTPClient: offlineHTTPClient(),
				providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
					if id == providerOpenAI {
						return provider
					}
					return mockllm.New(mockllm.TextTurn("unused"))
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, "Remember that I prefer concise answers")
			if err != nil {
				t.Fatal(err)
			}
			_ = drainRunToLearningLog(ctx, t, built.Service, sess.ID, run)
			if tc.want == 2 {
				deadline := time.Now().Add(2 * time.Second)
				for provider.Calls() < 2 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if got := provider.Calls(); got != 2 {
					t.Fatalf("provider calls = %d, want run + alternate-root reflection", got)
				}
			} else {
				time.Sleep(50 * time.Millisecond)
				if got := provider.Calls(); got != 1 {
					t.Fatalf("provider calls = %d, want run only under zero limit", got)
				}
			}
		})
	}
}

func TestExplicitReflectionUsesPersistedSessionProvider(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	userModelDir := t.TempDir()
	providers := map[string]*mockllm.Provider{}
	var requestMu sync.Mutex
	requestModels := map[string][]string{}
	built, err := buildIsolated(t, ctx, Config{
		Model: "default-model", Workspace: workspace, NoSoul: true, LearningMode: learning.Off,
		UserModelDir:        userModelDir,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "test", "OPENROUTER_API_KEY": "test"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			turns := []mockllm.Turn{mockllm.TextTurn("unused")}
			if id == providerOpenRouter {
				turns = []mockllm.Turn{mockllm.TextTurn("selected completion"), mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/output","value":"concise","evidence":["m:0"]}]}`)}
			}
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				requestMu.Lock()
				requestModels[id] = append(requestModels[id], req.Model)
				requestMu.Unlock()
			})}, turns...)
			providers[id] = provider
			return provider
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if _, statErr := os.Stat(filepath.Join(userModelDir, "reflections")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("off mode eagerly initialized reflection repository: %v", statErr)
	}
	sess, err := built.Service.CreateSessionWithProvider(ctx, session.ModeDefault, defaultLimits(), server.ProviderSelector{ProviderID: providerOpenRouter})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "Remember that I prefer concise answers")
	if err != nil {
		t.Fatal(err)
	}
	_ = drainRun(run)
	if _, err = built.Service.ReflectSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(userModelDir, "reflections")); statErr != nil {
		t.Fatalf("explicit reflection did not initialize lazy repository: %v", statErr)
	}
	if got := providers[providerOpenRouter].Calls(); got != 2 {
		t.Fatalf("selected provider calls = %d, want run + reflection", got)
	}
	if got := providers[providerOpenAI].Calls(); got != 0 {
		t.Fatalf("default provider received selected transcript: calls=%d", got)
	}
	requestMu.Lock()
	selectedModels := append([]string(nil), requestModels[providerOpenRouter]...)
	defaultModels := append([]string(nil), requestModels[providerOpenAI]...)
	requestMu.Unlock()
	if len(selectedModels) != 2 || selectedModels[0] != "openai/gpt-5" || selectedModels[1] != "openai/gpt-5" {
		t.Fatalf("selected provider models = %v, want its own non-empty default for run and reflection", selectedModels)
	}
	if len(defaultModels) != 0 {
		t.Fatalf("default provider received requests with models %v", defaultModels)
	}
}

func TestExplicitReflectionAlternateRootStagesButCannotPromoteProjectProposal(t *testing.T) {
	ctx := context.Background()
	alternateRoot := osfsWSForTest(t, t.TempDir()).Root()
	provider := mockllm.New(
		mockllm.TextTurn("completed"),
		mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"project_fact","key":"project/build","value":"task build","evidence":["m:0"]}]}`),
	)
	built, err := buildIsolated(t, ctx, Config{
		Model: "model", Workspace: alternateRoot, TrustProject: true, NoSoul: true,
		LearningMode: learning.Off, UserModelDir: t.TempDir(), MemoryDir: t.TempDir(),
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "test"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerOpenAI {
				return provider
			}
			return mockllm.New(mockllm.TextTurn("unused"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, defaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "Remember that this project uses task build")
	if err != nil {
		t.Fatal(err)
	}
	_ = drainRun(run)
	receipt, err := built.Service.ReflectSession(ctx, sess.ID)
	if err != nil || receipt.GetStaged() != 1 || receipt.GetPromoted() != 0 {
		t.Fatalf("alternate-root reflection receipt=%+v err=%v", receipt, err)
	}
	page, err := built.Service.ListLearningProposals(ctx, "", "", 10, alternateRoot)
	if err != nil || len(page.GetProposals()) != 1 || page.GetProposals()[0].GetStatus() != string(learning.ProposalStaged) {
		t.Fatalf("alternate-root project proposals=%+v err=%v", page.GetProposals(), err)
	}
	proposal := page.GetProposals()[0]
	if !proposal.GetPromotionAvailable() || proposal.GetPromotionUnavailableReason() != "" {
		t.Fatalf("configured-placement proposal unexpectedly unavailable: %+v", proposal)
	}
	if _, err = built.Service.DecideLearningProposal(ctx, proposal.GetId(), proposal.GetVersion(), "approve", "", alternateRoot); err != nil {
		t.Fatalf("configured-placement approval err=%v", err)
	}
}

func TestBuildGRPCReflectionPartitionsVerifiedPrincipals(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	provider := mockllm.New(
		mockllm.TextTurn("alice completed"),
		mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/alice","value":"alice value","evidence":["m:0"]}]}`),
		mockllm.TextTurn("bob completed"),
		mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/bob","value":"bob value","evidence":["m:0"]}]}`),
	)
	built, err := buildIsolated(t, ctx, Config{
		Model: "model", Workspace: workspace, NoSoul: true, OwnershipEnforced: true,
		LearningMode: learning.Off, UserModelDir: t.TempDir(),
		envDetector: fakeEnv(map[string]string{"OPENAI_API_KEY": "test"}), liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, id, _, _ string) port.LLMProvider {
			if id == providerOpenAI {
				return provider
			}
			return mockllm.New(mockllm.TextTurn("unused"))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	handler := server.NewHarnessServer(built.Service)
	principals := []*session.Principal{
		{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser},
		{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser},
	}
	for _, principal := range principals {
		principalCtx := session.WithPrincipal(ctx, principal)
		sess, createErr := built.Service.CreateSession(principalCtx, session.ModeDefault, defaultLimits())
		if createErr != nil {
			t.Fatal(createErr)
		}
		run, runErr := built.Service.StartRun(principalCtx, sess.ID, "Remember that I prefer my exact value")
		if runErr != nil {
			t.Fatal(runErr)
		}
		_ = drainRun(run)
		response, reflectErr := handler.ReflectSession(principalCtx, &mecatlv1.ReflectSessionRequest{SessionId: string(sess.ID)})
		if reflectErr != nil || response.GetReceipt().GetStaged() != 1 || response.GetReceipt().GetPromoted() != 0 {
			t.Fatalf("%s reflection=%+v err=%v", principal.Subject, response, reflectErr)
		}
	}
	for _, principal := range principals {
		principalCtx := session.WithPrincipal(ctx, principal)
		page, listErr := handler.ListLearningProposals(principalCtx, &mecatlv1.ListLearningProposalsRequest{Limit: 10})
		if listErr != nil || len(page.GetProposals()) != 1 || page.GetProposals()[0].GetKey() != "user/"+principal.Subject {
			t.Fatalf("%s partition=%+v err=%v", principal.Subject, page.GetProposals(), listErr)
		}
	}
}

func TestServiceExplicitReflectionReceiptsMatchReviewAndAutoPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode         learning.Mode
		wantPromoted int
		wantStatus   learning.ProposalStatus
	}{
		{learning.Off, 0, learning.ProposalStaged},
		{learning.Review, 0, learning.ProposalStaged},
		{learning.Auto, 1, learning.ProposalPromoted},
	} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			workspace := t.TempDir()
			provider := mockllm.New(mockllm.TextTurn(`{"kind":"proposed","candidates":[{"kind":"operator_fact","key":"user/output","value":"concise","evidence":["m:0"]}]}`))
			cfg := Config{Model: "model", Workspace: workspace, LearningMode: tc.mode}
			if tc.mode == learning.Off && buildReflectionObserver(cfg, provider, testProviderModel(cfg.Model), memmemory.New(), nil, memproposal.New(), nil, nil) != nil {
				t.Fatal("off mode wired an automatic reflection observer")
			}
			repository := memproposal.New()
			memory := memmemory.New()
			observer := buildExplicitReflectionObserver(cfg, provider, testProviderModel(cfg.Model), memory, nil, repository, nil)
			trajectory := learning.NewTrajectory("explicit-policy", workspace, session.StopEndTurn, session.Usage{}, []session.Message{session.NewUserMessage("Remember that I prefer concise output")})
			trajectory.Current = learning.MessageSpan{Start: 0, End: 1}
			receipt, err := observer.Reflect(context.Background(), trajectory, false)
			if err != nil || receipt.Staged != 1 || receipt.Promoted != tc.wantPromoted {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			page, err := repository.List(context.Background(), learning.ProposalPartition{Principal: reflectionPrincipal(nil)}, learning.ProposalList{Limit: 10})
			if err != nil || len(page.Records) != 1 || page.Records[0].Status != tc.wantStatus {
				t.Fatalf("proposals=%+v err=%v", page.Records, err)
			}
			if calls := provider.Calls(); calls != 1 {
				t.Fatalf("provider calls=%d, want one explicit reflection", calls)
			}
		})
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
	observer := buildLearningObserver(cfg, reg, selectedID, selectedProvider, userStore, newLearningAdmissionGate(1))
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
			if got := buildLearningObserver(cfg, regForTest(provider, providerMock, cfg.Model), providerMock, provider, userStore, newLearningAdmissionGate(1)); got != nil {
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
	observer := buildLearningObserver(cfg, regForTest(provider, providerMock, cfg.Model), providerMock, provider, userStore, newLearningAdmissionGate(1))
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

// TestUserModelReviewEngineCascadeCompactorIdentity covers the common child-engine
// deps builder used by buildUserModelReviewEngine. Its Engine retains deps privately,
// so this is the narrow observable seam for the nested compactor attribution.
func TestUserModelReviewEngineCascadeCompactorIdentity(t *testing.T) {
	const (
		providerID = "selected-non-default"
		model      = "user-review-model"
	)
	cfg := Config{Model: model, Compaction: "cascade"}
	deps := childEngineDepsForProvider(cfg, "usermodel-review", mockllm.New(),
		session.ProviderModelID{ProviderID: providerID, ModelID: model},
		func() int { return defaultContextWindowTokens }, nil, prompt.Config{}, nil)
	compactor, ok := deps.Compactor.(agent.CascadeCompactor)
	if !ok {
		t.Fatalf("compactor = %T, want agent.CascadeCompactor", deps.Compactor)
	}
	want := session.ProviderModelID{ProviderID: providerID, ModelID: model}
	if compactor.ProviderModel != want {
		t.Fatalf("cascade ProviderModel = %+v, want %+v", compactor.ProviderModel, want)
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
