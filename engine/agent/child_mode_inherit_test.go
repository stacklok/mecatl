package agent_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestEffectiveChildMode pins the inheritance table: a default-configured child
// inherits an auto or yolo parent; other parents and explicitly configured
// child modes are kept.
func TestEffectiveChildMode(t *testing.T) {
	for _, tc := range []struct {
		configured, parent, want session.PermissionMode
	}{
		{session.ModeDefault, session.ModeYolo, session.ModeYolo},
		{session.ModeDefault, session.ModeAuto, session.ModeAuto},
		{session.ModeDefault, session.ModeAccept, session.ModeDefault},
		{session.ModeDefault, session.ModePlan, session.ModeDefault},
		{session.ModeDefault, "", session.ModeDefault},
		{session.ModePlan, session.ModeYolo, session.ModePlan},
		{session.ModeAccept, session.ModeAuto, session.ModeAccept},
	} {
		if got := agent.EffectiveChildModeForTest(tc.configured, tc.parent); got != tc.want {
			t.Errorf("effectiveChildMode(%q, parent %q) = %q, want %q", tc.configured, tc.parent, got, tc.want)
		}
	}
}

// TestSubagentInheritsYoloParentMode drives a real parent run: a child tool
// that asks under the default mode (no rules, headless → denied) runs when the
// parent session is in yolo, because the child inherits the parent's mode.
func TestSubagentInheritsYoloParentMode(t *testing.T) {
	for _, tc := range []struct {
		parent session.PermissionMode
		want   bool
	}{
		{session.ModeDefault, false},
		{session.ModeYolo, true},
	} {
		var executed atomic.Bool
		childTool := &fakeTool{name: "Mutate", readOnly: false,
			exec: func(_ context.Context, in session.ToolCall, _ tool.Workspace) (session.ToolResult, error) {
				executed.Store(true)
				return session.NewToolResult(in.ID, "ran"), nil
			}}
		childEngine := agent.NewEngine(agent.Deps{
			LLM: mockllm.New(
				mockllm.ToolCallTurn(toolCall("k1", "Mutate", `{}`)),
				mockllm.TextTurn("child done"),
			),
			Catalog: catalogWith(t, childTool),
			Policy:  permpolicy.NewPolicy(nil, nil),
			Model:   "child-model",
		})
		task := agent.NewSubagentTool(childEngine)
		e := newEngine(agent.Deps{LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"go"}`)),
			mockllm.TextTurn("ok"),
		), Catalog: catalogWith(t, task)})
		sess := newSession(t, session.Limits{})
		if err := sess.SetMode(tc.parent); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("parent run did not complete")
		}
		if executed.Load() != tc.want {
			t.Errorf("parent %s: child mutating tool executed=%t, want %t", tc.parent, executed.Load(), tc.want)
		}
	}
}
