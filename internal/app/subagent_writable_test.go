package app

import (
	"context"
	"os/exec"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
)

// recordingMerger is a tool.ForkMerger test double recording every Merge call's
// fork+parent roots. It is the SAME-instance probe: one recordingMerger placed on
// catalogAssets.autoMerger must receive calls from BOTH the writable Subagent and
// the Parallel single-branch paths, proving they share the composition-injected
// merger.
type recordingMerger struct {
	mu    sync.Mutex
	calls []struct{ fork, parent string }
}

func (m *recordingMerger) Merge(_ context.Context, forkRoot string, parentWS tool.Workspace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, struct{ fork, parent string }{forkRoot, parentWS.Root()})
	return nil
}

func (m *recordingMerger) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// TestBuildSubagentToolWiresWritableChildAndMerger drives the REAL buildSubagentTool
// with a writable subagent (mode:"read-write") and proves the composition wiring:
// the resulting tool runs a WRITABLE child engine (Edit/Write catalog) in a
// force-copy fork and calls the SHARED catalogAssets.autoMerger post-run with the
// fork root. The merger is a recording double placed on the assets, so this asserts
// buildSubagentTool consumes a.autoMerger.
func TestBuildSubagentToolWiresWritableChildAndMerger(t *testing.T) {
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

	// The child engine buildWritableSubagentChildEngine builds is opaque; we drive it
	// with a scripted provider that simply summarizes (the merge fires regardless of
	// whether the child wrote, so a text-only child still exercises the merge seam).
	childProvider := mockllm.New(mockllm.TextTurn("did the work"))
	task, closeFn := buildSubagentTool(context.Background(),
		cfg, regForTest(childProvider, providerMock, cfg.Model), childProvider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, nil, assets, false)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}

	parentWS := osfsWSForTest(t, repo)
	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"implement the fix","mode":"read-write"}`)),
		parentWS)
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("writable subagent returned an error: %q", res.Content)
	}
	if merger.count() != 1 {
		t.Fatalf("shared merger called %d times, want 1 (writable subagent post-run merge)", merger.count())
	}
	m := merger.calls[0]
	if m.parent != repo {
		t.Errorf("merge parent root = %q, want the parent workspace %q", m.parent, repo)
	}
	if m.fork == repo || m.fork == "" {
		t.Errorf("merge fork root = %q, want an isolated fork (not the parent repo / empty)", m.fork)
	}
}

// TestNoFSSubagentToolRejectsWritable proves the no-FS path wires NO writable child
// engine, so a mode:"read-write" call there is a model-addressable "not supported"
// error (D4 — the no-FS gate falls out of the unwired writable engine).
func TestNoFSSubagentToolRejectsWritable(t *testing.T) {
	cfg := teamCfg(t)
	childProvider := mockllm.New(mockllm.TextTurn("x"))
	task, _ := buildSubagentTool(context.Background(),
		cfg, regForTest(childProvider, providerMock, cfg.Model), childProvider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, nil, catalogAssets{}, true /* noFS */)

	res, err := task.Execute(context.Background(),
		session.NewToolCall("c1", "Subagent", []byte(`{"prompt":"go","mode":"read-write"}`)),
		nofs.New())
	if err != nil {
		t.Fatalf("Subagent.Execute: %v", err)
	}
	if !res.IsError {
		t.Fatalf("no-FS read-write must be rejected, got success: %q", res.Content)
	}
}

// TestSharedMergerReachesParallelAndSubagent proves the SAME catalogAssets.autoMerger
// instance reaches BOTH the Parallel register path and the writable Subagent register
// path: one recording merger, placed on the assets, receives a Merge call from each.
func TestSharedMergerReachesParallelAndSubagent(t *testing.T) {
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

	// (1) Writable Subagent path.
	subProvider := mockllm.New(mockllm.TextTurn("subagent did work"))
	subTask, subClose := buildSubagentTool(context.Background(),
		cfg, regForTest(subProvider, providerMock, cfg.Model), subProvider, providerMock, cfg.Model,
		hookexec.New(nil), agents.NewRegistry(nil), nil, nil, nil, nil, assets, false)
	if subClose != nil {
		defer func() { _ = subClose() }()
	}
	parentWS := osfsWSForTest(t, repo)
	if r, err := subTask.Execute(context.Background(),
		session.NewToolCall("s1", "Subagent", []byte(`{"prompt":"implement","mode":"read-write"}`)),
		parentWS); err != nil || r.IsError {
		t.Fatalf("writable subagent failed: err=%v res=%q", err, r.Content)
	}
	afterSubagent := merger.count()
	if afterSubagent != 1 {
		t.Fatalf("after subagent: merger calls = %d, want 1", afterSubagent)
	}

	// (2) Parallel single-branch path, same assets ⇒ same merger.
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
		parentWS); err != nil || r.IsError {
		t.Fatalf("parallel single-branch failed: err=%v res=%q", err, r.Content)
	}
	if got := merger.count(); got != 2 {
		t.Fatalf("after parallel: merger calls = %d, want 2 (the SAME merger received BOTH the subagent and the parallel merge)", got)
	}
}

// compile-time: recordingMerger is a ForkMerger.
var _ tool.ForkMerger = (*recordingMerger)(nil)
