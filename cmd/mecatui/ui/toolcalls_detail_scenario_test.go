package ui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func inspectorDetail(t *testing.T, s *toolcallsState, width, height int) string {
	t.Helper()
	body, _ := s.Render(width, height)
	return stripANSIstr(body)
}

func inspectorOpenDetail(t *testing.T, m *Model) *toolcallsState {
	t.Helper()
	*m = openToolcallsForTest(t, *m)
	s := toolcallsForTest(t, *m)
	s.Render(70, 12)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	*m = updated.(Model)
	return toolcallsForTest(t, *m)
}

func TestMecatuiToolcallsInspector_DetailUsesToolPaletteAndKeepsResultsFlush(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("styled", "Read", `{"path":"safe\u001b[31m","options":{"mode":"full"}}`)
	m.conv.resolveTool("styled", "a long result line\n     1\twith a Read gutter", false)
	s := inspectorOpenDetail(t, &m)
	s.refreshDetail(&m.conv.scrollback)

	styled := strings.Join(s.styledToolcallDetailLines(*s.detailEntry), "\n")
	if strings.Contains(styled, "\x1b[31m") || strings.Contains(styled, "\x1b]") {
		t.Fatalf("untrusted terminal control reached styled detail: %q", styled)
	}
	if !strings.Contains(styled, s.deps.theme.Style("toolName").Render("  Path:")+s.deps.theme.Style("toolArgs").Render(" safe[31m")) {
		t.Fatalf("argument key/value did not use semantic contrast: %q", styled)
	}
	if !strings.Contains(stripANSIstr(styled), "Arguments:\n  Path: safe[31m\n  Options:\n    Mode: full") {
		t.Fatalf("arguments are not indented as labeled rows: %q", stripANSIstr(styled))
	}
	if !strings.Contains(stripANSIstr(styled), "Result:\na long result line\n     1  with a Read gutter") {
		t.Fatalf("result text was indented or Read gutter changed: %q", stripANSIstr(styled))
	}
	if !strings.Contains(styled, s.deps.theme.Style("toolArgs").Render("a long result line")) {
		t.Fatalf("result does not use the muted tool-args style: %q", styled)
	}

	for _, width := range []int{38, 24} {
		body := inspectorDetail(t, s, width, 12)
		for _, row := range strings.Split(body, "\n") {
			if ansi.StringWidth(row) > width {
				t.Fatalf("width %d overflowed row %q", width, row)
			}
		}
	}
}

func TestMecatuiToolcallsInspector_DetailWrapsResultsFlushLeftAcrossThemes(t *testing.T) {
	registry := theme.NewRegistry()
	for _, name := range []string{"aztec", "mono", "solar"} {
		t.Run(name, func(t *testing.T) {
			th, ok := registry.Get(name)
			if !ok {
				t.Fatalf("missing theme %q", name)
			}
			m := newToolcallsInspectorModel(t)
			m.deps.Theme = th
			m.conv.addTool("wrapped", "Shell", `{"command":"echo wrapped"}`)
			m.conv.resolveTool("wrapped", strings.Repeat("wrapped-result-token ", 12), false)
			s := inspectorOpenDetail(t, &m)
			s.refreshDetail(&m.conv.scrollback)

			const width = 55
			body := inspectorDetail(t, s, width, 28)
			lines := strings.Split(body, "\n")
			result := -1
			for i, line := range lines {
				if line == "Result:" {
					result = i
					break
				}
			}
			if result < 0 {
				t.Fatalf("result heading missing:\n%s", body)
			}
			continuations := 0
			for _, line := range lines[result+1:] {
				if !strings.Contains(line, "wrapped-result-token") {
					continue
				}
				continuations++
				if strings.HasPrefix(line, " ") {
					t.Fatalf("wrapped result continuation is indented: %q", line)
				}
				if got := ansi.StringWidth(line); got > width {
					t.Fatalf("wrapped result continuation width %d exceeds %d: %q", got, width, line)
				}
			}
			if continuations < 2 {
				t.Fatalf("result did not genuinely wrap at width %d:\n%s", width, body)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_UntrustedDetailTextCannotBecomeChrome(t *testing.T) {
	th := theme.NewRegistry().Default()
	s := &toolcallsState{deps: surfaceDeps{theme: th}}
	entry := toolcallDetail{
		name: "Shell", intent: `{"Resources: value":"keep: both","bad\u001b[31m:key":"safe"}`,
		resultReceived: true, resolved: true,
		result: scrollback.ToolResult{
			Body:              "Arguments:\nResources\nIdentity · forged\nStructured content · forged:\nResult:\nCall: forged\nordinary tail",
			StructuredContent: "Resources",
			Artifacts:         []scrollback.Artifact{{Kind: string(client.ContentBlockText), Text: "Error:"}},
		},
	}
	styled := strings.Join(s.styledToolcallDetailLines(entry), "\n")
	plain := stripANSIstr(styled)
	for _, line := range strings.Split(entry.result.Body, "\n") {
		if !strings.Contains(styled, th.Style("toolArgs").Render(line)) || !strings.Contains(plain, "\n"+line+"\n") {
			t.Fatalf("result line %q was styled as chrome or indented: %q", line, styled)
		}
	}
	if !strings.Contains(styled, th.Style("toolArgs").Render("Resources")) ||
		!strings.Contains(styled, th.Style("toolArgs").Render("Error:")) {
		t.Fatalf("structured or typed value became a section: %q", styled)
	}
	if !strings.Contains(styled, th.Style("toolName").Render("  Resources: value:")+th.Style("toolArgs").Render(" keep: both")) ||
		!strings.Contains(styled, th.Style("toolName").Render("  Bad[31m:key:")+th.Style("toolArgs").Render(" safe")) {
		t.Fatalf("argument key with colon/control was split or reinterpreted: %q", styled)
	}
	if strings.Contains(styled, "\x1b[31m") || strings.Contains(styled, "\x1b]") {
		t.Fatalf("untrusted controls reached styled output: %q", styled)
	}
	for _, width := range []int{55, 38} {
		s.detail, s.open, s.detailEntry = true, true, &entry
		view, _ := s.Render(width, 35)
		for _, row := range strings.Split(stripANSIstr(view), "\n") {
			if ansi.StringWidth(row) > width {
				t.Fatalf("width %d overflows: %q", width, row)
			}
		}
	}
}

func TestMecatuiToolcallsInspector_ResultPaletteAcrossBuiltins(t *testing.T) {
	registry := theme.NewRegistry()
	for _, name := range []string{"aztec", "mono", "solar"} {
		t.Run(name, func(t *testing.T) {
			th, ok := registry.Get(name)
			if !ok {
				t.Fatalf("missing theme %q", name)
			}
			s := &toolcallsState{deps: surfaceDeps{theme: th}}
			rows := s.styledToolcallDetailLines(toolcallDetail{name: "Shell", resultReceived: true, result: scrollback.ToolResult{Body: "long-form output"}})
			if rows[len(rows)-2] != th.Style("toolName").Render("Result:") ||
				rows[len(rows)-1] != th.Style("toolArgs").Render("long-form output") ||
				rows[len(rows)-1] == th.Style("viewport").Render("long-form output") {
				t.Fatalf("result does not match muted tool palette: %q", rows)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_ListUsesToolPaletteAcrossBuiltins(t *testing.T) {
	registry := theme.NewRegistry()
	for _, name := range []string{"aztec", "mono", "solar"} {
		t.Run(name, func(t *testing.T) {
			th, ok := registry.Get(name)
			if !ok {
				t.Fatalf("missing built-in theme %q", name)
			}
			m := newToolcallsInspectorModel(t)
			m.deps.Theme = th
			m.conv.addTool("call", "Read", `{"path":"palette.go"}`)
			m = openToolcallsForTest(t, m)
			s := toolcallsForTest(t, m)
			body, _ := s.Render(100, 10)
			selected := renderToolcallListRow(presentListRow(s.list.ViewWithIndicators(6, false).Rows[0], th.Style("toolName"), th.Style("toolArgs")), th.Style("toolName"))
			if !strings.Contains(body, selected) {
				t.Fatalf("selected row does not use tool-name palette: %q", body)
			}
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
			m.conv.addTool("later", "Read", `{"path":"later.go"}`)
			m.syncToolcalls()
			body, _ = s.Render(100, 10)
			row := s.list.ViewWithIndicators(6, false).Rows[0]
			unselected := renderToolcallListRow(presentListRow(row, th.Style("toolName"), th.Style("toolArgs")), th.Style("toolName"))
			if row.Selected || !strings.Contains(body, unselected) {
				t.Fatalf("unselected row does not use tool-args palette: row=%+v body=%q", row, body)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_StatusGlyphsAcrossThemes(t *testing.T) {
	registry := theme.NewRegistry()
	for _, name := range []string{"aztec", "mono", "solar"} {
		t.Run(name, func(t *testing.T) {
			th, ok := registry.Get(name)
			if !ok {
				t.Fatalf("missing built-in theme %q", name)
			}
			m := newToolcallsInspectorModel(t)
			m.deps.Theme = th
			m = applyAll(m, client.ToolCallMsg{ID: "lifecycle", Name: "Read", Args: `{"path":"lifecycle.go"}`})
			m = openToolcallsForTest(t, m)
			s := toolcallsForTest(t, m)
			selected := s.entries[s.selected].blockID

			assertList := func(glyph, status, slot string) {
				t.Helper()
				body := m.View().Content
				if plain := stripANSIstr(body); !strings.Contains(plain, glyph+" "+status) {
					t.Fatalf("list lacks non-color status %q %q:\n%s", glyph, status, plain)
				}
				if !strings.Contains(body, th.Style(slot).Render(glyph)) {
					t.Fatalf("list glyph %q does not use %s: %q", glyph, slot, body)
				}
				if got := toolcallsForTest(t, m).entries[toolcallsForTest(t, m).selected].blockID; got != selected {
					t.Fatalf("lifecycle update selected block %d, want %d", got, selected)
				}
			}

			m = applyAll(m, tea.WindowSizeMsg{Width: 46, Height: 30})
			assertDetail := func(glyph, status, slot string) string {
				t.Helper()
				m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEnter})
				s = toolcallsForTest(t, m)
				if !s.detail {
					t.Fatal("Enter did not open the selected call detail")
				}
				detail := m.View().Content
				if s.compact {
					t.Fatalf("narrow detail unexpectedly used compact fallback: %q", detail)
				}
				if plain := stripANSIstr(detail); !strings.Contains(plain, "Identity · "+glyph+" Read · "+status) {
					t.Fatalf("detail lacks non-color status %q: %q", status, plain)
				}
				if !strings.Contains(detail, th.Style(slot).Render(glyph)) || !strings.Contains(detail, th.Style(slot).Render(status)) {
					t.Fatalf("detail status %q does not use %s: %q", status, slot, detail)
				}
				if got := s.entries[s.selected].blockID; got != selected {
					t.Fatalf("detail selected block %d, want %d", got, selected)
				}
				return stripANSIstr(detail)
			}

			assertList("…", "running", "toolName")
			assertDetail("…", "running", "toolName")
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			m = applyAll(m, client.ToolResultMsg{CallID: "lifecycle", Content: "provisional failure", Available: true, IsError: true})
			assertList("✗", "failed", "toolErr")
			if detail := assertDetail("✗", "failed", "toolErr"); !strings.Contains(detail, "provisional failure") {
				t.Fatalf("provisional result missing from detail: %q", detail)
			}
			m = applyAll(m, tea.KeyPressMsg{Code: tea.KeyEsc})
			m = applyAll(m, client.ToolResultMsg{CallID: "lifecycle", Content: "canonical success"})
			assertList("✓", "done", "toolOk")
			if detail := assertDetail("✓", "done", "toolOk"); strings.Contains(detail, "provisional failure") || !strings.Contains(detail, "canonical success") {
				t.Fatalf("canonical detail lifecycle state: %q", detail)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_Scenario2_LiveResultAndStatus(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.conv.addTool("first", "Read", `{"path":"first"}`)
	m.conv.addTool("second", "Edit", `{"path":"second"}`)
	s := inspectorOpenDetail(t, &m)
	pending := inspectorDetail(t, s, 70, 12)
	if !strings.Contains(pending, "Edit") || !strings.Contains(pending, "Call: second") || !strings.Contains(pending, "Path: second") || !strings.Contains(pending, "running") {
		t.Fatalf("pending detail: %q", pending)
	}
	m = applyAll(m, client.ToolResultMsg{CallID: "second", Content: "temporary output", Available: true, IsError: true})
	provisional := inspectorDetail(t, s, 70, 12)
	if !strings.Contains(provisional, "temporary output") || !strings.Contains(provisional, "failed") {
		t.Fatalf("provisional detail: %q", provisional)
	}
	m = applyAll(m, client.ToolResultMsg{CallID: "second", Content: "canonical output"})
	final := inspectorDetail(t, s, 70, 12)
	if !strings.Contains(final, "canonical output") || !strings.Contains(final, "done") || strings.Contains(final, "temporary output") {
		t.Fatalf("canonical detail: %q", final)
	}
	m.conv.addTool("third", "Write", `{"path":"third"}`)
	m.syncToolcalls()
	if got := inspectorDetail(t, s, 70, 12); !strings.Contains(got, "Edit") || strings.Contains(got, `"third"`) {
		t.Fatalf("detail switched calls: %q", got)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	s.Render(70, 12)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = applyAll(m, client.ToolResultMsg{CallID: "third", Content: "write failed", IsError: true})
	if got := inspectorDetail(t, s, 70, 12); !strings.Contains(got, "failed") || !strings.Contains(got, "write failed") {
		t.Fatalf("canonical failed result: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario2_FullScrollableDetail(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	args := strings.Join([]string{"first-argument", strings.Repeat("longargument", 12), "last-argument"}, "\n")
	m.conv.addTool("edit", "Edit", args)
	s := inspectorOpenDetail(t, &m)
	initial := inspectorDetail(t, s, 70, 9)
	if !strings.Contains(initial, "first-argument") {
		t.Fatalf("first argument missing: %q", initial)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := inspectorDetail(t, s, 70, 9); !strings.Contains(got, "last-argument") {
		t.Fatalf("last argument unreachable: %q", got)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	m = applyAll(m, client.ToolResultMsg{CallID: "edit", Content: strings.Repeat("diff line\n", 40) + "final-diff-line"})
	if got := inspectorDetail(t, s, 70, 9); !strings.Contains(got, "first-argument") || strings.Contains(got, "final-diff-line") {
		t.Fatalf("lost reading position: %q", got)
	}
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := inspectorDetail(t, s, 70, 9); !strings.Contains(got, "final-diff-line") {
		t.Fatalf("last diff unreachable: %q", got)
	}
	m.conv.addTool("more", "Write", "other")
	m.syncToolcalls()
	s.Render(35, 9)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if s.selected != 0 || s.detail {
		t.Fatalf("escape lost selected call: selected=%d detail=%v", s.selected, s.detail)
	}
	// The list is still anchored at the same selected row after a detail resize.
	if got := inspectorDetail(t, s, 50, 11); !strings.Contains(got, "Edit") {
		t.Fatalf("list lost selection: %q", got)
	}

	m2 := newToolcallsInspectorModel(t)
	m2.conv.addTool("write", "Write", strings.Repeat("argument\n", 12))
	s2 := inspectorOpenDetail(t, &m2)
	inspectorDetail(t, s2, 55, 8)
	s2.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	inspectorDetail(t, s2, 55, 8)
	m2 = applyAll(m2, client.ToolResultMsg{CallID: "write", Content: "appended output marker"})
	if got := inspectorDetail(t, s2, 55, 8); !strings.Contains(got, "appended output marker") {
		t.Fatalf("tail reader did not follow appended result: %q", got)
	}
	s2.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	before := inspectorDetail(t, s2, 55, 8)
	if got := inspectorDetail(t, s2, 45, 10); !strings.Contains(got, "argument") || strings.Contains(got, "appended output marker") || !strings.Contains(before, "argument") {
		t.Fatalf("resize moved non-following reader: before=%q after=%q", before, got)
	}

	m4 := newToolcallsInspectorModel(t)
	m4 = addToolcallsForTest(t, m4, 25)
	m4 = openToolcallsForTest(t, m4)
	s4 := toolcallsForTest(t, m4)
	s4.Render(70, 8)
	s4.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	s4.Render(70, 8)
	listOffset, selected := s4.list.Offset(), s4.selected
	s4.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m4.syncToolcalls()
	m4 = addToolcallsForTest(t, m4, 1)
	s4.Render(70, 8)
	s4.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	s4.Render(70, 8)
	if s4.selected != selected || s4.list.Offset() != listOffset {
		t.Fatalf("return from detail changed list window: selected=%d (want %d), offset=%d (want %d)", s4.selected, selected, s4.list.Offset(), listOffset)
	}

	m3 := newToolcallsInspectorModel(t)
	m3.conv.addTool("reflow", "Read", "start\n"+strings.Repeat("wide", 25)+"\nanchor-row\n"+strings.Repeat("after\n", 80))
	s3 := inspectorOpenDetail(t, &m3)
	inspectorDetail(t, s3, 70, 8)
	for i := 0; i < 7; i++ {
		s3.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	at := inspectorDetail(t, s3, 70, 8)
	if !strings.Contains(strings.Split(at, "\n")[2], "anchor-row") {
		t.Fatalf("anchor setup failed: %q", at)
	}
	resized := inspectorDetail(t, s3, 55, 8)
	if !strings.Contains(strings.Split(resized, "\n")[2], "anchor-row") {
		t.Fatalf("narrow reflow shifted reading position: before=%q after=%q", at, resized)
	}
	wide := inspectorDetail(t, s3, 90, 8)
	if !strings.Contains(strings.Split(wide, "\n")[2], "anchor-row") {
		t.Fatalf("wide reflow shifted reading position: before=%q after=%q", resized, wide)
	}
	m3 = applyAll(m3, client.ToolResultMsg{CallID: "reflow", Available: true, Content: strings.Repeat("streamed-line\n", 20)})
	reading := inspectorDetail(t, s3, 90, 8)
	if !strings.Contains(strings.Split(reading, "\n")[2], "anchor-row") || strings.Contains(reading, "streamed-line") {
		t.Fatalf("append after resize displaced non-following reader: before=%q after=%q", wide, reading)
	}
	s3.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := inspectorDetail(t, s3, 90, 8); !strings.Contains(got, "streamed-line") {
		t.Fatalf("streamed tail unreachable: %q", got)
	}
	m3 = applyAll(m3, client.ToolResultMsg{CallID: "reflow", Content: strings.Repeat("streamed-line\n", 20) + "canonical-tail"})
	if got := inspectorDetail(t, s3, 90, 8); !strings.Contains(got, "canonical-tail") || strings.Contains(got, "Result: pending") {
		t.Fatalf("following reader lost canonical appended tail: %q", got)
	}

	m5 := newToolcallsInspectorModel(t)
	var wrapped strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&wrapped, "%04d", i)
	}
	m5.conv.addTool("wrapped", "Read", wrapped.String())
	s5 := inspectorOpenDetail(t, &m5)
	inspectorDetail(t, s5, 70, 8)
	for i := 0; i < 10; i++ {
		s5.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	original := strings.Split(inspectorDetail(t, s5, 70, 8), "\n")[2]
	marker := original[2:6]
	for _, w := range []int{55, 90, 70} {
		got := inspectorDetail(t, s5, w, 8)
		if !strings.Contains(strings.ReplaceAll(got, "\n", ""), marker) {
			t.Fatalf("wrapped text anchor moved at width %d: before=%q after=%q", w, original, got)
		}
	}
}

func TestMecatuiToolcallsInspector_Scenario2_CanonicalDetailOnly(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	long := strings.Repeat("heavy-argument-", 500)
	m.conv.addTool("first", "Read", long)
	m.conv.addTool("reused", "Read", "original")
	m.conv.resolveTool("first", strings.Repeat("large-result", 1000), false, client.ContentBlock{Kind: client.ContentBlockImage, Data: []byte(strings.Repeat("bytes", 1000))})
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if _, ok := reflect.TypeOf(toolcallEntry{}).FieldByName("result"); ok {
		t.Fatal("list entries retain full historical results instead of canonical scrollback")
	}
	for _, entry := range s.entries {
		if len(entry.intent) > 128 {
			t.Fatalf("list retained full arguments: %d bytes", len(entry.intent))
		}
	}
	s.Render(70, 12)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.syncToolcalls() // the owning reducer refreshes the selected snapshot on entry
	if got := inspectorDetail(t, s, 70, 12); !strings.Contains(got, "Call: reused") || !strings.Contains(got, "original") {
		t.Fatalf("selected pending detail: %q", got)
	}
	m.conv.resolveTool("reused", "old result", false)
	m.syncToolcalls()
	m.conv.addTool("reused", "Write", "new arguments")
	m.syncToolcalls()
	m.conv.resolveTool("reused", "new result", false)
	m.syncToolcalls()
	got := inspectorDetail(t, s, 70, 12)
	if !strings.Contains(got, "old result") || strings.Contains(got, "new result") || strings.Contains(got, "new arguments") {
		t.Fatalf("selected block changed when call ID was reused: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario2_FinishBeforeResult(t *testing.T) {
	cases := []struct {
		name          string
		start, finish tea.Msg
	}{
		{"Parallel", nil, client.ParallelMsg{Kind: client.ParallelEnd, ParentCallID: "call", Stop: "error"}},
		{"Subagent", client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "call", ChildID: "child"}, client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "call", ChildID: "child", Stop: "error"}},
		{"Team", client.TeamMsg{Kind: client.TeamStart, ParentCallID: "call", TeamID: "team"}, client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "call", TeamID: "team", Stop: "error"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.phase = phaseRunning
			m.conv.addTool("call", tc.name, `{}`)
			if tc.start != nil {
				m = applyAll(m, tc.start)
			}
			m = openToolcallsForTest(t, m)
			s := toolcallsForTest(t, m)
			s.Render(70, 12)
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			m.syncToolcalls()
			m = applyAll(m, tc.finish)
			if got := inspectorDetail(t, s, 70, 12); !strings.Contains(got, "failed") || !strings.Contains(got, "Result: pending") {
				t.Fatalf("finished-before-result not reflected: %q", got)
			}
			m = applyAll(m, client.ToolResultMsg{CallID: "call", Content: "canonical"})
			if got := inspectorDetail(t, s, 70, 12); !strings.Contains(got, "canonical") {
				t.Fatalf("canonical result missing after terminal event: %q", got)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_Scenario2_StructuredAndTypedResults(t *testing.T) {
	for _, json := range []string{`{"count":2}`, `[1,2]`, `true`} {
		t.Run(json, func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.conv.addTool("typed", "Search", `{}`)
			m.conv.scrollback.Tools().Resolve("typed", scrollback.ToolResult{Body: "human explanation", StructuredContent: `"STALE_FIELD_MIRROR"`, Artifacts: []scrollback.Artifact{
				{Kind: string(client.ContentBlockText), Text: "human explanation"},
				{Kind: string(client.ContentBlockStructuredContent), Text: json},
				{Kind: string(client.ContentBlockResourceLink), Name: "report\x1b]8;;bad\a", URL: "https://example.com/report\x1b[31m"},
				{Kind: string(client.ContentBlockEmbeddedResource), Text: "embedded explanation"},
				{Kind: string(client.ContentBlockImage), MIMEType: "image/png", Data: []byte("RAW_IMAGE_SECRET")},
				{Kind: string(client.ContentBlockAudio), MIMEType: "audio/wav", Data: []byte("RAW_AUDIO_SECRET")},
				{Kind: string(client.ContentBlockEmbeddedResource), MIMEType: "application/octet-stream", Data: []byte("RAW_BLOB_SECRET")},
			}})
			s := inspectorOpenDetail(t, &m)
			inspectorDetail(t, s, 70, 12)
			// Probe the full detail via page navigation, not just the visible tail.
			var pages string
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
			for i := 0; i < 12; i++ {
				pages += inspectorDetail(t, s, 70, 12)
				s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
			}
			for _, want := range []string{"human explanation", "Structured JSON", json, "https://example.com/report", "embedded explanation", "image/png", "audio/wav", "application/octet-stream"} {
				if !strings.Contains(pages, want) {
					t.Errorf("missing %q in detail %q", want, pages)
				}
			}
			for _, forbidden := range []string{"RAW_IMAGE_SECRET", "RAW_AUDIO_SECRET", "RAW_BLOB_SECRET", "\x1b]8;;bad", "\x1b[31m"} {
				if strings.Contains(pages, forbidden) {
					t.Errorf("untrusted/binary %q in detail %q", forbidden, pages)
				}
			}
			if n := strings.Count(pages, "human explanation"); n == 0 {
				t.Errorf("missing distinct text")
			}
			full := inspectorDetail(t, s, 100, 28)
			raw, _ := s.Render(100, 28)
			if strings.Contains(raw, "\x1b]8;;bad") || strings.Contains(raw, "\x1b[31m") {
				t.Errorf("unsanitized terminal controls in detail: %q", raw)
			}
			if strings.Contains(full, "STALE_FIELD_MIRROR") || strings.Count(full, "human explanation") != 1 {
				t.Errorf("typed block did not supersede field or repeated text mirror: %q", full)
			}
		})
	}
}

func TestMecatuiToolcallsInspector_Scenario2_ResumedFieldOnlyStructuredResult(t *testing.T) {
	for _, json := range []string{`{"field":1}`, `["array"]`, `42`} {
		t.Run(fmt.Sprintf("json=%s", json), func(t *testing.T) {
			msg := client.ToolResultMsg{CallID: "field", Content: "distinct text", StructuredContent: json}
			m := newToolcallsInspectorModel(t)
			m.conv.addTool("field", "Read", `{}`)
			s := inspectorOpenDetail(t, &m)
			m = applyAll(m, msg)
			live := inspectorDetail(t, s, 100, 18)
			if !strings.Contains(live, "Structured JSON") || !strings.Contains(live, json) || !strings.Contains(live, "distinct text") {
				t.Errorf("live field-only result: %q", live)
			}
			resumed := conversationFromTranscript([]client.ConversationMessage{{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "field", Name: "Read", Args: `{}`}}}, {Role: "tool", ToolResult: &client.ConvToolResult{CallID: "field", Content: "distinct text", StructuredContent: json}}})
			m.conv = resumed
			s = inspectorOpenDetail(t, &m)
			got := inspectorDetail(t, s, 100, 18)
			if !strings.Contains(got, "Structured JSON") || !strings.Contains(got, json) || !strings.Contains(got, "distinct text") {
				t.Errorf("rehydrated field-only result: %q", got)
			}
		})
	}
}
