package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type workerAuthorityTool struct{ name string }

func (t workerAuthorityTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (workerAuthorityTool) ReadOnly() bool { return true }
func (workerAuthorityTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "ok"), nil
}

type workerAuthorityReviewer struct{ requests []ToolReviewRequest }

func (r *workerAuthorityReviewer) Review(_ context.Context, req ToolReviewRequest, _ ReviewEvidenceSource) (ToolReviewResult, error) {
	r.requests = append(r.requests, req)
	return ToolReviewResult{Assessment: ReviewAcceptable}, nil
}

type workerAuthorityPolicy struct{}

func (workerAuthorityPolicy) Evaluate(context.Context, session.SessionID, session.PermissionMode, session.ToolCall, tool.WorkspaceReader) governance.PermissionDecision {
	return governance.PermissionDecision{Effect: governance.Allow}
}
func (workerAuthorityPolicy) Learn(session.SessionID, session.ToolCall) {}

func TestADR_0363_ContextualGuardrails_Scenario4_WorkerAuthority(t *testing.T) {
	reviewer := &workerAuthorityReviewer{}
	root := newReviewRoot(reviewer, nil, nil)
	cases := []struct {
		role     string
		isolated bool
	}{
		{"main", false}, {"task", true}, {"task:read-write", false}, {"parallel", true}, {"member:researcher", true}, {"lead", true},
	}
	for i, tc := range cases {
		name := "Action" + tc.role
		cat := tool.NewCatalog()
		cat.MustRegister(workerAuthorityTool{name: name})
		deps := Deps{LLM: mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("call", name, []byte(`{}`))), mockllm.TextTurn("done")), Catalog: cat, Policy: workerAuthorityPolicy{}, Role: tc.role}
		eng := NewEngine(deps)
		env := MemEnv("/ws")
		sess := session.New(session.SessionID("worker-authority-"+time.Now().Add(time.Duration(i)).Format("150405.000000000")), session.ModeDefault, env.Ref(), session.Limits{}, time.Now())
		for range eng.Run(context.Background(), sess, env, RunRequest{Text: "act", reviewRoot: root, reviewIsolated: tc.isolated}).Events() {
		}
	}
	if len(reviewer.requests) != len(cases) {
		t.Fatalf("reviews=%d want=%d", len(reviewer.requests), len(cases))
	}
	for i, req := range reviewer.requests {
		if req.Caller.Role != cases[i].role || req.Caller.Isolated != cases[i].isolated {
			t.Errorf("request %d caller=%+v want role=%q isolated=%v", i, req.Caller, cases[i].role, cases[i].isolated)
		}
		if len(req.Caller.Capabilities) != 1 || req.Caller.Capabilities[0] != req.EffectiveCall.Name {
			t.Errorf("request %d widened capabilities: %+v", i, req.Caller.Capabilities)
		}
	}
}
