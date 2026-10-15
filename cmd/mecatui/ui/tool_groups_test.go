package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestToolPresentationGroupsSettledAndMixed(t *testing.T) {
	m := scenario2Model(t, 100, 24, false, phaseRunning, nil)
	for i, name := range []string{"Read", "Shell", "Grep", "Glob"} {
		id := name
		m = applyAll(m, client.ToolCallMsg{ID: id, Name: name, Args: `{"path":"file.go"}`}, client.ToolResultMsg{CallID: id, Content: "PRIVATE_RESULT", IsError: i == 1})
	}
	got := detailText(m)
	if !strings.Contains(got, "● 4 tools run · 1 failed") || !strings.Contains(got, "f9") || !strings.Contains(got, "ctrl+t") {
		t.Fatalf("missing aggregate: %q", got)
	}
	styled := m.conversationContent()
	if !strings.Contains(styled, m.rend.th.Style("toolErr").Render(" · 1 failed")) || !strings.Contains(styled, m.rend.th.Style("transcriptToolHeading").Render(" 4 tools run")) {
		t.Fatal("aggregate lost tool accent or failure styling")
	}
	if strings.Contains(got, "PRIVATE_RESULT") || strings.Contains(got, "✓ Read") {
		t.Fatalf("hidden tool leaked: %q", got)
	}
	m, _ = pressKey(m, f9)
	detailed := detailText(m)
	if !strings.Contains(detailed, "✓ Read") || !strings.Contains(detailed, "✗ Shell") || strings.Contains(detailed, "PRIVATE_RESULT") || strings.Contains(detailed, "\n\n") {
		t.Fatalf("bad detailed group: %q", detailed)
	}
	m, _ = pressKey(m, ctrlT)
	if s := toolcallsForTest(t, m); len(s.entries) != 4 {
		t.Fatalf("inventory lost calls: %d", len(s.entries))
	}
}

func TestToolPresentationGroupsBoundariesAndSpacing(t *testing.T) {
	for _, boundary := range []struct {
		name           string
		add            func(*conversation)
		comfortVisible bool
	}{
		{"empty assistant", func(c *conversation) { c.startAssistant() }, false},
		{"hidden stats", func(c *conversation) { c.addTurnStat("stat boundary") }, false},
		{"user", func(c *conversation) { c.addUser("user boundary") }, true},
		{"assistant", func(c *conversation) { c.appendAssistant("assistant boundary") }, true},
		{"notice", func(c *conversation) { c.addNotice("notice boundary") }, true},
		{"error", func(c *conversation) { c.addError("error boundary") }, true},
		{"hook", func(c *conversation) { c.addHook("hook boundary", "before", "Read", "blocked") }, true},
		{"delivery", func(c *conversation) { c.addDelivery("schedule", "fire", "delivery boundary") }, true},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			c := &conversation{}
			c.addTool("one", "Read", `{}`)
			c.resolveTool("one", "PRIVATE_ONE", false)
			boundary.add(c)
			boundaryID := uint64(c.scrollback.MetadataAt(1).ID)
			c.addTool("two", "Read", `{}`)
			c.resolveTool("two", "PRIVATE_TWO", false)
			r := newCacheRenderer()
			first, second := toolBlockID(t, c.scrollback, "one"), toolBlockID(t, c.scrollback, "two")
			for _, detailed := range []bool{false, true} {
				frame := r.renderConversationFrame(&c.scrollback, detailed)
				plain := stripANSIstr(strings.Join(frame.lines, "\n"))
				visible := boundary.comfortVisible || (detailed && boundary.name == "hidden stats")
				if visible {
					assertLongRunGap(t, frame, first, boundaryID, 1)
					assertLongRunGap(t, frame, boundaryID, second, 1)
					if !detailed && strings.Count(plain, "● 1 tool run") != 2 {
						t.Fatalf("boundary failed to split: %q", plain)
					}
				} else if detailed {
					assertLongRunGap(t, frame, first, second, 0)
				} else if !strings.Contains(plain, "● 2 tools run") {
					t.Fatalf("hidden card split group: %q", plain)
				}
				if strings.Contains(plain, "PRIVATE_") {
					t.Fatal("settled payload leaked")
				}
				if got, want := strings.Join(frame.lines, "\n"), r.renderConversation(c, detailed); got != want {
					t.Fatalf("frame/string differ: %q != %q", got, want)
				}
			}
		})
	}
}

func TestToolPresentationGroupsWarmAppendAndVisibility(t *testing.T) {
	c := &conversation{}
	c.addUser("unrelated prefix")
	c.addTool("one", "Read", `{"path":"file.go"}`)
	c.resolveTool("one", strings.Repeat("PRIVATE_PAYLOAD", 10000), false, client.ContentBlock{Kind: client.ContentBlockImage, Data: []byte(strings.Repeat("artifact", 10000))})
	r := newCacheRenderer()
	check := func(step string, detailed bool) {
		t.Helper()
		got := strings.Join(r.renderConversationFrame(&c.scrollback, detailed).lines, "\n")
		fresh := newCacheRenderer()
		if want := strings.Join(fresh.renderConversationFrame(&c.scrollback, detailed).lines, "\n"); got != want {
			t.Fatalf("%s stale frame: %q != %q", step, got, want)
		}
		if want := r.renderConversation(c, detailed); got != want {
			t.Fatalf("%s string mismatch", step)
		}
		if strings.Contains(got, "PRIVATE_PAYLOAD") || strings.Contains(got, "artifact") {
			t.Fatalf("%s leaked payload", step)
		}
	}
	check("cold", false)
	check("warm", false)
	loads, prepares := r.snapshotLoads, r.cardPrepares
	check("steady", false)
	if r.snapshotLoads != loads || r.cardPrepares != prepares {
		t.Fatal("warm group cloned or prepared payloads")
	}
	c.startAssistant() // hidden insertion extends the cached tail group
	boundary := c.scrollback.Notices().AddGuardrailNotice("hidden boundary", true)
	check("hidden append", false)
	loads = r.snapshotLoads
	c.addTool("two", "Shell", `{"command":"false"}`)
	c.resolveTool("two", "PRIVATE_FAILURE", true)
	check("settled append", false)
	if r.snapshotLoads != loads+1 {
		t.Fatalf("append reloaded settled payloads: delta %d", r.snapshotLoads-loads)
	}
	c.addTool("three", "Grep", `{}`)
	c.startAssistant()
	check("pending non-tail", false)
	c.scrollback.Tools().Finish("three", true)
	check("non-tail lifecycle failure", false)
	got := stripANSIstr(strings.Join(r.renderConversationFrame(&c.scrollback, false).lines, "\n"))
	if !strings.Contains(got, "● 3 tools run · 2 failed") {
		t.Fatalf("late failure counts: %q", got)
	}
	c.scrollback.Notices().UpdateGuardrailNotice(boundary, "now visible", false)
	check("revealed boundary", false)
	c.scrollback.Notices().UpdateGuardrailNotice(boundary, "hidden again", true)
	check("hidden boundary again", false)
	check("F9", true)
	check("comfort", false)
	r.setWidth(28)
	fresh := newCacheRenderer()
	fresh.setWidth(28)
	if got, want := strings.Join(r.renderConversationFrame(&c.scrollback, false).lines, "\n"), strings.Join(fresh.renderConversationFrame(&c.scrollback, false).lines, "\n"); got != want {
		t.Fatal("width retained stale group")
	}
	r.resetBlockCaches()
	c = &conversation{}
	c.addTool("new", "Read", `{}`)
	c.resolveTool("new", "new result", false)
	r.setWidth(100)
	check("rebuilt conversation", false)
}

func TestToolPresentationGroupsDerivedSelectionAndMemberAnchors(t *testing.T) {
	c := &conversation{}
	c.addTool("one", "Read", `{"path":"one.go"}`)
	c.resolveTool("one", "PRIVATE_ONE", false)
	c.addTool("two", "Shell", `{"command":"false"}`)
	c.resolveTool("two", "PRIVATE_TWO", true)
	c.addTool("three", "Grep", `{}`)
	c.addUser("later user keeps reader away from tail")
	r := newCacheRenderer()
	frame := r.renderConversationFrame(&c.scrollback, false)
	summary := frame.toolSummaryRow(toolBlockID(t, c.scrollback, "two"))
	if summary < 0 || frame.provenance[summary].region != conversationRegionToolSummary {
		t.Fatal("hidden member lacks summary anchor")
	}
	memberAnchor := readingAnchor{blockID: toolBlockID(t, c.scrollback, "two"), region: conversationRegionBody, text: true}
	if got := (conversationView{}).restore(frame, memberAnchor); got != summary {
		t.Fatalf("member restored to %d, want summary %d", got, summary)
	}
	sel := selection{active: true, anchorL: summary, headL: summary, anchorC: 1, headC: graphemeCount(stripANSIstr(frame.lines[summary]))}
	if !sel.snapshotLogical(frame, strings.Join(frame.lines, "\n")) || !strings.HasPrefix(sel.copied, "● 2 tools run · 1 failed") || strings.Contains(sel.copied, "PRIVATE_") {
		t.Fatalf("summary copy not derived: %q", sel.copied)
	}
	unchanged := r.renderConversationFrame(&c.scrollback, false)
	if !sel.resolveLogical(unchanged) {
		t.Fatal("unchanged summary selection cleared")
	}
	detailed := r.renderConversationFrame(&c.scrollback, true)
	if sel.resolveLogical(detailed) {
		t.Fatal("hidden summary selection survived F9")
	}
	exact := detailed.firstRegionRow(memberAnchor.blockID, memberAnchor.region)
	if got := (conversationView{}).restore(detailed, memberAnchor); got != exact {
		t.Fatal("visible member did not retain exact anchor")
	}
	frame = r.renderConversationFrame(&c.scrollback, false)
	summary = frame.toolSummaryRow(memberAnchor.blockID)
	// Select only the hint; a changed count still invalidates the derived display.
	text := stripANSIstr(frame.lines[summary])
	at := strings.Index(text, "Toolcalls")
	sel = selection{active: true, anchorL: summary, headL: summary, anchorC: graphemeCount(text[:at]), headC: graphemeCount(text)}
	if !sel.snapshotLogical(frame, strings.Join(frame.lines, "\n")) {
		t.Fatal("hint selection failed")
	}
	c.scrollback.Tools().Finish("three", true)
	frame = r.renderConversationFrame(&c.scrollback, false)
	if sel.resolveLogical(frame) {
		t.Fatal("changed failure count retained stale selection")
	}
}

func TestToolPresentationGroupsAllFailedAndActiveOnly(t *testing.T) {
	var c scrollback.Conversation
	c.Tools().Add(scrollback.ToolCall{ID: "ordinary", Name: "Read"})
	c.Tools().Add(scrollback.ToolCall{ID: "subagent", Name: "Subagent"})
	c.Subagents().Start("subagent", scrollback.SubagentStart{})
	c.Tools().Add(scrollback.ToolCall{ID: "team", Name: "Team"})
	c.Teams().Start("team", scrollback.TeamStart{})
	r := newCacheRenderer()
	plain := stripANSIstr(strings.Join(r.renderConversationFrame(&c, false).lines, "\n"))
	if strings.Contains(plain, "tools run") || !strings.Contains(plain, "Read") || !strings.Contains(plain, "Subagent") || !strings.Contains(plain, "Team") {
		t.Fatalf("active-only group lost cards or invented aggregate: %q", plain)
	}
	c.Tools().Finish("ordinary", true)
	c.Subagents().Update("subagent", scrollback.SubagentUpdate{Done: true, Stop: "error"})
	c.Teams().Update("team", scrollback.TeamUpdate{Done: true, Stop: "error"})
	plain = stripANSIstr(strings.Join(r.renderConversationFrame(&c, false).lines, "\n"))
	if !strings.Contains(plain, "● 3 tools run · 3 failed") || strings.Contains(plain, "Read") || strings.Contains(plain, "\n\n") {
		t.Fatalf("recorded terminal failures not aggregated: %q", plain)
	}
}

func TestToolPresentationGroupsReboundHintsAndRouting(t *testing.T) {
	m := scenario2Model(t, 100, 24, false, phaseIdle, map[string][]string{"ExpandConversation": {"ctrl+f10"}, "Toolcalls": {"ctrl+f11"}})
	m.conv.addTool("one", "Read", `{"path":"file.go"}`)
	m.conv.resolveTool("one", "PRIVATE_RESULT", false)
	m.refreshView()
	before := detailText(m)
	if !strings.Contains(before, "ctrl+f10 detailed · ctrl+f11 Toolcalls") || strings.Contains(before, "f9") || strings.Contains(before, "ctrl+t") {
		t.Fatalf("unresolved hints: %q", before)
	}
	m, _ = pressKey(m, f9)
	if detailText(m) != before {
		t.Fatal("default detail key still active after rebind")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModCtrl})
	if got := detailText(m); !strings.Contains(got, "✓ Read") || strings.Contains(got, "PRIVATE_RESULT") {
		t.Fatalf("rebound detail did not restore per-call summary: %q", got)
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF11, Mod: tea.ModCtrl})
	s := toolcallsForTest(t, m)
	if len(s.entries) != 1 {
		t.Fatal("rebound inspector lost aggregate member")
	}
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "PRIVATE_RESULT") {
		t.Fatalf("inspector did not expose full result: %q", got)
	}
	detailed := m.expandConversation
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyF10, Mod: tea.ModCtrl})
	if m.expandConversation != detailed {
		t.Fatal("detail key escaped inspector ownership")
	}
}

func TestToolPresentationGroupsModelSelectionAndReading(t *testing.T) {
	m, _ := selModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	m.conv.addTool("one", "Read", `{"path":"one.go"}`)
	m.conv.resolveTool("one", "PRIVATE_ONE", false)
	m.conv.addTool("active", "Read", `{"path":"active.go"}`)
	m.conv.addTool("two", "Shell", `{"command":"false"}`)
	m.conv.resolveTool("two", "PRIVATE_TWO", true)
	m.conv.addUser(strings.Repeat("later prose\n", 40))
	m.refreshView()
	m, _ = pressKey(m, f9)
	memberID := toolBlockID(t, m.conv.scrollback, "two")
	line := m.conversationView.frame.firstRegionRow(memberID, conversationRegionBody)
	m.vp.SetYOffset(line)
	m.conversationView.mode = anchored
	m, _ = pressKey(m, f9)
	summary := m.conversationView.frame.toolSummaryRow(memberID)
	if m.vp.YOffset() != summary || m.conversationView.mode != anchored {
		t.Fatal("collapsed member jumped out of its group")
	}
	text := stripANSIstr(m.conversationView.frame.lines[summary])
	at := strings.Index(text, "Toolcalls")
	m = m.wordSelect(summary, graphemeCount(text[:at]))
	if !m.sel.active || m.sel.snapshot != "Toolcalls" {
		t.Fatalf("derived model selection: %q", m.sel.snapshot)
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 90, Height: 20})
	if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != "Toolcalls" {
		t.Fatal("unchanged summary selection lost on resize")
	}
	_, cmd := m.copySelection()
	if copied, ok := osc52Payload(collectLeaves(cmd)); !ok || copied != "Toolcalls" {
		t.Fatalf("summary copy=%q ok=%t", copied, ok)
	}
	m.conv.resolveTool("active", "PRIVATE_ACTIVE", false)
	m.refreshView()
	if m.sel.active {
		t.Fatal("changed aggregate retained model selection")
	}
	if m.vp.YOffset() != m.conversationView.frame.toolSummaryRow(memberID) || m.conversationView.mode != anchored {
		t.Fatal("settlement moved reading away from summary")
	}
	m, _ = pressKey(m, f9)
	if got := m.conversationView.frame.provenance[m.vp.YOffset()].blockID; got != memberID {
		t.Fatalf("F9 lost preserved hidden member: got %d want %d", got, memberID)
	}
	m.vp.GotoBottom()
	m.conversationView.mode = followTail
	m.conv.addNotice("tail notice")
	m.refreshView()
	if !m.vp.AtBottom() || m.conversationView.mode != followTail {
		t.Fatal("grouping stole tail-follow ownership")
	}
}

func TestToolPresentationGroupsMixedOrder(t *testing.T) {
	m := scenario2Model(t, 100, 24, false, phaseRunning, nil)
	m = applyAll(m,
		client.ToolCallMsg{ID: "sub", Name: "Subagent", Args: `{"goal":"active goal"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "active goal"},
		client.ToolCallMsg{ID: "read", Name: "Read", Args: `{"path":"file.go"}`}, client.ToolResultMsg{CallID: "read", Content: "PRIVATE_READ"},
		client.ToolCallMsg{ID: "shell", Name: "Shell", Args: `{"command":"false"}`}, client.ToolResultMsg{CallID: "shell", Content: "PRIVATE_FAIL", IsError: true},
		client.ToolCallMsg{ID: "grep", Name: "Grep", Args: `{"pattern":"finalizing"}`}, client.ToolResultMsg{CallID: "grep", Content: "PRIVATE_PROVISIONAL", IsError: true, Available: true},
	)
	got := detailText(m)
	if !strings.HasPrefix(strings.TrimSpace(got), "● 2 tools run · 1 failed") || !strings.Contains(got, "Subagent") || !strings.Contains(got, "finalizing") {
		t.Fatalf("bad mixed group: %q", got)
	}
	if strings.Contains(got, "PRIVATE_READ") || strings.Contains(got, "PRIVATE_FAIL") {
		t.Fatalf("settled payload leaked: %q", got)
	}
	firstRow := func(frame renderedFrame, id uint64) int {
		t.Helper()
		for row, provenance := range frame.provenance {
			if provenance.blockID == id {
				return row
			}
		}
		t.Fatalf("displayed frame has no rows for block %d", id)
		return -1
	}
	assertNoSeparator := func(frame renderedFrame, left, right int) {
		t.Helper()
		for row := left + 1; row < right; row++ {
			if frame.provenance[row].separator {
				t.Fatalf("unexpected separator row %d between displayed rows %d and %d", row, left, right)
			}
		}
	}
	frame := m.conversationView.frame
	readID := toolBlockID(t, m.conv.scrollback, "read")
	summaryRow := frame.toolSummaryRow(readID)
	subRow := firstRow(frame, toolBlockID(t, m.conv.scrollback, "sub"))
	grepRow := firstRow(frame, toolBlockID(t, m.conv.scrollback, "grep"))
	if summaryRow < 0 || summaryRow >= subRow || subRow >= grepRow {
		t.Fatalf("comfort frame order summary=%d subagent=%d grep=%d", summaryRow, subRow, grepRow)
	}
	assertNoSeparator(frame, summaryRow, subRow)
	assertNoSeparator(frame, subRow, grepRow)

	active := scenario2Model(t, 100, 24, false, phaseRunning, nil)
	active = applyAll(active,
		client.ToolCallMsg{ID: "sub", Name: "Subagent", Args: `{"goal":"active goal"}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child", Goal: "active goal"},
		client.ToolCallMsg{ID: "grep", Name: "Grep", Args: `{"pattern":"active"}`},
	)
	activeFrame := active.conversationView.frame
	activeSubRow := firstRow(activeFrame, toolBlockID(t, active.conv.scrollback, "sub"))
	activeGrepRow := firstRow(activeFrame, toolBlockID(t, active.conv.scrollback, "grep"))
	if activeFrame.toolSummaryRow(toolBlockID(t, active.conv.scrollback, "sub")) >= 0 || activeSubRow >= activeGrepRow {
		t.Fatalf("all-active comfort frame reordered or aggregated cards: subagent=%d grep=%d", activeSubRow, activeGrepRow)
	}
	assertNoSeparator(activeFrame, activeSubRow, activeGrepRow)

	m, _ = pressKey(m, f9)
	got = detailText(m)
	if strings.Index(got, "Subagent") >= strings.Index(got, "✓ Read") || strings.Index(got, "✓ Read") >= strings.Index(got, "✗ Shell") || strings.Index(got, "✗ Shell") >= strings.Index(got, "Grep") {
		t.Fatalf("source order lost: %q", got)
	}
}
