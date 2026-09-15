package server

import (
	"context"
	"encoding/json"
	"errors"
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
