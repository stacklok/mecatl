// Package main demonstrates resolving a mecatl permission request.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type approvalTool struct{}

func (approvalTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Ping", Description: "Return a greeting.", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (approvalTool) ReadOnly() bool { return false }
func (approvalTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "pong"), nil
}

func main() {
	catalog := tool.NewCatalog()
	catalog.MustRegister(approvalTool{})
	policy := permpolicy.NewPolicy([]governance.Rule{{Tool: "Ping", Effect: governance.Ask}}, permstore.New())
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("call-1", "Ping", json.RawMessage(`{}`))),
		mockllm.TextTurn("The approved tool replied: pong.")), Catalog: catalog, Policy: policy, Model: "mock"})
	ws := memfs.NewWorkspace("/workspace")
	env := tool.MustEnvironment(session.EnvironmentRef{}, ws, nil)
	sess := session.New("first-agent-approval", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	run := engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "Ask before pinging the tool."})
	for event := range run.Events() {
		fmt.Println(event.Type)
		if event.Type == session.EvPermissionAsk && event.Ask != nil {
			run.Approve(event.Ask.AskID, session.VerdictAllowOnce)
		}
	}
}
