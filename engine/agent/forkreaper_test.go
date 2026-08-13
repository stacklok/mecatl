package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// TestLRUForkReaperUnit drives the bounded reaper directly: it keeps the most
// recent cap forks and reaps the OLDEST beyond the cap (invoking its cleanup).
func TestLRUForkReaperUnit(t *testing.T) {
	var mu sync.Mutex
	reaped := map[string]bool{}
	cleanup := func(root string) func() error {
		return func() error {
			mu.Lock()
			reaped[root] = true
			mu.Unlock()
			return nil
		}
	}

	r := agent.NewLRUForkReaper(2)
	r.Preserve("/a", cleanup("/a"))
	r.Preserve("/b", cleanup("/b"))
	if r.Len() != 2 {
		t.Fatalf("len after 2 within cap = %d, want 2", r.Len())
	}
	// Third preserve evicts the oldest (/a).
	r.Preserve("/c", cleanup("/c"))
	if r.Len() != 2 {
		t.Fatalf("len after exceeding cap = %d, want 2 (bounded)", r.Len())
	}
	mu.Lock()
	if !reaped["/a"] {
		t.Fatalf("oldest /a was not reaped on eviction")
	}
	if reaped["/b"] || reaped["/c"] {
		t.Fatalf("surviving forks were reaped: b=%v c=%v", reaped["/b"], reaped["/c"])
	}
	mu.Unlock()
}

// TestLRUForkReaperRefreshesRecency asserts re-preserving an existing root refreshes
// its recency (it is NOT the one evicted next) rather than double-counting.
func TestLRUForkReaperRefreshesRecency(t *testing.T) {
	var mu sync.Mutex
	reaped := map[string]bool{}
	cl := func(root string) func() error {
		return func() error { mu.Lock(); reaped[root] = true; mu.Unlock(); return nil }
	}
	r := agent.NewLRUForkReaper(2)
	r.Preserve("/a", cl("/a"))
	r.Preserve("/b", cl("/b"))
	r.Preserve("/a", cl("/a")) // refresh /a => now /b is oldest
	r.Preserve("/c", cl("/c")) // evicts /b
	if r.Len() != 2 {
		t.Fatalf("len = %d, want 2", r.Len())
	}
	mu.Lock()
	defer mu.Unlock()
	if !reaped["/b"] {
		t.Fatalf("expected /b reaped (oldest after refresh)")
	}
	if reaped["/a"] {
		t.Fatalf("/a should have survived (recency refreshed)")
	}
}

// TestLRUForkReaperNilCleanupIgnored asserts a nil cleanup is not tracked (nothing
// to reap), so it never displaces a real preserved fork.
func TestLRUForkReaperNilCleanupIgnored(t *testing.T) {
	r := agent.NewLRUForkReaper(1)
	r.Preserve("/x", nil)
	if r.Len() != 0 {
		t.Fatalf("nil cleanup tracked: len = %d, want 0", r.Len())
	}
}

// uniqueForker is an EnvironmentForker whose every fork gets a globally-unique root
// (call-scoped, unlike labeledForker which reuses /fork/<label>), and which records
// every root whose cleanup ran. It is the vehicle for the cross-call reaping assert:
// after N judge Forks, only the cap-many most-recent winner roots should remain
// un-cleaned on "disk".
type uniqueForker struct {
	mu      sync.Mutex
	seq     int
	cleaned map[string]bool
}

func newUniqueForker() *uniqueForker { return &uniqueForker{cleaned: map[string]bool{}} }

func (m *uniqueForker) Fork(_ context.Context, _ tool.Environment, label string) (tool.Environment, func() error, string, error) {
	m.mu.Lock()
	m.seq++
	root := fmt.Sprintf("/fork/%s/%d", label, m.seq)
	m.mu.Unlock()
	ws := memfs.NewWorkspace(root)
	cleanup := func() error {
		m.mu.Lock()
		m.cleaned[root] = true
		m.mu.Unlock()
		return nil
	}
	return agent.ForkEnv(ws), cleanup, "", nil
}

func (m *uniqueForker) wasCleaned(root string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleaned[root]
}

// TestForkWinnerReaperBoundsPreservedForks runs several join=judge Forks through one
// ParallelTool wired with a small-cap reaper and asserts that only `cap` winner forks
// remain un-cleaned: the oldest winners beyond the cap are reaped, while the most
// recent winner stays inspectable (its fork is NOT cleaned).
func TestForkWinnerReaperBoundsPreservedForks(t *testing.T) {
	const capN = 2
	const calls = 5

	childEngine := childEngineWith(&routingBranchProvider{summaries: map[string]string{
		"alpha": "alpha result", "beta": "WIN beta",
	}}, tool.NewCatalog())
	uf := newUniqueForker()
	judge := &fakeJudge{pick: "WIN", rationale: "beta wins"}
	reaper := agent.NewLRUForkReaper(capN)
	fork := agent.NewParallelTool(childEngine, uf,
		agent.WithParallelConcurrency(1),
		agent.WithParallelJudge(judge),
		agent.WithWinnerReaper(reaper))

	var winnerRoots []string
	for i := 0; i < calls; i++ {
		res, err := fork.Execute(context.Background(),
			session.NewToolCall(session.ToolCallID(fmt.Sprintf("c%d", i)), "Parallel",
				json.RawMessage(`{"tasks":["do alpha","do beta"],"join":"judge","criteria":"pick beta"}`)),
			agent.MemEnv("/ws"))
		if err != nil || res.IsError {
			t.Fatalf("call %d: err=%v res=%+v", i, err, res)
		}
		// The winner (branch-2) is preserved; extract its reported root from the result.
		root := extractWinnerRoot(t, res.Content)
		winnerRoots = append(winnerRoots, root)
	}

	if reaper.Len() != capN {
		t.Fatalf("reaper retains %d forks, want the cap %d", reaper.Len(), capN)
	}

	// The oldest (calls-cap) winners must be reaped; the most-recent capN survive.
	for i, root := range winnerRoots {
		recent := i >= calls-capN
		cleaned := uf.wasCleaned(root)
		if recent && cleaned {
			t.Fatalf("recent winner %q (call %d) was reaped but should survive", root, i)
		}
		if !recent && !cleaned {
			t.Fatalf("old winner %q (call %d) was NOT reaped (unbounded leak)", root, i)
		}
	}
}

// extractWinnerRoot pulls the preserved winner workspace path out of a join=judge
// result body ("winner workspace (PRESERVED ...): <root>").
func extractWinnerRoot(t *testing.T, content string) string {
	t.Helper()
	const marker = "): "
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "winner workspace (PRESERVED") {
			if j := strings.Index(line, marker); j >= 0 {
				return line[j+len(marker):]
			}
		}
	}
	t.Fatalf("no winner workspace path in result:\n%s", content)
	return ""
}
