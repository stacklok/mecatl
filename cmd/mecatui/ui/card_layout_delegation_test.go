package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// TestMecatuiCardLayout_Scenario2_DelegationRowsFitBodyWidth verifies AC2.1:
// every delegation row is wrapped as raw text within the supplied card body
// before its style and the final frame are applied.
func TestMecatuiCardLayout_Scenario2_DelegationRowsFitBodyWidth(t *testing.T) {
	const width = 36
	long := strings.Repeat("unbreakable-delegation-value-", 5)

	viewAtWidth := func(t *testing.T, m Model) string {
		t.Helper()
		mm, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		return stripANSIstr(mm.(Model).View().Content)
	}
	assertFits := func(t *testing.T, name, out string) {
		t.Helper()
		if !strings.Contains(out, "unbreakable-") {
			t.Errorf("%s lost the raw delegation content: %q", name, out)
		}
		for row, line := range strings.Split(out, "\n") {
			if got := maxLineWidth(line); got > width {
				t.Errorf("%s row %d width = %d, want ≤ %d: %q", name, row, got, width, line)
			}
		}
	}

	t.Run("trace", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedSubagents(m, "p1",
			startSub("p1", "child-1", "inspect"),
			toolSubPreview("p1", "child-1", "tool.call", long, long, 1),
		)
		m.agentsTab = tabSubagents
		m.team.view = teamRoster
		m.subagents = subagentState{view: subagentFocus, child: "child-1"}
		assertFits(t, "trace", viewAtWidth(t, m))
	})

	t.Run("roster", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedTeam(m, func(c *conversation) {
			c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: long, Role: long}})
			c.addTeamMember(member(long, "tool.call", client.TeamMsg{ToolName: long}))
		})
		m.agentsTab = tabTeams
		m.team = teamState{view: teamRoster}
		assertFits(t, "roster", viewAtWidth(t, m))
	})

	t.Run("task", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedTeam(m, func(c *conversation) {
			c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: "lead"}})
			c.setTeamTasks("t1", []client.TeamTask{{ID: long, Description: long, State: taskStatePending, Assignee: long, Deps: []string{long}}})
		})
		m.agentsTab = tabTeams
		m.team = teamState{view: teamTasks}
		assertFits(t, "task", viewAtWidth(t, m))
	})

	t.Run("finding", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedTeam(m, func(c *conversation) {
			c.setTeamStart("t1", "", []client.TeamMemberSpec{{Name: "lead"}})
			c.setTeamFindings("t1", []client.TeamFinding{{Member: long, Body: long + "   "}})
		})
		m.agentsTab = tabTeams
		m.team = teamState{view: teamFindings}
		assertFits(t, "finding", viewAtWidth(t, m))
	})

	t.Run("focus", func(t *testing.T) {
		m := newMCPModel(t, aztec(), nil)
		m = seedParallel(m, "p1",
			startPar("p1", "all", 1),
			branchStartPar("p1", 0, long, long),
			branchToolParPreview("p1", 0, "tool.call", long, long, 1),
		)
		m.agentsTab = tabParallel
		m.team.view = teamRoster
		m.parallel = parallelState{view: parallelGroupView, group: "p1"}
		assertFits(t, "focus", viewAtWidth(t, m))
	})
}

// TestMecatuiCardLayout_Scenario2_ApprovalRowsWrapBeforeStyle verifies AC2.2:
// reason and non-diff argument source rows are sanitized and bounded to the
// approval body's fixed width before their styles and card frame are applied.
func TestMecatuiCardLayout_Scenario2_ApprovalRowsWrapBeforeStyle(t *testing.T) {
	const width = permissionModalMaxWidth + 24
	r := newTestRenderer()
	th := r.th
	contentWidth := askArgsCardContentWidth(th, width-th.Style("askCard").GetHorizontalFrameSize())
	argsWidth := contentWidth - th.Style("askArgs").GetHorizontalFrameSize()
	longArg := "argument-row-" + strings.Repeat("x", argsWidth+17)
	longReason := "reason-row-" + strings.Repeat("y", contentWidth+17)
	ask := pendingAsk{
		Tool:   "Bash",
		Args:   `{"command":"` + longArg + `"}`,
		Reason: longReason,
	}

	pretty, _, ok := askArgsContent(th, ask)
	if !ok {
		t.Fatal("non-diff ask must have an args region")
	}
	args := askArgsMiniViewport(th, pretty, contentWidth, 40)
	for i, row := range args.lines {
		if got := lipgloss.Width(row); got > argsWidth {
			t.Errorf("argument source row %d width = %d, want ≤ %d before askArgs style: %q", i, got, argsWidth, row)
		}
		if strings.TrimSpace(strings.TrimSuffix(row, wrapContinuationMarker)) == "" {
			t.Errorf("argument continuation row %d is whitespace-only: %q", i, row)
		}
	}
	for i, row := range strings.Split(wrapApprovalReason(ask.Reason, contentWidth), "\n") {
		if got := lipgloss.Width(row); got > contentWidth {
			t.Errorf("reason source row %d width = %d, want ≤ %d before muted style: %q", i, got, contentWidth, row)
		}
		if strings.TrimSpace(row) == "" {
			t.Errorf("reason continuation row %d is whitespace-only: %q", i, row)
		}
	}

	rendered := stripANSIstr(renderApprovalModalWithRenderer(r, ask, false, width, 40))
	for i, row := range strings.Split(rendered, "\n") {
		if got := maxLineWidth(row); got > width {
			t.Errorf("approval card row %d width = %d, want ≤ %d: %q", i, got, width, row)
		}
	}

	// The wrapped source rows do not alter the button hit row while the args
	// mini-viewport scrolls: both frames derive their geometry from the same body.
	s := approvalSurfaceForRender(r, ask, false, 0, 0)
	hits := &hitRegions{}
	s.deps.hits = hits
	offeredWidth := width - th.Style("askCard").GetHorizontalFrameSize()
	_, before := s.permissionModalBodyParts(offeredWidth, 40)
	_, beforeHits := s.Render(offeredWidth, 40)
	if len(beforeHits) != 2 {
		t.Fatalf("initial approval hit regions = %d, want 2", len(beforeHits))
	}
	s.miniScroll(s.miniScrollRange(), s.miniScrollRange())
	_, after := s.permissionModalBodyParts(offeredWidth, 40)
	_, afterHits := s.Render(offeredWidth, 40)
	if before != after {
		t.Errorf("approval buttons row changed while scrolling args: got %d, want %d", after, before)
	}
	for i := range beforeHits {
		if beforeHits[i].rect != afterHits[i].rect {
			t.Errorf("approval hit region %d changed while scrolling: got %#v, want %#v", i, afterHits[i].rect, beforeHits[i].rect)
		}
	}
}
