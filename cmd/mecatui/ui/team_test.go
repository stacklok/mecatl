package ui

// Tests for inline agent-team visibility: a Team tool card renders a BOUNDED
// per-member projection of the team's run (a team header, one calm lane line per
// member while live, per-member message/tool traces when expanded, and a resolved
// stat line with rounds + summed usage + stop reason). Member content is bounded
// server-side; the lanes are attributed to the Team card by ParentCallID and
// never enter the parent conversation.

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// roster is a small two-member roster (a lead + a read-only scout) reused across
// the team tests.
func roster() []client.TeamMemberSpec {
	return []client.TeamMemberSpec{
		{Name: "lead", Role: "coordinator", Lead: true, Mutating: true},
		{Name: "scout", Role: "researcher", Mutating: false},
	}
}

// teamCard builds a Team tool block, applies the given team.* projection via the
// conversation accumulators (by ParentCallID == toolID), and renders it.
func teamCard(t *testing.T, expand bool, build func(c *conversation)) string {
	t.Helper()
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("t1", "Team", `{"goal":"ship the feature"}`)
	build(c)
	return stripANSIstr(r.renderBlock(0, &c.blocks[0], expand))
}

// member builds a TeamMember msg for the canonical team t1.
func member(name, inner string, m client.TeamMsg) client.TeamMsg {
	m.Kind = client.TeamMember
	m.ParentCallID = "t1"
	m.Member = name
	m.InnerKind = inner
	return m
}

// TestTeamStartInitializesLanes asserts team.start seeds one lane per roster
// member, in roster order, carrying the lead/mutating flags.
func TestTeamStartInitializesLanes(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	if !c.setTeamStart("t1", "", roster()) {
		t.Fatal("setTeamStart should attribute to the Team card")
	}
	lanes := c.blocks[0].teamLanes
	if len(lanes) != 2 {
		t.Fatalf("want 2 lanes, got %d", len(lanes))
	}
	if lanes[0].name != "lead" || !lanes[0].lead || !lanes[0].mutating {
		t.Errorf("lead lane mis-seeded: %+v", lanes[0])
	}
	if lanes[1].name != "scout" || lanes[1].lead || lanes[1].mutating {
		t.Errorf("scout lane mis-seeded: %+v", lanes[1])
	}
}

// TestTeamMemberRoutesByName asserts team.member events accumulate into the lane
// named by Member: tools bump the lane count and set the current tool, turn.end
// adds usage, and they never bleed across members.
func TestTeamMemberRoutesByName(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())

	c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	c.addTeamMember(member("scout", "tool.result", client.TeamMsg{ToolName: "Grep", IsError: false}))
	c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 200, OutputTokens: 30}}))
	c.addTeamMember(member("lead", "tool.call", client.TeamMsg{ToolName: "Edit"}))

	lead := c.blocks[0].teamLanes[0]
	scout := c.blocks[0].teamLanes[1]
	if scout.toolCount != 1 || scout.current != "Grep" {
		t.Errorf("scout lane: count=%d current=%q, want 1/Grep", scout.toolCount, scout.current)
	}
	if scout.usage.InputTokens != 200 || scout.usage.OutputTokens != 30 {
		t.Errorf("scout usage = %+v, want 200/30", scout.usage)
	}
	if lead.toolCount != 1 || lead.current != "Edit" {
		t.Errorf("lead lane: count=%d current=%q, want 1/Edit", lead.toolCount, lead.current)
	}
}

// TestTeamLiveCollapsed asserts the default live card: a team header with the
// member count + ctrl+t affordance, and one calm lane line per member naming the
// current tool/state and token totals — with the lead tagged.
func TestTeamLiveCollapsed(t *testing.T) {
	out := teamCard(t, false, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
		c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 1200, OutputTokens: 80}}))
	})
	if !strings.Contains(out, "team ·") || !strings.Contains(out, "2 members") {
		t.Errorf("live card should show a team header with the member count, got %q", out)
	}
	if !strings.Contains(out, "ctrl+t trace") {
		t.Errorf("live card should advertise the ctrl+t trace, got %q", out)
	}
	if !strings.Contains(out, "[lead]") {
		t.Errorf("lead member should be tagged [lead], got %q", out)
	}
	// The current tool fills the state column with a "…" in-flight heartbeat.
	if !strings.Contains(out, "Grep…") {
		t.Errorf("scout lane should name the current tool with a heartbeat, got %q", out)
	}
	if !strings.Contains(out, "↑1.2K") {
		t.Errorf("scout lane should show running token totals, got %q", out)
	}
	// The mutating lead carries the persistent "✎" cue; the read-only scout the "·".
	if !strings.Contains(out, "✎ lead") {
		t.Errorf("mutating lead should carry the persistent ✎ cue, got %q", out)
	}
}

// TestTeamLeadAnchoredFirst asserts the lead lane renders at the TOP even when the
// server sent it last in the roster, without mutating teamLanes order.
func TestTeamLeadAnchoredFirst(t *testing.T) {
	leadLast := []client.TeamMemberSpec{
		{Name: "scout", Mutating: false},
		{Name: "builder", Mutating: true},
		{Name: "lead", Lead: true, Mutating: true},
	}
	out := teamCard(t, false, func(c *conversation) {
		c.setTeamStart("t1", "", leadLast)
	})
	lines := strings.Split(out, "\n")
	var laneLines []string
	for _, ln := range lines {
		if strings.Contains(ln, "◆") || strings.Contains(ln, "○") {
			laneLines = append(laneLines, ln)
		}
	}
	if len(laneLines) < 1 || !strings.Contains(laneLines[0], "lead") {
		t.Errorf("lead lane should render first, got lane lines %q", laneLines)
	}
}

// TestTeamLaneOrderDoesNotMutate asserts teamLaneOrder anchors the lead first via
// an index slice without reordering the underlying teamLanes (routing-by-name
// stability).
func TestTeamLaneOrderDoesNotMutate(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", []client.TeamMemberSpec{
		{Name: "scout"}, {Name: "lead", Lead: true},
	})
	order := teamLaneOrder(c.blocks[0].teamLanes)
	if order[0] != 1 {
		t.Errorf("lead (index 1) should sort to the front, got order %v", order)
	}
	if c.blocks[0].teamLanes[0].name != "scout" || c.blocks[0].teamLanes[1].name != "lead" {
		t.Errorf("teamLanes order must not be mutated, got %+v", c.blocks[0].teamLanes)
	}
}

// TestTeamLaneStateMapping asserts the three-state label/glyph mapping: WORKING
// (◆, tool-name/working… with a heartbeat), IDLE (○, "idle", no heartbeat, finished
// its round), and DONE (✓, "done", terminal — driven by teamDone, never a per-round
// result). Terminal wins over both idle and working.
func TestTeamLaneStateMapping(t *testing.T) {
	// working: tool active, live team → heartbeat on the tool name.
	if got := teamLaneState(&teamLane{current: "Grep"}, false); got != "Grep…" {
		t.Errorf("working member state = %q, want Grep…", got)
	}
	// working: live, no tool yet → "working…".
	if got := teamLaneState(&teamLane{}, false); got != "working…" {
		t.Errorf("live-no-tool member state = %q, want working…", got)
	}
	// idle: finished its round → "idle", no heartbeat, no stale tool name.
	if got := teamLaneState(&teamLane{idle: true, current: "Grep"}, false); got != "idle" {
		t.Errorf("idle member state = %q, want idle (no heartbeat)", got)
	}
	// terminal wins over idle.
	if got := teamLaneState(&teamLane{idle: true}, true); got != "done" {
		t.Errorf("terminal+idle member state = %q, want done", got)
	}
	// terminal wins over working.
	if got := teamLaneState(&teamLane{current: "Grep"}, true); got != "done" {
		t.Errorf("terminal+working member state = %q, want done", got)
	}
	// terminal + a RECOVERED run-level failure (issue #318): the member finished, so it
	// is not "stopped", but a bare "done" would contradict the supervisor's own report.
	if got := teamLaneState(&teamLane{errorRounds: 1}, true); got != "done (retried)" {
		t.Errorf("terminal+retried member state = %q, want %q", got, "done (retried)")
	}
	// A STOPPED lane keeps its stopped label even with error rounds — the stopped arm is
	// more specific and must win, or a benched member would read as one that recovered.
	if got := teamLaneState(&teamLane{stopped: true, stopReason: "error", errorRounds: 2}, true); got != "stopped — error" {
		t.Errorf("terminal+stopped+retried member state = %q, want %q", got, "stopped — error")
	}

	// glyph parity: ◆ working / ○ idle / ✓ terminal.
	if got := teamGlyph(&teamLane{}, false); got != "◆" {
		t.Errorf("working glyph = %q, want ◆", got)
	}
	if got := teamGlyph(&teamLane{idle: true}, false); got != "○" {
		t.Errorf("idle glyph = %q, want ○", got)
	}
	if got := teamGlyph(&teamLane{}, true); got != "✓" {
		t.Errorf("terminal glyph = %q, want ✓", got)
	}
}

// TestTeamLaneIdleResetsOnActivity drives one member through a full round cycle on a
// real conversation: a per-round result marks the lane IDLE (not terminal), and any
// forward activity (message.delta / tool.call / turn.end) on the next round CLEARS
// idle so it reads as working again. This is the fail-on-regression for the original
// bug (a permanent terminal flag set on a per-round result): with that bug, the lane
// would still read idle/done after step 4's delta and the assert fails.
func TestTeamLaneIdleResetsOnActivity(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())

	scout := func() *teamLane { return &c.blocks[0].teamLanes[1] }

	// round 0: starts working on a tool.
	c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
	if scout().idle {
		t.Fatal("after tool.call: lane must be working, got idle")
	}
	// round 0 result → idle, current cleared.
	c.addTeamMember(member("scout", "result", client.TeamMsg{}))
	if !scout().idle || scout().current != "" {
		t.Fatalf("after result: want idle && current=='', got idle=%v current=%q", scout().idle, scout().current)
	}
	// round 1 first activity = message.delta → idle cleared.
	c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "again"}))
	if scout().idle {
		t.Fatal("after message.delta on next round: idle must be cleared")
	}
	// round 1 result → idle again.
	c.addTeamMember(member("scout", "result", client.TeamMsg{}))
	if !scout().idle {
		t.Fatal("after round-1 result: want idle again")
	}
	// tool.call also clears idle.
	c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Read"}))
	if scout().idle {
		t.Fatal("tool.call must clear idle")
	}
	// back to idle, then turn.end clears it too.
	c.addTeamMember(member("scout", "result", client.TeamMsg{}))
	if !scout().idle {
		t.Fatal("want idle before turn.end check")
	}
	c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 10}}))
	if scout().idle {
		t.Fatal("turn.end must clear idle")
	}
}

// TestTeamLaneCapRollup asserts that with more than maxTeamLanes members, only
// maxTeamLanes lanes render and the overflow folds into a "· +K more" roll-up.
func TestTeamLaneCapRollup(t *testing.T) {
	var big []client.TeamMemberSpec
	big = append(big, client.TeamMemberSpec{Name: "lead", Lead: true})
	for i := 0; i < maxTeamLanes+3; i++ {
		big = append(big, client.TeamMemberSpec{Name: "m" + string(rune('a'+i))})
	}
	out := teamCard(t, false, func(c *conversation) {
		c.setTeamStart("t1", "", big)
	})
	laneLines := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "◆") || strings.Contains(ln, "○") {
			laneLines++
		}
	}
	if laneLines != maxTeamLanes {
		t.Errorf("want %d lane lines (capped), got %d: %q", maxTeamLanes, laneLines, out)
	}
	// total = lead + maxTeamLanes+3 members = maxTeamLanes+4; shown maxTeamLanes →
	// roll-up of (maxTeamLanes+4 - maxTeamLanes) = 4.
	if !strings.Contains(out, "+4 more") {
		t.Errorf("overflow should roll up into a '+K more' line, got %q", out)
	}
}

// TestTeamLiveNoFlicker asserts the collapsed lane line is monotonic — a later
// turn.end never decreases the displayed token totals (counts accumulate).
func TestTeamLiveNoFlicker(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())
	c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 100}}))
	c.addTeamMember(member("scout", "turn.end", client.TeamMsg{Usage: client.Usage{InputTokens: 50}}))
	if got := c.blocks[0].teamLanes[1].usage.InputTokens; got != 150 {
		t.Errorf("lane usage should accumulate monotonically, got %d want 150", got)
	}
}

// TestTeamExpandedTrace asserts the expanded (ctrl+t) card shows, per member, the
// forwarded message lines (clamped) and tool chips with ✓/✗ glyphs.
func TestTeamExpandedTrace(t *testing.T) {
	out := teamCard(t, true, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "searching for the bug"}))
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep", Detail: "pattern: handleErr"}))
		c.addTeamMember(member("scout", "tool.result", client.TeamMsg{ToolName: "Grep", Detail: "3 matches in dispatch.go"}))
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Read"}))
		c.addTeamMember(member("scout", "tool.result", client.TeamMsg{ToolName: "Read", IsError: true}))
	})
	if !strings.Contains(out, "searching for the bug") {
		t.Errorf("expanded card should show the member message line, got %q", out)
	}
	if !strings.Contains(out, "Grep") || !strings.Contains(out, "Read") {
		t.Errorf("expanded card should show member tool chips, got %q", out)
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "✗") {
		t.Errorf("expanded chips should carry ok/error glyphs, got %q", out)
	}
	// The bounded result preview (Detail) renders next to the chip; the result
	// preview supersedes the call's arg preview.
	if !strings.Contains(out, "3 matches in dispatch.go") {
		t.Errorf("expanded chip should render its Detail preview, got %q", out)
	}
}

// TestTeamExpandedSeparatesMembers asserts the expanded view inserts a blank line
// between members' trace blocks so boundaries are clear at 3+ members.
func TestTeamExpandedSeparatesMembers(t *testing.T) {
	out := teamCard(t, true, func(c *conversation) {
		c.setTeamStart("t1", "", []client.TeamMemberSpec{
			{Name: "lead", Lead: true, Mutating: true},
			{Name: "scout"},
			{Name: "builder", Mutating: true},
		})
		c.addTeamMember(member("lead", "message.delta", client.TeamMsg{Text: "planning"}))
		c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "scanning"}))
		c.addTeamMember(member("builder", "message.delta", client.TeamMsg{Text: "editing"}))
	})
	// Each member's trace block is preceded by a blank line (after the first). The
	// card border pads every line, so a separator line is whitespace-only between
	// two content lines — detect a blank (or whitespace-only) line that is not the
	// card's top/bottom border.
	lines := strings.Split(out, "\n")
	separators := 0
	for i := 1; i < len(lines)-1; i++ {
		if strings.TrimSpace(strings.Trim(lines[i], "│ ")) == "" &&
			strings.Contains(lines[i-1], "│") && strings.Contains(lines[i+1], "│") {
			separators++
		}
	}
	if separators < 2 {
		t.Errorf("expanded view should separate the 3 members with blank lines, got %d: %q", separators, out)
	}
}

// TestTeamExpandedCapsTrace asserts a single member lane's trace is capped at
// maxTeamTrace, dropping the oldest entries.
func TestTeamExpandedCapsTrace(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())
	for i := 0; i < maxTeamTrace+5; i++ {
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Read"}))
	}
	if got := len(c.blocks[0].teamLanes[1].trace); got != maxTeamTrace {
		t.Errorf("lane trace should cap at %d, got %d", maxTeamTrace, got)
	}
}

// TestTeamMessageCoalesces asserts consecutive message.delta fragments merge onto
// one trace line rather than producing a storm of entries.
func TestTeamMessageCoalesces(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	c.setTeamStart("t1", "", roster())
	c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "look"}))
	c.addTeamMember(member("scout", "message.delta", client.TeamMsg{Text: "ing"}))
	tr := c.blocks[0].teamLanes[1].trace
	if len(tr) != 1 || tr[0].text != "looking" {
		t.Errorf("message deltas should coalesce, got %+v", tr)
	}
}

// TestTeamResolved asserts the resolved card: a muted stat line with the round
// count, summed team usage, and the human stop label, plus the Team tool's joined
// summary rendered via the normal result body path.
func TestTeamResolved(t *testing.T) {
	out := teamCard(t, false, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.addTeamMember(member("scout", "tool.call", client.TeamMsg{ToolName: "Grep"}))
		c.setTeamEnd("t1", "", 4, "end_turn", client.Usage{InputTokens: 5200, OutputTokens: 410}, nil)
		c.resolveTool("t1", "team shipped the feature", false)
	})
	if !strings.Contains(out, "4 rounds") {
		t.Errorf("resolved card should show the round count, got %q", out)
	}
	if !strings.Contains(out, "stop:done") {
		t.Errorf("resolved card should map end_turn → stop:done, got %q", out)
	}
	if !strings.Contains(out, "↑5.2K") {
		t.Errorf("resolved card should show summed team usage, got %q", out)
	}
	if !strings.Contains(out, "team shipped the feature") {
		t.Errorf("resolved card should render the joined summary via the result body, got %q", out)
	}
}

// TestTeamErrorResolves asserts a Team tool error resolves the card with a "✗"
// glyph and stop:error, with the error text in the result slot.
func TestTeamErrorResolves(t *testing.T) {
	out := teamCard(t, false, func(c *conversation) {
		c.setTeamStart("t1", "", roster())
		c.setTeamEnd("t1", "", 1, "error", client.Usage{}, nil)
		c.resolveTool("t1", "Team: the run failed", true)
	})
	if !strings.Contains(out, "✗") {
		t.Errorf("errored card should carry the error glyph, got %q", out)
	}
	if !strings.Contains(out, "stop:error") {
		t.Errorf("errored card should show stop:error, got %q", out)
	}
	if !strings.Contains(out, "the run failed") {
		t.Errorf("errored card should render the error text, got %q", out)
	}
}

// TestTeamManyMembersLegible asserts a 4-member team renders one lane line per
// member (legible, no truncation of the roster) with each name present.
func TestTeamManyMembersLegible(t *testing.T) {
	big := []client.TeamMemberSpec{
		{Name: "lead", Lead: true, Mutating: true},
		{Name: "scout", Mutating: false},
		{Name: "builder", Mutating: true},
		{Name: "tester", Mutating: true},
	}
	out := teamCard(t, false, func(c *conversation) {
		c.setTeamStart("t1", "", big)
		c.addTeamMember(member("builder", "tool.call", client.TeamMsg{ToolName: "Write"}))
	})
	for _, name := range []string{"lead", "scout", "builder", "tester"} {
		if !strings.Contains(out, name) {
			t.Errorf("member %q lane missing from card, got %q", name, out)
		}
	}
	if !strings.Contains(out, "4 members") {
		t.Errorf("header should report 4 members, got %q", out)
	}
}

// TestTeamAttributionByParentCallID asserts team.* events are attributed to the
// correct Team card by ParentCallID, even with two Team cards interleaved.
func TestTeamAttributionByParentCallID(t *testing.T) {
	c := &conversation{}
	c.addTool("ta", "Team", `{}`)
	c.addTool("tb", "Team", `{}`)
	if !c.setTeamStart("ta", "", []client.TeamMemberSpec{{Name: "a1"}}) ||
		!c.setTeamStart("tb", "", []client.TeamMemberSpec{{Name: "b1"}}) {
		t.Fatal("both starts should attribute")
	}
	c.addTeamMember(client.TeamMsg{Kind: client.TeamMember, ParentCallID: "ta", Member: "a1", InnerKind: "tool.call", ToolName: "Grep"})
	c.addTeamMember(client.TeamMsg{Kind: client.TeamMember, ParentCallID: "tb", Member: "b1", InnerKind: "tool.call", ToolName: "Read"})

	if c.blocks[0].teamLanes[0].current != "Grep" {
		t.Errorf("card ta mis-attributed: %+v", c.blocks[0].teamLanes[0])
	}
	if c.blocks[1].teamLanes[0].current != "Read" {
		t.Errorf("card tb mis-attributed: %+v", c.blocks[1].teamLanes[0])
	}
}

// TestTeamMissAttributionIsSafe asserts a team.* event with no matching Team card
// is silently dropped (no panic, returns false).
func TestTeamMissAttributionIsSafe(t *testing.T) {
	c := &conversation{}
	if c.setTeamStart("nope", "", roster()) {
		t.Errorf("setTeamStart should miss when no Team card matches")
	}
	if c.addTeamMember(client.TeamMsg{Kind: client.TeamMember, ParentCallID: "nope", Member: "x"}) {
		t.Errorf("addTeamMember should miss when no Team card matches")
	}
	if c.setTeamEnd("nope", "", 0, "end_turn", client.Usage{}, nil) {
		t.Errorf("setTeamEnd should miss when no Team card matches")
	}
}

// TestTeamMemberCreatesLaneWhenRosterMissed asserts a team.member event for an
// unlisted member creates a lane rather than dropping the event (defensive against
// a missed/partial team.start).
func TestTeamMemberCreatesLaneWhenRosterMissed(t *testing.T) {
	c := &conversation{}
	c.addTool("t1", "Team", `{}`)
	// No setTeamStart — the member arrives "cold".
	if !c.addTeamMember(member("ghost", "tool.call", client.TeamMsg{ToolName: "Glob"})) {
		t.Fatal("addTeamMember should attribute to the Team card even without a roster")
	}
	lanes := c.blocks[0].teamLanes
	if len(lanes) != 1 || lanes[0].name != "ghost" || lanes[0].current != "Glob" {
		t.Errorf("a cold member should create its own lane, got %+v", lanes)
	}
}
