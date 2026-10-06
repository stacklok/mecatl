package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiToolcallPreviews_Scenario1_EditArgumentParity(t *testing.T) {
	const args = `{"path":"pkg/example.go","old_string":"old one\nold two","new_string":"new one\nnew two","replace_all":true,"extra":"retained"}`
	m := newToolcallsInspectorModel(t)
	m = applyAll(m, client.ToolCallMsg{ID: "edit", Name: "Edit", Args: args})
	m = applyAll(m, client.ToolResultMsg{CallID: "edit", Content: "complete result"})
	s := inspectorOpenDetail(t, &m)
	s.refreshDetail(&m.conv.scrollback)

	rows := s.styledToolcallDetailLines(*s.detailEntry)
	styled := strings.Join(rows, "\n")
	plain := stripANSIstr(styled)
	for _, want := range []string{"Edit request:", "pkg/example.go  -2 +2 (replace all)", "- old one", "- old two", "+ new one", "+ new two", "Replace all: true", "Extra: retained", "complete result"} {
		if !strings.Contains(plain, want) {
			t.Errorf("detail missing %q:\n%s", want, plain)
		}
	}
	if !strings.Contains(styled, s.deps.theme.Style("diffRemove").Render("- old one")) ||
		!strings.Contains(styled, s.deps.theme.Style("diffAdd").Render("+ new one")) {
		t.Fatalf("Edit request sides do not retain contrasting diff styles: %q", styled)
	}

	inline, ok := m.rend.renderToolDiff("Edit", args, true)
	if !ok {
		t.Fatal("inline Edit request did not render")
	}
	for _, want := range []string{"pkg/example.go  -2 +2 (replace all)", "- old one", "+ new one"} {
		if !strings.Contains(stripANSIstr(inline), want) || !strings.Contains(plain, want) {
			t.Errorf("inline and inspector disagree about %q: inline=%q detail=%q", want, stripANSIstr(inline), plain)
		}
	}
}

func TestMecatuiToolcallPreviews_Scenario1_EditLifecycleAndSafety(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = applyAll(m, client.ToolCallMsg{ID: "pending", Name: "Edit", Args: `{"path":"pending.go","old_string":"old","new_string":"new"}`})
	m = applyAll(m, client.ToolCallMsg{ID: "failed", Name: "Edit", Args: `{"path":"failed.go","old_string":"old","new_string":"new"}`})
	m = applyAll(m, client.ToolResultMsg{CallID: "failed", Content: "failed result", IsError: true})
	m = applyAll(m, client.ToolCallMsg{ID: "provisional", Name: "Edit", Args: `{"path":"provisional.go","old_string":"old","new_string":"new"}`})
	m = applyAll(m, client.ToolResultMsg{CallID: "provisional", Content: "provisional result", Available: true})
	m = applyAll(m, client.ToolCallMsg{ID: "canonical", Name: "Edit", Args: `{"path":"canonical.go","old_string":"old","new_string":"new"}`})
	m = applyAll(m, client.ToolResultMsg{CallID: "canonical", Content: "canonical result"})

	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if len(s.entries) != 4 {
		t.Fatalf("entries = %d, want 4", len(s.entries))
	}
	for i, want := range []string{"Result: pending", "failed result", "provisional result", "canonical result"} {
		s.selected = i
		s.detail = true
		s.refreshDetail(&m.conv.scrollback)
		body := inspectorDetail(t, s, 80, 24)
		if !strings.Contains(body, want) {
			t.Errorf("Edit detail %d missing its independent lifecycle/result %q:\n%s", i, want, body)
		}
		s.detail = false
	}

	resumed := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context(), Resume: &client.ResumeSelection{
		Row: client.SessionListItem{ID: "resumed"},
		Transcript: client.SessionTranscript{Messages: []client.ConversationMessage{
			{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "resumed-edit", Name: "Edit", Args: `{"path":"resumed.go","old_string":"old","new_string":"new"}`}}},
			{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "resumed-edit", Content: "resumed result"}},
		}},
	}})
	resumed.width, resumed.height = 80, 24
	resumed.relayout()
	resumed = openToolcallsForTest(t, resumed)
	resumedInspector := toolcallsForTest(t, resumed)
	resumedInspector.selected, resumedInspector.detail = 0, true
	resumedInspector.refreshDetail(&resumed.conv.scrollback)
	if body := inspectorDetail(t, resumedInspector, 80, 24); !strings.Contains(body, "resumed result") || !strings.Contains(body, "Edit request:") {
		t.Fatalf("resumed Edit detail lost request or result: %q", body)
	}

	malformed := toolcallDetailLines(toolcallDetail{name: "Edit", intent: "not\x1b[31m json", state: toolcallPending})
	if got := strings.Join(malformed, "\n"); !strings.Contains(got, "Original arguments: not[31m json") {
		t.Fatalf("malformed Edit args lost readable fallback: %q", got)
	}

	hostile := toolcallDetail{name: "Edit", intent: `{"path":"a\u001b[2J.go","old_string":"` + strings.Repeat("old", 120) + `","new_string":"new\u001b[2J"}`, state: toolcallPending}
	s.detail, s.detailEntry, s.open = true, &hostile, true
	for _, width := range []int{55, 70} {
		body := inspectorDetail(t, s, width, 24)
		if strings.Contains(body, "\x1b[2J") || !strings.Contains(strings.Join(toolcallDetailLines(hostile), "\n"), "Path: a[2J.go") {
			t.Fatalf("width %d hid reachable safe Edit arguments: %q", width, body)
		}
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
		body = inspectorDetail(t, s, width, 24)
		if !strings.Contains(body, "Edit request:") || !strings.Contains(body, "- old") {
			t.Fatalf("width %d hid reachable safe Edit request: %q", width, body)
		}
	}
}
