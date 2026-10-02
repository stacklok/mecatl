package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func TestMecatuiToolcallsInspector_Scenario1_StreamingThroughOpenModel(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	m.phase = phaseRunning
	m.liveGen = 4
	deliver := func(ev *mecatlv1.Event) {
		t.Helper()
		updated, _ := m.Update(liveMsg{gen: 4, msg: client.EventToMsg(ev)})
		m = updated.(Model)
	}
	call := func(id, name string) *mecatlv1.Event {
		return &mecatlv1.Event{Type: "tool.call", ToolCall: &mecatlv1.ToolCall{Id: id, Name: name, Args: `{"path":"file"}`}}
	}
	result := func(id, body, kind string) *mecatlv1.Event {
		return &mecatlv1.Event{Type: kind, ToolResult: &mecatlv1.ToolResult{CallId: id, Content: body}}
	}
	deliver(call("older", "Read"))
	deliver(call("reused", "Edit"))
	m = openToolcallsForTest(t, m)
	s := toolcallsForTest(t, m)
	s.Render(80, 12)
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	if s.selected != 0 {
		t.Fatalf("older selection = %d", s.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(Model)
	s = toolcallsForTest(t, m)
	deliver(result("older", "temporary", "tool.result.available"))
	if got := inspectorDetail(t, s, 80, 12); !strings.Contains(got, "temporary") {
		t.Fatalf("provisional detail %q", got)
	}
	deliver(result("older", "canonical", "tool.result"))
	deliver(result("reused", "old result", "tool.result"))
	deliver(call("reused", "Write"))
	deliver(result("reused", "new result", "tool.result"))
	if s.selected != 0 || len(s.entries) != 3 || s.detailEntry == nil || s.detailEntry.callID != "older" {
		t.Fatalf("live selection changed: %#v", s)
	}
	if got := inspectorDetail(t, s, 80, 12); !strings.Contains(got, "canonical") || strings.Contains(got, "temporary") || strings.Contains(got, "new result") {
		t.Fatalf("canonical detail %q", got)
	}
	for i, want := range []string{"canonical", "old result", "new result"} {
		snap := m.conv.scrollback.SnapshotAt(s.entries[i].index)
		if body := snap.Payload.(scrollback.ToolCardSnapshot).Result.Body; body != want {
			t.Fatalf("scrollback %d = %q, want %q", i, body, want)
		}
	}
	m.liveGen++
	stale, _ := m.Update(liveMsg{gen: 4, msg: client.EventToMsg(call("late", "Write"))})
	m = stale.(Model)
	if m.conv.scrollback.Len() != 3 || len(toolcallsForTest(t, m).entries) != 3 {
		t.Fatal("stale live callback reached replaced feed or inspector")
	}
}

func TestMecatuiToolcallsInspector_Scenario2_ReceivedProjectionBoundary(t *testing.T) {
	m := newToolcallsInspectorModel(t)
	call := &mecatlv1.Event{Type: "tool.call", ToolCall: &mecatlv1.ToolCall{Id: "safe", Name: "Read", Args: `{"path":"safe"}`}}
	m = applyAll(m, client.EventToMsg(call))
	_ = inspectorOpenDetail(t, &m)
	// Only the effective, display-safe result arrives on this client boundary.
	// The original withheld body and decoded media bytes are not reconstructible.
	result := &mecatlv1.Event{Type: "tool.result", ToolResult: &mecatlv1.ToolResult{CallId: "safe", Content: "effective text", Blocks: []*mecatlv1.ContentBlock{
		{Kind: mecatlv1.ContentBlock_KIND_IMAGE, MimeType: "image/png\x1b]8;;evil\a", Data: []byte("binary-bytes")},
		{Kind: mecatlv1.ContentBlock_KIND_RESOURCE_LINK, Name: strings.Repeat("oversized", 100) + "\x1b[31m", Url: "https://example.invalid/safe\x1b]8;;evil\a"},
	}}}
	m = applyAll(m, client.EventToMsg(result))
	if snap := m.conv.scrollback.SnapshotAt(0).Payload.(scrollback.ToolCardSnapshot); snap.Result.Body != "effective text" {
		t.Fatalf("received body: %q", snap.Result.Body)
	}
	s := toolcallsForTest(t, m)
	var pages, raw string
	for _, width := range []int{70, 100} {
		s.Render(width, 9)
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
		for i := 0; i < 80; i++ {
			body, _ := s.Render(width, 9)
			raw += body
			pages += stripANSIstr(body)
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		}
	}
	if !strings.Contains(pages, "effective text") || !strings.Contains(pages, "image/png") || !strings.Contains(pages, "https://example.invalid/safe") {
		t.Fatalf("projection missing: %q", pages)
	}
	for _, forbidden := range []string{"withheld-original", "binary-bytes", "YmluYXJ5LWJ5dGVz"} {
		if strings.Contains(pages, forbidden) {
			t.Fatalf("projection exposed %q", forbidden)
		}
	}
	for _, unsafe := range []string{"\x1b]8;;evil", "\x1b[31m"} {
		if strings.Contains(raw, unsafe) {
			t.Fatalf("terminal control reached output: %q", unsafe)
		}
	}
}
