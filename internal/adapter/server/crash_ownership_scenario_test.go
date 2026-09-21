package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
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
	releases      atomic.Int64
	rejectRenew   atomic.Bool
	renewAttempts atomic.Int64
}

func (l *crashOwnershipLease) Renew(ctx context.Context, held port.Lease) (port.Lease, error) {
	l.renewAttempts.Add(1)
	if l.rejectRenew.Load() {
		return port.Lease{}, port.ErrLeaseHeld
	}
	return l.SessionLease.Renew(ctx, held)
}

func (l *crashOwnershipLease) Release(ctx context.Context, held port.Lease) error {
	l.releases.Add(1)
	return l.SessionLease.Release(ctx, held)
}

type saveObservation struct {
	state        session.State
	messages     []session.Message
	pairingError error
}

type countingRedisStore struct {
	store     *redisstore.Store
	mutations atomic.Int64
	mu        sync.Mutex
	saves     []saveObservation
}

func (s *countingRedisStore) Save(ctx context.Context, sess *session.Session) error {
	s.mutations.Add(1)
	messages := session.CloneMessages(sess.Conversation.Messages)
	s.mu.Lock()
	s.saves = append(s.saves, saveObservation{
		state:        sess.State,
		messages:     messages,
		pairingError: session.ValidateToolPairing(messages),
	})
	s.mu.Unlock()
	return s.store.Save(ctx, sess)
}

func (s *countingRedisStore) Create(ctx context.Context, sess *session.Session) error {
	s.mutations.Add(1)
	return s.store.Create(ctx, sess)
}

func (s *countingRedisStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.store.Load(ctx, id)
}

func (s *countingRedisStore) resetSaveObservations() {
	s.mu.Lock()
	s.saves = nil
	s.mu.Unlock()
}

func (s *countingRedisStore) saveObservations() []saveObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]saveObservation(nil), s.saves...)
}

type crashBlockingProvider struct {
	calls    atomic.Int64
	entered  chan struct{}
	once     sync.Once
	delegate port.LLMProvider
}

func newCrashBlockingProvider() *crashBlockingProvider {
	return &crashBlockingProvider{entered: make(chan struct{})}
}

func (p *crashBlockingProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	p.calls.Add(1)
	p.once.Do(func() { close(p.entered) })
	if p.delegate != nil {
		return p.delegate.Stream(ctx, req)
	}
	return func(yield func(port.Chunk, error) bool) {
		<-ctx.Done()
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopCancelled}, nil)
	}, nil
}

func (*crashBlockingProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

type crashOwnershipFixture struct {
	redis            *miniredis.Miniredis
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
		redis:            mr,
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
		PlacementProvider:  testPlacementProvider{root: "/workspace", firstBind: &atomic.Bool{}},
		PlacementScope:     "test",
		SharedEngineRoot:   "/workspace",
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
	sess, err := f.owner.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{ProviderID: "modeled", ModelID: "blocked"})
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

const (
	crashOrphanCallID = "crash-side-effect-call"
	crashSidecarText  = "owner-sidecar-before-crash"
)

type handoffProviderCapture struct {
	header            string
	contextSessionID  session.SessionID
	persistedState    session.State
	persistedMessages []session.Message
}

type handoffHTTPProvider struct {
	client    *http.Client
	endpoint  string
	mu        sync.Mutex
	contextID session.SessionID
}

func (p *handoffHTTPProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	id, _ := port.SessionIDFromContext(ctx)
	p.mu.Lock()
	p.contextID = id
	p.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, nil)
	if err != nil {
		return nil, err
	}
	if value := string(id); sessionaffinity.ValidValue(value) {
		req.Header.Set(sessionaffinity.HeaderName, value)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	return func(yield func(port.Chunk, error) bool) {
		if !yield(port.Chunk{Kind: port.ChunkText, Text: "continued after takeover"}, nil) {
			return
		}
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func (*handoffHTTPProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *handoffHTTPProvider) sessionID() session.SessionID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contextID
}

func (f *crashOwnershipFixture) seedCrashOrphanWithRedisSidecars(t *testing.T) {
	t.Helper()
	persisted := f.waitRedisState(t, session.StateRunning)
	call := session.NewToolCall(crashOrphanCallID, "ExternalWrite", nil)
	if err := persisted.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatalf("record crash-orphaned tool call: %v", err)
	}
	if err := f.ownerStore.Save(context.Background(), persisted); err != nil {
		t.Fatalf("persist crash-orphaned snapshot: %v", err)
	}
	if err := f.ownerStore.store.Append(context.Background(), f.sessionID, session.Event{Type: session.EvMessageDelta, Text: crashSidecarText}); err != nil {
		t.Fatalf("append pre-crash event sidecar: %v", err)
	}
	f.ownerStore.store.ToolCall(f.sessionID, call, session.NewToolResult(call.ID, "external side effect observed"), 0, time.Millisecond)
	f.survivorStore.resetSaveObservations()
}

func (f *crashOwnershipFixture) continueThroughHTTPProvider(t *testing.T, prompt string) (*agent.Run, handoffProviderCapture) {
	t.Helper()
	captures := make(chan handoffProviderCapture, 1)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		persisted, err := f.survivorStore.Load(r.Context(), f.sessionID)
		if err != nil {
			captures <- handoffProviderCapture{header: r.Header.Get(sessionaffinity.HeaderName)}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		captures <- handoffProviderCapture{
			header:            r.Header.Get(sessionaffinity.HeaderName),
			persistedState:    persisted.State,
			persistedMessages: session.CloneMessages(persisted.Conversation.Messages),
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(providerServer.Close)
	provider := &handoffHTTPProvider{client: providerServer.Client(), endpoint: providerServer.URL}
	f.survivorProvider.delegate = provider

	run, err := f.survivor.StartRunContent(context.Background(), f.sessionID, prompt, nil)
	if err != nil {
		t.Fatalf("successor StartRunContent: %v", err)
	}
	var capture handoffProviderCapture
	select {
	case capture = <-captures:
	case <-time.After(3 * time.Second):
		t.Fatal("successor provider request was not captured")
	}
	capture.contextSessionID = provider.sessionID()
	for range run.Events() {
	}
	f.survivor.FinishRun(f.sessionID, run)
	return run, capture
}

func (f *crashOwnershipFixture) assertRepairWasFirstSuccessorSave(t *testing.T) {
	t.Helper()
	saves := f.survivorStore.saveObservations()
	if len(saves) == 0 {
		t.Fatal("successor persisted no snapshots")
	}
	first := saves[0]
	if first.state != session.StateIdle {
		t.Fatalf("first successor save state = %q, want repaired idle before continuation", first.state)
	}
	if first.pairingError != nil {
		t.Fatalf("first successor save did not persist tool-pair closure: %v", first.pairingError)
	}
	const abandonResult = "tool call aborted: the process driving this run exited before this call's result was recorded"
	for _, message := range first.messages {
		if message.ToolResult != nil && message.ToolResult.CallID == crashOrphanCallID {
			if !message.ToolResult.IsError || message.ToolResult.Content != abandonResult {
				t.Fatalf("orphaned call repair = error %t content %q, want Session.Abandon result %q", message.ToolResult.IsError, message.ToolResult.Content, abandonResult)
			}
			return
		}
	}
	t.Fatalf("first successor save has no synthetic result for orphaned call %q", crashOrphanCallID)
}

func (f *crashOwnershipFixture) assertRedisSidecarsSurvivedReload(t *testing.T) {
	t.Helper()
	var foundEvent bool
	for event, err := range f.survivorStore.store.Read(context.Background(), f.sessionID) {
		if err != nil {
			t.Fatalf("read Redis event sidecar through successor: %v", err)
		}
		if event.Text == crashSidecarText {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("successor Redis reload did not see event sidecar %q", crashSidecarText)
	}
	tools, err := f.redis.List("mecatl:store:v2:tools:" + string(f.sessionID))
	if err != nil {
		t.Fatalf("read Redis tool sidecar: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("successor Redis reload saw %d tool sidecar records, want 1", len(tools))
	}
	var record struct {
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal([]byte(tools[0]), &record); err != nil {
		t.Fatalf("decode Redis tool sidecar: %v", err)
	}
	if record.CallID != crashOrphanCallID {
		t.Fatalf("successor Redis tool sidecar call ID = %q, want %q", record.CallID, crashOrphanCallID)
	}
}

func newCrashAwaitingService(
	t *testing.T,
	store port.SessionStore,
	lease port.SessionLease,
	owner string,
	llm port.LLMProvider,
	ran *atomic.Int64,
	newID func() session.SessionID,
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
		PlacementProvider:  testPlacementProvider{root: "/workspace", firstBind: &atomic.Bool{}},
		PlacementScope:     "test",
		SharedEngineRoot:   "/workspace",
		SessionLease:       lease,
		LeaseOwner:         owner,
		LeaseTTL:           crashOwnershipTTL,
		LeaseRenewInterval: time.Millisecond,
		MutationCapability: capability,
		NewID:              newID,
	})
	if err != nil {
		t.Fatalf("new %s awaiting service: %v", owner, err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func TestSessionAffinityAndHandoff_Scenario7_AwaitingLeaseLossHandoff(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	var staleRan, successorRan atomic.Int64
	ownerLLM := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("write-handoff", "Write", json.RawMessage(`{"path":"handoff.go"}`))),
		mockllm.TextTurn("stale owner must not continue"),
	)
	successorLLM := mockllm.New(mockllm.TextTurn("continued by successor"))
	owner := newCrashAwaitingService(t, f.ownerStore, f.lease, "awaiting-owner", ownerLLM, &staleRan, func() session.SessionID {
		return "awaiting-handoff-session"
	})
	successor := newCrashAwaitingService(t, f.survivorStore, f.lease, "awaiting-successor", successorLLM, &successorRan, nil)

	sess, err := owner.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("create awaiting owner session: %v", err)
	}
	f.sessionID = sess.ID
	run, err := owner.StartRun(context.Background(), sess.ID, "write after approval")
	if err != nil {
		t.Fatalf("start awaiting owner run: %v", err)
	}

	var pending session.PendingAsk
	deadline := time.After(3 * time.Second)
	for pending.AskID == "" {
		select {
		case ev := <-run.Events():
			if ev.Type != session.EvPermissionAsk || ev.Ask == nil {
				continue
			}
			owner.Persist(context.Background(), sess.ID)
			parked, loadErr := f.survivorStore.Load(context.Background(), sess.ID)
			if loadErr != nil {
				t.Fatalf("load durable awaiting snapshot: %v", loadErr)
			}
			var ok bool
			pending, ok = parked.PendingAsk()
			if parked.State != session.StateAwaiting || !ok {
				t.Fatalf("durable snapshot = state %q pending=%t, want awaiting PendingAsk", parked.State, ok)
			}
		case <-deadline:
			t.Fatal("timed out waiting for durable permission ask")
		}
	}
	parked, err := f.survivorStore.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("reload durable awaiting snapshot: %v", err)
	}
	before, err := sessnap.Marshal(parked)
	if err != nil {
		t.Fatalf("marshal durable awaiting snapshot: %v", err)
	}

	if _, err := successor.ApproveRun(context.Background(), sess.ID, pending.AskID, session.VerdictAllowOnce, ""); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("pre-TTL successor resolved ask: %v", err)
	}
	f.lease.rejectRenew.Store(true)
	var retracts int
	var staleStop session.StopReason
	deadline = time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-run.Events():
			if !ok {
				owner.FinishRun(sess.ID, run)
				goto staleStopped
			}
			if ev.Type == session.EvPermissionRetract && ev.Ask != nil && ev.Ask.AskID == pending.AskID {
				retracts++
			}
			if ev.Type == session.EvResult && ev.Result != nil {
				staleStop = ev.Result.Stop
			}
		case <-deadline:
			t.Fatal("timed out waiting for renewal-loss invalidation")
		}
	}

staleStopped:
	if f.lease.renewAttempts.Load() == 0 {
		t.Fatal("owner stopped without a lease renewal attempt")
	}
	if retracts != 1 {
		t.Fatalf("local permission retracts = %d, want exactly 1", retracts)
	}
	if staleStop != session.StopCancelled {
		t.Fatalf("stale local run stop = %q, want %q", staleStop, session.StopCancelled)
	}
	if staleRan.Load() != 0 {
		t.Fatalf("stale owner executed pending tool %d times, want 0", staleRan.Load())
	}
	if got := f.lease.releases.Load(); got != 0 {
		t.Fatalf("renewal-lost owner released lease %d times, want 0", got)
	}
	if _, err := owner.ApproveRun(context.Background(), sess.ID, pending.AskID, session.VerdictAllowOnce, ""); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("stale owner resolved ask after lease loss: %v", err)
	}
	if _, err := successor.ApproveRun(context.Background(), sess.ID, pending.AskID, session.VerdictAllowOnce, ""); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("pre-TTL successor resolved ask after owner loss: %v", err)
	}
	afterLoss, err := f.survivorStore.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("load snapshot after renewal loss: %v", err)
	}
	afterLossBytes, err := sessnap.Marshal(afterLoss)
	if err != nil {
		t.Fatalf("marshal snapshot after renewal loss: %v", err)
	}
	if !reflect.DeepEqual(afterLossBytes, before) {
		t.Fatalf("renewal loss changed durable awaiting snapshot\n before: %s\n after:  %s", before, afterLossBytes)
	}
	gotPending, ok := afterLoss.PendingAsk()
	if !ok || !reflect.DeepEqual(gotPending, pending) {
		t.Fatalf("renewal loss replaced PendingAsk: got %#v present=%t, want %#v", gotPending, ok, pending)
	}

	f.advancePastTTL()
	f.lease.rejectRenew.Store(false)
	type resumeResult struct {
		run *agent.Run
		err error
	}
	start := make(chan struct{})
	results := make(chan resumeResult, 2)
	for range 2 {
		go func() {
			<-start
			resumed, resumeErr := successor.ApproveRun(context.Background(), sess.ID, pending.AskID, session.VerdictAllowOnce, "")
			results <- resumeResult{run: resumed, err: resumeErr}
		}()
	}
	close(start)
	var resumed *agent.Run
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("post-TTL successor resume: %v", result.err)
		}
		if result.run != nil {
			if resumed != nil {
				t.Fatal("more than one successor resumed the durable ask")
			}
			resumed = result.run
		}
	}
	if resumed == nil {
		t.Fatal("no successor resumed the durable ask")
	}
	var successorStop session.StopReason
	for ev := range resumed.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			successorStop = ev.Result.Stop
		}
	}
	successor.FinishRun(sess.ID, resumed)
	if successorRan.Load() != 1 {
		t.Fatalf("successor executed pending tool %d times, want exactly 1", successorRan.Load())
	}
	if successorLLM.Calls() != 1 {
		t.Fatalf("successor provider calls = %d, want exactly 1", successorLLM.Calls())
	}
	if staleRan.Load() != 0 {
		t.Fatalf("stale owner executed pending tool %d times after takeover, want 0", staleRan.Load())
	}
	if successorStop != session.StopEndTurn {
		t.Fatalf("successor stop = %q, want %q", successorStop, session.StopEndTurn)
	}
	final := f.waitRedisState(t, session.StateCompleted)
	if _, ok := final.PendingAsk(); ok {
		t.Fatal("successor retained the PendingAsk instead of resolving it")
	}
}

func TestSessionAffinityAndHandoff_Scenario7_KilledOwnerDropsStream(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.assertKilledOwnerSnapshot(t)
}

func TestADR_0294_PreTTLRequestsCannotAcquireOrRun(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.assertPreTTLBlocked(t)
}

func TestADR_0294_PostTTLSingleSurvivorAcquires(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.advancePastTTL()
	f.assertOneConcurrentSurvivor(t)
}

func TestSessionAffinityAndHandoff_Scenario7_RehydrateRepairAndContinue(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.seedCrashOrphanWithRedisSidecars(t)
	f.assertPreTTLBlocked(t)
	f.advancePastTTL()

	run, capture := f.continueThroughHTTPProvider(t, "continue durable work")
	if run.RunID() == "" || run.RunID() == f.ownerRun.RunID() {
		t.Fatalf("successor run ID = %q, owner run ID = %q; want a new non-empty run ID", run.RunID(), f.ownerRun.RunID())
	}
	if capture.persistedState != session.StateIdle {
		t.Fatalf("snapshot visible at first successor provider request = %q, want persisted idle repair before continuation", capture.persistedState)
	}
	if err := session.ValidateToolPairing(capture.persistedMessages); err != nil {
		t.Fatalf("snapshot visible at first successor provider request has unpaired tools: %v", err)
	}
	f.assertRepairWasFirstSuccessorSave(t)
	f.assertRedisSidecarsSurvivedReload(t)

	reloaded, err := f.survivorStore.Load(context.Background(), f.sessionID)
	if err != nil {
		t.Fatalf("reload continued session: %v", err)
	}
	if reloaded.ID != f.sessionID || reloaded.State != session.StateCompleted {
		t.Fatalf("continued snapshot = id %q state %q, want id %q state %q", reloaded.ID, reloaded.State, f.sessionID, session.StateCompleted)
	}
}

func TestADR_0294_HandoffEndToEndCorrelation(t *testing.T) {
	f := newCrashOwnershipFixture(t)
	f.startOwnerAndDropStream(t)
	f.seedCrashOrphanWithRedisSidecars(t)
	f.assertPreTTLBlocked(t)
	f.advancePastTTL()

	run, capture := f.continueThroughHTTPProvider(t, "continue correlated work")
	if capture.header != string(f.sessionID) {
		t.Fatalf("first successor provider %s = %q, want exact ingress/durable session ID %q", sessionaffinity.HeaderName, capture.header, f.sessionID)
	}
	if capture.contextSessionID != f.sessionID {
		t.Fatalf("provider context session ID = %q, want authoritative durable ID %q", capture.contextSessionID, f.sessionID)
	}
	if run.RunID() == "" || run.RunID() == f.ownerRun.RunID() {
		t.Fatalf("successor run ID = %q, owner run ID = %q; want a distinct run on the same durable session", run.RunID(), f.ownerRun.RunID())
	}
}
