package agent_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type canonicalResultRecorder struct {
	calls   []session.ToolCall
	results []session.ToolResult
}

func (r *canonicalResultRecorder) ToolCall(_ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	r.calls = append(r.calls, call)
	r.results = append(r.results, result)
}

func TestCanonicalDispatchRejectsForeignToolResultIdentity(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "serial", true: "read batch"}[readOnly], func(t *testing.T) {
			recorder := &canonicalResultRecorder{}
			act := &fakeTool{name: "Act", readOnly: readOnly, exec: func(_ context.Context, _ session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				return session.NewToolResult("private-foreign-token", "must not release"), nil
			}}
			sess := newSession(t, session.Limits{})
			e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("provider", "Act", `{}`)), mockllm.TextTurn("done")), Catalog: catalogWith(t, act), ToolCallRecorder: recorder})
			events := drain(e.Run(t.Context(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			id := boundCallID(t, sess, "provider")
			available, final := 0, 0
			for _, event := range events {
				if event.ToolResult == nil {
					continue
				}
				if event.ToolResult.CallID != id || !event.ToolResult.IsError || strings.Contains(event.ToolResult.Content, "private-foreign-token") || strings.Contains(event.ToolResult.Content, "must not release") {
					t.Fatalf("foreign result escaped: %+v", event.ToolResult)
				}
				switch event.Type {
				case session.EvToolResultAvailable:
					available++
				case session.EvToolResult:
					final++
				}
			}
			if available != 1 || final != 1 || len(recorder.calls) != 1 || recorder.calls[0].ID != id || recorder.results[0].CallID != id {
				t.Fatalf("canonical publication/recorder mismatch: available=%d final=%d recorder=%+v", available, final, recorder)
			}
			if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCanonicalCancelledResumePairsPendingAndSibling(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executed := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed++
		return session.NewToolResult(call.ID, "wrong effect"), nil
	}}
	catalog := catalogWith(t, act)
	sess := newSession(t, session.Limits{})
	first := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("prepared", "Act", `{"position":"prepared"}`), toolCall("sibling", "Act", `{}`))), Catalog: catalog, Policy: &identityAskPolicy{}})
	askID, restored := driveToAwaiting(t, first, sess, agent.MemEnv("/ws"), "go")
	resume := newEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("must not run")), Catalog: catalog, Policy: cancelReadPreparationPolicy{cancel: cancel, at: "prepared"}})
	events := drain(resume.ResumeApproval(ctx, restored, agent.MemEnv("/ws"), askID, session.VerdictAllowOnce))
	want := []session.ToolCallID{boundCallID(t, restored, "prepared"), boundCallID(t, restored, "sibling")}
	var final, recorded []session.ToolCallID
	for _, event := range events {
		if event.Type == session.EvToolResult {
			final = append(final, event.ToolResult.CallID)
			if !event.ToolResult.IsError {
				t.Fatal("cancelled resume published success")
			}
		}
	}
	for _, message := range restored.Conversation.Messages {
		if message.ToolResult != nil {
			recorded = append(recorded, message.ToolResult.CallID)
		}
	}
	if executed != 0 || restored.State != session.StateCancelled || restored.Counters.ToolCalls != 2 || !reflect.DeepEqual(final, want) || !reflect.DeepEqual(recorded, want) {
		t.Fatalf("executed=%d state=%s counters=%+v final=%v recorded=%v want=%v", executed, restored.State, restored.Counters, final, recorded, want)
	}
	if err := session.ValidateToolPairing(restored.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalCancelledSerialPreparationPairsEveryCall(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	executed := 0
	act := &fakeTool{name: "Act", exec: func(_ context.Context, call session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
		executed++
		return session.NewToolResult(call.ID, "wrong effect"), nil
	}}
	sess := newSession(t, session.Limits{})
	e := newEngine(agent.Deps{LLM: mockllm.New(mockllm.ToolCallTurn(toolCall("prepared", "Act", `{"position":"prepared"}`), toolCall("sibling", "Act", `{}`))), Catalog: catalogWith(t, act), Policy: cancelReadPreparationPolicy{cancel: cancel, at: "prepared"}})
	events := drain(e.Run(ctx, sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
	want := []session.ToolCallID{boundCallID(t, sess, "prepared"), boundCallID(t, sess, "sibling")}
	var final, available, recorded []session.ToolCallID
	for _, event := range events {
		if event.ToolResult == nil {
			continue
		}
		if !event.ToolResult.IsError || !strings.Contains(event.ToolResult.Content, "cancelled") {
			t.Fatalf("non-cancellation result: %+v", event.ToolResult)
		}
		switch event.Type {
		case session.EvToolResult:
			final = append(final, event.ToolResult.CallID)
		case session.EvToolResultAvailable:
			available = append(available, event.ToolResult.CallID)
		}
	}
	for _, message := range sess.Conversation.Messages {
		if message.ToolResult != nil {
			recorded = append(recorded, message.ToolResult.CallID)
		}
	}
	if executed != 0 || sess.State != session.StateCancelled || sess.Counters.ToolCalls != 2 || !reflect.DeepEqual(final, want) || !reflect.DeepEqual(available, want) || !reflect.DeepEqual(recorded, want) {
		t.Fatalf("executed=%d state=%s counters=%+v final=%v available=%v recorded=%v want=%v", executed, sess.State, sess.Counters, final, available, recorded, want)
	}
	if err := session.ValidateToolPairing(sess.Conversation.Messages); err != nil {
		t.Fatal(err)
	}
}
