package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/tools"
)

// These tests cover Phase 2 — the Subagent tool (read-only explorer) gets full Bash
// inside an isolated git worktree, mirroring the Phase 1 team-member treatment.

// TestBuildChildEngineWithRunnerHasBash proves the default Subagent explorer's catalog
// gains Bash when a runner is configured (the worktree-isolation path), while still
// excluding Edit (a read-only explorer cannot edit the project).
func TestBuildChildEngineWithRunnerHasBash(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner with Shell set")
	}
	eng := buildChildEngine(cfg, nil, bashThenEdit(), "", cfg.Model, runner)

	events := drainEngine(t, eng)
	if unknownToolResult(events, "b1") {
		t.Error("Subagent child did NOT have Bash; the worktree-isolated explorer must get a shell")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("Subagent child did not dispatch Bash; it must be present in the catalog")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("Subagent child got Edit; a read-only explorer must NOT be able to edit the project")
	}
}

// TestBuildChildEngineNoRunnerHasNoBash proves a shell-less deployment (nil runner)
// keeps the original Bash-less read-only explorer, so the no-forker / no-shell path is
// unchanged.
func TestBuildChildEngineNoRunnerHasNoBash(t *testing.T) {
	cfg := teamCfg(t)
	eng := buildChildEngine(cfg, nil, bashThenEdit(), "", cfg.Model, nil)

	events := drainEngine(t, eng)
	if !unknownToolResult(events, "b1") {
		t.Error("Subagent child with a nil runner dispatched Bash; without a runner there must be no shell")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("Subagent child got Edit; a read-only explorer must never have Edit")
	}
}

// TestBuildSubagentToolWiresForkerWhenShell proves buildSubagentTool wires a child forker iff
// Bash is configured: with a shell the Subagent tool isolates each child in a real git
// worktree (the child's working dir is NOT the parent repo root); with no shell no
// forker is wired (the child shares the parent base). The proof uses a real git repo
// and a real `pwd` so it cannot be faked by the scripted summary.
func TestBuildSubagentToolWiresForkerWhenShell(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")

	cfg := teamCfg(t)
	cfg.Workspace = repo

	// Child runs `pwd`; a recording logger captures the REAL Bash output so the
	// assertion is on what git/the shell actually printed, not the scripted summary.
	rec := &recordingToolLogger{}
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "p1", Name: "Bash", Args: gitArgs("pwd")}),
		mockllm.TextTurn("done"),
	)
	taskWS := t.TempDir() // known worktree base for the cleanup assertion
	task := newSubagentToolForTest(t, cfg, childProvider, rec, taskWS)

	parentEnv := osfsEnvironment(t, repo, nil)
	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", json.RawMessage(`{"prompt":"run pwd"}`)),
		parentEnv)
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Subagent result is an error: %q", res.Content)
	}

	pwd := strings.TrimSpace(rec.contentForCall("p1"))
	if pwd == "" {
		t.Fatal("child Bash produced no pwd output")
	}
	// The child ran in an ISOLATED worktree under taskWS, not the parent repo root.
	if pwd == repo {
		t.Errorf("child pwd = %q (the parent repo root); it must run in an isolated worktree", pwd)
	}
	// Canonicalize taskWS the same way git/the shell resolves the worktree path
	// (EvalSymlinks), so the prefix check holds on a symlinked temp root
	// (macOS /var -> /private/var).
	wantBase, err := osfs.ResolveRoot(taskWS)
	if err != nil {
		t.Fatalf("resolve taskWS %q: %v", taskWS, err)
	}
	if !strings.HasPrefix(pwd, wantBase) {
		t.Errorf("child pwd = %q, want a worktree under the configured base %q", pwd, wantBase)
	}
}

// TestBuildAgentSubagentEnginesWithRunnerKeepsBashDropsEdit proves a per-def Subagent engine
// that scopes Bash keeps it (registered with the hardened runner) when a runner is
// wired, while Edit/Write stay dropped (read-only explorer). Without a runner, Bash is
// dropped too.
func TestBuildAgentSubagentEnginesWithRunnerKeepsBashDropsEdit(t *testing.T) {
	cfg := teamCfg(t)
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner")
	}
	reg := regOf(agents.AgentDef{
		Name: "inspector", Description: "i", Tools: []string{"Read", "Edit", "Bash"},
	})

	// With a runner: Bash kept, Edit dropped.
	engines, _, _ := agentSubagentEnginesForTest(context.Background(), cfg, bashThenEdit(), reg, nil, hookexec.New(nil), runner, nil)
	eng := engines["inspector"]
	if eng == nil {
		t.Fatal("inspector engine not built")
	}
	events := drainEngine(t, eng)
	if unknownToolResult(events, "b1") {
		t.Error("per-def Subagent engine scoping Bash did NOT get Bash; it must keep it for worktree-isolated inspection")
	}
	if !sawDispatchedTool(events, "b1") {
		t.Error("per-def Subagent engine did not dispatch Bash; a def listing Bash must yield it")
	}
	if !unknownToolResult(events, "e1") {
		t.Error("per-def Subagent engine got Edit; Edit/Write must be dropped for a read-only explorer")
	}

	// Without a runner: Bash dropped too (no shell, no isolation).
	enginesNoShell, _, _ := agentSubagentEnginesForTest(context.Background(), cfg, bashThenEdit(), reg, nil, hookexec.New(nil), nil, nil)
	engNoShell := enginesNoShell["inspector"]
	if engNoShell == nil {
		t.Fatal("inspector engine (no shell) not built")
	}
	eventsNoShell := drainEngine(t, engNoShell)
	if !unknownToolResult(eventsNoShell, "b1") {
		t.Error("per-def Subagent engine kept Bash with a nil runner; without isolation there must be no shell")
	}
}

// TestSubagentRunsGitInWorktreeEndToEnd is the key Phase 2 proof: a Subagent tool, driven
// through a real parent engine whose catalog has the Subagent tool wired with a real
// worktree forker + sandboxed runner, runs git (log/show) over a cheap worktree that
// SHARES the base repo's .git — so it sees the full history — confined to a throwaway
// checkout, and the worktree is cleaned up afterwards (no leak). The child never edits
// anything; it only inspects. The REAL git output is captured via a recording logger
// on the child engine, so the history assertion proves git actually ran (the scripted
// child summary cannot fake it).
func TestSubagentRunsGitInWorktreeEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// A real git repo with two commits whose subjects we assert the child can read.
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")
	writeRepoFile(t, repo, "beta.txt", "beta\n")
	gitCommitTest(t, repo, "add beta")

	cfg := teamCfg(t)
	cfg.Workspace = repo

	// The child explorer: git log --oneline, then git show --stat HEAD, then a probe of
	// the workspace .git (worktree pointer is a FILE; a force-copy would be a DIR).
	rec := &recordingToolLogger{}
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "g1", Name: "Bash", Args: gitArgs("git log --oneline")}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "g2", Name: "Bash", Args: gitArgs("git show --stat HEAD")}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "g3", Name: "Bash", Args: gitArgs("if [ -f .git ]; then echo DOTGIT_IS_FILE; elif [ -d .git ]; then echo DOTGIT_IS_DIR; else echo DOTGIT_MISSING; fi")}),
		mockllm.TextTurn("inspection complete"),
	)

	worktreeBase := t.TempDir() // scope worktrees here so we can assert cleanup
	task := newSubagentToolForTest(t, cfg, childProvider, rec, worktreeBase)

	parentEnv := osfsEnvironment(t, repo, nil)

	// Drive a real parent engine that calls Subagent once.
	parentProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "t1", Name: "Subagent", Args: json.RawMessage(`{"prompt":"run git log and git show and report"}`)}),
		mockllm.TextTurn("parent received the child report"),
	)
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	parentEng := newChildEngine(cfg, "", parentProvider, parentCat, cfg.Model, fixedDefaultWindow, promptConfig(cfg, cfg.gitStatus))

	sess := session.New("parent", session.ModeDefault, repo, session.Limits{MaxTurns: 5}, time.Now())
	run := parentEng.Run(context.Background(), sess, parentEnv, agent.RunRequest{Text: "go"})

	var sawSubagentResult bool
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "t1" {
			if ev.ToolResult.IsError {
				t.Fatalf("Subagent tool result is an error: %q", ev.ToolResult.Content)
			}
			sawSubagentResult = true
		}
	}
	if !sawSubagentResult {
		t.Fatal("parent never observed the Subagent tool result")
	}

	// The REAL git output (captured from the child's Bash tool results) must reflect the
	// repo history — proving a Subagent tool ran git in its worktree.
	gitLog := rec.contentForCall("g1")
	gitShow := rec.contentForCall("g2")
	dotgit := rec.contentForCall("g3")
	combined := gitLog + "\n" + gitShow
	for _, want := range []string{"add alpha", "add beta"} {
		if !strings.Contains(combined, want) {
			t.Errorf("child git output missing %q; git log:\n%s\ngit show:\n%s", want, gitLog, gitShow)
		}
	}
	if !strings.Contains(gitShow, "beta.txt") {
		t.Errorf("git show --stat HEAD output missing beta.txt; got:\n%s", gitShow)
	}
	// The child's workspace must be a git WORKTREE (.git is a pointer FILE), not a
	// force-copy (.git would be a DIRECTORY).
	if !strings.Contains(dotgit, "DOTGIT_IS_FILE") {
		t.Errorf("child workspace is not a git worktree (.git is not a pointer file); got: %q", dotgit)
	}
	if strings.Contains(dotgit, "DOTGIT_IS_DIR") {
		t.Errorf("child workspace has a .git DIRECTORY (force-copy), expected a worktree pointer; got: %q", dotgit)
	}

	// The worktree must be cleaned up: no leftover child dir under worktreeBase.
	entries, err := os.ReadDir(worktreeBase)
	if err != nil {
		t.Fatalf("read worktree base: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("worktree base not cleaned up; leftover entries: %v", entries)
	}
}

// TestBuildSubagentToolRealWiringForksChildShellWhenShell drives the REAL buildSubagentTool
// (the live Phase 2 composition seam) — NOT the hand-wired newSubagentToolForTest helper —
// to prove the Bash⟺forker coupling at the composition layer. With a shell-configured
// cfg over a real git repo, the resulting Subagent tool, when invoked, must run the child's
// Bash in an ISOLATED git WORKTREE: the child's pwd is NOT the parent repo root and its
// `.git` is a worktree POINTER FILE (a force-copy would be a directory). The child has
// no recording logger we can inject (buildSubagentTool builds an opaque child engine), so
// the child's Bash writes its pwd + `.git` status into an EXTERNAL probe file we read
// back — the same proof the E2E uses, routed through the real builder. A regression that
// wired the forker unconditionally OR never (a Bash child in the SHARED base) would
// surface here: the probe would show the parent repo root / a `.git` directory.
func TestBuildSubagentToolRealWiringForksChildShellWhenShell(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "alpha.txt", "alpha\n")
	gitCommitTest(t, repo, "add alpha")

	// The child's Bash writes its pwd and `.git` kind to this EXTERNAL probe file,
	// outside any workspace, so we can read what the shell actually saw. Separate
	// statements (pwd>file; then echo>>file) rather than a `{ ...; }` brace group —
	// a leading-brace command does not survive the bash gate's canonicalization.
	probeDir := t.TempDir()
	probeFile := filepath.Join(probeDir, "probe.txt")
	probeCmd := "pwd > " + probeFile +
		"; if [ -f .git ]; then echo DOTGIT_IS_FILE >> " + probeFile +
		"; elif [ -d .git ]; then echo DOTGIT_IS_DIR >> " + probeFile +
		"; else echo DOTGIT_MISSING >> " + probeFile + "; fi"

	cfg := teamCfg(t)
	cfg.Workspace = repo

	// The child provider drives the Subagent child engine buildSubagentTool builds: one Bash
	// call (the probe) then a summary. The parent provider is SEPARATE so the two
	// engines never share a mockllm cursor (the E2E does the same).
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "p1", Name: "Bash", Args: gitArgs(probeCmd)}),
		mockllm.TextTurn("probed"),
	)

	task, closeFn := taskToolForTest(context.Background(), cfg, childProvider, hookexec.New(nil), regOf(), nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	parentProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "t1", Name: "Subagent", Args: json.RawMessage(`{"prompt":"probe the workspace"}`)}),
		mockllm.TextTurn("parent received the child report"),
	)
	parentEnv := osfsEnvironment(t, repo, nil)
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	parentEng := newChildEngine(cfg, "", parentProvider, parentCat, cfg.Model, fixedDefaultWindow, promptConfig(cfg, cfg.gitStatus))

	sess := session.New("parent", session.ModeDefault, repo, session.Limits{MaxTurns: 5}, time.Now())
	run := parentEng.Run(context.Background(), sess, parentEnv, agent.RunRequest{Text: "go"})
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "t1" && ev.ToolResult.IsError {
			t.Fatalf("Subagent tool result is an error: %q", ev.ToolResult.Content)
		}
	}

	out, err := os.ReadFile(probeFile)
	if err != nil {
		t.Fatalf("child Bash never wrote the probe file (the forker was not wired, so the child had no shell): %v", err)
	}
	probe := string(out)
	lines := strings.SplitN(strings.TrimSpace(probe), "\n", 2)
	pwd := strings.TrimSpace(lines[0])
	if pwd == "" {
		t.Fatalf("probe produced no pwd; got:\n%s", probe)
	}
	// The child ran in an ISOLATED worktree, not the parent repo root.
	if pwd == repo {
		t.Errorf("child pwd = %q (the parent repo root); the forker must isolate the child's shell in a worktree", pwd)
	}
	// `.git` must be a worktree POINTER FILE (default forker mode), not a force-copy DIR.
	if !strings.Contains(probe, "DOTGIT_IS_FILE") {
		t.Errorf("child workspace is not a git worktree (.git is not a pointer file); probe:\n%s", probe)
	}
	if strings.Contains(probe, "DOTGIT_IS_DIR") {
		t.Errorf("child workspace has a .git DIRECTORY (force-copy), expected a worktree pointer; probe:\n%s", probe)
	}
}

// TestBuildSubagentToolRealWiringNoShellNoForker drives the REAL buildSubagentTool with a
// nil-runner cfg (NoBash) and proves the other side of the coupling: the resulting Subagent
// child has NO Bash and NO forker, so it cannot run a shell at all. The child's Bash
// call returns an unknown-tool result (the tool is absent from the catalog) and the
// external probe file is NEVER written (no worktree, no shell). This locks the
// "no shell ⇒ no forker" half of the composition coupling.
func TestBuildSubagentToolRealWiringNoShellNoForker(t *testing.T) {
	repo := t.TempDir()

	probeDir := t.TempDir()
	probeFile := filepath.Join(probeDir, "probe.txt")
	probeCmd := "pwd > " + probeFile

	cfg := teamCfg(t)
	cfg.Workspace = repo
	cfg.NoBash = true // nil runner ⇒ no Bash, no forker

	if buildSandboxedCommandRunner(cfg) != nil {
		t.Fatal("precondition: expected a nil sandboxed runner with NoBash set")
	}

	childProvider := mockllm.New(
		// Child turn: attempt Bash (must be unknown — no Bash in the catalog), then summarize.
		mockllm.ToolCallTurn(session.ToolCall{ID: "p1", Name: "Bash", Args: gitArgs(probeCmd)}),
		mockllm.TextTurn("could not run a shell"),
	)

	task, closeFn := taskToolForTest(context.Background(), cfg, childProvider, hookexec.New(nil), regOf(), nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	parentProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "t1", Name: "Subagent", Args: json.RawMessage(`{"prompt":"try to run a shell"}`)}),
		mockllm.TextTurn("parent done"),
	)
	parentEnv := osfsEnvironment(t, repo, nil)
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	parentEng := newChildEngine(cfg, "", parentProvider, parentCat, cfg.Model, fixedDefaultWindow, promptConfig(cfg, cfg.gitStatus))

	sess := session.New("parent", session.ModeDefault, repo, session.Limits{MaxTurns: 5}, time.Now())
	run := parentEng.Run(context.Background(), sess, parentEnv, agent.RunRequest{Text: "go"})
	for ev := range run.Events() {
		_ = ev // drain to completion; the child's lack of Bash is asserted via the probe
	}

	if _, err := os.Stat(probeFile); err == nil {
		t.Fatalf("probe file was written; the child must have NO shell when NoBash is set (no Bash, no forker)")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stating probe file: %v", err)
	}
}

// TestSubagentSeesDirtyWorkspaceEndToEnd is the model-facing proof for the dirty-aware
// overlay (ADR 0033): a read-only Subagent dispatched over a DIRTY parent repo — one
// uncommitted tracked modification AND one new untracked file — runs `git status` and
// `git diff` in its worktree and the captured REAL git output reflects the operator's
// in-progress work. Without WithDirtyOverlay the worktree is a clean HEAD checkout and
// both commands would report nothing; with it, the child sees what the operator sees.
func TestSubagentSeesDirtyWorkspaceEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repo := t.TempDir()
	initGitRepoTest(t, repo)
	writeRepoFile(t, repo, "tracked.txt", "committed v1\n")
	gitCommitTest(t, repo, "add tracked")
	// Make the workspace DIRTY: modify the committed file and add an untracked one.
	writeRepoFile(t, repo, "tracked.txt", "operator WIP edit\n")
	writeRepoFile(t, repo, "scratch.txt", "operator scratch notes\n")

	cfg := teamCfg(t)
	cfg.Workspace = repo

	rec := &recordingToolLogger{}
	childProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "s1", Name: "Bash", Args: gitArgs("git status --porcelain")}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "d1", Name: "Bash", Args: gitArgs("git --no-pager diff --no-ext-diff")}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "c1", Name: "Bash", Args: gitArgs("cat scratch.txt")}),
		mockllm.TextTurn("the workspace has uncommitted changes"),
	)

	worktreeBase := t.TempDir()
	task := newSubagentToolForTestDirty(t, cfg, childProvider, rec, worktreeBase)

	parentEnv := osfsEnvironment(t, repo, nil)
	parentProvider := mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{ID: "t1", Name: "Subagent", Args: json.RawMessage(`{"prompt":"report the uncommitted changes"}`)}),
		mockllm.TextTurn("parent received the dirty-state report"),
	)
	parentCat := tool.NewCatalog()
	parentCat.MustRegister(task)
	parentEng := newChildEngine(cfg, "", parentProvider, parentCat, cfg.Model, fixedDefaultWindow, promptConfig(cfg, cfg.gitStatus))

	sess := session.New("parent", session.ModeDefault, repo, session.Limits{MaxTurns: 6}, time.Now())
	run := parentEng.Run(context.Background(), sess, parentEnv, agent.RunRequest{Text: "go"})
	for ev := range run.Events() {
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "t1" && ev.ToolResult.IsError {
			t.Fatalf("Subagent tool result is an error: %q", ev.ToolResult.Content)
		}
	}

	status := rec.contentForCall("s1")
	diff := rec.contentForCall("d1")
	scratch := rec.contentForCall("c1")

	// git status must list BOTH the modified tracked file and the new untracked file.
	if !strings.Contains(status, "tracked.txt") {
		t.Errorf("git status missing the modified tracked file; got:\n%s", status)
	}
	if !strings.Contains(status, "scratch.txt") {
		t.Errorf("git status missing the untracked file; got:\n%s", status)
	}
	// git diff must show the operator's uncommitted edit.
	if !strings.Contains(diff, "operator WIP edit") {
		t.Errorf("git diff missing the uncommitted edit; got:\n%s", diff)
	}
	// The untracked file's content is readable in the worktree.
	if !strings.Contains(scratch, "operator scratch notes") {
		t.Errorf("untracked scratch.txt not visible to child; got:\n%s", scratch)
	}
}

// newSubagentToolForTest builds a Subagent tool wired exactly like buildSubagentTool's shell path
// — a sandboxed command runner + a worktree forker (rooted under worktreeBase for the
// cleanup assertion) — but with a child engine carrying the given recording logger so a
// test can read the child's REAL Bash output. It mirrors the composition wiring without
// going through buildSubagentTool (which builds an opaque child engine).
func newSubagentToolForTest(t *testing.T, cfg Config, childProvider *mockllm.Provider, logger *recordingToolLogger, worktreeBase string) tool.Tool {
	t.Helper()
	return newSubagentToolForTestOpts(t, cfg, childProvider, logger, worktreeBase)
}

// newSubagentToolForTestDirty is newSubagentToolForTest with the forker carrying
// WithDirtyOverlay (the composition wiring for read-only explorers), so the child's
// worktree mirrors the parent's uncommitted state.
func newSubagentToolForTestDirty(t *testing.T, cfg Config, childProvider *mockllm.Provider, logger *recordingToolLogger, worktreeBase string) tool.Tool {
	t.Helper()
	return newSubagentToolForTestOpts(t, cfg, childProvider, logger, worktreeBase, forker.WithDirtyOverlay())
}

func newSubagentToolForTestOpts(t *testing.T, cfg Config, childProvider *mockllm.Provider, logger *recordingToolLogger, worktreeBase string, forkOpts ...forker.Option) tool.Tool {
	t.Helper()
	runner := buildSandboxedCommandRunner(cfg)
	if runner == nil {
		t.Fatal("precondition: expected a non-nil sandboxed runner")
	}
	childCat := tool.NewCatalog()
	childCat.MustRegister(tools.ReadTool{})
	childCat.MustRegister(tools.GrepTool{})
	childCat.MustRegister(tools.GlobTool{})
	// agent.NewBashTool, mirroring the production child construction
	// (readOnlyExplorerCatalog), so the test child exercises the same Bash the
	// composition root hands real children.
	childCat.MustRegister(agent.NewBashTool())
	childEng := agent.NewEngine(agent.Deps{
		LLM:              childProvider,
		Catalog:          childCat,
		Policy:           permpolicy.NewPolicy([]governance.Rule{{Effect: governance.Allow}}, nil),
		ToolCallRecorder: logger,
		PromptConfig:     promptConfig(cfg, cfg.gitStatus),
		Model:            cfg.Model,
	})
	opts := append([]forker.Option{forker.WithTempBase(worktreeBase)}, forkOpts...)
	// WithRunner (issue #462): the forker mints a BOUND runner for each child
	// worktree so the forked subagent's Bash observes its OWN namespace. The
	// builder applies the SAME trust-gated hardening buildSandboxedCommandRunner
	// does (runner != nil above already proves the gate passed at build time).
	roFk := forker.New(func(root string) (tool.Workspace, error) { return osfs.NewWorkspace(root) },
		append(opts, forker.WithRunner(func(childRoot string) tool.CommandRunner {
			if cfg.NoBash || cfg.Shell == "" || !cfg.TrustProject {
				return nil
			}
			return newHardenedRunnerForRoot(cfg, childRoot)
		}))...)
	return agent.NewSubagentTool(childEng, agent.WithChildForker(roFk))
}

// osfsWSForTest builds an osfs workspace rooted at dir, failing the test on error.
func osfsWSForTest(t *testing.T, dir string) tool.Workspace {
	t.Helper()
	ws, err := osfs.NewWorkspace(dir)
	if err != nil {
		t.Fatalf("osfs workspace %q: %v", dir, err)
	}
	return ws
}

// recordingToolLogger is a port.ToolCallRecorder that records each tool call's result content by
// call ID, so a test can read the REAL output a child's Bash produced. It is
// concurrency-safe (the loop logs from its own goroutine).
type recordingToolLogger struct {
	mu       sync.Mutex
	byCallID map[session.ToolCallID]string
}

func (l *recordingToolLogger) ToolCall(_ session.SessionID, call session.ToolCall, result session.ToolResult, _, _ time.Duration) {
	l.mu.Lock()
	if l.byCallID == nil {
		l.byCallID = map[session.ToolCallID]string{}
	}
	l.byCallID[call.ID] = result.Content
	l.mu.Unlock()
}

func (l *recordingToolLogger) contentForCall(id session.ToolCallID) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byCallID[id]
}
