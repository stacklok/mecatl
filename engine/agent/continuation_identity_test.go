package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestResumeApprovalCanonicalSameToolExactOccurrence(t *testing.T) {
	policy := permpolicy.NewPolicy([]governance.Rule{{Tool: "Write", Pattern: "first", Effect: governance.Allow}}, permstore.New())
	var executed []session.ToolCall
	write := &fakeTool{name: "Write", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed = append(executed, call)
		return session.NewToolResult(call.ID, "written"), nil
	}}
	first := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(
		toolCall("provider-first", "Write", `{"path":"first"}`),
		toolCall("provider-second", "Write", `{"path":"second","content":"retained"}`),
		toolCall("provider-third", "Write", `{"path":"third"}`))), Catalog: catalogWith(t, write), Policy: policy})
	sess := session.New("canonical-resume", session.ModeDefault, agent.MemEnv("/ws").Ref(), session.Limits{}, time.Unix(0, 0))
	askID, restored := driveToAwaiting(t, first, sess, agent.MemEnv("/ws"), "write")
	var calls []session.ToolCall
	// Find the accepted batch independently of how many earlier results were saved.
	for _, m := range restored.Conversation.Messages {
		if len(m.ToolCalls) == 3 {
			calls = m.ToolCalls
		}
	}
	ask, _ := restored.PendingAsk()
	if ask.Call != calls[1].ID || ask.Call == "provider-second" {
		t.Fatal("live ask did not retain the second canonical occurrence")
	}
	snap, err := sessnap.Of(restored)
	if err != nil {
		t.Fatal(err)
	}
	snap.Pending.Args = json.RawMessage(`{"command_summary":"old display only","path":"wrong"}`)
	restored, err = snap.Restore()
	if err != nil {
		t.Fatal(err)
	}
	executed = nil
	resumed := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t, write), Policy: policy})
	events := resumeEvents(resumed.ResumeApproval(t.Context(), restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce))
	if len(executed) != 1 || executed[0].ID != calls[1].ID || !bytes.Equal(executed[0].Args, calls[1].Args) {
		t.Fatalf("executed calls = %v; want only the retained second occurrence", executed)
	}
	if lastResult(t, events).Stop != session.StopEndTurn {
		t.Fatal("resume did not finish")
	}
	if err := session.ValidateToolPairing(restored.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}

func TestResumeApprovalCanonicalInvalidTargetIsInert(t *testing.T) {
	for _, name := range []string{"missing", "empty", "answered", "wrong-tool", "ambiguous", "earlier-turn", "wrong-ask"} {
		t.Run(name, func(t *testing.T) {
			env := agent.MemEnv("/ws")
			s := session.New("invalid-target", session.ModeDefault, env.Ref(), session.Limits{}, time.Unix(0, 0))
			call := session.NewToolCall("canonical-target", "Write", []byte(`{"path":"target"}`))
			if err := s.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAssistant(session.NewAssistantMessage("", []session.ToolCall{call})); err != nil {
				t.Fatal(err)
			}
			ask := session.PendingAsk{AskID: "control-prefix:ask", Call: call.ID, Tool: call.Name, Origin: session.ApprovalOriginPermission}
			askID := ask.AskID
			switch name {
			case "missing":
				ask.Call = "absent"
			case "empty":
				ask.Call = ""
			case "wrong-tool":
				ask.Tool = "Shell"
			case "answered":
				if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(call.ID, "already done")}); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				s.Conversation.Messages[0].ToolCalls = append(s.Conversation.Messages[0].ToolCalls, call)
			case "earlier-turn":
				s.Conversation.Append(session.NewAssistantMessage("later", nil))
			case "wrong-ask":
				askID = "other-control-prefix:ask"
			}
			if err := s.PauseForApproval(ask); err != nil {
				t.Fatal(err)
			}
			before := session.CloneMessages(s.Conversation.Messages)
			store := memstore.New()
			rules := permstore.New()
			policy := permpolicy.NewPolicy(nil, rules)
			ran := 0
			write := &fakeTool{name: "Write", exec: func(_ context.Context, c session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				ran++
				return session.NewToolResult(c.ID, "wrong"), nil
			}}
			provider := mockllm.New(mockllm.TextTurn("unreachable"))
			e := newEngine(agent.Deps{LLM: provider, Catalog: catalogWith(t, write), Policy: policy, Store: store})
			events := resumeEvents(e.ResumeApproval(t.Context(), s, tool.Environment{}, askID, session.VerdictAllowAlways))
			got, ok := s.PendingAsk()
			if ran != 0 || !ok || got.AskID != ask.AskID || s.State != session.StateAwaiting || !reflect.DeepEqual(before, s.Conversation.Messages) || provider.Calls() != 0 {
				t.Fatal("invalid target consumed the ask or caused effects")
			}
			if len(rules.Rules(s.ID)) != 0 {
				t.Fatal("invalid target learned an approval rule")
			}
			if _, err := store.Load(t.Context(), s.ID); !errors.Is(err, memstore.ErrNotFound) {
				t.Fatalf("unexpected save: %v", err)
			}
			if lastResult(t, events).Stop != session.StopError {
				t.Fatal("invalid target not rejected")
			}
		})
	}
}

func TestAuthorizationCanonicalInvalidContinuationIsInert(t *testing.T) {
	for _, name := range []string{"missing", "answered", "wrong-tool", "ambiguous", "wrong-suffix"} {
		t.Run(name, func(t *testing.T) {
			s := session.New("auth-invalid", session.ModeDefault, agent.MemEnv("/ws").Ref(), session.Limits{}, time.Unix(0, 0))
			call := session.NewToolCall("exact-call", "Write", []byte(`{"path":"exact"}`))
			if err := s.BeginTurn(); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAssistant(session.NewAssistantMessage("", []session.ToolCall{call})); err != nil {
				t.Fatal(err)
			}
			pending := session.PendingAuthorization{Authorization: session.ExternalAuthorization{ID: "auth", Binding: "bound", ExpiresAt: time.Unix(100, 0)}, Call: call}
			if err := s.PauseForAuthorization(pending); err != nil {
				t.Fatal(err)
			}
			pending, err := s.ClaimAuthorization()
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "missing":
				pending.Call.ID = "absent"
			case "wrong-tool":
				pending.Call.Name = "Shell"
			case "wrong-suffix":
				pending.Deferred = []session.ToolCall{session.NewToolCall("extra", "Write", nil)}
			case "answered":
				if err := s.RecordToolResults([]session.ToolResult{session.NewToolResult(call.ID, "already answered")}); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				s.Conversation.Messages[0].ToolCalls = append(s.Conversation.Messages[0].ToolCalls, call)
			}
			before := session.CloneMessages(s.Conversation.Messages)
			provider := mockllm.New(mockllm.TextTurn("unreachable"))
			e := newEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog()})
			resolution, _ := session.NewAuthorizationResolution(session.AuthorizationGranted)
			prepared, err := e.PrepareAuthorizationContinuation(t.Context(), s, tool.Environment{}, pending, resolution)
			if err == nil || prepared != nil || provider.Calls() != 0 || s.State != session.StateRunning || !reflect.DeepEqual(before, s.Conversation.Messages) {
				t.Fatal("invalid authorization continuation prepared or mutated")
			}
		})
	}
}

func TestAuthorizationMigratedContinuationKeepsExactEffectiveArguments(t *testing.T) {
	data, err := os.ReadFile("../adapter/sessnap/testdata/legacy-authorizing.json")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("eyJjb21tYW5kIjogICJlY2hvIGF1dGgifQ=="), []byte("eyJjb21tYW5kIjogICJlZmZlY3RpdmUifQ=="), 1)
	s, err := sessnap.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.ClaimAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	if string(pending.Call.Args) != `{"command":  "effective"}` {
		t.Fatal("effective arguments normalized or replaced")
	}
	var executed []session.ToolCall
	protected := &fakeTool{name: "Shell", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed = append(executed, call)
		return session.NewToolResult(call.ID, "executed"), nil
	}}
	mem := agent.MemEnv("/ws")
	env := tool.MustEnvironment(s.EnvironmentRef, mem.Workspace(), mem.ReadLedger(), nil)
	e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: catalogWith(t, protected), Policy: authorizationPolicy{}})
	resolution, _ := session.NewAuthorizationResolution(session.AuthorizationGranted)
	prepared, err := e.PrepareAuthorizationContinuation(t.Context(), s, env, pending, resolution)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := prepared.Start()
	events := resumeEvents(run)
	if len(executed) != 1 || executed[0].ID != pending.Call.ID || !bytes.Equal(executed[0].Args, pending.Call.Args) {
		t.Fatal("wrong authorization occurrence or arguments executed")
	}
	if n, _ := resultsFor(events, pending.Deferred[0].ID); n != 0 {
		t.Fatal("deferred same-tool sibling executed")
	}
	if err := session.ValidateToolPairing(s.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationHoldContinuationRejectsWithoutSaving(t *testing.T) {
	data, err := os.ReadFile("../adapter/sessnap/testdata/legacy-ambiguous-pending.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := sessnap.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	before, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	store := &saveCountingStore{inner: memstore.New()}
	if err := store.Save(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	provider := mockllm.New(mockllm.TextTurn("unreachable"))
	e := newEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Store: store})
	for _, start := range []func() *agent.Run{
		func() *agent.Run {
			return e.ResumeApproval(t.Context(), s, tool.Environment{}, "ask-ambiguous", session.VerdictAllowAlways)
		},
		func() *agent.Run {
			return e.Run(t.Context(), s, tool.Environment{}, agent.RunRequest{Text: "must not append"})
		},
		func() *agent.Run { return e.RetryFailedStep(t.Context(), s, tool.Environment{}) },
	} {
		if result := lastResult(t, resumeEvents(start())); result.Error != session.ErrMigrationHeld.Error() {
			t.Fatalf("hold error = %q", result.Error)
		}
	}
	if provider.Calls() != 0 || store.saves.Load() != 1 {
		t.Fatal("held session called the model or saved during rejection")
	}
	after, err := sessnap.Marshal(s)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("held session changed")
	}
	loaded, err := store.Load(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := sessnap.Marshal(loaded)
	if err != nil || !bytes.Equal(before, persisted) {
		t.Fatal("persisted hold changed")
	}
	resolution, _ := session.NewAuthorizationResolution(session.AuthorizationGranted)
	if _, err := e.PrepareAuthorizationContinuation(t.Context(), s, tool.Environment{}, session.PendingAuthorization{}, resolution); !errors.Is(err, session.ErrMigrationHeld) {
		t.Fatalf("authorization hold = %v", err)
	}
	if _, err := e.PrepareAfterAuthorization(t.Context(), s, tool.Environment{}, session.ExternalAuthorization{}, "", nil, resolution); !errors.Is(err, session.ErrMigrationHeld) {
		t.Fatalf("post-authorization hold = %v", err)
	}
}
