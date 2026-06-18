package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// labeledForker is a test tool.WorkspaceForker that records, PER LABEL, whether a
// branch's fork was created and whether its cleanup ran. It is the primary vehicle
// for the winner-preservation / loser-cleanup asserts: after a strategy runs, the
// test reads cleaned[label] to prove the winner was PRESERVED (not cleaned) while
// the losers WERE cleaned. Each fork is an independent in-memory workspace, so
// branches are isolated by construction (no disk, no git).
type labeledForker struct {
	mu      sync.Mutex
	seq     int
	created map[string]bool // label -> fork created
	cleaned map[string]bool // label -> cleanup invoked
	roots   map[string]string
}

func newLabeledForker() *labeledForker {
	return &labeledForker{
		created: map[string]bool{},
		cleaned: map[string]bool{},
		roots:   map[string]string{},
	}
}

func (m *labeledForker) Fork(_ context.Context, _ tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	m.mu.Lock()
	m.seq++
	m.created[label] = true
	// Root is keyed on the LABEL only (no seq), so it is deterministic across runs —
	// important for the byte-for-byte join=all backward-compat assert.
	root := fmt.Sprintf("/fork/%s", label)
	m.roots[label] = root
	m.mu.Unlock()

	ws := memfs.NewWorkspace(root)
	cleanup := func() error {
		m.mu.Lock()
		m.cleaned[label] = true
		m.mu.Unlock()
		return nil
	}
	return ws, cleanup, "", nil
}

func (m *labeledForker) wasCleaned(label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleaned[label]
}

func (m *labeledForker) wasForked(label string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.created[label]
}

func (m *labeledForker) root(label string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.roots[label]
}

// routingBranchProvider is a STATELESS scripted LLM whose per-branch reply is keyed
// on a marker found in the branch's own prompt (the user message), so concurrent
// branches drawing from ONE provider stay deterministic regardless of interleaving
// (the shared-cursor mockllm.Provider would not). For a branch whose prompt contains
// key K, the first call returns text summaries[K] and stops. An unknown prompt
// returns a generic summary.
type routingBranchProvider struct {
	summaries map[string]string
}

func (*routingBranchProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *routingBranchProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	prompt := lastUserText(req)
	text := "generic branch summary"
	for key, summary := range p.summaries {
		if strings.Contains(prompt, key) {
			text = summary
			break
		}
	}
	chunks := []port.Chunk{
		{Kind: port.ChunkText, Text: text},
		{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}
	return streamChunks(ctx, chunks), nil
}

// lastUserText returns the text of the last user message in the request (the branch
// task prompt the child engine was run with).
func lastUserText(req port.LLMRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == session.RoleUser {
			return req.Messages[i].Text
		}
	}
	return ""
}

func streamChunks(ctx context.Context, chunks []port.Chunk) iter.Seq2[port.Chunk, error] {
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
	}
}

// fakeJudge is a deterministic, non-LLM BranchJudge for selection asserts: it picks
// the candidate whose Label or Summary contains `pick`, returning its position. If
// errOut is set it returns that error (to exercise the fallback); if pick matches
// nothing it returns an out-of-range index (also a fallback path).
type fakeJudge struct {
	pick      string
	rationale string
	errOut    error
	outOfBand bool // force an out-of-range winner regardless of pick
	gotCalled bool
	gotCrit   string
	mu        sync.Mutex
}

func (j *fakeJudge) Judge(_ context.Context, candidates []agent.BranchSummary, criteria string) (int, string, error) {
	j.mu.Lock()
	j.gotCalled = true
	j.gotCrit = criteria
	j.mu.Unlock()
	if j.errOut != nil {
		return 0, "", j.errOut
	}
	if j.outOfBand {
		return len(candidates) + 5, "", nil
	}
	for i, c := range candidates {
		if strings.Contains(c.Label, j.pick) || strings.Contains(c.Summary, j.pick) {
			return i, j.rationale, nil
		}
	}
	return -1, "", nil // out of range -> ParallelTool falls back to first success
}

func (j *fakeJudge) called() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.gotCalled
}

// --- all: byte-for-byte unchanged --------------------------------------------

// TestParallelJoinAllUnchanged asserts that an explicit join="all" and an ABSENT join
// produce byte-identical ToolResult content (the backward-compat guarantee).
func TestParallelJoinAllUnchanged(t *testing.T) {
	build := func() tool.Tool {
		childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
			"A": "summary A", "B": "summary B",
		}}, tool.NewCatalog())
		return agent.NewParallelTool(childEngine, newLabeledForker(), agent.WithParallelConcurrency(1))
	}

	exec := func(args string) string {
		res, err := build().Execute(context.Background(),
			session.NewToolCall("c1", "Parallel", json.RawMessage(args)), memfs.NewWorkspace("/ws"))
		if err != nil {
			t.Fatalf("unexpected harness error: %v", err)
		}
		if res.IsError {
			t.Fatalf("unexpected error result: %s", res.Content)
		}
		return res.Content
	}

	absent := exec(`{"tasks":["task A","task B"]}`)
	explicit := exec(`{"tasks":["task A","task B"],"join":"all"}`)
	if absent != explicit {
		t.Fatalf("join=all differs from absent join:\n--- absent ---\n%s\n--- explicit ---\n%s", absent, explicit)
	}
	// Sanity: it really is the legacy joinBranches shape.
	if !strings.Contains(absent, "Parallel joined 2 branch(es)") {
		t.Fatalf("join=all is not the legacy shape:\n%s", absent)
	}
}

// TestParallelJoinAllCleansEveryFork asserts join=all tears down EVERY fork (no
// preservation), matching today's contract.
func TestParallelJoinAllCleansEveryFork(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"A": "sa", "B": "sb", "C": "sc",
	}}, tool.NewCatalog())
	rf := newLabeledForker()
	fork := agent.NewParallelTool(childEngine, rf, agent.WithParallelConcurrency(1))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["task A","task B","task C"],"join":"all"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("unexpected: err=%v res=%+v", err, res)
	}
	for _, label := range []string{"branch-1", "branch-2", "branch-3"} {
		if !rf.wasCleaned(label) {
			t.Fatalf("join=all did not clean %s", label)
		}
	}
}

// --- judge -------------------------------------------------------------------

// TestParallelJoinJudgeSelectsWinnerPreservesFork asserts join=judge selects the
// scripted winner, PRESERVES the winner's fork (cleanup NOT called), CLEANS the
// losers, surfaces the rationale, and reports the preserved path.
func TestParallelJoinJudgeSelectsWinnerPreservesFork(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"alpha": "alpha result", "beta": "WINNINGRESULT beta", "gamma": "gamma result",
	}}, tool.NewCatalog())
	rf := newLabeledForker()
	// Judge picks the branch whose summary contains "WINNINGRESULT" (branch-2).
	judge := &fakeJudge{pick: "WINNINGRESULT", rationale: "beta is best"}
	fork := agent.NewParallelTool(childEngine, rf,
		agent.WithParallelConcurrency(1), agent.WithParallelJudge(judge))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel",
			json.RawMessage(`{"tasks":["do alpha","do beta","do gamma"],"join":"judge","criteria":"pick beta"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("unexpected: err=%v res=%+v", err, res)
	}
	if !judge.called() {
		t.Fatalf("judge was not called for a ≥2-success judge run")
	}
	if judge.gotCrit != "pick beta" {
		t.Fatalf("criteria not forwarded to judge: %q", judge.gotCrit)
	}
	// branch-2 is the winner.
	if !strings.Contains(res.Content, "selected branch-2") {
		t.Fatalf("expected branch-2 selected:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "beta is best") {
		t.Fatalf("rationale not surfaced:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, rf.root("branch-2")) {
		t.Fatalf("winner workspace path not reported:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "PRESERVED") {
		t.Fatalf("preservation note missing:\n%s", res.Content)
	}
	// Winner preserved; losers cleaned.
	if rf.wasCleaned("branch-2") {
		t.Fatalf("winner fork branch-2 was cleaned (should be PRESERVED)")
	}
	if !rf.wasCleaned("branch-1") || !rf.wasCleaned("branch-3") {
		t.Fatalf("loser forks not cleaned: b1=%v b3=%v", rf.wasCleaned("branch-1"), rf.wasCleaned("branch-3"))
	}
}

// TestParallelJoinBestAliasesJudge asserts join="best" behaves as join="judge".
func TestParallelJoinBestAliasesJudge(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"x": "rx", "y": "WIN ry",
	}}, tool.NewCatalog())
	rf := newLabeledForker()
	judge := &fakeJudge{pick: "WIN", rationale: "y wins"}
	fork := agent.NewParallelTool(childEngine, rf, agent.WithParallelConcurrency(1), agent.WithParallelJudge(judge))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["do x","do y"],"join":"best"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("unexpected: err=%v res=%+v", err, res)
	}
	if !judge.called() || !strings.Contains(res.Content, "selected branch-2") {
		t.Fatalf("best did not alias judge / pick branch-2:\n%s", res.Content)
	}
}

// TestParallelJoinJudgeFallbackOnBadVerdict asserts that when the judge errors (or
// returns out-of-range), Fork FALLS BACK to the first successful branch instead of
// hard-failing, and still preserves that branch's fork.
func TestParallelJoinJudgeFallbackOnBadVerdict(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"A": "sa", "B": "sb",
	}}, tool.NewCatalog())
	rf := newLabeledForker()
	judge := &fakeJudge{errOut: fmt.Errorf("judge exploded")}
	fork := agent.NewParallelTool(childEngine, rf, agent.WithParallelConcurrency(1), agent.WithParallelJudge(judge))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["do A","do B"],"join":"judge"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("unexpected harness error: %v", err)
	}
	if res.IsError {
		t.Fatalf("judge error must NOT hard-fail Fork; got error result:\n%s", res.Content)
	}
	// Fallback = first successful branch = branch-1.
	if !strings.Contains(res.Content, "selected branch-1") {
		t.Fatalf("expected fallback to first success (branch-1):\n%s", res.Content)
	}
	if rf.wasCleaned("branch-1") {
		t.Fatalf("fallback winner branch-1 fork was cleaned (should be preserved)")
	}
	if !rf.wasCleaned("branch-2") {
		t.Fatalf("loser branch-2 fork not cleaned")
	}
}

// TestParallelJoinJudgeSingleSuccessSkipsJudge asserts that with exactly ONE successful
// branch the judge is NOT called (trivial winner) and that branch is preserved.
func TestParallelJoinJudgeSingleSuccessSkipsJudge(t *testing.T) {
	// branch-1 succeeds; branch-2's fork fails (so only one success).
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"A": "only A", "B": "sb",
	}}, tool.NewCatalog())
	rf := newLabeledForker()
	// Make branch-2 fail by failing its fork via a wrapper.
	failing := &labelFailForker{inner: rf, failLabel: "branch-2"}
	judge := &fakeJudge{pick: "nonexistent"}
	fork := agent.NewParallelTool(childEngine, failing, agent.WithParallelConcurrency(1), agent.WithParallelJudge(judge))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["do A","do B"],"join":"judge"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("unexpected: err=%v res=%+v", err, res)
	}
	if judge.called() {
		t.Fatalf("judge must be SKIPPED when only one branch succeeds")
	}
	if !strings.Contains(res.Content, "selected branch-1") {
		t.Fatalf("expected trivial winner branch-1:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "only one branch succeeded") {
		t.Fatalf("expected single-success note:\n%s", res.Content)
	}
	if rf.wasCleaned("branch-1") {
		t.Fatalf("single-success winner branch-1 fork was cleaned")
	}
}

// TestParallelJoinJudgeNoSuccessAllFailedReport asserts 0 successful branches yield the
// all-failed report (no judge call) and every fork is cleaned.
func TestParallelJoinJudgeNoSuccessAllFailedReport(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{}},
		tool.NewCatalog())
	rf := newLabeledForker()
	// Both forks fail.
	failing := &labelFailForker{inner: rf, failAll: true}
	judge := &fakeJudge{pick: "x"}
	fork := agent.NewParallelTool(childEngine, failing, agent.WithParallelConcurrency(1), agent.WithParallelJudge(judge))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["do A","do B"],"join":"judge"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("unexpected: err=%v res=%+v", err, res)
	}
	if judge.called() {
		t.Fatalf("judge must NOT be called when no branch succeeds")
	}
	// All-failed report uses the legacy joinBranches header.
	if !strings.Contains(res.Content, "0 succeeded, 2 failed") {
		t.Fatalf("expected all-failed report:\n%s", res.Content)
	}
}

// TestParallelJoinJudgeUnavailable asserts join=judge with NO judge wired returns a
// model-addressable error result (not a panic / not a silent fallback).
func TestParallelJoinJudgeUnavailable(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{}}, tool.NewCatalog())
	rf := newLabeledForker()
	fork := agent.NewParallelTool(childEngine, rf, agent.WithParallelConcurrency(1)) // no WithParallelJudge

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["a","b"],"join":"judge"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("unexpected harness error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "judge selection is unavailable") {
		t.Fatalf("expected judging-unavailable error:\n%+v", res)
	}
	// No forks should have been created (we reject before fanning out).
	if len(rf.created) != 0 {
		t.Fatalf("forks created despite unavailable judge: %v", rf.created)
	}
}

// --- first -------------------------------------------------------------------

// TestParallelJoinFirstReturnsFirstSuccessCancelsLosers asserts join=first returns the
// FIRST branch to succeed by COMPLETION ORDER (not index), cancels the in-flight
// losers, cleans loser forks, and PRESERVES the winner's fork. Determinism: a gated
// child tool lets branch-2 finish first while branch-1 and branch-3 block until the
// winner's completion cancels them — so completion order != index order.
func TestParallelJoinFirstReturnsFirstSuccessCancelsLosers(t *testing.T) {
	winnerDone := make(chan struct{})
	// Child tool: the "fast" branch (prompt contains FAST) returns immediately and
	// closes winnerDone; the others block until ctx is cancelled (losers) — proving
	// the winner finished first by completion order.
	childTool := &fakeTool{name: "Read", readOnly: true,
		exec: func(ctx context.Context, in session.ToolCall, ws tool.Workspace) (session.ToolResult, error) {
			if strings.Contains(ws.Root(), "branch-2") {
				close(winnerDone)
				return session.NewToolResult(in.ID, "fast done"), nil
			}
			// Loser: wait until cancelled (winner triggers the cancel).
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			return session.NewToolResult(in.ID, "slow done"), nil
		}}
	// Each branch reads once then summarizes (stateless, history-driven).
	childEngine := childEngineWith(&firstBranchProvider{}, catalogWith(t, childTool))
	rf := newLabeledForker()
	fork := agent.NewParallelTool(childEngine, rf, agent.WithParallelConcurrency(3))

	done := make(chan session.ToolResult, 1)
	go func() {
		res, _ := fork.Execute(context.Background(),
			session.NewToolCall("c1", "Parallel",
				json.RawMessage(`{"tasks":["slow one","FAST two","slow three"],"join":"first"}`)),
			memfs.NewWorkspace("/ws"))
		done <- res
	}()

	select {
	case res := <-done:
		if res.IsError {
			t.Fatalf("unexpected error result:\n%s", res.Content)
		}
		if !strings.Contains(res.Content, "branch-2 succeeded first") {
			t.Fatalf("expected branch-2 to win by completion order:\n%s", res.Content)
		}
		if rf.wasCleaned("branch-2") {
			t.Fatalf("first winner branch-2 fork was cleaned (should be preserved)")
		}
		// The real invariant is "no loser fork LEAKS" — cleaned-or-never-forked.
		// A loser may legitimately never fork at all: launchBranch's slot select can
		// see BOTH the semaphore slot and its (winner-triggered) cancelled ctx ready,
		// and Go's select picks pseudo-randomly — a late loser then takes the
		// cancelledBeforeStart path and there is no fork to clean. Asserting
		// wasCleaned alone false-failed on that schedule (a pre-existing I2 flake).
		for _, loser := range []string{"branch-1", "branch-3"} {
			if rf.wasForked(loser) && !rf.wasCleaned(loser) {
				t.Fatalf("loser %s fork LEAKED (forked but never cleaned)", loser)
			}
		}
		if !strings.Contains(res.Content, rf.root("branch-2")) {
			t.Fatalf("winner path not reported:\n%s", res.Content)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("join=first hung — losers not cancelled after the winner finished")
	}
	<-winnerDone // ensure the winner actually ran
}

// TestParallelJoinFirstAllFailDegrades asserts join=first with NO success degrades to
// the all-failed report and cleans every fork.
func TestParallelJoinFirstAllFailDegrades(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{}}, tool.NewCatalog())
	rf := newLabeledForker()
	failing := &labelFailForker{inner: rf, failAll: true}
	fork := agent.NewParallelTool(childEngine, failing, agent.WithParallelConcurrency(2))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["a","b"],"join":"first"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil || res.IsError {
		t.Fatalf("unexpected: err=%v res=%+v", err, res)
	}
	if !strings.Contains(res.Content, "0 succeeded, 2 failed") {
		t.Fatalf("expected all-failed report:\n%s", res.Content)
	}
}

// --- unknown join ------------------------------------------------------------

// TestParallelJoinUnknownStrategyErrors asserts an unknown join value yields a
// model-addressable error result and creates no forks.
func TestParallelJoinUnknownStrategyErrors(t *testing.T) {
	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{}}, tool.NewCatalog())
	rf := newLabeledForker()
	fork := agent.NewParallelTool(childEngine, rf, agent.WithParallelConcurrency(1))

	res, err := fork.Execute(context.Background(),
		session.NewToolCall("c1", "Parallel", json.RawMessage(`{"tasks":["a"],"join":"bogus"}`)),
		memfs.NewWorkspace("/ws"))
	if err != nil {
		t.Fatalf("unexpected harness error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "unknown join strategy") {
		t.Fatalf("expected unknown-join error:\n%+v", res)
	}
	if len(rf.created) != 0 {
		t.Fatalf("forks created despite unknown join: %v", rf.created)
	}
}

// firstBranchProvider is a stateless child LLM for the first-strategy test: it asks
// for the Read tool once (so the gated child tool runs), then summarizes "ok" once a
// tool result is present.
type firstBranchProvider struct{}

func (firstBranchProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (firstBranchProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	for _, m := range req.Messages {
		if m.ToolResult != nil {
			return streamChunks(ctx, []port.Chunk{
				{Kind: port.ChunkText, Text: "branch ok"},
				{Kind: port.ChunkUsage, Usage: &session.Usage{}},
				{Kind: port.ChunkDone, Stop: session.StopEndTurn},
			}), nil
		}
	}
	call := toolCall("k", "Read", `{"path":"x"}`)
	return streamChunks(ctx, []port.Chunk{
		{Kind: port.ChunkToolCall, ToolCall: &call},
		{Kind: port.ChunkUsage, Usage: &session.Usage{}},
		{Kind: port.ChunkDone, Stop: session.StopEndTurn},
	}), nil
}

// labelFailForker wraps a forker and fails Fork for specific branch labels (or all),
// so a branch is forced to FAIL deterministically by index — independent of which
// goroutine forks first. A non-failing label delegates to inner.
type labelFailForker struct {
	inner     *labeledForker
	failLabel string
	failAll   bool
}

func (m *labelFailForker) Fork(ctx context.Context, base tool.Workspace, label string) (tool.Workspace, func() error, string, error) {
	if m.failAll || label == m.failLabel {
		return nil, nil, "", fmt.Errorf("labelFailForker: scripted fork failure on %s", label)
	}
	return m.inner.Fork(ctx, base, label)
}

// Compile-time assertions that the fakes satisfy the seams they stand in for.
var (
	_ tool.WorkspaceForker = (*labeledForker)(nil)
	_ tool.WorkspaceForker = (*labelFailForker)(nil)
	_ port.LLMProvider     = (*routingBranchProvider)(nil)
	_ port.LLMProvider     = firstBranchProvider{}
	_ agent.BranchJudge    = (*fakeJudge)(nil)
)
