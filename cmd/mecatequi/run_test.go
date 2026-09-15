package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/tools"
	"github.com/stacklok/mecatl/internal/app"
)

// initTestRepo creates a hermetic git repo in dir with one commit. It mirrors the
// recipe in internal/app/agency_test.go so the gitDiffPatch path has a real repo to
// diff against, with a deterministic, leak-free identity.
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "initial commit")
}

// gitInRepo runs one git command in dir with the hermetic, leak-free identity/env (the
// same as initTestRepo), failing the test on a non-zero exit. It is the test-only git
// driver for the round-trip and gitignore assertions.
func gitInRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestRunEndToEndMockProvider is the model-facing e2e: a full app.Build over a
// hermetic git repo with UseMock, then run() against the real Service. It asserts the
// run completes cleanly (end_turn), leaves no diff, carries a usage struct, writes a
// non-empty human log, and that the captured event slice carries an EvResult.
func TestRunEndToEndMockProvider(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initTestRepo(t, repo)

	built, err := buildIsolated(t, ctx, app.Config{
		Workspace:   repo,
		UseMock:     true,
		NoSoul:      true,
		Diagnostics: port.NopDiagnostics{},
	})
	if err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	defer built.Close()

	var human bytes.Buffer
	outcome, err := run(ctx, built.Service, session.Limits{}, "summarise the repo", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	sum := outcome.Summary

	if sum.SchemaVersion != SummarySchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", sum.SchemaVersion, SummarySchemaVersion)
	}
	if sum.StopReason != string(session.StopEndTurn) {
		t.Errorf("StopReason = %q, want end_turn", sum.StopReason)
	}
	if sum.Error != "" {
		t.Errorf("clean run must carry no Error; got %q", sum.Error)
	}
	if outcome.NoApprover {
		t.Error("a clean mock run must not report NoApprover")
	}

	// Compute the diff like main does — the mock makes no tool calls, so the tree is
	// untouched.
	patch, nonEmpty, derr := gitDiffPatch(ctx, repo)
	if derr != nil {
		t.Fatalf("gitDiffPatch: %v", derr)
	}
	if nonEmpty || len(strings.TrimSpace(string(patch))) != 0 {
		t.Errorf("mock run made no edits; want empty diff, got %q", patch)
	}

	if human.Len() == 0 {
		t.Error("human log must be non-empty")
	}

	// The captured events must include a terminal EvResult (the durable-log source).
	if countResults(outcome.Events) != 1 {
		t.Errorf("want exactly one EvResult in the captured stream, got %d", countResults(outcome.Events))
	}

	// And the durable JSONL log must serialise that EvResult on its own line, and EVERY
	// line must decode.
	var jsonl bytes.Buffer
	if werr := writeDurableLog(&jsonl, outcome.Events); werr != nil {
		t.Fatalf("writeDurableLog: %v", werr)
	}
	if n := jsonlResultCount(t, jsonl.Bytes()); n != 1 {
		t.Errorf("durable JSONL log: want exactly 1 EvResult line, got %d\n%s", n, jsonl.String())
	}
}

// TestRunUsageAndFinalTextFaithfullyCopied proves the terminal EvResult's usage and
// final text are copied verbatim into the Summary (the honesty invariant on the
// success path): a scripted text turn carrying explicit usage (incl. reasoning
// tokens, a subset of output) shows up in the Summary.
func TestRunUsageAndFinalTextFaithfullyCopied(t *testing.T) {
	usage := session.Usage{InputTokens: 120, OutputTokens: 30, CacheReadTokens: 90, ReasoningTokens: 20}
	turn := mockllm.ChunksTurn(
		mockllm.TextChunk("the deliverable answer"),
		port.Chunk{Kind: port.ChunkUsage, Usage: &usage},
		port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	)
	svc := scriptedService(t, nil, nil, turn)

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "answer", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	sum := outcome.Summary

	if sum.FinalText != "the deliverable answer" {
		t.Errorf("FinalText = %q, want the terminal assistant text", sum.FinalText)
	}
	if sum.Usage.InputTokens != 120 || sum.Usage.OutputTokens != 30 || sum.Usage.CacheReadTokens != 90 || sum.Usage.ReasoningTokens != 20 {
		t.Errorf("usage not faithfully copied: %+v", sum.Usage)
	}
	if sum.Usage.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150 (input+output, cache and reasoning excluded as subsets)", sum.Usage.TotalTokens)
	}
}

// TestRunFinalTextClamped proves the summary's FinalText is bounded: a terminal text
// longer than finalTextMaxRunes is clamped rune-aware to at most finalTextMaxRunes+1
// runes (the trailing ellipsis), so the scan-index summary stays bounded while the
// full text lives in the durable event log.
func TestRunFinalTextClamped(t *testing.T) {
	long := strings.Repeat("a", finalTextMaxRunes+500)
	turn := mockllm.ChunksTurn(
		mockllm.TextChunk(long),
		port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	)
	svc := scriptedService(t, nil, nil, turn)

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "answer", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := []rune(outcome.Summary.FinalText)
	if len(got) > finalTextMaxRunes+1 {
		t.Errorf("FinalText = %d runes, want <= %d (clamped + ellipsis)", len(got), finalTextMaxRunes+1)
	}
	if !strings.HasSuffix(outcome.Summary.FinalText, "…") {
		t.Errorf("a clamped FinalText must end with the ellipsis marker; got tail %q", outcome.Summary.FinalText[len(outcome.Summary.FinalText)-8:])
	}
}

// TestExitCodeCleanIsZero pins the exit-code contract: every CLEAN terminal maps to 0,
// error/cancelled/none map to 1.
func TestExitCodeCleanIsZero(t *testing.T) {
	clean := []session.StopReason{
		session.StopEndTurn,
		session.StopNoProgress,
		session.StopBudget,
		session.StopMaxTurns,
		session.StopMaxToolCalls,
		session.StopMaxConsecutiveFailures,
		session.StopStructuredOutput,
	}
	for _, s := range clean {
		if got := exitCode(Summary{StopReason: string(s)}); got != 0 {
			t.Errorf("exitCode(%q) = %d, want 0 (clean terminal)", s, got)
		}
	}
	fail := []session.StopReason{session.StopError, session.StopCancelled, session.StopNone}
	for _, s := range fail {
		if got := exitCode(Summary{StopReason: string(s)}); got != 1 {
			t.Errorf("exitCode(%q) = %d, want 1", s, got)
		}
	}
}

type scriptedPlacementProvider struct {
	root       string
	workspaces func(string) tool.Workspace
}

func (p scriptedPlacementProvider) Bind(_ context.Context, _ server.PlacementBindRequest) (server.PlacementBinding, error) {
	workspace := p.workspaces(p.root)
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: p.root, Revision: "test-v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, workspace, memledger.New(), nil)}, nil
}
func (p scriptedPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, p.workspaces(req.Ref.ID), memledger.New(), nil)}, nil
}

// scriptedService builds a real server.Service over a SCRIPTED mockllm provider — the
// adversarial-test seam (app.Build's canned mock cannot be scripted). It mirrors the
// construction in internal/adapter/server/budget_test.go. extraTools are registered
// into the catalog (e.g. a real Write tool); workspaces, when non-nil, overrides the
// default memfs factory (e.g. an osfs factory for a real-FS diff test).
func scriptedService(t *testing.T, extraTools []tool.Tool, workspaces func(string) tool.Workspace, turns ...mockllm.Turn) *server.Service {
	return scriptedServiceAtRoot(t, "/ws", extraTools, workspaces, turns...)
}

func scriptedServiceAtRoot(t *testing.T, root string, extraTools []tool.Tool, workspaces func(string) tool.Workspace, turns ...mockllm.Turn) *server.Service {
	t.Helper()
	llm := mockllm.New(turns...)
	cat := tool.NewCatalog()
	for _, tl := range extraTools {
		cat.MustRegister(tl)
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
		Model:   "test-model",
	})
	if workspaces == nil {
		workspaces = func(root string) tool.Workspace { return memfs.NewWorkspace(root) }
	}
	svc, err := server.NewService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		PlacementProvider:   scriptedPlacementProvider{root: root, workspaces: workspaces},
		PlacementScope:      "test",
		SharedEngineRoot:    root,
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine, Capabilities: llm.Capabilities(), Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestRunNonEmptyDiffWhenAgentWritesFile drives a scripted Write tool call over a REAL
// osfs workspace inside a temp git repo and asserts the run's diff is non-empty and the
// patch body carries the new content. This is the honesty invariant on the
// files-CHANGED path (the empty-diff path is covered by the mock e2e).
func TestRunNonEmptyDiffWhenAgentWritesFile(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	// Read the tracked f.txt first (satisfying Write's read-before-overwrite
	// invariant), then overwrite it — so `git diff HEAD` shows a tracked-file
	// modification and the patch BODY carries the new content.
	readCall := session.NewToolCall("r1", "Read", []byte(`{"path":"f.txt"}`))
	writeCall := session.NewToolCall("w1", "Write", []byte(`{"path":"f.txt","content":"CHANGED BY THE AGENT\n"}`))
	svc := scriptedServiceAtRoot(t, repo,
		[]tool.Tool{tools.ReadTool{}, tools.WriteTool{}},
		func(root string) tool.Workspace {
			ws, err := osfs.NewWorkspace(root)
			if err != nil {
				t.Fatalf("osfs.NewWorkspace(%q): %v", root, err)
			}
			return ws
		},
		mockllm.ToolCallTurn(readCall),
		mockllm.ToolCallTurn(writeCall),
		mockllm.TextTurn("done"),
	)

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "edit f.txt", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Summary.StopReason != string(session.StopEndTurn) {
		t.Fatalf("StopReason = %q, want end_turn (human log below)\n%s", outcome.Summary.StopReason, human.String())
	}

	patch, nonEmpty, derr := gitDiffPatch(context.Background(), repo)
	if derr != nil {
		t.Fatalf("gitDiffPatch: %v", derr)
	}
	if !nonEmpty {
		t.Fatalf("the agent edited a tracked file; NonEmptyDiff must be true\nhuman log:\n%s", human.String())
	}
	if !strings.Contains(string(patch), "CHANGED BY THE AGENT") {
		t.Errorf("patch must carry the new content; got:\n%s", patch)
	}
}

// TestRunAdversarialError drives a scripted provider that fails mid-stream and asserts
// the Summary HONESTLY reports the error terminal (StopError, non-empty Error) and that
// exitCode maps it to 1. It proves the summary is not hardcoded optimism.
func TestRunAdversarialError(t *testing.T) {
	svc := scriptedService(t, nil, nil, mockllm.ErrorTurn(errors.New("upstream 503 exhausted retries")))

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "do the thing", &human)
	if err != nil {
		t.Fatalf("run (a model error is reported in the Summary, NOT as a setup error): %v", err)
	}
	sum := outcome.Summary

	if sum.StopReason != string(session.StopError) {
		t.Errorf("StopReason = %q, want error", sum.StopReason)
	}
	if sum.Error == "" {
		t.Error("an error terminal must carry a non-empty Error in the Summary")
	}
	if got := exitCode(sum); got != 1 {
		t.Errorf("exitCode = %d, want 1 for an error terminal", got)
	}
	// The error terminal is still recorded in the durable log.
	if len(outcome.Events) == 0 {
		t.Error("an errored run must still capture events")
	}
}

// TestRunAdversarialNoProgress drives a scripted provider that emits only empty
// (no-text, no-tool) turns until the loop's no-progress budget exhausts. It asserts the
// Summary HONESTLY reports no_progress (NOT end_turn), with no diff, and exitCode 0
// (a clean terminal). This is the "the summary must not claim success" guard.
func TestRunAdversarialNoProgress(t *testing.T) {
	// Enough empty turns to exhaust the default no-progress nudge budget.
	turns := make([]mockllm.Turn, 0, 8)
	for i := 0; i < 8; i++ {
		turns = append(turns, mockllm.EmptyTurn())
	}
	svc := scriptedService(t, nil, nil, turns...)

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "do nothing useful", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	sum := outcome.Summary

	if sum.StopReason != string(session.StopNoProgress) {
		t.Errorf("StopReason = %q, want no_progress (HONEST, not end_turn)", sum.StopReason)
	}
	if sum.StopReason == string(session.StopEndTurn) {
		t.Error("a no-progress run must NOT be relabelled end_turn (the honesty invariant)")
	}
	if sum.NonEmptyDiff {
		t.Error("a no-progress run made no edits; NonEmptyDiff must be false")
	}
	if got := exitCode(sum); got != 0 {
		t.Errorf("exitCode = %d, want 0 (no_progress is a CLEAN terminal)", got)
	}
}

// TestRunAdversarialCancelled drives a scripted provider that reports a cancelled
// terminal and asserts the Summary HONESTLY reports cancelled with exitCode 1 — the
// real loop drive, not just the unit table.
func TestRunAdversarialCancelled(t *testing.T) {
	svc := scriptedService(t, nil, nil, mockllm.EmptyTurnWithStop(session.StopCancelled))

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "abandon", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Summary.StopReason != string(session.StopCancelled) {
		t.Errorf("StopReason = %q, want cancelled", outcome.Summary.StopReason)
	}
	if got := exitCode(outcome.Summary); got != 1 {
		t.Errorf("exitCode = %d, want 1 for a cancelled terminal", got)
	}
}

// TestRunCancelOnMainAskBoundsAndExits is the headless main-ask trap: a strict-posture
// engine asks on a mutate, but no approver is attached. The run() loop must detect the
// parent-own EvPermissionAsk, cancel, drain-to-close (NOT hang), set NoApprover, and
// surface a non-zero exit. A real Write tool over a memfs workspace exercises the gate;
// the policy is the BUILT-IN floor (which ASKS on a mutate), not allow-all.
func TestRunCancelOnMainAskBoundsAndExits(t *testing.T) {
	writeCall := session.NewToolCall("w1", "Write", []byte(`{"path":"x.txt","content":"hi\n"}`))
	llm := mockllm.New(mockllm.ToolCallTurn(writeCall), mockllm.TextTurn("done"))
	cat := tool.NewCatalog()
	cat.MustRegister(tools.WriteTool{})
	// The built-in default policy (no allow rules): a mutate (Write) floors to ASK.
	engine := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	ws := func(root string) tool.Workspace { return memfs.NewWorkspace(root) }
	svc, err := server.NewService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		PlacementProvider:   scriptedPlacementProvider{root: "/ws", workspaces: ws},
		PlacementScope:      "test",
		SharedEngineRoot:    "/ws",
		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	// A timeout on the test context is the backstop: if the loop hangs (the bug this
	// guards), the test fails by deadline instead of blocking forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	var outcome runOutcome
	var runErr error
	go func() {
		var human bytes.Buffer
		outcome, runErr = run(ctx, svc, session.Limits{}, "write a file", &human)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("run() did not return after a main-engine ask with no approver — it hung (cancel-on-ask regression)")
	}

	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	if !outcome.NoApprover {
		t.Error("a main-engine ask with no approver must set NoApprover")
	}
	if got := exitCode(outcome.Summary); got != 1 {
		t.Errorf("exitCode = %d, want 1 (no-approver cancel)", got)
	}
}

// TestRealMainMissingPromptIsSetupFailure proves the SETUP-failure exit code (2): a
// missing prompt fails flag parsing before any build.
func TestRealMainMissingPromptIsSetupFailure(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := realMain(nil, &out, &errBuf); code != 2 {
		t.Errorf("realMain(no prompt) = %d, want 2 (setup failure)", code)
	}
}

// TestRealMainNonGitWorkspaceIsSetupFailure proves a non-git --workspace fails at setup
// (exit 2) — the validateWorkspaceRepo guard, exercised through the whole main path.
func TestRealMainNonGitWorkspaceIsSetupFailure(t *testing.T) {
	notARepo := t.TempDir() // a bare temp dir, no `git init`
	var out, errBuf bytes.Buffer
	code := realMain([]string{"--prompt", "x", "--mock", "--workspace", notARepo}, &out, &errBuf)
	if code != 2 {
		t.Errorf("realMain(non-git workspace) = %d, want 2\nstderr=%s", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "not a git repository") {
		t.Errorf("stderr should explain the non-git workspace; got %q", errBuf.String())
	}
}

// TestRealMainMockHappyPath drives the WHOLE main path (parse -> app.Build(UseMock) ->
// run -> emit diff/summary/events -> exit) over a hermetic git repo, asserting exit 0
// and that the summary JSON written to --out-summary parses with end_turn and an empty
// diff. It also confirms the durable JSONL log file is created and carries an EvResult.
func TestRealMainMockHappyPath(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)
	outDir := t.TempDir()
	summaryPath := filepath.Join(outDir, "summary.json")
	diffPath := filepath.Join(outDir, "diff.patch")
	eventsPath := filepath.Join(outDir, "events.jsonl")

	var stdout, stderr bytes.Buffer
	code := realMain([]string{
		"--prompt", "summarise the repo",
		"--mock",
		"--workspace", repo,
		"--out-summary", summaryPath,
		"--out-diff", diffPath,
		"--out-events", eventsPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("realMain = %d, want 0\nstderr=%s", code, stderr.String())
	}

	raw, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var sum Summary
	if uerr := json.Unmarshal(raw, &sum); uerr != nil {
		t.Fatalf("summary JSON: %v\n%s", uerr, raw)
	}
	if sum.SchemaVersion != SummarySchemaVersion {
		t.Errorf("summary SchemaVersion = %d, want %d", sum.SchemaVersion, SummarySchemaVersion)
	}
	if sum.StopReason != string(session.StopEndTurn) {
		t.Errorf("summary StopReason = %q, want end_turn", sum.StopReason)
	}
	if sum.NonEmptyDiff {
		t.Error("mock run made no edits; NonEmptyDiff must be false")
	}
	if sum.SessionID == "" {
		t.Error("summary must carry the session id")
	}

	// The final stderr verdict line must reflect the honest outcome.
	if !strings.Contains(stderr.String(), "stop=end_turn") {
		t.Errorf("stderr verdict line missing; got %q", stderr.String())
	}

	evData, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events log: %v", err)
	}
	if jsonlResultCount(t, evData) != 1 {
		t.Errorf("durable events log: want exactly 1 EvResult line\n%s", evData)
	}
}

// TestRealMainCollidingOutputsIsSetupFailure proves the output-collision guard: two
// outputs on stdout fail at setup (exit 2).
func TestRealMainCollidingOutputsIsSetupFailure(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)
	var out, errBuf bytes.Buffer
	code := realMain([]string{
		"--prompt", "x", "--mock", "--workspace", repo,
		"--out-summary", "-", "--out-diff", "-",
	}, &out, &errBuf)
	if code != 2 {
		t.Errorf("realMain(both stdout) = %d, want 2\nstderr=%s", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "both write to stdout") {
		t.Errorf("stderr should name the stdout collision; got %q", errBuf.String())
	}
}

// TestGitDiffPatchUntrackedFile proves a NEW untracked file is included in the patch
// with its FULL CONTENT (not merely folded into the non-empty bool) — the FIX-A
// regression guard: the prior `git diff HEAD` path emitted an empty patch for a new
// file, which a downstream `git apply` would silently lose.
func TestGitDiffPatchUntrackedFile(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("brand new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, nonEmpty, err := gitDiffPatch(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitDiffPatch: %v", err)
	}
	if !nonEmpty {
		t.Error("an untracked file must yield nonEmpty=true")
	}
	if !strings.Contains(string(patch), "new file mode") {
		t.Errorf("patch must carry a new-file hunk for the untracked file; got:\n%s", patch)
	}
	if !strings.Contains(string(patch), "brand new") {
		t.Errorf("patch must carry the untracked file's CONTENT; got:\n%s", patch)
	}
	if !strings.Contains(string(patch), "new.txt") {
		t.Errorf("patch must name the untracked file; got:\n%s", patch)
	}
}

// TestGitDiffPatchIgnoredFileExcluded proves a .gitignore'd untracked file is NOT
// dumped into the patch (the --exclude-standard guarantee).
func TestGitDiffPatchIgnoredFileExcluded(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)
	gitInRepo(t, repo, "config", "core.excludesFile", "/dev/null") // isolation
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("secret.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInRepo(t, repo, "add", ".gitignore")
	gitInRepo(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "ignore")
	if err := os.WriteFile(filepath.Join(repo, "secret.txt"), []byte("DO NOT LEAK\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, _, err := gitDiffPatch(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitDiffPatch: %v", err)
	}
	if strings.Contains(string(patch), "DO NOT LEAK") || strings.Contains(string(patch), "secret.txt") {
		t.Errorf("an ignored file must NOT appear in the patch; got:\n%s", patch)
	}
}

// TestGitDiffPatchRoundTripAppliesAllChanges is the real oracle: it makes all three
// kinds of change in a temp repo — (1) a NEW untracked file, (2) a MODIFIED tracked
// file, (3) a DELETED tracked file — captures the patch via gitDiffPatch, then applies
// it onto a SEPARATE clean checkout of HEAD and asserts every change is reproduced with
// correct content. This is what would have caught FIX A (the empty patch losing new
// files).
func TestGitDiffPatchRoundTripAppliesAllChanges(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo) // commits f.txt = "hello\n"
	// Add a second tracked file we will DELETE.
	if err := os.WriteFile(filepath.Join(repo, "doomed.txt"), []byte("delete me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInRepo(t, repo, "add", "doomed.txt")
	gitInRepo(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "add doomed")

	// (1) NEW untracked file, (2) MODIFY f.txt, (3) DELETE doomed.txt.
	if err := os.WriteFile(filepath.Join(repo, "added.txt"), []byte("ADDED LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("MODIFIED LINE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "doomed.txt")); err != nil {
		t.Fatal(err)
	}

	patch, nonEmpty, err := gitDiffPatch(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitDiffPatch: %v", err)
	}
	if !nonEmpty || len(patch) == 0 {
		t.Fatalf("expected a non-empty patch; nonEmpty=%v len=%d", nonEmpty, len(patch))
	}
	if !strings.Contains(string(patch), "ADDED LINE") {
		t.Errorf("patch missing the new file's content:\n%s", patch)
	}

	// Apply the patch onto a SEPARATE clean clone of HEAD and verify all three changes.
	clean := t.TempDir()
	gitInRepo(t, repo, "clone", "-q", repo, clean)
	patchPath := filepath.Join(t.TempDir(), "run.patch")
	if err := os.WriteFile(patchPath, patch, 0o600); err != nil {
		t.Fatal(err)
	}
	gitInRepo(t, clean, "apply", patchPath)

	if got, _ := os.ReadFile(filepath.Join(clean, "added.txt")); string(got) != "ADDED LINE\n" {
		t.Errorf("added file not reproduced: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(clean, "f.txt")); string(got) != "MODIFIED LINE\n" {
		t.Errorf("modified file not reproduced: %q", got)
	}
	if _, err := os.Stat(filepath.Join(clean, "doomed.txt")); !os.IsNotExist(err) {
		t.Errorf("deleted file should be gone after apply; stat err=%v", err)
	}
}

// TestGitDiffPatchNotAGitRepo proves a non-git workspace is an error from gitDiffPatch.
func TestGitDiffPatchNotAGitRepo(t *testing.T) {
	if _, _, err := gitDiffPatch(context.Background(), t.TempDir()); err == nil {
		t.Fatal("gitDiffPatch(non-repo): want error, got nil")
	}
}

// exit1Err runs a process that exits with status 1, returning the resulting
// *exec.ExitError, so a test can exercise classifyNoIndexExit1 with a genuine exit-1
// error value (rather than constructing one by hand).
func exit1Err(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 1").Run()
	if err == nil {
		t.Fatal("expected a non-nil error from `sh -c 'exit 1'`")
	}
	return err
}

// TestClassifyNoIndexExit1 unit-tests the CWE-754 guard's classification: exit-1 with
// content is the new-file hunk (success); exit-1 with EMPTY stdout (git could not
// access the path) is NOT a hunk and must be propagated; a no-difference (nil err) is
// not classified as a hunk here (the caller handles exit 0 separately).
func TestClassifyNoIndexExit1(t *testing.T) {
	e1 := exit1Err(t)

	if hunk, ok := classifyNoIndexExit1([]byte("diff --git a/x b/x\nnew file\n"), e1); !ok || len(hunk) == 0 {
		t.Errorf("exit-1 + content must be the success hunk; ok=%v len=%d", ok, len(hunk))
	}
	if _, ok := classifyNoIndexExit1(nil, e1); ok {
		t.Error("exit-1 + EMPTY stdout must NOT be classified as a hunk (CWE-754: propagate it)")
	}
	if _, ok := classifyNoIndexExit1([]byte{}, e1); ok {
		t.Error("exit-1 + zero-length stdout must NOT be classified as a hunk")
	}
	// A nil error (exit 0) is not the exit-1 success path either.
	if _, ok := classifyNoIndexExit1([]byte("anything"), nil); ok {
		t.Error("a nil error must not be classified as the exit-1 hunk path")
	}
}

// TestGitDiffPatchUnreadableUntrackedPropagates is the real OS-level trigger for the
// CWE-754 fix: an untracked SYMLINK pointing to a directory makes
// `git diff --no-index -- /dev/null <link>` exit 1 with EMPTY stdout and an
// "error: Could not access" on stderr. The pre-fix code would silently drop it (and
// under-report the diff); gitDiffPatch must now PROPAGATE an error rather than emit a
// short patch. A normal new file in the SAME repo still produces its hunk (the happy
// path is unchanged).
func TestGitDiffPatchUnreadableUntrackedPropagates(t *testing.T) {
	repo := t.TempDir()
	initTestRepo(t, repo)

	// A genuine new file (the happy path that must keep working).
	if err := os.WriteFile(filepath.Join(repo, "real.txt"), []byte("REAL NEW FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An untracked symlink -> directory: git cannot access "<link>/null" and exits 1
	// with empty stdout. ls-files --others lists the symlink, so gitDiffPatch reaches it.
	if err := os.Mkdir(filepath.Join(repo, "target_dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target_dir", filepath.Join(repo, "dirlink")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}

	_, _, err := gitDiffPatch(context.Background(), repo)
	if err == nil {
		t.Fatal("gitDiffPatch must PROPAGATE the unreadable-untracked error, not silently drop the file (CWE-754)")
	}
	if !strings.Contains(err.Error(), "dirlink") {
		t.Errorf("error should name the offending path; got: %v", err)
	}

	// Sanity: with ONLY the genuine new file (no dir-symlink), the happy path produces
	// the hunk and no error — proving the guard didn't break normal new files.
	clean := t.TempDir()
	initTestRepo(t, clean)
	if err := os.WriteFile(filepath.Join(clean, "real.txt"), []byte("REAL NEW FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	patch, nonEmpty, perr := gitDiffPatch(context.Background(), clean)
	if perr != nil {
		t.Fatalf("a normal new file must NOT error: %v", perr)
	}
	if !nonEmpty || !strings.Contains(string(patch), "REAL NEW FILE") {
		t.Errorf("a normal new file must still produce its hunk; nonEmpty=%v patch=%q", nonEmpty, patch)
	}
}

// TestRunUnknownToolTurnCleanTerminal proves an unknown-tool call does not panic and the
// run reaches a clean terminal (the loop opens a card then records an error result and
// continues).
func TestRunUnknownToolTurnCleanTerminal(t *testing.T) {
	svc := scriptedService(t, nil, nil,
		mockllm.ToolCallTurn(session.NewToolCall("u1", "Nonexistent", []byte(`{}`))),
		mockllm.TextTurn("recovered and done"),
	)
	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{}, "call a bad tool", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := exitCode(outcome.Summary); got != 0 {
		t.Errorf("an unknown-tool run that recovers must end clean; exitCode=%d stop=%q", got, outcome.Summary.StopReason)
	}
}

// TestRunMaxTurnsCapFromLimits proves the limits passed to run() (the --max-turns
// path) actually cap the session: a provider that keeps requesting tool calls would
// otherwise run to script exhaustion, but MaxTurns=1 stops it at the turn limit
// (StopMaxTurns) after the first model call. The scriptedService has zero
// DefaultLimits, so the {MaxTurns:1} survives per-field defaulting verbatim.
func TestRunMaxTurnsCapFromLimits(t *testing.T) {
	svc := scriptedService(t, nil, nil,
		mockllm.ToolCallTurn(session.NewToolCall("c1", "Nonexistent", []byte(`{}`))),
		mockllm.ToolCallTurn(session.NewToolCall("c2", "Nonexistent", []byte(`{}`))),
		mockllm.TextTurn("would have finished here"),
	)

	var human bytes.Buffer
	outcome, err := run(context.Background(), svc, session.Limits{MaxTurns: 1}, "loop", &human)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Summary.StopReason != string(session.StopMaxTurns) {
		t.Fatalf("StopReason = %q, want max_turns (the --max-turns cap)\nhuman log:\n%s", outcome.Summary.StopReason, human.String())
	}
	if got := exitCode(outcome.Summary); got != 0 {
		t.Errorf("exitCode = %d, want 0 (max_turns is a CLEAN terminal)", got)
	}
}

// countResults counts EvResult events in a captured stream.
func countResults(events []session.Event) int {
	n := 0
	for _, ev := range events {
		if ev.Type == session.EvResult {
			n++
		}
	}
	return n
}

// jsonlResultCount decodes EVERY JSONL line (asserting each is valid) and returns the
// number whose Type is EvResult.
func jsonlResultCount(t *testing.T, data []byte) int {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(data))
	n := 0
	for sc.Scan() {
		var ev session.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("decode JSONL line: %v\nline=%s", err, sc.Text())
		}
		if ev.Type == session.EvResult {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	return n
}
