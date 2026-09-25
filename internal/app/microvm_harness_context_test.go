package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func microVMHarnessConfig(t *testing.T, daemon *placementTestDaemon, source string) Config {
	t.Helper()
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{source}, Mode: "combine"}
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{source}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{source}, Kinds: kinds})
	cfg.DefaultPlacement, cfg.DefaultPlacementSet = PlacementMicroVMLocal, true
	cfg.MicroVMReadyRequest = func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
		return microvmmanager.ReadyRequest{}, nil
	}
	cfg.MicroVMManagerFactory = func() (MicroVMReadyManager, string, error) {
		return &placementReadyManager{endpoint: daemon.endpoint()}, daemon.endpoint(), nil
	}
	return cfg
}

func TestMicroVMPlacementFixtureInvalidatesDetachedAndStaleClaims(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg, err := ConfigureExecution(microVMHarnessConfig(t, daemon, "repository"))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := cfg.PlacementProvider.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: cfg.PlacementScope, Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	daemon.writeGuestFile(t, "logical-1", "marker", "DURABLE")
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Environment.Workspace().Read(t.Context(), "marker"); err == nil {
		t.Fatal("detached handle still reads")
	}
	reattach := cfg.PlacementProvider.(server.PlacementReattacher)
	stale := binding.Ref
	stale.Revision = "8"
	if _, err := reattach.Reattach(t.Context(), server.PlacementReattachRequest{Ref: stale, Scope: cfg.PlacementScope}); err == nil {
		t.Fatal("stale generation accepted")
	}
	fresh, err := reattach.Reattach(t.Context(), server.PlacementReattachRequest{Ref: binding.Ref, Scope: cfg.PlacementScope})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if data, err := fresh.Environment.Workspace().Read(t.Context(), "marker"); err != nil || string(data) != "DURABLE" {
		t.Fatalf("resolve failed to restore durable worktree: %q, %v", data, err)
	}
}

func TestMicroVMDirectBuildResolvesExecutionBeforeSources(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing release", true: "lazy readiness failure"}[ready], func(t *testing.T) {
			cfg := Config{Workspace: t.TempDir(), UseMock: true, PermissionConfigs: []string{writeOperatorSettingsFile(t, "execution: {default_placement: microvm-local}")}}
			var readiness atomic.Int32
			if ready {
				cfg.MicroVMReadyRequest = func(microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, error) {
					readiness.Add(1)
					return microvmmanager.ReadyRequest{}, errors.New("release unavailable")
				}
				cfg.MicroVMManagerFactory = func() (MicroVMReadyManager, string, error) { return &executionReadyManager{}, "unix:///unused", nil }
			}
			built, err := buildIsolated(t, t.Context(), cfg)
			if !ready {
				if err == nil || !strings.Contains(err.Error(), "release readiness configuration") {
					t.Fatalf("Build silently fell back: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			if readiness.Load() != 0 {
				t.Fatal("Build provisioned execution")
			}
			if _, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err == nil {
				t.Fatal("readiness failure fell back to host")
			}
			if readiness.Load() != 1 {
				t.Fatal("creation did not reach configured readiness")
			}
		})
	}
}

func TestMicroVMRestartReattachUsesConfiguredReadiness(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg := microVMHarnessConfig(t, daemon, "repository")
	cfg.PermissionConfigs = nil
	cfg.StoreDir = t.TempDir()
	manager := &placementReadyManager{endpoint: daemon.endpoint()}
	cfg.MicroVMManagerFactory = func() (MicroVMReadyManager, string, error) {
		return manager, daemon.endpoint(), nil
	}

	first, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := first.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if manager.calls != 1 {
		first.Close()
		t.Fatalf("initial readiness calls = %d, want 1", manager.calls)
	}
	first.Close()

	second, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	assertSuccessfulRun(t, second.Service, sess.ID, "continue")
	if manager.calls != 2 {
		t.Fatalf("restart readiness calls = %d, want 2", manager.calls)
	}
}

func TestMicroVMConfigurationCopiesDelegationMaps(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	base := microVMHarnessConfig(t, daemon, "repository")
	base.EnvironmentForkers = make(map[session.EnvironmentKind]tool.EnvironmentForker)
	base.EnvironmentMergers = make(map[session.EnvironmentKind]tool.EnvironmentMerger)
	first, err := ConfigureExecution(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ConfigureExecution(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlacementProvider == second.PlacementProvider {
		t.Fatal("fixture did not create separate clients")
	}
	if first.EnvironmentForkers["microvm"] != first.PlacementProvider.(tool.EnvironmentForker) || first.EnvironmentMergers["microvm"] != first.PlacementProvider.(tool.EnvironmentMerger) {
		t.Fatal("second configuration rerouted first client's children")
	}
	if len(base.EnvironmentForkers) != 0 || len(base.EnvironmentMergers) != 0 {
		t.Fatal("configuration mutated input maps")
	}
}

func TestMicroVMIndependentContextIgnoresGuestAndSurvivesUnavailableExecution(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg := microVMHarnessConfig(t, daemon, "independent")
	source := memfs.NewWorkspace("/independent")
	for path, body := range map[string]string{"AGENTS.md": "INDEPENDENT-INSTRUCTION", ".mecatl/commands/which.md": "INDEPENDENT-COMMAND"} {
		if _, err := source.CreateFile(t.Context(), path, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "independent", Provenance: HarnessProvenancePolicy{Fixed: "project"}, Bind: func(_ context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		if scope.AcquireExecutionWorkspace != nil {
			t.Error("independent source received acquisition")
		}
		return prompt.RootAssembler{Source: source}, nil, nil
	}}}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "independent", Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return prompt.NewDirCommandExpander(source), nil, nil
	}}}
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("guest"), mockllm.TextTurn("no fs"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	guest, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	daemon.writeGuestFile(t, "logical-1", "AGENTS.md", "POISON-GUEST-INSTRUCTION")
	daemon.writeGuestFile(t, "logical-1", ".mecatl/commands/which.md", "POISON-GUEST-COMMAND")
	daemon.writeGuestFile(t, "logical-1", ".mecatl/commands/poison.md", "POISON-ONLY")
	assertSuccessfulRun(t, built.Service, guest.ID, "/which")
	// Independent discovery must still work when exact guest acquisition is revoked.
	daemon.mu.Lock()
	daemon.rejectResolve = true
	daemon.mu.Unlock()
	noFS, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []session.SessionID{guest.ID, noFS.ID} {
		commands, err := built.Service.ListCommandsForSession(t.Context(), id)
		if err != nil || len(commands) != 1 || commands[0].Name != "which" || commands[0].Description != "INDEPENDENT-COMMAND" {
			t.Fatalf("commands: %+v, %v", commands, err)
		}
	}
	assertSuccessfulRun(t, built.Service, noFS.ID, "/which")
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for _, request := range requests {
		text := harnessRequestText(request)
		if !strings.Contains(text, "INDEPENDENT-INSTRUCTION") || !strings.Contains(text, "INDEPENDENT-COMMAND") || strings.Contains(text, "POISON-") {
			t.Fatalf("wrong source: %s", text)
		}
	}
	for _, spec := range requests[1].Tools {
		switch spec.Name {
		case "Read", "Write", "Edit", "Shell":
			t.Fatalf("no-FS gained %s", spec.Name)
		}
	}
	if daemon.operationCount("resolve") != 0 || daemon.operationCount("workspace") != 0 {
		t.Fatal("independent sources acquired/read guest files")
	}
}

func TestMicroVMCancelledUnpublishedSourceReleasesOnlyAttempt(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg, err := ConfigureExecution(microVMHarnessConfig(t, daemon, "repository"))
	if err != nil {
		t.Fatal(err)
	}
	owner, err := cfg.PlacementProvider.Bind(t.Context(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: cfg.PlacementScope, Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	daemon.writeGuestFile(t, "logical-1", "AGENTS.md", "RETAINED-OWNER")
	attemptCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var built *Built
	original := cfg.HarnessInstructionSources[0].Bind
	var attempts atomic.Int32
	cfg.HarnessInstructionSources[0].Bind = func(ctx context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		if _, err := built.Service.GetSession(t.Context(), scope.SessionID); !errors.Is(err, server.ErrNotFound) {
			t.Errorf("source acquired after publication: %v", err)
		}
		ws, release, err := scope.AcquireExecutionWorkspace(ctx)
		if err != nil {
			return nil, nil, err
		}
		defer release()
		if _, ok := ws.(tool.CommandRunner); ok {
			t.Error("source exposed runner")
		}
		if _, ok := ws.(tool.WorkspaceNamespace); ok {
			t.Error("source exposed namespace mutation")
		}
		_, version, err := ws.ReadVersion(ctx, "AGENTS.md")
		if err != nil {
			return nil, nil, err
		}
		if _, err := ws.CreateFile(ctx, "forbidden", []byte("x")); !errors.Is(err, tool.ErrFileOperationUnsupported) {
			t.Errorf("source CreateFile: %v", err)
		}
		if _, err := ws.ReplaceFile(ctx, "AGENTS.md", version, []byte("x")); !errors.Is(err, tool.ErrFileOperationUnsupported) {
			t.Errorf("source ReplaceFile: %v", err)
		}
		if _, read, err := owner.Environment.ReadLedger().RecordedVersion(ctx, tool.LedgerKey("/workspace", "AGENTS.md")); read || err != nil {
			t.Errorf("source polluted execution ledger: %v %v", read, err)
		}
		source, cleanup, err := original(ctx, scope)
		if attempts.Add(1) == 1 {
			cancel()
		}
		return source, cleanup, err
	}
	built, err = buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	reattach := cfg.PlacementProvider.(server.PlacementReattacher)
	borrow := func() server.PlacementBinding {
		b, err := reattach.Reattach(t.Context(), server.PlacementReattachRequest{Ref: owner.Ref, Scope: cfg.PlacementScope})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if _, err := built.Service.CreateSessionWithProfile(attemptCtx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("unpublished"), server.WithPlacementBinding(borrow())); err == nil {
		t.Fatal("cancelled setup published")
	}
	if daemon.operationCount("delete") != 0 {
		t.Fatal("cancelled attempt destructively deleted retained execution")
	}
	if data, err := owner.Environment.Workspace().Read(t.Context(), "AGENTS.md"); err != nil || string(data) != "RETAINED-OWNER" {
		t.Fatalf("retained owner lost: %q, %v", data, err)
	}
	created, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("unpublished"), server.WithPlacementBinding(borrow()))
	if err != nil {
		t.Fatalf("same-ID retry: %v", err)
	}
	built.Service.CloseSession(created.ID)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if daemon.operationCount("detach") < 1 {
		t.Fatal("cancelled attempt or retry did not release source borrows")
	}
}

func TestMicroVMChildRetainsParentSourceAfterRetirement(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg, err := ConfigureExecution(microVMHarnessConfig(t, daemon, "repository"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := &harnessCommandResolver{}
	cfg.harnessResolver = resolver
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	parent, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	daemon.writeGuestFile(t, "logical-1", "AGENTS.md", "PARENT-SOURCE")
	binding, release, err := resolver.Borrow(t.Context(), parent.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	resolved := binding.(*resolvedCommandBinding)
	cfg.harnessInstructions = generationInstructions{harnessGeneration: resolved.generation, source: resolved.context.harnessInstructions}
	arrived, resume := make(chan port.LLMRequest, 2), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	var calls atomic.Int32
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		arrived <- req
		if calls.Add(1) == 1 {
			<-resume
		}
	})}, mockllm.TextTurn("child"), mockllm.TextTurn("after retirement"))
	eng := buildChildEngine(cfg, regForTest(llm, providerMock, "m"), llm, providerMock, "m", nil)
	parentBorrow, err := cfg.PlacementProvider.(server.PlacementReattacher).Reattach(t.Context(), server.PlacementReattachRequest{Ref: parent.EnvironmentRef, Scope: cfg.PlacementScope})
	if err != nil {
		t.Fatal(err)
	}
	childEnv, cleanup, _, err := cfg.EnvironmentForkers["microvm"].Fork(t.Context(), parentBorrow.Environment, "held-child")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := parentBorrow.Close(); err != nil {
		t.Fatal(err)
	}
	daemon.writeGuestFile(t, "logical-2", "AGENTS.md", "POISON-CHILD")
	child := session.New("held-child", session.ModeDefault, childEnv.Ref(), session.Limits{}, parent.CreatedAt)
	run := eng.Run(t.Context(), child, childEnv, agent.RunRequest{Text: "inspect"})
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not reach provider")
	}
	built.Service.CloseSession(parent.ID)
	release()
	next := session.New("held-child-next", session.ModeDefault, childEnv.Ref(), session.Limits{}, parent.CreatedAt)
	for range eng.Run(t.Context(), next, childEnv, agent.RunRequest{Text: "read after retirement"}).Events() {
	}
	select {
	case req := <-arrived:
		if text := harnessRequestText(req); !strings.Contains(text, "PARENT-SOURCE") || strings.Contains(text, "POISON-CHILD") {
			t.Fatalf("child source changed: %s", text)
		}
	default:
		t.Fatal("retired parent's source failed through real MicroVM workspace")
	}
	close(resume)
	for range run.Events() {
	}
	if !eventually(5*time.Second, func() bool { return daemon.operationCount("detach") >= 1 }) {
		t.Fatal("drained child did not release source attachment")
	}
}

func TestMicroVMSuccessorRetainsPlacementAndContext(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(map[bool]string{false: "default context", true: "repository context"}[selected], func(t *testing.T) {
			daemon := startPlacementTestDaemon(t)
			cfg := microVMHarnessConfig(t, daemon, "repository")
			if !selected {
				cfg.PermissionConfigs = nil
			}
			cfg.Shell, cfg.AllowAllTools = "/bin/sh", true
			var requests []port.LLMRequest
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
				mockllm.ToolCallTurn(session.NewToolCall("still-attached", "Shell", json.RawMessage(`{"command":"test -f AGENTS.md"}`))), mockllm.TextTurn("successor"))
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			parent, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			daemon.writeGuestFile(t, "logical-1", "AGENTS.md", "SUCCESSOR-CONTEXT")
			successor, err := built.Service.ForkSessionSuccessor(t.Context(), server.ForkSuccessorRequest{Source: parent.ID})
			if err != nil {
				t.Fatal(err)
			}
			built.Service.CloseSession(parent.ID)
			before := daemon.operationCount("resolve")
			assertSuccessfulRun(t, built.Service, successor, "continue")
			if daemon.operationCount("resolve") != before {
				t.Fatal("successor lost its placement and had to reacquire")
			}
			if selected && (len(requests) != 2 || !strings.Contains(harnessRequestText(requests[0]), "SUCCESSOR-CONTEXT")) {
				t.Fatal("successor lost selected context")
			}
			built.Service.CloseSession(successor)
			if daemon.operationCount("detach") < 1 {
				t.Fatal("successor did not release placement acquisitions")
			}
		})
	}
}

func TestMicroVMSelectedScheduleAndRestartReauthorizeContext(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	daemon.seed = func(root string) error {
		if err := os.MkdirAll(filepath.Join(root, ".mecatl/commands"), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("INSTRUCTIONS-"+filepath.Base(root)), 0o600); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, ".mecatl/commands/which.md"), []byte("COMMAND-"+filepath.Base(root)), 0o600)
	}
	cfg := microVMHarnessConfig(t, daemon, "repository")
	cfg.StoreDir, cfg.SchedulerEnabled, cfg.SchedulerTickInterval = t.TempDir(), true, time.Hour
	cfg.SessionLeaseTTL = 100 * time.Millisecond
	var requests []port.LLMRequest
	build := func(config Config) *Built {
		t.Helper()
		config.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })}, mockllm.TextTurn("fire"), mockllm.TextTurn("resume"))
		b, err := buildIsolated(t, t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first := build(cfg)
	parent, err := first.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	schedule, err := first.Service.CreateSchedule(t.Context(), port.ScheduleSpec{Name: "selected", Prompt: "/which", Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, Mutating: true})
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	fire, err := first.Service.FireNow(t.Context(), schedule.Spec.Name)
	if err != nil || fire.Stop != session.StopEndTurn {
		first.Close()
		t.Fatalf("fire=%+v, %v", fire, err)
	}
	if len(requests) != 1 || !strings.Contains(harnessRequestText(requests[0]), "INSTRUCTIONS-logical-2") || !strings.Contains(harnessRequestText(requests[0]), "COMMAND-logical-2") || strings.Contains(harnessRequestText(requests[0]), "INSTRUCTIONS-logical-1") {
		first.Close()
		t.Fatal("fire did not use its exact unpublished placement")
	}
	first.Close()
	second := build(cfg)
	assertSuccessfulRun(t, second.Service, parent.ID, "/which")
	if !strings.Contains(harnessRequestText(requests[1]), "INSTRUCTIONS-logical-1") {
		t.Fatal("restart failed exact source reattachment")
	}
	second.Close()
	// Rebind the existing session to current independent policy, without resolving execution.
	localCfg := cfg
	localCfg.PermissionConfigs = microVMHarnessConfig(t, daemon, "local").PermissionConfigs
	localCfg.EnableCommands = true
	if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte("CURRENT-LOCAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Workspace, ".mecatl/commands"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Workspace, ".mecatl/commands/which.md"), []byte("CURRENT-COMMAND"), 0o600); err != nil {
		t.Fatal(err)
	}
	third := build(localCfg)
	before := daemon.operationCount("resolve")
	commands, err := third.Service.ListCommandsForSession(t.Context(), parent.ID)
	if err != nil || len(commands) != 1 || commands[0].Description != "CURRENT-COMMAND" || daemon.operationCount("resolve") != before {
		third.Close()
		t.Fatalf("restart discovery ignored current independent policy: %+v, %v", commands, err)
	}
	assertSuccessfulRun(t, third.Service, parent.ID, "/which")
	if text := harnessRequestText(requests[2]); !strings.Contains(text, "CURRENT-LOCAL") || strings.Contains(text, "INSTRUCTIONS-logical-1") {
		t.Fatal("restart retained previous context policy")
	}
	third.Close()
	daemon.mu.Lock()
	daemon.rejectResolve = true
	daemon.mu.Unlock()
	fourth := build(cfg)
	defer fourth.Close()
	if _, err := fourth.Service.ListCommandsForSession(t.Context(), parent.ID); err == nil {
		t.Fatal("revoked repository source fell back to local")
	}
	before = daemon.operationCount("resolve")
	if !eventually(5*time.Second, func() bool {
		fire, err = fourth.Service.FireNow(t.Context(), schedule.Spec.Name)
		return daemon.operationCount("resolve") > before
	}) {
		t.Fatalf("revoked fire never reached exact acquisition: %+v, %v", fire, err)
	}
	if err == nil && fire.Stop == session.StopEndTurn {
		t.Fatal("revoked scheduled source ran")
	}
	if daemon.operationCount("create") != 2 {
		t.Fatal("restart or source failure allocated substitute placement")
	}
}

func TestMicroVMRepositorySelectionDoesNotGrantProjectAdmission(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg := microVMHarnessConfig(t, daemon, "repository")
	cfg.TrustProject = false
	cfg.Posture, cfg.PostureFlagSet = PostureAuto, true
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if _, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	if daemon.operationCount("resolve") != 0 {
		t.Fatal("selection or headless posture granted repository admission")
	}
}

func TestMicroVMRequiredRepositorySourceFailsForNoFS(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg := microVMHarnessConfig(t, daemon, "repository")
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if _, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS); err == nil {
		t.Fatal("required repository source invented no-FS storage")
	}
	if daemon.operationCount("create") != 0 || daemon.operationCount("resolve") != 0 {
		t.Fatal("no-FS touched guest placement")
	}
}

func TestMicroVMSelectedContextFreshnessAndReadEvidence(t *testing.T) {
	daemon := startPlacementTestDaemon(t)
	cfg := microVMHarnessConfig(t, daemon, "repository")
	cfg.AllowAllTools = true
	diag := newCapturingDiagnostics()
	cfg.Diagnostics = diag
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) { requests = append(requests, r) })},
		mockllm.TextTurn("one"), mockllm.ToolCallTurn(session.NewToolCall("unread-edit", "Edit", json.RawMessage(`{"path":"AGENTS.md","old_string":"SECOND-INSTRUCTION","new_string":"MUTATED"}`))), mockllm.TextTurn("two"), mockllm.TextTurn("diagnosed read fault"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	daemon.writeGuestFile(t, "logical-1", "AGENTS.md", " \n\t")
	daemon.writeGuestFile(t, "logical-1", "CLAUDE.md", "FALLBACK-INSTRUCTION")
	daemon.writeGuestFile(t, "logical-1", ".mecatl/commands/which.md", "FIRST-COMMAND")
	assertSuccessfulRun(t, built.Service, sess.ID, "/which")
	daemon.writeGuestFile(t, "logical-1", "AGENTS.md", "SECOND-INSTRUCTION")
	daemon.writeGuestFile(t, "logical-1", ".mecatl/commands/which.md", "SECOND-COMMAND")
	commands, err := built.Service.ListCommandsForSession(t.Context(), sess.ID)
	if err != nil || len(commands) != 1 || commands[0].Description != "SECOND-COMMAND" {
		t.Fatalf("stale list: %+v, %v", commands, err)
	}
	events := harnessRun(t, built, t.Context(), sess.ID, "/which")
	denied := false
	for _, ev := range events {
		if ev.ToolResult != nil && ev.ToolResult.CallID == "unread-edit" {
			denied = ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "not read")
		}
	}
	if !denied {
		t.Fatal("source instruction read authorized Edit")
	}
	if len(requests) != 3 {
		t.Fatalf("requests=%d", len(requests))
	}
	first, second := harnessRequestText(requests[0]), harnessRequestText(requests[1])
	if !strings.Contains(first, "FALLBACK-INSTRUCTION") || !strings.Contains(first, "FIRST-COMMAND") {
		t.Fatal("whitespace fallback or expansion missing")
	}
	if !strings.Contains(second, "SECOND-INSTRUCTION") || strings.Contains(second, "FALLBACK-INSTRUCTION") || !strings.Contains(second, "SECOND-COMMAND") {
		t.Fatal("instructions or commands did not refresh")
	}
	// A genuine read error must not turn into CLAUDE fallback or optional absence.
	path := filepath.Join(daemon.root, "logical-1", "AGENTS.md")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	before := len(requests)
	harnessRun(t, built, t.Context(), sess.ID, "read fault")
	if len(requests) != before+1 || strings.Contains(harnessRequestText(requests[before]), "FALLBACK-INSTRUCTION") || !strings.Contains(strings.Join(diag.capturedStrings(), "\n"), "instruction-fragment assembly failed") {
		t.Fatalf("source read error did not preserve diagnosed fail-soft assembly: before=%d after=%d diagnostics=%q", before, len(requests), diag.capturedStrings())
	}
}
