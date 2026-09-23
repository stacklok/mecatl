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
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// recordingMerger is a tool.EnvironmentMerger test double recording every Merge call. It is
// used to prove the Parallel single-branch path still consumes the shared
// catalogAssets.autoMerger — the writable Subagent NO LONGER merges (direct-write,
// ADR 0041), so it is the Parallel-only consumer now.
type recordingMerger struct {
	mu    sync.Mutex
	calls []struct{ fork, parent string }
}

func (m *recordingMerger) Merge(_ context.Context, child, parent tool.Environment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, struct{ fork, parent string }{child.Workspace().Root(), parent.Workspace().Root()})
	return nil
}

func (m *recordingMerger) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// TestBuildSubagentToolWritableWritesParentDirectly drives the REAL buildSubagentTool
// with a writable subagent (mode:"read-write") and proves the direct-write wiring
// (ADR 0041): the writable child runs Edit/Write/Shell DIRECTLY against the parent repo
// (no fork, no merge). The probe is a child that writes a real file into the workspace
// it is handed; the test asserts the file lands in the REAL repo and NO sibling fork
// directory was created. A recording merger placed on the assets must receive ZERO
// calls — Subagent does not merge anymore.
func TestBuildSubagentToolWritableWritesParentDirectly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")

	cfg := teamCfg(t)
	cfg.Workspace = repo

	merger := &recordingMerger{}
	assets := catalogAssets{autoMerger: merger}

	// A child that uses its real Write tool (wired by buildWritableSubagentChildEngine)
	// to create a file in the workspace it is handed.
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.NewToolCall("w1", "Write", []byte(`{"path":"beta.txt","content":"written directly\n"}`))),
		mockllm.TextTurn("did the work"),
	)
	task, closeFn := buildSubagentTool(context.Background(),
		cfg, regForTest(childProvider, providerMock, cfg.Model), childProvider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, assets, false)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	parentEnv := osfsEnvironment(t, repo, buildCommandRunner(cfg))
	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"implement the fix","mode":"read-write"}`)),
		parentEnv)
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("writable subagent returned an error: %q", res.Content)
	}
	// The edit landed DIRECTLY in the real repo (proving direct-write).
	if got, rerr := os.ReadFile(filepath.Join(repo, "beta.txt")); rerr != nil {
		t.Fatalf("the writable subagent's edit did not land in the real repo: %v", rerr)
	} else if !strings.Contains(string(got), "written directly") {
		t.Fatalf("beta.txt content unexpected: %q", got)
	}
	// No merge happened — Subagent writes the parent tree directly (ADR 0041).
	if merger.count() != 0 {
		t.Fatalf("a direct-write subagent must NOT merge, but merger was called %d times", merger.count())
	}
	// No sibling fork directory was created next to the repo.
	assertNoSiblingForkDir(t, repo)
	// The result honestly notes the child had direct write access (capability, not a
	// claim of mutation — "had direct write access to your workspace").
	if !strings.Contains(res.Content, "had direct write access to your workspace") {
		t.Fatalf("result must note the child had direct write access, got:\n%s", res.Content)
	}
	// It must NOT claim an edit occurred (the old false phrasing).
	if strings.Contains(res.Content, "edited your workspace directly") {
		t.Fatalf("result must NOT claim the child 'edited your workspace directly' (capability, not mutation), got:\n%s", res.Content)
	}
}

// TestNoFSSubagentToolRejectsWritable proves the no-FS path wires NO writable child
// engine, so a mode:"read-write" call there is a model-addressable "not supported"
// error (the no-FS gate falls out of the unwired writable engine).
func TestNoFSSubagentToolRejectsWritable(t *testing.T) {
	cfg := teamCfg(t)
	childProvider := mockllm.New(mockllm.TextTurn("x"))
	task, _ := buildSubagentTool(context.Background(),
		cfg, regForTest(childProvider, providerMock, cfg.Model), childProvider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, catalogAssets{}, true /* noFS */)

	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"go","mode":"read-write"}`)),
		tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "test"}, nofs.New(), testReadLedger(), nil))
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("no-FS read-write must be rejected, got success: %q", res.Content)
	}
}

// TestSharedMergerReachesParallel proves the Parallel single-branch auto-merge still
// consumes the shared catalogAssets.autoMerger (the writable Subagent no longer does —
// direct-write, ADR 0041). One recording merger on the assets receives the Parallel
// branch's merge call.
func TestSharedMergerReachesParallel(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")

	cfg := teamCfg(t)
	cfg.Workspace = repo
	cfg.EnableParallel = true

	merger := &recordingMerger{}
	assets := catalogAssets{autoMerger: merger, forkReaper: agent.NewLRUForkReaper(forkPreservedCap(cfg))}
	parentEnv := osfsEnvironment(t, repo, nil)

	parProvider := mockllm.New(mockllm.TextTurn("branch did work"))
	cat := tool.NewCatalog()
	registerParallelTool(context.Background(), cfg,
		cat, regForTest(parProvider, providerMock, cfg.Model), nil, hookexec.New(nil),
		assets, catalogSession{provider: parProvider, providerID: providerMock, model: cfg.Model})
	par, perr := cat.Lookup("Parallel")
	if !perr {
		t.Fatal("Parallel tool not registered")
	}
	if r, err := par.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", []byte(`{"tasks":["implement X"],"join":"first"}`)),
		parentEnv); err != nil || r.IsError {
		t.Fatalf("parallel single-branch failed: err=%v res=%q", err, r.Content)
	}
	if got := merger.count(); got != 1 {
		t.Fatalf("Parallel single-branch must merge via the shared merger, got %d calls", got)
	}
}

// assertNoSiblingForkDir asserts no directory whose name suggests a fork/worktree
// checkout was created alongside the given repo root. Direct-write creates none.
func assertNoSiblingForkDir(t *testing.T, repo string) {
	t.Helper()
	parent := filepath.Dir(repo)
	base := filepath.Base(repo)
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent dir: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == base {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "fork") || strings.Contains(name, "worktree") || strings.HasPrefix(e.Name(), base+"-") {
			t.Fatalf("a direct-write subagent must NOT create a fork/worktree dir, but found a sibling %q in %q", e.Name(), parent)
		}
	}
}

// compile-time: recordingMerger is an EnvironmentMerger.
var _ tool.EnvironmentMerger = (*recordingMerger)(nil)
