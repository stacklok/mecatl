package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestClearSessionRejectsApprovalAfterCancellationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict session.ApprovalVerdict
	}{
		{name: "allow_once", verdict: session.VerdictAllowOnce},
		{name: "allow_always", verdict: session.VerdictAllowAlways},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict := tc.verdict
			write := &scriptTool{name: "Write", content: "wrote"}
			catalog := tool.NewCatalog()
			catalog.MustRegister(write)
			store := memstore.New()
			learned := permstore.New()
			engine := agent.NewEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(call("write-1", "Write", `{"path":"a.go"}`))),
				Catalog: catalog,
				Policy:  permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Ask}}, learned),
				Model:   "test-model",
				Store:   store,
			})
			svc, err := newPlacementTeamTestService(server.Config{
				Engine: engine,
				Store:  store,
				Now:    func() time.Time { return time.Unix(0, 0) },
			})
			if err != nil {
				t.Fatal(err)
			}
			source, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			run, err := svc.StartRunContent(t.Context(), source.ID, "write", nil)
			if err != nil {
				t.Fatal(err)
			}
			var askID string
			for ev := range run.Events() {
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
					askID = ev.Ask.AskID
					break
				}
			}
			if askID == "" {
				t.Fatal("run ended without permission ask")
			}

			type clearResult struct {
				id  session.SessionID
				err error
			}
			cleared := make(chan clearResult, 1)
			go func() {
				id, clearErr := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{})
				cleared <- clearResult{id: id, err: clearErr}
			}()
			// The cancellation result is emitted only after clear marked this exact
			// lifecycle and signalled it. Keep the lifecycle registered to make the
			// post-boundary approval race deterministic.
			for range run.Events() {
			}
			if _, err := svc.ApproveRun(t.Context(), source.ID, askID, verdict, ""); !errors.Is(err, server.ErrNoActiveRun) {
				t.Fatalf("ApproveRun after clear boundary = %v, want ErrNoActiveRun", err)
			}
			if _, promoted, _, err := svc.Steer(t.Context(), source.ID, "continue", nil, "", ""); !errors.Is(err, server.ErrNoActiveRun) || promoted {
				t.Fatalf("Steer after clear boundary = (promoted=%v, err=%v), want refusal", promoted, err)
			}
			if write.runs() != 0 {
				t.Fatalf("tool ran %d times after clear began", write.runs())
			}
			if got := learned.Rules(source.ID); len(got) != 0 {
				t.Fatalf("clear-racing verdict %v learned %d rules", verdict, len(got))
			}

			svc.FinishRun(source.ID, run)
			select {
			case got := <-cleared:
				if got.err != nil || got.id == "" {
					t.Fatalf("clear = (%q, %v)", got.id, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("clear did not finish after run deregistration")
			}
		})
	}
}

func TestClearSessionWaitsForExactRunningLifecycleToDeregister(t *testing.T) {
	bt := &blockingTool{started: make(chan struct{})}
	cat := tool.NewCatalog()
	cat.MustRegister(bt)
	llm := mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("c1", "Read", json.RawMessage(`{"path":"a.go"}`))))
	svc, store := newServiceWithEngine(t, llm, cat)

	source, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRunContent(t.Context(), source.ID, "run", nil)
	if err != nil {
		t.Fatal(err)
	}
	<-bt.started

	type result struct {
		id  session.SessionID
		err error
	}
	cleared := make(chan result, 1)
	go func() {
		id, clearErr := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{})
		cleared <- result{id: id, err: clearErr}
	}()

	drainRun(t, run)
	select {
	case got := <-cleared:
		t.Fatalf("clear returned before exact run deregistered: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}
	svc.FinishRun(source.ID, run)

	select {
	case got := <-cleared:
		if got.err != nil {
			t.Fatalf("clear: %v", got.err)
		}
		successor, loadErr := store.Load(t.Context(), got.id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(successor.Conversation.Messages) != 0 {
			t.Fatalf("successor history = %d, want empty", len(successor.Conversation.Messages))
		}
	case <-time.After(time.Second):
		t.Fatal("clear did not continue after FinishRun")
	}
}

func TestClearSessionWaitsForTerminalRegisteredLifecycle(t *testing.T) {
	svc, _ := newServiceWithEngine(t, mockllm.New(mockllm.TextTurn("done")), tool.NewCatalog())
	source, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.StartRunContent(t.Context(), source.ID, "run", nil)
	if err != nil {
		t.Fatal(err)
	}
	drainRun(t, run) // terminal aggregate, but relay ownership is still registered

	cleared := make(chan error, 1)
	go func() {
		_, clearErr := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{})
		cleared <- clearErr
	}()
	select {
	case err := <-cleared:
		t.Fatalf("clear returned before terminal lifecycle deregistered: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	svc.FinishRun(source.ID, run)
	select {
	case err := <-cleared:
		if err != nil {
			t.Fatalf("clear: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("clear did not continue after terminal lifecycle deregistered")
	}
}

func TestClearSessionCancelsDurableAwaitingSource(t *testing.T) {
	svc, store := newServiceWithEngine(t, mockllm.New(), tool.NewCatalog())
	source, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.RecordUserPrompt("approve", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.PauseForApproval(session.PendingAsk{AskID: "ask-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}

	successorID, err := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{})
	if err != nil {
		t.Fatalf("clear awaiting: %v", err)
	}
	cancelled, err := store.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != session.StateCancelled {
		t.Fatalf("source state = %q, want cancelled", cancelled.State)
	}
	if _, ok := cancelled.PendingAsk(); ok {
		t.Fatal("clear left durable approval pending")
	}
	successor, err := store.Load(t.Context(), successorID)
	if err != nil {
		t.Fatal(err)
	}
	if len(successor.Conversation.Messages) != 0 {
		t.Fatalf("successor history = %d, want empty", len(successor.Conversation.Messages))
	}
}

type clearResumeRaceStore struct {
	*memstore.Store
	failCreate       atomic.Bool
	armed            atomic.Bool
	loads            atomic.Int64
	firstLoad        chan struct{}
	releaseFirstLoad chan struct{}
	secondLoad       chan struct{}
}

func (s *clearResumeRaceStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	sess, err := s.Store.Load(ctx, id)
	if err != nil || !s.armed.Load() {
		return sess, err
	}
	switch s.loads.Add(1) {
	case 1:
		close(s.firstLoad)
		select {
		case <-s.releaseFirstLoad:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case 2:
		close(s.secondLoad)
	}
	return sess, nil
}

func (s *clearResumeRaceStore) Create(ctx context.Context, sess *session.Session) error {
	if s.failCreate.Load() {
		return errors.New("injected successor persistence failure")
	}
	return s.Store.Create(ctx, sess)
}

type clearResumeRacePlacement struct {
	testPlacementProvider
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (p *clearResumeRacePlacement) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.once.Do(func() {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return server.PlacementBinding{}, err
	}
	return p.testPlacementProvider.Reattach(ctx, req)
}

func TestClearSessionExcludesStaleAwaitingResume(t *testing.T) {
	store := &clearResumeRaceStore{
		Store:            memstore.New(),
		firstLoad:        make(chan struct{}),
		releaseFirstLoad: make(chan struct{}),
		secondLoad:       make(chan struct{}),
	}
	placement := &clearResumeRacePlacement{
		testPlacementProvider: testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}},
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
	var ran atomic.Int64
	catalog := tool.NewCatalog()
	catalog.MustRegister(&writeAskTool{ran: &ran})
	learned := permstore.New()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("done")),
		Catalog: catalog,
		Policy:  permpolicy.NewPolicy(nil, learned),
		Model:   "test-model",
		Store:   store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: placement,
		PlacementScope: "test", SharedEngineRoot: "/ws", Now: func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("write-1", "Write", json.RawMessage(`{"path":"a.go"}`))
	if err := source.RecordUserPrompt("write", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	const askID = "ask-1"
	if err := source.PauseForApproval(session.PendingAsk{AskID: askID, Tool: "Write", Call: call.ID}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	store.failCreate.Store(true)

	type clearResult struct {
		id  session.SessionID
		err error
	}
	clearDone := make(chan clearResult, 1)
	go func() {
		id, clearErr := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{})
		clearDone <- clearResult{id: id, err: clearErr}
	}()
	select {
	case <-placement.entered:
	case <-time.After(time.Second):
		t.Fatal("clear did not reach successor placement")
	}

	store.armed.Store(true)
	type approvalResult struct {
		run *agent.Run
		err error
	}
	approvalDone := make(chan approvalResult, 1)
	go func() {
		run, approveErr := svc.ApproveRun(t.Context(), source.ID, askID, session.VerdictAllowAlways, "")
		approvalDone <- approvalResult{run: run, err: approveErr}
	}()
	select {
	case <-store.firstLoad:
	case <-time.After(time.Second):
		t.Fatal("approval did not complete its pre-lock load")
	}
	close(store.releaseFirstLoad)
	// Before the repair resumeFromAwaiting performs a second, stale load here while
	// Clear owns runEntryMu. The repaired path waits and reloads only after Clear.
	select {
	case <-store.secondLoad:
	case <-time.After(100 * time.Millisecond):
	}
	close(placement.release)

	cleared := <-clearDone
	if cleared.err == nil || cleared.id != "" {
		t.Fatalf("clear with failed successor persistence = (%q, %v), want empty id and error", cleared.id, cleared.err)
	}
	approved := <-approvalDone
	if approved.run != nil {
		drainRun(t, approved.run)
		svc.FinishRun(source.ID, approved.run)
	}
	if !errors.Is(approved.err, server.ErrNoActiveRun) {
		t.Fatalf("approval after clear cancellation = %v, want ErrNoActiveRun", approved.err)
	}
	if ran.Load() != 0 {
		t.Fatalf("stale approval executed pending tool %d times", ran.Load())
	}
	if got := learned.Rules(source.ID); len(got) != 0 {
		t.Fatalf("stale approval learned %d policy rules", len(got))
	}
	persisted, err := store.Store.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != session.StateCancelled {
		t.Fatalf("persisted source state = %q, want cancelled", persisted.State)
	}
}

var _ port.SessionCreator = (*clearResumeRaceStore)(nil)

type failingSuccessorCreateStore struct {
	*memstore.Store
	fail bool
}

func (s *failingSuccessorCreateStore) Create(ctx context.Context, sess *session.Session) error {
	if s.fail {
		return errors.New("injected successor persistence failure")
	}
	return s.Store.Create(ctx, sess)
}

func TestClearSessionSuccessorPersistenceFailureKeepsCancellationBoundary(t *testing.T) {
	store := &failingSuccessorCreateStore{Store: memstore.New()}
	svc := newServiceWithEngineOverStore(t, store, mockllm.New())
	source, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := source.RecordUserPrompt("approve", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.PauseForApproval(session.PendingAsk{AskID: "ask-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}

	store.fail = true
	if successorID, err := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{}); err == nil || successorID != "" {
		t.Fatalf("clear with failed successor persistence = (%q, %v), want empty id and error", successorID, err)
	}
	cancelled, err := store.Load(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != session.StateCancelled {
		t.Fatalf("source state after failed successor persistence = %q, want cancelled", cancelled.State)
	}
	if _, ok := cancelled.PendingAsk(); ok {
		t.Fatal("failed successor persistence left source approval pending")
	}
	stored, err := store.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != source.ID {
		t.Fatalf("published sessions after failed successor persistence = %+v, want only source %q", stored, source.ID)
	}

	store.fail = false
	if successorID, err := svc.ClearSessionSuccessor(t.Context(), source.ID, server.SuccessorPlacement{}); err != nil || successorID == "" {
		t.Fatalf("retry clear = (%q, %v), want successor", successorID, err)
	}
}

func TestClearGenerationRejectsQueuedPromptAndSteer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		invoke func(context.Context, *server.Service, session.SessionID) (*agent.Run, error)
	}{
		{
			name: "prompt",
			invoke: func(ctx context.Context, svc *server.Service, id session.SessionID) (*agent.Run, error) {
				return svc.StartRunContent(ctx, id, "stale prompt", nil)
			},
		},
		{
			name: "promoted steer",
			invoke: func(ctx context.Context, svc *server.Service, id session.SessionID) (*agent.Run, error) {
				_, _, run, err := svc.Steer(ctx, id, "stale steer", nil, "", "")
				return run, err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			llm := mockllm.New(mockllm.TextTurn("done"))
			svc, store, placement, ctx := newClearGenerationRaceService(t, llm, true)
			source, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			store.armed.Store(true)

			type runResult struct {
				run *agent.Run
				err error
			}
			requestDone := make(chan runResult, 1)
			go func() {
				run, runErr := tc.invoke(ctx, svc, source.ID)
				requestDone <- runResult{run: run, err: runErr}
			}()
			awaitSignal(t, store.firstLoad, "request did not capture its generation before preflight")

			clearDone := make(chan error, 1)
			go func() {
				_, clearErr := svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
				clearDone <- clearErr
			}()
			awaitSignal(t, placement.entered, "clear did not cross its cancellation boundary")
			close(store.releaseFirstLoad)
			close(placement.release)
			if err := <-clearDone; err != nil {
				t.Fatalf("clear: %v", err)
			}
			result := <-requestDone
			if result.run != nil {
				drainRun(t, result.run)
				svc.FinishRun(source.ID, result.run)
			}
			if !errors.Is(result.err, server.ErrFailedPrecondition) {
				t.Fatalf("queued request error = %v, want ErrFailedPrecondition", result.err)
			}
			if got := llm.Calls(); got != 0 {
				t.Fatalf("model started %d times after clear boundary", got)
			}

			// Retirement is generation-scoped, not a permanent ban on the old id.
			fresh, err := svc.StartRunContent(ctx, source.ID, "fresh prompt", nil)
			if err != nil {
				t.Fatalf("fresh post-clear prompt: %v", err)
			}
			drainRun(t, fresh)
			svc.FinishRun(source.ID, fresh)
			if got := llm.Calls(); got != 1 {
				t.Fatalf("model calls after fresh request = %d, want 1", got)
			}
		})
	}
}

func TestClearGenerationRejectsQueuedPlanApprovalContinuation(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc, store, placement, ctx := newClearGenerationRaceService(t, llm, false)
	source, err := svc.CreateSession(ctx, session.ModePlan, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("plan-1", "PresentPlan", json.RawMessage(`{"note":"do it"}`))
	if err := source.RecordUserPrompt("plan", nil); err != nil {
		t.Fatal(err)
	}
	if err := source.BeginTurn(); err != nil {
		t.Fatal(err)
	}
	if err := source.RecordAssistant(session.NewAssistantMessage("", "", []session.ToolCall{call})); err != nil {
		t.Fatal(err)
	}
	if err := source.PauseForApproval(session.PendingAsk{
		AskID: "ask-plan", Tool: "PresentPlan", Call: call.ID, Origin: session.ApprovalOriginPlan,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	store.armed.Store(true)

	type planResult struct {
		events <-chan session.Event
		err    error
	}
	approvalDone := make(chan planResult, 1)
	go func() {
		events, approveErr := svc.ApprovePlan(ctx, source.ID, session.ModeDefault, "")
		approvalDone <- planResult{events: events, err: approveErr}
	}()
	awaitSignal(t, store.firstLoad, "plan approval did not capture its generation before preflight")
	clearDone := make(chan error, 1)
	go func() {
		_, clearErr := svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{})
		clearDone <- clearErr
	}()
	awaitSignal(t, placement.entered, "clear did not cross its cancellation boundary")
	close(store.releaseFirstLoad)
	close(placement.release)
	if err := <-clearDone; err != nil {
		t.Fatalf("clear: %v", err)
	}
	result := <-approvalDone
	if result.events != nil {
		for range result.events {
		}
	}
	if !errors.Is(result.err, server.ErrFailedPrecondition) {
		t.Fatalf("queued plan approval error = %v, want ErrFailedPrecondition", result.err)
	}
	if got := llm.Calls(); got != 0 {
		t.Fatalf("plan continuation started model %d times after clear boundary", got)
	}
}

func newClearGenerationRaceService(t *testing.T, llm *mockllm.Provider, ownershipEnforced bool) (*server.Service, *clearResumeRaceStore, *clearResumeRacePlacement, context.Context) {
	t.Helper()
	store := &clearResumeRaceStore{
		Store:            memstore.New(),
		firstLoad:        make(chan struct{}),
		releaseFirstLoad: make(chan struct{}),
		secondLoad:       make(chan struct{}),
	}
	placement := &clearResumeRacePlacement{
		testPlacementProvider: testPlacementProvider{root: "/ws", firstBind: &atomic.Bool{}},
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: tool.NewCatalog(), Model: "test-model", Store: store,
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine, Store: store, PlacementProvider: placement,
		PlacementScope: "test", SharedEngineRoot: "/ws", OwnershipEnforced: ownershipEnforced,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "test", Subject: "clear-generation"})
	return svc, store, placement, ctx
}

func awaitSignal(t *testing.T, ch <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

var _ port.SessionCreator = (*failingSuccessorCreateStore)(nil)
