package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// worktree_session_test.go is the acceptance e2e for issue #102: a session
// created against a SIBLING git worktree writes to THAT worktree, not the
// original checkout, and a restarted process rehydrates the SAME worktree-rooted
// engine.

// wsRunGit + wsInitRepo + wsCommit mirror the forker_test.go helpers locally
// (the test cannot cross-import a `_test` package).
func wsRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func wsInitRepo(t *testing.T, dir string) {
	t.Helper()
	wsRunGit(t, dir, "init")
	wsRunGit(t, dir, "config", "user.email", "test@example.com")
	wsRunGit(t, dir, "config", "user.name", "Test")
}

func wsCommit(t *testing.T, dir string, msg string) {
	t.Helper()
	wsRunGit(t, dir, "add", "-A")
	wsRunGit(t, dir, "commit", "-m", msg)
}

// TestWorktreeSessionWritesToWorktreeNotBase is THE acceptance test for issue
// #102. It builds a real git repo (base) + a sibling worktree (wtB), builds the
// FULL composition (app.Build) with the SERVER workspace = base, creates a
// session rooted at wtB, drives one turn whose mockllm calls Write, and asserts
// the file landed in wtB and NOT in base — i.e. the worktree-rooted session's
// file tools are scoped to the chosen worktree, exactly the issue's acceptance
// criterion.
func TestClientWorkspaceCannotOverrideServerPlacement(t *testing.T) {
	ctx := context.Background()

	base := t.TempDir()
	wsInitRepo(t, base)
	if err := os.WriteFile(filepath.Join(base, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	wsCommit(t, base, "init")

	wtB := filepath.Join(filepath.Dir(base), filepath.Base(base)+"-wtB")
	wsRunGit(t, base, "worktree", "add", "--detach", wtB)
	if _, err := os.Stat(wtB); err != nil {
		t.Fatalf("worktree wtB not created: %v", err)
	}
	if base == wtB {
		t.Fatal("base and wtB are the same path — fixture broken")
	}

	built, err := Build(ctx, Config{
		Workspace:           base, // the SERVER's launch root — DefaultWorkspace
		NoSoul:              true,
		MemoryDir:           t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(
				mockllm.ToolCallTurn(session.ToolCall{
					ID:   "w1",
					Name: "Write",
					Args: json.RawMessage(`{"path":"marker.txt","content":"x"}`),
				}),
				mockllm.TextTurn("wrote it"),
			)
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	svc := built.Service

	sess, err := svc.CreateSession(ctx, wtB, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession(wtB): %v", err)
	}
	if sess.EnvironmentRef.ID != base {
		t.Fatalf("session placement = %q, want server-owned %q", sess.EnvironmentRef.ID, base)
	}

	run, err := svc.StartRun(ctx, sess.ID, "write a marker file")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	events := runEvents(run)

	if !sawDispatchedTool(events, "w1") {
		t.Fatal("Write did not dispatch")
	}
	for _, ev := range events {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "w1" && ev.ToolResult.IsError {
			t.Fatalf("Write failed in the worktree session: %s", ev.ToolResult.Content)
		}
	}

	markerBase := filepath.Join(base, "marker.txt")
	if _, err := os.Stat(markerBase); err != nil {
		t.Fatalf("marker.txt missing from server-owned workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtB, "marker.txt")); err == nil {
		t.Fatal("client-supplied workspace escaped the server-owned placement")
	}
}

// TestWorktreeSessionRehydratesAfterRestart asserts a worktree session's
// per-session engine is rebuilt after a REAL process restart (a second app.Build
// over the SAME session store dir), so a prompt run after the restart completes
// and STILL writes to the worktree root — proving the needsRehydration widening
// fires for Workspace != DefaultWorkspace and the rehydrated engine rebinds to
// the persisted worktree. (The pure needsRehydration boolean is pinned directly
// in internal/adapter/server/worktree_engine_test.go via the export_test seam.)
func TestServerPlacementReattachesAfterRestart(t *testing.T) {
	ctx := context.Background()

	base := t.TempDir()
	wsInitRepo(t, base)
	if err := os.WriteFile(filepath.Join(base, "README"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	wsCommit(t, base, "init")
	wtB := filepath.Join(filepath.Dir(base), filepath.Base(base)+"-wtB")
	wsRunGit(t, base, "worktree", "add", "--detach", wtB)

	storeDir := t.TempDir()
	provider := func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			mockllm.ToolCallTurn(session.ToolCall{
				ID:   "w2",
				Name: "Write",
				Args: json.RawMessage(`{"path":"after-restart.txt","content":"y"}`),
			}),
			mockllm.TextTurn("ok"),
		)
	}

	// First process: create the worktree session + run once to terminal.
	built1, err := Build(ctx, Config{
		Workspace:           base,
		NoSoul:              true,
		MemoryDir:           t.TempDir(),
		StoreDir:            storeDir, // shared with the restart — a real session store
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: provider,
	})
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, wtB, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession(wtB): %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "first")
	if err != nil {
		t.Fatalf("StartRun 1: %v", err)
	}
	_ = runEvents(run1)
	built1.Close()

	// Second process (restart): rebuild over the SAME store. The worktree
	// session's in-memory engine is gone; rehydration must rebuild it.
	built2, err := Build(ctx, Config{
		Workspace:           base,
		NoSoul:              true,
		MemoryDir:           t.TempDir(),
		StoreDir:            storeDir,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-test"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: provider,
	})
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	defer built2.Close()

	run2, err := built2.Service.StartRun(ctx, sess.ID, "after restart")
	if err != nil {
		t.Fatalf("StartRun after restart (rehydrate): %v", err)
	}
	_ = runEvents(run2)

	afterRestart := filepath.Join(base, "after-restart.txt")
	if _, err := os.Stat(afterRestart); err != nil {
		t.Fatalf("after-restart.txt missing from server-owned workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtB, "after-restart.txt")); err == nil {
		t.Fatal("reattachment followed the obsolete client-selected worktree")
	}
}
