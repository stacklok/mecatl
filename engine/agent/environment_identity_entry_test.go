package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestInvariant_session_environment_identity_required(t *testing.T) {
	t.Run("aggregate construction rejects an invalid ref", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("session.New accepted an invalid EnvironmentRef")
			}
		}()
		_ = session.New("invalid", session.ModeDefault, session.EnvironmentRef{}, session.Limits{}, time.Unix(0, 0))
	})

	t.Run("run rejects a mismatched live environment before provider access", func(t *testing.T) {
		llm := mockllm.New(mockllm.TextTurn("must not run"))
		eng := agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog()})
		sessionRef := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "session", Revision: "v1"}
		envRef := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "other", Revision: "v1"}
		sess := session.New("mismatch", session.ModeDefault, sessionRef, session.Limits{}, time.Unix(0, 0))
		env := tool.MustEnvironment(envRef, memfs.NewWorkspace("/private/other"), nil)

		run := eng.Run(context.Background(), sess, env, agent.RunRequest{Text: "hello"})
		var cause string
		for ev := range run.Events() {
			if ev.Result != nil {
				cause = ev.Result.Error
			}
		}
		if llm.Calls() != 0 {
			t.Fatalf("provider calls = %d, want 0", llm.Calls())
		}
		if !strings.Contains(cause, "environment identity mismatch") {
			t.Fatalf("terminal cause = %q, want identity mismatch", cause)
		}
	})
}
