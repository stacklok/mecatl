package server_test

import (
	"context"
	"errors"
	"sync"
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
	ref             session.EnvironmentRef
	acquireErr      error
	acquireHook     func()
	released        chan struct{}
	renewed         chan struct{}
	renewErr        error
	renewalDeadline time.Time
	acquires        atomic.Int64
	releases        atomic.Int64
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
	if p.acquireErr != nil {
		return nil, p.acquireErr
	}
	if p.acquireHook != nil {
		p.acquireHook()
	}
	handle := &nativeRunHandle{provider: p, env: p.binding().Environment}
	if !p.renewalDeadline.IsZero() {
		return &renewingNativeRunHandle{nativeRunHandle: handle, deadline: p.renewalDeadline}, nil
	}
	return handle, nil
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
func (h *nativeRunHandle) Release(context.Context) error {
	h.provider.releases.Add(1)
	if h.provider.released != nil {
		h.provider.released <- struct{}{}
	}
	return nil
}

type renewingNativeRunHandle struct {
	*nativeRunHandle
	deadline time.Time
}

func (h *renewingNativeRunHandle) RenewalDeadline() time.Time { return h.deadline }
func (h *renewingNativeRunHandle) Renew(context.Context) error {
	if h.provider.renewed != nil {
		h.provider.renewed <- struct{}{}
	}
	return h.provider.renewErr
}

func TestNativePersistedResolveAcquisitionFailurePreservesDurableAsk(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	acquireErr := errors.New("native run unavailable")
	provider := &nativeRunProvider{ref: ref, acquireErr: acquireErr}
	store := memstore.New()
	parked, ask := makePersistedControlSession(t, "native-persisted-resolve")
	parked.EnvironmentRef = ref
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: &ran})
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("must not continue")), Catalog: cat,
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test",
		ExecutionAccess: provider, SharedEngineRoot: "/native", Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	_, err = svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce)
	if !errors.Is(err, acquireErr) {
		t.Fatalf("ResolveRunAsk error = %v, want acquisition error", err)
	}
	if provider.acquires.Load() != 1 || provider.releases.Load() != 0 || ran.Load() != 0 {
		t.Fatalf("acquires=%d releases=%d executions=%d", provider.acquires.Load(), provider.releases.Load(), ran.Load())
	}
	got, err := store.Load(t.Context(), parked.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := got.PendingAsk()
	if got.State != session.StateAwaiting || !ok || pending.AskID != ask.AskID {
		t.Fatalf("durable ask changed: state=%q pending=%+v ok=%t", got.State, pending, ok)
	}
}

func TestNativePersistedResolveEmptyContinuationReleasesClaim(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	provider := &nativeRunProvider{ref: ref, released: make(chan struct{}, 1)}
	store := memstore.New()
	parked, ask := makePersistedControlSession(t, "native-empty-continuation")
	parked.EnvironmentRef = ref
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: &ran})
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: cat, Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test",
		ExecutionAccess: provider, SharedEngineRoot: "/native", Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	if _, err := svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.released:
	case <-time.After(time.Second):
		t.Fatal("empty continuation did not settle its native claim")
	}
	if provider.acquires.Load() != 1 || provider.releases.Load() != 1 || ran.Load() != 1 {
		t.Fatalf("acquires=%d releases=%d executions=%d", provider.acquires.Load(), provider.releases.Load(), ran.Load())
	}
}

func TestNativePersistedResolveRenewalFailureCancelsAndReleasesAfterDrain(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	provider := &nativeRunProvider{
		ref: ref, released: make(chan struct{}, 1), renewed: make(chan struct{}, 1),
		renewErr: errors.New("renewal lost"), renewalDeadline: time.Now().Add(600 * time.Millisecond),
	}
	store := memstore.New()
	parked, ask := makePersistedControlSession(t, "native-renewal-failure")
	parked.EnvironmentRef = ref
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	model := &closeJoinProvider{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	var ran atomic.Int64
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: &ran})
	engine := agent.NewEngine(agent.Deps{LLM: model, Catalog: cat, Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test",
		ExecutionAccess: provider, SharedEngineRoot: "/native", Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	releaseModel := sync.OnceFunc(func() { close(model.release) })
	defer releaseModel()

	if _, err := svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.entered:
	case <-time.After(time.Second):
		t.Fatal("resumed run did not reach its model continuation")
	}
	select {
	case <-provider.renewed:
	case <-time.After(time.Second):
		t.Fatal("execution renewal did not start")
	}
	select {
	case <-model.cancelled:
	case <-time.After(time.Second):
		t.Fatal("renewal failure did not cancel the resumed run")
	}
	if provider.releases.Load() != 0 {
		t.Fatal("claim released before the resumed run drained")
	}
	releaseModel()
	select {
	case <-provider.released:
	case <-time.After(time.Second):
		t.Fatal("claim was not released after detached relay drain")
	}
	if provider.acquires.Load() != 1 || provider.releases.Load() != 1 || ran.Load() != 1 {
		t.Fatalf("acquires=%d releases=%d executions=%d", provider.acquires.Load(), provider.releases.Load(), ran.Load())
	}
}

func TestNativePersistedResolveDrainBeforePublicationReleasesClaimOnce(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvironmentKind("kubernetes"), ID: "env", Revision: "rev"}
	provider := &nativeRunProvider{ref: ref, released: make(chan struct{}, 1)}
	store := memstore.New()
	parked, ask := makePersistedControlSession(t, "native-drain-before-publication")
	parked.EnvironmentRef = ref
	if err := store.Save(t.Context(), parked); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	cat := tool.NewCatalog()
	cat.MustRegister(&writeAskTool{ran: &ran})
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("must not continue")), Catalog: cat,
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test",
	})
	var svc *server.Service
	provider.acquireHook = func() { svc.Drain() }
	var err error
	svc, err = newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test",
		ExecutionAccess: provider, SharedEngineRoot: "/native", Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	_, err = svc.ResolveRunAsk(t.Context(), parked.ID, parked.RunID(), ask.AskID, session.VerdictAllowOnce)
	if err == nil {
		t.Fatal("ResolveRunAsk succeeded after drain began")
	}
	select {
	case <-provider.released:
	case <-time.After(time.Second):
		t.Fatal("acquired claim was not released")
	}
	if provider.acquires.Load() != 1 || provider.releases.Load() != 1 || ran.Load() != 0 {
		t.Fatalf("acquires=%d releases=%d executions=%d", provider.acquires.Load(), provider.releases.Load(), ran.Load())
	}
}

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
	svc.FinishRun(sess.ID, second)
	svc.Close()
	svc.Close()
	if provider.acquires.Load() != 2 || provider.releases.Load() != 2 {
		t.Fatalf("after continuation acquires=%d releases=%d", provider.acquires.Load(), provider.releases.Load())
	}
}
