package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// TestMecatuiCardLayout_Scenario4_DynamicCardTextIsTerminalSafe verifies AC4.3:
// dynamic plain-text card fields cannot inject terminal controls or bidi formatting.
// Layout newlines and tabs remain available to the card renderers, and assistant
// markdown continues through its dedicated Glamour renderer.
func TestMecatuiCardLayout_Scenario4_DynamicCardTextIsTerminalSafe(t *testing.T) {
	const width = 52
	const marker = "visible\tcolumn\nnext-row"
	const unsafe = "\x00\x1b]0;spoof\x07\u009d9;title\u202e\u2066\x1b[2J"
	value := marker + unsafe + " trailing"

	assertPlainCard := func(t *testing.T, name, out string, maxWidth int) {
		t.Helper()
		for _, forbidden := range []string{"\x00", "\a", "\x1b]", "\x1b[2J", "\u009d", "\u202e", "\u2066"} {
			if strings.Contains(out, forbidden) {
				t.Fatalf("%s retained unsafe terminal sequence %q in %q", name, forbidden, out)
			}
		}
		plain := stripANSIstr(out)
		if got := terminaltext.Sanitize(plain); got != plain {
			t.Fatalf("%s retained terminal control or format rune in %q", name, plain)
		}
		if !strings.Contains(plain, "visible") || !strings.Contains(plain, "column") || !strings.Contains(plain, "next-row") {
			t.Errorf("%s lost permitted layout content: %q", name, plain)
		}
		for row, line := range strings.Split(plain, "\n") {
			if strings.Contains(line, "visible") && lipgloss.Width(line) > maxWidth {
				t.Errorf("%s dynamic row %d width = %d, want <= %d: %q", name, row, lipgloss.Width(line), maxWidth, line)
			}
		}
	}

	if got, want := terminaltext.Sanitize(value), marker+"]0;spoof9;title[2J trailing"; got != want {
		t.Fatalf("plain card sanitizer = %q, want %q; layout tab/newline must survive", got, want)
	}

	r := newTestRenderer()
	r.setWidth(width)
	_, cardWidth, _ := r.toolCardLayout()
	t.Run("tool card", func(t *testing.T) {
		out := r.renderTool(&block{
			kind:       blockTool,
			toolID:     "unsafe-tool",
			toolName:   "Read-" + value,
			toolArgs:   `{"path":"` + value + `"}`,
			resolved:   true,
			resultBody: value,
		}, true)
		assertPlainCard(t, "tool card", out, cardWidth)
	})

	t.Run("approval", func(t *testing.T) {
		ask := pendingAsk{
			Tool:   "Shell-" + value,
			Args:   `{"command":"` + value + `"}`,
			Reason: value,
		}
		assertPlainCard(t, "approval", renderApprovalModalWithRenderer(r, ask, false, width, 30), width)
	})

	th, hk := aztec(), defaultHelpKeys()
	t.Run("inventory", func(t *testing.T) {
		out := renderAgentsInvPanel(th, agentsInvState{agents: []client.Agent{{
			Name:           value,
			Description:    value,
			Model:          value,
			PermissionMode: value,
			Tools:          []string{value},
		}}}, client.Capabilities{Agents: true}, hk, width)
		assertPlainCard(t, "inventory", out, width)
	})

	t.Run("detail", func(t *testing.T) {
		out := centerCard(th, renderLearnedSkillDetail(th, client.LearnedSkill{
			Name:        value,
			OwnerAgent:  value,
			State:       value,
			Version:     value,
			Revision:    value,
			Description: value,
			Body:        value,
		}, value, width), width, 40)
		assertPlainCard(t, "detail", out, 72)
	})

	t.Run("markdown remains glamour-rendered", func(t *testing.T) {
		out := stripANSIstr(r.markdown("**markdown**  \nsecond line"))
		if !strings.Contains(out, "markdown") || !strings.Contains(out, "second line") {
			t.Fatalf("Glamour markdown path lost formatted content: %q", out)
		}
		if strings.Contains(out, "**markdown**") {
			t.Fatalf("markdown bypassed Glamour rendering: %q", out)
		}
	})
}
