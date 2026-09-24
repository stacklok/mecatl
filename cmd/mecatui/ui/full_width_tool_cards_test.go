package ui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiFullWidthToolCards_Scenario1_WideToolCardFillsConversationWidth(t *testing.T) {
	const width = 180
	r := newTestRenderer()
	r.setWidth(width)
	c := &conversation{}
	c.addTool("read-1", "Read", `{"path":"README.md"}`)

	out := r.renderConversation(c, false)
	if got := maxLineWidth(out); got != width {
		t.Fatalf("wide conversation tool card width = %d, want terminal width %d", got, width)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("row %d width = %d, want <= %d", i, got, width)
		}
	}
}

func TestMecatuiFullWidthToolCards_Scenario1_SkillAndOrdinaryToolsShareLayout(t *testing.T) {
	const width = 180
	for _, tool := range []string{"Skill", "Read"} {
		t.Run(tool, func(t *testing.T) {
			for _, expanded := range []bool{false, true} {
				r := newTestRenderer()
				r.setWidth(width)
				c := &conversation{}
				args := mustJSON(t, map[string]string{"name": "fixture", "complete_argument": strings.Repeat("argument-", 20)})
				c.addTool(tool+"-1", tool, args)
				if !c.resolveTool(tool+"-1", "complete-result-"+strings.Repeat("result-", 20), false) {
					t.Fatal("resolve tool")
				}

				out := stripANSIstr(r.renderConversation(c, expanded))
				if got := maxLineWidth(out); got != width {
					t.Errorf("expanded=%t card width = %d, want %d", expanded, got, width)
				}
				if !strings.Contains(out, tool) || !strings.Contains(out, "fixture") || !strings.Contains(out, "complete-result-") {
					t.Errorf("expanded=%t card omitted representative tool, argument, or result content:\n%s", expanded, out)
				}
				if expanded && !strings.Contains(out, "complete_argument") {
					t.Errorf("expanded card omitted complete arguments:\n%s", out)
				}
			}
		})
	}
}

func TestMecatuiFullWidthToolCards_Scenario1_ResizeReflowsWithoutOverflow(t *testing.T) {
	const height = 30
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 180, Height: height},
		client.ToolCallMsg{ID: "shell-1", Name: "Shell", Args: mustJSON(t, map[string]string{"command": strings.Repeat("command-", 40)})},
		client.ToolResultMsg{CallID: "shell-1", Content: strings.Repeat("result-", 50)},
	)

	for _, width := range []int{180, 37, 180} {
		m = applyAll(m, tea.WindowSizeMsg{Width: width, Height: height})
		if got := maxLineWidth(m.vp.GetContent()); got != width {
			t.Errorf("terminal width %d: framed card width = %d, want %d", width, got, width)
		}
		for i, line := range strings.Split(m.vp.GetContent(), "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("terminal width %d row %d overflows: got %d", width, i, got)
			}
		}
	}

	r := newTestRenderer()
	frame := r.th.Style("toolCard").GetHorizontalFrameSize()
	for _, tc := range []struct {
		width      int
		wantFramed bool
	}{
		{defaultBlockIndent + frame + 1, true},
		{defaultBlockIndent + frame, false},
	} {
		r.setWidth(tc.width)
		out := stripANSIstr(r.renderToolBlock("Read", `{"path":"x"}`, false))
		framed := strings.ContainsAny(out, "╭╮╰╯│")
		if framed != tc.wantFramed {
			t.Errorf("width %d framed = %t, want %t:\n%s", tc.width, framed, tc.wantFramed, out)
		}
		if got := maxLineWidth(out); got > r.contentWidth() {
			t.Errorf("width %d raw card width = %d, want <= content width %d", tc.width, got, r.contentWidth())
		}
	}
}

func TestADR_0301_full_width_tool_card_reflow_preserves_logical_view_state(t *testing.T) {
	t.Run("selection and expansion", func(t *testing.T) {
		const marker = "STABLE_SELECTION_MARKER"
		m, _ := selModel(t)
		m = applyAll(m, tea.WindowSizeMsg{Width: 180, Height: 30})
		m.conv.addTool("read", "Read", `{"path":"a/long/path/that/reflows.txt"}`)
		m.conv.resolveTool("read", marker+" remains selected across width changes", false)
		m.expandTools = true
		m.refreshView()
		if got := maxLineWidth(m.vp.GetContent()); got != 180 {
			t.Fatalf("wide card width = %d, want 180", got)
		}
		line := lineIndexContaining(m.vp.GetContent(), marker)
		if line < 0 {
			t.Fatal("selection marker missing")
		}
		col := strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), marker)
		m = m.wordSelect(line, col)
		m = applyAll(m, tea.WindowSizeMsg{Width: 52, Height: 30})
		if !m.expandTools {
			t.Fatal("resize cleared expanded tool detail state")
		}
		if !m.sel.active || selectedText(m.vp.GetContent(), m.sel) != marker {
			t.Fatalf("stable selection after reflow = %q (active=%t), want %q", selectedText(m.vp.GetContent(), m.sel), m.sel.active, marker)
		}

		tool := &m.conv.blocks[len(m.conv.blocks)-1]
		tool.resultBody = "replacement text"
		tool.rev++
		m.refreshView()
		if m.sel.active {
			t.Fatal("selection survived after its selected visible text changed")
		}
	})

	t.Run("width-driven hidden selection clears", func(t *testing.T) {
		const marker = "WIDTH_HIDDEN_SELECTION_MARKER"
		m, _ := selModel(t)
		m = applyAll(m, tea.WindowSizeMsg{Width: 180, Height: 30})
		m.conv.addTool("read", "Read", `{"path":"reflow.txt"}`)
		m.conv.resolveTool("read", strings.Repeat("wide-content ", 50)+marker, false)
		m.refreshView()
		line := lineIndexContaining(m.vp.GetContent(), marker)
		if line < 0 {
			t.Fatalf("precondition: selection marker missing at wide width:\n%s", ansi.Strip(m.vp.GetContent()))
		}
		col := strings.Index(ansi.Strip(strings.Split(m.vp.GetContent(), "\n")[line]), marker)
		m = m.wordSelect(line, col)
		if got := selectedText(m.vp.GetContent(), m.sel); got != marker {
			t.Fatalf("selected wide marker = %q, want %q", got, marker)
		}

		m = applyAll(m, tea.WindowSizeMsg{Width: 52, Height: 30})
		if strings.Contains(ansi.Strip(m.vp.GetContent()), marker) {
			t.Fatal("precondition: narrow collapsed card still exposes width-hidden marker")
		}
		if m.sel.active {
			t.Fatal("selection survived after width-driven reflow hid its visible text")
		}
	})

	t.Run("manual anchor and tail follow", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = applyAll(m, tea.WindowSizeMsg{Width: 72, Height: 18}, client.SessionReadyMsg{SessionID: "anchor-session"})
		for i := range 24 {
			id := "read-" + strconv.Itoa(i)
			m.conv.addTool(id, "Read", mustJSON(t, map[string]string{"path": strings.Repeat("path/", 8) + strconv.Itoa(i)}))
			m.conv.resolveTool(id, "result "+strconv.Itoa(i)+" "+strings.Repeat("content ", 8), false)
		}
		m.refreshView()
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
		if m.conversationView.mode != anchored {
			t.Fatal("precondition: pgup did not establish a manual reading anchor")
		}
		before := m.conversationView.anchor
		m = applyAll(m, tea.WindowSizeMsg{Width: 140, Height: 18})
		after := m.conversationView.anchor
		if after.blockID != before.blockID || after.region != before.region || after.sourceOffset != before.sourceOffset {
			t.Fatalf("manual anchor moved from %#v to %#v", before, after)
		}

		m.conversationView.mode = followTail
		m.refreshView()
		if !m.vp.AtBottom() {
			t.Fatal("tail-follow reader is not at bottom before resize")
		}
		m = applyAll(m, tea.WindowSizeMsg{Width: 90, Height: 18})
		m = applyAll(m, client.ToolCallMsg{ID: "tail", Name: "Read", Args: `{"path":"tail"}`})
		if m.conversationView.mode != followTail || !m.vp.AtBottom() {
			t.Fatalf("tail follow after resize and event = mode %v atBottom %t", m.conversationView.mode, m.vp.AtBottom())
		}
	})
}

func TestMecatuiFullWidthToolCards_Scenario1_ContentAndSafetyContractsRemainIntact(t *testing.T) {
	const width = 180
	r := newTestRenderer()
	r.setWidth(width)
	resultRows := make([]string, maxToolResultLines+2)
	for i := range resultRows {
		resultRows[i] = "result-row-" + strconv.Itoa(i) + strings.Repeat("x", 20)
	}
	b := &block{
		kind:       blockTool,
		toolID:     "read-1",
		toolName:   "Read\x1b[2J",
		toolArgs:   mustJSON(t, map[string]string{"path": "❤️ 🇺🇸 " + strings.Repeat("path/", 50) + "\x1b[2J"}),
		resolved:   true,
		resultBody: "❤️ 🇺🇸 " + strings.Join(resultRows, "\n") + "\x1b]0;unsafe\a",
	}

	collapsed := r.renderTool(b, false)
	if got := maxLineWidth(collapsed); got != r.contentWidth() {
		t.Fatalf("full-width raw card = %d, want content width %d", got, r.contentWidth())
	}
	plainCollapsed := stripANSIstr(collapsed)
	if strings.Contains(collapsed, "\x1b[2J") || strings.Contains(collapsed, "\x1b]0;unsafe") {
		t.Fatal("tool card retained server-supplied terminal controls")
	}
	if !strings.Contains(plainCollapsed, "+2 more lines · ctrl+t expand") || strings.Contains(plainCollapsed, "result-row-12") {
		t.Fatalf("collapsed result-row cap changed:\n%s", plainCollapsed)
	}
	assertToolCardRowsSafe(t, collapsed, r.contentWidth())

	expandedRaw := r.renderTool(b, true)
	expanded := stripANSIstr(expandedRaw)
	if !strings.Contains(expanded, "result-row-13") || strings.Contains(expanded, "ctrl+t expand") {
		t.Fatalf("expanded card did not expose complete result:\n%s", expanded)
	}
	assertToolCardRowsSafe(t, expandedRaw, r.contentWidth())
	for i, row := range strings.Split(expanded, "\n") {
		if got := lipgloss.Width(row); got > r.contentWidth() {
			t.Errorf("expanded raw-before-style row %d width = %d, want <= %d", i, got, r.contentWidth())
		}
	}

	c := &conversation{blocks: []block{*b}}
	c.blocks[0].id = 1
	frame := r.renderConversationFrame(c, false)
	if len(frame.lines) != len(frame.provenance) {
		t.Fatalf("frame lines/provenance = %d/%d", len(frame.lines), len(frame.provenance))
	}
	seenResult := false
	for i, line := range frame.lines {
		if strings.Contains(stripANSIstr(line), "result-row-") {
			seenResult = true
			if frame.provenance[i].region != conversationRegionResult {
				t.Errorf("result row %d provenance = %v, want result", i, frame.provenance[i].region)
			}
		}
	}
	if !seenResult {
		t.Fatal("frame contained no visible semantic result row")
	}
}

func assertToolCardRowsSafe(t *testing.T, out string, width int) {
	t.Helper()
	for i, row := range strings.Split(out, "\n") {
		if grapheme, wc := ansi.StringWidth(row), ansi.StringWidthWc(row); grapheme != wc {
			t.Errorf("tool-card row %d width methods disagree: grapheme=%d wc=%d: %q", i, grapheme, wc, stripANSIstr(row))
		}
		if got := ansi.StringWidth(row); got > width {
			t.Errorf("tool-card row %d width = %d, want <= %d: %q", i, got, width, stripANSIstr(row))
		}
	}
}
