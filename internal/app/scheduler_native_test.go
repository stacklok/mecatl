package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type scheduledNativeProvider struct {
	mu        sync.Mutex
	ref       session.EnvironmentRef
	owner     *session.Principal
	published map[session.SessionID]bool
	pending   map[session.SessionID]bool
	busy      bool

	loseCommitResponse bool
	commits            int
	aborts             int
	closes             int
}

func (p *scheduledNativeProvider) environment() tool.Environment {
	return tool.MustEnvironment(p.ref, memfs.NewWorkspace("/workspace"), memledger.New(), nil)
}
func (p *scheduledNativeProvider) Applies(ref session.EnvironmentRef) bool {
	return ref.Kind == p.ref.Kind
}
func (p *scheduledNativeProvider) Bind(_ context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.BindingID == "" || !req.Principal.SameIdentity(p.owner) {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	p.published[req.BindingID] = true
	return server.PlacementBinding{Ref: p.ref, Environment: p.environment()}, nil
}
func (p *scheduledNativeProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.Ref != p.ref || !req.Principal.SameIdentity(p.owner) || !p.published[req.BindingID] {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	return server.PlacementBinding{Ref: p.ref, Environment: p.environment()}, nil
}
func (p *scheduledNativeProvider) ReserveSuccessor(_ context.Context, req server.PlacementSuccessorRequest) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return server.PlacementBinding{}, server.ErrPlacementChanged
	}
	if req.Ref != p.ref || !req.Principal.SameIdentity(p.owner) || !p.published[req.SourceBindingID] || req.DestinationBindingID == "" {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	p.pending[req.DestinationBindingID] = true
	id := req.DestinationBindingID
	committed := false
	return server.PlacementBinding{Ref: p.ref, Environment: p.environment(), Commit: func(context.Context) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.pending[id] {
			return errors.New("successor reservation was aborted before publication")
		}
		p.commits++
		committed = true
		p.published[id] = true
		delete(p.pending, id)
		if p.loseCommitResponse {
			return errors.New("commit response lost")
		}
		return nil
	}, Close: func() error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.closes++
		if !committed && p.pending[id] {
			p.aborts++
			delete(p.pending, id)
		}
		return nil
	}}, nil
}
func (p *scheduledNativeProvider) AcquireRun(_ context.Context, req server.ExecutionRunRequest) (server.ExecutionRunHandle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy || !p.published[req.BindingID] || req.Ref != p.ref || !req.Principal.SameIdentity(p.owner) {
		return nil, server.ErrPlacementChanged
	}
	p.busy = true
	return &scheduledNativeRun{provider: p}, nil
}

type scheduledNativeRun struct{ provider *scheduledNativeProvider }

func (h *scheduledNativeRun) Environment() tool.Environment { return h.provider.environment() }
func (*scheduledNativeRun) Renew(context.Context) error     { return nil }
func (h *scheduledNativeRun) Release(context.Context) error {
	h.provider.mu.Lock()
	defer h.provider.mu.Unlock()
	h.provider.busy = false
	return nil
}

func TestMakeFireFuncReleasesSessionAfterLostNativeCommitResponse(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	provider := &scheduledNativeProvider{
		ref: session.EnvironmentRef{Kind: "kubernetes", ID: "env", Revision: "r1"}, owner: owner,
		published: map[session.SessionID]bool{}, pending: map[session.SessionID]bool{}, loseCommitResponse: true,
	}
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	llm := mockllm.New()
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test"})
	engineCloses := 0
	svc, err := server.NewService(server.Config{
		Engine: engine, Store: store, PlacementProvider: provider, ExecutionAccess: provider,
		PlacementScope: "native-test", OwnershipEnforced: true, MaxSessionEngines: 1,
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine, Close: func() error { engineCloses++; return nil }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := session.WithPrincipal(t.Context(), owner)
	origin, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}
	svc.CloseSession(origin.ID)
	engineCloses = 0
	sched := port.Schedule{Spec: port.ScheduleSpec{
		Name: "lost-commit", Prompt: "fire", Mode: session.ModePlan,
		Owner: owner, EnvironmentRef: origin.EnvironmentRef, PlacementScope: "native-test",
		Selector: port.ScheduleProviderSelector{ProviderID: "test"}, OriginSessionID: origin.ID,
	}}
	fire, err := makeFireFunc(svc, nil, defaultFireTimeout, nil)(ctx, sched, time.Now())
	if !errors.Is(err, server.ErrInternal) || fire.Stop != session.StopError || fire.SessionID == "" {
		t.Fatalf("lost commit response: fire=%+v err=%v", fire, err)
	}
	if svc.IsLive(fire.SessionID) || engineCloses != 1 {
		t.Errorf("fire engine retained: live=%v closes=%d", svc.IsLive(fire.SessionID), engineCloses)
	}
	// One Close is the fire's explicit rollback; the second releases the
	// installed service attachment. Neither may abort a dispatched Commit.
	if provider.closes != 2 || provider.commits != 1 || provider.aborts != 0 || !provider.published[fire.SessionID] {
		t.Errorf("reference lifecycle: closes=%d commits=%d aborts=%d published=%v", provider.closes, provider.commits, provider.aborts, provider.published)
	}
	stored, err := store.Load(ctx, fire.SessionID)
	if err != nil || stored.EnvironmentRef != origin.EnvironmentRef {
		t.Fatalf("durable fire snapshot lost: %+v %v", stored, err)
	}
	if llm.Calls() != 0 {
		t.Errorf("ambiguous publication ran the model: calls=%d", llm.Calls())
	}
	if _, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: "test"}, server.ProfileDefault); err != nil {
		t.Fatalf("subsequent create with MaxSessionEngines=1: %v", err)
	}
}

func TestBuiltSchedulerBorrowsNativeOriginAndPublishesEachFire(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	provider := &scheduledNativeProvider{ref: session.EnvironmentRef{Kind: "kubernetes", ID: "env", Revision: "r1"}, owner: owner, published: map[session.SessionID]bool{}, pending: map[session.SessionID]bool{}}
	dir := t.TempDir()
	llm := mockllm.New(mockllm.TextTurn("origin"), mockllm.TextTurn("fire one"), mockllm.TextTurn("fire two"), mockllm.TextTurn("resumed"), mockllm.TextTurn("no-fs"))
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), StoreDir: dir, UseMock: true, MockProvider: llm, NoSoul: true, NoShell: true, RemoteExecution: true, OwnershipEnforced: true, PlacementScope: "native-test", PlacementProvider: provider, ExecutionOwnerAllowed: func(p *session.Principal) bool {
		return p != nil && p.Subject == "alice" && (p.GrantType == session.GrantTypeUser || p.GrantType == session.GrantTypeClientCredentials)
	}, ExecutionTemplateAllowed: func(p *session.Principal, _, _ string) bool {
		return p != nil && p.Subject == "alice"
	}, SchedulerEnabled: true, SchedulerTickInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	if !built.Service.HasScheduler() {
		t.Fatal("test scheduler not wired")
	}
	ctx := session.WithPrincipal(t.Context(), owner)
	origin, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRunContent(ctx, origin.ID, "origin", nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := port.ScheduleSpec{Name: "native-borrowed", Prompt: "fire", Mode: session.ModePlan, Trigger: port.TriggerSpec{Cron: "0 0 * * *"}, OriginSessionID: origin.ID}
	sched, err := built.Service.CreateSchedule(ctx, spec)
	if err != nil {
		t.Fatalf("create during origin run: %v", err)
	}
	busyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	// Build starts leadership acquisition asynchronously. Wait only for that
	// gate; once admitted, the busy origin must still refuse the fire.
	retry := time.NewTicker(time.Millisecond)
	defer retry.Stop()
	for {
		got, err := built.Service.FireNow(busyCtx, sched.Spec.Name)
		if !errors.Is(err, server.ErrScheduleNotLeader) {
			if !errors.Is(err, server.ErrPlacementChanged) {
				t.Fatalf("busy manual fire=%+v %v", got, err)
			}
			break
		}
		select {
		case <-retry.C:
		case <-busyCtx.Done():
			t.Fatalf("scheduler leadership not ready: %v (last fire error: %v)", busyCtx.Err(), err)
		}
	}
	busyCalls := llm.Calls()
	for range run.Events() {
	}
	built.Service.FinishRun(origin.ID, run)
	if busyCalls > 1 || llm.Calls() > 1 {
		t.Fatalf("busy manual fire consumed model turns: before=%d after=%d", busyCalls, llm.Calls())
	}
	ids := map[session.SessionID]bool{}
	fire := makeFireFunc(built.Service, nil, defaultFireTimeout, nil)
	for range 2 {
		got, err := fire(ctx, sched, time.Now())
		if err != nil || got.Stop != session.StopEndTurn {
			t.Fatalf("fire=%+v %v (mock calls=%d)", got, err, llm.Calls())
		}
		if ids[got.SessionID] || got.SessionID == origin.ID {
			t.Fatalf("duplicate fire session: %s", got.SessionID)
		}
		ids[got.SessionID] = true
	}
	store, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	for id := range ids {
		if !provider.published[id] || provider.pending[id] {
			provider.mu.Unlock()
			t.Fatalf("unpublished fire reference: %s", id)
		}
	}
	provider.mu.Unlock()
	for id := range ids {
		sess, err := store.Load(ctx, id)
		if err != nil || sess.EnvironmentRef != origin.EnvironmentRef {
			t.Fatalf("fire session %s: %v", id, err)
		}
	}
	resumed, err := built.Service.StartRunContent(ctx, origin.ID, "resume", nil)
	if err != nil {
		t.Fatalf("origin not resumable: %v", err)
	}
	for range resumed.Events() {
	}
	built.Service.FinishRun(origin.ID, resumed)
	if _, err := built.Service.CreateSchedule(ctx, port.ScheduleSpec{Name: "independent", Prompt: "unsupported", Mode: session.ModePlan, Trigger: port.TriggerSpec{Cron: "0 0 * * *"}}); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("independent native schedule: %v", err)
	}
	noFS, err := built.Service.CreateSchedule(ctx, port.ScheduleSpec{Name: "no-fs", Prompt: "safe", Mode: session.ModePlan, Profile: string(server.ProfileNoFS), Trigger: port.TriggerSpec{Cron: "0 0 * * *"}})
	if err != nil || noFS.Spec.EnvironmentRef.Kind != session.EnvKindNoFS {
		t.Fatalf("unrelated no-FS schedule excluded: %+v %v", noFS, err)
	}
	if got, err := built.Service.FireNow(ctx, noFS.Spec.Name); err != nil || got.Stop != session.StopEndTurn {
		t.Fatalf("no-FS fire: %+v %v", got, err)
	}
}
