package agent_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memmemory"
	"github.com/stacklok/mecatl/engine/adapter/memorytools"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func memoryCatalog(t *testing.T, candidates []tool.Tool) *tool.Catalog {
	t.Helper()
	catalog := tool.NewCatalog()
	for _, candidate := range candidates {
		catalog.MustRegister(candidate)
	}
	return catalog
}

func TestExplicitMemoryWriteCarriesCurrentRunAttributionWhenLearningOff(t *testing.T) {
	store := memmemory.New()
	args, _ := json.Marshal(map[string]any{"key": "project/editor", "value": "helix"})
	eng := newEngine(agent.Deps{
		LLM:          mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("m1", "Remember", args)), mockllm.TextTurn("done")),
		Catalog:      memoryCatalog(t, memorytools.ProjectTools(store)),
		LearningMode: learning.Off,
	})
	sess := session.New("original-explicit-session", session.ModeDefault, "/ws", session.Limits{}, time.Unix(0, 0))
	drain(eng.Run(context.Background(), sess, agent.MemEnv("/ws"), agent.RunRequest{Text: "remember it"}))
	record, found, err := store.Inspect(context.Background(), "project/editor")
	if err != nil || !found {
		t.Fatalf("inspect = (%+v, %v, %v)", record, found, err)
	}
	if record.Current.Source.SessionID != "original-explicit-session" {
		t.Fatalf("source session = %q", record.Current.Source.SessionID)
	}
	if record.Current.Origin != tool.MemoryOriginExplicit {
		t.Fatalf("origin = %q", record.Current.Origin)
	}
}

func TestAutomaticReviewerAttributesWriteToCompletedParentTrajectory(t *testing.T) {
	store := memmemory.New()
	args, _ := json.Marshal(map[string]any{"key": "editor", "value": "prefers helix"})
	child := newEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.ToolCallTurn(session.NewToolCall("m1", "RememberUser", args)), mockllm.TextTurn("done")),
		Catalog: memoryCatalog(t, memorytools.UserTools(store)),
	})
	observer := agent.NewUserModelObserver(child)
	err := observer.Observe(context.Background(), learning.Trajectory{
		SessionID: "completed-parent", Workspace: "/ws",
		Messages: []session.Message{session.NewUserMessage("I prefer helix"), session.NewAssistantMessage("noted", "", nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, found, err := store.Inspect(context.Background(), "user/editor")
	if err != nil || !found {
		t.Fatalf("inspect = (%+v, %v, %v)", record, found, err)
	}
	got := record.Current
	if got.Source.SessionID != "completed-parent" || got.Origin != tool.MemoryOriginLearning || got.Writer != tool.MemoryWriterModel {
		t.Fatalf("review attribution = %+v", got)
	}
	if got.Source.SessionID == "usermodel-review-completed-parent" {
		t.Fatal("write cited reviewer child")
	}
}
