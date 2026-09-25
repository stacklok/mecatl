package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

func TestMecatuiAgentInventoryBoundedViewport_Scenario2_CurrentOpenOwnsResult(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})

	opened, first := m.openAgentsInv()
	m = opened.(Model)
	closed, _ := m.closeAgentsInv()
	m = closed.(Model)
	opened, second := m.openAgentsInv()
	m = opened.(Model)

	stale := first()
	updated, _ := m.updateAgentsInvMsg(stale)
	m = updated.(Model)
	if !m.agentsInv.loading || len(m.agentsInv.agents) != 0 || m.agentsInv.err != nil || m.agentsInv.viewport != nil {
		t.Fatalf("stale result changed reopened state: %#v", m.agentsInv)
	}
	updated, _ = m.updateAgentsInvMsg(agentsInvResultMsg{
		generation: m.agentsInv.generation - 1,
		result:     client.AgentsMsg{Err: errors.New("stale")},
	})
	m = updated.(Model)
	if !m.agentsInv.loading || m.agentsInv.err != nil || m.agentsInv.viewport != nil {
		t.Fatalf("stale error changed reopened state: %#v", m.agentsInv)
	}

	updated, handled := m.updateAgentsInvMsg(second())
	if !handled {
		t.Fatal("current result was not handled")
	}
	m = updated.(Model)
	if m.agentsInv.loading || len(m.agentsInv.agents) != 2 || m.agentsInv.err != nil || m.agentsInv.viewport == nil {
		t.Fatalf("current result did not replace the snapshot: %#v", m.agentsInv)
	}
	m.agentsInv.viewport = agentsTestViewport(7)
	if m.agentsInv.viewport.Offset() != 7 {
		t.Fatal("precondition: viewport did not move away from the top")
	}
	opened, third := m.openAgentsInv()
	m = opened.(Model)
	updated, _ = m.updateAgentsInvMsg(third())
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("current successful result offset = %d, want 0", got)
	}

	closed, _ = m.closeAgentsInv()
	m = closed.(Model)
	updated, _ = m.updateAgentsInvMsg(third())
	m = updated.(Model)
	if m.agentsInv.view != agentsInvNone || m.agentsInv.loading || len(m.agentsInv.agents) != 0 || m.agentsInv.err != nil || m.agentsInv.viewport != nil {
		t.Fatalf("closed result changed state: %#v", m.agentsInv)
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario3_OpenCloseLifecycle(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	opened, cmd := m.openAgentsInv()
	m = opened.(Model)
	if m.agentsInv.view != agentsInvPanel || !m.agentsInv.loading || m.prompt.Focused() || cmd == nil {
		t.Fatalf("open state = %#v, focused=%v, cmd=%v", m.agentsInv, m.prompt.Focused(), cmd != nil)
	}
	m = feedCmd(t, m, cmd)
	if got := m.agentsInv.agents; len(got) != 2 {
		t.Fatalf("ListAgents result = %#v, want one request's two definitions", got)
	}
	closed, _, _ := m.onAgentsInvKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = closed.(Model)
	if m.agentsInv.view != agentsInvNone || !m.prompt.Focused() {
		t.Fatalf("close state = %#v, focused=%v", m.agentsInv, m.prompt.Focused())
	}

	m.phase = phaseRunning
	blocked, blockedCmd := m.openAgentsInv()
	if blocked.(Model).agentsInv.view != agentsInvNone || blockedCmd != nil {
		t.Fatal("running phase opened the inventory")
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario3_RemappablePhysicalLineNavigation(t *testing.T) {
	m := newAgentsInvModel(t, scrollAgents(40), client.Capabilities{Agents: true})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{
		"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n"}, "ScrollTop": {"t"}, "ScrollBottom": {"b"},
	})
	opened, cmd := m.openAgentsInv()
	m = feedCmd(t, opened.(Model), cmd)
	m.configureAgentsInvViewport()
	rows := agentsInvRowLines(m.deps.Theme, m.agentsInv.agents, newAgentsInvLayout(m.deps.Theme, m.helpKeyMarkings(), m.width, m.vp.Height()).bodyWidth)

	for _, key := range []rune{'d', 'n'} {
		updated, _, handled := m.onAgentsInvKey(tea.KeyPressMsg{Code: key})
		if !handled {
			t.Fatalf("remapped %q was not handled", key)
		}
		m = updated.(Model)
	}
	if got := m.agentsInv.viewport.Offset(); got != 2 {
		t.Fatalf("remapped Down/ScrollD offset = %d, want 2 physical lines", got)
	}
	for _, key := range []rune{'u', 'p'} {
		updated, _, _ := m.onAgentsInvKey(tea.KeyPressMsg{Code: key})
		m = updated.(Model)
	}
	if got := m.agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("remapped Up/ScrollU offset = %d, want top", got)
	}
	updated, _, _ := m.onAgentsInvKey(tea.KeyPressMsg{Code: 'b'})
	m = updated.(Model)
	wantEnd := len(rows) - m.agentsInv.viewport.Height()
	if got := m.agentsInv.viewport.Offset(); got != wantEnd {
		t.Fatalf("remapped ScrollBottom offset = %d, want %d", got, wantEnd)
	}
	updated, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: 't'})
	if got := updated.(Model).agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("remapped ScrollTop offset = %d, want 0", got)
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario3_ReadOnlyInputOwnership(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	opened, cmd := m.openAgentsInv()
	m = feedCmd(t, opened.(Model), cmd)
	for _, msg := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: 'x'}, {Code: tea.KeyTab}} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	beforeOffset := m.vp.YOffset()
	for _, msg := range []tea.Msg{
		tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1},
		tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: 1},
		tea.MouseMotionMsg{Button: tea.MouseLeft, X: 2, Y: 2},
		tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 2, Y: 2},
	} {
		updated, _ := m.Update(msg)
		m = updated.(Model)
	}
	if got := m.vp.YOffset(); got != beforeOffset {
		t.Fatalf("overlay input changed hidden conversation offset from %d to %d", beforeOffset, got)
	}
	if m.agentsInv.view != agentsInvPanel || m.team.view != teamNone || m.modal != nil || m.sel.active || m.prompt.Value() != "" || m.prompt.Focused() {
		t.Fatalf("read-only input changed ownership: inventory=%v team=%v modal=%T selection=%v prompt=%q focused=%v", m.agentsInv.view, m.team.view, m.modal, m.sel.active, m.prompt.Value(), m.prompt.Focused())
	}
	if strings.Contains(stripANSIstr(m.View().Content), "Subagents") {
		t.Fatal("read-only definition inventory became the /team overlay")
	}
}
