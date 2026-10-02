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
	m.phase = phaseRunning
	m.liveGen = 2
	deliver := func(ev *mecatlv1.Event) {
		t.Helper()
		updated, _ := m.Update(liveMsg{gen: 2, msg: client.EventToMsg(ev)})
		m = updated.(Model)
	}
	deliver(&mecatlv1.Event{Type: "tool.call", ToolCall: &mecatlv1.ToolCall{Id: "safe", Name: "Read", Args: `{"path":"safe"}`}})
	s := inspectorOpenDetail(t, &m)
	deliver(&mecatlv1.Event{Type: "tool.result.available", ToolResult: &mecatlv1.ToolResult{CallId: "safe", Content: "provisional text", StructuredContent: `{"old":true}`}})
	if got := inspectorDetail(t, s, 80, 12); !strings.Contains(got, "provisional text") || !strings.Contains(got, `{"old":true}`) {
		t.Fatalf("provisional projection: %q", got)
	}
	// The server supplies the effective result. Received media bytes remain in
	// scrollback, but the inspector must describe rather than render them.
	const bytes = "binary-bytes-unique"
	deliver(&mecatlv1.Event{Type: "tool.result", ToolResult: &mecatlv1.ToolResult{CallId: "safe", Content: "effective text", StructuredContent: `{"stale":true}`, Blocks: []*mecatlv1.ContentBlock{
		{Kind: mecatlv1.ContentBlock_KIND_STRUCTURED_CONTENT, Text: `{"current":42}`},
		{Kind: mecatlv1.ContentBlock_KIND_IMAGE, MimeType: "image/png\x1b]8;;evil\a", Data: []byte(bytes)},
		{Kind: mecatlv1.ContentBlock_KIND_RESOURCE_LINK, Name: strings.Repeat("oversized", 100) + "\x1b[31m", Url: "https://example.invalid/safe\x1b]8;;evil\a"},
	}}})
	snap := m.conv.scrollback.SnapshotAt(0).Payload.(scrollback.ToolCardSnapshot)
	if snap.Result.Body != "effective text" || len(snap.Result.Artifacts) != 3 || string(snap.Result.Artifacts[1].Data) != bytes {
		t.Fatalf("received result not retained in scrollback: %#v", snap.Result)
	}
	var pages, raw string
	for _, width := range []int{70, 100} {
		s.Render(width, 9)
		s.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
		for i := 0; i < 12; i++ {
			body, _ := s.Render(width, 9)
			raw += body
			pages += stripANSIstr(body)
			s.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
		}
	}
	for _, want := range []string{"effective text", "Structured JSON", `{"current":42}`, "image/png", "https://example.invalid/safe"} {
		if !strings.Contains(pages, want) {
			t.Errorf("projection missing %q: %q", want, pages)
		}
	}
	for _, forbidden := range []string{"provisional text", `{"old":true}`, `{"stale":true}`, bytes, "YmluYXJ5LWJ5dGVzLXVuaXF1ZQ=="} {
		if strings.Contains(pages, forbidden) {
			t.Errorf("projection exposed %q", forbidden)
		}
	}
	for _, unsafe := range []string{"\x1b]8;;evil", "\x1b[31m"} {
		if strings.Contains(raw, unsafe) {
			t.Errorf("terminal control reached output: %q", unsafe)
		}
	}
}
