package agent_test

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSyntheticUserPromptReplay_Scenario1_EmissionOriginIsExplicit(t *testing.T) {
	parts := []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte("pixels")}}
	gate := newFirstTurnGate()
	llm := mockllm.NewWith(
		[]mockllm.Option{mockllm.WithRequestObserver(gate.observe)},
		mockllm.ToolCallTurn(toolCall("c1", "Probe", `{}`)),
		mockllm.EmptyTurn(),
		mockllm.TextTurn("done"),
	)
	eng := newEngine(agent.Deps{LLM: llm, Catalog: catalogWith(t, &probeTool{}), EnableSteer: true})
	sess := newSession(t, session.Limits{})
	run := eng.Run(context.Background(), sess, agent.EnvForWS(memfs.NewWorkspace("/ws"), nil), agent.RunRequest{
		Text:  "inspect this",
		Parts: parts,
	})
	gate.awaitEntered()
	if got := steerEnqueueOutcome(t, run, "principal steer"); got != agent.SteerAccepted {
		t.Fatalf("steer outcome = %q, want accepted", got)
	}
	gate.release()
	events := drainObserving(t, run, nil)

	var prompts []*session.UserPromptPayload
	for _, ev := range events {
		if ev.Type == session.EvUserPrompt {
			prompts = append(prompts, ev.UserPrompt)
		}
	}
	if len(prompts) != 3 {
		t.Fatalf("user_prompt events = %d, want principal, steer, and continuation", len(prompts))
	}
	if prompts[0] == nil || prompts[0].Synthetic || prompts[0].Text != "inspect this" || len(prompts[0].Parts) != 1 {
		t.Fatalf("principal prompt origin/content = %+v, want genuine multimodal prompt", prompts[0])
	}
	if prompts[1] == nil || prompts[1].Synthetic || prompts[1].Text != "principal steer" || len(prompts[1].Parts) != 0 {
		t.Fatalf("committed steer origin/content = %+v, want genuine text-only prompt", prompts[1])
	}
	if prompts[2] == nil || !prompts[2].Synthetic || prompts[2].Text == "" || len(prompts[2].Parts) != 0 {
		t.Fatalf("continuation origin/content = %+v, want synthetic text-only continuation", prompts[2])
	}
}

func TestInvariant_synthetic_user_prompt_preserves_child_event_isolation(t *testing.T) {
	const childPrompt = "CHILD-ONLY-PROMPT"
	child := childEngineWith(mockllm.New(mockllm.TextTurn("child summary")), catalogWith(t))
	parentCatalog := catalogWith(t, agent.NewSubagentTool(child))
	parent := newEngine(agent.Deps{
		LLM: mockllm.New(
			mockllm.ToolCallTurn(toolCall("p1", "Subagent", `{"prompt":"`+childPrompt+`"}`)),
			mockllm.TextTurn("parent done"),
		),
		Catalog: parentCatalog,
	})
	sess := newSession(t, session.Limits{})
	events := drain(parent.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "parent prompt"}))

	userPrompts := 0
	for _, ev := range events {
		if ev.Type != session.EvUserPrompt {
			continue
		}
		userPrompts++
		if ev.UserPrompt == nil {
			t.Fatal("parent user_prompt event has nil payload")
		}
		if ev.UserPrompt.Text == childPrompt || ev.UserPrompt.Synthetic {
			t.Fatalf("child prompt origin/content leaked into parent stream: %+v", ev.UserPrompt)
		}
	}
	if userPrompts != 1 {
		t.Fatalf("parent user_prompt events = %d, want only its principal prompt", userPrompts)
	}
}
