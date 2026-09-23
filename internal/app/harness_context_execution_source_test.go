package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/scheduler"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

type harnessExecutionProvider struct {
	mu           sync.Mutex
	bindings     map[session.EnvironmentRef]server.PlacementBinding
	reattaches   []server.PlacementReattachRequest
	sourceCloses atomic.Int32
}

func (p *harnessExecutionProvider) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, binding := range p.bindings {
		return binding, nil
	}
	return server.PlacementBinding{}, server.ErrPlacementUnavailable
}

func (p *harnessExecutionProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reattaches = append(p.reattaches, req)
	binding, ok := p.bindings[req.Ref]
	if !ok {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	workspace := &invalidatingHarnessWorkspace{Workspace: binding.Environment.Workspace()}
	binding.Environment = tool.MustEnvironment(binding.Ref, workspace, binding.Environment.ReadLedger(), binding.Environment.CommandRunner())
	binding.Close = func() error {
		workspace.closed.Store(true)
		p.sourceCloses.Add(1)
		return nil
	}
	return binding, nil
}

type invalidatingHarnessWorkspace struct {
	tool.Workspace
	closed atomic.Bool
}

func (w *invalidatingHarnessWorkspace) Read(ctx context.Context, path string) ([]byte, error) {
	if w.closed.Load() {
		return nil, errors.New("source lease closed")
	}
	return w.Workspace.Read(ctx, path)
}

type executionCommands struct{ source tool.Workspace }

func (c executionCommands) List(ctx context.Context) ([]prompt.Command, error) {
	if _, err := c.source.Read(ctx, "command.md"); err != nil {
		return nil, err
	}
	return []prompt.Command{{Name: "which"}}, nil
}

func (c executionCommands) Expand(ctx context.Context, input string) (string, bool, error) {
	if !strings.HasPrefix(strings.TrimSpace(input), "/which") {
		return input, false, nil
	}
	body, err := c.source.Read(ctx, "command.md")
	return string(body), err == nil, err
}

func harnessExecutionBinding(t *testing.T, id, instruction, command string) server.PlacementBinding {
	t.Helper()
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: id, Revision: "v1"}
	workspace := memfs.NewWorkspace("/" + id)
	if _, err := workspace.CreateFile(t.Context(), "AGENTS.md", []byte(instruction)); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CreateFile(t.Context(), "command.md", []byte(command)); err != nil {
		t.Fatal(err)
	}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, workspace, memledger.New(), nil)}
}

func harnessExecutionConfig(t *testing.T, provider *harnessExecutionProvider, requests *[]port.LLMRequest) Config {
	t.Helper()
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"repository"}, Mode: "combine"}
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"repository"}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"repository"}, Kinds: kinds})
	cfg.PlacementProvider, cfg.PlacementScope = provider, "test"
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { *requests = append(*requests, req) })}, mockllm.TextTurn("done"), mockllm.TextTurn("done"))
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{{
		ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "project"}, UsesExecutionWorkspace: true,
		Bind: func(ctx context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			if scope.SessionID == "" || scope.AcquireExecutionWorkspace == nil {
				return nil, nil, errors.New("missing exact execution-file capability")
			}
			workspace, release, err := scope.AcquireExecutionWorkspace(ctx)
			if err != nil {
				return nil, nil, err
			}
			return prompt.RootAssembler{Source: workspace}, release, nil
		},
	}}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{
		ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "project"}, UsesExecutionWorkspace: true,
		Bind: func(ctx context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			workspace, release, err := scope.AcquireExecutionWorkspace(ctx)
			if err != nil {
				return nil, nil, err
			}
			return executionCommands{source: workspace}, release, nil
		},
	}}
	return cfg
}

func TestHarnessPublishedCloseRequiresExplicitLoad(t *testing.T) {
	binding := harnessExecutionBinding(t, "closed", "EXACT-INSTRUCTIONS", "EXACT-COMMAND")
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{binding.Ref: binding}}
	var requests []port.LLMRequest
	cfg := harnessExecutionConfig(t, provider, &requests)
	cfg.OwnershipEnforced = true
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "owner", GrantType: session.GrantTypeUser})
	var binds atomic.Int32
	original := cfg.HarnessInstructionSources[0].Bind
	cfg.HarnessInstructionSources[0].Bind = func(ctx context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		binds.Add(1)
		return original(ctx, scope)
	}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID("closed-published"), server.WithPlacementBinding(binding))
	if err != nil {
		t.Fatal(err)
	}
	built.Service.CloseSession(sess.ID)
	before := binds.Load()
	if _, err := built.Service.ListCommandsForSession(ctx, sess.ID); err == nil {
		t.Error("discovery reactivated retired source")
	}
	if run, err := built.Service.StartRun(ctx, sess.ID, "ordinary run"); err == nil {
		for range run.Events() {
		}
		built.Service.FinishRun(sess.ID, run)
		t.Error("ordinary run reactivated retired source")
	}
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "foreign", GrantType: session.GrantTypeUser})
	if _, err := built.Service.LoadSession(foreign, sess.ID); err == nil {
		t.Error("foreign caller activated source")
	}
	if binds.Load() != before {
		t.Errorf("retired source rebound: before=%d after=%d", before, binds.Load())
	}
	if _, err := built.Service.LoadSession(ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	harnessRun(t, built, ctx, sess.ID, "/which")
	if len(requests) != 1 || !strings.Contains(harnessRequestText(requests[0]), "EXACT-COMMAND") {
		t.Fatalf("explicit load requests=%d", len(requests))
	}
}

func TestADR_0359_HarnessContext_Scenario6_SameOwnerSessionsStayDistinct(t *testing.T) {
	first := harnessExecutionBinding(t, "first", "FIRST-INSTRUCTIONS", "FIRST-COMMAND")
	second := harnessExecutionBinding(t, "second", "SECOND-INSTRUCTIONS", "SECOND-COMMAND")
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{first.Ref: first, second.Ref: second}}
	var requests []port.LLMRequest
	cfg := harnessExecutionConfig(t, provider, &requests)
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()

	owner := &session.Principal{Issuer: "issuer", Subject: "same-owner"}
	ctx := session.WithPrincipal(t.Context(), owner)
	for i, binding := range []server.PlacementBinding{first, second} {
		id := session.SessionID(fmt.Sprintf("exact-%d", i))
		sess, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithPlacementBinding(binding))
		if err != nil {
			t.Fatal(err)
		}
		harnessRun(t, built, ctx, sess.ID, "/which")
	}
	if len(requests) != 2 {
		t.Fatalf("requests=%d", len(requests))
	}
	for i, markers := range [][2]string{{"FIRST-INSTRUCTIONS", "FIRST-COMMAND"}, {"SECOND-INSTRUCTIONS", "SECOND-COMMAND"}} {
		text := harnessRequestText(requests[i])
		if !strings.Contains(text, markers[0]) || !strings.Contains(text, markers[1]) {
			t.Fatalf("session %d used wrong exact source: %s", i, text)
		}
		other := [2]string{"SECOND-INSTRUCTIONS", "SECOND-COMMAND"}
		if i == 1 {
			other = [2]string{"FIRST-INSTRUCTIONS", "FIRST-COMMAND"}
		}
		if strings.Contains(text, other[0]) || strings.Contains(text, other[1]) {
			t.Fatalf("session %d observed the other source: %s", i, text)
		}
	}
}

func TestADR_0359_HarnessContext_Scenario6_DynamicExactSourceAcquisition(t *testing.T) {
	t.Run("scheduled fire and restart", testExecutionSourceScheduledRestart)
	binding := harnessExecutionBinding(t, "reserved", "RESERVED-INSTRUCTIONS", "RESERVED-COMMAND")
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{binding.Ref: binding}}
	var requests []port.LLMRequest
	cfg := harnessExecutionConfig(t, provider, &requests)
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	id := session.SessionID("reserved-before-publication")
	sess, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithPlacementBinding(binding))
	if err != nil || sess.ID != id {
		t.Fatalf("reserved create = %+v, %v", sess, err)
	}
	provider.mu.Lock()
	calls := append([]server.PlacementReattachRequest(nil), provider.reattaches...)
	provider.mu.Unlock()
	if len(calls) < 2 || calls[0].Ref != binding.Ref {
		t.Fatalf("exact source was not acquired before publication: %+v", calls)
	}
}

func testExecutionSourceScheduledRestart(t *testing.T) {
	placement := harnessExecutionBinding(t, "durable-exact", "BEFORE-RESTART", "EXACT-COMMAND")
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{placement.Ref: placement}}
	storeDir := t.TempDir()
	store, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	owner := &session.Principal{Issuer: "issuer", Subject: "scheduled-owner", GrantType: session.GrantTypeUser}
	ctx := session.WithPrincipal(t.Context(), owner)
	const rootID session.SessionID = "reserved-root"
	var requests []port.LLMRequest
	cfg := harnessExecutionConfig(t, provider, &requests)
	cfg.StoreDir, cfg.AllowAllTools, cfg.OwnershipEnforced = storeDir, true, true
	original := cfg.HarnessInstructionSources[0].Bind
	var unpublished []session.SessionID
	var resumed atomic.Int32
	cfg.HarnessInstructionSources[0].Bind = func(ctx context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		if !scope.Principal.SameIdentity(owner) {
			return nil, nil, errors.New("wrong source owner")
		}
		_, loadErr := store.Load(ctx, scope.SessionID)
		if errors.Is(loadErr, port.ErrSessionNotFound) {
			unpublished = append(unpublished, scope.SessionID)
		} else if loadErr == nil && scope.SessionID == rootID {
			resumed.Add(1)
		} else {
			return nil, nil, fmt.Errorf("unexpected source identity %q: %w", scope.SessionID, loadErr)
		}
		return original(ctx, scope)
	}
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })},
		mockllm.ToolCallTurn(session.ToolCall{ID: "schedule", Name: "Schedule", Args: json.RawMessage(`{"verb":"create","name":"execution-proof","prompt":"scheduled task","cron":"@every 1h"}`)}), mockllm.TextTurn("scheduled"), mockllm.TextTurn("fired"))
	first, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := first.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(rootID), server.WithPlacementBinding(placement)); err != nil {
		t.Fatal(err)
	}
	for _, ev := range harnessRun(t, first, ctx, rootID, "schedule") {
		if ev.ToolResult != nil && ev.ToolResult.IsError {
			t.Fatal(ev.ToolResult.Content)
		}
	}
	fire := func(b *Built) port.ScheduleFire {
		runner := scheduler.New(scheduler.Config{Store: store.ScheduleStore(), Clock: testWallClock{}, Diagnostics: port.NopDiagnostics{}, TickInterval: time.Hour})
		runner.SetFire(makeFireFunc(b.Service, store.ScheduleStore(), defaultFireTimeout, nil))
		b.Service.SetScheduler(runner)
		defer func() { _ = runner.Stop() }()
		result, err := b.Service.FireNow(ctx, "execution-proof")
		if err != nil || result.Stop == session.StopError {
			t.Fatalf("fire=%+v err=%v", result, err)
		}
		return result
	}
	fire(first)
	if len(unpublished) != 2 || unpublished[0] != rootID || unpublished[1] == rootID {
		t.Fatalf("unpublished source IDs=%v", unpublished)
	}
	if len(requests) != 3 || !strings.Contains(harnessRequestText(requests[2]), "BEFORE-RESTART") {
		t.Fatal("scheduled model missed acquired source")
	}
	first.Close()
	ws := placement.Environment.Workspace()
	_, version, err := ws.ReadVersion(ctx, "AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ReplaceFile(ctx, "AGENTS.md", version, []byte("AFTER-RESTART")); err != nil {
		t.Fatal(err)
	}
	requests = nil
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })}, mockllm.TextTurn("resumed"), mockllm.TextTurn("fired again"))
	second, err := buildIsolated(t, ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	harnessRun(t, second, ctx, rootID, "/which")
	fire(second)
	if resumed.Load() != 1 || len(unpublished) != 3 || len(requests) != 2 {
		t.Fatalf("resume binds=%d unpublished=%v requests=%d", resumed.Load(), unpublished, len(requests))
	}
	for _, request := range requests {
		text := harnessRequestText(request)
		if !strings.Contains(text, "AFTER-RESTART") || strings.Contains(text, "BEFORE-RESTART") {
			t.Fatal("restart did not rebind live exact source")
		}
	}
	for _, req := range provider.reattaches {
		if req.Ref != placement.Ref || !req.Principal.SameIdentity(owner) {
			t.Fatalf("reattachment lost exact source authority: %+v", req)
		}
	}
}

func TestADR_0359_HarnessContext_Scenario6_AcquisitionFailureIsolation(t *testing.T) {
	t.Run("service attempt boundaries", testExecutionSourceFailureBoundaries)
	binding := harnessExecutionBinding(t, "retry", "RETRY-INSTRUCTIONS", "RETRY-COMMAND")
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{binding.Ref: binding}}
	var requests []port.LLMRequest
	cfg := harnessExecutionConfig(t, provider, &requests)
	var attempts atomic.Int32
	original := cfg.HarnessInstructionSources[0].Bind
	cfg.HarnessInstructionSources[0].Bind = func(ctx context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		assembler, cleanup, err := original(ctx, scope)
		if err == nil && attempts.Add(1) == 1 {
			return nil, cleanup, errors.New("construction failed")
		}
		return assembler, cleanup, err
	}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	id := session.SessionID("retry-same-unpublished-id")
	if _, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithPlacementBinding(binding)); err == nil {
		t.Fatal("construction failure published a session")
	}
	if provider.sourceCloses.Load() != 1 {
		t.Fatalf("failed attempt released %d source borrows, want its one acquired borrow", provider.sourceCloses.Load())
	}
	if _, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithPlacementBinding(binding)); err != nil {
		t.Fatalf("same unpublished id could not retry: %v", err)
	}
}

type failingHarnessPublicationStore struct {
	*memstore.Store
	fail atomic.Bool
}

func (s *failingHarnessPublicationStore) Create(ctx context.Context, sess *session.Session) error {
	if sess.ID == "attempt" && s.fail.Load() {
		return errors.New("publication failed")
	}
	return s.Store.Create(ctx, sess)
}

func testExecutionSourceFailureBoundaries(t *testing.T) {
	for _, failure := range []string{"bind", "cancel", "engine construction", "publication"} {
		t.Run(failure, func(t *testing.T) {
			placement := harnessExecutionBinding(t, "attempt-files", "attempt-instructions", "attempt-command")
			otherPlacement := harnessExecutionBinding(t, "other-files", "other-instructions", "other-command")
			provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{placement.Ref: placement, otherPlacement.Ref: otherPlacement}}
			store := &failingHarnessPublicationStore{Store: memstore.New()}
			var failing atomic.Bool
			failing.Store(true)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var failedWorkspace, otherWorkspace tool.Workspace
			reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "project"}, UsesExecutionWorkspace: true, Bind: func(bindCtx context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
				ws, release, err := scope.AcquireExecutionWorkspace(bindCtx)
				if err != nil {
					return nil, nil, err
				}
				if scope.SessionID == "other" {
					otherWorkspace = ws
				} else if failing.Load() {
					failedWorkspace = ws
					if failure == "bind" {
						return nil, release, errors.New("binding failed after acquisition")
					}
					if failure == "cancel" {
						cancel()
					}
				}
				return executionCommands{source: ws}, release, nil
			}}
			resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"repository"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
			if err != nil {
				t.Fatal(err)
			}
			defer resolver.Close()
			engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})
			factory := func(factoryCtx context.Context, id session.SessionID, owner *session.Principal, acquire server.ExecutionWorkspaceAcquirer, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, _ session.PermissionMode, _ []tool.Tool) (server.SessionEngineResult, error) {
				_, release, err := resolver.BorrowWithExecutionWorkspace(factoryCtx, id, owner, string(profile), acquire)
				if err != nil {
					return server.SessionEngineResult{}, err
				}
				if id == "attempt" && failure == "engine construction" && failing.Load() {
					release()
					return server.SessionEngineResult{}, errors.New("downstream engine construction failed")
				}
				return server.SessionEngineResult{Engine: engine, Close: func() error { release(); return nil }}, nil
			}
			svc, err := server.NewService(server.Config{Engine: engine, Store: store, Commands: resolver, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/attempt-files", OwnershipEnforced: true,
				SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
					t.Fatal("legacy factory used")
					return server.SessionEngineResult{}, nil
				}, SessionContextEngine: factory, OnCloseSession: resolver.Retire})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			owner := &session.Principal{Issuer: "issuer", Subject: "attempt-owner", GrantType: session.GrantTypeUser}
			otherCtx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "other-owner", GrantType: session.GrantTypeUser})
			create := func(ctx context.Context, id session.SessionID, binding server.PlacementBinding) error {
				_, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithSessionID(id), server.WithPlacementBinding(binding))
				return err
			}
			if err := create(otherCtx, "other", otherPlacement); err != nil {
				t.Fatal(err)
			}
			store.fail.Store(failure == "publication")
			if err := create(session.WithPrincipal(ctx, owner), "attempt", placement); err == nil {
				t.Fatal("failed attempt published")
			} else if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error=%v", err)
			}
			if _, err := store.Load(t.Context(), "attempt"); !errors.Is(err, port.ErrSessionNotFound) {
				t.Fatalf("failed attempt persisted: %v", err)
			}
			if provider.sourceCloses.Load() != 1 || failedWorkspace == nil {
				t.Fatalf("failed borrow cleanup=%d workspace=%v", provider.sourceCloses.Load(), failedWorkspace)
			}
			if _, err := failedWorkspace.Read(t.Context(), "command.md"); err == nil {
				t.Fatal("failed borrow not invalidated")
			}
			if body, err := otherWorkspace.Read(t.Context(), "command.md"); err != nil || string(body) != "other-command" {
				t.Fatalf("other caller source invalidated: %q %v", body, err)
			}
			if body, err := placement.Environment.Workspace().Read(t.Context(), "command.md"); err != nil || string(body) != "attempt-command" {
				t.Fatalf("retained placement invalidated: %q %v", body, err)
			}
			if _, _, err := resolver.Borrow(t.Context(), "attempt", owner, ""); !errors.Is(err, server.ErrCommandBindingRetired) {
				t.Fatalf("stale borrow reactivated: %v", err)
			}
			failing.Store(false)
			store.fail.Store(false)
			if err := create(session.WithPrincipal(t.Context(), owner), "attempt", placement); err != nil {
				t.Fatalf("same-ID retry: %v", err)
			}
			if _, err := svc.ListCommandsForSession(session.WithPrincipal(t.Context(), owner), "attempt"); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ListCommandsForSession(otherCtx, "other"); err != nil {
				t.Fatal(err)
			}
			svc.CloseSession("attempt")
			svc.CloseSession("other")
			if provider.sourceCloses.Load() != 3 {
				t.Fatalf("final cleanup count=%d", provider.sourceCloses.Load())
			}
		})
	}
}

func testExecutionSourceSelectionFactory(t *testing.T) {
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"process", "independent"}, Mode: "combine"}
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"repository"}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"process", "independent", "repository"}, Kinds: kinds})
	placement := harnessExecutionBinding(t, "unattached", "UNSELECTED-EXECUTION", "wrong")
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{placement.Ref: placement}}
	cfg.PlacementProvider, cfg.PlacementScope = provider, "test"
	var process, independent, commands, forbidden atomic.Int32
	check := func(scope HarnessSourceScope) {
		if scope.AcquireExecutionWorkspace != nil {
			t.Error("non-execution registration received acquisition callback")
		}
	}
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{
		{ID: "process", Scope: HarnessSourceScopeProcess, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			check(scope)
			process.Add(1)
			if scope.SessionID != "" || scope.Principal != nil {
				t.Error("process source received caller identity")
			}
			return hcAssembler("PROCESS"), nil, nil
		}},
		{ID: "independent", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			check(scope)
			independent.Add(1)
			return hcAssembler("INDEPENDENT"), nil, nil
		}},
	}
	for _, id := range []HarnessSourceID{"disabled", "repository"} {
		cfg.HarnessInstructionSources = append(cfg.HarnessInstructionSources, HarnessSourceRegistration[prompt.InstructionAssembler]{ID: id, Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "project"}, UsesExecutionWorkspace: true, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			forbidden.Add(1)
			return nil, nil, errors.New("unselected registration bound")
		}})
	}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(_ context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		check(scope)
		commands.Add(1)
		return &hcCommands{values: map[string]string{"which": "INDEPENDENT-COMMAND"}}, nil, nil
	}}}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithPlacementBinding(placement))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Service.ListCommandsForSession(t.Context(), sess.ID); err != nil {
		t.Fatal(err)
	}
	if process.Load() != 1 || independent.Load() != 1 || commands.Load() != 1 || forbidden.Load() != 0 || len(provider.reattaches) != 0 {
		t.Fatalf("bind counts: process=%d independent=%d commands=%d unselected=%d attachments=%d", process.Load(), independent.Load(), commands.Load(), forbidden.Load(), len(provider.reattaches))
	}
}

func TestADR_0359_HarnessContext_Scenario6_NonselectedSourcesDoNotAttachExecution(t *testing.T) {
	t.Run("real factory selection", testExecutionSourceSelectionFactory)
	reg := HarnessSourceRegistration[prompt.InstructionAssembler]{ID: "bad", Scope: HarnessSourceScopeProcess, Provenance: HarnessProvenancePolicy{Fixed: "project"}, UsesExecutionWorkspace: true, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		return hcAssembler("x"), nil, nil
	}}
	if err := validateHarnessRegistration("instructions", []HarnessSourceRegistration[prompt.InstructionAssembler]{reg}); err == nil {
		t.Fatal("process-scoped execution-file registration passed startup validation")
	}
	reg.Scope = HarnessSourceScopePrincipal
	reg.Provenance = HarnessProvenancePolicy{Fixed: "driver"}
	if err := validateHarnessRegistration("instructions", []HarnessSourceRegistration[prompt.InstructionAssembler]{reg}); err == nil {
		t.Fatal("non-project execution-file registration passed startup validation")
	}

	acquire := func(context.Context) (tool.Workspace, func() error, error) { return nil, nil, nil }
	cfg := Config{harnessScope: &HarnessSourceScope{SessionID: "selected", AcquireExecutionWorkspace: acquire}}
	if harnessBindingScope(cfg, false).AcquireExecutionWorkspace != nil {
		t.Fatal("unselected registration received execution-file capability")
	}
	if harnessBindingScope(cfg, true).AcquireExecutionWorkspace == nil {
		t.Fatal("selected execution-file registration lost capability")
	}

	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}
	binding := server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)}
	provider := &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{ref: binding}}
	var requests []port.LLMRequest
	buildCfg := harnessExecutionConfig(t, provider, &requests)
	built, err := buildIsolated(t, t.Context(), buildCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if _, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithSessionID("no-fs-source"), server.WithPlacementBinding(binding)); err == nil {
		t.Fatal("required execution-file source invented files for no-fs placement")
	}
}
