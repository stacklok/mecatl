// Package main demonstrates the minimal offline mecatl agent.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func main() {
	workspace := memfs.NewWorkspace("/workspace")
	env := tool.MustEnvironment(session.EnvironmentRef{}, workspace, memledger.New(), nil)
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("Hello from your first agent.")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   "mock",
	})
	sess := session.New("first-agent", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	run := engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "Say hello."})
	for event := range run.Events() {
		if event.Type == session.EvResult && event.Result != nil {
			fmt.Println(event.Result.Text)
		}
	}
}
