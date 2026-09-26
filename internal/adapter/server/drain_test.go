package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// newDrainTestService builds a minimal Service over mockllm for the drain-gate
// tests: one turn that ends immediately, no lease wired (the gate must work
// WITHOUT a lease too — a draining replica rejects new runs regardless).
func newDrainTestService(t *testing.T) *server.Service {
	t.Helper()
	store := memstore.New()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  store,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// miniredisRun starts an in-process miniredis for the StorageReady tests.
func miniredisRun() (*miniredis.Miniredis, error) { return miniredis.Run() }

// redisstoreNew wraps redisstore.New for the StorageReady tests.
func redisstoreNew(addr string) (*redisstore.Store, error) { return redisstore.New(addr) }

// newStorageReadyTestService builds a Service backed by the given store (for the
// StorageReady tests): the Service serves traffic through THIS store, so
// StorageReady pings the same client /readyz would test in production.
func newStorageReadyTestService(t *testing.T, st port.SessionStore) *server.Service {
	t.Helper()
	ps := permstore.New()
	cat := tool.NewCatalog()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, ps),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  st,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestDrainGateStartsFalse: a fresh Service accepts run-entries (the gate is
// byte-identical to pre-ADR-0048 when Drain has not been called).
func TestDrainGateStartsFalse(t *testing.T) {
	svc := newDrainTestService(t)
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.StartRun(context.Background(), sess.ID, "go"); err != nil {
		t.Fatalf("StartRun on a fresh (non-draining) service = %v, want nil", err)
	}
	if svc.ActiveRuns() != 0 {
		t.Errorf("ActiveRuns on a no-lease service = %d, want 0", svc.ActiveRuns())
	}
}

// TestDrainRejectsNewRuns: once Drain is armed, StartRun (and the
// awaiting-resume path) returns ErrUnavailable BEFORE leasing/launching — the
// run never starts. A drain gate without a wired lease still rejects (the gate
// is not lease-dependent). We ALSO assert the run was never registered
// (LookupRun misses) and no lease is held (ActiveRuns==0) — the gate must
// refuse BEFORE the register/acquire side effects, not just return an error
// after launching.
func TestDrainRejectsNewRuns(t *testing.T) {
	svc := newDrainTestService(t)
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svc.Drain()
	_, err = svc.StartRun(context.Background(), sess.ID, "after drain")
	if !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after Drain = %v, want ErrUnavailable", err)
	}
	// The run must NEVER have been registered — the gate refused before launch.
	if _, ok := svc.LookupRun(sess.ID); ok {
		t.Fatal("a run was registered after Drain (the drain gate must refuse BEFORE registering/launching, not after)")
	}
	if svc.ActiveRuns() != 0 {
		t.Errorf("ActiveRuns after a drained StartRun = %d, want 0 (no lease held)", svc.ActiveRuns())
	}
}

// TestDrainIsIdempotent: calling Drain twice is harmless (a one-way gate).
func TestDrainIsIdempotent(t *testing.T) {
	svc := newDrainTestService(t)
	if svc.IsDraining() {
		t.Fatalf("IsDraining = true on a fresh service, want false (draining starts false)")
	}
	svc.Drain()
	if !svc.IsDraining() {
		t.Fatalf("IsDraining = false after Drain, want true")
	}
	svc.Drain() // must not panic or error
	if !svc.IsDraining() {
		t.Fatalf("IsDraining = false after double Drain, want true")
	}
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := svc.StartRun(context.Background(), sess.ID, "go"); !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after double Drain = %v, want ErrUnavailable", err)
	}
}

// TestDrainDoesNotBlockExistingRun: Drain only gates NEW run-entries; an
// already-launched run is NOT cancelled by Drain itself (that is the bounded
// GracefulStop's job in the cmd binary). The run stays LIVE while draining —
// asserted with a blockingProvider (streams nothing until ctx is cancelled) so
// the run cannot have completed by the time Drain is armed, then the explicit
// cancel ends it with the cancelled stop (NOT a benign terminal Drain would
// have imposed — proving Drain did not touch the in-flight run).
func TestDrainDoesNotBlockExistingRun(t *testing.T) {
	svc := newLeasedService(t, nil, blockingProvider{})
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "go")
	if err != nil {
		t.Fatalf("StartRun before drain: %v", err)
	}
	// Arm the drain while the run is LIVE (blockingProvider streams nothing
	// until cancelled, so the run is StateRunning here, not done). The run must
	// stay live — Drain must NOT cancel an in-flight run.
	svc.Drain()
	// Give the run a moment to observe (and reject) any drain side-effect; a
	// regression that cancelled on Drain would end the run within this window.
	time.Sleep(50 * time.Millisecond)
	if _, ok := svc.LookupRun(sess.ID); !ok {
		t.Fatal("the live run was removed/ended after Drain — Drain must NOT cancel an in-flight run")
	}
	// Now explicitly cancel the run (the bounded GracefulStop's job, not Drain's).
	run.Cancel()
	var stop session.StopReason
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	svc.FinishRun(sess.ID, run)
	if stop != session.StopCancelled {
		t.Fatalf("the live run's stop after explicit cancel = %q, want %q (the run must stay live through Drain and end only on the explicit cancel)", stop, session.StopCancelled)
	}
}

// TestDrainAwaitingResumePathGated asserts that even an idle-session resume
// attempt is refused at admission once drain begins. The drain gate is a
// replica-wide retirement boundary, not a state oracle: no retry/resume path may
// proceed toward ownership acquisition after it is armed.
func TestDrainAwaitingResumePathGated(t *testing.T) {
	svc := newDrainTestService(t)
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svc.Drain()
	_, err = svc.ApproveRun(context.Background(), sess.ID, "ask-x", session.VerdictAllowOnce, "")
	if !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("ApproveRun after Drain = %v, want ErrUnavailable", err)
	}
}

// TestDrainGateRefusesBeforeAcquire: with a wired lease, the drain gate must
// refuse BEFORE calling Acquire — a regression that acquired-then-refused would
// leak a held lease on a draining replica (the survivor would then have to
// wait the TTL). The fakeLease counts acquires; after Drain, StartRun must
// return ErrUnavailable with the acquire counter UNCHANGED.
func TestDrainGateRefusesBeforeAcquire(t *testing.T) {
	lease := &fakeLease{}
	svc := newLeasedService(t, lease, mockllm.New(mockllm.TextTurn("never runs")))
	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	svc.Drain()
	_, err = svc.StartRun(context.Background(), sess.ID, "after drain")
	if !errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("StartRun after Drain with a wired lease = %v, want ErrUnavailable", err)
	}
	lease.mu.Lock()
	acquires := lease.acquires
	lease.mu.Unlock()
	if acquires != 0 {
		t.Fatalf("Acquire called %d time(s) on a draining replica, want 0 (the drain gate must refuse BEFORE acquiring — a held lease on a draining replica would leak)", acquires)
	}
}

// TestStorageReadyMemstoreAlwaysReady: a non-pinging store (memstore) is
// always ready — readiness is then drain-gated only (the byte-identical
// fallback when --redis-url is empty). Confirms StorageReady does not panic on
// a store without a Ping method.
func TestStorageReadyMemstoreAlwaysReady(t *testing.T) {
	svc := newDrainTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !svc.StorageReady(ctx) {
		t.Error("StorageReady on a memstore = false, want true (a non-pinging store is always ready)")
	}
}

// TestStorageReadyRedis: a Redis-backed service's StorageReady pings the SAME
// store the Service serves traffic through. Over a live miniredis it reports
// true; after the broker closes it reports false (so /readyz flips not-ready on
// a Redis outage). Built inline (newDrainTestService uses memstore).
func TestStorageReadyRedis(t *testing.T) {
	mr, err := miniredisRun()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	st, err := redisstoreNew(mr.Addr())
	if err != nil {
		t.Fatalf("redisstore.New: %v", err)
	}
	defer st.Close()
	svc := newStorageReadyTestService(t, st)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !svc.StorageReady(ctx) {
		t.Error("StorageReady on a live miniredis = false, want true")
	}
	mr.Close()
	// After the broker closes, the ping must fail (a short-timeout ctx keeps it
	// from wedging). Allow a brief window for the client to notice.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if !svc.StorageReady(pingCtx) {
			pingCancel()
			return
		}
		pingCancel()
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("StorageReady on a closed miniredis stayed true, want false (Redis outage → /readyz not-ready)")
}

type slowPingingStore struct {
	*memstore.Store
	started chan struct{}
}

func (s *slowPingingStore) Ping(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}

// TestStorageReadySlowStoreRespectsCallerDeadline proves a Redis-like backend
// that does not answer promptly cannot wedge /readyz beyond the caller's bound.
// The mecak8s HTTP edge supplies that bound; the Helm probe timeout is longer so
// it receives the resulting 503 instead of abandoning the request first.
func TestStorageReadySlowStoreRespectsCallerDeadline(t *testing.T) {
	st := &slowPingingStore{Store: memstore.New(), started: make(chan struct{})}
	svc := newStorageReadyTestService(t, st)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	if svc.StorageReady(ctx) {
		t.Fatal("StorageReady on a deadline-blocked store = true, want false")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("StorageReady context error = %v, want deadline exceeded", ctx.Err())
	}
	select {
	case <-st.started:
	default:
		t.Fatal("StorageReady did not call the store pinger")
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("StorageReady exceeded the caller bound by too much: %v", elapsed)
	}
}

type drainPersistBarrierStore struct {
	*memstore.Store
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
	fail    bool
}

func (s *drainPersistBarrierStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	entered, release, fail := s.entered, s.release, s.fail
	s.entered = nil
	s.mu.Unlock()
	if entered != nil {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if fail {
			return errors.New("scripted awaiting persistence failure")
		}
	}
	return s.Store.Save(ctx, sess)
}

func (s *drainPersistBarrierStore) arm(fail bool) (<-chan struct{}, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entered = make(chan struct{})
	s.release = make(chan struct{})
	s.fail = fail
	return s.entered, s.release
}

func TestADR_0294_AwaitingPersistAndDrainLifecycleIsAtomic(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fail      bool
		wantState session.State
	}{
		{name: "successful awaiting save is preserved", wantState: session.StateAwaiting},
		{name: "failed awaiting save is cancelled and settled", fail: true, wantState: session.StateCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := memstore.New()
			store := &drainPersistBarrierStore{Store: base}
			lease := &fakeLease{}
			cat := tool.NewCatalog()
			cat.MustRegister(&writeAskTool{})
			eng := agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("atomic-call", "Write", json.RawMessage(`{}`)))),
				Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test-model",
			})
			svc, err := server.NewService(server.Config{
				Engine: eng, Store: store, SessionLease: lease, LeaseOwner: "atomic-drain",
				LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
				PlacementProvider: testPlacementProvider{root: "/ws"},
				PlacementScope:    "test",
				SharedEngineRoot:  "/ws",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.StartRun(context.Background(), sess.ID, "park")
			if err != nil {
				t.Fatal(err)
			}
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk {
					break
				}
			}
			entered, release := store.arm(tc.fail)
			persisted := make(chan struct{})
			go func() {
				svc.Persist(context.Background(), sess.ID)
				close(persisted)
			}()
			<-entered
			joined := make(chan struct{})
			go func() {
				for range run.Events() {
				}
				svc.FinishRun(sess.ID, run)
				close(joined)
			}()
			drained := make(chan error, 1)
			go func() { drained <- svc.GracefulDrain(context.Background()) }()
			close(release)
			<-persisted
			if err := <-drained; err != nil {
				t.Fatalf("GracefulDrain: %v", err)
			}
			<-joined
			got, err := base.Load(context.Background(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != tc.wantState {
				t.Fatalf("durable state = %s, want %s", got.State, tc.wantState)
			}
		})
	}
}

type drainFailStore struct {
	*memstore.Store
	mu      sync.Mutex
	fail    bool
	attempt bool
}

func (s *drainFailStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	fail := s.fail
	if fail {
		s.attempt = true
	}
	s.mu.Unlock()
	if fail {
		return errors.New("scripted drain persistence failure")
	}
	return s.Store.Save(ctx, sess)
}

func (s *drainFailStore) failSaves() {
	s.mu.Lock()
	s.fail = true
	s.mu.Unlock()
}

func (s *drainFailStore) attempted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempt
}

type drainDiagnostics struct {
	mu       sync.Mutex
	messages []string
}

func (d *drainDiagnostics) Log(_ context.Context, _ port.Level, msg string, _ ...any) {
	d.mu.Lock()
	d.messages = append(d.messages, msg)
	d.mu.Unlock()
}

func (d *drainDiagnostics) With(...any) port.Diagnostics { return d }

func (d *drainDiagnostics) contains(want string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, msg := range d.messages {
		if strings.Contains(msg, want) {
			return true
		}
	}
	return false
}

func newGracefulDrainService(t *testing.T, store port.SessionStore, lease port.SessionLease, llm port.LLMProvider, diag port.Diagnostics) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{
		LLM:         llm,
		Catalog:     tool.NewCatalog(),
		Policy:      permpolicy.NewPolicy(nil, permstore.New()),
		Model:       "test-model",
		Diagnostics: diag,
	})
	svc, err := server.NewService(server.Config{
		Engine:             eng,
		Store:              store,
		PlacementProvider:  testPlacementProvider{root: "/ws"},
		PlacementScope:     "test",
		SharedEngineRoot:   "/ws",
		SessionLease:       lease,
		LeaseOwner:         "drain-owner",
		LeaseTTL:           time.Hour,
		LeaseRenewInterval: 30 * time.Minute,
		Diagnostics:        diag,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func makeAwaitingSession(t *testing.T, id session.SessionID) (*session.Session, session.PendingAsk) {
	t.Helper()
	sess := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{MaxTurns: 3}, time.Unix(0, 0))
	if err := sess.RecordUserPrompt("durable request", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("durable-call", "Write", json.RawMessage(`{"path":"f.go"}`))
	if err := sess.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	ask := session.PendingAsk{AskID: "durable-ask", Tool: "Write", Call: call.ID}
	if err := sess.PauseForApproval(ask); err != nil {
		t.Fatal(err)
	}
	return sess, ask
}

func TestADR_0294_DrainStopsAdmissionBeforeOwnershipChange(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	svc := newGracefulDrainService(t, store, lease, mockllm.New(mockllm.TextTurn("unused")), port.NopDiagnostics{})
	defer svc.Close()

	idle, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	awaiting, _ := makeAwaitingSession(t, "awaiting-drain-admission")
	if err := store.Save(context.Background(), awaiting); err != nil {
		t.Fatal(err)
	}

	svc.Drain()
	checks := []struct {
		name string
		call func() error
	}{
		{"prompt", func() error { _, err := svc.StartRun(context.Background(), idle.ID, "blocked"); return err }},
		{"retry", func() error { _, err := svc.RetryFailedRun(context.Background(), idle.ID); return err }},
		{"resume", func() error {
			_, err := svc.ApproveRun(context.Background(), awaiting.ID, "durable-ask", session.VerdictAllowOnce, "")
			return err
		}},
		{"mutation", func() error { _, err := svc.SetMode(context.Background(), idle.ID, session.ModePlan); return err }},
	}
	for _, check := range checks {
		if err := check.call(); !errors.Is(err, server.ErrUnavailable) {
			t.Errorf("%s admission after drain = %v, want ErrUnavailable", check.name, err)
		}
	}
	lease.mu.Lock()
	acquires := lease.acquires
	lease.mu.Unlock()
	if acquires != 0 {
		t.Fatalf("drained admissions acquired ownership %d times, want 0", acquires)
	}
}

func TestADR_0294_ShutdownPreservesAwaitingDurableEventProjection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown func(*server.Service) error
	}{
		{name: "graceful drain", shutdown: func(svc *server.Service) error { return svc.GracefulDrain(context.Background()) }},
		{name: "close", shutdown: func(svc *server.Service) error { svc.Close(); return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lease := &fakeLease{}
			store := memstore.New()
			eventLog := memstore.NewEventLog()
			capability := server.NewSessionMutationCapability(true)
			cat := tool.NewCatalog()
			cat.MustRegister(&writeAskTool{})
			eng := agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("relay-call", "Write", json.RawMessage(`{}`)))),
				Catalog: cat, Policy: permpolicy.NewPolicy(nil, permstore.New()), Model: "test-model",
				Store: capability.GuardStore(store),
			})
			svc, err := server.NewService(server.Config{
				Engine: eng, Store: store, EventLog: eventLog, SessionLease: lease, LeaseOwner: "relay-drain",
				MutationCapability: capability,
				LeaseTTL:           time.Hour, LeaseRenewInterval: time.Hour,
				PlacementProvider: testPlacementProvider{root: "/ws"},
				PlacementScope:    "test",
				SharedEngineRoot:  "/ws",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			client, cleanup := dialGRPC(t, svc)
			defer cleanup()
			created, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
			if err != nil {
				t.Fatal(err)
			}
			id := session.SessionID(created.GetSessionId())
			stream, err := client.Converse(affinityContext(string(id)))
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: "park"}}}); err != nil {
				t.Fatal(err)
			}
			var askID string
			for askID == "" {
				resp, recvErr := stream.Recv()
				if recvErr != nil {
					t.Fatalf("receive ask: %v", recvErr)
				}
				if ask := resp.GetEvent().GetAsk(); ask != nil {
					askID = ask.GetAskId()
				}
			}

			shutdownDone := make(chan error, 1)
			go func() { shutdownDone <- tc.shutdown(svc) }()
			liveCancelled := false
			for {
				resp, recvErr := stream.Recv()
				if recvErr != nil {
					break
				}
				if result := resp.GetEvent().GetResult(); result != nil && result.GetStop() == string(session.StopCancelled) {
					liveCancelled = true
				}
			}
			if err := <-shutdownDone; err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			if !liveCancelled {
				t.Fatal("live relay did not deliver shutdown cancellation")
			}

			got, err := store.Load(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			pending, ok := got.PendingAsk()
			if got.State != session.StateAwaiting || !ok || pending.AskID != askID {
				t.Fatalf("relay-backed durable resume point changed: state=%q ask=%+v ok=%t", got.State, pending, ok)
			}

			var durableAsk bool
			for ev, readErr := range eventLog.Read(context.Background(), id) {
				if readErr != nil {
					t.Fatalf("read event log: %v", readErr)
				}
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.AskID == askID {
					durableAsk = true
				}
				if ev.Type == session.EvPermissionRetract && ev.Ask != nil && ev.Ask.AskID == askID {
					t.Fatalf("durable replay retracted preserved ask %q", askID)
				}
				if ev.Type == session.EvResult && ev.Result != nil && ev.Result.Stop == session.StopCancelled {
					t.Fatal("durable replay terminated the preserved awaiting run")
				}
			}
			if !durableAsk {
				t.Fatalf("durable replay omitted unresolved ask %q", askID)
			}
		})
	}
}

func TestADR_0294_DrainPreservesAwaitingResumePoint(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	svc := newGracefulDrainService(t, store, lease, mockllm.New(mockllm.TextTurn("done")), port.NopDiagnostics{})
	defer svc.Close()

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "acquire ownership")
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
	awaiting, wantAsk := makeAwaitingSession(t, sess.ID)
	if err := store.Save(context.Background(), awaiting); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.GracefulDrain(ctx); err != nil {
		t.Fatalf("GracefulDrain: %v", err)
	}
	got, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	gotAsk, ok := got.PendingAsk()
	if got.State != session.StateAwaiting || !ok || gotAsk.AskID != wantAsk.AskID || gotAsk.Call != wantAsk.Call {
		t.Fatalf("durable resume point changed: state=%q ask=%+v ok=%t", got.State, gotAsk, ok)
	}
	if lease.releaseCount() != 1 {
		t.Fatalf("lease releases = %d, want 1", lease.releaseCount())
	}
}

func TestSessionAffinityAndHandoff_Scenario6_DrainCancelsJoinsAndDiagnosesPersistFailure(t *testing.T) {
	lease := &fakeLease{}
	store := &drainFailStore{Store: memstore.New()}
	diag := &drainDiagnostics{}
	svc := newGracefulDrainService(t, store, lease, blockingProvider{}, diag)
	defer svc.Close()

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRun(context.Background(), sess.ID, "execute")
	if err != nil {
		t.Fatal(err)
	}
	store.failSaves()
	drained := make(chan struct{})
	go func() {
		for range run.Events() {
		}
		close(drained)
		svc.FinishRun(sess.ID, run)
	}()
	lease.releaseHook = func(port.Lease) error {
		if !store.attempted() {
			t.Error("lease released before terminal persistence attempt")
		}
		select {
		case <-drained:
		default:
			t.Error("lease released before run joined")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.GracefulDrain(ctx); err != nil {
		t.Fatalf("GracefulDrain: %v", err)
	}
	if lease.releaseCount() != 1 {
		t.Fatalf("lease releases = %d, want 1", lease.releaseCount())
	}
	if !diag.contains("drain persistence failed") {
		t.Fatalf("diagnostics = %v, want drain persistence failure", diag.messages)
	}
	prior, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prior.State != session.StateIdle {
		t.Fatalf("prior durable state = %q, want idle", prior.State)
	}
}

func TestADR_0294_DrainSettlesReadyRunsWithoutMapOrderStarvation(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	svc := newGracefulDrainService(t, store, lease, blockingProvider{}, port.NopDiagnostics{})
	defer svc.Close()

	const runCount = 9
	runs := make([]*agent.Run, 0, runCount)
	ids := make([]session.SessionID, 0, runCount)
	for range runCount {
		sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		run, err := svc.StartRun(context.Background(), sess.ID, "execute")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
		runs = append(runs, run)
	}

	ctx, cancel := context.WithCancel(context.Background())
	drainDone := make(chan error, 1)
	go func() { drainDone <- svc.GracefulDrain(ctx) }()
	for _, run := range runs {
		for range run.Events() {
		}
	}
	// Only one relay joins. The other eight remain genuinely unjoined and must not
	// starve this ready lifecycle merely because map iteration sees them first.
	svc.FinishRun(ids[0], runs[0])
	deadline := time.After(time.Second)
	for lease.releaseCount() == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("settled run was starved behind unjoined map entries")
		case <-time.After(time.Millisecond):
		}
	}
	if got := lease.lastReleasedLease().SessionID; got != ids[0] {
		t.Fatalf("released session = %q, want settled %q", got, ids[0])
	}
	cancel()
	if err := <-drainDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("GracefulDrain = %v, want context.Canceled for remaining unjoined runs", err)
	}
	if got := lease.releaseCount(); got != 1 {
		t.Fatalf("lease releases = %d, want only the one settled run", got)
	}
}

func TestADR_0294_DrainTimeoutRetainsLeaseForTTLTakeover(t *testing.T) {
	lease := &fakeLease{}
	store := memstore.New()
	svc := newGracefulDrainService(t, store, lease, blockingProvider{}, port.NopDiagnostics{})

	sess, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartRun(context.Background(), sess.ID, "execute"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := svc.GracefulDrain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GracefulDrain = %v, want context.Canceled", err)
	}
	svc.Close()
	if lease.releaseCount() != 0 {
		t.Fatalf("timed-out drain explicitly released lease %d times, want 0", lease.releaseCount())
	}
}
