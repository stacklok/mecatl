package server

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type controlPersistBarrierStore struct {
	*memstore.Store
	entered  chan struct{}
	release  chan struct{}
	returned atomic.Bool
}

func (s *controlPersistBarrierStore) Save(ctx context.Context, sess *session.Session) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	err := s.Store.Save(ctx, sess)
	s.returned.Store(true)
	return err
}

func completedControlRun(t *testing.T) *agent.Run {
	t.Helper()
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New("control-run", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), permstore.New()),
		Model:   "test-model",
	})
	run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "go", RunID: "run-old"})
	for range run.Events() {
	}
	return run
}

func awaitingControlSession(t *testing.T, id session.SessionID) *session.Session {
	t.Helper()
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New(id, session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	sess.BeginRun("run-old")
	if err := sess.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := sess.PauseForApproval(session.PendingAsk{AskID: "ask-1", Tool: "Write", Origin: session.ApprovalOriginPermission}); err != nil {
		t.Fatal(err)
	}
	return sess
}

type controlAskTool struct{}

func (controlAskTool) Spec() tool.ToolSpec { return tool.ToolSpec{Name: "Write"} }
func (controlAskTool) ReadOnly() bool      { return false }
func (controlAskTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "wrote"), nil
}

func liveAwaitingControlRun(t *testing.T, id session.SessionID) (*session.Session, *agent.Run, string) {
	t.Helper()
	ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "in-tree-v1"}
	sess := session.New(id, session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	env := tool.MustEnvironment(ref, nofs.New(), memledger.New(), nil)
	catalog := tool.NewCatalog()
	catalog.MustRegister(controlAskTool{})
	eng := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("write-1", "Write", json.RawMessage(`{}`))), mockllm.TextTurn("done")),
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Ask}}, permstore.New()),
		Model:   "test-model",
	})
	run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "go", RunID: "run-old"})
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			return sess, run, ev.Ask.AskID
		}
	}
	t.Fatal("run ended without a permission ask")
	return nil, nil, ""
}

// TestPermissionAskPersistenceSerializesLiveControls forces the awaiting Save
// to remain in flight while approval or cancellation arrives. The control must
// wait for that Save to return before it signals the run or marks the ask stale.
func TestPermissionAskPersistenceSerializesLiveControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		act    func(*Service, session.SessionID, *runState, *agent.Run, string) error
		marked func(*runState) bool
	}{
		{
			name: "approval",
			act: func(s *Service, id session.SessionID, st *runState, run *agent.Run, askID string) error {
				return s.approveRunState(id, st, run, askID, session.VerdictAllowOnce, run.RunID())
			},
			marked: func(st *runState) bool { return st.resolvedAskID != "" },
		},
		{
			name: "cancellation",
			act: func(s *Service, id session.SessionID, _ *runState, run *agent.Run, _ string) error {
				return s.cancelLiveRun(id, run, run.RunID())
			},
			marked: func(st *runState) bool { return st.cancelSignaled },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := session.SessionID("control-persist-" + tc.name)
			sess, run, askID := liveAwaitingControlRun(t, id)
			base := memstore.New()
			if err := base.Save(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			store := &controlPersistBarrierStore{
				Store: base, entered: make(chan struct{}), release: make(chan struct{}),
			}
			st := &runState{run: run, sess: sess, settled: make(chan struct{})}
			svc := &Service{
				cfg:  Config{Store: store, MutationCapability: NewSessionMutationCapability(false)},
				runs: map[session.SessionID]*runState{id: st},
			}

			persisted := make(chan struct{})
			go func() {
				svc.persistPermissionAsk(context.Background(), id, askID)
				close(persisted)
			}()
			released := false
			defer func() {
				if !released {
					close(store.release)
					<-persisted
				}
			}()
			<-store.entered

			controlled := make(chan error, 1)
			controlStarted := make(chan struct{})
			go func() {
				close(controlStarted)
				controlled <- tc.act(svc, id, st, run, askID)
			}()
			<-controlStarted
			select {
			case err := <-controlled:
				t.Fatalf("control crossed an in-flight awaiting Save: %v", err)
			default:
			}

			close(store.release)
			released = true
			<-persisted
			if err := <-controlled; err != nil {
				t.Fatalf("control after Save: %v", err)
			}
			if !store.returned.Load() {
				t.Fatal("control returned before the awaiting Save")
			}
			if !tc.marked(st) {
				t.Fatal("control did not mark the persisted ask before signaling the run")
			}
			run.Cancel()
			for range run.Events() {
			}
		})
	}
}

// waitCompleteRelayMutex observes the actual mutex wait, not just goroutine
// startup, so registry changes cannot win before completeRelay captures its entry.
func waitCompleteRelayMutex(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 64<<10)
	for {
		n := runtime.Stack(buf, true)
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, "[sync.Mutex.Lock") && strings.Contains(stack, "(*Service).completeRelay(") {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("completeRelay did not reach its mutex barrier:\n%s", buf[:n])
		}
		runtime.Gosched()
	}
}

func TestCompleteRelayRejectsReplacedRunAfterPersistenceBarrier(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "deregistered"
		if replacement {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			id := session.SessionID("relay-" + name)
			sess := awaitingControlSession(t, id)
			store := memstore.New()
			if err := store.Save(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			if err := sess.Complete(); err != nil {
				t.Fatal(err)
			}
			run := completedControlRun(t)
			st := &runState{run: run, sess: sess}
			svc := &Service{
				cfg:  Config{Store: store, MutationCapability: NewSessionMutationCapability(false)},
				runs: map[session.SessionID]*runState{id: st},
			}
			st.persistMu.Lock()
			unlock := sync.OnceFunc(st.persistMu.Unlock)
			done := make(chan struct{})
			go func() {
				svc.completeRelay(t.Context(), id, run)
				close(done)
			}()
			defer func() { unlock(); <-done }()
			waitCompleteRelayMutex(t)

			svc.mu.Lock()
			delete(svc.runs, id)
			if replacement {
				// A provisional successor has no run yet and owns mutable state.
				svc.runs[id] = &runState{sess: awaitingControlSession(t, id)}
			}
			svc.mu.Unlock()
			unlock()
			<-done
			got, err := store.Load(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != session.StateAwaiting {
				t.Fatalf("stale relay persisted %s over durable awaiting snapshot", got.State)
			}
		})
	}
}

func TestCompleteRelayRejectsSuccessorDuringPromotion(t *testing.T) {
	id := session.SessionID("relay-promotion")
	oldRun, nextRun := completedControlRun(t), completedControlRun(t)
	sess := awaitingControlSession(t, id)
	st := &runState{sess: sess}
	svc := &Service{runs: map[session.SessionID]*runState{id: st}}
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	promoted := make(chan error, 1)
	go func() {
		_, err := svc.promoteRunAdmission(id, st, nil, func() *agent.Run {
			close(entered)
			<-release
			// Engine launch may mutate the successor before promotion publishes run.
			if _, err := sess.ResumeWith(); err != nil {
				t.Error(err)
			}
			return nextRun
		})
		promoted <- err
	}()
	defer unblock()
	<-entered
	done := make(chan struct{})
	go func() {
		svc.completeRelay(t.Context(), id, oldRun)
		close(done)
	}()
	defer func() { unblock(); <-done }()
	waitCompleteRelayMutex(t)
	unblock()
	if err := <-promoted; err != nil {
		t.Fatal(err)
	}
	<-done
	if st.run != nextRun || sess.State != session.StateRunning {
		t.Fatal("stale relay changed the promoted successor")
	}
}

func TestCompleteRelayConcurrentAdmission(t *testing.T) {
	id := session.SessionID("relay-concurrent-admission")
	oldRun, nextRun := completedControlRun(t), completedControlRun(t)
	sess := awaitingControlSession(t, id)
	svc := &Service{runs: make(map[session.SessionID]*runState)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10000 {
			svc.completeRelay(t.Context(), id, oldRun)
		}
	}()
	defer func() { <-done }()
	for range 10000 {
		st := &runState{sess: sess}
		svc.mu.Lock()
		svc.runs[id] = st
		svc.mu.Unlock()
		if _, err := svc.promoteRunAdmission(id, st, nil, func() *agent.Run { return nextRun }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompleteRelayTerminalPersistence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    session.State
		settle   func(*session.Session) error
		preserve bool
		save     bool
	}{
		{name: "completed", state: session.StateCompleted, settle: (*session.Session).Complete, save: true},
		{name: "cancelled", state: session.StateCancelled, settle: (*session.Session).Cancel, save: true},
		{name: "failed", state: session.StateFailed, settle: (*session.Session).Fail, save: true},
		{name: "awaiting", state: session.StateAwaiting},
		{name: "preserved", state: session.StateCancelled, settle: (*session.Session).Cancel, preserve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := session.SessionID("relay-terminal-" + tc.name)
			sess := awaitingControlSession(t, id)
			base := memstore.New()
			if err := base.Save(t.Context(), sess); err != nil {
				t.Fatal(err)
			}
			if tc.settle != nil {
				if err := tc.settle(sess); err != nil {
					t.Fatal(err)
				}
			}
			store := &controlPersistBarrierStore{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
			run := completedControlRun(t)
			st := &runState{run: run, sess: sess}
			st.preserveDurable.Store(tc.preserve)
			svc := &Service{
				cfg:  Config{Store: store, MutationCapability: NewSessionMutationCapability(false)},
				runs: map[session.SessionID]*runState{id: st},
			}
			done := make(chan struct{})
			go func() {
				svc.completeRelay(t.Context(), id, run)
				close(done)
			}()
			unblock := sync.OnceFunc(func() { close(store.release) })
			defer func() { unblock(); <-done }()
			select {
			case <-store.entered:
				if !tc.save {
					t.Fatal("relay attempted to overwrite a nonterminal or preserved snapshot")
				}
				// Store I/O must not hold the global registry mutex.
				locked := make(chan struct{})
				go func() {
					svc.mu.Lock()
					current := svc.runs[id]
					svc.mu.Unlock()
					if current != st {
						t.Error("relay changed the registry during Store.Save")
					}
					close(locked)
				}()
				defer func() { unblock(); <-locked }()
				select {
				case <-locked:
				case <-time.After(5 * time.Second):
					t.Fatal("relay held the registry mutex during Store.Save")
				}
			case <-done:
				if tc.save {
					t.Fatal("matching terminal relay did not persist")
				}
			}
			unblock()
			<-done
			got, err := base.Load(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			want := session.StateAwaiting
			if tc.save {
				want = tc.state
			}
			if got.State != want {
				t.Fatalf("durable state = %s, want %s", got.State, want)
			}
		})
	}
}

// TestApproveRunStateRejectsDeregisteredOrReplacedRunAfterPersistenceBarrier
// pins the identity recheck after persistMu. A captured old runState must not
// receive an approval once its registry entry is removed or replaced.
func TestApproveRunStateRejectsDeregisteredOrReplacedRunAfterPersistenceBarrier(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "deregistered"
		if replacement {
			name = "replaced"
		}
		t.Run(name, func(t *testing.T) {
			id := session.SessionID("approve-old-" + name)
			run := completedControlRun(t)
			old := &runState{run: run, settled: make(chan struct{})}
			var next *runState
			if replacement {
				next = &runState{run: completedControlRun(t), settled: make(chan struct{})}
			}
			svc := &Service{runs: map[session.SessionID]*runState{id: old}}
			old.persistMu.Lock()
			result := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				result <- svc.approveRunState(id, old, run, "ask-old", session.VerdictAllowOnce, run.RunID())
			}()
			<-started

			svc.mu.Lock()
			delete(svc.runs, id)
			if replacement {
				svc.runs[id] = next
			}
			svc.mu.Unlock()
			old.persistMu.Unlock()

			if err := <-result; !errors.Is(err, ErrNoActiveRun) {
				t.Fatalf("approval of old runState = %v, want ErrNoActiveRun", err)
			}
			if old.resolvedAskID != "" {
				t.Fatalf("old runState resolved ask %q after registry replacement", old.resolvedAskID)
			}
		})
	}
}
