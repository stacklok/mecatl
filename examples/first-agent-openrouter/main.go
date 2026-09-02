// Package main demonstrates using the mecatl engine with OpenRouter.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/provider/openai"
)

func main() {
	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = "openai/gpt-5.6-luna"
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     openai.New(openai.WithAPIKey(os.Getenv("OPENROUTER_API_KEY")), openai.WithBaseURL("https://openrouter.ai/api/v1")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, permstore.New()),
		Model:   model,
	})
	ws := memfs.NewWorkspace("/workspace")
	env := tool.MustEnvironment(session.EnvironmentRef{}, ws, nil)
	sess := session.New("first-agent-openrouter", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	run := engine.Run(context.Background(), sess, env, agent.RunRequest{Text: "Reply with exactly: Hello from OpenRouter."})
	for event := range run.Events() {
		if event.Type == session.EvResult && event.Result != nil {
			if event.Result.Error != "" {
				fmt.Fprintln(os.Stderr, event.Result.Error)
				os.Exit(1)
			}
			fmt.Println(event.Result.Text)
		}
	}
}
