package ui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func toolcallDetailBytes(t *testing.T, depth int) uint64 {
	t.Helper()
	args := `{"root":` + strings.Repeat(`{"child":`, depth) + `{"last":"complete"}` + strings.Repeat(`}`, depth) + `}`
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	lines := toolcallArgumentLines("Subagent", args)
	runtime.ReadMemStats(&after)
	if !strings.Contains(strings.Join(lines, "\n"), "Last: complete") {
		t.Fatal("deepest value lost")
	}
	return after.TotalAlloc - before.TotalAlloc
}

func TestMecatuiToolcallsInspector_Scenario4_DeepArgumentsLinear(t *testing.T) {
	small := toolcallDetailBytes(t, 400)
	large := toolcallDetailBytes(t, 800)
	if large > small*3+250000 {
		t.Fatalf("doubling depth grows allocation superlinearly: %d -> %d", small, large)
	}
	fields := make([]string, 1200)
	for i := range fields {
		fields[i] = fmt.Sprintf(`"key%04d":{"child":"value%04d"}`, i, i)
	}
	got := strings.Join(toolcallArgumentLines("other", `{`+strings.Join(fields, ",")+`}`), "\n")
	if !strings.Contains(got, "Child: value1199") || !strings.Contains(got, "Child: value0000") {
		t.Fatal("fan-out lost values")
	}
	if got := strings.Join(toolcallArgumentLines("other", `{"path":"x"} trailing`), "\n"); !strings.Contains(got, `Original arguments: {"path":"x"} trailing`) {
		t.Fatalf("invalid trailing data not preserved: %q", got)
	}
	if got := strings.Join(toolcallArgumentLines("other", `{"precise":12345678901234567890123456789}`), "\n"); !strings.Contains(got, "Precise: 12345678901234567890123456789") {
		t.Fatalf("large numeric value changed: %q", got)
	}
}

func TestMecatuiToolcallsInspector_Scenario4_ListIntentCachedAcrossResults(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	content := strings.Repeat("write payload", 20000)
	m.conv.addTool("write", "Write", `{"path":"first","content":"`+content+`"}`)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if got := s.entries[0].intent; got != "Write first" {
		t.Fatalf("initial intent: %q", got)
	}
	m.conv.resolveTool("write", "first result", false)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 5; i++ {
		m.syncToolcalls()
	}
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 100000 {
		t.Fatalf("historical payload copied during result sync: %d bytes", used)
	}
	m.conv.addTool("pending", "Write", `{"path":"before"}`)
	m.syncToolcalls()
	selected := s.entries[s.selected].blockID
	if !m.conv.scrollback.Tools().ReconcileUnresolved(scrollback.ToolCall{ID: "pending", Name: "Write", Arguments: `{"path":"after"}`}) {
		t.Fatal("reconcile failed")
	}
	m.syncToolcalls()
	if s.entries[1].intent != "Write after" || s.entries[s.selected].blockID != selected {
		t.Fatalf("reconciled entry or selection stale: %+v", s.entries)
	}
	m.conv.resolveTool("pending", "done", false)
	m.syncToolcalls()
	if s.entries[1].intent != "Write after" {
		t.Fatalf("result changed cached intent: %+v", s.entries[1])
	}
}

func TestMecatuiToolcallsInspector_Scenario4_RenderedPaginationAndStyle(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	nested := `{"path":"file.go","meta":{"child":["first",{"last":"` + strings.Repeat("complete-value", 20) + `FINAL-NESTED-TAIL"}]}}`
	m.conv.addTool("read", "Read", nested)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.Render(70, 12)
	s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	s.refreshDetail(&m.conv.scrollback)
	m.conv.resolveTool("read", "     1\t"+strings.Repeat("wrapped", 20)+"\n     2\tend-gutter", true)
	m.syncToolcalls()
	raw := m.View().Content
	if !strings.Contains(raw, s.deps.theme.Style("errorText").Render("Error:")) || !strings.Contains(raw, s.deps.theme.Style("errorText").Render("Identity · Read · failed")) || !strings.Contains(raw, s.deps.theme.Style("toolName").Render("Arguments:")) {
		t.Fatalf("rendered sections lack distinct styles: %q", raw)
	}
	for _, width := range []int{70, 55} {
		m.width = width
		m.relayout()
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
		top := ansi.Strip(m.View().Content)
		if !strings.Contains(top, "Arguments:") || !strings.Contains(top, "Meta:") {
			t.Fatalf("width %d lost start of nested detail: %q", width, top)
		}
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
		end := ansi.Strip(m.View().Content)
		if !strings.Contains(end, "     2  end-gutter") {
			t.Fatalf("width %d lost Read gutter after wrapping: %q", width, end)
		}
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
		found := false
		for i := 0; i < 100; i++ {
			view := ansi.Strip(m.View().Content)
			if strings.Contains(view, "FINAL-NESTED-TAIL") {
				found = true
				break
			}
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		}
		if !found {
			t.Fatalf("width %d nested long value unreachable", width)
		}
	}
	m2 := newToolcallsInspectorModel(t)
	m2.conv.addTool("bad", "odd", "broken\x1b[31m-original")
	m2 = openToolcallsForTest(t, m2)
	s2 := toolcallsForTest(t, m2)
	s2.Render(70, 12)
	s2.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	s2.refreshDetail(&m2.conv.scrollback)
	if got := ansi.Strip(m2.View().Content); !strings.Contains(got, "broken") || strings.Contains(got, "\x1b[31m") {
		t.Fatalf("malformed rendered unsafely: %q", got)
	}
	m2.conv.resolveTool("bad", strings.Repeat("provisional-line\n", 20)+"provisional-tail", false)
	m2.syncToolcalls()
	s2.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if got := ansi.Strip(m2.View().Content); !strings.Contains(got, "provisional-tail") {
		t.Fatalf("provisional result tail not rendered: %q", got)
	}
	m2.width = 55
	m2.relayout()
	if got := ansi.Strip(m2.View().Content); !strings.Contains(got, "provisional-tail") {
		t.Fatalf("provisional result tail lost after reflow: %q", got)
	}
	m3 := newToolcallsInspectorModel(t)
	m3.conv.addTool("typed", "Read", `{"path":"x"}`)
	m3.conv.resolveTool("typed", "result", false,
		client.ContentBlock{Kind: client.ContentBlockStructuredContent, Text: `{"count":1}`},
		client.ContentBlock{Kind: client.ContentBlockResourceLink, Name: "link", URL: "mcp://link"})
	m3 = openToolcallsForTest(t, m3)
	m3.height = 45
	m3.relayout()
	s3 := toolcallsForTest(t, m3)
	s3.Render(70, 12)
	s3.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	s3.refreshDetail(&m3.conv.scrollback)
	styled := m3.View().Content
	for _, heading := range []string{"Arguments:", "Result:", "Structured content · Structured JSON:", "Resources"} {
		if !strings.Contains(styled, s3.deps.theme.Style("toolName").Render(heading)) {
			t.Fatalf("section %q not distinguished in rendered detail: %q", heading, styled)
		}
	}
}

func TestMecatuiToolcallsInspector_Scenario5_NormalResizeStaleHitAndNoMouse(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 5)
	m = openToolcallsForTest(t, m)
	_ = m.View()
	stale := m.hits.frame[0].id
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 86, Height: 25})
	m = updated.(Model)
	_ = m.View()
	s := toolcallsForTest(t, m)
	if s.compact || len(m.hits.frame) == 0 {
		t.Fatal("normal resize lost live row hits")
	}
	s.selected = len(s.entries) - 1
	updated, _ = m.Update(surfaceHitMsg{ID: stale})
	m = updated.(Model)
	if s.selected != len(s.entries)-1 {
		t.Fatal("stale normal-size hit selected a row")
	}
	fresh := m.hits.frame[0]
	updated, _ = m.Update(surfaceHitMsg{ID: fresh.id})
	m = updated.(Model)
	if s.selected == len(s.entries)-1 {
		t.Fatal("fresh resized row hit did not select")
	}
	m.deps.NoMouse = true
	if view := m.View(); view.MouseMode != 0 {
		t.Fatalf("NoMouse alt-screen captured pointer: %v", view.MouseMode)
	}
	before := s.selected
	region := m.hits.frame[len(m.hits.frame)-1]
	x, y := m.metrics.localToGlobal(region.rect.x0, region.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if s.selected != before {
		t.Fatal("NoMouse click selected row")
	}
}
