package app

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// bashWriteProvider is a STATELESS scripted LLM (safe for the concurrent child
// runs Fork fans out): on the first turn of a branch it calls Shell to write a
// relative-path marker file; once a tool result is present it ends the turn with a
// summary. It decides from the request's own history, not a shared cursor.
type bashWriteProvider struct {
	command string
	marker  string
}

func (*bashWriteProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *bashWriteProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	hasToolResult := false
	for _, m := range req.Messages {
		if m.ToolResult != nil {
			hasToolResult = true
			break
		}
	}
	var chunks []port.Chunk
	if hasToolResult {
		chunks = []port.Chunk{
			{Kind: port.ChunkText, Text: "wrote " + p.marker},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	} else {
		call := session.NewToolCall("bash1", "Shell", json.RawMessage(`{"command":`+strconvQuote(p.command)+`}`))
		chunks = []port.Chunk{
			{Kind: port.ChunkToolCall, ToolCall: &call},
			{Kind: port.ChunkUsage, Usage: &session.Usage{}},
			{Kind: port.ChunkDone, Stop: session.StopEndTurn},
		}
	}
	return func(yield func(port.Chunk, error) bool) {
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if !yield(c, nil) {
				return
			}
		}
	}, nil
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// recordingForker wraps the real osfs forker and records the child workspace roots
// it hands out, so the test can read the marker from the exact fork directory.
type recordingForker struct {
	inner  tool.EnvironmentForker
	mu     sync.Mutex
	childs []string
}

func (rf *recordingForker) Fork(ctx context.Context, base tool.Environment, label string) (tool.Environment, func() error, string, error) {
	child, cleanup, advisory, err := rf.inner.Fork(ctx, base, label)
	if err != nil {
		return tool.Environment{}, nil, "", err
	}
	rf.mu.Lock()
	rf.childs = append(rf.childs, child.Workspace().Root())
	rf.mu.Unlock()
	return child, cleanup, advisory, nil
}

func (rf *recordingForker) roots() []string {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	out := make([]string, len(rf.childs))
	copy(out, rf.childs)
	return out
}

// TestForkShellWritesIntoForkNotBase is the end-to-end isolation proof: a Fork
// MUTATING branch whose scripted turn runs Shell (`echo hi > marker.txt`) lands the
// marker in the branch's ISOLATED fork — NOT in the shared parent base. This is the
// behavior the workspace-aware-Shell fix exists for; before the fix the Shell runner
// was rooted at the parent base and the marker would have appeared THERE (the
// assertion at the end would fail if Shell were pointed back at the base).
func TestForkShellWritesIntoForkNotBase(t *testing.T) {
	base := t.TempDir()
	cfg := Config{Workspace: base, Model: "mock", Shell: "/bin/sh"}

	// The PRODUCTION Parallel-branch runner (issue #40): hardened (env-scrubbed)
	// but trust-ungated — buildForceCopyRunner, the same construction Mutating
	// members use. This end-to-end test doubles as the proof that the scrub (which
	// pins only the fixed git env keys) leaves ordinary branch shell work intact.
	runner := buildForceCopyRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil command runner (Shell set)")
	}

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}

	rf := &recordingForker{inner: forker.New(func(root string) (tool.Workspace, error) {
		return osfs.NewWorkspace(root)
	}, forker.WithRunner(func(root string) tool.CommandRunner {
		childCfg := cfg
		childCfg.Workspace = root
		return buildForceCopyRunner(childCfg)
	}))}

	provider := &bashWriteProvider{command: "echo hi > marker.txt", marker: "marker.txt"}
	childEngine := buildParallelChildEngine(cfg, nil, provider, "", cfg.Model, runner)

	// join=first PRESERVES the winning branch's fork (cleanup not called), so the
	// marker survives for the assertion below.
	fork := agent.NewParallelTool(childEngine, rf)

	call := session.NewToolCall("c1", "Parallel",
		json.RawMessage(`{"tasks":["write the marker"],"join":"first"}`))
	baseEnv := testEnvironment(baseWS, buildCommandRunner(cfg))
	res, err := fork.Execute(context.Background(), call, baseEnv)
	if err != nil {
		t.Fatalf("Fork.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Fork result is an error: %s", res.Content)
	}

	roots := rf.roots()
	if len(roots) != 1 {
		t.Fatalf("expected exactly 1 forked child workspace, got %d: %v", len(roots), roots)
	}
	forkRoot := roots[0]

	// The marker MUST be in the fork.
	forkMarker := filepath.Join(forkRoot, "marker.txt")
	if _, err := os.Stat(forkMarker); err != nil {
		t.Errorf("marker.txt NOT found in the branch fork %q: %v (Shell did not run in the fork)", forkRoot, err)
	}

	// The marker MUST be ABSENT from the shared parent base — this is the isolation
	// guarantee. (If Shell were rooted at the base, this is exactly where it would
	// have landed, and this assertion would FAIL.)
	baseMarker := filepath.Join(base, "marker.txt")
	if _, err := os.Stat(baseMarker); !os.IsNotExist(err) {
		t.Errorf("marker.txt LEAKED into the shared parent base %q (Shell escaped the fork)", base)
	}
}

// TestForkGitCommitDoesNotTouchBaseRepo is the end-to-end git-isolation proof: a
// Fork MUTATING branch whose scripted turn runs `git commit` via Shell lands the
// commit in the branch's ISOLATED fork's OWN .git — NOT the shared base repo. This
// is the gap WithForceCopy closes: with the old worktree path the commit object/ref
// would have written into the base's shared .git. The composition root wires the
// Fork forker WithForceCopy, so this end-to-end build uses the same one. Skipped
// when git is unavailable.
func TestForkGitCommitDoesNotTouchBaseRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	initBaseGitRepo(t, base)
	cfg := Config{Workspace: base, Model: "mock", Shell: "/bin/sh"}

	// The PRODUCTION Parallel-branch runner (issue #40): hardened, trust-ungated.
	// Running a real `git add/commit/update-ref` through the SCRUBBED env below
	// proves the trusted-path behavior is unchanged — gitenv.Scrub pins only the
	// fixed keys (hooks/pager/fsmonitor/diff.external); repo-local user.name/email
	// and ordinary git verbs still work.
	runner := buildForceCopyRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil command runner (Shell set)")
	}

	baseHeadBefore := gitOut(t, base, "rev-parse", "HEAD")
	baseRefsBefore := gitOut(t, base, "show-ref")

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}

	// Mirror the composition root: mutating Fork branches get FULLY isolated forks.
	rf := &recordingForker{inner: forker.New(func(root string) (tool.Workspace, error) {
		return osfs.NewWorkspace(root)
	}, forker.WithForceCopy(), forker.WithRunner(func(root string) tool.CommandRunner {
		childCfg := cfg
		childCfg.Workspace = root
		return buildForceCopyRunner(childCfg)
	}))}

	// The branch writes a file then commits it — all inside its fork.
	provider := &bashWriteProvider{
		command: "echo branchwork > branch.txt && git add -A && git commit -m 'branch commit' && git update-ref refs/heads/sneaky HEAD",
		marker:  "branch.txt",
	}
	childEngine := buildParallelChildEngine(cfg, nil, provider, "", cfg.Model, runner)

	// join=first PRESERVES the winner's fork so we can inspect it.
	fork := agent.NewParallelTool(childEngine, rf)

	call := session.NewToolCall("c1", "Parallel",
		json.RawMessage(`{"tasks":["commit the work"],"join":"first"}`))
	baseEnv := testEnvironment(baseWS, buildCommandRunner(cfg))
	res, err := fork.Execute(context.Background(), call, baseEnv)
	if err != nil {
		t.Fatalf("Fork.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Fork result is an error: %s", res.Content)
	}

	roots := rf.roots()
	if len(roots) != 1 {
		t.Fatalf("expected exactly 1 forked child workspace, got %d: %v", len(roots), roots)
	}
	forkRoot := roots[0]

	// The commit landed in the FORK's own repo (its HEAD advanced past the base's).
	if forkHead := gitOut(t, forkRoot, "rev-parse", "HEAD"); forkHead == baseHeadBefore {
		t.Errorf("fork HEAD did not advance; the branch's git commit may not have run (head=%q)", forkHead)
	}

	// The base repo's .git MUST be UNCHANGED: same HEAD, same refs, no sneaky ref,
	// no committed file in the working tree.
	if got := gitOut(t, base, "rev-parse", "HEAD"); got != baseHeadBefore {
		t.Errorf("base HEAD changed: before=%q after=%q (fork commit escaped into base)", baseHeadBefore, got)
	}
	if got := gitOut(t, base, "show-ref"); got != baseRefsBefore {
		t.Errorf("base refs changed:\nbefore=%q\nafter=%q (fork ref write escaped into base)", baseRefsBefore, got)
	}
	if _, err := os.Stat(filepath.Join(base, "branch.txt")); !os.IsNotExist(err) {
		t.Errorf("fork's working-tree file leaked into base (err=%v)", err)
	}
}

func initBaseGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "init")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestParallelBranchRunnerIsHardened pins the issue-#40 registerParallelTool wiring:
// the Parallel branch Shell must run through the HARDENED, env-scrubbed
// buildForceCopyRunner (the same construction Mutating members use) — NEVER the
// unhardened buildCommandRunner. The fork copies a possibly-untrusted base `.git`
// VERBATIM, so the branch's run-time git needs the scrubbed env (fixed keys pinned).
// Observable from the branch itself: gitenv.Scrub injects GIT_CONFIG_NOSYSTEM=1
// into the command environment, so a branch that dumps its env to an absolute
// out-of-fork path must see it. Swapping registerParallelTool back to
// buildCommandRunner makes the dump lack the marker and fails this test
// (mutation-verified).
func TestParallelBranchRunnerIsHardened(t *testing.T) {
	base := t.TempDir()
	cfg := Config{Workspace: base, Model: "mock", Shell: "/bin/sh", EnableParallel: true}

	envFile := filepath.Join(t.TempDir(), "env.txt")
	provider := &bashWriteProvider{command: "env > " + envFile, marker: "env.txt"}

	// The REAL wiring under test: registerParallelTool builds the branch runner
	// itself (this is exactly the line that regressed to the unhardened runner).
	cat := tool.NewCatalog()
	registerParallelTool(context.Background(), cfg, cat,
		regForTest(provider, providerMock, cfg.Model), nil, nil, catalogAssets{},
		catalogSession{provider: provider, providerID: providerMock, model: cfg.Model})
	par, ok := cat.Lookup("Parallel")
	if !ok {
		t.Fatal("registerParallelTool did not register the Parallel tool")
	}

	baseWS, err := osfs.NewWorkspace(base)
	if err != nil {
		t.Fatalf("base workspace: %v", err)
	}
	res, err := par.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["dump the env"],"join":"first"}`)),
		testEnvironment(baseWS, buildCommandRunner(cfg)))
	if err != nil {
		t.Fatalf("Parallel.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Parallel result is an error: %s", res.Content)
	}

	dump, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("the branch never ran its Shell (no env dump): %v", err)
	}
	// Deliberately do NOT print the dump on failure: an unscrubbed env is the
	// OPERATOR'S real environment, secrets included.
	if !strings.Contains(string(dump), "GIT_CONFIG_NOSYSTEM=1") {
		t.Fatal("Parallel branch Shell ran with an UNSCRUBBED env (no GIT_CONFIG_NOSYSTEM=1): " +
			"registerParallelTool must wire buildForceCopyRunner, not buildCommandRunner")
	}
}
