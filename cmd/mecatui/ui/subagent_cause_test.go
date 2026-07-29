package ui

// Tests for issue #319's TUI half: a FAILED delegation must be able to tell the operator
// WHY. The inline Subagent card already surfaces the cause through the tool result the
// agent received (that is the server's model-facing body — see TestSubagentErrorCardShows
// ProviderCause below), but the ctrl+a fleet pane has no result body at all: a roster row
// shows only "stop:error", and a BACKGROUND child's failure never reaches an inline card
// because its Subagent call already returned the started-result. subagent.end's Cause is
// the only channel there.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// endSubFailed builds an errored subagent.end carrying a failure cause.
func endSubFailed(parent, child, cause string) client.SubagentMsg {
	return client.SubagentMsg{
		Kind: client.SubagentEnd, ParentCallID: parent, ChildID: child,
		Stop: "error", Cause: cause, DurationMs: 900,
	}
}

// focusFirstChild opens the ctrl+a overlay and focuses the first fleet row, returning the
// stripped focus-pane render.
func focusFirstChild(t *testing.T, m Model) string {
	t.Helper()
	mm, _ := m.Update(ctrlKey('a'))
	m = mm.(Model)
	if m.agentsTab != tabSubagents {
		t.Fatalf("expected the Subagents tab, got %v", m.agentsTab)
	}
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if m.subagents.view != subagentFocus {
		t.Fatalf("enter should focus a child, view = %v", m.subagents.view)
	}
	return stripANSIstr(m.View().Content)
}

// TestSubagentFocusPaneShowsFailureCause is the fleet-pane half: an errored child's focus
// pane names the failure, so ctrl+a answers "why did it fail" instead of only "it did".
func TestSubagentFocusPaneShowsFailureCause(t *testing.T) {
	const cause = "upstream 503: model overloaded"
	m := newMCPModel(t, aztec(), nil)
	m = seedSubagents(m, "p1",
		startSub("p1", "c1", "audit auth"),
		endSubFailed("p1", "c1", cause),
	)
	out := focusFirstChild(t, m)
	if !strings.Contains(out, cause) {
		t.Fatalf("the focus pane must name the failure cause, got %q", out)
	}
	if !strings.Contains(out, "failed:") {
		t.Fatalf("the failure line should be labelled, got %q", out)
	}
}

// TestSubagentFocusPaneOmitsCauseWhenBenign is the negative half: no failure line for a
// clean terminal, and none for an errored child whose server sent no cause (an older
// server) — the pane then reads exactly as it did before.
func TestSubagentFocusPaneOmitsCauseWhenBenign(t *testing.T) {
	t.Run("clean terminal", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "p1",
			startSub("p1", "c1", "audit auth"),
			endSub("p1", "c1", 100, 20, 2, "end_turn"),
		)
		if out := focusFirstChild(t, m); strings.Contains(out, "failed:") {
			t.Fatalf("a clean child must show no failure line, got %q", out)
		}
	})
	t.Run("errored but no cause from the server", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "p1",
			startSub("p1", "c1", "audit auth"),
			endSubFailed("p1", "c1", ""),
		)
		if out := focusFirstChild(t, m); strings.Contains(out, "failed:") {
			t.Fatalf("no cause means no failure line, got %q", out)
		}
	})
}

// TestSubagentFailureLineIsBoundedAndSingleLine proves the display is safe against a
// pathological provider error body: multi-line content is collapsed to one line (the pane
// is line-oriented) and the whole thing is clamped, so one failure cannot blow the
// overlay card's width out or push the roster off-screen.
func TestSubagentFailureLineIsBoundedAndSingleLine(t *testing.T) {
	ln := &subagentLane{
		done:  true,
		stop:  "error",
		cause: "line one\nline two\n" + strings.Repeat("z", maxSubagentCauseWidth*3),
	}
	got := subagentFailureLine(ln)
	if strings.Contains(got, "\n") {
		t.Fatalf("the failure line must be collapsed to ONE line, got %q", got)
	}
	if !strings.Contains(got, "line one line two") {
		t.Fatalf("collapsing must join the lines with spaces, got %q", got)
	}
	if n := len([]rune(got)); n > maxSubagentCauseWidth+len("  failed: ")+1 {
		t.Fatalf("the failure line was not clamped: %d runes (%q)", n, got)
	}
}

// TestSubagentErrorCardShowsProviderCause is the INLINE-card half. The card deliberately
// has no second cause slot: the server's error body already carries the cause (issue #319
// composes it there), and the card renders that body in its result slot. This pins that
// path end-to-end so a regression in either half — the server dropping the cause, or the
// card stopping rendering the error body — is caught here.
func TestSubagentErrorCardShowsProviderCause(t *testing.T) {
	const cause = "upstream 503: model overloaded"
	out := subagentCard(t, false, func(c *conversation) {
		c.setSubagentStart("p1", "investigate the loop", "", "", "")
		c.setSubagentEnd("p1", client.Usage{}, 0, "error", 100)
		// The server-composed body: the cause leads, the child's last text follows as
		// labelled context (renderSubagentResult → subagentErrorBody).
		c.resolveTool("p1", "Subagent: "+cause+
			"\n\nLast activity before the failure: Now let me check the tests."+
			"\n\nagentId: subagent-p1", true)
	})
	if !strings.Contains(out, "stop:error") {
		t.Fatalf("errored card should show stop:error, got %q", out)
	}
	if !strings.Contains(out, cause) {
		t.Fatalf("errored card must render the provider cause, got %q", out)
	}
}
