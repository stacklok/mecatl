package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type serviceCompactCompactor struct {
	out     []session.Message
	summary string
}

func (c serviceCompactCompactor) Compact(context.Context, *session.Conversation) ([]session.Message, string, error) {
	return c.out, c.summary, nil
}

type serviceCompactCounter struct{}

func (serviceCompactCounter) Count(s string) int { return len(s) }
func (serviceCompactCounter) CountMessages(msgs []session.Message) int {
	total := len(msgs)
	for _, msg := range msgs {
		total += len(msg.Text)
	}
	return total
}

type compactTrackingStore struct {
	*memstore.Store
	mu               sync.Mutex
	saves            int
	saved            bool
	appendBeforeSave bool
	events           []session.Event
	saveErr          error
	appendErr        error
}

func (s *compactTrackingStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	s.saves++
	if s.saveErr != nil {
		err := s.saveErr
		s.mu.Unlock()
		return err
	}
	s.saved = true
	s.mu.Unlock()
	return s.Store.Save(ctx, sess)
}

func (s *compactTrackingStore) Append(_ context.Context, _ session.SessionID, ev session.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.saved {
		s.appendBeforeSave = true
	}
	s.events = append(s.events, ev)
	return s.appendErr
}

func (s *compactTrackingStore) Read(_ context.Context, _ session.SessionID) iter.Seq2[session.Event, error] {
	_, _, events := s.snapshot()
	return func(yield func(session.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func (s *compactTrackingStore) snapshot() (int, bool, []session.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves, s.appendBeforeSave, append([]session.Event(nil), s.events...)
}

func newCompactService(t *testing.T, store *compactTrackingStore, compactor agent.Compactor, ownership bool, lease port.SessionLease, factory server.SessionEngineFactory) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test",
		Compactor: compactor, TokenCounter: serviceCompactCounter{},
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store, EventLog: store,

		Now: time.Now, OwnershipEnforced: ownership, SessionLease: lease,
		LeaseOwner: "compact", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
		SessionEngine: factory,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func compactFixture(t *testing.T, state session.State) (*compactTrackingStore, *session.Session, *session.Principal) {
	t.Helper()
	store := &compactTrackingStore{Store: memstore.New()}
	owner := &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser}
	sess := session.New("compact-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := sess.SeedHistory([]session.Message{
		session.NewUserMessage("a long original user instruction"),
		session.NewAssistantMessage("a long original assistant response", "", nil),
	}); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	switch state {
	case session.StateRunning:
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
	case session.StateAwaiting:
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := sess.PauseForApproval(session.PendingAsk{AskID: "ask"}); err != nil {
			t.Fatal(err)
		}
	case session.StateCompleted:
		if err := sess.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := sess.Complete(); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Store.Save(context.Background(), sess); err != nil {
		t.Fatalf("fixture Save: %v", err)
	}
	return store, sess, owner
}

func TestCompactSessionPersistsBeforeOrderedAttributedEvents(t *testing.T) {
	store, sess, owner := compactFixture(t, session.StateIdle)
	original := session.CloneMessages(sess.Conversation.Messages)
	svc := newCompactService(t, store, serviceCompactCompactor{
		out: []session.Message{session.NewUserMessage("short")}, summary: "manual summary",
	}, true, nil, nil)

	result, err := svc.CompactSession(context.Background(), sess.ID, owner)
	if err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	if !result.Changed || !reflect.DeepEqual(result.Archive, original) {
		t.Fatalf("result = %#v, want changed with exact archive", result)
	}
	saves, appendBeforeSave, events := store.snapshot()
	if saves != 1 || appendBeforeSave {
		t.Fatalf("saves=%d appendBeforeSave=%t", saves, appendBeforeSave)
	}
	if len(events) != 2 || events[0].Type != session.EvCompaction || events[0].Text != "manual summary" || events[1].Type != session.EvCompactionArchive {
		t.Fatalf("events = %#v, want compaction then archive", events)
	}
	if events[0].Actor == nil || events[0].Actor.Subject != owner.Subject || events[1].Actor == nil || events[1].Actor.Subject != owner.Subject {
		t.Fatalf("event actors = %#v, %#v", events[0].Actor, events[1].Actor)
	}
	if !reflect.DeepEqual(events[1].CompactionArchive.Replaced, original) {
		t.Fatal("archive event lost pre-compaction history")
	}
	reloaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.State != session.StateIdle || len(reloaded.Conversation.Messages) != 1 || reloaded.Conversation.Messages[0].Text != "short" {
		t.Fatalf("reloaded session = state %q history %#v", reloaded.State, reloaded.Conversation.Messages)
	}
}

func TestCompactSessionPreservesTerminalState(t *testing.T) {
	store, sess, owner := compactFixture(t, session.StateCompleted)
	svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
	if _, err := svc.CompactSession(context.Background(), sess.ID, owner); err != nil {
		t.Fatalf("CompactSession: %v", err)
	}
	reloaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.State != session.StateCompleted {
		t.Fatalf("state = %q, want completed", reloaded.State)
	}
}

func TestCompactSessionNoOpAndSaveFailureDoNotAppend(t *testing.T) {
	t.Run("no-op", func(t *testing.T) {
		store, sess, owner := compactFixture(t, session.StateIdle)
		svc := newCompactService(t, store, serviceCompactCompactor{out: session.CloneMessages(sess.Conversation.Messages)}, true, nil, nil)
		result, err := svc.CompactSession(context.Background(), sess.ID, owner)
		if err != nil || result.Changed {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		saves, _, events := store.snapshot()
		if saves != 0 || len(events) != 0 {
			t.Fatalf("no-op saves=%d events=%d", saves, len(events))
		}
	})

	t.Run("save failure", func(t *testing.T) {
		store, sess, owner := compactFixture(t, session.StateIdle)
		store.saveErr = errors.New("disk full")
		svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
		if _, err := svc.CompactSession(context.Background(), sess.ID, owner); !errors.Is(err, server.ErrInternal) {
			t.Fatalf("error = %v, want ErrInternal", err)
		}
		_, _, events := store.snapshot()
		if len(events) != 0 {
			t.Fatalf("save failure appended %d events", len(events))
		}
	})

	t.Run("append failure does not roll back or retry", func(t *testing.T) {
		store, sess, owner := compactFixture(t, session.StateIdle)
		store.appendErr = errors.New("log unavailable")
		svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
		result, err := svc.CompactSession(context.Background(), sess.ID, owner)
		if err != nil || !result.Changed {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		_, _, events := store.snapshot()
		if len(events) != 2 {
			t.Fatalf("append attempts = %d, want one per event", len(events))
		}
		reloaded, loadErr := store.Load(context.Background(), sess.ID)
		if loadErr != nil || len(reloaded.Conversation.Messages) != 1 || reloaded.Conversation.Messages[0].Text != "short" {
			t.Fatalf("committed snapshot was rolled back: session=%#v err=%v", reloaded, loadErr)
		}
	})
}

func TestCompactSessionRegisteredGRPCClientPath(t *testing.T) {
	store, sess, _ := compactFixture(t, session.StateIdle)
	svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, false, nil, nil)
	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(svc))
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	response, err := mecatlv1.NewHarnessServiceClient(conn).CompactSession(context.Background(), &mecatlv1.CompactSessionRequest{SessionId: string(sess.ID)})
	if err != nil || !response.GetCompacted() {
		t.Fatalf("registered CompactSession response=%+v err=%v", response, err)
	}
}

func TestCompactSessionWireSurfaces(t *testing.T) {
	t.Run("grpc success no-op validation and active error", func(t *testing.T) {
		store, sess, owner := compactFixture(t, session.StateIdle)
		svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
		h := server.NewHarnessServer(svc)
		ctx := session.WithPrincipal(context.Background(), owner)
		resp, err := h.CompactSession(ctx, &mecatlv1.CompactSessionRequest{SessionId: string(sess.ID)})
		if err != nil || !resp.GetCompacted() {
			t.Fatalf("CompactSession response=%#v err=%v", resp, err)
		}
		if _, err := h.CompactSession(ctx, &mecatlv1.CompactSessionRequest{}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("empty id code=%v err=%v", status.Code(err), err)
		}

		noopStore, noopSess, noopOwner := compactFixture(t, session.StateIdle)
		noopSvc := newCompactService(t, noopStore, serviceCompactCompactor{out: session.CloneMessages(noopSess.Conversation.Messages)}, true, nil, nil)
		noopResp, err := server.NewHarnessServer(noopSvc).CompactSession(session.WithPrincipal(context.Background(), noopOwner), &mecatlv1.CompactSessionRequest{SessionId: string(noopSess.ID)})
		if err != nil || noopResp.GetCompacted() {
			t.Fatalf("no-op response=%#v err=%v", noopResp, err)
		}

		activeStore, active, activeOwner := compactFixture(t, session.StateRunning)
		activeSvc := newCompactService(t, activeStore, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
		_, err = server.NewHarnessServer(activeSvc).CompactSession(session.WithPrincipal(context.Background(), activeOwner), &mecatlv1.CompactSessionRequest{SessionId: string(active.ID)})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("active code=%v err=%v", status.Code(err), err)
		}
	})

	t.Run("http success no-op and error mapping", func(t *testing.T) {
		run := func(state session.State, out []session.Message) (*httptest.ResponseRecorder, *session.Session) {
			store, sess, owner := compactFixture(t, state)
			svc := newCompactService(t, store, serviceCompactCompactor{out: out}, true, nil, nil)
			req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(sess.ID)+"/compact", nil)
			req = req.WithContext(session.WithPrincipal(req.Context(), owner))
			rr := httptest.NewRecorder()
			server.NewHTTPHandler(svc).ServeHTTP(rr, req)
			return rr, sess
		}

		rr, _ := run(session.StateIdle, []session.Message{session.NewUserMessage("short")})
		var success mecatlv1.CompactSessionResponse
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &success) != nil || !success.GetCompacted() {
			t.Fatalf("success status=%d body=%s", rr.Code, rr.Body.String())
		}

		store, sess, owner := compactFixture(t, session.StateIdle)
		noopSvc := newCompactService(t, store, serviceCompactCompactor{out: session.CloneMessages(sess.Conversation.Messages)}, true, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+string(sess.ID)+"/compact", nil)
		req = req.WithContext(session.WithPrincipal(req.Context(), owner))
		rr = httptest.NewRecorder()
		server.NewHTTPHandler(noopSvc).ServeHTTP(rr, req)
		var noop mecatlv1.CompactSessionResponse
		if rr.Code != http.StatusOK || json.Unmarshal(rr.Body.Bytes(), &noop) != nil || noop.GetCompacted() {
			t.Fatalf("no-op status=%d body=%s", rr.Code, rr.Body.String())
		}

		rr, _ = run(session.StateRunning, []session.Message{session.NewUserMessage("short")})
		if rr.Code != http.StatusPreconditionFailed {
			t.Fatalf("active status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestCompactSessionCapabilityAdvertised(t *testing.T) {
	store, _, _ := compactFixture(t, session.StateIdle)
	svc := newCompactService(t, store, serviceCompactCompactor{}, false, nil, nil)
	resp, err := server.NewHarnessServer(svc).CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !resp.GetCapabilities().GetManualCompaction() {
		t.Fatal("manual_compaction capability is false on a service with an engine")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(rr, req)
	var body struct {
		Capabilities struct {
			ManualCompaction bool `json:"manual_compaction"`
		} `json:"capabilities"`
	}
	if rr.Code != http.StatusCreated || json.Unmarshal(rr.Body.Bytes(), &body) != nil || !body.Capabilities.ManualCompaction {
		t.Fatalf("HTTP capability status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCompactSessionAuthorizationIdentityAndActiveGates(t *testing.T) {
	store, sess, owner := compactFixture(t, session.StateIdle)
	svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
	bob := &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser}
	if _, err := svc.CompactSession(context.Background(), sess.ID, bob); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign caller error = %v, want ErrNotFound", err)
	}
	if _, err := svc.CompactSession(context.Background(), "subagent-child", owner); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("child error = %v, want ErrFailedPrecondition", err)
	}

	scheduledStore, scheduled, scheduledOwner := compactFixture(t, session.StateIdle)
	if err := scheduled.RestoreSessionMetadata(session.SessionKindScheduled, session.SessionRelationship{ScheduleName: "nightly", OriginSessionID: "origin"}); err != nil {
		t.Fatalf("RestoreSessionMetadata: %v", err)
	}
	if err := scheduledStore.Store.Save(context.Background(), scheduled); err != nil {
		t.Fatal(err)
	}
	scheduledSvc := newCompactService(t, scheduledStore, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
	if _, err := scheduledSvc.CompactSession(context.Background(), scheduled.ID, scheduledOwner); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("scheduled error = %v, want ErrFailedPrecondition", err)
	}

	for _, state := range []session.State{session.StateRunning, session.StateAwaiting} {
		stateStore, stateSess, stateOwner := compactFixture(t, state)
		stateSvc := newCompactService(t, stateStore, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, nil, nil)
		if _, err := stateSvc.CompactSession(context.Background(), stateSess.ID, stateOwner); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("state %q error = %v, want ErrFailedPrecondition", state, err)
		}
	}

	run, err := svc.StartRunContent(session.WithPrincipal(context.Background(), owner), sess.ID, "live", nil)
	if err != nil {
		t.Fatalf("StartRunContent: %v", err)
	}
	if _, err := svc.CompactSession(context.Background(), sess.ID, owner); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("live error = %v, want ErrFailedPrecondition", err)
	}
	run.Cancel()
	for range run.Events() {
	}
	svc.FinishRun(sess.ID, run)
}

type blockingAcquireLease struct {
	entered chan struct{}
	release chan struct{}
}

func (l *blockingAcquireLease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	close(l.entered)
	select {
	case <-l.release:
		return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
	case <-ctx.Done():
		return port.Lease{}, ctx.Err()
	}
}
func (*blockingAcquireLease) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	return lease, nil
}
func (*blockingAcquireLease) Release(context.Context, port.Lease) error { return nil }

type countingServiceCompactor struct {
	calls int
	wait  bool
}

func (c *countingServiceCompactor) Compact(ctx context.Context, _ *session.Conversation) ([]session.Message, string, error) {
	c.calls++
	if c.wait {
		<-ctx.Done()
	}
	return []session.Message{session.NewUserMessage("short")}, "summary", nil
}

func TestCompactSessionReloadsAuthoritativeStateAfterLeaseAcquire(t *testing.T) {
	for _, state := range []session.State{session.StateRunning, session.StateAwaiting} {
		t.Run(string(state), func(t *testing.T) {
			store, sess, owner := compactFixture(t, session.StateIdle)
			lease := &blockingAcquireLease{entered: make(chan struct{}), release: make(chan struct{})}
			compactor := &countingServiceCompactor{}
			svc := newCompactService(t, store, compactor, true, lease, nil)
			errCh := make(chan error, 1)
			go func() {
				_, err := svc.CompactSession(context.Background(), sess.ID, owner)
				errCh <- err
			}()
			<-lease.entered
			fresh, err := store.Load(context.Background(), sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := fresh.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if state == session.StateAwaiting {
				if err := fresh.PauseForApproval(session.PendingAsk{AskID: "ask"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Store.Save(context.Background(), fresh); err != nil {
				t.Fatal(err)
			}
			close(lease.release)
			if err := <-errCh; !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("error = %v, want ErrFailedPrecondition", err)
			}
			if compactor.calls != 0 {
				t.Fatalf("compactor calls = %d, want 0", compactor.calls)
			}
			if saves, _, _ := store.snapshot(); saves != 0 {
				t.Fatalf("service saves = %d, want 0", saves)
			}
		})
	}
}

func TestCompactSessionLeaseLossCancelsCompactorAndPreventsSave(t *testing.T) {
	store, sess, owner := compactFixture(t, session.StateIdle)
	lease := &fakeLease{acquireExpiry: time.Now().Add(time.Hour), renewHook: func(port.Lease) (port.Lease, error) {
		return port.Lease{}, port.ErrLeaseHeld
	}}
	compactor := &countingServiceCompactor{wait: true}
	eng := agent.NewEngine(agent.Deps{Compactor: compactor, TokenCounter: serviceCompactCounter{}})
	svc, err := newPlacementTestService(server.Config{
		Engine: eng, Store: store, EventLog: store,
		OwnershipEnforced: true, SessionLease: lease, LeaseOwner: "compact-loss", LeaseTTL: time.Hour, LeaseRenewInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	_, err = svc.CompactSession(context.Background(), sess.ID, owner)
	if !errors.Is(err, server.ErrSessionLeasedElsewhere) {
		t.Fatalf("error = %v, want ErrSessionLeasedElsewhere", err)
	}
	if saves, _, _ := store.snapshot(); saves != 0 {
		t.Fatalf("service saves after lease loss = %d, want 0", saves)
	}
}

func TestCompactSessionLeaseConflictAndNoFSRehydration(t *testing.T) {
	t.Run("lease conflict", func(t *testing.T) {
		store, sess, owner := compactFixture(t, session.StateIdle)
		lease := &fakeLease{acquireErr: port.ErrLeaseHeld}
		svc := newCompactService(t, store, serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, true, lease, nil)
		if _, err := svc.CompactSession(context.Background(), sess.ID, owner); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
			t.Fatalf("error = %v, want ErrSessionLeasedElsewhere", err)
		}
	})

	t.Run("no-fs uses rehydrated engine", func(t *testing.T) {
		store, sess, owner := compactFixture(t, session.StateIdle)
		sess.Profile = string(server.ProfileNoFS)
		sess.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
		if err := store.Store.Save(context.Background(), sess); err != nil {
			t.Fatal(err)
		}
		factoryCalls := 0
		factory := func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			factoryCalls++
			return server.SessionEngineResult{
				Engine:     agent.NewEngine(agent.Deps{Compactor: serviceCompactCompactor{out: []session.Message{session.NewUserMessage("short")}}, TokenCounter: serviceCompactCounter{}}),
				ProviderID: "p", ModelID: "m", Close: func() error { return nil },
			}, nil
		}
		svc := newCompactService(t, store, serviceCompactCompactor{out: nil}, true, nil, factory)
		result, err := svc.CompactSession(context.Background(), sess.ID, owner)
		if err != nil || !result.Changed || factoryCalls != 1 {
			t.Fatalf("result=%#v err=%v factoryCalls=%d", result, err, factoryCalls)
		}
	})
}
