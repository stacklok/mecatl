package agent

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type delayedSnapshotWorkspace struct {
	tool.Workspace
	reads int
}

func (w *delayedSnapshotWorkspace) ReadVersionBounded(_ context.Context, _ string, _ int64) ([]byte, tool.FileVersion, error) {
	w.reads++
	<-time.After(50 * time.Second)
	return nil, tool.FileVersion{}, fs.ErrNotExist
}

type reviewPathTool struct {
	name       string
	executions *int
}

func (t reviewPathTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (t reviewPathTool) ReadOnly() bool { return t.name == "Read" }
func (t reviewPathTool) Execute(_ context.Context, c session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	*t.executions++
	return session.NewToolResult(c.ID, "done"), nil
}

func TestActionDependencySnapshotSpendsReviewBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executions := 0
		catalog := tool.NewCatalog()
		catalog.MustRegister(reviewPathTool{name: "Write", executions: &executions})
		catalog.MustRegister(reviewPathTool{name: "Read", executions: &executions})
		reviewer := &lateAcceptReviewer{job: ReviewJobAction, delay: 41 * time.Second}
		base := memEnv("/ws")
		ws := &delayedSnapshotWorkspace{Workspace: base.Workspace()}
		env := tool.MustEnvironment(base.Ref(), ws, base.ReadLedger(), nil)
		engine := NewEngine(Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", "Write", []byte(`{"path":"a","content":"x"}`))), mockllm.TextTurn("done")), Catalog: catalog, Policy: terminalFailurePolicy{job: ReviewJobAction}, ToolReviewer: reviewer})
		sess := session.New("snapshot-budget", session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		timedOut := false
		for ev := range engine.Run(context.Background(), sess, env, RunRequest{Text: "write"}).Events() {
			if ev.Hook != nil && ev.Hook.Guardrail != nil && ev.Hook.Guardrail.ReasonCode == string(ReviewFailureTimeout) {
				timedOut = true
			}
		}
		if ws.reads != 1 || reviewer.calls != 1 || reviewer.remaining != 40*time.Second || !timedOut || executions != 1 {
			t.Fatalf("snapshot reads=%d reviewer calls=%d remaining=%v timeout=%t executions=%d", ws.reads, reviewer.calls, reviewer.remaining, timedOut, executions)
		}
	})
}
