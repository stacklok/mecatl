package server_test

import (
	"context"
	"errors"
	"iter"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const crashOwnershipTTL = time.Minute

type crashOwnershipClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *crashOwnershipClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *crashOwnershipClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type crashOwnershipLease struct {
	port.SessionLease
	releases atomic.Int64
}

func (l *crashOwnershipLease) Release(ctx context.Context, held port.Lease) error {
	l.releases.Add(1)
	return l.SessionLease.Release(ctx, held)
}

type countingRedisStore struct {
	store     *redisstore.Store
	mutations atomic.Int64
}

func (s *countingRedisStore) Save(ctx context.Context, sess *session.Session) error {
	s.mutations.Add(1)
	return s.store.Save(ctx, sess)
}

func (s *countingRedisStore) Create(ctx context.Context, sess *session.Session) error {
	s.mutations.Add(1)
	return s.store.Create(ctx, sess)
}

func (s *countingRedisStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.store.Load(ctx, id)
}

type crashBlockingProvider struct {
	calls   atomic.Int64
	entered chan struct{}
	once    sync.Once
}

func newCrashBlockingProvider() *crashBlockingProvider {
	return &crashBlockingProvider{entered: make(chan struct{})}
}

func (p *crashBlockingProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done()
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (*crashBlockingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

type crashOwnershipFixture struct {
	clock            *crashOwnershipClock
	lease            *crashOwnershipLease
	ownerStore       *countingRedisStore
	survivorStore    *countingRedisStore
	owner            *server.Service
	survivor         *server.Service
	ownerProvider    *crashBlockingProvider
	survivorProvider *crashBlockingProvider
	factoryCalls     atomic.Int64
	sessionID        session.SessionID
	ownerRun         *agent.Run
}

func newCrashOwnershipFixture(t *testing.T) *crashOwnershipFixture {
	t.Helper()
	mr := miniredis.RunT(t)
	ownerRedis, err := redisstore.New(mr.Addr())
	if err != nil {
		t.Fatalf("new owner Redis store: %v", err)
	}
	survivorRedis, err := redisstore.New(mr.Addr())
	if err != nil {
		_ = ownerRedis.Close()
		t.Fatalf("new survivor Redis store: %v", err)
	}
	clock := &crashOwnershipClock{now: time.Unix(1_700_000_000, 0)}
	lease := &crashOwnershipLease{SessionLease: memlease.New(clock, crashOwnershipTTL)}
	f := &crashOwnershipFixture{
		clock:            clock,
		lease:            lease,
		ownerStore:       &countingRedisStore{store: ownerRedis},
		survivorStore:    &countingRedisStore{store: survivorRedis},
		ownerProvider:    newCrashBlockingProvider(),
		survivorProvider: newCrashBlockingProvider(),
	}
	f.owner = f.newService(t, "killed-owner", f.ownerStore, f.ownerProvider, nil, func() session.SessionID { return "crash-owned-session" })
	f.survivor = f.newService(t, "survivor", f.survivorStore, f.survivorProvider, &f.factoryCalls, nil)
	t.Cleanup(func() {
		f.owner.Close()
		f.survivor.Close()
		_ = ownerRedis.Close()
		_ = survivorRedis.Close()
	})
	return f
}

func (f *crashOwnershipFixture) newService(
	t *testing.T,
	owner string,
	store *countingRedisStore,
	provider port.LLMProvider,
	factoryCalls *atomic.Int64,
	newID func() session.SessionID,
) *server.Service {
	t.Helper()
	capability := server.NewSessionMutationCapability(true)
	newEngine := func(llm port.LLMProvider) *agent.Engine {
		return agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "test-model",
			Store:   capability.GuardStore(store),
		})
	}
	factory := func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, _ server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
		if factoryCalls != nil {
			factoryCalls.Add(1)
		}
		return server.SessionEngineResult{Engine: newEngine(provider), Close: func() error { return nil }}, nil
	}
	cfg := server.Config{
		Engine:             newEngine(provider),
		Store:              store,
		Workspaces:         func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		SessionEngine:      factory,
		SessionLease:       f.lease,
		LeaseOwner:         owner,
		LeaseTTL:           crashOwnershipTTL,
		LeaseRenewInterval: time.Hour,
		MutationCapability: capability,
		Now:                f.clock.Now,
		NewID:              newID,
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatalf("new %s Service: %v", owner, err)
	}
	return svc
}

func (f *crashOwnershipFixture) startOwnerAndDropStream(t *testing.T) {
	t.Helper()
	sess, err := f.owner.CreateSessionWithProvider(context.Background(), "/workspace", session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: "modeled", ModelID: "blocked"})
	if err != nil {
		t.Fatalf("create owner session: %v", err)
	}
	f.sessionID = sess.ID
	f.ownerRun, err = f.owner.StartRunContent(context.Background(), sess.ID, "owner work", nil)
	if err != nil {
		t.Fatalf("start owner run: %v", err)
	}
	select {
	case <-f.ownerProvider.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("owner provider was not entered")
	}
	// Capture the last authoritative in-flight snapshot exactly as a relay-side
	// persistence point would; the modeled crash happens immediately afterward.
	f.owner.Persist(context.Background(), f.sessionID)
	f.waitRedisState(t, session.StateRunning)

	// Model the killed client's stream disappearing: observe only what was already
	// delivered, then abandon the stream without Cancel, FinishRun, Close, or Release.
	for {
		select {
		case ev := <-f.ownerRun.Events():
			if ev.Type == session.EvResult {
				t.Fatalf("killed owner stream fabricated terminal result: %#v", ev.Result)
			}
		default:
			if f.lease.releases.Load() != 0 {
				t.Fatalf("hard-dead owner released lease %d times, want 0", f.lease.releases.Load())
			}
			return
		}
	}
}

func (f *crashOwnershipFixture) waitRedisState(t *testing.T, want session.State) *session.Session {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		persisted, err := f.survivorStore.Load(context.Background(), f.sessionID)
		if err != nil {
			t.Fatalf("load authoritative Redis snapshot: %v", err)
		}
		if persisted.State == want {
			return persisted
		}
		if time.Now().After(deadline) {
			t.Fatalf("authoritative Redis snapshot stayed %q, want %q", persisted.State, want)
		}
		runtime.Gosched()
	}
}

func (f *crashOwnershipFixture) assertKilledOwnerSnapshot(t *testing.T) {
	t.Helper()
	persisted := f.waitRedisState(t, session.StateRunning)
	if stop, ok := persisted.StopReason(); ok || stop != session.StopNone {
		t.Fatalf("snapshot stop after hard death = %q (present=%t), want none", stop, ok)
	}
	if f.ownerProvider.calls.Load() != 1 {
		t.Fatalf("owner provider calls = %d, want 1", f.ownerProvider.calls.Load())
	}
}

func (f *crashOwnershipFixture) assertPreTTLBlocked(t *testing.T) {
	t.Helper()
	before, err := f.survivorStore.Load(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("load pre-TTL snapshot: %v", err)
	}
	f.survivorStore.mutations.Store(0)
	_, err = f.survivor.StartRunContent(context.Background(), f.sessionID, "premature survivor", nil)
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("pre-TTL run entry = %v, want ErrSessionLeasedElsewhere", err)
	}
	after, loadErr := f.survivorStore.Load(context.Background(), f.sessionID)
	if loadErr != nil {
		t.Fatalf("reload pre-TTL snapshot: %v", loadErr)
	}
	if f.survivorStore.mutations.Load() != 0 {
		t.Fatalf("pre-TTL survivor mutations = %d, want 0", f.survivorStore.mutations.Load())
	}
	if f.factoryCalls.Load() != 0 {
		t.Fatalf("pre-TTL rehydrations = %d, want 0", f.factoryCalls.Load())
	}
	if f.survivorProvider.calls.Load() != 0 {
		t.Fatalf("pre-TTL provider calls = %d, want 0", f.survivorProvider.calls.Load())
	}
	beforeStop, beforeStopped := before.StopReason()
	afterStop, afterStopped := after.StopReason()
	if before.State != after.State || beforeStop != afterStop || beforeStopped != afterStopped || !reflect.DeepEqual(before.Conversation.Messages, after.Conversation.Messages) {
		t.Fatalf("pre-TTL request changed authoritative snapshot: before state=%q stop=%q/%t messages=%d; after state=%q stop=%q/%t messages=%d",
			before.State, beforeStop, beforeStopped, len(before.Conversation.Messages), after.State, afterStop, afterStopped, len(after.Conversation.Messages))
	}
}

func (f *crashOwnershipFixture) advancePastTTL() {
	f.clock.advance(crashOwnershipTTL + time.Nanosecond)
}

func (f *crashOwnershipFixture) assertOneConcurrentSurvivor(t *testing.T) {
	t.Helper()
	type result struct {
		run *agent.Run
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, prompt := range []string{"survivor one", "survivor two"} {
		prompt := prompt
		go func() {
			<-start
			run, err := f.survivor.StartRunContent(context.Background(), f.sessionID, prompt, nil)
			results <- result{run: run, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	got := []result{first, second}
	var winner *agent.Run
	losers := 0
	for _, res := range got {
		switch {
		case res.err == nil && res.run != nil:
			if winner != nil {
				t.Fatal("both post-TTL requests acquired and started")
			}
			winner = res.run
		case errors.Is(res.err, server.ErrFailedPrecondition):
			losers++
		default:
			t.Fatalf("post-TTL contender result = run %v err %v", res.run, res.err)
		}
	}
	if winner == nil || losers != 1 {
		t.Fatalf("post-TTL winners/losers = %t/%d, want exactly 1/1", winner != nil, losers)
	}
	select {
	case <-f.survivorProvider.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("winning survivor did not reach provider")
	}
	if f.factoryCalls.Load() != 1 {
		t.Fatalf("post-TTL rehydrations = %d, want exactly 1", f.factoryCalls.Load())
	}
	if f.survivorProvider.calls.Load() != 1 {
		t.Fatalf("post-TTL provider calls = %d, want exactly 1", f.survivorProvider.calls.Load())
	}
	f.survivor.Persist(context.Background(), f.sessionID)
	persisted := f.waitRedisState(t, session.StateRunning)
	if persisted.State != session.StateRunning {
		t.Fatalf("post-takeover snapshot state = %q, want running", persisted.State)
	}
	if _, err := f.lease.Acquire(context.Background(), f.sessionID, "third-owner"); !errors.Is(err, port.ErrLeaseHeld) {
		t.Fatalf("lease after survivor takeover = %v, want ErrLeaseHeld", err)
	}

	winner.Cancel()
	for range winner.Events() {
	}
	f.survivor.FinishRun(f.sessionID, winner)
}

func TestSessionAffinityAndHandoff_Scenario7_KilledOwnerDropsStream(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.assertKilledOwnerSnapshot(t)
}

func TestADR_0290_PreTTLRequestsCannotAcquireOrRun(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.assertPreTTLBlocked(t)
}

func TestADR_0290_PostTTLSingleSurvivorAcquires(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.advancePastTTL()
	f.assertOneConcurrentSurvivor(t)
}
