package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// tui_multi_session_delete_test.go holds the service-level Scenario 3 proofs of
// docs/acceptance/tui-multi-session.md (ADR 0374 Decision 4). The real-git
// worktree proofs live in internal/app/tui_multi_session_delete_test.go.

// tmsCancelTool blocks until the run is cancelled, then runs onCancel (the
// "stopped run wrote a file" simulation) before returning.
type tmsCancelTool struct {
	started  chan struct{}
	onCancel func()
}

func (*tmsCancelTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Read", Description: "Read: test tool", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (*tmsCancelTool) ReadOnly() bool { return true }
func (b *tmsCancelTool) Execute(ctx context.Context, _ session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	close(b.started)
	<-ctx.Done()
	if b.onCancel != nil {
		b.onCancel()
	}
	return session.ToolResult{}, ctx.Err()
}

// tmsRemover is a placement provider whose worktree verdicts are scripted.
type tmsRemover struct {
	testPlacementProvider
	serverCreated bool
	dirty         atomic.Bool
	removeErr     error
	removes       atomic.Int64
}

func (p *tmsRemover) WorktreeOwnership(context.Context, server.PlacementReattachRequest) (server.WorktreeOwnership, error) {
	return server.WorktreeOwnership{ServerCreated: p.serverCreated, Clean: !p.dirty.Load()}, nil
}

func (p *tmsRemover) RemoveWorktree(context.Context, server.PlacementReattachRequest) error {
	p.removes.Add(1)
	return p.removeErr
}

type tmsStopFixture struct {
	svc   *server.Service
	store *memstore.Store
	tool  *tmsCancelTool
}

func newTMSStopFixture(t *testing.T, cfg server.Config) *tmsStopFixture {
	t.Helper()
	store := memstore.New()
	bt := &tmsCancelTool{started: make(chan struct{})}
	cat := tool.NewCatalog()
	cat.MustRegister(bt)
	cfg.Engine = agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`)))),
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
		Store:   store,
	})
	cfg.Store = store
	cfg.Now = func() time.Time { return time.Unix(0, 0) }
	svc, err := newPlacementTestService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &tmsStopFixture{svc: svc, store: store, tool: bt}
}

// startBlockedRun starts a run parked in the blocking tool. With finish set,
// a relay goroutine drains it and deregisters it, as a real relay does.
func (f *tmsStopFixture) startBlockedRun(t *testing.T, finish bool) (session.SessionID, *agent.Run) {
	t.Helper()
	sess, err := f.svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.svc.StartRunContent(t.Context(), sess.ID, "run", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-f.tool.started
	go func() {
		for range run.Events() {
		}
		if finish {
			f.svc.FinishRun(sess.ID, run)
		}
	}()
	return sess.ID, run
}

func assertTMSStored(t *testing.T, store port.SessionStore, id session.SessionID) *session.Session {
	t.Helper()
	sess, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("session %s not kept: %v", id, err)
	}
	return sess
}

func assertTMSDeleted(t *testing.T, store port.SessionStore, id session.SessionID) {
	t.Helper()
	if _, err := store.Load(context.Background(), id); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("session %s still stored: %v", id, err)
	}
}

func TestTUIMultiSession_Scenario3_StopActiveDeletesRunningSession(t *testing.T) {
	t.Run("refused without stop_active", func(t *testing.T) {
		f := newTMSStopFixture(t, server.Config{})
		id, run := f.startBlockedRun(t, true)
		if err := f.svc.DeleteSession(t.Context(), id); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("DeleteSession on a running session = %v, want failed precondition", err)
		}
		if !f.svc.IsLive(id) {
			t.Fatal("refused delete stopped the run")
		}
		assertTMSStored(t, f.store, id)
		run.Cancel()
	})

	t.Run("cancels, waits, and deletes", func(t *testing.T) {
		f := newTMSStopFixture(t, server.Config{})
		id, _ := f.startBlockedRun(t, true)
		resp, err := server.NewHarnessServer(f.svc).DeleteSession(t.Context(), &mecatlv1.DeleteSessionRequest{SessionId: string(id), StopActive: true})
		if err != nil {
			t.Fatalf("DeleteSession{stop_active}: %v", err)
		}
		if resp.GetWorktreeRemoved() || resp.GetWorktreeRetainedReason() != "" {
			t.Fatalf("worktree outcome without remove_worktree: %+v", resp)
		}
		if f.svc.IsLive(id) {
			t.Fatal("run still registered after stop-and-delete")
		}
		assertTMSDeleted(t, f.store, id)
	})

	t.Run("run that does not end in time deletes nothing", func(t *testing.T) {
		f := newTMSStopFixture(t, server.Config{StopActiveTimeout: 50 * time.Millisecond})
		id, run := f.startBlockedRun(t, false) // never deregisters
		_, err := server.NewHarnessServer(f.svc).DeleteSession(t.Context(), &mecatlv1.DeleteSessionRequest{SessionId: string(id), StopActive: true})
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("stop timeout = %v, want DEADLINE_EXCEEDED", err)
		}
		if _, err := f.svc.DeleteSessionWithOptions(t.Context(), id, server.DeleteSessionOptions{StopActive: true}); !errors.Is(err, server.ErrSessionStopTimeout) {
			t.Fatalf("service stop timeout = %v, want ErrSessionStopTimeout", err)
		}
		assertTMSStored(t, f.store, id)
		f.svc.FinishRun(id, run)
	})
}

// tmsPark stores sess in the given non-terminal state with no live owner.
func tmsPark(t *testing.T, store port.SessionStore, sess *session.Session, awaiting bool) {
	t.Helper()
	if err := sess.RecordUserPrompt("go", nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if awaiting {
		if err := sess.PauseForApproval(session.PendingAsk{AskID: "ask-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
}

func TestTUIMultiSession_Scenario3_StopActiveDeletesAwaitingSession(t *testing.T) {
	for _, tc := range []struct {
		name     string
		awaiting bool
		want     session.State
	}{
		{"awaiting with no live owner after restart", true, session.StateAwaiting},
		{"crash-orphaned running snapshot", false, session.StateRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTMSStopFixture(t, server.Config{})
			sess, err := f.svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			tmsPark(t, f.store, sess, tc.awaiting)
			// A fresh process over the same store: nothing is live here.
			restarted, err := newPlacementTestService(server.Config{Engine: noopTMSEngine(), Store: f.store})
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if err := restarted.DeleteSession(t.Context(), sess.ID); !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("DeleteSession without stop_active = %v, want failed precondition", err)
			}
			if got := assertTMSStored(t, f.store, sess.ID); got.State != tc.want {
				t.Fatalf("refused delete changed state to %s", got.State)
			}
			if _, err := restarted.DeleteSessionWithOptions(t.Context(), sess.ID, server.DeleteSessionOptions{StopActive: true}); err != nil {
				t.Fatalf("DeleteSession{stop_active}: %v", err)
			}
			assertTMSDeleted(t, f.store, sess.ID)
		})
	}
}

func noopTMSEngine() *agent.Engine {
	return agent.NewEngine(agent.Deps{
		LLM: mockllm.New(), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Model: "test-model",
	})
}

func TestTUIMultiSession_Scenario3_StopActiveRequiresLease(t *testing.T) {
	t.Run("lease held elsewhere", func(t *testing.T) {
		f := newTMSStopFixture(t, server.Config{
			SessionLease: &fakeLease{acquireErr: port.ErrLeaseHeld}, LeaseOwner: "owner-test",
			LeaseTTL: 90 * time.Millisecond, LeaseRenewInterval: 15 * time.Millisecond,
		})
		sess, err := f.svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		tmsPark(t, f.store, sess, true)
		_, err = server.NewHarnessServer(f.svc).DeleteSession(t.Context(), &mecatlv1.DeleteSessionRequest{SessionId: string(sess.ID), StopActive: true})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("stop_active with a lease held elsewhere = %v, want FAILED_PRECONDITION", err)
		}
		if _, err := f.svc.DeleteSessionWithOptions(t.Context(), sess.ID, server.DeleteSessionOptions{StopActive: true}); !errors.Is(err, server.ErrSessionLeasedElsewhere) {
			t.Fatalf("service error = %v, want ErrSessionLeasedElsewhere", err)
		}
		got := assertTMSStored(t, f.store, sess.ID)
		if _, pending := got.PendingAsk(); got.State != session.StateAwaiting || !pending {
			t.Fatalf("refused stop changed the session: state=%s pending=%v", got.State, pending)
		}
	})

	t.Run("delegation child ids are refused", func(t *testing.T) {
		f := newTMSStopFixture(t, server.Config{})
		for _, id := range []session.SessionID{agent.SubagentSessionPrefix + "a", agent.ParallelSessionPrefix + "b", agent.TeamSessionPrefix + "t-m"} {
			child := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(0, 0))
			if err := f.store.Save(t.Context(), child); err != nil {
				t.Fatal(err)
			}
			_, err := server.NewHarnessServer(f.svc).DeleteSession(t.Context(), &mecatlv1.DeleteSessionRequest{SessionId: string(id), StopActive: true})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("stop_active on %s = %v, want FAILED_PRECONDITION", id, err)
			}
			assertTMSStored(t, f.store, id)
		}
	})
}

func TestTUIMultiSession_Scenario3_PostStopDirtyKeepsWorktree(t *testing.T) {
	newRemoverFixture := func(t *testing.T, provider *tmsRemover) *tmsStopFixture {
		t.Helper()
		provider.testPlacementProvider = testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}}
		return newTMSStopFixture(t, server.Config{PlacementProvider: provider, PlacementScope: "test"})
	}

	t.Run("stopped run dirtied the worktree", func(t *testing.T) {
		provider := &tmsRemover{serverCreated: true}
		f := newRemoverFixture(t, provider)
		f.tool.onCancel = func() { provider.dirty.Store(true) }
		id, _ := f.startBlockedRun(t, true)
		result, err := f.svc.DeleteSessionWithOptions(t.Context(), id, server.DeleteSessionOptions{StopActive: true, RemoveWorktree: true})
		if err != nil {
			t.Fatalf("DeleteSession{stop_active, remove_worktree}: %v", err)
		}
		if result.WorktreeRemoved || result.WorktreeRetainedReason != server.WorktreeRetainedDirty {
			t.Fatalf("result = %+v, want kept with reason dirty", result)
		}
		if provider.removes.Load() != 0 {
			t.Fatal("dirty worktree was removed")
		}
		assertTMSDeleted(t, f.store, id)
	})

	t.Run("removal failure keeps the worktree", func(t *testing.T) {
		provider := &tmsRemover{serverCreated: true, removeErr: errors.New("git refused")}
		f := newRemoverFixture(t, provider)
		sess, err := f.svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		result, err := f.svc.DeleteSessionWithOptions(t.Context(), sess.ID, server.DeleteSessionOptions{RemoveWorktree: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.WorktreeRemoved || result.WorktreeRetainedReason != server.WorktreeRetainedRemoveFailed {
			t.Fatalf("result = %+v, want kept with reason remove_failed", result)
		}
		assertTMSDeleted(t, f.store, sess.ID)
	})
}

// TestTUIMultiSession_Scenario3_RemoveWorktreeRefusalStopsNothing is the
// service-level half of AC3.5: a refused removal leaves a live run running.
func TestTUIMultiSession_Scenario3_RemoveWorktreeRefusalStopsNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider *tmsRemover
	}{
		{"not server-created", &tmsRemover{}},
		{"dirty", func() *tmsRemover { p := &tmsRemover{serverCreated: true}; p.dirty.Store(true); return p }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.provider.testPlacementProvider = testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}}
			f := newTMSStopFixture(t, server.Config{PlacementProvider: tc.provider, PlacementScope: "test"})
			id, run := f.startBlockedRun(t, true)
			_, err := server.NewHarnessServer(f.svc).DeleteSession(t.Context(), &mecatlv1.DeleteSessionRequest{SessionId: string(id), StopActive: true, RemoveWorktree: true})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("refused removal = %v, want FAILED_PRECONDITION", err)
			}
			if !f.svc.IsLive(id) {
				t.Fatal("refused removal stopped the run")
			}
			assertTMSStored(t, f.store, id)
			run.Cancel()
		})
	}

	t.Run("provider without removal", func(t *testing.T) {
		f := newTMSStopFixture(t, server.Config{})
		sess, err := f.svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.DeleteSessionWithOptions(t.Context(), sess.ID, server.DeleteSessionOptions{RemoveWorktree: true}); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("remove_worktree without a remover = %v, want failed precondition", err)
		}
		assertTMSStored(t, f.store, sess.ID)
	})
}

func TestTUIMultiSession_Scenario3_HTTPDeleteBody(t *testing.T) {
	provider := &tmsRemover{serverCreated: true}
	provider.testPlacementProvider = testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}}
	f := newTMSStopFixture(t, server.Config{PlacementProvider: provider, PlacementScope: "test"})
	srv := httptest.NewServer(server.NewHTTPHandler(f.svc))
	defer srv.Close()

	post := func(t *testing.T, id session.SessionID, body string) (int, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		resp, err := http.Post(srv.URL+"/v1/sessions/"+string(id)+"/delete", "application/json", reader)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	create := func(t *testing.T) session.SessionID {
		t.Helper()
		sess, err := f.svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		return sess.ID
	}

	if code, raw := post(t, create(t), ""); code != http.StatusNoContent || raw != "" {
		t.Fatalf("empty body = %d %q, want 204", code, raw)
	}
	if code, raw := post(t, create(t), `{"stop_active":true}`); code != http.StatusNoContent {
		t.Fatalf("stop_active body = %d %q, want 204", code, raw)
	}
	id := create(t)
	code, raw := post(t, id, `{"remove_worktree":true}`)
	if code != http.StatusOK {
		t.Fatalf("remove_worktree body = %d %q, want 200", code, raw)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if got["worktree_removed"] != true || got["worktree_retained_reason"] != "" || len(got) != 2 {
		t.Fatalf("remove_worktree response = %s", raw)
	}
	assertTMSDeleted(t, f.store, id)

	provider.dirty.Store(true)
	refused := create(t)
	if code, raw := post(t, refused, `{"remove_worktree":true}`); code != http.StatusPreconditionFailed {
		t.Fatalf("dirty remove_worktree = %d %q, want 412", code, raw)
	}
	if code, raw := post(t, refused, `{"remove_worktree":true,"force":true}`); code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d %q, want 400", code, raw)
	}
	assertTMSStored(t, f.store, refused)
}
