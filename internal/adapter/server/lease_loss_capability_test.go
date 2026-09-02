package server_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type leaseLossRecorder struct {
	calls atomic.Int64
}

type leaseLossBarrierStore struct {
	next    port.SessionStore
	entered chan struct{}
	release chan struct{}
}

func (s *leaseLossBarrierStore) Save(ctx context.Context, sess *session.Session) error {
	close(s.entered)
	select {
	case <-s.release:
		return s.next.Save(ctx, sess)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *leaseLossBarrierStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.next.Load(ctx, id)
}

func (r *leaseLossRecorder) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
	r.calls.Add(1)
}

type leaseLossDiagnostics struct {
	mu       sync.Mutex
	messages []string
}

func (d *leaseLossDiagnostics) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.messages = append(d.messages, msg)
}

func (d *leaseLossDiagnostics) With(...any) port.Diagnostics { return d }

func (d *leaseLossDiagnostics) count(msg string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, got := range d.messages {
		if got == msg {
			n++
		}
	}
	return n
}

func newCapabilityService(t *testing.T, lease port.SessionLease, capability *server.SessionMutationCapability, recorder port.ToolCallRecorder, diag port.Diagnostics) (*server.Service, *memstore.Store, port.SessionStore, port.ToolCallRecorder) {
	t.Helper()
	store := memstore.New()
	guardedStore := capability.GuardStore(store)
	guardedRecorder := capability.GuardToolCallRecorder(recorder)
	eng := agent.NewEngine(agent.Deps{
		LLM:              blockingProvider{},
		Catalog:          tool.NewCatalog(),
		Policy:           permpolicy.NewPolicy(nil, permstore.New()),
		Model:            "test-model",
		Store:            guardedStore,
		ToolCallRecorder: guardedRecorder,
	})
	svc, err := server.NewService(server.Config{
		Engine:             eng,
		Store:              store,
		Workspaces:         func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		SessionLease:       lease,
		LeaseOwner:         "lease-loss-owner",
		LeaseTTL:           time.Hour,
		LeaseRenewInterval: 5 * time.Millisecond,
		Diagnostics:        diag,
		MutationCapability: capability,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, store, guardedStore, guardedRecorder
}

func TestSessionAffinityAndHandoff_Scenario5_LeaseLossCancelsAndPreventsNewMutations(t *testing.T) {
	lease := &fakeLease{}
	renewed := make(chan struct{})
	declareLoss := make(chan struct{})
	lease.renewHook = func(port.Lease) (port.Lease, error) {
		select {
		case <-renewed:
		default:
			close(renewed)
		}
		<-declareLoss
		return port.Lease{}, port.ErrLeaseHeld
	}
	capability := server.NewSessionMutationCapability(true)
	recorder := &leaseLossRecorder{}
	svc, baseStore, guardedStore, guardedRecorder := newCapabilityService(t, lease, capability, recorder, nil)
	sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	<-renewed

	// A call admitted while the capability is valid may complete. Hold it inside
	// the backend while renewal loss is declared, then release it afterward.
	barrier := &leaseLossBarrierStore{next: baseStore, entered: make(chan struct{}), release: make(chan struct{})}
	admittedDone := make(chan error, 1)
	go func() { admittedDone <- capability.GuardStore(barrier).Save(context.Background(), sess) }()
	<-barrier.entered
	close(declareLoss)

	var sawCancelled bool
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
			sawCancelled = true
		}
	}
	svc.FinishRun(sess.ID, run)
	if !sawCancelled {
		t.Fatal("lease loss did not cancel the owning run")
	}
	close(barrier.release)
	if err := <-admittedDone; err != nil {
		t.Fatalf("mutation admitted before loss did not complete: %v", err)
	}

	before := recorder.calls.Load()
	guardedRecorder.ToolCall(sess.ID, session.ToolCall{ID: "late", Name: "Write"}, session.NewToolResult("late", "must not land"), 0, 0)
	if got := recorder.calls.Load(); got != before {
		t.Fatalf("tool recorder calls after loss = %d, want %d", got, before)
	}
	if err := guardedStore.Save(context.Background(), sess); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("save after declared loss = %v, want ErrSessionLeasedElsewhere", err)
	}
	if _, err := svc.SetMode(context.Background(), sess.ID, session.ModePlan); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("new mutation after declared loss = %v, want ErrSessionLeasedElsewhere", err)
	}
}

func TestADR_0290_OptionalLeaseCompatibilityAndUnsupportedFallback(t *testing.T) {
	t.Run("no lease", func(t *testing.T) {
		capability := server.NewSessionMutationCapability(false)
		recorder := &leaseLossRecorder{}
		svc, _, guardedStore, guardedRecorder := newCapabilityService(t, nil, capability, recorder, nil)
		sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if err := guardedStore.Save(context.Background(), sess); err != nil {
			t.Fatalf("no-lease guarded save = %v", err)
		}
		guardedRecorder.ToolCall(sess.ID, session.ToolCall{ID: "no-lease"}, session.NewToolResult("no-lease", "ok"), 0, 0)
		if got := recorder.calls.Load(); got != 1 {
			t.Fatalf("no-lease recorder calls = %d, want 1", got)
		}
	})

	t.Run("unsupported is sticky fallback", func(t *testing.T) {
		lease := &fakeLease{acquireErr: port.ErrLeaseUnsupported}
		capability := server.NewSessionMutationCapability(true)
		diag := &leaseLossDiagnostics{}
		recorder := &leaseLossRecorder{}
		svc, _, guardedStore, guardedRecorder := newCapabilityService(t, lease, capability, recorder, diag)
		sess, err := svc.CreateSession(context.Background(), "/ws", session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		run, err := svc.StartRun(context.Background(), sess.ID, "go")
		if err != nil {
			t.Fatalf("unsupported lease start: %v", err)
		}
		run.Cancel()
		for range run.Events() {
		}
		svc.FinishRun(sess.ID, run)
		if err := guardedStore.Save(context.Background(), sess); err != nil {
			t.Fatalf("unsupported fallback save = %v", err)
		}
		guardedRecorder.ToolCall(sess.ID, session.ToolCall{ID: "unsupported"}, session.NewToolResult("unsupported", "ok"), 0, 0)
		if got := recorder.calls.Load(); got != 1 {
			t.Fatalf("unsupported fallback recorder calls = %d, want 1", got)
		}
		if _, err := svc.SetMode(context.Background(), sess.ID, session.ModePlan); err != nil {
			t.Fatalf("unsupported fallback second mutation = %v", err)
		}
		lease.mu.Lock()
		acquires := lease.acquires
		lease.mu.Unlock()
		if acquires != 1 {
			t.Fatalf("unsupported lease acquires = %d, want 1", acquires)
		}
		const unsupportedMessage = "session leasing unsupported by backend; disabling (running without cross-process exclusion)"
		if got := diag.count(unsupportedMessage); got != 1 {
			t.Fatalf("unsupported diagnostics = %d, want one sticky message", got)
		}
	})
}

func TestADR_0290_LeaseRemainsSessionScoped(t *testing.T) {
	lease := &fakeLease{}
	var lostID session.SessionID
	lease.renewHook = func(l port.Lease) (port.Lease, error) {
		if l.SessionID == lostID {
			return port.Lease{}, port.ErrLeaseHeld
		}
		return l, nil
	}
	capability := server.NewSessionMutationCapability(true)
	recorder := &leaseLossRecorder{}
	svc, _, guardedStore, guardedRecorder := newCapabilityService(t, lease, capability, recorder, nil)
	a, err := svc.CreateSession(context.Background(), "/a", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	lostID = a.ID
	b, err := svc.CreateSession(context.Background(), "/b", session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	runA, err := svc.StartRun(context.Background(), a.ID, "a")
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	runB, err := svc.StartRun(context.Background(), b.ID, "b")
	if err != nil {
		t.Fatalf("start b: %v", err)
	}
	for range runA.Events() {
	}
	svc.FinishRun(a.ID, runA)

	if err := guardedStore.Save(context.Background(), a); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("lost session save = %v, want ErrSessionLeasedElsewhere", err)
	}
	if err := guardedStore.Save(context.Background(), b); err != nil {
		t.Fatalf("peer session save after a loss = %v", err)
	}
	guardedRecorder.ToolCall(a.ID, session.ToolCall{ID: "a"}, session.NewToolResult("a", "blocked"), 0, 0)
	guardedRecorder.ToolCall(b.ID, session.ToolCall{ID: "b"}, session.NewToolResult("b", "allowed"), 0, 0)
	if got := recorder.calls.Load(); got != 1 {
		t.Fatalf("session-scoped recorder calls = %d, want only session-b", got)
	}
	runB.Cancel()
	for range runB.Events() {
	}
	svc.FinishRun(b.ID, runB)
}
