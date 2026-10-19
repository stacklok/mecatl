package ui

import (
	"image/color"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Check the displayed Model frame against both assembly paths without resetting
// the warm renderer. Model owns an independent copy of provenance scratch.
func assertGroupedModelFrame(t *testing.T, m Model) {
	t.Helper()
	frame := m.conversationView.frame
	fresh := newRenderer(m.deps.Theme, m.rend.marks)
	fresh.setWidth(m.width)
	want := fresh.renderConversationFrame(&m.conv.scrollback, m.expandConversation)
	if !reflect.DeepEqual(frame.lines, want.lines) || !reflect.DeepEqual(frame.provenance, want.provenance) || !reflect.DeepEqual(frame.toolSummaries, want.toolSummaries) {
		t.Fatalf("displayed frame differs from fresh at width %d detailed=%t:\n%q\n%q", m.width, m.expandConversation, frame.lines, want.lines)
	}
	if got := m.rend.renderConversation(&m.conv, m.expandConversation); got != strings.Join(frame.lines, "\n") {
		t.Fatal("warm string assembly differs from displayed frame")
	}
}

func TestToolGroupsModelStableSelections(t *testing.T) {
	for _, marker := range []string{"PROSEMARKER", "ACTIVEMARKER"} {
		t.Run(marker, func(t *testing.T) {
			m := scenario2Model(t, 100, 20, false, phaseRunning, nil)
			m.conv.addNotice("untouched PROSEMARKER prose")
			m = applyAll(m,
				client.ToolCallMsg{ID: "earlier", Name: "Read", Args: `{"path":"earlier.go"}`},
				client.ToolCallMsg{ID: "active", Name: "Read", Args: `{"path":"ACTIVEMARKER"}`},
				client.ToolCallMsg{ID: "done", Name: "Read", Args: `{}`},
				client.ToolResultMsg{CallID: "done", Content: "PRIVATE_DONE"},
			)
			m.conv.addUser(strings.Repeat("later prose\n", 40))
			m.refreshView()
			line := lineIndexContaining(m.vp.GetContent(), marker)
			if line < 0 {
				t.Fatal("selection marker missing")
			}
			m.vp.SetYOffset(line)
			m.conversationView.mode = anchored
			text := stripANSIstr(m.conversationView.frame.lines[line])
			m = m.wordSelect(line, graphemeCount(text[:strings.Index(text, marker)]))
			if m.sel.snapshot != marker {
				t.Fatalf("selection fixture: %+v", m.sel)
			}
			point := m.sel.anchorPoint
			check := func(step string) {
				t.Helper()
				if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != marker || m.sel.anchorPoint != point {
					t.Fatalf("%s lost unchanged selection: %+v", step, m.sel)
				}
				if m.conversationView.mode != anchored || m.conversationView.frame.provenance[m.vp.YOffset()].blockID != point.blockID {
					t.Fatalf("%s moved reading away from untouched content", step)
				}
				_, cmd := m.copySelection()
				if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != marker {
					t.Fatalf("%s copy=%q ok=%t", step, got, ok)
				}
				assertGroupedModelFrame(t, m)
			}
			m = applyAll(m, client.ToolResultMsg{CallID: "earlier", Content: "PRIVATE_EARLIER"})
			check("earlier settlement")
			m = applyAll(m, client.ToolCallMsg{ID: "append", Name: "Shell", Args: `{}`}, client.ToolResultMsg{CallID: "append", Content: "PRIVATE_APPEND", IsError: true})
			check("count growth in later group")
			for _, msg := range []tea.Msg{f9, tea.WindowSizeMsg{Width: 80, Height: 20}, f9} {
				m = applyAll(m, msg)
				check("detail/resize")
			}
			// A changed active card is the negative control, not permission to
			// weaken preservation for the untouched prose.
			m = applyAll(m, client.ToolResultMsg{CallID: "active", Content: "PRIVATE_ACTIVE"})
			if marker == "ACTIVEMARKER" {
				if m.sel.active {
					t.Fatal("disappeared active selection survived settlement")
				}
			} else {
				check("active settlement leaves prose")
			}
		})
	}
}

func TestToolGroupsModelMemberAnchorGrowthAndInspector(t *testing.T) {
	m := scenario2Model(t, 100, 18, false, phaseRunning, nil)
	m = applyAll(m,
		client.ToolCallMsg{ID: "earlier", Name: "Read", Args: `{"path":"earlier.go"}`},
		client.ToolCallMsg{ID: "member", Name: "Shell", Args: `{"command":"false"}`},
		client.ToolResultMsg{CallID: "member", Content: "PRIVATE_MEMBER", IsError: true},
	)
	// Hidden assistant insertion lets a new call grow the same presentation group.
	m.conv.startAssistant()
	m = applyAll(m, client.ToolCallMsg{ID: "growth", Name: "Read", Args: `{"path":"growth.go"}`})
	m.conv.addUser(strings.Repeat("later prose\n", 40))
	m.refreshView()
	m = applyAll(m, f9)
	member := toolBlockID(t, m.conv.scrollback, "member")
	m.vp.SetYOffset(m.conversationView.frame.firstRegionRow(member, conversationRegionBody))
	m.conversationView.mode = anchored
	m = applyAll(m, f9)
	check := func(step string) {
		t.Helper()
		if m.conversationView.mode != anchored || m.conversationView.anchor.blockID != member || m.vp.YOffset() != m.conversationView.frame.toolSummaryRow(member) {
			t.Fatalf("%s lost member-owned summary edge: offset=%d anchor=%+v", step, m.vp.YOffset(), m.conversationView.anchor)
		}
		assertGroupedModelFrame(t, m)
	}
	check("collapse")
	m = applyAll(m, client.ToolResultMsg{CallID: "growth", Content: "PRIVATE_GROWTH"})
	check("count growth")
	oldSummary := m.conversationView.frame.toolSummaries[member]
	m = applyAll(m, client.ToolResultMsg{CallID: "earlier", Content: "PRIVATE_EARLIER"})
	check("earlier active settles")
	if m.conversationView.frame.toolSummaries[member] == oldSummary {
		t.Fatal("fixture did not change first settled summary ID")
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 80, Height: 18})
	check("resize")
	before, offset, anchor := m.conversationContent(), m.vp.YOffset(), m.conversationView.anchor
	m = applyAll(m, ctrlT, tea.KeyPressMsg{Code: tea.KeyHome})
	s := toolcallsForTest(t, m)
	if len(s.entries) != 3 {
		t.Fatalf("inspector entries=%d", len(s.entries))
	}
	for i, want := range []struct{ id, args, result string }{
		{"earlier", "Path: earlier.go", "PRIVATE_EARLIER"},
		{"member", "Command: false", "PRIVATE_MEMBER"},
		{"growth", "Path: growth.go", "PRIVATE_GROWTH"},
	} {
		if uint64(s.entries[i].blockID) != toolBlockID(t, m.conv.scrollback, want.id) {
			t.Fatal("inspector reordered source inventory")
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
		out := stripANSIstr(m.View().Content)
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
		out += stripANSIstr(m.View().Content)
		if !strings.Contains(out, want.args) || !strings.Contains(out, want.result) {
			t.Fatalf("inspector truncated exact call details: %q", out)
		}
		m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc}, tea.KeyPressMsg{Code: tea.KeyDown})
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || m.expandConversation || m.conversationContent() != before || m.vp.YOffset() != offset || m.conversationView.anchor != anchor {
		t.Fatal("inspector close changed projection or reading")
	}
	check("inspector close")
	m = applyAll(m, f9)
	if m.conversationView.frame.provenance[m.vp.YOffset()].blockID != member {
		t.Fatal("detail did not restore original member after summary ID changed")
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = applyAll(m, client.ToolCallMsg{ID: "tail", Name: "Read", Args: `{}`}, client.ToolResultMsg{CallID: "tail", Content: "PRIVATE_TAIL"}, f9)
	if !m.vp.AtBottom() || m.conversationView.mode != followTail {
		t.Fatal("settlement/detail stole tail ownership")
	}
	assertGroupedModelFrame(t, m)
}

func TestToolGroupsModelSummaryCopyAndTheme(t *testing.T) {
	m := newTestModelFromDeps(Deps{Theme: theme.NewRegistry().Default(), ThemeAutoDetect: true, Ctx: t.Context(), NoBanner: true})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 20}, tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	m.phase = phaseRunning
	m = applyAll(m,
		client.ToolCallMsg{ID: "one", Name: "Read", Args: `{}`},
		client.ToolResultMsg{CallID: "one", Content: strings.Repeat("PRIVATE_RESULT", 10000), IsError: true, Blocks: []client.ContentBlock{{Kind: client.ContentBlockImage, Data: []byte(strings.Repeat("PRIVATE_ARTIFACT", 10000))}}},
		client.ToolCallMsg{ID: "pending", Name: "Read", Args: `{}`},
	)
	m.conv.addNotice("neutral body")
	m.conv.addUser(strings.Repeat("later prose\n", 40))
	m.refreshView()
	member := toolBlockID(t, m.conv.scrollback, "one")
	row := m.conversationView.frame.toolSummaryRow(member)
	m.vp.SetYOffset(row)
	m.conversationView.mode = anchored
	plain := stripANSIstr(m.conversationView.frame.lines[row])
	x := strings.Index(plain, "●")
	y := convTopRow(m)
	m, _ = pressMouse(m, tea.MouseLeft, x, y)
	m, _ = motionMouse(m, len(plain), y)
	if got := selectedText(m.vp.GetContent(), m.sel); got != strings.TrimSpace(plain) {
		t.Fatalf("summary gesture selected %q", got)
	}
	beforeSel, beforeAnchor, dark := m.sel, m.conversationView.anchor, m.rend
	darkBG := selectionBgSGR(t, m)
	if !strings.Contains(m.vp.GetContent(), darkBG) {
		t.Fatal("summary selection has no dark highlight")
	}
	_, cmd := m.copySelection()
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != strings.TrimSpace(plain) || strings.Contains(got, "PRIVATE") {
		t.Fatalf("derived copy=%q ok=%t", got, ok)
	}
	m = applyAll(m, tea.BackgroundColorMsg{Color: color.White})
	if m.rend == dark || m.deps.Theme.Name != "solar" {
		t.Fatal("background response did not rebuild renderer")
	}
	if m.sel != beforeSel || m.conversationView.anchor != beforeAnchor || m.vp.YOffset() != row || m.conversationView.mode != anchored {
		t.Fatal("theme changed summary selection/reading")
	}
	lightBG := selectionBgSGR(t, m)
	if lightBG == darkBG || !strings.Contains(m.vp.GetContent(), lightBG) || strings.Contains(m.vp.GetContent(), darkBG) {
		t.Fatal("summary selection retained dark highlight")
	}
	out := m.conversationContent()
	for _, token := range []string{m.deps.Theme.Style("transcriptToolHeading").Render(" 1 tool run"), m.deps.Theme.Style("toolErr").Render(" · 1 failed"), m.deps.Theme.Style("transcriptHarnessText").Render("neutral body")} {
		if !strings.Contains(out, token) {
			t.Fatalf("light transcript missing role %q", token)
		}
	}
	assertGroupedModelFrame(t, m)
	loads, prepares := m.rend.snapshotLoads, m.rend.cardPrepares
	m.refreshView()
	if m.rend.snapshotLoads != loads || m.rend.cardPrepares != prepares {
		t.Fatal("warm Model render reloaded settled payload")
	}
	m = applyAll(m, f9)
	if m.sel.active {
		t.Fatal("disappeared summary retained selection")
	}
	m = applyAll(m, f9)
	row = m.conversationView.frame.toolSummaryRow(member)
	plain = stripANSIstr(m.conversationView.frame.lines[row])
	m = m.wordSelect(row, graphemeCount(plain[:strings.Index(plain, "Toolcalls")]))
	// A changed count must invalidate even an unchanged selected hint.
	m = applyAll(m, client.ToolCallMsg{ID: "two", Name: "Shell", Args: `{}`}, client.ToolResultMsg{CallID: "two", Content: "PRIVATE_GROWTH", IsError: true})
	// This later group leaves the selected summary unchanged.
	if !m.sel.active {
		t.Fatal("unrelated group growth cleared summary selection")
	}
	m = applyAll(m, client.ToolResultMsg{CallID: "pending", Content: "PRIVATE_SETTLEMENT", IsError: true})
	if m.sel.active {
		t.Fatal("changed summary retained stale derived hint selection")
	}
	assertGroupedModelFrame(t, m)
}

func TestToolGroupsModelFailureLifecycleAndReplay(t *testing.T) {
	m := scenario2Model(t, 100, 20, false, phaseRunning, nil)
	events := []tea.Msg{
		client.ToolCallMsg{ID: "read", Name: "Read", Args: `{}`},
		client.ToolResultMsg{CallID: "read", Content: "PRIVATE_PROVISIONAL", IsError: true, Available: true},
		client.ToolResultMsg{CallID: "read", Content: "PRIVATE_CANONICAL", IsError: true},
		client.ToolCallMsg{ID: "sub", Name: "Subagent", Args: `{}`},
		client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "sub", ChildID: "child"},
		client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "sub", ChildID: "child", Stop: "error"},
		client.ToolCallMsg{ID: "team", Name: "Team", Args: `{}`},
		client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team"},
		client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "team", TeamID: "team", Stop: "error"},
	}
	replay := sessionsState{}
	for i, event := range events {
		m = applyAll(m, event)
		replay.applyReplayEvent(event)
		assertGroupedModelFrame(t, m)
		fresh := newRenderer(m.deps.Theme, m.rend.marks)
		fresh.setWidth(m.width)
		if got := strings.Join(fresh.renderConversationFrame(&replay.transcript.scrollback, false).lines, "\n"); got != m.conversationContent() {
			t.Fatalf("event %d replay/live differ", i)
		}
		if i < 2 && strings.Contains(detailText(m), "tools run") {
			t.Fatal("active/provisional failure counted as settled")
		}
		if i == 1 && !strings.Contains(detailText(m), "finalizing") {
			t.Fatal("provisional failure lost finalizing state")
		}
	}
	if got := detailText(m); !strings.Contains(got, "● 3 tools run · 3 failed") {
		t.Fatalf("canonical and delegation failures: %q", got)
	}
	for _, msg := range []tea.Msg{f9, tea.WindowSizeMsg{Width: 28, Height: 20}, f9, tea.WindowSizeMsg{Width: 100, Height: 20}} {
		m = applyAll(m, msg)
		assertGroupedModelFrame(t, m)
	}
}

func TestToolGroupsModelOrdinaryTerminalFailure(t *testing.T) {
	m := scenario2Model(t, 100, 20, false, phaseRunning, nil)
	m = applyAll(m, client.ToolCallMsg{ID: "failed", Name: "Read", Args: `{"path":"failed.go"}`}, client.ToolCallMsg{ID: "awaiting", Name: "Read", Args: `{"path":"awaiting.go"}`})
	// Ordinary terminal metadata has no dedicated client message. Exercise its
	// owning scrollback seam, then the real Model/viewport replacement.
	if !m.conv.scrollback.Tools().Finish("failed", true) || !m.conv.scrollback.Tools().Finish("awaiting", false) {
		t.Fatal("terminal metadata not recorded")
	}
	m.refreshView()
	if got := detailText(m); !strings.Contains(got, "● 1 tool run · 1 failed") || !strings.Contains(got, "awaiting result") {
		t.Fatalf("terminal failure/success-without-result: %q", got)
	}
	assertGroupedModelFrame(t, m)
	m = applyAll(m, f9)
	if got := detailText(m); !strings.Contains(got, "✗ Read · failed.go") || !strings.Contains(got, "awaiting result") {
		t.Fatalf("terminal detail: %q", got)
	}
	m = applyAll(m, client.ToolResultMsg{CallID: "failed", Content: "PRIVATE_FAILURE", IsError: true}, client.ToolResultMsg{CallID: "awaiting", Content: "PRIVATE_SUCCESS"}, f9)
	if got := detailText(m); !strings.Contains(got, "● 2 tools run · 1 failed") || strings.Contains(got, "awaiting result") {
		t.Fatalf("canonical results did not replace terminal projection: %q", got)
	}
	assertGroupedModelFrame(t, m)
}

func TestToolGroupsModelHistoryReplacementReusesLocalIDs(t *testing.T) {
	loader := &fakeSessionTranscriptLoader{}
	m := newSessionsModel(t, newSessionsConv(), &fakeSessionLister{}, loader)
	m.deps.NoAltScreen = false
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 20})
	messages := func(prefix string, split bool) []client.ConversationMessage {
		out := []client.ConversationMessage{
			{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "one", Name: "Read", Args: `{"path":"` + prefix + `.go"}`}}},
			{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "one", Content: "PRIVATE_" + prefix, IsError: !split}},
		}
		if split {
			out = append(out, client.ConversationMessage{Role: "user", Text: "new visible boundary"})
		}
		return append(out,
			client.ConversationMessage{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "two", Name: "Shell", Args: `{"command":"false"}`}}},
			client.ConversationMessage{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "two", Content: "PRIVATE_SECOND"}},
			client.ConversationMessage{Role: "user", Text: strings.Repeat("later prose\n", 40)},
		)
	}
	var lastLoad tea.Msg
	load := func(id string, inspect bool, messages []client.ConversationMessage) {
		t.Helper()
		loader.transcript = client.SessionTranscript{SessionID: id, Complete: true, Messages: messages}
		mm, cmd, handled := m.loadSessionTranscript(client.SessionListItem{ID: id, Title: id, Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true}}, inspect)
		if !handled || cmd == nil {
			t.Fatal("history load was not issued")
		}
		m = mm.(Model)
		lastLoad = cmd()
		m = applyAll(m, lastLoad)
	}
	load("old", false, messages("OLD", false))
	oldMember := toolBlockID(t, m.conv.scrollback, "one")
	if !strings.Contains(detailText(m), "● 2 tools run · 1 failed") {
		t.Fatal("history did not assemble settled group")
	}
	m.vp.SetYOffset(m.conversationView.frame.toolSummaryRow(oldMember))
	m.conversationView.mode = anchored
	row := m.vp.YOffset()
	plain := stripANSIstr(m.conversationView.frame.lines[row])
	m = m.wordSelect(row, graphemeCount(plain[:strings.Index(plain, "Toolcalls")]))
	if !m.sel.active || m.conversationView.mode != anchored || len(m.rend.blocks.rendered) == 0 {
		t.Fatal("old projection was not primed")
	}
	assertGroupedModelFrame(t, m)
	load("new", false, messages("NEW", true))
	if toolBlockID(t, m.conv.scrollback, "one") != oldMember {
		t.Fatal("replacement did not reuse local block ID/index")
	}
	if m.sel.active || m.selBase != "" || m.conversationView.anchor != (readingAnchor{}) || m.conversationView.mode != followTail || !m.vp.AtBottom() {
		t.Fatal("history adoption leaked old selection/reading state")
	}
	if got := detailText(m); strings.Count(got, "● 1 tool run") != 2 || strings.Contains(got, "failed") || strings.Contains(got, "OLD") {
		t.Fatalf("replacement leaked grouping/cache: %q", got)
	}
	for _, msg := range []tea.Msg{f9, tea.WindowSizeMsg{Width: 40, Height: 20}, f9, tea.WindowSizeMsg{Width: 100, Height: 20}} {
		m = applyAll(m, msg)
		assertGroupedModelFrame(t, m)
	}
	// Read-only history has a separate renderer/viewport and must not replace
	// the active document. Reopening the same session reuses all local slots.
	active := m.conversationContent()
	load("inspect", true, messages("INSPECT_OLD", false))
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "● 2 tools run · 1 failed") || strings.Contains(got, "PRIVATE_") {
		t.Fatalf("inspection aggregate: %q", got)
	}
	st := sessionsSurface(&m)
	stale := lastLoad
	oldRenderer := st.transcriptRend
	m = applyAll(m, f9)
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "✗ Read") || strings.Contains(got, "PRIVATE_") {
		t.Fatalf("history detailed projection: %q", got)
	}
	load("inspect", true, messages("INSPECT_NEW", true))
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyHome})
	freshInspection := m.View().Content
	m = applyAll(m, stale)
	if m.View().Content != freshInspection {
		t.Fatal("late same-session history response resurrected old grouping")
	}
	if sessionsSurface(&m).transcriptRend == oldRenderer || sessionsSurface(&m).transcriptExpand {
		t.Fatal("same-session reload retained renderer/mode")
	}
	if got := stripANSIstr(m.View().Content); !strings.Contains(got, "● 1 tool run") || strings.Contains(got, "failed") || strings.Contains(got, "INSPECT_OLD") {
		t.Fatalf("inspection reused stale groups: %q", got)
	}
	m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc}, tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.modal != nil || m.conversationContent() != active || m.sessionID != "new" {
		t.Fatal("inspection replaced active conversation")
	}
	assertGroupedModelFrame(t, m)
}
