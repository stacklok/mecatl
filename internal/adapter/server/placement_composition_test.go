package server_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	microvmadapter "github.com/stacklok/mecatl/internal/adapter/microvm"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type placementLifecycleSpy struct {
	compositionRoot string
	kind            session.EnvironmentKind
	invalid         bool
	binds           atomic.Int32
	defaultBinds    atomic.Int32
	noFSBinds       atomic.Int32
	reattaches      atomic.Int32
	closes          atomic.Int32
}

func (p *placementLifecycleSpy) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	p.binds.Add(1)
	if req.Selector.IsNoFS() {
		p.noFSBinds.Add(1)
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "no-fs", Revision: "v1"}
		return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)}, nil
	}
	p.defaultBinds.Add(1)
	return p.binding(), nil
}

func (p *placementLifecycleSpy) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattaches.Add(1)
	binding := p.binding()
	binding.Ref = req.Ref
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/workspace"), memledger.New(), nil)
	binding.Close = nil
	return binding, nil
}

func (p *placementLifecycleSpy) binding() server.PlacementBinding {
	kind := p.kind
	if kind == "" {
		kind = "microvm"
	}
	ref := session.EnvironmentRef{Kind: kind, ID: "opaque", Revision: "7"}
	binding := server.PlacementBinding{
		Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/workspace"), memledger.New(), nil),
		CompositionRoot: p.compositionRoot,
		Close:           func() error { p.closes.Add(1); return nil },
	}
	if p.invalid {
		binding.Ref.Revision = ""
	}
	return binding
}

func placementLifecycleConfig(provider server.PlacementProvider, store *memstore.Store) server.Config {
	var ids atomic.Int32
	return server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()}),
		Store:  store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/host/shared",
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})}, nil
		},
		NewID: func() session.SessionID {
			if ids.Add(1) == 1 {
				return "created"
			}
			return "created-next"
		},
	}
}

func TestServiceConstructionDoesNotAllocatePlacement(t *testing.T) {
	provider := &placementLifecycleSpy{compositionRoot: "/host/source"}
	svc, err := server.NewService(placementLifecycleConfig(provider, memstore.New()))
	if err != nil {
		t.Fatal(err)
	}
	svc.Close()
	if provider.binds.Load() != 0 || provider.reattaches.Load() != 0 || provider.closes.Load() != 0 {
		t.Fatalf("service build/close allocated placement: bind=%d reattach=%d close=%d", provider.binds.Load(), provider.reattaches.Load(), provider.closes.Load())
	}
}

func TestPlacementAllocationOccursOnlyForRequestedProfile(t *testing.T) {
	provider := &placementLifecycleSpy{compositionRoot: "/host/source"}
	svc, err := server.NewService(placementLifecycleConfig(provider, memstore.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	if _, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	if provider.defaultBinds.Load() != 1 {
		t.Fatalf("default Bind calls = %d, want 1", provider.defaultBinds.Load())
	}
	if _, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS); err != nil {
		t.Fatal(err)
	}
	if provider.defaultBinds.Load() != 1 || provider.noFSBinds.Load() != 1 {
		t.Fatalf("bind calls after no-fs: default=%d no-fs=%d", provider.defaultBinds.Load(), provider.noFSBinds.Load())
	}
}

func TestPlacementReadinessFailurePersistsNothingAndDoesNotFallBack(t *testing.T) {
	store := memstore.New()
	readinessCalls := 0
	provider, err := microvmadapter.NewPlacementProvider("unix:///run/unused-microvmd.sock", "/host/source", "microvm-local", "test", func(context.Context) error {
		readinessCalls++
		return errors.New("readiness failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := server.NewService(placementLifecycleConfig(provider, store))
	if err != nil {
		t.Fatalf("startup must not exercise readiness: %v", err)
	}
	if readinessCalls != 0 {
		t.Fatalf("startup readiness calls = %d, want 0", readinessCalls)
	}
	defer svc.Close()
	if _, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err == nil {
		t.Fatal("CreateSession succeeded after readiness failure")
	}
	if _, err := store.Load(context.Background(), "created"); err == nil {
		t.Fatal("failed readiness persisted a session")
	}
	if readinessCalls != 1 {
		t.Fatalf("create readiness calls = %d, want 1", readinessCalls)
	}
}

func TestACPUsesHostCompositionRootAndClosesRejectedBinding(t *testing.T) {
	hostRoot := t.TempDir()
	provider := &placementLifecycleSpy{compositionRoot: hostRoot}
	svc, err := server.NewService(placementLifecycleConfig(provider, memstore.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	created, err := svc.CreateACPSession(t.Context(), hostRoot, session.ModeDefault, session.Limits{}, nil, nil)
	if err != nil {
		t.Fatalf("ACP rejected host composition root in favor of guest /workspace: %v", err)
	}
	if created == nil || provider.closes.Load() != 0 {
		t.Fatalf("created=%v close calls=%d, want persisted session and retained attachment", created != nil, provider.closes.Load())
	}

	if _, err := svc.CreateACPSession(t.Context(), t.TempDir(), session.ModeDefault, session.Limits{}, nil, nil); err == nil {
		t.Fatal("ACP accepted cwd outside the host composition root")
	}
	if provider.closes.Load() != 1 {
		t.Fatalf("rejected ACP binding close calls=%d, want 1", provider.closes.Load())
	}

	if _, err := svc.CreateACPSession(t.Context(), hostRoot, session.ModeDefault, session.Limits{}, nil, func(session.SessionID, tool.Environment) (tool.Environment, error) {
		return tool.Environment{}, errors.New("overlay failed")
	}); err == nil {
		t.Fatal("ACP accepted failed editor overlay")
	}
	if provider.closes.Load() != 2 {
		t.Fatalf("failed-overlay binding close calls=%d, want 2", provider.closes.Load())
	}
}

type acpBindingOwnershipSpy struct {
	*placementLifecycleSpy
	reattachCloses atomic.Int32
}

func (p *acpBindingOwnershipSpy) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattaches.Add(1)
	binding := p.binding()
	binding.Ref = req.Ref
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/workspace"), memledger.New(), nil)
	binding.Close = func() error { p.reattachCloses.Add(1); return nil }
	return binding, nil
}

func TestLoadACPSessionOwnsExactBindingUntilOverrideLifecycleEnds(t *testing.T) {
	overlay := func(_ session.SessionID, base tool.Environment) (tool.Environment, error) { return base, nil }
	for _, tc := range []struct {
		name     string
		shutdown bool
	}{
		{name: "replacement and close session"},
		{name: "service shutdown", shutdown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hostRoot := t.TempDir()
			provider := &acpBindingOwnershipSpy{placementLifecycleSpy: &placementLifecycleSpy{compositionRoot: hostRoot}}
			svc, err := server.NewService(placementLifecycleConfig(provider, memstore.New()))
			if err != nil {
				t.Fatal(err)
			}
			created, err := svc.CreateACPSession(t.Context(), hostRoot, session.ModeDefault, session.Limits{}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.LoadACPSession(t.Context(), created.ID, hostRoot, nil, overlay); err != nil {
				t.Fatal(err)
			}
			if got := provider.reattaches.Load(); got != 0 {
				t.Fatalf("successful load multiplied attachment owners: reattaches = %d, want 0", got)
			}
			if tc.shutdown {
				svc.Close()
				if got := provider.closes.Load(); got != 1 {
					t.Fatalf("shutdown close calls = %d, want 1", got)
				}
				return
			}
			if _, err := svc.LoadACPSession(t.Context(), created.ID, hostRoot, nil, overlay); err != nil {
				t.Fatal(err)
			}
			if got := provider.reattaches.Load(); got != 0 {
				t.Fatalf("replacement multiplied attachment owners: reattaches = %d, want 0", got)
			}
			svc.CloseSession(created.ID)
			if got := provider.closes.Load(); got != 1 {
				t.Fatalf("CloseSession close calls = %d, want 1", got)
			}
			svc.Close()
			if got := provider.closes.Load(); got != 1 {
				t.Fatalf("shutdown reclosed binding: got %d calls, want 1", got)
			}
		})
	}
}

func TestRemotePlacementWithoutCompositionRootNeverUsesGuestRoot(t *testing.T) {
	for _, kind := range []session.EnvironmentKind{"microvm", "another-remote-backend"} {
		t.Run(string(kind), func(t *testing.T) {
			provider := &placementLifecycleSpy{kind: kind}
			cfg := placementLifecycleConfig(provider, memstore.New())
			var roots []string
			cfg.SessionEngine = func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, compositionRoot string, _ session.PermissionMode) (server.SessionEngineResult, error) {
				roots = append(roots, compositionRoot)
				return server.SessionEngineResult{Engine: cfg.Engine}, nil
			}
			svc, err := server.NewService(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()

			sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			svc.DropSessionEngineForTest(sess.ID)
			run, err := svc.StartRunContent(t.Context(), sess.ID, "continue", nil)
			if err != nil {
				t.Fatal(err)
			}
			for range run.Events() {
			}
			if len(roots) != 2 || roots[0] != "" || roots[1] != "" {
				t.Fatalf("composition roots = %q, want two empty roots (never guest /workspace)", roots)
			}
			if provider.defaultBinds.Load() != 1 || provider.reattaches.Load() != 0 {
				t.Fatalf("placement calls: bind=%d reattach=%d, want one retained exact binding", provider.defaultBinds.Load(), provider.reattaches.Load())
			}
		})
	}
}

func TestInvalidProviderBindingIsClosed(t *testing.T) {
	provider := &placementLifecycleSpy{invalid: true}
	svc, err := server.NewService(placementLifecycleConfig(provider, memstore.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if _, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{}); !errors.Is(err, server.ErrInvalidPlacementBinding) {
		t.Fatalf("CreateSession error = %v, want invalid placement binding", err)
	}
	if provider.closes.Load() != 1 {
		t.Fatalf("invalid binding close calls=%d, want 1", provider.closes.Load())
	}
}

func TestGuestExecutionRootNeverBecomesHostCompositionRoot(t *testing.T) {
	hostSource := t.TempDir()
	const marker = "host-source-project-policy"
	if err := os.WriteFile(filepath.Join(hostSource, "project.policy"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &placementLifecycleSpy{compositionRoot: hostSource}
	store := memstore.New()
	cfg := placementLifecycleConfig(provider, store)
	cfg.SessionEngine = func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, compositionRoot string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		if profile == server.ProfileNoFS {
			if compositionRoot != "" {
				return server.SessionEngineResult{}, errors.New("no-fs received host composition root")
			}
		} else {
			content, err := os.ReadFile(filepath.Join(compositionRoot, "project.policy"))
			if err != nil || string(content) != marker {
				return server.SessionEngineResult{}, errors.New("factory did not receive exact host project root")
			}
		}
		return server.SessionEngineResult{Engine: cfg.Engine}, nil
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if provider.defaultBinds.Load() != 1 {
		t.Fatalf("create Bind calls = %d, want 1", provider.defaultBinds.Load())
	}

	svc.DropSessionEngineForTest(sess.ID)
	run, err := svc.StartRunContent(context.Background(), sess.ID, "continue", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	if provider.reattaches.Load() != 0 {
		t.Fatal("run entry multiplied the retained exact placement attachment")
	}
	if provider.defaultBinds.Load() != 1 {
		t.Fatalf("reattach path called Bind: default Bind calls = %d", provider.defaultBinds.Load())
	}
}
