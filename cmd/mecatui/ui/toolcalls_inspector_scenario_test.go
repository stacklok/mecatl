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

func TestMecatuiToolcallsInspector_Scenario4_CoreToolPresentation(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	calls := []struct {
		id, name, args, wantIntent string
	}{
		{"read", "Read", `{"path":"src/main.go","offset":12,"limit":20,"extra":"kept"}`, "Read src/main.go"},
		{"list", "ListDir", `{"path":"src","depth":2}`, "List src"},
		{"glob", "Glob", `{"pattern":"**/*.go","path":"cmd"}`, "Find **/*.go"},
		{"grep", "Grep", `{"pattern":"TODO","path":"cmd"}`, "Search TODO"},
		{"edit", "Edit", `{"path":"a.go","old_string":"old","new_string":"new","extra":true}`, "Edit a.go"},
		{"write", "Write", `{"path":"a.go","content":"complete replacement"}`, "Write a.go"},
		{"copy", "Copy", `{"source":"a.go","destination":"b.go"}`, "Copy a.go → b.go"},
		{"move", "Move", `{"source":"a.go","destination":"b.go"}`, "Move a.go → b.go"},
		{"remove", "Remove", `{"path":"old.go"}`, "Remove old.go"},
		{"shell", "Shell", `{"command":"go test ./..."}`, "Run go test ./..."},
		{"web", "WebFetch", `{"url":"https://example.invalid/docs"}`, "Fetch https://example.invalid/docs"},
		{"resource", "FetchMcpResource", `{"uri":"mcp://docs/readme"}`, "Fetch mcp://docs/readme"},
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
	for i, want := range []string{"future-tool useful target", "mcp__docs__lookup needle", "Subagent delegate this", "odd", "odd-array"} {
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
			intent: "future-tool 2 fields",
			want:   []string{"Target:", "  Path: one", "  Revision: 2", "Other:", "  [0]: null", "  [1]: true", "  [2]: 3.25", "Empty: (empty object)", "Items: (empty array)"},
		},
		{
			name: "array target and unicode key", tool: "mcp__lookup",
			args:   `{"target":[{"étiquette":"safe"},null],"payload":{"notes":"\u001b[31m"}}`,
			intent: "mcp__lookup 2 items",
			want:   []string{"Target:", "  [0]:", "    Étiquette: safe", "  [1]: null", "Payload:", "  Notes:"},
			absent: []string{"\x1b[31m", `[{"étiquette"`},
		},
		{
			name: "hostile controls and long nested values", tool: "Read",
			args:   `{"path":"safe","extra":{"\u001b[31m":"\u001b]8;;evil\u0007"},"content":"` + strings.Repeat("large-value-", 30) + `"}`,
			intent: "Read safe",
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
