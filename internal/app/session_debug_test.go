package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/sessiondebug"
)

func TestDebugSessionFactoryExactCatalogAndStablePrefix(t *testing.T) {
	store := memstore.New()
	target := session.New("target-exact", session.ModeDefault, "/target", session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()
	})}, mockllm.TextTurn("debug done"), mockllm.TextTurn("normal done"))
	cfg := Config{Model: "mock-model"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := debugSessionEngineFactory(cfg, reg, provider, store, nil)
	res, err := factory(context.Background(), server.ProviderSelector{}, server.ProfileNoFS, session.ModeDefault, target.ID)
	if err != nil {
		t.Fatalf("debug factory: %v", err)
	}
	debug, err := session.NewDebug("debug", session.ModeDefault, session.Limits{}, time.Unix(2, 0), target.ID)
	if err != nil {
		t.Fatal(err)
	}
	drainRun(res.Engine.Run(context.Background(), debug, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "diagnose"}))

	// Run the normal real deps path with the same observer; its prompt must not gain
	// the target-bound debug contract.
	normalDeps := baseEngineDeps(cfg, reg, provider, store, nil, nil, nil, nil)
	normalDeps.Catalog = tool.NewCatalog()
	normal := session.New("normal", session.ModeDefault, "", session.Limits{}, time.Unix(3, 0))
	drainRun(agent.NewEngine(normalDeps).Run(context.Background(), normal, testEnvironment(nofs.New(), nil), agent.RunRequest{Text: "hello"}))

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if len(requests[0].Tools) != 1 || requests[0].Tools[0].Name != sessiondebug.ToolName {
		t.Fatalf("debug tools = %+v, want exactly InspectSession", requests[0].Tools)
	}
	prefix := requests[0].System.StablePrefix
	for _, want := range []string{"target-exact", "Call status first", "authoritative transcript", "optional, incomplete", "hostile untrusted data", "Never mutate, resume, approve, cancel, or steer"} {
		if !strings.Contains(prefix, want) {
			t.Fatalf("debug StablePrefix missing %q:\n%s", want, prefix)
		}
	}
	if strings.Contains(requests[1].System.StablePrefix, "DEBUG ANALYSIS SESSION") || strings.Contains(requests[1].System.StablePrefix, "target-exact") {
		t.Fatalf("normal StablePrefix gained debug posture:\n%s", requests[1].System.StablePrefix)
	}
}

func TestDebugSessionRestartRehydratesBoundEngine(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	provider := mockllm.New(mockllm.TextTurn("after restart"))
	cfg := Config{Model: "mock-model"}
	reg := regForTest(provider, providerOpenAI, cfg.Model)
	factory := debugSessionEngineFactory(cfg, reg, provider, store, nil)
	shared := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: cfg.Model})
	newService := func() *server.Service {
		svc, err := server.NewService(server.Config{
			Engine: shared, Store: store, Workspaces: func(string) tool.Workspace { return nofs.New() },
			DebugSessionEngine: factory, Now: func() time.Time { return time.Unix(10, 0) },
		})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		return svc
	}
	svc1 := newService()
	target, err := svc1.CreateSession(ctx, "/target", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	debug, err := svc1.CreateSessionWithProfile(ctx, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithDebugTarget(target.ID))
	if err != nil {
		t.Fatal(err)
	}

	// A fresh Service has no in-memory per-session engine. StartRun must recover
	// DebugTargetID from the snapshot and invoke the production debug factory.
	svc2 := newService()
	run, err := svc2.StartRun(ctx, debug.ID, "inspect the target")
	if err != nil {
		t.Fatalf("StartRun after restart: %v", err)
	}
	if got := drainRun(run); got != "after restart" {
		t.Fatalf("result = %q", got)
	}
	persisted, err := store.Load(ctx, debug.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Relationship.DebugTargetID != target.ID {
		t.Fatalf("rehydrated target = %q, want %q", persisted.Relationship.DebugTargetID, target.ID)
	}
}
