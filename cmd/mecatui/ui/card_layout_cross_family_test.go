package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiCardLayout_Scenario4_NoPaddingBeforeWrapRegression verifies AC4.1:
// each card family prepares its long, short, and whitespace-only source rows before
// style alignment can add padding. The fixture has one intentional blank source row;
// no renderer-created padding may add another visual row.
func TestMecatuiCardLayout_Scenario4_NoPaddingBeforeWrapRegression(t *testing.T) {
	const bodyWidth = 12
	long := "long-row-" + strings.Repeat("value-", 4)
	fixture := long + "\nshort\n   \t"

	assertRows := func(t *testing.T, family string, rows []string, wantBlank int) {
		t.Helper()
		blank := 0
		for i, row := range rows {
			if got := lipgloss.Width(row); got > bodyWidth {
				t.Errorf("%s row %d width = %d, want <= %d: %q", family, i, got, bodyWidth, row)
			}
			if strings.TrimSpace(row) == "" {
				blank++
			}
		}
		if blank != wantBlank {
			t.Errorf("%s blank rows = %d, want %d; style padding must not create rows: %q", family, blank, wantBlank, rows)
		}
		if !strings.Contains(strings.Join(rows, "\n"), "short") {
			t.Errorf("%s lost the short source row: %q", family, rows)
		}
	}

	t.Run("tool result", func(t *testing.T) {
		lines := resultLines(fixture, resultLineBody)
		rows := wrapResultDisplayLines(lines, bodyWidth)
		plain := make([]string, len(rows))
		for i, row := range rows {
			plain[i] = row.text
		}
		assertRows(t, "tool result", plain, 1)
	})

	t.Run("delegation", func(t *testing.T) {
		rows := wrapDelegationRow("· ", fixture, bodyWidth)
		assertRows(t, "delegation", rows, 0)
	})

	t.Run("approval", func(t *testing.T) {
		rows := strings.Split(wrapApprovalReason(fixture, bodyWidth), "\n")
		assertRows(t, "approval", rows, 1)
	})

	t.Run("dynamic inventory", func(t *testing.T) {
		rows := strings.Split(stripANSIstr(agentsInvRowLines(aztec(), []client.Agent{{Name: fixture}}, bodyWidth)[0]), "\n")
		// Dynamic inventory names normalize display-only trailing whitespace before
		// wrapping, so the final whitespace-only source row is not a paragraph.
		assertRows(t, "dynamic inventory", rows, 0)
	})

	t.Run("tool card plain region", func(t *testing.T) {
		rows := strings.Split(wrapToolCardText(fixture, bodyWidth), "\n")
		assertRows(t, "tool card", rows, 1)
	})
}

// TestMecatuiCardLayout_Scenario4_RenderingExceptionsRemainIntact verifies AC4.2:
// plain dynamic card text is sanitized and its display-only right padding is removed
// before wrapping and styling. Markdown and the full-width input rail deliberately
// retain their separate renderer contracts.
func TestMecatuiCardLayout_Scenario4_RenderingExceptionsRemainIntact(t *testing.T) {
	const bodyWidth = 14
	plain := "dynamic-row-" + strings.Repeat("value-", 3) + "\x1b[2J\u202e   "

	t.Run("plain dynamic card text", func(t *testing.T) {
		rows := strings.Split(stripANSIstr(agentsInvRowLines(aztec(), []client.Agent{{Name: plain}}, bodyWidth)[0]), "\n")
		joined := strings.Join(rows, "\n")
		if strings.ContainsRune(joined, '\x1b') || strings.ContainsRune(joined, '\u202e') {
			t.Fatalf("plain card text retained terminal controls: %q", joined)
		}
		for i, row := range rows {
			if got := lipgloss.Width(row); got > bodyWidth {
				t.Errorf("plain card row %d width = %d, want <= %d: %q", i, got, bodyWidth, row)
			}
			if strings.HasSuffix(row, " ") {
				t.Errorf("plain card row %d retained display-only trailing padding: %q", i, row)
			}
		}
	})

	t.Run("markdown preserves hard breaks", func(t *testing.T) {
		r := newTestRenderer()
		out := stripANSIstr(r.markdown("first line  \nsecond line"))
		if !strings.Contains(out, "first line") || !strings.Contains(out, "second line") {
			t.Fatalf("markdown lost hard-break content: %q", out)
		}
		if strings.Index(out, "first line") > strings.Index(out, "second line") {
			t.Errorf("markdown reordered hard-break content: %q", out)
		}
	})

	t.Run("input rail keeps fixed background width", func(t *testing.T) {
		m, _, _ := newTestModel(t, aztec())
		m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})
		m.rend.inputValid = false
		if got := lipgloss.Width(m.renderInput()); got != m.width {
			t.Errorf("input rail width = %d, want fixed background width %d", got, m.width)
		}
	})
}
