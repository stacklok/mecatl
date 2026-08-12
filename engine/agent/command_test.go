package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// TestCommandExpanderExpandsRecordedPrompt verifies that, with a
// DirCommandExpander wired in, a "/cmd args" user prompt is recorded EXPANDED
// (the template body with substitutions) so the model sees the expanded text,
// not the raw invocation.
func TestCommandExpanderExpandsRecordedPrompt(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(context.Background(), ".mecatl/commands/review.md",
		[]byte("Please review $1 carefully.")); err != nil {
		t.Fatalf("seed command: %v", err)
	}

	llm := mockllm.New(mockllm.TextTurn("done"))
	e := newEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t),
		CommandExpander: prompt.NewDirCommandExpander(),
	})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "/review foo.go"})
	drain(r)

	var found bool
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "Please review foo.go carefully.") {
			found = true
		}
		if m.Role == session.RoleUser && strings.Contains(m.Text, "/review foo.go") {
			t.Fatalf("raw command invocation recorded instead of expansion: %q", m.Text)
		}
	}
	if !found {
		t.Fatalf("expanded command text not found in conversation: %+v", sess.Conversation.Messages)
	}
}

// TestCommandExpanderLeavesNonCommandUnchanged verifies that with a
// DirCommandExpander wired in, a plain prompt is recorded verbatim.
func TestCommandExpanderLeavesNonCommandUnchanged(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	llm := mockllm.New(mockllm.TextTurn("done"))
	e := newEngine(agent.Deps{
		LLM:             llm,
		Catalog:         catalogWith(t),
		CommandExpander: prompt.NewDirCommandExpander(),
	})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "hello there"})
	drain(r)

	var found bool
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && m.Text == "hello there" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plain prompt not recorded verbatim: %+v", sess.Conversation.Messages)
	}
}

// TestDefaultExpanderUnchanged verifies the default (no CommandExpander wired →
// NoopExpander) records the raw text, preserving v1 behaviour.
func TestDefaultExpanderUnchanged(t *testing.T) {
	ws := memfs.NewWorkspace("/ws")
	if err := ws.Write(context.Background(), ".mecatl/commands/review.md",
		[]byte("Please review $1.")); err != nil {
		t.Fatalf("seed command: %v", err)
	}

	llm := mockllm.New(mockllm.TextTurn("done"))
	e := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t)})
	sess := newSession(t, session.Limits{})
	r := e.Run(context.Background(), sess, ws, agent.RunRequest{Text: "/review foo.go"})
	drain(r)

	// With the default NoopExpander, the raw "/review foo.go" must be recorded
	// even though a matching command file exists.
	var found bool
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && m.Text == "/review foo.go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("default expander must record raw text: %+v", sess.Conversation.Messages)
	}
}
