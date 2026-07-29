package ui

// Tests for the BACKGROUND-subagent surfaces (background-subagents arc, I4): the
// fleet-lane "⇢ bg" marker (driven by subagent.start's Background field), the
// transient footer notice when a background child completes (mirroring the
// team-done / no-progress advisory channel — never a durable scrollback line),
// the focus pane's honest delivery note (the registry's delivered state is NOT
// on the wire; the pane renders only background + done), and the footer fleet
// count carrying a cross-turn background child.

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// startBgSub builds a background subagent.start projection (Background: true).
func startBgSub(parent, child, goal string) client.SubagentMsg {
	msg := startSub(parent, child, goal)
	msg.Background = true
	return msg
}

// TestSubagentRosterLineBackgroundMarker locks the lane marker: a background lane
// carries the "⇢ bg" marker after its #hash; a foreground lane does not.
func TestSubagentRosterLineBackgroundMarker(t *testing.T) {
	bg := &subagentLane{childID: "subagent-p1", goal: "long audit", background: true}
	fg := &subagentLane{childID: "subagent-p2", goal: "quick trace"}
	if got := subagentRosterLine(bg); !strings.Contains(got, subagentBackgroundMarker) {
		t.Errorf("background lane must carry the %q marker, got %q", subagentBackgroundMarker, got)
	}
	if got := subagentRosterLine(fg); strings.Contains(got, subagentBackgroundMarker) {
		t.Errorf("foreground lane must NOT carry the %q marker, got %q", subagentBackgroundMarker, got)
	}
}

// TestSubagentRosterBackgroundMarkerEndToEnd drives the real Update path: a
// background start + a foreground start, then asserts the rendered roster shows
// the marker on exactly the background row.
func TestSubagentRosterBackgroundMarkerEndToEnd(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startBgSub("p1", "subagent-p1", "long audit"),
		startSub("p1", "subagent-p2", "quick trace"),
	)
	out := stripANSIstr(renderSubagentRoster(m.deps.Theme, subagentState{}, m.conv.subagentFleet, 0))
	var bgLine, fgLine string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "long audit") {
			bgLine = line
		}
		if strings.Contains(line, "quick trace") {
			fgLine = line
		}
	}
	if bgLine == "" || fgLine == "" {
		t.Fatalf("roster must list both lanes, got:\n%s", out)
	}
	if !strings.Contains(bgLine, subagentBackgroundMarker) {
		t.Errorf("background row must carry %q, got %q", subagentBackgroundMarker, bgLine)
	}
	if strings.Contains(fgLine, subagentBackgroundMarker) {
		t.Errorf("foreground row must NOT carry %q, got %q", subagentBackgroundMarker, fgLine)
	}
}

// TestSubagentFocusBackgroundNote asserts the focus pane's honest delivery note: a
// RUNNING background child reads "runs detached … SubagentStatus"; a DONE one reads
// "result ready for the agent"; a foreground child carries no background note. The
// note never claims a collected/uncollected state — the registry's delivered flag is
// not on the wire.
func TestSubagentFocusBackgroundNote(t *testing.T) {
	th := aztec()
	running := []subagentLane{{childID: "subagent-p1", goal: "long audit", background: true}}
	out := stripANSIstr(renderSubagentFocus(th, running, "subagent-p1", 0, 0))
	if !strings.Contains(out, "background: runs detached") || !strings.Contains(out, "SubagentStatus") {
		t.Errorf("running background focus must note the detached delivery channel, got:\n%s", out)
	}

	done := []subagentLane{{childID: "subagent-p1", goal: "long audit", background: true, done: true, stop: "end_turn"}}
	out = stripANSIstr(renderSubagentFocus(th, done, "subagent-p1", 0, 0))
	if !strings.Contains(out, "background: done — result ready for the agent") {
		t.Errorf("done background focus must note the result is ready, got:\n%s", out)
	}

	fg := []subagentLane{{childID: "subagent-p2", goal: "quick trace"}}
	out = stripANSIstr(renderSubagentFocus(th, fg, "subagent-p2", 0, 0))
	if strings.Contains(out, "background:") {
		t.Errorf("foreground focus must carry no background note, got:\n%s", out)
	}
}

// TestBackgroundSubagentEndTransientNotice asserts a background child's
// subagent.end surfaces the transient footer notice (advisory channel, like
// team-done) and appends NO durable scrollback block.
func TestBackgroundSubagentEndTransientNotice(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1", startBgSub("p1", "subagent-p1", "long audit"))
	blocksBefore := len(m.conv.blocks)

	m = seedSubagents(m, "p2", endSub("p1", "subagent-p1", 9000, 1200, 4, "end_turn"))
	got := stripANSIstr(m.statusMsg)
	if !strings.Contains(got, "done — result ready for the agent") ||
		!strings.Contains(got, shortChildID("subagent-p1")) {
		t.Errorf("background end must set the transient footer notice with the #hash, got %q", got)
	}
	// seedSubagents added one Subagent card block for "p2"; beyond that the end event
	// must leave scrollback untouched (the notice is transient, never durable).
	if len(m.conv.blocks) != blocksBefore+1 {
		t.Errorf("background end must not append scrollback notices, blocks %d → %d", blocksBefore, len(m.conv.blocks))
	}
}

// TestForegroundSubagentEndNoTransientNotice asserts a FOREGROUND child's end stays
// silent on the footer status — its result already landed on its own Subagent card.
func TestForegroundSubagentEndNoTransientNotice(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "subagent-p1", "quick trace"),
		endSub("p1", "subagent-p1", 9000, 1200, 4, "end_turn"),
	)
	if got := stripANSIstr(m.statusMsg); strings.Contains(got, "result ready for the agent") {
		t.Errorf("foreground end must not set the background-done notice, got %q", got)
	}
}

// TestFooterCountsCrossTurnBackgroundChild asserts the footer delegation segment
// keeps counting a background child that spans TURNS as running (the fleet is
// session-scoped, keyed on subagent.end — not on turn boundaries), and flips it to
// done when its end finally arrives.
func TestFooterCountsCrossTurnBackgroundChild(t *testing.T) {
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1", startBgSub("p1", "subagent-p1", "long audit"))

	// The turn the background child was started in ends; a new turn begins. The
	// child is still detached-running.
	for _, msg := range []interface{}{
		client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 900, OutputTokens: 100}},
		client.TurnStartMsg{Turn: 2},
	} {
		mm, _ := m.Update(msg)
		m = mm.(Model)
	}
	out := stripANSIstr(m.fitFooter(m.deps.Theme.Style("muted").Render("connected"), 160))
	if !strings.Contains(out, "1"+subagentRunGlyph+" 0"+subagentDoneGlyph) {
		t.Errorf("cross-turn background child must still count as running, got %q", out)
	}

	m = seedSubagents(m, "p2", endSub("p1", "subagent-p1", 9000, 1200, 4, "end_turn"))
	out = stripANSIstr(m.fitFooter(m.deps.Theme.Style("muted").Render("connected"), 160))
	if !strings.Contains(out, "0"+subagentRunGlyph+" 1"+subagentDoneGlyph) {
		t.Errorf("ended background child must count as done, got %q", out)
	}
}
