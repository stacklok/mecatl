// Package main demonstrates registering a custom mecatl tool.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type pingTool struct{}

func (pingTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Ping", Description: "Return a greeting.", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (pingTool) ReadOnly() bool { return true }
func (pingTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult(call.ID, "pong"), nil
}

func main() {
	catalog := tool.NewCatalog()
	catalog.MustRegister(pingTool{})
	policy := permpolicy.NewPolicy([]governance.Rule{{Tool: "Ping", Effect: governance.Allow}}, permstore.New())
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("call-1", "Ping", json.RawMessage(`{}`))),
		mockllm.TextTurn("The tool replied: pong.")), Catalog: catalog, Policy: policy, Model: "mock"})
	ws := memfs.NewWorkspace("/workspace")
	env := tool.MustEnvironment(session.EnvironmentRef{}, ws, memledger.New(), nil)
	sess := session.New("first-agent-tool", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	for event := range engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "Ping the tool."}).Events() {
		if event.Type == session.EvToolCall || event.Type == session.EvToolResult || event.Type == session.EvResult {
			fmt.Println(event.Type)
		}
	}
}
