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

func TestMecatuiToolcallsInspector_Scenario4_ListIntentRefreshesWithCardRevision(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	content := strings.Repeat("write payload", 20000)
	m.conv.addTool("write", "Write", `{"path":"first","content":"`+content+`"}`)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	if got := s.entries[0].intent; got != "Write first" {
		t.Fatalf("initial intent: %q", got)
	}
	initialRevision := s.entries[0].revision
	m.conv.resolveTool("write", "first result", false)
	m.syncToolcalls()
	if got := s.entries[0]; got.revision != initialRevision+1 || got.intent != "Write first" || !got.resolved {
		t.Fatalf("result lifecycle entry = %+v", got)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 5; i++ {
		m.syncToolcalls()
	}
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > 100000 {
		t.Fatalf("historical payload copied during stable result sync: %d bytes", used)
	}
	m.conv.addTool("pending", "Write", `{"path":"before"}`)
	m.syncToolcalls()
	selected := s.entries[s.selected].blockID
	pendingRevision := s.entries[1].revision
	if !m.conv.scrollback.Tools().ReconcileUnresolved(scrollback.ToolCall{ID: "pending", Name: "Write", Arguments: `{"path":"after"}`}) {
		t.Fatal("reconcile failed")
	}
	m.syncToolcalls()
	if got := s.entries[1]; got.revision != pendingRevision+1 || got.intent != "Write after" || s.entries[s.selected].blockID != selected {
		t.Fatalf("reconciled entry or selection stale: %+v", s.entries)
	}
	reconciledRevision := s.entries[1].revision
	m.conv.resolveTool("pending", "done", false)
	m.syncToolcalls()
	if got := s.entries[1]; got.revision != reconciledRevision+1 || got.intent != "Write after" || !got.resolved {
		t.Fatalf("result lifecycle changed reconciled entry: %+v", got)
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
	if !strings.Contains(raw, s.deps.theme.Style("errorText").Render("Error:")) || !strings.Contains(raw, s.deps.theme.Style("toolErr").Render("✗")) || !strings.Contains(raw, s.deps.theme.Style("toolErr").Render("failed")) || !strings.Contains(raw, s.deps.theme.Style("toolName").Render("Arguments:")) {
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

func TestMecatuiToolcallsInspector_DetailActivationIntentRefreshesSelectedCall(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m = addToolcallsForTest(t, m, 2)
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)

	if _, handled, _ := s.HandleMsg(surfaceHitMsg{}); !handled || s.takeSurfaceIntent() != nil {
		t.Fatal("invalid hit emitted a detail intent")
	}
	if _, handled, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown}); !handled || s.takeSurfaceIntent() != nil {
		t.Fatal("navigation emitted a detail intent")
	}
	if _, handled, _ := s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); !handled {
		t.Fatal("Enter was not handled")
	}
	intent, ok := s.takeSurfaceIntent().(toolcallsDetailIntent)
	if !ok || intent.blockID != s.entries[s.selected].blockID || s.takeSurfaceIntent() != nil {
		t.Fatalf("Enter detail intent = %#v", intent)
	}
	if _, _, _ = s.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}); s.takeSurfaceIntent() != nil {
		t.Fatal("Enter in detail emitted a second intent")
	}

	keyboard := newToolcallsInspectorModel(t)
	keyboard.conv.addTool("keyboard", "Read", `{"path":"keyboard.go"}`)
	keyboard = openToolcallsForTest(t, keyboard)
	updated, _ := keyboard.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	keyboard = updated.(Model)
	if detail := toolcallsForTest(t, keyboard).detailEntry; detail == nil || detail.callID != "keyboard" || !strings.Contains(ansi.Strip(keyboard.View().Content), "Path: keyboard.go") {
		t.Fatalf("keyboard detail did not refresh the selected call: %#v", detail)
	}

	clicked := newToolcallsInspectorModel(t)
	clicked.conv.addTool("first", "Read", `{"path":"first.go"}`)
	clicked.conv.addTool("second", "Read", `{"path":"clicked.go"}`)
	clicked = openToolcallsForTest(t, clicked)
	_ = clicked.View()
	s = toolcallsForTest(t, clicked)
	target := s.entries[1].blockID
	regionIndex := -1
	for i, candidate := range clicked.hits.frame {
		if s.hitItems[candidate.id] == target {
			regionIndex = i
			break
		}
	}
	if regionIndex < 0 {
		t.Fatal("clicked call has no hit region")
	}
	region := clicked.hits.frame[regionIndex]
	x, y := clicked.metrics.localToGlobal(region.rect.x0, region.rect.y0)
	updated, _ = clicked.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	clicked = updated.(Model)
	if detail := toolcallsForTest(t, clicked).detailEntry; detail == nil || detail.callID != "second" || !strings.Contains(ansi.Strip(clicked.View().Content), "Path: clicked.go") {
		t.Fatalf("clicked detail did not refresh the selected call: %#v", detail)
	}
}

func TestMecatuiToolcallsInspector_Scenario5_NormalResizeStaleHitAndNoMouse(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.deps.NoAltScreen = false
	m = addToolcallsForTest(t, m, 5)
	m.conv.addTool("reused", "Read", `{"path":"before-reuse.go"}`)
	m.conv.resolveTool("reused", "before reuse", false)
	m.conv.addTool("reused", "Read", `{"path":"after-reuse.go"}`)
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
	target := s.entries[len(s.entries)-1].blockID
	s.selected = 0
	updated, _ = m.Update(surfaceHitMsg{ID: stale})
	m = updated.(Model)
	if s.selected != 0 {
		t.Fatal("stale normal-size hit selected a row")
	}
	fresh := -1
	for i, region := range m.hits.frame {
		if s.hitItems[region.id] == target {
			fresh = i
			break
		}
	}
	if fresh < 0 {
		t.Fatal("reused-ID call has no fresh click target")
	}
	x, y := m.metrics.localToGlobal(m.hits.frame[fresh].rect.x0, m.hits.frame[fresh].rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if s.selected != len(s.entries)-1 || s.entries[s.selected].blockID != target {
		t.Fatalf("click selected unstable block: selected=%d entries=%#v", s.selected, s.entries)
	}
	if !s.detail {
		t.Fatal("fresh resized row click did not open detail")
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "Arguments:") || !strings.Contains(plain, "Path: after-reuse.go") || strings.Contains(plain, "call unavailable") {
		t.Fatalf("clicked call detail = %q", plain)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = updated.(Model)
	if s.detail {
		t.Fatal("Escape did not return fresh resized row hit to the list")
	}
	m.deps.NoMouse = true
	if view := m.View(); view.MouseMode != 0 {
		t.Fatalf("NoMouse alt-screen captured pointer: %v", view.MouseMode)
	}
	before := s.selected
	region := m.hits.frame[len(m.hits.frame)-1]
	x, y = m.metrics.localToGlobal(region.rect.x0, region.rect.y0)
	updated, _ = m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: x, Y: y})
	m = updated.(Model)
	if s.selected != before {
		t.Fatal("NoMouse click selected row")
	}
}
