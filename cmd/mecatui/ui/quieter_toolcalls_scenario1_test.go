package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestMecatuiQuieterToolCalls_Scenario1_PendingAndSettledParity(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.ToolCallMsg{ID: "read", Name: "Read", Args: `{"path":"greeting.txt"}`})

	pending := m.rend.renderConversationFrame(&m.conv.scrollback, false)
	pendingID := toolBlockID(t, m.conv.scrollback, "read")
	if got := blockRows(pending, pendingID); len(got) < 2 || !strings.Contains(strings.Join(got, "\n"), "Read") || !strings.Contains(strings.Join(got, "\n"), "greeting.txt") {
		t.Fatalf("pending call must retain its bounded bordered card with action and target, got:\n%s", strings.Join(got, "\n"))
	}
	if !strings.Contains(strings.Join(blockRows(pending, pendingID), "\n"), "…") {
		t.Fatalf("pending card missing pending glyph:\n%s", strings.Join(blockRows(pending, pendingID), "\n"))
	}

	m = applyAll(m,
		client.ToolResultMsg{CallID: "read", Content: "greeting"},
		client.ToolCallMsg{ID: "failed", Name: "Read", Args: `{"path":"missing.txt"}`},
		client.ToolResultMsg{CallID: "failed", Content: "missing", IsError: true},
		client.ToolCallMsg{ID: "mcp", Name: "mcp__github__issue_write", Args: `{"path":"issue-42"}`},
		client.ToolResultMsg{CallID: "mcp", Content: "created"},
		client.ToolCallMsg{ID: "edit", Name: "Edit", Args: `{"path":"x.go","old_string":"old","new_string":"new"}`},
		client.ToolResultMsg{CallID: "edit", Content: "edited"},
		client.ToolCallMsg{ID: "write", Name: "Write", Args: `{"path":"new.go","content":"contents"}`},
		client.ToolResultMsg{CallID: "write", Content: "written"},
	)

	frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
	for _, want := range []struct {
		call, line string
	}{
		{"read", "✓ done · Read · Read greeting.txt"},
		{"failed", "✗ failed · Read · Read missing.txt"},
		{"edit", "✓ done · Edit · Edit x.go"},
		{"write", "✓ done · Write · Write new.go"},
	} {
		id := toolBlockID(t, m.conv.scrollback, want.call)
		rows := blockRows(frame, id)
		if len(rows) != 1 {
			t.Fatalf("%s settled rows = %d, want 1:\n%s", want.call, len(rows), strings.Join(rows, "\n"))
		}
		if got := strings.TrimSpace(stripANSIstr(rows[0])); got != want.line {
			t.Fatalf("%s settled line = %q, want %q", want.call, got, want.line)
		}
		if want.call == "read" && id != pendingID {
			t.Fatalf("settled block ID = %d, want pending block ID %d", id, pendingID)
		}
		if strings.ContainsAny(rows[0], "╭╮╰╯┌┐└┘─│") {
			t.Fatalf("%s settled line retained card border: %q", want.call, rows[0])
		}
	}

	entries := m.toolcallEntries()
	for _, entry := range entries {
		id := uint64(entry.blockID)
		rows := blockRows(frame, id)
		if !entry.settled() || len(rows) != 1 {
			continue
		}
		glyph, status, _ := entry.state.status()
		want := glyph + " " + status + " · " + entry.displayName + " · " + entry.intent
		if got := strings.TrimSpace(stripANSIstr(rows[0])); got != want {
			t.Fatalf("conversation %q differs from inspector semantic projection %q", got, want)
		}
	}

	mcpEntry := toolcallEntryByID(t, entries, "mcp", m.conv.scrollback)
	mcpRows := blockRows(frame, uint64(mcpEntry.blockID))
	if got, want := strings.TrimSpace(stripANSIstr(mcpRows[0])), "✓ done · GitHub · Issue write · mcp__github__issue_write issue-42"; got != want {
		t.Fatalf("MCP conversation line = %q, want %q", got, want)
	}
	m.phase = phaseIdle
	model, _ := m.runToolcalls()
	inspector := toolcallsForTest(t, model.(Model))
	// The rendered inspector list (cursor/gutter/styling aside) carries the same
	// semantic status, display name, and intent as each conversation line.
	list, _ := inspector.Render(160, 30)
	listText := stripANSIstr(list)
	for _, entry := range entries {
		_, status, _ := entry.state.status()
		semantic := status + " · " + entry.displayName + " · " + entry.intent
		if !strings.Contains(listText, semantic) {
			t.Fatalf("inspector list missing %q:\n%s", semantic, listText)
		}
		if rows := blockRows(frame, uint64(entry.blockID)); len(rows) != 1 || !strings.Contains(stripANSIstr(rows[0]), semantic) {
			t.Fatalf("conversation line for %q = %q", semantic, rows)
		}
	}
	inspector.selected = mcpEntry.index
	inspector.refreshDetail(&m.conv.scrollback)
	if inspector.detailEntry == nil || inspector.detailEntry.name != "mcp__github__issue_write" {
		t.Fatalf("inspector detail did not retain received MCP full name: %#v", inspector.detailEntry)
	}
}

func toolBlockID(t *testing.T, c scrollback.Conversation, callID string) uint64 {
	t.Helper()
	for i := 0; i < c.Len(); i++ {
		metadata, ok := c.ToolCallMetadataAt(i)
		if ok && metadata.CallID == callID {
			return uint64(metadata.ID)
		}
	}
	t.Fatalf("tool block %q not found", callID)
	return 0
}

func blockRows(frame renderedFrame, blockID uint64) []string {
	var rows []string
	for i, provenance := range frame.provenance {
		if provenance.blockID == blockID {
			rows = append(rows, frame.lines[i])
		}
	}
	return rows
}

func toolcallEntryByID(t *testing.T, entries []toolcallEntry, callID string, c scrollback.Conversation) toolcallEntry {
	t.Helper()
	blockID := scrollback.BlockID(toolBlockID(t, c, callID))
	for _, entry := range entries {
		if entry.blockID == blockID {
			return entry
		}
	}
	t.Fatalf("inspector entry for %q not found", callID)
	return toolcallEntry{}
}
