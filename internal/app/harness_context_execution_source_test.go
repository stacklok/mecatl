package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
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
	binding.Close = func() error {
		p.sourceCloses.Add(1)
		return nil
	}
	return binding, nil
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
		ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "project"}, ExecutionFiles: true,
		Bind: func(ctx context.Context, scope HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
			if scope.SessionID == "" || scope.AcquireExecutionFiles == nil {
				return nil, nil, errors.New("missing exact execution-file capability")
			}
			workspace, release, err := scope.AcquireExecutionFiles(ctx)
			if err != nil {
				return nil, nil, err
			}
			return prompt.RootAssembler{Source: workspace}, release, nil
		},
	}}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{
		ID: "repository", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "project"}, ExecutionFiles: true,
		Bind: func(ctx context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			workspace, release, err := scope.AcquireExecutionFiles(ctx)
			if err != nil {
				return nil, nil, err
			}
			return executionCommands{source: workspace}, release, nil
		},
	}}
	return cfg
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

func TestADR_0359_HarnessContext_Scenario6_AcquisitionFailureIsolation(t *testing.T) {
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

func TestADR_0359_HarnessContext_Scenario6_NonselectedSourcesDoNotAttachExecution(t *testing.T) {
	reg := HarnessSourceRegistration[prompt.InstructionAssembler]{ID: "bad", Scope: HarnessSourceScopeProcess, Provenance: HarnessProvenancePolicy{Fixed: "project"}, ExecutionFiles: true, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
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
	cfg := Config{harnessScope: &HarnessSourceScope{SessionID: "selected", AcquireExecutionFiles: acquire}}
	if harnessBindingScope(cfg, false).AcquireExecutionFiles != nil {
		t.Fatal("unselected registration received execution-file capability")
	}
	if harnessBindingScope(cfg, true).AcquireExecutionFiles == nil {
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
