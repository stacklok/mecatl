package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiQuieterToolCalls_Scenario4_LongRunAnchorsAndSelection proves that
// a long settled-call run stays compact without turning its logical blocks into
// physical-row identities.
func TestMecatuiQuieterToolCalls_Scenario4_LongRunAnchorsAndSelection(t *testing.T) {
	const settled = 105
	const anchorCall = 52
	const anchorMarker = "ANCHOR052"
	const selectedWord = "✓"

	m, _ := selModel(t)
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.conv.addUser("long run request")
	userID := uint64(m.conv.testBlocks()[len(m.conv.testBlocks())-1].ID)
	m.conv.appendAssistant("starting the long run")
	assistantID := uint64(m.conv.testBlocks()[len(m.conv.testBlocks())-1].ID)
	m = applyAll(m, client.CompactionMsg{Text: "readable long-run notice"})
	noticeID := uint64(m.conv.testBlocks()[len(m.conv.testBlocks())-1].ID)
	// Keep this bordered card before the selected settled call. Its collapse must
	// restore the selected block, selection, and later tail-follow semantically.
	m.conv.addTool("live", "Read", `{"path":"live-call-with-a-readable-target.txt"}`)
	liveID := toolBlockID(t, m.conv.scrollback, "live")
	for i := 0; i < settled; i++ {
		id := fmt.Sprintf("settled-%03d", i)
		path := fmt.Sprintf("settled-%03d.txt", i)
		name := "Read"
		if i == anchorCall {
			path = "anchor.txt"
			name = anchorMarker
		}
		m.conv.addTool(id, name, `{"path":"`+path+`"}`)
		if !m.conv.resolveTool(id, "received", false) {
			t.Fatalf("resolve %q", id)
		}
	}
	m.conv.appendAssistant("ordinary assistant block after the run")
	m.phase = phaseIdle
	m.refreshView()

	for _, width := range []int{28, 100} {
		m.rend.setWidth(width)
		frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
		firstID := toolBlockID(t, m.conv.scrollback, "settled-000")
		assertLongRunGap(t, frame, userID, assistantID, 1)
		assertLongRunGap(t, frame, assistantID, noticeID, 1)
		assertLongRunGap(t, frame, noticeID, liveID, 1)
		assertLongRunGap(t, frame, liveID, firstID, 0)
		for i := 0; i < settled; i++ {
			id := toolBlockID(t, m.conv.scrollback, fmt.Sprintf("settled-%03d", i))
			rows := blockRows(frame, id)
			if len(rows) != 1 || strings.ContainsAny(rows[0], "╭╮╰╯┌┐└┘─│") {
				t.Fatalf("width %d settled call %d = %q, want one borderless line", width, i, rows)
			}
			if i > 0 {
				assertLongRunGap(t, frame, toolBlockID(t, m.conv.scrollback, fmt.Sprintf("settled-%03d", i-1)), id, 0)
			}
		}
		liveRows := blockRows(frame, toolBlockID(t, m.conv.scrollback, "live"))
		if len(liveRows) < 2 || !strings.Contains(strings.Join(liveRows, "\n"), "Read") {
			t.Fatalf("width %d live call lost its bounded card: %q", width, liveRows)
		}
		plain := ansi.Strip(strings.Join(frame.lines, "\n"))
		if !strings.Contains(plain, "long run request") || !strings.Contains(plain, "starting the long run") || !strings.Contains(plain, "readable long-run notice") || !strings.Contains(plain, "ordinary assistant block") {
			t.Fatalf("width %d lost ordinary readable conversation blocks:\n%s", width, plain)
		}
	}

	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.refreshView()
	line := lineIndexContaining(m.vp.GetContent(), anchorMarker)
	if line < 0 {
		t.Fatal("precondition: anchored settled line is not rendered")
	}
	m.vp.SetYOffset(line)
	m.conversationView.mode = anchored
	anchorLine := ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line])
	wordOffset := strings.Index(anchorLine, selectedWord)
	m = m.wordSelect(line, graphemeColForCellX(anchorLine, ansi.StringWidth(anchorLine[:wordOffset])))
	if !m.sel.active || m.sel.snapshot != selectedWord {
		t.Fatalf("precondition: selected settled text = %q (active=%t line=%q)", m.sel.snapshot, m.sel.active, anchorLine)
	}
	anchorID := toolBlockID(t, m.conv.scrollback, fmt.Sprintf("settled-%03d", anchorCall))
	if got := m.conversationView.frame.provenance[m.vp.YOffset()].blockID; got != anchorID {
		t.Fatalf("anchored block = %d, want %d", got, anchorID)
	}

	if !m.conv.resolveTool("live", "received", false) {
		t.Fatal("settle live call")
	}
	m.refreshView()
	for _, width := range []int{28, 100} {
		m.rend.setWidth(width)
		frame := m.rend.renderConversationFrame(&m.conv.scrollback, false)
		rows := blockRows(frame, liveID)
		if len(rows) != 1 || strings.ContainsAny(rows[0], "╭╮╰╯┌┐└┘─│") {
			t.Fatalf("width %d settled preceding call = %q, want one borderless line", width, rows)
		}
	}
	if got := m.conversationView.frame.provenance[m.vp.YOffset()].blockID; got != anchorID {
		t.Fatalf("settling a preceding card moved semantic anchor to block %d, want %d", got, anchorID)
	}
	if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != selectedWord {
		t.Fatalf("settling a later card lost selection = %q (active=%t)", selectedText(m.vp.GetContent(), m.sel), m.sel.active)
	}

	m = applyAll(m, tea.WindowSizeMsg{Width: 28, Height: 30})
	if got := m.conversationView.frame.provenance[m.vp.YOffset()].blockID; got != anchorID {
		t.Fatalf("resize moved semantic anchor to block %d, want %d", got, anchorID)
	}
	copied, cmd := m.copySelection()
	if got, ok := osc52Payload(collectLeaves(cmd)); !ok || got != selectedWord {
		t.Fatalf("copied settled selection = %q (ok=%t), want %q", got, ok, selectedWord)
	}
	m = copied.(Model)
	m.vp.GotoBottom()
	m.conversationView.mode = followTail
	m.conv.appendAssistant("tail after restored auto-follow")
	m.refreshView()
	if m.conversationView.mode != followTail || !m.vp.AtBottom() || !strings.Contains(ansi.Strip(m.vp.GetContent()), "restored auto-follow") {
		t.Fatalf("restored auto-follow: mode=%v bottom=%t content=%q", m.conversationView.mode, m.vp.AtBottom(), ansi.Strip(m.vp.GetContent()))
	}
}

func assertLongRunGap(t *testing.T, frame renderedFrame, before, after uint64, want int) {
	t.Helper()
	last, first := -1, -1
	for i, row := range frame.provenance {
		if row.blockID == before {
			last = i
		}
		if row.blockID == after && first < 0 {
			first = i
		}
	}
	if last < 0 || first < 0 || first-last-1 != want {
		t.Fatalf("block %d → %d gap = %d rows, want %d (positions %d → %d)", before, after, first-last-1, want, last, first)
	}
	for i := last + 1; i < first; i++ {
		if frame.lines[i] != "" || !frame.provenance[i].separator {
			t.Fatalf("block %d → %d gap row %d = %q, want derived blank separator", before, after, i, frame.lines[i])
		}
	}
}
