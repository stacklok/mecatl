package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBuildContextualReviewerAllowsNewFileWriteWithoutMissingEvidenceAsk(t *testing.T) {
	cfg := guardrailE2ECfg(t, true, PostureYolo, "unused")
	provider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("write-new", "Write", json.RawMessage(`{"path":"new.txt","content":"reviewed"}`))),
		mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`),
		mockllm.TextTurn("done"),
	)
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider { return provider }
	built, err := Build(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 2})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRunContent(context.Background(), sess.ID, "write the new file", nil)
	if err != nil {
		t.Fatal(err)
	}
	asks := 0
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			asks++
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if asks != 0 {
		t.Fatalf("new-file Write raised %d spurious approval asks", asks)
	}
	got, err := os.ReadFile(filepath.Join(cfg.Workspace, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "reviewed" {
		t.Fatalf("new.txt = %q", got)
	}
}
