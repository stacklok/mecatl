package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// AC1.3 pins lifecycle parity at the real UI reducer, renderer, and inspector seams.
func TestMecatuiQuieterToolCalls_Scenario1_OutOfOrderAndResume(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 35})
	check := func(m Model, callID, wantStatus string, settled bool, result string) {
		t.Helper()
		id := toolBlockID(t, m.conv.scrollback, callID)
		frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
		rows := blockRows(frame, id)
		m.phase = phaseIdle
		inspected := openToolcallsForTest(t, m)
		s := toolcallsForTest(t, inspected)
		entry := toolcallEntryByID(t, s.entries, callID, m.conv.scrollback)
		glyph, status, _ := entry.state.status()
		if status != wantStatus || uint64(entry.blockID) != id || len(rows) == 0 {
			t.Fatalf("%s: entry=%#v rows=%q, want %s", callID, entry, rows, wantStatus)
		}
		text := strings.Join(rows, "\n")
		plain := stripANSIstr(text)
		if settled {
			if strings.Contains(plain, " "+status+" ·") || !strings.Contains(plain, glyph+" "+entry.summary()) {
				t.Fatalf("%s: settled conversation line must retain glyph and intent without status word: %q", callID, plain)
			}
		} else if !strings.Contains(plain, glyph+" "+status+" · ") {
			t.Fatalf("%s: conversation header differs from inspector status %q: %q", callID, status, rows)
		}
		list, _ := s.Render(160, 35)
		if !strings.Contains(stripANSIstr(list), glyph+" "+entry.summary()) {
			t.Fatalf("%s: inspector row missing %q: %q", callID, glyph+" "+entry.summary(), stripANSIstr(list))
		}
		if settled {
			if len(rows) != 1 || strings.ContainsAny(text, "╭╮╰╯┌┐└┘─│") || !strings.Contains(plain, glyph+" "+entry.summary()) {
				t.Fatalf("%s: settled call must use exactly one summary line: %q", callID, rows)
			}
		} else if !strings.ContainsAny(text, "╭╮╰╯┌┐└┘─│") || strings.Contains(plain, "✓") || strings.Contains(plain, " done ") {
			t.Fatalf("%s: unresolved call must stay bordered without success: %q", callID, rows)
		}
		if result != "" && strings.Contains(plain, result) {
			t.Fatalf("%s: conversation fabricated or leaked result %q: %q", callID, result, plain)
		}
		for i := range s.entries {
			if s.entries[i].blockID == entry.blockID {
				s.selected = i
				break
			}
		}
		s.refreshDetail(&m.conv.scrollback)
		s.detail = true
		if s.detailEntry == nil {
			t.Fatalf("%s: missing inspector detail", callID)
		}
		detail := inspectorDetail(t, s, 100, 20)
		if !strings.Contains(detail, glyph+" "+entry.fullName+" · "+status) {
			t.Fatalf("%s: inspector detail status differs from conversation: %q", callID, detail)
		}
		if result != "" && (s.detailEntry.result.Body != "" || strings.Contains(detail, result)) {
			t.Fatalf("%s: invented result in detail: %#v", callID, s.detailEntry)
		}
	}

	m = applyAll(m, client.ToolCallMsg{ID: "first", Name: "Read", Args: `{"path":"first.txt"}`},
		client.ToolCallMsg{ID: "second", Name: "Read", Args: `{"path":"second.txt"}`})
	firstID, secondID, length := toolBlockID(t, m.conv.scrollback, "first"), toolBlockID(t, m.conv.scrollback, "second"), m.conv.scrollback.Len()
	check(m, "first", "running", false, "lost-first-result")
	check(m, "second", "running", false, "lost-second-result")
	m = applyAll(m, client.ParallelMsg{Kind: client.ParallelEnd, ParentCallID: "first", Stop: "end_turn"})
	check(m, "first", "awaiting result", false, "lost-first-result")
	check(m, "second", "running", false, "lost-second-result")
	m = applyAll(m, client.ToolResultMsg{CallID: "second", Content: "temporary", Available: true})
	check(m, "second", "result received · finalizing", false, "")
	check(m, "first", "awaiting result", false, "lost-first-result")
	m = applyAll(m, client.ToolResultMsg{CallID: "second", Content: "canonical", IsError: true})
	check(m, "second", "failed", true, "")
	check(m, "first", "awaiting result", false, "lost-first-result")
	m = applyAll(m, client.ToolResultMsg{CallID: "first", Content: "temporary", Available: true})
	check(m, "first", "result received · finalizing", false, "")
	m = applyAll(m, client.ToolResultMsg{CallID: "first", Content: "temporary"}) // identical confirmation
	check(m, "first", "done", true, "")
	if m.conv.scrollback.Len() != length || len(m.toolcallEntries()) != 2 || toolBlockID(t, m.conv.scrollback, "first") != firstID || toolBlockID(t, m.conv.scrollback, "second") != secondID {
		t.Fatal("out-of-order and provisional confirmations duplicated or replaced a block")
	}

	m = applyAll(m, client.ToolCallMsg{ID: "failed-no-result", Name: "Read", Args: `{"path":"failed.txt"}`},
		client.ParallelMsg{Kind: client.ParallelEnd, ParentCallID: "failed-no-result", Stop: "error"})
	check(m, "failed-no-result", "failed", true, "lost-failure-result")
	for _, kind := range []string{"Subagent", "Team"} {
		id := "failed-" + kind
		m.conv.addTool(id, kind, `{}`)
		if kind == "Subagent" {
			m = applyAll(m, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: id, ChildID: id}, client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: id, ChildID: id, Stop: "error"})
		} else {
			m = applyAll(m, client.TeamMsg{Kind: client.TeamStart, ParentCallID: id, TeamID: id}, client.TeamMsg{Kind: client.TeamEnd, ParentCallID: id, TeamID: id, Stop: "error"})
		}
		m.syncToolcalls()
		check(m, id, "failed", true, "lost-delegation-result")
	}

	resume := &client.ResumeSelection{Row: client.SessionListItem{ID: "resumed"}, Transcript: client.SessionTranscript{Messages: []client.ConversationMessage{
		{Role: "assistant", ToolCalls: []client.ConvToolCall{{ID: "completed", Name: "Read", Args: `{"path":"complete.txt"}`}, {ID: "unresolved", Name: "Read", Args: `{"path":"unresolved.txt"}`}}},
		{Role: "tool", ToolResult: &client.ConvToolResult{CallID: "completed", Content: "persisted result"}},
	}}}
	r := newTestModelFromDeps(Deps{Theme: testTheme(), Ctx: t.Context(), Resume: resume})
	r = applyAll(r, tea.WindowSizeMsg{Width: 120, Height: 35})
	r = openToolcallsForTest(t, r)
	check(r, "completed", "done", true, "")
	check(r, "unresolved", "running", false, "lost-unresolved-result")
	if len(r.toolcallEntries()) != 2 {
		t.Fatalf("rehydrated inventory duplicated a call: %#v", r.toolcallEntries())
	}
	unresolved := toolcallEntryByID(t, r.toolcallEntries(), "unresolved", r.conv.scrollback)
	metadata, ok := r.conv.scrollback.ToolCallMetadataAt(unresolved.index)
	if !ok || metadata.ResultReceived || metadata.Terminal {
		t.Fatalf("unresolved resume invented a result/lifecycle: %#v", metadata)
	}
}
