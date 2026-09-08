package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionTitleGeneration_PromptIngressCapturesOnlyPrincipalPrompts(t *testing.T) {
	llm := mockllm.New(
		mockllm.TextTurn("one"),
		mockllm.TextTurn("two"),
		mockllm.TextTurn("three"),
		mockllm.TextTurn("four"),
	)
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	sess.SetTitleGeneration(session.TitleGenerationPending)
	env := agent.EnvForWS(memfs.NewWorkspace("/ws"), nil)

	for _, prompt := range []string{"first", "second", "third", "fourth"} {
		drain(e.Run(context.Background(), sess, env, agent.RunRequest{Text: prompt}))
		if prompt != "fourth" {
			if err := sess.Reopen(); err != nil {
				t.Fatalf("Reopen after %q: %v", prompt, err)
			}
		}
	}
	if got, want := sess.TitleSourcePrompts(), []string{"first", "second", "third"}; !sameTitlePrompts(got, want) {
		t.Fatalf("TitleSourcePrompts = %#v, want %#v", got, want)
	}
}

func sameTitlePrompts(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
