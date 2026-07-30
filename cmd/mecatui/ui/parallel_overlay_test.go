package ui

// Tests for the third ctrl+a tab — Parallel (the fork-join GROUP roster + per-group
// focus). A Parallel run is a GROUP (not a flat fleet): branches share a join mode + a
// single winner + preserved fork paths. Everything renders from the REDACTED,
// metadata-only parallel.* event projection — no branch content (gauntlet #7). The seeds
// drive the REAL client.ParallelMsg flow through Update/applyParallel (NOT struct
// literals), so a regression in apply is caught.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// startPar / branchStartPar / branchToolPar / branchEndPar / endPar build the parallel.*
// projections for a Parallel run.
func startPar(parent, join string, count int) client.ParallelMsg {
	return client.ParallelMsg{Kind: client.ParallelStart, ParentCallID: parent, Join: join, BranchCount: count}
}

func branchStartPar(parent string, idx int, label, goal string) client.ParallelMsg {
	return client.ParallelMsg{Kind: client.ParallelBranchStart, ParentCallID: parent, BranchIndex: idx, BranchLabel: label, Goal: goal}
}

func branchToolPar(parent string, idx int, tool string, isErr bool, count int) client.ParallelMsg {
	return client.ParallelMsg{Kind: client.ParallelBranchTool, ParentCallID: parent, BranchIndex: idx, ToolName: tool, IsError: isErr, ToolCount: count}
}

func branchEndPar(parent string, idx int, in, out int64, count int, stop string, failed bool, ws string) client.ParallelMsg {
	return client.ParallelMsg{
		Kind: client.ParallelBranchEnd, ParentCallID: parent, BranchIndex: idx,
		Usage: client.Usage{InputTokens: in, OutputTokens: out}, ToolCount: count,
		Stop: stop, Failed: failed, Workspace: ws, DurationMs: 900,
	}
}

func endPar(parent, join string, count, winner int, winnerWS, stop string) client.ParallelMsg {
	return client.ParallelMsg{
		Kind: client.ParallelEnd, ParentCallID: parent, Join: join, BranchCount: count,
		Winner: winner, WinnerWorkspace: winnerWS, Stop: stop,
	}
}

// seedParallel applies a sequence of parallel.* msgs through the real Update path so the
// model's grouped parallelGroups state is built exactly as it would be at runtime. It
// seeds a Parallel tool card for the inline-card routing first.
func seedParallel(m Model, parent string, msgs ...client.ParallelMsg) Model {
	m.conv.addTool(parent, "Parallel", `{"tasks":["a","b"]}`)
	for _, msg := range msgs {
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	return m
}

// TestParallelGroupCountsThroughWire locks the (running, done) group classification built
// through the REAL wire path (Update → applyParallel): a group is done once its
// parallel.end arrived.
func TestParallelGroupCountsThroughWire(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 2),
		branchStartPar("p1", 0, "branch-1", "explore A"),
		branchStartPar("p1", 1, "branch-2", "explore B"),
		branchEndPar("p1", 0, 100, 20, 3, "end_turn", false, "/fork/branch-1"),
	)
	running, done := m.conv.parallelGroupCounts()
	if running != 1 || done != 0 {
		t.Fatalf("before end: running=%d done=%d want 1/0", running, done)
	}
	mm, _ := m.Update(endPar("p1", "all", 2, -1, "", "end_turn"))
	m = mm.(Model)
	running, done = m.conv.parallelGroupCounts()
	if running != 0 || done != 1 {
		t.Fatalf("after end: running=%d done=%d want 0/1", running, done)
	}
}

// TestParallelRosterRendersGroup asserts the Parallel tab roster shows the group with its
// join mode, branch tally, and winner once resolved.
func TestParallelRosterRendersGroup(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "judge", 2),
		branchStartPar("p1", 0, "branch-1", "approach A"),
		branchStartPar("p1", 1, "branch-2", "approach B"),
		branchEndPar("p1", 0, 100, 20, 2, "end_turn", false, "/fork/branch-1"),
		branchEndPar("p1", 1, 120, 25, 3, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "judge", 2, 1, "/fork/branch-2", "end_turn"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.agentsTab != tabParallel {
		t.Fatalf("expected Parallel tab as default (a finished parallel run, no team/sub), got %v", m.agentsTab)
	}
	out := stripANSIstr(m.View().Content)
	for _, want := range []string{"parallel ·", "judge", "2/2 branches", "winner branch-2"} {
		if !strings.Contains(out, want) {
			t.Errorf("roster missing %q:\n%s", want, out)
		}
	}
}

// TestParallelGroupFocusWinnerHighlight asserts enter on a group focuses it, shows the
// branches inline with the WINNER row marked (★) and the preserved fork path, and esc
// steps back to the roster.
func TestParallelGroupFocusWinnerHighlight(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "judge", 2),
		branchStartPar("p1", 0, "branch-1", "approach A"),
		branchStartPar("p1", 1, "branch-2", "approach B"),
		branchToolPar("p1", 1, "Edit", false, 1),
		branchEndPar("p1", 0, 100, 20, 2, "end_turn", false, "/fork/branch-1"),
		branchEndPar("p1", 1, 120, 25, 3, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "judge", 2, 1, "/fork/branch-2", "end_turn"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.parallel.view != parallelGroupView {
		t.Fatalf("enter should focus the group, view = %v", m.parallel.view)
	}
	if m.parallel.group != "p1" {
		t.Fatalf("focused group = %q want p1", m.parallel.group)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "★") {
		t.Errorf("group focus should highlight the winner row with ★:\n%s", out)
	}
	if !strings.Contains(out, "branch-1") || !strings.Contains(out, "branch-2") {
		t.Errorf("group focus should list all branches inline:\n%s", out)
	}
	if !strings.Contains(out, "winner fork (preserved)") || !strings.Contains(out, "/fork/branch-2") {
		t.Errorf("group focus should show the preserved winner fork path:\n%s", out)
	}
	if !strings.Contains(out, "bounded previews") {
		t.Errorf("group focus should carry the bounded-previews honesty note:\n%s", out)
	}
	// esc steps back to the roster (ONE level — no deeper branch focus).
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.parallel.view != parallelRoster {
		t.Fatalf("esc should step back to the roster, view = %v", m.parallel.view)
	}
}

// TestParallelFailedBranchGlyph asserts a failed branch renders with the ✗ glyph and a
// "failed" label in the group focus, distinct from a clean branch's ✓.
func TestParallelFailedBranchGlyph(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 2),
		branchStartPar("p1", 0, "branch-1", "explore A"),
		branchStartPar("p1", 1, "branch-2", "explore B"),
		branchEndPar("p1", 0, 100, 20, 2, "error", true, "/fork/branch-1"),
		branchEndPar("p1", 1, 120, 25, 3, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "all", 2, -1, "", "end_turn"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "✗") {
		t.Errorf("a failed branch should render the ✗ glyph:\n%s", out)
	}
	if !strings.Contains(out, "failed") {
		t.Errorf("a failed branch should carry the 'failed' label:\n%s", out)
	}
}

// TestParallelGroupFocusBranchDurationAndRunStop covers WI-5 (a finished branch row
// carries its wall-clock duration) and WI-6 (a resolved group's focus shows the
// run-level stop). The seeded branchEndPar sets DurationMs=900 and the judge endPar
// carries stop="end_turn".
func TestParallelGroupFocusBranchDurationAndRunStop(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "judge", 2),
		branchStartPar("p1", 0, "branch-1", "approach A"),
		branchStartPar("p1", 1, "branch-2", "approach B"),
		branchEndPar("p1", 0, 100, 20, 2, "end_turn", false, "/fork/branch-1"),
		branchEndPar("p1", 1, 120, 25, 3, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "judge", 2, 1, "/fork/branch-2", "end_turn"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	// WI-5: branch duration adjacent to the stop label.
	if !strings.Contains(out, "done · 900ms") {
		t.Errorf("a finished branch should show its duration (done · 900ms):\n%s", out)
	}
	// WI-6: the resolved group's run-level stop.
	if !strings.Contains(out, "run stop: done") {
		t.Errorf("a resolved group focus should show the run-level stop:\n%s", out)
	}
}

// TestParallelGroupFocusNoRunStopForJoinAll covers the WI-6 empty-guard: a join=all run
// carries no winner-bearing stop, so the focus shows NO "run stop:" line.
func TestParallelGroupFocusNoRunStopForJoinAll(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 2),
		branchStartPar("p1", 0, "branch-1", "explore A"),
		branchStartPar("p1", 1, "branch-2", "explore B"),
		branchEndPar("p1", 0, 100, 20, 2, "end_turn", false, "/fork/branch-1"),
		branchEndPar("p1", 1, 120, 25, 3, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "all", 2, -1, "", ""), // join=all: no winner, no run stop
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if strings.Contains(out, "run stop:") {
		t.Errorf("a join=all group (no run stop) must render no run-stop line:\n%s", out)
	}
}

// TestParallelTabRoutingAndEsc asserts ctrl+a → tab reaches the Parallel tab and esc
// from the roster closes the overlay.
func TestParallelTabRoutingAndEsc(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	// Seed BOTH a subagent and a parallel run so the default tab is Subagents (haveSub
	// beats finished parallel), and tab must reach Parallel.
	m = seedSubagents(m, "s1", startSub("s1", "c1", "audit"))
	m = seedParallel(m, "p1", startPar("p1", "all", 1), branchStartPar("p1", 0, "branch-1", "go"))
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.agentsTab != tabParallel {
		// parallel is LIVE (no branch_end) so parallelLive beats haveSub.
		t.Fatalf("expected Parallel tab (a live parallel run), got %v", m.agentsTab)
	}
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "▸ Parallel") {
		t.Errorf("tab strip should mark Parallel active:\n%s", out)
	}
	// esc from the roster closes the overlay.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm.(Model)
	if m.team.view != teamNone {
		t.Errorf("esc from the Parallel roster should close the overlay")
	}
}

// TestParallelOverlayBoundsBranchContent is the client-side boundedness guard for the
// Parallel group focus under ADR 0079: branch content reaches the overlay ONLY as
// bounded previews (engine-clamped; the TUI caps them again) and the honesty note
// states "bounded previews" — content is bounded, never hidden and never unbounded.
func TestParallelOverlayBoundsBranchContent(t *testing.T) {
	const canary = "CANARYLEAK"
	longPreview := strings.Repeat("z", maxTraceDetailLen*3)
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 1),
		branchStartPar("p1", 0, "branch-1", "explore"),
		branchToolParPreview("p1", 0, "tool.call", "Grep", longPreview, 1),
		branchEndPar("p1", 0, 10, 2, 1, "end_turn", false, "/fork/branch-1"),
		endPar("p1", "all", 1, -1, "", "end_turn"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	// The honesty note states the accurate posture: bounded previews, not hidden.
	if !strings.Contains(out, "bounded previews") {
		t.Errorf("group focus must carry the bounded-previews note:\n%s", out)
	}
	if strings.Contains(out, "args/results hidden") {
		t.Errorf("group focus must not carry the stale hidden-interior claim:\n%s", out)
	}
	// The preview is rendered — but CAPPED at the TUI's secondary bound.
	if strings.Contains(out, longPreview) {
		t.Errorf("an unbounded preview leaked into the group focus (past maxTraceDetailLen):\n%s", out)
	}
	if !strings.Contains(out, strings.Repeat("z", maxTraceDetailLen-1)) {
		t.Errorf("the bounded preview should render (truncated):\n%s", out)
	}
	// A canary in a tool NAME renders only as a name chip — never as a body line.
	m2 := newMCPModel(t, aztec(), nil)
	m2 = seedParallel(m2, "p1",
		startPar("p1", "all", 1),
		branchStartPar("p1", 0, "branch-1", "explore"),
		branchToolPar("p1", 0, canary, false, 1),
		branchEndPar("p1", 0, 10, 2, 1, "end_turn", false, "/fork/branch-1"),
		endPar("p1", "all", 1, -1, "", "end_turn"),
	)
	mm2, _ := m2.Update(ctrlKey('a'))
	m2 = mm2.(Model)
	mm2, _ = m2.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m2 = mm2.(Model)
	out2 := stripANSIstr(m2.View().Content)
	for _, banned := range []string{canary + " =", "result: " + canary, "args: " + canary} {
		if strings.Contains(out2, banned) {
			t.Fatalf("overlay leaked branch content %q:\n%s", banned, out2)
		}
	}
}

// TestParallelGroupFocusHeightBounded asserts the group focus stays within its height
// budget when every branch carries a trace: a tight window clamps the trace with a
// "+N more lines" tail and surfaces a "+K more branch(es)" roll-up instead of
// overflowing — the header and footer hints are never pushed off.
func TestParallelGroupFocusHeightBounded(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 18)
	var msgs []client.ParallelMsg
	msgs = append(msgs, startPar("p1", "all", 4))
	for i := 0; i < 4; i++ {
		msgs = append(msgs, branchStartPar("p1", i, "branch-"+string(rune('1'+i)), "explore"))
		msgs = append(msgs, branchToolParPreview("p1", i, "tool.call", "Grep", "pattern: foo", 1))
		msgs = append(msgs, branchToolParPreview("p1", i, "message.delta", "", "a branch note", 1))
	}
	m = seedParallel(m, "p1", msgs...)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.parallel.view != parallelGroupView {
		t.Fatalf("enter should focus the group, view = %v", m.parallel.view)
	}
	out := stripANSIstr(m.View().Content)
	// The focus stays height-bounded: an overflow rolls up into a "+K more" tail
	// (branch-level) or a "+N more lines" clamp (trace-level) rather than pushing the
	// footer hint off.
	rolled := strings.Contains(out, "more branch(es)") || strings.Contains(out, "more line")
	if !rolled {
		t.Errorf("a tight group focus should roll up overflow into a +K/+N tail, got %q", out)
	}
	if !strings.Contains(out, "esc back") {
		t.Errorf("the footer hint must survive the height clamp, got %q", out)
	}
}

// TestParallelRosterWindowed asserts a group list larger than the height windows like the
// subagent roster (only fitting rows render, footer hint stays, "+K below" surfaces).
func TestParallelRosterWindowed(t *testing.T) {
	const n = 20
	m := newMCPModel(t, aztec(), nil)
	m = resize(m, 100, 24)
	for i := 0; i < n; i++ {
		parent := "p" + string(rune('a'+i))
		m = seedParallel(m, parent, startPar(parent, "all", 1), branchStartPar(parent, 0, "branch-1", "go"))
	}
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.agentsTab != tabParallel {
		t.Fatalf("expected Parallel tab, got %v", m.agentsTab)
	}
	out := stripANSIstr(m.View().Content)
	rows := teamRosterRows(agentsBodyHeight(m.vp.Height()))
	if rows >= n {
		t.Fatalf("test premise broken: window %d must be < groups %d", rows, n)
	}
	if !strings.Contains(out, "below") {
		t.Errorf("a windowed group list should show a '+K below' tail:\n%s", out)
	}
	if !strings.Contains(out, "enter focus") {
		t.Errorf("footer hint clipped by the window:\n%s", out)
	}
}

// TestParallelRosterOrderInsertionStable guards group row ORDER against a regression to
// map iteration: parallelGroupFor uses an insertion-order index map, so two groups must
// render in the order their parallel.start events arrived (p_a before p_b), every time.
// A regression to ranging a map would make this flake. Seeding p_b FIRST then p_a (so the
// insertion order is the reverse of any lexical sort) makes the assertion bite.
func TestParallelRosterOrderInsertionStable(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	// Insert p_b before p_a — render order must follow INSERTION, not the ids' sort order.
	m = seedParallel(m, "p_b", startPar("p_b", "all", 1), branchStartPar("p_b", 0, "branch-1", "second-seeded BBB"))
	m = seedParallel(m, "p_a", startPar("p_a", "judge", 1), branchStartPar("p_a", 0, "branch-1", "first-after AAA"))
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.agentsTab != tabParallel {
		t.Fatalf("expected Parallel tab, got %v", m.agentsTab)
	}
	// Run several renders; insertion order must be byte-stable (a map-iteration regression
	// would shuffle it across iterations).
	for iter := 0; iter < 8; iter++ {
		out := stripANSIstr(m.View().Content)
		// The roster lines carry the join mode; p_b was seeded first (all), p_a second
		// (judge), so "all" must appear BEFORE "judge" in the rendered roster.
		ai := strings.Index(out, "all ·")
		ji := strings.Index(out, "judge ·")
		if ai < 0 || ji < 0 {
			t.Fatalf("iter %d: roster missing both group rows:\n%s", iter, out)
		}
		if ai > ji {
			t.Fatalf("iter %d: groups not in insertion order — first-seeded (all) must precede second (judge):\n%s", iter, out)
		}
	}
}

// TestParallelBranchOrderByIndex guards branch row ORDER inside a focused group: branch
// events arriving OUT OF ORDER (branch 2's events before branch 0's) must still render BY
// INDEX (branch-1, branch-2, branch-3), because parallelBranchFor keys on a first-seen
// index map and the render walks the branch slice. The seed delivers indices 2,0,1 so a
// regression to map iteration / arrival order would reorder the rows.
func TestParallelBranchOrderByIndex(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 3),
		// Out of order: index 2 first, then 0, then 1.
		branchStartPar("p1", 2, "branch-3", "third"),
		branchStartPar("p1", 0, "branch-1", "first"),
		branchStartPar("p1", 1, "branch-2", "second"),
		branchEndPar("p1", 2, 30, 6, 1, "end_turn", false, "/fork/branch-3"),
		branchEndPar("p1", 0, 10, 2, 1, "end_turn", false, "/fork/branch-1"),
		branchEndPar("p1", 1, 20, 4, 1, "end_turn", false, "/fork/branch-2"),
		endPar("p1", "all", 3, -1, "", "end_turn"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.parallel.view != parallelGroupView {
		t.Fatalf("enter should focus the group, view = %v", m.parallel.view)
	}
	out := stripANSIstr(m.View().Content)
	i1 := strings.Index(out, "branch-1")
	i2 := strings.Index(out, "branch-2")
	i3 := strings.Index(out, "branch-3")
	if i1 < 0 || i2 < 0 || i3 < 0 {
		t.Fatalf("group focus missing a branch row:\n%s", out)
	}
	if i1 >= i2 || i2 >= i3 {
		t.Fatalf("branches not rendered BY INDEX (want branch-1<branch-2<branch-3 despite out-of-order events): "+
			"i1=%d i2=%d i3=%d\n%s", i1, i2, i3, out)
	}
}

// TestParallelBranchTransientToolGlyph (the optional add) asserts a RUNNING branch's
// transient tool activity surfaces in the group focus row: a branch_tool with IsError
// shows the current tool name as the row's live state (no terminal Failed ✗ yet — the
// branch has not ended). It complements TestParallelFailedBranchGlyph (terminal ✗).
func TestParallelBranchTransientToolGlyph(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 1),
		branchStartPar("p1", 0, "branch-1", "explore"),
		branchToolPar("p1", 0, "Grep", true, 1), // a tool errored, but the branch is still RUNNING
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	// The running branch shows its current tool ("Grep…") as the live state, with the
	// in-flight ◐ glyph (NOT a terminal ✗ — the branch has not ended).
	if !strings.Contains(out, "Grep…") {
		t.Errorf("running branch should surface its current tool as live state:\n%s", out)
	}
	if !strings.Contains(out, "◐ branch-1") {
		t.Errorf("a still-running branch (transient tool error) should keep the ◐ glyph, not a terminal ✗:\n%s", out)
	}
}

// branchStartParRouted is branchStartPar plus the opt-in model router's bare metadata
// (a category label + a model id, ADR 0034) — set on branch_start only when the router
// classified the branch.
func branchStartParRouted(parent string, idx int, label, goal, routedCat, routedModel string) client.ParallelMsg {
	msg := branchStartPar(parent, idx, label, goal)
	msg.RoutedCategory = routedCat
	msg.RoutedModel = routedModel
	return msg
}

// branchStartParModel is branchStartPar plus the generic model surface (issue #112 /
// ADR 0035): the concrete model id the branch ACTUALLY ran on, for the non-routed case
// (inherited default / agent-def pin / per-call override).
func branchStartParModel(parent string, idx int, label, goal, model string) client.ParallelMsg {
	msg := branchStartPar(parent, idx, label, goal)
	msg.Model = model
	return msg
}

// TestParallelBranchRoutedMetadata asserts the opt-in model router's bare metadata
// (category + model, ADR 0034) surfaces on a branch row in the group focus view as a
// muted "routed: <category> → <model>" cue — and is absent for an unrouted branch. It
// rides the REAL wire path (Update → applyParallel) and carries no branch content
// (gauntlet #7).
func TestParallelBranchRoutedMetadata(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedParallel(m, "p1",
		startPar("p1", "all", 3),
		branchStartParRouted("p1", 0, "branch-1", "trivial single-step", "small", "openai/gpt-4.1-mini"),
		// branch-2 is the PLAIN (non-routed) inherited-model case (issue #112): it shows
		// "model: <id>" instead of a routed cue.
		branchStartParModel("p1", 1, "branch-2", "unrouted", "anthropic/claude-3.5"),
		branchStartPar("p1", 2, "branch-3", "no model known"),
	)
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	out := stripANSIstr(m.View().Content)
	if !strings.Contains(out, "routed: small → openai/gpt-4.1-mini") {
		t.Errorf("routed branch should surface the routed-model cue in group focus:\n%s", out)
	}
	// The plain inherited-model branch shows the model: cue.
	if !strings.Contains(out, "model: anthropic/claude-3.5") {
		t.Errorf("plain-model branch should surface the model: cue in group focus:\n%s", out)
	}
	// Exactly one "routed:" occurrence (the routed branch-1 only); the plain-model and
	// unknown-model branches carry no routed cue.
	if n := strings.Count(out, "routed:"); n != 1 {
		t.Errorf("exactly one routed cue expected (the routed branch only), got %d:\n%s", n, out)
	}
	// Exactly one "model:" occurrence (the plain branch-2 only); the routed branch shows
	// the model inside its routed cue, not as a standalone "model:" line, and the
	// unknown-model branch shows nothing.
	if n := strings.Count(out, "model:"); n != 1 {
		t.Errorf("exactly one plain model: cue expected (the inherited-model branch only), got %d:\n%s", n, out)
	}
}
