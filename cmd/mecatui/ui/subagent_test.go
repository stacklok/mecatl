package ui

// Tests for inline Subagent tool visibility: a Subagent tool card renders the
// BOUNDED projection of its child run (goal title, live counts + current tool,
// expanded Team-format chips with bounded previews under a "bounded previews"
// honesty note, and a resolved stat line with stop reason). The previews are
// bounded, scrubbed, client-only — they never enter the parent conversation
// (ADR 0079; gauntlet #7).

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// subagentCard builds a Subagent tool block, applies the given subagent.* projection
// via the conversation accumulators (by ParentCallID == toolID), and renders it.
func subagentCard(t *testing.T, expand bool, build func(c *conversation)) string {
	t.Helper()
	r := newTestRenderer()
	c := &conversation{}
	c.addTool("p1", "Subagent", `{"prompt":"investigate the loop"}`)
	build(c)
	return stripANSIstr(r.renderBlock(0, &c.blocks[0], expand))
}

// addSubTool routes a bare (kind-less) subagent.tool event into the card — the
// pre-ADR-0079 shape, where a tool chip is appended with no preview. Returns the
// accumulator's match bool so the miss-attribution test can assert it.
func addSubTool(c *conversation, parentCallID, toolName string, isError bool, toolCount int) bool {
	return c.addSubagentTool(client.SubagentMsg{
		Kind: client.SubagentTool, ParentCallID: parentCallID,
		ToolName: toolName, IsError: isError, ToolCount: toolCount,
	})
}

// TestSubagentLiveCollapsed asserts the default (collapsed, unresolved) card: the
// goal title, a calm status line (LATEST child tool name + tokens + tool count)
// with the ctrl+t trace affordance — and no heartbeat ticker (ADR 0079: the line
// changes only when the tool actually changes).
func TestSubagentLiveCollapsed(t *testing.T) {
	out := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "", "")
		addSubTool(c, "p1", "Grep", false, 1)
		addSubTool(c, "p1", "Read", false, 2)
	})
	if !strings.Contains(out, "investigate the loop") {
		t.Errorf("live card should show the goal title, got %q", out)
	}
	if !strings.Contains(out, "subagent · Read ·") || !strings.Contains(out, "2 tools") {
		t.Errorf("live card should show the latest tool name + counts, got %q", out)
	}
	if !strings.Contains(out, "ctrl+t trace") {
		t.Errorf("live card should advertise the ctrl+t trace, got %q", out)
	}
}

// TestSubagentExpandedChips asserts the expanded (ctrl+t) card: glyph + name chips
// for each child tool in the Team trace format, under the "bounded previews"
// honesty note (ADR 0079).
func TestSubagentExpandedChips(t *testing.T) {
	out := subagentCard(t, true, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "", "")
		addSubTool(c, "p1", "Grep", false, 1)
		addSubTool(c, "p1", "Read", true, 2)
	})
	if !strings.Contains(out, "bounded previews") {
		t.Errorf("expanded card should carry the bounded-previews note, got %q", out)
	}
	if !strings.Contains(out, "Grep") || !strings.Contains(out, "Read") {
		t.Errorf("expanded card should show child tool chips, got %q", out)
	}
	if !strings.Contains(out, "✓") || !strings.Contains(out, "✗") {
		t.Errorf("expanded chips should carry ok/error glyphs, got %q", out)
	}
}

// TestSubagentExpandedCapsTrace asserts the expanded chip trace is capped at
// maxSubagentTrace, dropping the oldest chips.
func TestSubagentExpandedCapsTrace(t *testing.T) {
	out := subagentCard(t, true, func(c *conversation) {
		c.setSubagentStart("p1", "big investigation", "", "", "", "")
		for i := 0; i < maxSubagentTrace+5; i++ {
			addSubTool(c, "p1", "Read", false, i+1)
		}
	})
	// The trace itself caps at maxSubagentTrace chips. Count the glyphs as a proxy.
	if got := strings.Count(out, "✓"); got != maxSubagentTrace {
		t.Errorf("expanded trace should cap at %d chips, got %d (%q)", maxSubagentTrace, got, out)
	}
}

// TestWrapChips asserts the expanded chip row wraps BETWEEN chips at the content
// width (visible-width measured) and never splits a chip: each output line stays
// within width, and every chip survives intact across the wrap.
func TestWrapChips(t *testing.T) {
	// Plain chips (no ANSI) so the width math is easy to reason about: each "✓ Read"
	// has visible width 6; with the 2-space separator, two chips + sep = 14.
	chips := []string{"✓ Read", "✓ Grep", "✗ Read", "✓ Glob", "✓ Read"}

	// Width 14 fits exactly two chips per line (6 + 2 + 6 = 14).
	out := wrapChips(chips, 14)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 { // 2 + 2 + 1
		t.Fatalf("want 3 wrapped lines at width 14, got %d: %q", len(lines), lines)
	}
	for _, ln := range lines {
		if w := lipgloss.Width(ln); w > 14 {
			t.Errorf("line exceeds width 14 (got %d): %q", w, ln)
		}
	}
	// Every chip survives intact (no mid-chip split).
	for _, chip := range chips {
		if !strings.Contains(out, chip) {
			t.Errorf("chip %q was split or dropped by wrapping: %q", chip, out)
		}
	}

	// Width 0 disables wrapping: a single row joined by the separator.
	if got := wrapChips(chips, 0); strings.Contains(got, "\n") {
		t.Errorf("width 0 should not wrap, got %q", got)
	}
}

// TestSubagentExpandedWrapsNarrow asserts that, rendered into a narrow card, the
// expanded chip trace spans multiple lines (it wraps) and no rendered line is
// absurdly long — exercising the full renderTool → renderSubagent → wrapChips path.
func TestSubagentExpandedWrapsNarrow(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(24) // narrow card
	c := &conversation{}
	c.addTool("p1", "Subagent", `{"prompt":"x"}`)
	c.setSubagentStart("p1", "narrow", "", "", "", "")
	for i := 0; i < 6; i++ {
		addSubTool(c, "p1", "Read", false, i+1)
	}
	out := stripANSIstr(r.renderBlock(0, &c.blocks[0], true))
	// The chip region must occupy more than one visual line (it wrapped).
	if strings.Count(out, "✓") != 6 {
		t.Fatalf("want all 6 chips present, got %q", out)
	}
	// Find the chip lines and confirm at least two of them carry chips.
	chipLines := 0
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "✓ Read") {
			chipLines++
		}
	}
	if chipLines < 2 {
		t.Errorf("expected the chip row to wrap across >=2 lines in a narrow card, got %d: %q", chipLines, out)
	}
}

// TestSubagentResolved asserts the resolved card: a single muted stat line with
// duration, token totals, final tool count, and the human stop label — no
// "0 writes", and the child's summary renders via the normal result body path.
func TestSubagentResolved(t *testing.T) {
	out := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "", "")
		addSubTool(c, "p1", "Grep", false, 1)
		c.setSubagentEnd("p1", client.Usage{InputTokens: 1200, OutputTokens: 80}, 4, "end_turn", 2500)
		c.resolveTool("p1", "found the bug in dispatch.go", false)
	})
	if !strings.Contains(out, "stop:done") {
		t.Errorf("resolved card should map end_turn → stop:done, got %q", out)
	}
	if !strings.Contains(out, "4 tools") {
		t.Errorf("resolved card should show the final tool count, got %q", out)
	}
	if !strings.Contains(out, "2.5s") {
		t.Errorf("resolved card should show a human duration, got %q", out)
	}
	if strings.Contains(out, "0 writes") {
		t.Errorf("resolved card must not show a writes count, got %q", out)
	}
	if !strings.Contains(out, "found the bug in dispatch.go") {
		t.Errorf("resolved card should render the child summary via the result body, got %q", out)
	}
}

// TestSubagentErrorResolves asserts a child error resolves the Subagent card with a
// "✗" glyph, stop:error, and the error text in the result slot.
func TestSubagentErrorResolves(t *testing.T) {
	out := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "", "")
		c.setSubagentEnd("p1", client.Usage{}, 0, "error", 100)
		// The server-composed StopError body shape (subagentErrorBody's floor, which is
		// caller-neutral: "Subagent: " + "failed without producing a summary").
		c.resolveTool("p1", "Subagent: failed without producing a summary", true)
	})
	if !strings.Contains(out, "✗") {
		t.Errorf("errored card should carry the error glyph, got %q", out)
	}
	if !strings.Contains(out, "stop:error") {
		t.Errorf("errored card should show stop:error, got %q", out)
	}
	if !strings.Contains(out, "failed without producing a summary") {
		t.Errorf("errored card should render the error text, got %q", out)
	}
}

// TestSubagentAttributionByParentCallID asserts subagent.* events are attributed
// to the correct Subagent card by ParentCallID, even with two Subagent cards interleaved.
func TestSubagentAttributionByParentCallID(t *testing.T) {
	c := &conversation{}
	c.addTool("pa", "Subagent", `{"prompt":"alpha"}`)
	c.addTool("pb", "Subagent", `{"prompt":"bravo"}`)

	if !c.setSubagentStart("pa", "alpha goal", "", "", "", "") || !c.setSubagentStart("pb", "bravo goal", "", "", "", "") {
		t.Fatalf("both starts should attribute")
	}
	addSubTool(c, "pa", "Grep", false, 1)
	addSubTool(c, "pb", "Read", false, 1)

	if c.blocks[0].subGoal != "alpha goal" || c.blocks[0].subTrace[0].name != "Grep" {
		t.Errorf("card pa mis-attributed: goal=%q trace=%+v", c.blocks[0].subGoal, c.blocks[0].subTrace)
	}
	if c.blocks[1].subGoal != "bravo goal" || c.blocks[1].subTrace[0].name != "Read" {
		t.Errorf("card pb mis-attributed: goal=%q trace=%+v", c.blocks[1].subGoal, c.blocks[1].subTrace)
	}
}

// TestSubagentMissAttributionIsSafe asserts a subagent.* event with no matching
// Subagent card is silently dropped (no panic, returns false).
func TestSubagentMissAttributionIsSafe(t *testing.T) {
	c := &conversation{}
	if c.setSubagentStart("nope", "goal", "", "", "", "") {
		t.Errorf("setSubagentStart should miss when no Subagent card matches")
	}
	if addSubTool(c, "nope", "Read", false, 1) {
		t.Errorf("addSubagentTool should miss when no Subagent card matches")
	}
	if c.setSubagentEnd("nope", client.Usage{}, 0, "end_turn", 0) {
		t.Errorf("setSubagentEnd should miss when no Subagent card matches")
	}
}

// TestSubagentRoutedMetadataSurfaced asserts the opt-in model router's bare
// metadata (a category label + a model id, ADR 0031) surfaces on the inline
// Subagent card as a muted "routed: <category> → <model>" line — and is absent
// when the child was not routed. It carries no child content (gauntlet #7).
func TestSubagentRoutedMetadataSurfaced(t *testing.T) {
	out := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "small", "openai/gpt-4.1-mini", "", "openai/gpt-4.1-mini")
		addSubTool(c, "p1", "Grep", false, 1)
	})
	if !strings.Contains(out, "routed: small → openai/gpt-4.1-mini") {
		t.Errorf("routed card should show the routed-model line, got %q", out)
	}

	// An unrouted child (empty category/model) must NOT render the routed line.
	unrouted := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "", "")
		addSubTool(c, "p1", "Grep", false, 1)
	})
	if strings.Contains(unrouted, "routed:") {
		t.Errorf("unrouted card must not show a routed line, got %q", unrouted)
	}
}

// TestSubagentModelMetadataSurfaced asserts the generic model surface (issue #112 /
// ADR 0035) renders as a muted "model: <model>" line for the common non-routed cases
// (inherited default, agent-def pin, per-call override) — and that when the router
// fired, the routed cue is shown instead (not duplicated as a model: line). Bare
// metadata only, gauntlet #7.
func TestSubagentModelMetadataSurfaced(t *testing.T) {
	// Inherited / default model (no router): the plain "model:" cue is shown.
	inherited := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "", "openai/gpt-4.5")
		addSubTool(c, "p1", "Grep", false, 1)
	})
	if !strings.Contains(inherited, "model: openai/gpt-4.5") {
		t.Errorf("inherited-model card should show the model: line, got %q", inherited)
	}
	if strings.Contains(inherited, "routed:") {
		t.Errorf("inherited-model card must not show a routed line, got %q", inherited)
	}
	// Routed child (model == routedModel): the routed cue is shown, NOT duplicated as model:.
	routed := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "small", "openai/gpt-4.1-mini", "", "openai/gpt-4.1-mini")
		addSubTool(c, "p1", "Grep", false, 1)
	})
	if !strings.Contains(routed, "routed: small → openai/gpt-4.1-mini") {
		t.Errorf("routed card should show the routed cue, got %q", routed)
	}
	if strings.Contains(routed, "model: openai/gpt-4.1-mini") {
		t.Errorf("routed card must not ALSO show a model: line (model==routedModel), got %q", routed)
	}
}

// TestSubagentFleetRoutedMetadata asserts the routed metadata surfaces on a fleet
// roster row (the ctrl+a Subagents tab), so a routed delegation is identifiable
// there too — not only on the inline card.
func TestSubagentFleetRoutedMetadata(t *testing.T) {
	c := &conversation{}
	c.fleetStart("c1", "audit auth", "large", "openai/gpt-4.1", "", "openai/gpt-4.1", false)
	ln := subagentRosterLine(&c.subagentFleet[0])
	if !strings.Contains(ln, "routed: large → openai/gpt-4.1") {
		t.Errorf("fleet roster row should carry the routed line, got %q", ln)
	}
	// Plain (non-routed) inherited model: the roster row shows "model: <id>" (issue #112).
	c.fleetStart("c2", "map coverage", "", "", "", "openai/gpt-4.5", false)
	plain := subagentRosterLine(&c.subagentFleet[1])
	if !strings.Contains(plain, "model: openai/gpt-4.5") {
		t.Errorf("fleet roster row should carry the plain model: line, got %q", plain)
	}
	if strings.Contains(plain, "routed:") {
		t.Errorf("non-routed fleet row must not show a routed line, got %q", plain)
	}
}

// TestSubagentRoutingReasonSurfaced asserts the routing-miss reason (issue #397 /
// ADR 0083) surfaces on the inline Subagent card and the fleet roster row, so a
// router-off / pinned / breaker-open delegation is distinguishable from a routed
// one — the distinction the feature exists to expose. Bare metadata, gauntlet #7.
func TestSubagentRoutingReasonSurfaced(t *testing.T) {
	// Router off (no router wired): the card shows the inherited model + the reason.
	off := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "router-disabled", "openai/gpt-4.5")
		addSubTool(c, "p1", "Grep", false, 1)
	})
	if !strings.Contains(off, "model: openai/gpt-4.5 · not routed: router-disabled") {
		t.Errorf("router-off card should show the miss reason, got %q", off)
	}
	// A routed hit carries an EMPTY reason: no "not routed" annotation leaks through.
	routed := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "small", "openai/gpt-4.1-mini", "", "openai/gpt-4.1-mini")
		addSubTool(c, "p1", "Grep", false, 1)
	})
	if strings.Contains(routed, "not routed:") {
		t.Errorf("routed card must not show a miss reason, got %q", routed)
	}
	// Fleet roster row: the reason rides the plain-model line there too.
	c := &conversation{}
	c.fleetStart("c1", "audit auth", "", "", "pinned-model", "openai/gpt-4.5", false)
	ln := subagentRosterLine(&c.subagentFleet[0])
	if !strings.Contains(ln, "model: openai/gpt-4.5 · not routed: pinned-model") {
		t.Errorf("fleet roster row should carry the miss reason, got %q", ln)
	}
}
