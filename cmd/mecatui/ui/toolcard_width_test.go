package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestToolCardWidthCap pins decision 7: a tool card never grows past
// toolCardMaxWidth columns on a wide terminal, but on a narrow terminal the
// contentWidth-2 inset wins (the card never exceeds the viewport content). The card is
// laid out against contentWidth() = r.width - the left-margin indent, so the cap binds at
// width ≥ toolCardMaxWidth + 2 + indent and the narrow card is (width - indent - 2). The
// card's rendered width is measured per-line via lipgloss.Width on the widest line
// (renderToolBlock calls renderTool directly, so the per-block indent prefix is NOT
// applied here — this measures the raw card).
func TestToolCardWidthCap(t *testing.T) {
	const capBindsAt = toolCardMaxWidth + 2 + defaultBlockIndent
	cases := []struct {
		name     string
		width    int
		wantMax  int // the card's rendered width must be ≤ this
		wantWide bool
	}{
		{"wide terminal caps at the max", 200, toolCardMaxWidth, true},
		{"exactly the cap-binding width caps at the max", capBindsAt, toolCardMaxWidth, true},
		{"narrow terminal uses contentWidth-2", 60, 60 - defaultBlockIndent - 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(tc.width)
			out := r.renderToolBlock("Read", `{"path":"greeting.txt"}`, false)
			got := maxLineWidth(out)
			if got > tc.wantMax {
				t.Errorf("width %d: card rendered %d cols, want ≤ %d", tc.width, got, tc.wantMax)
			}
			// A wide card should actually REACH the cap (border fills the card width), so
			// the cap is load-bearing, not vacuously satisfied by a short body.
			if tc.wantWide && got != toolCardMaxWidth {
				t.Errorf("width %d: capped card should render exactly %d cols, got %d", tc.width, toolCardMaxWidth, got)
			}
		})
	}
}

// TestToolCardWidthHardWrapsKnownRenderer covers the normal known-width card path:
// a collapsed Bash result's unbreakable divider must not escape the capped card.
func TestToolCardWidthHardWrapsKnownRenderer(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(185)
	resultLines := make([]string, 0, maxToolResultLines+27)
	resultLines = append(resultLines, strings.Repeat("-", 220))
	for range maxToolResultLines - 1 + 27 {
		resultLines = append(resultLines, "completed result line")
	}
	b := &block{
		kind:       blockTool,
		toolID:     "bash-1",
		toolName:   "Bash",
		toolArgs:   mustJSON(t, map[string]string{"command": strings.Repeat("x", toolCardMaxWidth+1)}),
		resolved:   true,
		resultBody: strings.Join(resultLines, "\n"),
	}

	out := r.renderTool(b, false)
	plain := stripANSIstr(out)
	if !strings.Contains(plain, "+27 more lines · ctrl+t expand") {
		t.Fatalf("collapsed Bash card lost its expansion marker:\n%s", plain)
	}
	if got := strings.Count(plain, "-"); got != 220 {
		t.Errorf("divider lost content while wrapping: got %d dashes, want 220", got)
	}
	maxWidth := 0
	for i, line := range strings.Split(out, "\n") {
		got := maxLineWidth(line)
		if got > maxWidth {
			maxWidth = got
		}
		if got > toolCardMaxWidth {
			t.Errorf("line %d exceeds card width %d (got %d): %q", i, toolCardMaxWidth, got, stripANSIstr(line))
		}
	}
	if maxWidth != toolCardMaxWidth {
		t.Errorf("card should be bounded at width %d, got %d", toolCardMaxWidth, maxWidth)
	}
}

// TestResolvedBashToolCardFitsViewport renders the normal transcript path, including
// the conversation indent, for a resolved Bash call whose command and result have no
// natural break points. Both views must remain within a narrow terminal.
func TestResolvedBashToolCardFitsViewport(t *testing.T) {
	const viewportWidth = 6
	command := strings.Repeat("x", 200)
	result := strings.Repeat("y", 200)

	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "collapsed", true: "expanded"}[expand], func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(viewportWidth)
			c := &conversation{}
			c.addTool("bash-1", "Bash", mustJSON(t, map[string]string{"command": command}))
			if !c.resolveTool("bash-1", result, false) {
				t.Fatal("resolve Bash tool")
			}

			out := r.renderConversation(c, expand)
			for i, line := range strings.Split(out, "\n") {
				if got := maxLineWidth(line); got > viewportWidth {
					t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, viewportWidth, got, stripANSIstr(line))
				}
			}
		})
	}
}

// TestTranscriptReflowsOnWidthOnlyResize covers the line-slice SetContentLines
// handoff used by the normal transcript. A narrow resize with unchanged body height
// must replace, rather than retain, a wide Bash card render.
func TestTranscriptReflowsOnWidthOnlyResize(t *testing.T) {
	const (
		wideWidth   = 160
		narrowWidth = 100
		height      = 30
	)
	command := "task docs && task site:build && git status --short --branch && git diff --check && git diff --stat && git diff --name-only --cached"
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: wideWidth, Height: height},
		client.ToolCallMsg{ID: "bash-1", Name: "Bash", Args: mustJSON(t, map[string]string{"command": command})},
		client.ToolResultMsg{CallID: "bash-1", Content: "docs and site passed\n M cmd/mecatui/ui/update.go"},
	)
	if m.sel.active || m.expandTools {
		t.Fatal("precondition: normal transcript must use SetContentLines")
	}
	bodyHeight := m.vp.Height()

	m = applyAll(m, tea.WindowSizeMsg{Width: narrowWidth, Height: height})
	if m.vp.Height() != bodyHeight {
		t.Fatalf("precondition: width-only resize changed body height from %d to %d", bodyHeight, m.vp.Height())
	}
	for i, line := range strings.Split(m.vp.GetContent(), "\n") {
		if width := maxLineWidth(line); width > narrowWidth {
			t.Errorf("viewport line %d exceeds width %d (got %d): %q", i, narrowWidth, width, stripANSIstr(line))
		}
	}
}

// TestTranscriptWidthOnlyResizeResticksAndFollowsTranscript covers the scroll-state
// half of the normal transcript width-only refresh. Reflow can reduce the transcript
// until an initially unstuck viewport is now at its bottom; the resize must derive
// stuck from that resulting position before the next transcript event arrives.
func TestTranscriptWidthOnlyResizeResticksAndFollowsTranscript(t *testing.T) {
	const (
		narrowWidth = 100
		wideWidth   = 200
		height      = 30
	)
	m := newMCPModel(t, aztec(), nil)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: narrowWidth, Height: height},
		client.TurnStartMsg{Turn: 1},
		client.AssistantDeltaMsg{Turn: 1, Text: strings.Repeat("reflowed transcript text ", 100)},
		renderTickMsg{},
	)

	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.stuck || m.vp.AtBottom() {
		t.Fatalf("precondition: pgup should leave the narrow viewport unstuck (stuck=%v atBottom=%v)", m.stuck, m.vp.AtBottom())
	}

	m = applyAll(m, tea.WindowSizeMsg{Width: wideWidth, Height: height})
	if m.stuck != m.vp.AtBottom() {
		t.Fatalf("width-only resize must synchronize stuck: stuck=%v atBottom=%v", m.stuck, m.vp.AtBottom())
	}
	if !m.stuck {
		t.Fatal("precondition: wider reflow should leave the viewport at bottom")
	}

	m = applyAll(m, client.ToolCallMsg{ID: "follow-1", Name: "Read", Args: `{"path":"README.md"}`})
	if !m.vp.AtBottom() {
		t.Fatal("a transcript event after the re-synchronized resize should follow the bottom")
	}
}

// TestResolvedBashToolCardFitsOneColumnViewport ensures the transcript indent is
// suppressed when it would otherwise make a frameless, one-column card overflow.
func TestResolvedBashToolCardFitsOneColumnViewport(t *testing.T) {
	r := newTestRenderer()
	r.setWidth(1)
	c := &conversation{}
	c.addTool("bash-1", "Bash", mustJSON(t, map[string]string{"command": strings.Repeat("x", 200)}))
	if !c.resolveTool("bash-1", strings.Repeat("y", 200), false) {
		t.Fatal("resolve Bash tool")
	}

	for i, line := range strings.Split(r.renderConversation(c, false), "\n") {
		if got := maxLineWidth(line); got > r.width {
			t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, r.width, got, stripANSIstr(line))
		}
	}
}

// TestResolvedBashToolCardFitsFrameTransition verifies the first width that can
// render the normal card frame while retaining the existing 2-cell right inset.
func TestResolvedBashToolCardFitsFrameTransition(t *testing.T) {
	command := strings.Repeat("x", 200)
	result := strings.Repeat("y", 200)

	for _, expand := range []bool{false, true} {
		t.Run(map[bool]string{false: "collapsed", true: "expanded"}[expand], func(t *testing.T) {
			r := newTestRenderer()
			r.setWidth(defaultBlockIndent + r.th.Style("toolCard").GetHorizontalFrameSize() + 2)
			c := &conversation{}
			c.addTool("bash-1", "Bash", mustJSON(t, map[string]string{"command": command}))
			if !c.resolveTool("bash-1", result, false) {
				t.Fatal("resolve Bash tool")
			}

			for i, line := range strings.Split(r.renderConversation(c, expand), "\n") {
				if got := maxLineWidth(line); got > r.width {
					t.Errorf("line %d exceeds viewport width %d (got %d): %q", i, r.width, got, stripANSIstr(line))
				}
			}
		})
	}
}

// TestToolCardWidthUnchangedAtCap proves the cap is a no-op exactly at the cap
// boundary: at the cap-binding width the card is identical to a card at a far wider
// width — i.e. the cap only ever clamps, it never changes a card that already fits.
func TestToolCardWidthUnchangedAtCap(t *testing.T) {
	const capBindsAt = toolCardMaxWidth + 2 + defaultBlockIndent

	r1 := newTestRenderer()
	r1.setWidth(capBindsAt) // contentWidth-2 == cap, min(cap, cap) == cap
	a := r1.renderToolBlock("Read", `{"path":"x"}`, false)

	r2 := newTestRenderer()
	r2.setWidth(400) // far past the cap → min clamps to the cap
	b := r2.renderToolBlock("Read", `{"path":"x"}`, false)

	if a != b {
		t.Errorf("card at the cap-binding width and at 400 must be byte-identical (both clamp to the cap):\nlen(a)=%d len(b)=%d", lipgloss.Width(a), lipgloss.Width(b))
	}
}
