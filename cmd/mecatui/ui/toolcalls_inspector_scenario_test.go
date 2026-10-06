package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
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
	if len(s.entries) != 1 || s.entries[0].fullName != "Read" {
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
		if s.entries[i].fullName != name {
			t.Fatalf("entry %d name = %q, want %q", i, s.entries[i].fullName, name)
		}
	}
	if !m.conv.scrollback.Tools().Resolve("sub", scrollback.ToolResult{Body: "child complete"}) ||
		!m.conv.scrollback.Tools().Resolve("team", scrollback.ToolResult{Body: "team complete", IsError: true}) {
		t.Fatal("could not resolve specialized parent calls")
	}
	m.syncToolcalls()
	if s.entries[1].state != toolcallDone || s.entries[2].state != toolcallFailed {
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
	if got, want := s.selected, 0; got != want || s.entries[0].state != toolcallDone {
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
	if len(fresh.entries) != 1 || fresh.entries[0].fullName != "Write" || fresh.detail || fresh == s {
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
	if fresh == s || len(fresh.entries) != 1 || fresh.entries[0].fullName != "Write" {
		t.Fatalf("target inventory leaked source: %#v", fresh.entries)
	}
	fresh.Render(80, 12)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	if got := inspectorDetail(t, fresh, 80, 12); !strings.Contains(got, "target result") || strings.Contains(got, "source result") || strings.Contains(got, "late source result") {
		t.Fatalf("target detail leaked source: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario4_CoreToolPresentation(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	calls := []struct {
		id, name, args, wantIntent string
	}{
		{"read", "Read", `{"path":"src/main.go","offset":12,"limit":20,"extra":"kept"}`, "src/main.go"},
		{"list", "ListDir", `{"path":"src","depth":2}`, "src"},
		{"glob", "Glob", `{"pattern":"**/*.go","path":"cmd"}`, "**/*.go"},
		{"grep", "Grep", `{"pattern":"TODO","path":"cmd"}`, "TODO"},
		{"edit", "Edit", `{"path":"a.go","old_string":"old","new_string":"new","extra":true}`, "a.go"},
		{"write", "Write", `{"path":"a.go","content":"complete replacement"}`, "a.go"},
		{"copy", "Copy", `{"source":"a.go","destination":"b.go"}`, "a.go → b.go"},
		{"move", "Move", `{"source":"a.go","destination":"b.go"}`, "a.go → b.go"},
		{"remove", "Remove", `{"path":"old.go"}`, "old.go"},
		{"shell", "Shell", `{"command":"go test ./..."}`, "go test ./..."},
		{"web", "WebFetch", `{"url":"https://example.invalid/docs"}`, "https://example.invalid/docs"},
		{"resource", "FetchMcpResource", `{"uri":"mcp://docs/readme"}`, "mcp://docs/readme"},
	}
	for _, call := range calls {
		m.conv.addTool(call.id, call.name, call.args)
	}
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	for i, call := range calls {
		if got := s.entries[i].intent; !strings.Contains(got, call.wantIntent) || strings.Contains(got, `{"`) {
			t.Fatalf("%s list intent = %q, want readable %q", call.name, got, call.wantIntent)
		}
		s.selected = i
		s.detail = true
		s.refreshDetail(&m.conv.scrollback)
		lines := strings.Join(toolcallDetailLines(*s.detailEntry), "\n")
		for _, want := range []string{"Arguments:"} {
			if !strings.Contains(lines, want) {
				t.Fatalf("%s detail missing %q: %q", call.name, want, lines)
			}
		}
	}
	// Read limits and unknowns, replacements, content, and commands must be
	// complete labeled values rather than an argument-object envelope.
	for _, check := range []struct {
		index int
		wants []string
	}{
		{0, []string{"Path: src/main.go", "Offset: 12", "Limit: 20", "Extra: kept"}},
		{4, []string{"Path: a.go", "Old string: old", "New string: new", "Extra: true"}},
		{5, []string{"Path: a.go", "Content: complete replacement"}},
		{9, []string{"Command: go test ./..."}},
	} {
		s.selected = check.index
		s.refreshDetail(&m.conv.scrollback)
		got := strings.Join(toolcallDetailLines(*s.detailEntry), "\n")
		for _, want := range check.wants {
			if !strings.Contains(got, want) {
				t.Fatalf("detail %d missing %q: %q", check.index, want, got)
			}
		}
	}
}

func TestMecatuiToolcallsInspector_Scenario4_GenericFallbackAndLifecycle(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("unknown", "future-tool", `{"zebra":"last","alpha":"first","target":"useful target"}`)
	m.conv.addTool("mcp", "mcp__docs__lookup", `{"query":"needle"}`)
	m.conv.addTool("delegate", "Subagent", `{"prompt":"delegate this"}`)
	m.conv.addTool("invalid", "odd", "not-json\x1b[31m")
	m.conv.addTool("array", "odd-array", `["not","an","object"]`)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	for i, want := range []string{"useful target", "needle", "delegate this", "odd", "odd-array"} {
		if got := s.entries[i].intent; !strings.Contains(got, want) || strings.Contains(got, `{"`) || strings.Contains(got, "\x1b[") {
			t.Fatalf("generic list %d = %q, want %q", i, got, want)
		}
	}
	for _, check := range []struct {
		index int
		wants []string
	}{
		{0, []string{"Alpha: first", "Target: useful target", "Zebra: last"}},
		{1, []string{"Query: needle"}},
		{2, []string{"Prompt: delegate this"}},
		{3, []string{"Original arguments: not-json"}},
		{4, []string{"Original arguments: [\"not\",\"an\",\"object\"]"}},
	} {
		s.selected = check.index
		s.detail = true
		s.refreshDetail(&m.conv.scrollback)
		got := strings.Join(toolcallDetailLines(*s.detailEntry), "\n")
		for _, want := range check.wants {
			if !strings.Contains(got, want) {
				t.Fatalf("generic detail %d missing %q: %q", check.index, want, got)
			}
		}
	}
	s.selected = 0
	s.refreshDetail(&m.conv.scrollback)
	m = applyAll(m, client.ToolResultMsg{CallID: "unknown", Content: "provisional", Available: true, StructuredContent: `{"phase":"temporary"}`})
	if got := strings.Join(toolcallDetailLines(*s.detailEntry), "\n"); !strings.Contains(got, "provisional") || !strings.Contains(got, `{"phase":"temporary"}`) {
		t.Fatalf("provisional result changed: %q", got)
	}
	m = applyAll(m, client.ToolResultMsg{CallID: "unknown", Content: "canonical", StructuredContent: `{"phase":"final"}`})
	if got := strings.Join(toolcallDetailLines(*s.detailEntry), "\n"); !strings.Contains(got, "canonical") || strings.Contains(got, "provisional") || !strings.Contains(got, `{"phase":"final"}`) {
		t.Fatalf("canonical result changed: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario4_NestedArgumentsAndEmptyKeys(t *testing.T) {
	cases := []struct {
		name, tool, args, intent string
		want, absent             []string
	}{
		{
			name: "top-level empty key", tool: "future-tool",
			args:   `{"":1,"ok":null}`,
			intent: "future-tool",
			want:   []string{"(empty key): 1", "Ok: null"},
		},
		{
			name: "empty keys and stable order", tool: "Subagent",
			args:   `{"authority":{"z":false,"":null,"a":[{"":1,"b":true},false]},"messages":["hello",{"role":"user","content":"hi"}]}`,
			intent: "Subagent",
			want:   []string{"Authority:", "  (empty key): null", "  A:", "    [0]:", "      (empty key): 1", "      B: true", "    [1]: false", "  Z: false", "Messages:", "  [0]: hello", "  [1]:", "    Content: hi", "    Role: user"},
			absent: []string{`{"z":`, `[{`, `{"role":`},
		},
		{
			name: "nested target intent", tool: "future-tool",
			args:   `{"target":{"path":"one","revision":2},"other":[null,true,3.25],"empty":{},"items":[]}`,
			intent: "2 fields",
			want:   []string{"Target:", "  Path: one", "  Revision: 2", "Other:", "  [0]: null", "  [1]: true", "  [2]: 3.25", "Empty: (empty object)", "Items: (empty array)"},
		},
		{
			name: "array target and unicode key", tool: "mcp__lookup",
			args:   `{"target":[{"étiquette":"safe"},null],"payload":{"notes":"\u001b[31m"}}`,
			intent: "2 items",
			want:   []string{"Target:", "  [0]:", "    Étiquette: safe", "  [1]: null", "Payload:", "  Notes:"},
			absent: []string{"\x1b[31m", `[{"étiquette"`},
		},
		{
			name: "hostile controls and long nested values", tool: "Read",
			args:   `{"path":"safe","extra":{"\u001b[31m":"\u001b]8;;evil\u0007"},"content":"` + strings.Repeat("large-value-", 30) + `"}`,
			intent: "safe",
			want:   []string{"Extra:", strings.Repeat("large-value-", 30)},
			absent: []string{"\x1b[31m", "\x1b]8;;evil"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.conv.addTool("call", tc.tool, tc.args)
			m = openToolcallsForTest(t, m)
			s := toolcallsForTest(t, m)
			if got := s.entries[0].intent; !strings.Contains(got, tc.intent) || strings.Contains(got, `{"`) || strings.Contains(got, "\x1b") || ansi.StringWidth(got) > 120 {
				t.Fatalf("list intent = %q", got)
			}
			s.detail = true
			s.refreshDetail(&m.conv.scrollback)
			lines := toolcallDetailLines(*s.detailEntry)
			got := strings.Join(lines, "\n")
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("detail missing %q: %q", want, got)
				}
			}
			for _, bad := range tc.absent {
				if strings.Contains(got, bad) {
					t.Errorf("detail contains %q: %q", bad, got)
				}
			}
			if tc.name == "empty keys and stable order" && (strings.Index(got, "(empty key): null") >= strings.Index(got, "  A:") || strings.Index(got, "  A:") >= strings.Index(got, "  Z: false")) {
				t.Errorf("nested fields not sorted: %q", got)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_Scenario4_LargeListIntentDoesNotBuildDetail(t *testing.T) {
	const items = 2000
	args := `{"prompt":"delegate","messages":[` + strings.TrimSuffix(strings.Repeat(`{"content":"body"},`, items), ",") + `]}`
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("large", "Subagent", args)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if got := s.entries[0].intent; got != "delegate" || ansi.StringWidth(got) > 120 {
		t.Fatalf("large list intent = %q", got)
	}
	// Parsing the top-level object is required, but the list must not expand
	// thousands of nested detail labels on every scrollback update.
	if allocs := testing.AllocsPerRun(5, func() { _ = toolcallIntentFor("Subagent", args) }); allocs > 200 {
		t.Fatalf("list intent allocated %.0f times for %d detail items", allocs, items)
	}
	s.detail = true
	s.refreshDetail(&m.conv.scrollback)
	lines := toolcallDetailLines(*s.detailEntry)
	if !strings.Contains(strings.Join(lines[len(lines)-6:], "\n"), "[1999]:") {
		t.Fatalf("full detail lost last nested item: %q", lines[len(lines)-6:])
	}
}

func TestMecatuiToolcallsInspector_Scenario4_SectionsAndReadGutter(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	const readResult = "     1\talpha\n     2\tbeta\nplain\ttext\n1\tshort\n      0\tzero\n1234567\twide\n     3\t\x1b[31munsafe"
	m.conv.addTool("read", "Read", `{"path":"notes.txt"}`)
	m.conv.resolveTool("read", readResult, true,
		client.ContentBlock{Kind: client.ContentBlockStructuredContent, Text: `{"count":2}`},
		client.ContentBlock{Kind: client.ContentBlockResourceLink, Name: "report", URL: "mcp://reports/latest"},
	)
	s := inspectorOpenDetail(t, &m)
	lines := strings.Join(toolcallDetailLines(*s.detailEntry), "\n")
	for _, want := range []string{"✗ Read · failed", "Arguments", "Error", "Structured content", "Resources", "alpha", `{"count":2}`, "report", "mcp://reports/latest"} {
		if !strings.Contains(lines, want) {
			t.Errorf("detail lines missing %q: %q", want, lines)
		}
	}
	if !strings.Contains(lines, "     1  alpha") || !strings.Contains(lines, "     2  beta") {
		t.Errorf("Read lines lack inspector gutter: %q", lines)
	}
	if !strings.Contains(lines, "plain\ttext") || !strings.Contains(lines, "1\tshort") || !strings.Contains(lines, "      0\tzero") {
		t.Errorf("changed non-numbered Read text: %q", lines)
	}
	if !strings.Contains(lines, "1234567  wide") || strings.Contains(lines, "\x1b[31m") {
		t.Errorf("wide Read line or terminal-control sanitation: %q", lines)
	}
	if got := toolcallResultBodyLines("Shell", readResult); len(got) != 1 || !strings.Contains(got[0], "     1\talpha") || strings.Contains(got[0], "     1  alpha") {
		t.Errorf("non-Read result changed: %q", got)
	}
	for _, width := range []int{80, 54} {
		s.window = new(bounded.Viewport)
		s.width = 0
		s.follow = false
		got := inspectorDetail(t, s, width, 40)
		for _, want := range []string{"✗ Read · failed", "Arguments", "Error", "Structured content", "Resources"} {
			if !strings.Contains(got, want) {
				t.Errorf("width %d detail missing %q: %q", width, want, got)
			}
		}
	}
	entry := *s.detailEntry
	if entry.result.Body != readResult {
		t.Fatalf("inspector changed canonical result: got %q, want %q", entry.result.Body, readResult)
	}
	previous := -1
	for _, row := range []string{"✗ Read · failed", "Arguments:", "Error:", "Structured content", "Resources"} {
		pos := strings.Index(lines, row)
		if pos <= previous {
			t.Fatalf("section %q missing or out of order in %q", row, lines)
		}
		previous = pos
	}
	okResult := toolcallDetailLines(toolcallDetail{name: "Shell", state: toolcallDone, resultReceived: true, result: scrollback.ToolResult{Body: "     1\tnot a Read row"}})
	if got := strings.Join(okResult, "\n"); !strings.Contains(got, "Result:\n     1\tnot a Read row") || strings.Contains(got, "Error:") {
		t.Errorf("successful non-Read result changed: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario5_ClickSelectsVisibleCall(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 125)
	m.conv.resolveTool("call-124", strings.Repeat("detail line\n", 40), false)
	m = openToolcallsForTest(t, m)
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	m.vp.SetYOffset(5)
	beforeConversation := m.vp.YOffset()
	_ = m.View()

	s := toolcallsForTest(t, m)
	selectedBlock := s.entries[s.selected].blockID
	beforeOffset := s.list.Offset()
	for range 8 {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		m = updated.(Model)
		_ = m.View()
	}
	s = toolcallsForTest(t, m)
	if got := s.list.Offset(); got >= beforeOffset {
		t.Fatalf("wheel list offset = %d, want less than %d", got, beforeOffset)
	}
	if got := s.entries[s.selected].blockID; got != selectedBlock {
		t.Fatalf("wheel changed selected block to %d, want %d", got, selectedBlock)
	}
	for _, blockID := range s.hitItems {
		if blockID == selectedBlock {
			t.Fatal("wheel did not move selected row offscreen")
		}
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("wheel moved hidden conversation from %d to %d", beforeConversation, got)
	}
	offTailOffset := s.list.Offset()

	m = addToolcallsForTest(t, m, 1)
	_ = m.View()
	s = toolcallsForTest(t, m)
	if got := s.list.Offset(); got != offTailOffset {
		t.Fatalf("live addition moved off-tail list from %d to %d", offTailOffset, got)
	}
	if got := s.entries[s.selected].blockID; got != selectedBlock {
		t.Fatalf("live addition changed off-tail selection to %d, want %d", got, selectedBlock)
	}
	if got, want := s.list.CursorID(), fmt.Sprintf("%d", selectedBlock); got != want {
		t.Fatalf("live addition moved list cursor to %q, want %q", got, want)
	}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	s.refreshDetail(&m.conv.scrollback)
	s.Render(m.width, 16)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	detailOffset := s.window.Offset()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = updated.(Model)
	_ = m.View()
	s = toolcallsForTest(t, m)
	if got := s.window.Offset(); got >= detailOffset {
		t.Fatalf("detail wheel offset = %d, want less than %d", got, detailOffset)
	}
	if got := s.entries[s.selected].blockID; got != selectedBlock {
		t.Fatalf("detail wheel changed selected block to %d, want %d", got, selectedBlock)
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("detail wheel moved hidden conversation from %d to %d", beforeConversation, got)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	m = updated.(Model)
	view := stripANSIstr(m.View().Content)
	s = toolcallsForTest(t, m)
	if s.selected != 0 {
		t.Fatalf("Home selected %d, want oldest row", s.selected)
	}
	if !strings.Contains(view, s.entries[s.selected].intent) {
		t.Fatalf("Home left selected row offscreen: %q", view)
	}
	var clicked renderedHitRegion
	for _, region := range m.hits.frame {
		if s.hitItems[region.id] != s.entries[s.selected].blockID {
			clicked = region
			break
		}
	}
	if clicked.id == 0 {
		t.Fatal("keyboard reveal left no alternate visible row to click")
	}
	wantBlock := s.hitItems[clicked.id]
	x, y := m.metrics.localToGlobal(clicked.rect.x0, clicked.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	_ = m.View()
	s = toolcallsForTest(t, m)
	if got := s.entries[s.selected].blockID; got != wantBlock {
		t.Fatalf("click selected block %d, want %d", got, wantBlock)
	}
	if s.list.CursorID() != fmt.Sprintf("%d", wantBlock) {
		t.Fatalf("click did not reveal selected block %d", wantBlock)
	}
	if !s.detail {
		t.Fatal("click did not open detail")
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("click moved hidden conversation from %d to %d", beforeConversation, got)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.detail || s.entries[s.selected].blockID != wantBlock {
		t.Fatalf("Escape did not return click-activated detail to its selected list row: detail=%t block=%d, want %d", s.detail, s.entries[s.selected].blockID, wantBlock)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if !s.detail || s.entries[s.selected].blockID != wantBlock {
		t.Fatalf("Enter did not open selected call detail: detail=%t block=%d, want %d", s.detail, s.entries[s.selected].blockID, wantBlock)
	}
}

func TestMecatuiToolcallsInspector_Scenario5_WheelReturnsToTailAndFollowsNewCalls(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 125)
	m = openToolcallsForTest(t, m)
	_ = m.View()
	s := toolcallsForTest(t, m)
	selectedBlock := s.entries[s.selected].blockID
	for range 8 {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
		m = updated.(Model)
		_ = m.View()
	}
	if s.list.View().Below == 0 || s.listFollow {
		t.Fatalf("wheel up did not leave tail: offset=%d follow=%v", s.list.Offset(), s.listFollow)
	}
	for i := 0; i < 125 && s.list.View().Below > 0; i++ {
		updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
		m = updated.(Model)
		_ = m.View()
	}
	if s.list.View().Below != 0 || !s.listFollow || s.list.RevealPending() {
		t.Fatalf("wheel down did not restore tail without reveal: offset=%d follow=%v reveal=%v", s.list.Offset(), s.listFollow, s.list.RevealPending())
	}
	if got := s.entries[s.selected].blockID; got != selectedBlock {
		t.Fatalf("wheel changed selection from %d to %d", selectedBlock, got)
	}
	m = addToolcallsForTest(t, m, 1)
	view := m.View()
	if s.selected != len(s.entries)-1 || s.entries[s.selected].blockID == selectedBlock {
		t.Fatalf("tail reader did not select new call: selected=%d entries=%d", s.selected, len(s.entries))
	}
	newBlock := s.entries[s.selected].blockID
	if s.list.CursorID() != fmt.Sprintf("%d", newBlock) || s.list.View().Below != 0 {
		t.Fatalf("new call not at visible tail: cursor=%q below=%d", s.list.CursorID(), s.list.View().Below)
	}
	visible := false
	for _, block := range s.hitItems {
		visible = visible || block == newBlock
	}
	if !visible || !strings.Contains(stripANSIstr(view.Content), "file-125.go") {
		t.Fatalf("newest call is not rendered in list: %q", stripANSIstr(view.Content))
	}
}

func TestMecatuiToolcallsInspector_Scenario5_IndicatorRowsDoNotShiftGlobalMouseHits(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 125)
	m = openToolcallsForTest(t, m)
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	m.vp.SetYOffset(5)
	beforeConversation := m.vp.YOffset()
	view := m.View()
	if !strings.Contains(stripANSIstr(view.Content), "↑ ") {
		t.Fatalf("rendered inspector missing above indicator:\n%s", stripANSIstr(view.Content))
	}

	s := toolcallsForTest(t, m)
	beforeListOffset := s.list.Offset()
	var target renderedHitRegion
	for _, region := range m.hits.frame {
		if s.hitItems[region.id] != s.entries[s.selected].blockID {
			target = region
			break
		}
	}
	if target.id == 0 || target.rect.y0 == 0 {
		t.Fatalf("rendered inspector has no non-selected row below its above indicator: %#v", m.hits.frame)
	}
	indicatorY := target.rect.y0 - 1
	if _, ok := m.hits.at(target.rect.x0, indicatorY); ok {
		t.Fatal("above indicator unexpectedly owns a click region")
	}

	x, y := m.metrics.localToGlobal(target.rect.x0, indicatorY)
	updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if got := s.entries[s.selected].blockID; got != s.entries[len(s.entries)-1].blockID {
		t.Fatalf("above-indicator click selected block %d, want unchanged block %d", got, s.entries[len(s.entries)-1].blockID)
	}
	if got := s.list.Offset(); got != beforeListOffset {
		t.Fatalf("above-indicator click moved browser list from %d to %d", beforeListOffset, got)
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("above-indicator click moved hidden conversation from %d to %d", beforeConversation, got)
	}

	wantBlock := s.hitItems[target.id]
	x, y = m.metrics.localToGlobal(target.rect.x0, target.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if got := s.entries[s.selected].blockID; got != wantBlock {
		t.Fatalf("global row click selected block %d, want %d", got, wantBlock)
	}
	if got := s.list.Offset(); got != beforeListOffset {
		t.Fatalf("visible-row click moved browser list from %d to %d", beforeListOffset, got)
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("visible-row click moved hidden conversation from %d to %d", beforeConversation, got)
	}
}

func TestMecatuiToolcallsInspector_Scenario5_ListIndicatorsCursorAndHitsStayBounded(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 125)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)

	assertList := func(width, height int, indicator string) []ClickableRegion {
		t.Helper()
		body, regions := s.Render(width, height)
		plain := stripANSIstr(body)
		if !strings.Contains(plain, indicator) {
			t.Fatalf("%dx%d missing %q:\n%s", width, height, indicator, plain)
		}
		if got, want := len(strings.Split(body, "\n")), height; got != want {
			t.Fatalf("%dx%d rendered %d lines, want full offered region of %d", width, height, got, want)
		}
		for _, row := range strings.Split(plain, "\n") {
			if ansi.StringWidth(row) > width {
				t.Fatalf("%dx%d overflowed row %q", width, height, row)
			}
		}
		if got, want := s.list.CursorID(), fmt.Sprintf("%d", s.entries[s.selected].blockID); got != want {
			t.Fatalf("cursor ID = %q, want selected block %q", got, want)
		}
		view := s.list.ViewWithIndicators(height-4, false)
		found := false
		for _, row := range view.Rows {
			if row.Selected && row.CursorMarker {
				found = true
			}
		}
		if !found {
			t.Fatalf("selected cursor is not visible: selected=%d view=%+v", s.selected, view)
		}
		return regions
	}

	assertList(80, 12, "↑ ")
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	assertList(80, 12, "↓ ")
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	regions := assertList(44, 9, "↑ ")
	for _, region := range regions {
		blockID := s.hitItems[region.hit]
		if blockID == s.entries[s.selected].blockID {
			continue
		}
		_, handled, closed := s.HandleMsg(surfaceHitMsg{ID: region.hit})
		if !handled || closed || s.entries[s.selected].blockID != blockID {
			t.Fatalf("resized row hit selected block %d, want %d (handled=%v closed=%v)", s.entries[s.selected].blockID, blockID, handled, closed)
		}
		return
	}
	t.Fatal("resized list had no selectable non-cursor row")
}

func TestMecatuiToolcallsInspector_Scenario5_ClickIsolationAndStaleHits(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 8)
	m = openToolcallsForTest(t, m)
	_ = m.View()
	s := toolcallsForTest(t, m)
	if len(m.hits.frame) == 0 {
		t.Fatal("rendered inspector has no row hits")
	}
	stale := m.hits.frame[0].id
	before := s.selected

	for _, point := range [][2]int{{0, m.metrics.contentOrigin.y}, {0, m.metrics.contentBounds.y1 - 1}, {m.width - 1, m.metrics.contentOrigin.y + 1}} {
		updated, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: point[0], Y: point[1]})
		m = updated.(Model)
		if got := toolcallsForTest(t, m).selected; got != before {
			t.Fatalf("non-row click at %v selected %d, want %d", point, got, before)
		}
		if toolcallsForTest(t, m).detail {
			t.Fatalf("non-row click at %v opened detail", point)
		}
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 30, Height: 10})
	m = updated.(Model)
	_ = m.View()
	s = toolcallsForTest(t, m)
	s.selected = len(s.entries) - 1
	updated, _ = m.Update(surfaceHitMsg{ID: stale})
	m = updated.(Model)
	if got, want := toolcallsForTest(t, m).selected, len(s.entries)-1; got != want {
		t.Fatalf("stale hit selected %d, want %d", got, want)
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
	m = updated.(Model)
	_ = m.View()
	before = toolcallsForTest(t, m).selected
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 0, Y: m.metrics.contentOrigin.y})
	m = updated.(Model)
	if got := toolcallsForTest(t, m).selected; got != before {
		t.Fatalf("compact click selected %d, want %d", got, before)
	}

	noMouse := newToolcallsInspectorModel(t)
	noMouse.deps.NoAltScreen = true
	noMouse = addToolcallsForTest(t, noMouse, 2)
	noMouse = openToolcallsForTest(t, noMouse)
	_ = noMouse.View()
	before = toolcallsForTest(t, noMouse).selected
	region := noMouse.hits.frame[0]
	x, y := noMouse.metrics.localToGlobal(region.rect.x0, region.rect.y0)
	updated, _ = noMouse.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	noMouse = updated.(Model)
	if got := toolcallsForTest(t, noMouse).selected; got != before {
		t.Fatalf("no-mouse click selected %d, want %d", got, before)
	}

	reused := newToolcallsInspectorModel(t)
	reused.deps.NoAltScreen = false
	reused.conv.addTool("reused", "Read", `{"path":"first"}`)
	reused.conv.resolveTool("reused", "complete", false)
	reused.conv.addTool("reused", "Write", `{"path":"second"}`)
	reused = openToolcallsForTest(t, reused)
	_ = reused.View()
	rs := toolcallsForTest(t, reused)
	wantBlock := rs.entries[1].blockID
	var reusedHit renderedHitRegion
	for _, candidate := range reused.hits.frame {
		if rs.hitItems[candidate.id] == wantBlock {
			reusedHit = candidate
			break
		}
	}
	if reusedHit.id == 0 {
		t.Fatal("reused call ID has no hit for its later block")
	}
	x, y = reused.metrics.localToGlobal(reusedHit.rect.x0, reusedHit.rect.y0)
	updated, _ = reused.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	reused = updated.(Model)
	if got := toolcallsForTest(t, reused).entries[toolcallsForTest(t, reused).selected].blockID; got != wantBlock {
		t.Fatalf("reused ID click selected block %d, want later block %d", got, wantBlock)
	}
	if !toolcallsForTest(t, reused).detail {
		t.Fatal("reused ID click did not open the selected call detail")
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
