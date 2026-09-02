package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type awaitingLeaseClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *awaitingLeaseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *awaitingLeaseClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type releaseCountingLease struct {
	port.SessionLease
	releases atomic.Int64
}

func (l *releaseCountingLease) Release(ctx context.Context, lease port.Lease) error {
	l.releases.Add(1)
	return l.SessionLease.Release(ctx, lease)
}

func newAwaitingLeaseService(
	t *testing.T,
	store port.SessionStore,
	lease port.SessionLease,
	owner string,
	llm port.LLMProvider,
	ran *atomic.Int64,
) *server.Service {
	t.Helper()
	capability := server.NewSessionMutationCapability(true)
	catalog := tool.NewCatalog()
	catalog.MustRegister(&writeAskTool{ran: ran})
	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "test-model",
		Store:   capability.GuardStore(store),
	})
	svc, err := server.NewService(server.Config{
		Engine:             eng,
		Store:              store,
		Workspaces:         func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		SessionLease:       lease,
		LeaseOwner:         owner,
		LeaseTTL:           time.Minute,
		LeaseRenewInterval: time.Millisecond,
		MutationCapability: capability,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

type awaitingLeaseLossFixture struct {
	store      *memstore.Store
	lease      *releaseCountingLease
	clock      *awaitingLeaseClock
	stale      *server.Service
	sessionID  session.SessionID
	ask        session.PendingAsk
	staleRan   atomic.Int64
	staleRun   *agent.Run
	retracts   int
	cancelStop session.StopReason
}

func startAwaitingLeaseLoss(t *testing.T) *awaitingLeaseLossFixture {
	t.Helper()
	clock := &awaitingLeaseClock{now: time.Unix(1_700_000_000, 0)}
	lease := &releaseCountingLease{SessionLease: memlease.New(clock, time.Minute)}
	store := memstore.New()
	f := &awaitingLeaseLossFixture{store: store, lease: lease, clock: clock}
	f.stale = newAwaitingLeaseService(t, store, lease, "stale-owner",
		mockllm.New(
			mockllm.ToolCallTurn(session.NewToolCall("write-1", "Write", json.RawMessage(`{"path":"a.go"}`))),
			mockllm.TextTurn("stale owner must not continue"),
		), &f.staleRan)

	sess, err := f.stale.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	f.sessionID = sess.ID
	f.staleRun, err = f.stale.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("start stale run: %v", err)
	}

	select {
	case ev := <-f.staleRun.Events():
		if ev.Type != session.EvSessionInit {
			t.Fatalf("first event = %q, want session.init", ev.Type)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stale run to start")
	}
	for {
		select {
		case ev := <-f.staleRun.Events():
			if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
				continue
			}
			f.stale.Persist(context.Background(), sess.ID)
			persisted, loadErr := store.Load(context.Background(), sess.ID)
			if loadErr != nil {
				t.Fatalf("load awaiting snapshot: %v", loadErr)
			}
			var ok bool
			f.ask, ok = persisted.PendingAsk()
			if !ok {
				t.Fatal("persisted awaiting snapshot has no PendingAsk")
			}
			return f
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for permission ask")
		}
	}
}

func (f *awaitingLeaseLossFixture) loseLeaseAndDrain(t *testing.T) {
	t.Helper()
	f.clock.advance(2 * time.Minute)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-f.staleRun.Events():
			if !ok {
				f.stale.FinishRun(f.sessionID, f.staleRun)
				return
			}
			if ev.Type == session.EvPermissionRetract && ev.Ask != nil && ev.Ask.AskID == f.ask.AskID {
				f.retracts++
			}
			if ev.Type == session.EvResult && ev.Result != nil {
				f.cancelStop = ev.Result.Stop
			}
		case <-deadline:
			t.Fatal("timed out waiting for stale awaiting run to stop after lease loss")
		}
	}
}

func TestADR_0290_AwaitingLeaseLossRetractsLocalAskPreservesSnapshot(t *testing.T) {
	f := startAwaitingLeaseLoss(t)
	before, err := f.store.Load(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("load snapshot before loss: %v", err)
	}
	beforeAsk, ok := before.PendingAsk()
	if !ok {
		t.Fatal("snapshot before loss has no PendingAsk")
	}

	f.loseLeaseAndDrain(t)

	if f.retracts != 1 {
		t.Fatalf("local permission retracts = %d, want exactly 1", f.retracts)
	}
	if f.cancelStop != session.StopCancelled {
		t.Fatalf("stale local run stop = %q, want %q", f.cancelStop, session.StopCancelled)
	}
	if f.staleRan.Load() != 0 {
		t.Fatalf("stale owner executed pending tool %d times, want 0", f.staleRan.Load())
	}
	after, err := f.store.Load(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("load snapshot after loss: %v", err)
	}
	afterAsk, ok := after.PendingAsk()
	if after.State != session.StateAwaiting || !ok {
		t.Fatalf("snapshot after loss = state %q pending=%t, want awaiting with pending ask", after.State, ok)
	}
	if !reflect.DeepEqual(afterAsk, beforeAsk) {
		t.Fatalf("PendingAsk changed across lease loss:\n before: %#v\n after:  %#v", beforeAsk, afterAsk)
	}
	if got := f.lease.releases.Load(); got != 0 {
		t.Fatalf("stale owner explicitly released lease %d times, want 0", got)
	}
	if _, err := f.stale.ApproveRun(context.Background(), f.sessionID, f.ask.AskID, session.VerdictAllowOnce, ""); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("stale owner approval after loss = %v, want ErrSessionLeasedElsewhere", err)
	}
	if _, err := f.stale.StartRun(context.Background(), f.sessionID, "stale retry"); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("stale owner run entry after loss = %v, want ErrSessionLeasedElsewhere", err)
	}
}

func TestSessionAffinityAndHandoff_Scenario5_AwaitingLeaseLossSuccessorResumesExactAsk(t *testing.T) {
	f := startAwaitingLeaseLoss(t)
	f.loseLeaseAndDrain(t)
	parked, err := f.store.Load(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("load handoff snapshot: %v", err)
	}
	parkedAsk, ok := parked.PendingAsk()
	if parked.State != session.StateAwaiting || !ok || !reflect.DeepEqual(parkedAsk, f.ask) {
		t.Fatalf("handoff snapshot = state %q ask %#v, want awaiting with exact ask %#v", parked.State, parkedAsk, f.ask)
	}

	var successorRan atomic.Int64
	successor := newAwaitingLeaseService(t, f.store, f.lease, "successor-owner",
		mockllm.New(mockllm.TextTurn("continued by successor")), &successorRan)
	resumed, err := successor.ApproveRun(context.Background(), f.sessionID, f.ask.AskID, session.VerdictAllowOnce, "")
	if err != nil {
		t.Fatalf("successor resume exact ask: %v", err)
	}
	if resumed == nil {
		t.Fatal("successor did not create an awaiting-resume run")
	}
	var stop session.StopReason
	for ev := range resumed.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	successor.FinishRun(f.sessionID, resumed)
	if successorRan.Load() != 1 {
		t.Fatalf("successor executed pending tool %d times, want exactly 1", successorRan.Load())
	}
	if f.staleRan.Load() != 0 {
		t.Fatalf("stale owner executed pending tool %d times, want 0", f.staleRan.Load())
	}
	if stop != session.StopEndTurn {
		t.Fatalf("successor run stop = %q, want %q", stop, session.StopEndTurn)
	}
	final, err := f.store.Load(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("load successor snapshot: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("successor snapshot state = %q, want completed", final.State)
	}
	if _, ok := final.PendingAsk(); ok {
		t.Fatal("successor completed while exact PendingAsk remained unresolved")
	}
}
