package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func testAgentsDetailScrolling(t *testing.T) {
	trace := make([]teamTrace, 30)
	tasks := make([]teamTask, 30)
	findings := make([]teamFinding, 30)
	for i := range trace {
		marker := fmt.Sprintf("detail-%02d", i)
		trace[i] = teamTrace{kind: teamTraceMessage, text: marker}
		tasks[i] = teamTask{id: marker, state: taskStatePending}
		findings[i] = teamFinding{member: "lead", body: marker}
	}

	cases := []struct {
		name     string
		setup    func(Model) Model
		resetKey rune
	}{
		{"subagent focus trace", func(m Model) Model {
			m.conv.subagentFleet = []subagentLane{{childID: "child", goal: "scroll", trace: trace}}
			m.team, m.agentsTab = teamState{view: teamRoster}, tabSubagents
			m.subagents = subagentState{view: subagentFocus, child: "child"}
			return m
		}, 0},
		{"team member focus trace", func(m Model) Model {
			m.conv.blocks = append(m.conv.blocks, block{kind: blockTool, team: true, teamLanes: []teamLane{{name: "lead", trace: trace}}})
			m.team, m.agentsTab = teamState{view: teamFocus, member: "lead"}, tabTeams
			return m
		}, 0},
		{"team tasks", func(m Model) Model {
			m.conv.blocks = append(m.conv.blocks, block{kind: blockTool, team: true, teamLanes: []teamLane{{name: "lead"}}, teamTasks: tasks})
			m.team, m.agentsTab = teamState{view: teamTasks}, tabTeams
			return m
		}, 't'},
		{"team findings", func(m Model) Model {
			m.conv.blocks = append(m.conv.blocks, block{kind: blockTool, team: true, teamLanes: []teamLane{{name: "lead"}}, teamFindings: findings})
			m.team, m.agentsTab = teamState{view: teamFindings}, tabTeams
			return m
		}, 'f'},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := resize(newMCPModel(t, aztec(), nil), 80, 40)
			m.prompt.Rewrite("draft")
			m.keys = applyKeyOverrides(m.keys, map[string][]string{
				"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n"},
				"JumpTop": {"h"}, "JumpEnd": {"e"},
			})
			m = tc.setup(m)

			m = pressDetail(m, 'e')
			activeOffset := agentsTestOffset(m.team.detail)
			if m.agentsTab == tabSubagents {
				activeOffset = agentsTestOffset(m.subagents.detail)
			}
			if activeOffset == 0 {
				t.Fatal("JumpEnd did not update boundedViewport source offset")
			}
			end := stripANSIstr(m.View().Content)
			if !strings.Contains(end, "detail-29") || strings.Contains(end, "detail-00") {
				t.Fatalf("JumpEnd did not reveal only the final hidden content:\n%s", end)
			}
			if !strings.Contains(end, "u/d") || !strings.Contains(end, "p/n") || !strings.Contains(end, "h/e") || !strings.Contains(end, "of 30") {
				t.Fatalf("footer lacks live markings or accurate total:\n%s", end)
			}
			if got := m.prompt.Value(); got != "draft" {
				t.Fatalf("overlay key leaked into prompt: %q", got)
			}

			m = pressDetail(m, 'u')
			up := stripANSIstr(m.View().Content)
			if up == end {
				t.Fatalf("Up did not scroll one rendered line (sub=%d team=%d):\n%s", agentsTestOffset(m.subagents.detail), agentsTestOffset(m.team.detail), up)
			}
			m = pressDetail(m, 'p')
			pageUp := stripANSIstr(m.View().Content)
			if pageUp == up {
				t.Fatal("ScrollU did not move by the physical-line window")
			}
			m = pressDetail(m, 'h')
			if top := stripANSIstr(m.View().Content); !strings.Contains(top, "detail-00") {
				t.Fatalf("JumpTop did not reveal first content:\n%s", top)
			}
			m = pressDetail(m, 'd')
			if down := stripANSIstr(m.View().Content); strings.Contains(down, "lines 1–") {
				t.Fatalf("Down did not advance exactly one rendered-line offset:\n%s", down)
			}
			beforePageDown := stripANSIstr(m.View().Content)
			m = pressDetail(m, 'n')
			beforeResize := stripANSIstr(m.View().Content)
			if beforeResize == beforePageDown {
				t.Fatal("ScrollD did not move by the physical-line window")
			}
			m = resize(m, 80, 80)
			afterResize := stripANSIstr(m.View().Content)
			if !strings.Contains(afterResize, "detail-29") || beforeResize == afterResize {
				t.Fatalf("resize did not clamp/recompute the live rendered-line window:\n%s", afterResize)
			}
			_, resizedWindow := m.agentsDetailMetrics()
			wantMax := maxScrollOffset(30, resizedWindow)
			gotOffset := agentsTestOffset(m.team.detail)
			if m.agentsTab == tabSubagents {
				gotOffset = agentsTestOffset(m.subagents.detail)
			}
			if gotOffset != wantMax {
				t.Fatalf("resize left offset %d, want clamped %d", gotOffset, wantMax)
			}

			m = pressDetail(m, 'e')
			if tc.resetKey != 0 {
				m = pressDetail(m, tc.resetKey)
				m = pressDetail(m, tc.resetKey)
			} else {
				m = pressDetailKey(m, tea.KeyEscape)
				m = pressDetailKey(m, tea.KeyEnter)
			}
			reset := stripANSIstr(m.View().Content)
			if !strings.Contains(reset, "detail-00") {
				t.Fatalf("focus/subview identity change did not reset offset:\n%s", reset)
			}
			if got := m.prompt.Value(); got != "draft" {
				t.Fatalf("rebound overlay actions changed prompt: %q", got)
			}
		})
	}
}

func pressDetail(m Model, r rune) Model {
	mm, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	return mm.(Model)
}

func pressDetailKey(m Model, code rune) Model {
	mm, _ := m.Update(tea.KeyPressMsg{Code: code})
	return mm.(Model)
}
