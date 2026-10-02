package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

// These acceptance pins intentionally start at the UI reducer seam: the tool-call
// browser is a local, current-session projection and has no server dependency.
func TestMecatuiToolcallsInspector_Scenario1_OpenEmptyRunningAndResume(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	if _, ok := builtinByName(m.caps, m.wiredCollaborators(), "toolcalls"); !ok {
		t.Fatal("/toolcalls is absent from the local slash palette")
	}
	m = openToolcallsForTest(t, m)
	if !toolcallsForTest(t, m).open {
		t.Fatal("/toolcalls did not open for an empty conversation")
	}
	m.closeModal()
	m.phase = phaseRunning
	m = addToolcallsForTest(t, m, 2) // reconstructed scrollback is the resume projection
	m = openToolcallsForTest(t, m)
	if got, want := len(toolcallsForTest(t, m).entries), 2; got != want {
		t.Fatalf("resumed current-session calls = %d, want %d", got, want)
	}
}

func TestMecatuiToolcallsInspector_Scenario1_ResumeProjection(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "resumed"},
		Transcript: client.SessionTranscript{Messages: []client.ConversationMessage{
			{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "reused", Name: "Read", Args: `{"path":"resumed"}`}}},
			{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "reused", Content: "rehydrated"}},
		}},
	}
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context(), Resume: resume})
	m.width, m.height = 100, 30
	m.relayout()
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if len(s.entries) != 1 || s.entries[0].name != "Read" {
		t.Fatalf("resumed inventory: %#v", s.entries)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if got := inspectorDetail(t, toolcallsForTest(t, m), 80, 12); !strings.Contains(got, "rehydrated") || !strings.Contains(got, "resumed") {
		t.Fatalf("resumed detail: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario1_TopLevelDelegationsOnly(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("ordinary", "Read", `{"path":"first"}`)
	m.conv.addTool("sub", "Subagent", `{"prompt":"delegate"}`)
	if !m.conv.scrollback.Subagents().Start("sub", scrollback.SubagentStart{ChildID: "child", Goal: "work"}) {
		t.Fatal("could not specialize Subagent parent call")
	}
	if !m.conv.scrollback.Subagents().Update("sub", scrollback.SubagentUpdate{Trace: []scrollback.TraceEntry{{Kind: "tool.call", ToolName: "nested-Read"}}}) {
		t.Fatal("could not record child tool trace")
	}
	m.conv.addTool("team", "Team", `{"task":"coordinate"}`)
	if !m.conv.scrollback.Teams().Start("team", scrollback.TeamStart{TeamID: "team-1"}) {
		t.Fatal("could not specialize Team parent call")
	}
	if !m.conv.scrollback.Teams().Update("team", scrollback.TeamUpdate{TeamID: "team-1", Lanes: []scrollback.TeamLane{{Name: "member", Trace: []scrollback.TraceEntry{{Kind: "tool.call", ToolName: "nested-Grep"}}}}}) {
		t.Fatal("could not record team member tool trace")
	}
	m.conv.addTool("last", "Grep", `{"pattern":"done"}`)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if len(s.entries) != 4 {
		t.Fatalf("top-level entries = %#v; want four parents and no nested child tools", s.entries)
	}
	for i, name := range []string{"Read", "Subagent", "Team", "Grep"} {
		if s.entries[i].name != name {
			t.Fatalf("entry %d name = %q, want %q", i, s.entries[i].name, name)
		}
	}
	if !m.conv.scrollback.Tools().Resolve("sub", scrollback.ToolResult{Body: "child complete"}) ||
		!m.conv.scrollback.Tools().Resolve("team", scrollback.ToolResult{Body: "team complete", IsError: true}) {
		t.Fatal("could not resolve specialized parent calls")
	}
	m.syncToolcalls()
	if !s.entries[1].resolved || s.entries[1].failed || !s.entries[2].resolved || !s.entries[2].failed {
		t.Fatalf("delegation parent result state did not update in place: %#v", s.entries)
	}
}

func TestMecatuiToolcallsInspector_Scenario1_ChronologicalNavigation(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 125)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.Render(80, 12)
	if s.selected != 124 || !strings.Contains(stripANSIstr(m.View().Content), "file-124.go") {
		t.Fatalf("opening did not reveal newest call: %d", s.selected)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.selected != 0 || !strings.Contains(stripANSIstr(m.View().Content), "file-0.go") {
		t.Fatalf("top did not reveal oldest call: %d", s.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.selected <= 0 || s.selected >= 124 {
		t.Fatalf("page down did not navigate: %d", s.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.selected != 124 {
		t.Fatalf("end selection = %d", s.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.selected >= 124 {
		t.Fatalf("page up did not navigate: %d", s.selected)
	}
	selected := s.selected
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.detail || s.selected != selected {
		t.Fatalf("first Escape lost list selection: %d", s.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if toolcallsForTest(t, m) != nil || !m.prompt.Focused() {
		t.Fatal("second Escape did not close and restore prompt")
	}
}

func TestMecatuiToolcallsInspector_Scenario1_ChronologicalNavigationShort(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 3)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if got, want := s.selected, 2; got != want {
		t.Fatalf("newest call selection = %d, want %d", got, want)
	}
	_, _ = s.Render(80, 20)
	_, handled, closed := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if !handled || closed || s.selected != 1 {
		t.Fatalf("up navigation = selected %d handled=%v closed=%v", s.selected, handled, closed)
	}
	_, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !s.detail {
		t.Fatal("enter did not open the detail placeholder")
	}
	_, _, closed = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.detail || closed || s.selected != 1 {
		t.Fatal("escape did not return from detail to its selected list call")
	}
}

func TestMecatuiToolcallsInspector_Scenario1_StableLiveSelection(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 2)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.selected = 0
	m = addToolcallsForTest(t, m, 1)
	if got, want := s.selected, 0; got != want {
		t.Fatalf("earlier selected call moved to %d, want %d", got, want)
	}
	m.conv.resolveTool("call-0", "done", false)
	m.syncToolcalls()
	if got, want := s.selected, 0; got != want || !s.entries[0].resolved {
		t.Fatalf("result changed selected row or did not update it: selected=%d entry=%#v", got, s.entries[0])
	}
	s.selected = len(s.entries) - 1
	m = addToolcallsForTest(t, m, 1)
	if got, want := s.selected, len(s.entries)-1; got != want {
		t.Fatalf("newest-follow selection = %d, want %d", got, want)
	}
}

func TestMecatuiToolcallsInspector_Scenario3_FullRegionAndCompactFallback(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 1)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if placement := s.modalPlacement(); placement != modalPlacementFill {
		t.Fatalf("inspector placement = %v, want conversation-region fill", placement)
	}
	m.width, m.height = 240, 35
	m.relayout()
	_ = m.View()
	if got, want := m.metrics.contentBounds.x1-m.metrics.contentBounds.x0, m.width; got != want {
		t.Fatalf("browser width = %d, want offered %d (not a capped centered card)", got, want)
	}
	if got, want := m.metrics.contentBounds.y1-m.metrics.contentBounds.y0, m.vp.Height(); got != want {
		t.Fatalf("browser height = %d, want offered %d", got, want)
	}
	if got, _ := s.Render(0, 1); got != "" {
		t.Fatalf("nonpositive geometry rendered %q", got)
	}
	compact, _ := s.Render(10, 3)
	if !s.compact || !strings.Contains(stripANSIstr(compact), s.deps.marks.closeOnly) {
		t.Fatalf("compact fallback = %q, compact=%v", compact, s.compact)
	}
	for _, width := range []int{12, 13, 14} {
		body, _ := s.Render(width, 3)
		if got := stripANSIstr(body); !s.compact || got != "small · "+s.deps.marks.closeOnly || ansi.StringWidth(got) > width {
			t.Fatalf("width %d lost too-small/Escape hint: %q", width, got)
		}
	}
	if body, _ := s.Render(15, 3); !s.compact || stripANSIstr(body) != "too small · "+s.deps.marks.closeOnly {
		t.Fatalf("width 15 lost full too-small hint: %q", body)
	}
	// The normal navigation hint is wider than this offer. Do not clip Escape:
	// switch to the close-only fallback before rendering the list.
	compact, _ = s.Render(30, 20)
	if !s.compact || !strings.Contains(stripANSIstr(compact), s.deps.marks.closeOnly) {
		t.Fatalf("narrow fallback clipped close hint: %q", compact)
	}
	normal, _ := s.Render(80, 20)
	if s.compact || !strings.Contains(stripANSIstr(normal), "Tool calls") {
		t.Fatalf("normal geometry did not restore the list: %q", normal)
	}
	listHint := s.deps.marks.navUp + "/" + s.deps.marks.navDown + " · " + s.deps.marks.scroll + " · " + s.deps.marks.choose + " detail · " + s.deps.marks.closeOnly + " close"
	limit := ansi.StringWidth(listHint)
	if body, _ := s.Render(limit-1, 20); !s.compact || strings.Contains(body, "Tool calls") {
		t.Fatalf("below list hint threshold: %q", body)
	}
	if body, _ := s.Render(limit, 20); s.compact || !strings.Contains(body, s.deps.marks.closeOnly) {
		t.Fatalf("at list hint threshold: %q", body)
	}
	if s.selected != 0 || s.list == nil || !strings.Contains(stripANSIstr(normal), "▶") {
		t.Fatalf("resize lost selection or non-color marker: selected=%d list=%v body=%q", s.selected, s.list, normal)
	}
	for _, width := range []int{1, 3, 10, 30, 80, 160} {
		body, _ := s.Render(width, 20)
		for _, row := range strings.Split(stripANSIstr(body), "\n") {
			if ansi.StringWidth(row) > width {
				t.Fatalf("list width %d overflows: %q", width, row)
			}
		}
		if s.compact && ansi.StringWidth(s.deps.marks.closeOnly) <= width && !strings.Contains(stripANSIstr(body), s.deps.marks.closeOnly) {
			t.Fatalf("width %d lost Escape hint: %q", width, body)
		}
	}
	s.Render(80, 20)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	detailHint := s.deps.marks.navUp + "/" + s.deps.marks.navDown + " · " + s.deps.marks.scroll + " · " + s.deps.marks.jumpTopFull + "/" + s.deps.marks.jumpEndFull + " · " + s.deps.marks.closeOnly + " back"
	limit = ansi.StringWidth(detailHint)
	if body, _ := s.Render(limit-1, 20); !s.compact || strings.Contains(body, "Tool calls") {
		t.Fatalf("below detail hint threshold: %q", body)
	}
	if body, _ := s.Render(limit, 20); s.compact || !strings.Contains(body, s.deps.marks.closeOnly) {
		t.Fatalf("at detail hint threshold: %q", body)
	}
	for _, width := range []int{1, 10, 30, 80} {
		body, _ := s.Render(width, 20)
		if width == 30 && (!s.compact || !strings.Contains(stripANSIstr(body), s.deps.marks.closeOnly)) {
			t.Fatalf("detail narrow hint clipped: %q", body)
		}
	}
}

func TestMecatuiToolcallsInspector_Scenario3_InputOwnershipAndSafety(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("unsafe", "Read\x1b]8;;https://unsafe.example\a", "{\"path\":\"x\x1b[31m\"}")
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if !s.open || m.prompt.Focused() {
		t.Fatal("open inspector did not own keyboard input")
	}
	body, _ := s.Render(80, 20)
	if strings.Contains(body, "\x1b]") || strings.Contains(body, "\x1b[31m") {
		t.Fatalf("untrusted terminal control reached inspector rendering: %q", body)
	}
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	before := m.vp.YOffset()
	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	m = updated.(Model)
	if got := m.vp.YOffset(); got != before {
		t.Fatalf("inspector wheel changed hidden conversation offset from %d to %d", before, got)
	}
}

func TestMecatuiToolcallsInspector_Scenario3_SessionReplacementClosesInspector(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.sessionID = "source"
	m.conv.addTool("reused", "Read", `{"path":"source"}`)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	_, _ = s.Render(80, 20)
	_, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.clearPending = &clearHandoff{sourceID: "source", token: 1}
	updated, _ := m.Update(clearSessionReadyMsg{
		oldID: "source", token: 1,
		ready: client.SessionReadyMsg{SessionID: "successor"},
	})
	m = updated.(Model)
	if m.sessionID != "successor" || toolcallsForTest(t, m) != nil || !m.conv.isEmpty() {
		t.Fatalf("clear lifecycle leaked source inspector or scrollback: session=%q modal=%T", m.sessionID, m.modal)
	}
	m.conv.addTool("reused", "Write", `{"path":"successor"}`)
	m = openToolcallsForTest(t, m)
	fresh := toolcallsForTest(t, m)
	if len(fresh.entries) != 1 || fresh.entries[0].name != "Write" || fresh.detail || fresh == s {
		t.Fatalf("reused call ID retained prior session state: %#v", fresh)
	}
}

func TestMecatuiToolcallsInspector_Scenario3_LoadedSessionReplacement(t *testing.T) {
	resume := &client.ResumeSelection{
		Row: client.SessionListItem{ID: "source"},
		Transcript: client.SessionTranscript{Messages: []client.ConversationMessage{
			{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "reused", Name: "Read", Args: `{"path":"source"}`}}},
			{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "reused", Content: "source result"}},
		}},
	}
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context(), Resume: resume})
	m.width, m.height = 100, 30
	m.relayout()
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.Render(80, 12)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if got := inspectorDetail(t, s, 80, 12); !strings.Contains(got, "source result") {
		t.Fatalf("loaded source detail missing: %q", got)
	}
	oldGen := m.liveGen
	target := conversationFromTranscript([]client.ConversationMessage{
		{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "reused", Name: "Write", Args: `{"path":"target"}`}}},
		{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "reused", Content: "target result"}},
	})
	updated, _, handled := m.adoptAuthoritativeTranscript(client.SessionListItem{ID: "target"}, target, client.SessionSnapshot{})
	m = updated.(Model)
	if m.liveGen == oldGen {
		t.Fatal("replacement did not invalidate former session's live reader")
	}
	if !handled || m.sessionID != "target" || toolcallsForTest(t, m) != nil {
		t.Fatalf("loaded target failed to close source inspector: session=%q modal=%T", m.sessionID, m.modal)
	}
	updated, _ = m.Update(liveMsg{gen: oldGen, msg: client.ToolCallMsg{ID: "late", Name: "Read", Args: `{"path":"source"}`}})
	m = updated.(Model)
	updated, _ = m.Update(liveMsg{gen: oldGen, msg: client.ToolResultMsg{CallID: "reused", Content: "late source result"}})
	m = updated.(Model)
	m = openToolcallsForTest(t, m)
	fresh := toolcallsForTest(t, m)
	if fresh == s || len(fresh.entries) != 1 || fresh.entries[0].name != "Write" {
		t.Fatalf("target inventory leaked source: %#v", fresh.entries)
	}
	fresh.Render(80, 12)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if got := inspectorDetail(t, fresh, 80, 12); !strings.Contains(got, "target result") || strings.Contains(got, "source result") || strings.Contains(got, "late source result") {
		t.Fatalf("target detail leaked source: %q", got)
	}
}

func newToolcallsInspectorModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context()})
	m.phase = phaseIdle
	m.width, m.height = 100, 30
	m.relayout()
	return m
}

func openToolcallsForTest(t *testing.T, m Model) Model {
	t.Helper()
	opened, _ := m.runToolcalls()
	return opened.(Model)
}

func addToolcallsForTest(t *testing.T, m Model, count int) Model {
	t.Helper()
	start := m.conv.scrollback.Len()
	for i := 0; i < count; i++ {
		m.conv.addTool(fmt.Sprintf("call-%d", start+i), "Read", fmt.Sprintf(`{"path":"file-%d.go"}`, start+i))
	}
	m.syncToolcalls()
	return m
}

func toolcallsForTest(t *testing.T, m Model) *toolcallsState {
	t.Helper()
	if m.modal == nil {
		return nil
	}
	s, ok := m.modal.(*toolcallsState)
	if !ok {
		t.Fatalf("modal = %T, want *toolcallsState", m.modal)
	}
	return s
}
