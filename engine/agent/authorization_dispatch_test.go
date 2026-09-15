package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type genericAuthorizationTool struct {
	fakeTool
	authorization session.ExternalAuthorization
	required      bool
	requestErr    error
	requestCancel context.CancelFunc
	requests      int
	aborts        int
	abortDeadline bool
	abortErr      error
	aborted       session.ExternalAuthorization
	requested     session.ToolCall
	order         *[]string
}

func (t *genericAuthorizationTool) RequestAuthorization(_ context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	t.requests++
	t.requested = call
	if t.order != nil {
		*t.order = append(*t.order, "authorization")
	}
	if t.requestCancel != nil {
		t.requestCancel()
	}
	return t.authorization, t.required, t.requestErr
}

func (t *genericAuthorizationTool) AbortAuthorization(ctx context.Context, authorization session.ExternalAuthorization) error {
	t.aborts++
	t.aborted = authorization
	_, t.abortDeadline = ctx.Deadline()
	return t.abortErr
}

type authorizationPolicy struct{ order *[]string }

func (p authorizationPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	if p.order != nil {
		*p.order = append(*p.order, "permission")
	}
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (authorizationPolicy) Learn(session.SessionID, session.ToolCall) {}

type authorizationHook struct {
	order   *[]string
	mutated []byte
}

func (h authorizationHook) Run(_ context.Context, event governance.HookEvent) (governance.HookOutcome, error) {
	if event.Phase != governance.PhasePreToolUse {
		return governance.HookOutcome{}, nil
	}
	if h.order != nil {
		*h.order = append(*h.order, "pre")
	}
	return governance.HookOutcome{Mutated: h.mutated}, nil
}

type askableAuthorizationHook struct{}

func (askableAuthorizationHook) Run(_ context.Context, event governance.HookEvent) (governance.HookOutcome, error) {
	if event.Phase == governance.PhasePreToolUse {
		return governance.HookOutcome{Block: true, AskApproval: true, Message: "review"}, nil
	}
	return governance.HookOutcome{}, nil
}

type authorizationFailStore struct{ err error }

func (s authorizationFailStore) Save(context.Context, *session.Session) error { return s.err }
func (authorizationFailStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, port.ErrSessionNotFound
}

type authorizationOrder struct {
	mu      sync.Mutex
	entries []string
}

func (o *authorizationOrder) add(s string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.entries = append(o.entries, s)
}
func (o *authorizationOrder) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return fmt.Sprint(o.entries)
}

type authorizationOrderStore struct {
	port.SessionStore
	order *authorizationOrder
}

type authorizationCancelStore struct {
	port.SessionStore
	cancel context.CancelFunc
	once   sync.Once
}

func (s *authorizationCancelStore) Save(ctx context.Context, sess *session.Session) error {
	if err := s.SessionStore.Save(ctx, sess); err != nil {
		return err
	}
	if sess.State == session.StateAuthorizing {
		s.once.Do(s.cancel)
	}
	return nil
}

func (s authorizationOrderStore) Save(ctx context.Context, sess *session.Session) error {
	if sess.State == session.StateAuthorizing {
		s.order.add("save")
	}
	return s.SessionStore.Save(ctx, sess)
}

type authorizationOrderSink struct{ order *authorizationOrder }

func (s authorizationOrderSink) Emit(_ context.Context, event session.Event) {
	if event.Type == session.EvAuthorizationRequired {
		s.order.add("event")
	}
}

func newGenericAuthorizationTool(name string) *genericAuthorizationTool {
	return &genericAuthorizationTool{
		fakeTool: fakeTool{name: name, readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			return session.NewToolResult(call.ID, "executed"), nil
		}},
		authorization: session.ExternalAuthorization{ID: "auth-1", DisplayName: "Calendar", Binding: "private-binding", ExpiresAt: time.Now().Add(time.Hour)},
		required:      true,
	}
}

func TestGenericAuthorizationGateOrderAndEffectiveCall(t *testing.T) {
	order := []string{}
	protected := newGenericAuthorizationTool("protected")
	protected.order = &order
	protected.exec = func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		order = append(order, "execute")
		return session.NewToolResult(call.ID, "ok"), nil
	}
	store := memstore.New()
	engine := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{"original":true}`))),
		Catalog: catalogWith(t, protected), Policy: authorizationPolicy{order: &order},
		Hooks: authorizationHook{order: &order, mutated: []byte(`{"effective":true}`)}, Store: store,
	})
	sess := newSession(t, session.Limits{})
	run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true})
	events := drain(run)

	if got := fmt.Sprint(order); got != "[permission pre permission authorization]" {
		t.Fatalf("gate order = %s", got)
	}
	if got := string(protected.requested.Args); got != `{"effective":true}` {
		t.Fatalf("requested args = %s", got)
	}
	pending, ok := sess.PendingAuthorization()
	if !ok || string(pending.Call.Args) != `{"effective":true}` {
		t.Fatalf("pending = %#v, %v", pending, ok)
	}
	if protected.aborts != 0 || run.Outcome() != agent.RunOutcomeAuthorizationPending {
		t.Fatalf("aborts/outcome = %d/%v", protected.aborts, run.Outcome())
	}
	for _, event := range events {
		if event.Type == session.EvResult {
			t.Fatal("authorization park emitted EvResult")
		}
		if event.Type == session.EvAuthorizationRequired && (event.Authorization == nil || event.Authorization.AuthorizationID != "auth-1" || event.Authorization.DisplayName != "Calendar" || event.Authorization.Status != session.AuthorizationPending) {
			t.Fatalf("unsafe or malformed authorization event: %#v", event.Authorization)
		}
	}
}

func TestGenericAuthorizationRequestFailureIsFatalAndDoesNotPark(t *testing.T) {
	protected := newGenericAuthorizationTool("protected")
	protected.requestErr = errors.New("broker unavailable")
	engine := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`))),
		Catalog: catalogWith(t, protected), Store: memstore.New(),
	})
	sess := newSession(t, session.Limits{})
	run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true})
	events := drain(run)

	if run.Outcome() != agent.RunOutcomeCompleted || sess.State != session.StateFailed {
		t.Fatalf("outcome/state = %v/%s", run.Outcome(), sess.State)
	}
	if _, ok := sess.PendingAuthorization(); ok {
		t.Fatal("request failure left a pending authorization")
	}
	if protected.aborts != 1 {
		t.Fatalf("aborts = %d, want 1", protected.aborts)
	}
	for _, event := range events {
		if event.Type == session.EvAuthorizationRequired {
			t.Fatal("request failure emitted authorization.required")
		}
	}
}

func TestGenericAuthorizationAbortFailureIsJoinedIntoFatalCause(t *testing.T) {
	protected := newGenericAuthorizationTool("protected")
	protected.requestErr = errors.New("request failed")
	protected.abortErr = errors.New("settlement failed")
	engine := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`))),
		Catalog: catalogWith(t, protected), Store: memstore.New(),
	})
	sess := newSession(t, session.Limits{})
	events := drain(engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true}))

	found := false
	for _, event := range events {
		found = found || (event.Type == session.EvResult && event.Result != nil && strings.Contains(event.Result.Error, "settlement failed"))
	}
	if sess.State != session.StateFailed || !found {
		t.Fatalf("state/found settlement cause = %s/%v", sess.State, found)
	}
}

func TestGenericAuthorizationCancellationAbortsWithoutParkingOrEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	protected := newGenericAuthorizationTool("protected")
	protected.requestCancel = cancel
	engine := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`))),
		Catalog: catalogWith(t, protected), Store: memstore.New(),
	})
	sess := newSession(t, session.Limits{})
	run := engine.Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true})
	events := drain(run)

	if run.Outcome() != agent.RunOutcomeCompleted || sess.State != session.StateCancelled {
		t.Fatalf("outcome/state = %v/%s", run.Outcome(), sess.State)
	}
	if _, ok := sess.PendingAuthorization(); ok {
		t.Fatal("cancelled request left a pending authorization")
	}
	if protected.aborts != 1 || !protected.abortDeadline {
		t.Fatalf("aborts/deadline = %d/%v, want 1/true", protected.aborts, protected.abortDeadline)
	}
	for _, event := range events {
		if event.Type == session.EvAuthorizationRequired {
			t.Fatal("cancelled request emitted authorization.required")
		}
	}
}

func TestGenericAuthorizationCancellationAfterParkSaveRollsBackBeforeEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &authorizationCancelStore{SessionStore: memstore.New(), cancel: cancel}
	protected := newGenericAuthorizationTool("protected")
	engine := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`))),
		Catalog: catalogWith(t, protected), Store: store,
	})
	sess := newSession(t, session.Limits{})
	events := drain(engine.Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true}))

	if sess.State != session.StateCancelled {
		t.Fatalf("state = %s, want cancelled", sess.State)
	}
	if _, ok := sess.PendingAuthorization(); ok {
		t.Fatal("cancelled saved park remained pending")
	}
	if protected.aborts != 1 {
		t.Fatalf("aborts = %d, want 1", protected.aborts)
	}
	loaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State == session.StateAuthorizing {
		t.Fatal("rollback left durable authorizing snapshot")
	}
	for _, event := range events {
		if event.Type == session.EvAuthorizationRequired {
			t.Fatal("cancellation after save emitted authorization.required")
		}
	}
}

func TestGenericAuthorizationRunsAfterHookApproval(t *testing.T) {
	protected := newGenericAuthorizationTool("protected")
	engine := newEngine(agent.Deps{
		LLM:         mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`))),
		Catalog:     catalogWith(t, protected),
		Hooks:       askableAuthorizationHook{},
		Interactive: true,
		Store:       memstore.New(),
	})
	sess := newSession(t, session.Limits{})
	run := engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true})
	for event := range run.Events() {
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	if protected.requests != 1 || run.Outcome() != agent.RunOutcomeAuthorizationPending || sess.State != session.StateAuthorizing {
		t.Fatalf("requests/outcome/state = %d/%v/%s", protected.requests, run.Outcome(), sess.State)
	}
}

func TestGenericAuthorizationRecordsEarlierAndDefersLaterSerially(t *testing.T) {
	executed := []string{}
	first := &fakeTool{name: "first", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed = append(executed, "first")
		return session.NewToolResult(call.ID, "first-result"), nil
	}}
	protected := newGenericAuthorizationTool("protected") // no DispatchSerial marker: dispatcher must still fail safe.
	later := &fakeTool{name: "later", readOnly: true, exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed = append(executed, "later")
		return session.NewToolResult(call.ID, "later-result"), nil
	}}
	store := memstore.New()
	engine := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(
		toolCall("first-call", first.name, `{}`), toolCall("protected-call", protected.name, `{}`), toolCall("later-call", later.name, `{}`),
	)), Catalog: catalogWith(t, first, protected, later), Store: store})
	sess := newSession(t, session.Limits{})
	drain(engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true}))

	if got := fmt.Sprint(executed); got != "[first]" {
		t.Fatalf("executed = %s", got)
	}
	pending, ok := sess.PendingAuthorization()
	if !ok || len(pending.Deferred) != 1 || pending.Deferred[0].ID != "later-call" {
		t.Fatalf("pending = %#v, %v", pending, ok)
	}
	loaded, err := store.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != session.StateAuthorizing || len(loaded.Conversation.Messages) == 0 {
		t.Fatalf("saved state = %s", loaded.State)
	}
	foundFirst := false
	for _, message := range loaded.Conversation.Messages {
		foundFirst = foundFirst || (message.ToolResult != nil && message.ToolResult.CallID == "first-call")
	}
	if !foundFirst {
		t.Fatal("earlier completed sibling result was not saved")
	}
}

func TestGenericAuthorizationPresentationAndCleanupMatrix(t *testing.T) {
	tests := []struct {
		name        string
		present     bool
		role        string
		store       port.SessionStore
		auth        session.ExternalAuthorization
		wantParked  bool
		wantRequest int
		wantAbort   int
	}{
		{name: "main capable", present: true, store: memstore.New(), auth: session.ExternalAuthorization{ID: "auth-1", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}, wantParked: true, wantRequest: 1},
		{name: "main cannot present", store: memstore.New(), auth: session.ExternalAuthorization{ID: "auth-1", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}},
		{name: "child cannot park", present: true, role: "subagent", store: memstore.New(), auth: session.ExternalAuthorization{ID: "auth-1", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}},
		{name: "pause failure", present: true, store: memstore.New(), auth: session.ExternalAuthorization{ID: "", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}, wantRequest: 1, wantAbort: 1},
		{name: "save failure", present: true, store: authorizationFailStore{err: errors.New("save failed")}, auth: session.ExternalAuthorization{ID: "auth-1", Binding: "binding", ExpiresAt: time.Now().Add(time.Hour)}, wantRequest: 1, wantAbort: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protected := newGenericAuthorizationTool("protected")
			protected.authorization = tt.auth
			engine := newEngine(agent.Deps{
				LLM:     mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`)), mockllm.TextTurn("done")),
				Catalog: catalogWith(t, protected), Store: tt.store, Role: tt.role,
			})
			sess := newSession(t, session.Limits{})
			events := drain(engine.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: tt.present}))
			if tt.wantParked {
				if protected.requests != tt.wantRequest || protected.aborts != 0 || sess.State != session.StateAuthorizing {
					t.Fatalf("requests/aborts/state = %d/%d/%s", protected.requests, protected.aborts, sess.State)
				}
				return
			}
			if protected.requests != tt.wantRequest || protected.aborts != tt.wantAbort {
				t.Fatalf("requests/aborts = %d/%d, want %d/%d", protected.requests, protected.aborts, tt.wantRequest, tt.wantAbort)
			}
			if tt.wantAbort == 1 && protected.aborted != tt.auth {
				t.Fatalf("aborted = %#v, want %#v", protected.aborted, tt.auth)
			}
			for _, event := range events {
				if event.Type == session.EvAuthorizationRequired {
					t.Fatal("authorization event emitted on failed presentation")
				}
			}
		})
	}
}

func TestGenericAuthorizationRequiredFalseExecutesAndSavePrecedesEvent(t *testing.T) {
	t.Run("required false", func(t *testing.T) {
		protected := newGenericAuthorizationTool("protected")
		protected.required = false
		executed := 0
		protected.exec = func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
			executed++
			return session.NewToolResult(call.ID, "ok"), nil
		}
		engine := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, protected), Store: memstore.New()})
		run := engine.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true})
		drain(run)
		if executed != 1 || protected.aborts != 0 || run.Outcome() != agent.RunOutcomeCompleted {
			t.Fatalf("executed/aborts/outcome = %d/%d/%v", executed, protected.aborts, run.Outcome())
		}
	})

	t.Run("save before event", func(t *testing.T) {
		order := &authorizationOrder{}
		protected := newGenericAuthorizationTool("protected")
		store := authorizationOrderStore{SessionStore: memstore.New(), order: order}
		engine := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("call-1", protected.name, `{}`))), Catalog: catalogWith(t, protected), Store: store, Sink: authorizationOrderSink{order: order}})
		drain(engine.Run(context.Background(), newSession(t, session.Limits{}), agent.MemEnv("/ws"), agent.RunRequest{Text: "go", CanPresentAuthorization: true}))
		if got := order.String(); got != "[save event]" {
			t.Fatalf("order = %s", got)
		}
	})
}
