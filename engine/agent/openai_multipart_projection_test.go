package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func TestADR_0302_EngineConcatenatesDistinctPartDeltasWithoutSeparators(t *testing.T) {
	llm := mockllm.New(mockllm.ChunksTurn(
		mockllm.TextChunk("alpha"),
		mockllm.TextChunk(" beta"),
		mockllm.TextChunk("\ngamma"),
		mockllm.DoneChunk(session.StopEndTurn),
	))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	events := drain(e.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "go"}))

	const want = "alpha beta\ngamma"
	result := lastResult(t, events)
	if result.Stop != session.StopEndTurn || result.Text != want {
		t.Fatalf("result = (stop %q, text %q), want (%q, %q)", result.Stop, result.Text, session.StopEndTurn, want)
	}
	if len(sess.Conversation.Messages) != 2 {
		t.Fatalf("conversation has %d messages, want user and assistant", len(sess.Conversation.Messages))
	}
	assistant := sess.Conversation.Messages[1]
	if assistant.Role != session.RoleAssistant || assistant.Text != want {
		t.Fatalf("assistant = (role %q, text %q), want exact separator-free concatenation %q", assistant.Role, assistant.Text, want)
	}
}
