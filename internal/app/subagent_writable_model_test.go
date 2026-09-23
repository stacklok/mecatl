package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agents "github.com/stacklok/mecatl/engine/adapter/agentfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// TestBuildSubagentToolWritableModelOverrideE2E drives the REAL buildSubagentTool with a
// mode:"read-write"+model call (issue #285) and proves BOTH halves of the fix end-to-end:
// (a) the writable child's provider request carries the OVERRIDE model (not the parent's),
// and (b) its Write lands DIRECTLY in the REAL parent workspace (direct-write parity, ADR
// 0041). Before #285 the writable clobber discarded the per-call model engine and ran the
// DEFAULT writable model, so the observer would never see the override.
func TestBuildSubagentToolWritableModelOverrideE2E(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")

	cfg := teamCfg(t)
	cfg.Workspace = repo

	const overrideModel = "gpt-5-mini"
	var (
		mu     sync.Mutex
		models []string
	)
	// The child (writable explorer minted on the override model) writes a real file, then
	// summarises. The observer records every request's model so we can prove the child ran
	// on the override, not the parent default (cfg.Model).
	childProvider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		mu.Lock()
		models = append(models, r.Model)
		mu.Unlock()
	})},
		mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", []byte(`{"path":"beta.txt","content":"written on the override model\n"}`))),
		mockllm.TextTurn("did the work"),
	)
	task, closeFn := buildSubagentTool(context.Background(),
		cfg, regForTest(childProvider, providerMock, cfg.Model), childProvider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, catalogAssets{}, false)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	parentWS := osfsWSForTest(t, repo)
	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"edit beta","mode":"read-write","model":"`+overrideModel+`"}`)),
		testEnvironment(parentWS, buildCommandRunner(cfg)))
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("writable+model subagent returned an error: %q", res.Content)
	}
	// (b) The edit landed DIRECTLY in the real repo.
	if got, rerr := os.ReadFile(filepath.Join(repo, "beta.txt")); rerr != nil {
		t.Fatalf("the writable subagent's edit did not land in the real repo: %v", rerr)
	} else if !strings.Contains(string(got), "written on the override model") {
		t.Fatalf("beta.txt content unexpected: %q", got)
	}
	// (a) The child ran on the OVERRIDE model (the per-call model took effect on the
	// writable engine — the #285 fix; pre-fix the clobber ran the default writable model).
	mu.Lock()
	defer mu.Unlock()
	var sawOverride bool
	for _, m := range models {
		if m == overrideModel {
			sawOverride = true
		}
	}
	if !sawOverride {
		t.Fatalf("no writable child request carried the override model %q (models=%v) — the per-call model was discarded", overrideModel, models)
	}
}
