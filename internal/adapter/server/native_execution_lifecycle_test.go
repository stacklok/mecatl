package server_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type nativeRunProvider struct {
	ref      session.EnvironmentRef
	acquires atomic.Int64
	releases atomic.Int64
}

func (p *nativeRunProvider) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	return p.binding(), nil
}
func (p *nativeRunProvider) Reattach(context.Context, server.PlacementReattachRequest) (server.PlacementBinding, error) {
	return p.binding(), nil
}
func (p *nativeRunProvider) Applies(ref session.EnvironmentRef) bool { return ref == p.ref }
func (p *nativeRunProvider) AcquireRun(context.Context, server.ExecutionRunRequest) (server.ExecutionRunHandle, error) {
	p.acquires.Add(1)
	return &nativeRunHandle{provider: p, env: p.binding().Environment}, nil
}
func (p *nativeRunProvider) binding() server.PlacementBinding {
	return server.PlacementBinding{Ref: p.ref, Environment: tool.MustEnvironment(p.ref, memfs.NewWorkspace("/native"), memledger.New(), nil)}
}

type nativeRunHandle struct {
	provider *nativeRunProvider
	env      tool.Environment
}

func (h *nativeRunHandle) Environment() tool.Environment { return h.env }
func (*nativeRunHandle) Renew(context.Context) error     { return nil }
func (h *nativeRunHandle) Release(context.Context) error { h.provider.releases.Add(1); return nil }

func TestNativeRunClaimHeldUntilActualDrainRelease(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	provider := &nativeRunProvider{ref: ref}
	llm := mockllm.New(mockllm.TextTurn("first"), mockllm.TextTurn("second"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	store := memstore.New()
	svc, err := newPlacementTestService(server.Config{Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test", ExecutionAccess: provider, SharedEngineRoot: "/native", DefaultLimits: session.Limits{MaxTurns: 2}, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	sess := session.New("native-session", session.ModeDefault, ref, session.Limits{MaxTurns: 2}, time.Unix(1, 0))
	if err := store.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}

	first, err := svc.StartRunContent(t.Context(), sess.ID, "one", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range first.Events() {
	}
	if _, err := svc.StartRunContent(t.Context(), sess.ID, "too early", nil); err == nil {
		t.Fatal("native continuation started before the prior relay released its claim")
	}
	if provider.acquires.Load() != 1 || provider.releases.Load() != 0 {
		t.Fatalf("before FinishRun acquires=%d releases=%d", provider.acquires.Load(), provider.releases.Load())
	}

	svc.FinishRun(sess.ID, first)
	if provider.releases.Load() != 1 {
		t.Fatalf("release count=%d, want 1", provider.releases.Load())
	}
	second, err := svc.StartRunContent(t.Context(), sess.ID, "two", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range second.Events() {
	}
	svc.FinishRun(sess.ID, second)
	if provider.acquires.Load() != 2 || provider.releases.Load() != 2 {
		t.Fatalf("after continuation acquires=%d releases=%d", provider.acquires.Load(), provider.releases.Load())
	}
}
