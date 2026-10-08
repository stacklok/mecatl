package ui

import (
	"slices"
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
	for _, repeated := range []string{"Old string:", "New string:"} {
		if strings.Contains(plain, repeated) {
			t.Errorf("Edit request is rendered twice via %q:\n%s", repeated, plain)
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

	// The diff, not a second pair of generic fields, must preserve terminal
	// newlines and trailing blank lines in the received replacement strings.
	for _, tc := range []struct {
		args string
		want []string
	}{
		{`{"path":"blank.go","old_string":"old\n\n","new_string":"new"}`, []string{"blank.go  -2 +1", "- old", "- ", "+ new", `\ No newline at end of added text`}},
		{`{"path":"newline.go","old_string":"\n","new_string":"new\n"}`, []string{"newline.go  -1 +1", "- ", "+ new"}},
		{`{"path":"trailing.go","old_string":"old\n","new_string":"new\n\n"}`, []string{"trailing.go  -1 +2", "- old", "+ new", "+ "}},
	} {
		rows := toolcallDetailLines(toolcallDetail{name: "Edit", intent: tc.args})
		start, end := slices.Index(rows, "Edit request:"), slices.Index(rows, "Arguments:")
		if start < 0 || end < start || !slices.Equal(rows[start:end], append([]string{"Edit request:"}, tc.want...)) {
			t.Errorf("lossless diff rows = %q, want %q", rows, tc.want)
		}
		got := strings.Join(rows, "\n")
		if strings.Contains(got, "Old string:") || strings.Contains(got, "New string:") {
			t.Errorf("lossless diff also duplicates its arguments: %q", got)
		}
	}

	// An Edit-shaped payload is only an Edit request when the received tool name
	// is Edit; other top-level calls retain their complete generic arguments.
	nonEdit := toolcallDetailLines(toolcallDetail{name: "Write", intent: args, state: toolcallPending})
	nonEditDetail := strings.Join(nonEdit, "\n")
	if strings.Contains(nonEditDetail, "Edit request:") {
		t.Fatalf("non-Edit call was labeled as an Edit request: %q", nonEditDetail)
	}
	for _, want := range []string{"Path: pkg/example.go", "Old string: old one", "New string: new one", "Replace all: true", "Extra: retained", "Result: pending"} {
		if !strings.Contains(nonEditDetail, want) {
			t.Errorf("non-Edit detail lost generic field %q: %q", want, nonEditDetail)
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
