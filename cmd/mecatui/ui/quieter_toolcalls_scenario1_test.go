package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func TestSkillSummaryIncludesNameOnConversationAndInspector(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 24},
		client.ToolCallMsg{ID: "skill", Name: "Skill", Args: `{"name":"test-writer","asset":"references/style.md"}`},
		client.ToolResultMsg{CallID: "skill", Content: "loaded"},
	)
	for _, width := range []int{28, 100} {
		m.rend.setWidth(width)
		frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
		rows := blockRows(frame, toolBlockID(t, m.conv.scrollback, "skill"))
		if len(rows) != 1 || strings.TrimSpace(stripANSIstr(rows[0])) != "✓ Skill · test-writer" || ansi.StringWidth(rows[0]) > width {
			t.Fatalf("width %d Skill conversation line = %q, want one bounded named row", width, rows)
		}
	}
	m.phase = phaseIdle
	model, _ := m.runToolcalls()
	for _, width := range []int{52, 100} {
		list, _ := toolcallsForTest(t, model.(Model)).Render(width, 24)
		var skillRow string
		for _, row := range strings.Split(stripANSIstr(list), "\n") {
			if strings.Contains(row, "✓ Skill") {
				skillRow = row
				break
			}
		}
		if !strings.Contains(skillRow, "✓ Skill · test-writer") || ansi.StringWidth(skillRow) > width {
			t.Fatalf("width %d Skill inspector row lost its bounded name: %q", width, skillRow)
		}
	}
}

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
		{"read", "✓ Read · greeting.txt"},
		{"failed", "✗ Read · missing.txt"},
		{"edit", "✓ Edit · x.go"},
		{"write", "✓ Write · new.go"},
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
		glyph, _, _ := entry.state.status()
		want := glyph + " " + entry.summary()
		if got := strings.TrimSpace(stripANSIstr(rows[0])); got != want {
			t.Fatalf("conversation %q differs from inspector semantic projection %q", got, want)
		}
	}

	mcpEntry := toolcallEntryByID(t, entries, "mcp", m.conv.scrollback)
	mcpRows := blockRows(frame, uint64(mcpEntry.blockID))
	if got, want := strings.TrimSpace(stripANSIstr(mcpRows[0])), "✓ GitHub · Issue write · issue-42"; got != want {
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
		semantic := entry.summary()
		if strings.Contains(semantic, "done ·") || strings.Contains(semantic, "failed ·") {
			t.Fatalf("settled inspector row still advertises redundant status: %q", semantic)
		}
		if !strings.Contains(listText, semantic) {
			t.Fatalf("inspector list missing %q:\n%s", semantic, listText)
		}
		if rows := blockRows(frame, uint64(entry.blockID)); len(rows) != 1 || !strings.Contains(stripANSIstr(rows[0]), semantic) {
			t.Fatalf("conversation line for %q = %q", semantic, rows)
		}
	}
	failedEntry := toolcallEntryByID(t, entries, "failed", m.conv.scrollback)
	var failedRow string
	for _, line := range strings.Split(listText, "\n") {
		if strings.Contains(line, "Read · missing.txt") {
			failedRow = line
			break
		}
	}
	if !strings.Contains(failedRow, "✗ Read · missing.txt") || strings.Contains(failedRow, "failed") {
		t.Fatalf("failed inspector list row must show only its status icon: %q", failedRow)
	}
	inspector.selected = failedEntry.index
	inspector.refreshDetail(&m.conv.scrollback)
	if inspector.detailEntry == nil || !strings.Contains(strings.Join(toolcallDetailLines(*inspector.detailEntry), "\n"), "✗ Read · failed") {
		t.Fatalf("failed inspector detail lost its status word: %#v", inspector.detailEntry)
	}
	inspector.selected = mcpEntry.index
	inspector.refreshDetail(&m.conv.scrollback)
	if inspector.detailEntry == nil || inspector.detailEntry.name != "mcp__github__issue_write" {
		t.Fatalf("inspector detail did not retain received MCP full name: %#v", inspector.detailEntry)
	}
}

func TestMecatuiQuieterToolCalls_Scenario1_SharedIntentAndSafety(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		client.ToolCallMsg{ID: "read", Name: "Read", Args: `{}`}, client.ToolResultMsg{CallID: "read", Content: "read result"},
		client.ToolCallMsg{ID: "grep", Name: "Grep", Args: `{"pattern":"needle","path":"cmd/**/*.go"}`}, client.ToolResultMsg{CallID: "grep", Content: "grep result"},
		client.ToolCallMsg{ID: "shell", Name: "Shell", Args: `{"command":"echo hi"}`}, client.ToolResultMsg{CallID: "shell", Content: "shell result"},
		client.ToolCallMsg{ID: "edit", Name: "Edit", Args: `{"path":"x.go"}`}, client.ToolResultMsg{CallID: "edit", Content: "edit result"},
		client.ToolCallMsg{ID: "write", Name: "Write", Args: `{"path":"x.go"}`}, client.ToolResultMsg{CallID: "write", Content: "write result"},
		client.ToolCallMsg{ID: "mcp", Name: "mcp__github__issue_write", Args: `{"target":"issue-1"}`}, client.ToolResultMsg{CallID: "mcp", Content: "mcp result"},
		client.ToolCallMsg{ID: "unknown", Name: "Mystery", Args: `{"task":"investigate"}`}, client.ToolResultMsg{CallID: "unknown", Content: "unknown result"},
		client.ToolCallMsg{ID: "malformed", Name: "Broken", Args: `{"path":`}, client.ToolResultMsg{CallID: "malformed", Content: "malformed result"},
		client.ToolCallMsg{ID: "hostile", Name: "Bad\x1b]8;;https://bad\a\n\t\u202e界😀", Args: `{"prompt":"\u001b[31mwide 界😀\n\t\u202e"}`}, client.ToolResultMsg{CallID: "hostile", Content: "\x1b]8;;https://bad\aresult wide 界😀\n\t\u202e"},
	)
	m.conv.addTool("subagent", "Subagent", `{"prompt":"investigate the loop"}`)
	applySubagentTo(&m.conv, client.SubagentMsg{Kind: client.SubagentStart, ParentCallID: "subagent", ChildID: "child", Goal: "investigate the loop"})
	applySubagentTo(&m.conv, client.SubagentMsg{Kind: client.SubagentTool, ParentCallID: "subagent", ChildID: "child", InnerKind: "tool.call", ToolName: "Read", ToolCount: 1})
	applySubagentTo(&m.conv, client.SubagentMsg{Kind: client.SubagentEnd, ParentCallID: "subagent", ChildID: "child", ToolCount: 1, Stop: "end_turn"})
	if !m.conv.resolveTool("subagent", "subagent result", false) {
		t.Fatal("subagent lifecycle did not apply")
	}
	m.conv.addTool("team", "Team", `{"goal":"ship the feature"}`)
	applyTeamTo(&m.conv, client.TeamMsg{Kind: client.TeamStart, ParentCallID: "team", TeamID: "team", Roster: roster()})
	applyTeamTo(&m.conv, client.TeamMsg{Kind: client.TeamMember, ParentCallID: "team", TeamID: "team", Member: "lead", InnerKind: "tool.call", ToolName: "Read"})
	applyTeamTo(&m.conv, client.TeamMsg{Kind: client.TeamEnd, ParentCallID: "team", TeamID: "team", Rounds: 1, Stop: "end_turn"})
	if !m.conv.resolveTool("team", "team result", false) {
		t.Fatal("team lifecycle did not apply")
	}

	for _, width := range []int{20, 12, 1} {
		m.rend.setWidth(width)
		frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
		for _, entry := range m.toolcallEntries() {
			rows := blockRows(frame, uint64(entry.blockID))
			if len(rows) != 1 {
				t.Fatalf("width %d %q rows = %d, want one: %q", width, entry.fullName, len(rows), rows)
			}
			plain := strings.TrimSpace(stripANSIstr(rows[0]))
			if hasUnsafeTerminalBytes(rows[0]) || strings.ContainsAny(plain, "\x1b\n\r\t") || ansi.StringWidth(plain) > width {
				t.Fatalf("width %d unsafe or too wide %q", width, plain)
			}
			if strings.ContainsAny(rows[0], "╭╮╰╯┌┐└┘─│") {
				t.Fatalf("settled %q retained card border: %q", entry.fullName, rows[0])
			}
		}
	}

	m.rend.setWidth(160)
	frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
	entries := m.toolcallEntries()
	m.phase = phaseIdle
	modal, _ := m.runToolcalls()
	list, _ := toolcallsForTest(t, modal.(Model)).Render(160, 30)
	listText := stripANSIstr(list)
	for _, entry := range entries {
		if entry.fullName == "Grep" && entry.intent != `"needle" in cmd/**/*.go` {
			t.Fatalf("Grep intent lost pattern or search scope: %q", entry.intent)
		}
		glyph, _, _ := entry.state.status()
		want := glyph + " " + entry.summary()
		if !strings.Contains(listText, want) {
			t.Fatalf("inspector list missing shared semantic text %q:\n%s", want, listText)
		}
		rows := blockRows(frame, uint64(entry.blockID))
		if got := strings.TrimSpace(stripANSIstr(rows[0])); got != want {
			t.Fatalf("conversation %q = %q, want shared semantic text %q", entry.fullName, got, want)
		}
		inspector := &toolcallsState{entries: entries, selected: entry.index}
		for i := range entries {
			if entries[i].blockID == entry.blockID {
				inspector.selected = i
				break
			}
		}
		inspector.refreshDetail(&m.conv.scrollback)
		metadata, ok := m.conv.scrollback.ToolCallMetadataAt(entry.index)
		if !ok || inspector.detailEntry == nil || inspector.detailEntry.name != entry.fullName || inspector.detailEntry.intent != metadata.Arguments || inspector.detailEntry.result.Body == "" {
			t.Fatalf("detail for %q lost received name, arguments, or result: %#v", entry.fullName, inspector.detailEntry)
		}
	}

	// The inspector must sanitize and bound the same hostile data at its own
	// narrow list/detail seam while retaining the complete received detail.
	inspector := toolcallsForTest(t, modal.(Model))
	hostile := toolcallEntryByID(t, inspector.entries, "hostile", m.conv.scrollback)
	inspector.selected = hostile.index
	list, _ = inspector.Render(52, 20)
	inspector.detail = true
	inspector.refreshDetail(&m.conv.scrollback)
	detail, _ := inspector.Render(52, 20)
	for surface, body := range map[string]string{"list": list, "detail": detail} {
		plain := stripANSIstr(body)
		if strings.Contains(body, "\x1b]") || strings.Contains(body, "\x1b[31m") {
			t.Fatalf("hostile %s emitted untrusted terminal controls: %q", surface, body)
		}
		for _, row := range strings.Split(plain, "\n") {
			if strings.ContainsAny(row, "\x1b\n\r\t") || ansi.StringWidth(row) > 52 {
				t.Fatalf("hostile %s row overflows or retains controls: %q", surface, row)
			}
		}
	}
	plainDetail := stripANSIstr(detail)
	if !strings.Contains(plainDetail, "result wide 界😀") || !strings.Contains(plainDetail, "wide 界😀") {
		t.Fatalf("hostile full received arguments/result are not reachable in detail: %q", plainDetail)
	}
}

func hasUnsafeTerminalBytes(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			if i+2 >= len(s) || s[i+1] != '[' {
				return true
			}
			i += 2
			for i < len(s) && s[i] != 'm' {
				if s[i] < '0' || s[i] > '?' {
					return true
				}
				i++
			}
			if i == len(s) {
				return true
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
		i += size - 1
	}
	return false
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
