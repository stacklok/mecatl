package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type recordingObserver struct {
	calls int
	state session.State
	tr    learning.Trajectory
	err   error
	sess  *session.Session
}

type usageErrorObserver struct{ recordingObserver }

func (o *usageErrorObserver) ObserveWithUsage(context.Context, learning.Trajectory) (session.AuxiliaryUsage, error) {
	return session.AuxiliaryUsage{}, o.err
}

func (o *recordingObserver) Observe(_ context.Context, tr learning.Trajectory) error {
	o.calls++
	o.tr = tr
	if o.sess != nil {
		o.state = o.sess.State
	}
	if len(tr.Messages) > 0 {
		tr.Messages[0] = session.Message{}
	}
	return o.err
}

func runLearningTurn(t *testing.T, mode learning.Mode, observer learning.Observer, llm *mockllm.Provider, sess *session.Session) {
	t.Helper()
	e := newEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), LearningMode: mode, LearningObserver: observer})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
	for range r.Events() {
	}
}

type blockingObserver struct {
	entered chan struct{}
	release chan struct{}
}

func (o *blockingObserver) Observe(_ context.Context, _ learning.Trajectory) error {
	close(o.entered)
	<-o.release
	return nil
}

type nestedMutatingObserver struct {
	store  *memstore.Store
	before []session.Message
}

func (o *nestedMutatingObserver) Observe(ctx context.Context, tr learning.Trajectory) error {
	persisted, err := o.store.Load(ctx, tr.SessionID)
	if err != nil {
		return err
	}
	o.before = learning.NewTrajectory("before", persisted.EnvironmentRef.ID, tr.Stop, tr.Usage, persisted.Conversation.Messages).Messages
	tr.Messages[0].ToolCalls[0].Args[2] = 'X'
	tr.Messages[0].ToolCalls = append(tr.Messages[0].ToolCalls, session.ToolCall{})
	tr.Messages[1].ToolResult.Content = "changed"
	tr.Messages[1].ToolResult.Parts[0].Data[0] = 'X'
	tr.Messages[1].ToolResult.Parts[0].Audience[0] = "changed"
	tr.Messages[1].ToolResult.Parts = append(tr.Messages[1].ToolResult.Parts, session.Content{})
	tr.Messages[2].Parts[0].Data[0] = 'X'
	tr.Messages[2].Parts[0].Audience[0] = "changed"
	tr.Messages[2].Parts = append(tr.Messages[2].Parts, session.Content{})
	return nil
}

func TestLearningTrajectoryNestedMutationCannotChangeLiveOrPersistedHistory(t *testing.T) {
	seed := []session.Message{
		session.NewAssistantMessage("assistant", "reasoning", []session.ToolCall{session.NewToolCall("call", "Tool", json.RawMessage(`{"key":"value"}`))}),
		session.NewToolMessage(session.NewToolResultWithParts("call", "result", []session.Content{{Data: []byte("result-data"), Audience: []string{"result-audience"}}})),
		session.NewUserMessageWithParts("user", []session.Content{{Data: []byte("user-data"), Audience: []string{"user-audience"}}}),
	}
	expected := learning.NewTrajectory("expected", "/ws", session.StopEndTurn, session.Usage{}, seed).Messages
	sess := newSession(t, session.Limits{})
	if err := sess.SeedHistory(seed); err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	observer := &nestedMutatingObserver{store: store}
	e := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Store: store,
		LearningMode: learning.Auto, LearningObserver: observer,
	})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
	for range r.Events() {
	}
	if !reflect.DeepEqual(sess.Conversation.Messages[:len(seed)], expected) {
		t.Fatal("observer nested mutation changed live history")
	}
	persisted, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.Conversation.Messages, observer.before) {
		t.Fatal("observer nested mutation changed persisted history")
	}
}

func TestLearningObserverRunsAfterTerminalPersistence(t *testing.T) {
	store := memstore.New()
	observer := &blockingObserver{entered: make(chan struct{}), release: make(chan struct{})}
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Store: store,
		LearningMode: learning.Auto, LearningObserver: observer,
	})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
	<-observer.entered
	persisted, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateCompleted {
		t.Fatalf("persisted state while observer blocked = %s, want completed", persisted.State)
	}
	close(observer.release)
	for range r.Events() {
	}
}

func TestLearningObserverOffAndNilAreInert(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode learning.Mode
		obs  *recordingObserver
	}{{"zero", learning.Off, &recordingObserver{}}, {"off", learning.Off, &recordingObserver{}}, {"nil", learning.Auto, nil}} {
		t.Run(tc.name, func(t *testing.T) {
			tllm := mockllm.New(mockllm.TextTurn("done"))
			var observer learning.Observer
			if tc.obs != nil {
				observer = tc.obs
			}
			runLearningTurn(t, tc.mode, observer, tllm, newSession(t, session.Limits{}))
			if tc.obs != nil && tc.obs.calls != 0 {
				t.Fatalf("observer calls = %d, want 0", tc.obs.calls)
			}
			if tllm.Calls() != 1 {
				t.Fatalf("LLM calls = %d, want 1", tllm.Calls())
			}
		})
	}
}

func TestLearningObserverEligibleCompletionAndClone(t *testing.T) {
	for _, mode := range []learning.Mode{learning.Review, learning.Auto} {
		t.Run(mode.String(), func(t *testing.T) {
			sess := newSession(t, session.Limits{})
			obs := &recordingObserver{sess: sess}
			runLearningTurn(t, mode, obs, mockllm.New(mockllm.TextTurn("done")), sess)
			if obs.calls != 1 || obs.state != session.StateCompleted || obs.tr.Stop != session.StopEndTurn {
				t.Fatalf("calls=%d state=%s stop=%s", obs.calls, obs.state, obs.tr.Stop)
			}
			if len(sess.Conversation.Messages) == 0 || sess.Conversation.Messages[0].Role != session.RoleUser {
				t.Fatal("observer mutation changed live session history")
			}
		})
	}
}

func TestLearningObserverIneligibleTerminals(t *testing.T) {
	t.Run("failed", func(t *testing.T) {
		obs := &recordingObserver{}
		runLearningTurn(t, learning.Auto, obs, mockllm.New(mockllm.ErrorTurn(errors.New("provider failed"))), newSession(t, session.Limits{}))
		if obs.calls != 0 {
			t.Fatalf("observer calls = %d, want 0", obs.calls)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		obs := &recordingObserver{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("unused")), Catalog: tool.NewCatalog(), LearningMode: learning.Auto, LearningObserver: obs})
		r := e.Run(ctx, newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
		for range r.Events() {
		}
		if obs.calls != 0 {
			t.Fatalf("observer calls = %d, want 0", obs.calls)
		}
	})
	t.Run("awaiting", func(t *testing.T) {
		obs := &recordingObserver{}
		write := &fakeTool{name: "Write", exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(in.ID, "wrote"), nil
		}}
		e := newEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("c1", "Write", `{}`))),
			Catalog: catalogWith(t, write), Policy: permpolicy.NewPolicy(nil, permstore.New()),
			LearningMode: learning.Auto, LearningObserver: obs,
		})
		r := e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
		for ev := range r.Events() {
			if ev.Type == session.EvPermissionAsk {
				if obs.calls != 0 {
					t.Fatal("observer ran while awaiting")
				}
				r.Cancel()
			}
		}
		if obs.calls != 0 {
			t.Fatalf("observer calls = %d, want 0", obs.calls)
		}
	})
}

func TestLearningObserverErrorIsDiagnostic(t *testing.T) {
	diag := newRecordingDiag()
	observer := &recordingObserver{err: errors.New("observe failed")}
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Diagnostics: diag,
		LearningMode: learning.Auto, LearningObserver: observer,
	})
	r := e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"})
	for range r.Events() {
	}
	if _, ok := diag.findLine("completed-trajectory observer failed"); !ok {
		t.Fatal("observer failure was not reported through run-scoped diagnostics")
	}
	if sess.State != session.StateCompleted {
		t.Fatalf("state = %s, want completed", sess.State)
	}
}

func TestLearningUsageObserverErrorHasDistinctDiagnostic(t *testing.T) {
	diag := newRecordingDiag()
	observer := &usageErrorObserver{recordingObserver: recordingObserver{err: errors.New("observe failed")}}
	e := newEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog(), Diagnostics: diag,
		LearningMode: learning.Auto, LearningObserver: observer,
	})
	for range e.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "hello"}).Events() {
	}
	if _, ok := diag.findLine("completed-trajectory usage observer failed"); !ok {
		t.Fatal("usage observer failure was not reported through a distinct diagnostic")
	}
}

func TestLearningObserverErrorPreservesCompletionAndReopen(t *testing.T) {
	sess := newSession(t, session.Limits{})
	obs := &recordingObserver{err: errors.New("observe failed")}
	runLearningTurn(t, learning.Auto, obs, mockllm.New(mockllm.TextTurn("one")), sess)
	if sess.State != session.StateCompleted || obs.calls != 1 {
		t.Fatalf("first run: state=%s calls=%d", sess.State, obs.calls)
	}
	if err := sess.Reopen(); err != nil {
		t.Fatal(err)
	}
	runLearningTurn(t, learning.Auto, obs, mockllm.New(mockllm.TextTurn("two")), sess)
	if sess.State != session.StateCompleted || obs.calls != 2 {
		t.Fatalf("second run: state=%s calls=%d", sess.State, obs.calls)
	}
}
