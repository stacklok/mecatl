package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func TestMecatuiAgentInventoryBoundedViewport_Scenario2_CurrentOpenOwnsResult(t *testing.T) {
	m := newAgentsInvModel(t, sampleAgents(), client.Capabilities{Agents: true})

	opened, staleOpen := m.openAgentsInv()
	m = opened.(Model)
	closed, _ := m.closeAgentsInv()
	m = closed.(Model)
	opened, current := m.openAgentsInv()
	m = opened.(Model)
	m.agentsInv.viewport = agentsTestViewport(7)
	updated, _ := m.Update(current())
	m = updated.(Model)
	if m.agentsInv.loading || len(m.agentsInv.agents) != 2 || m.agentsInv.err != nil || m.agentsInv.viewport == nil {
		t.Fatalf("current result did not replace the snapshot: %#v", m.agentsInv)
	}
	if got := m.agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("current successful result offset = %d, want 0", got)
	}

	m.agentsInv.viewport = agentsTestViewport(7)
	wantAgents := append([]client.Agent(nil), m.agentsInv.agents...)
	wantOffset := m.agentsInv.viewport.Offset()
	for _, stale := range []tea.Msg{
		staleOpen(),
		client.AgentsMsg{Err: errors.New("stale-bare")},
	} {
		updated, _ = m.Update(stale)
		m = updated.(Model)
		if m.agentsInv.loading || m.agentsInv.err != nil || !reflect.DeepEqual(m.agentsInv.agents, wantAgents) || m.agentsInv.viewport == nil || m.agentsInv.viewport.Offset() != wantOffset {
			t.Fatalf("stale result %T changed current state: %#v", stale, m.agentsInv)
		}
	}

	closed, _ = m.closeAgentsInv()
	m = closed.(Model)
	updated, _ = m.Update(agentsInvResultMsg{
		generation: m.agentsInv.generation,
		result:     client.AgentsMsg{Agents: []client.Agent{{Name: "closed-stale"}}},
	})
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

func TestMecatuiAgentInventoryBoundedViewport_Scenario3_RemappablePhysicalLineAndPagingNavigation(t *testing.T) {
	m := newAgentsInvModel(t, scrollAgents(40), client.Capabilities{Agents: true})
	m.keys = applyKeyOverrides(m.keys, map[string][]string{
		"Up": {"u"}, "Down": {"d"}, "ScrollU": {"p"}, "ScrollD": {"n"}, "ScrollTop": {"t"}, "ScrollBottom": {"b"},
	})
	opened, cmd := m.openAgentsInv()
	m = feedCmd(t, opened.(Model), cmd)
	m.configureAgentsInvViewport()
	rows := agentsInvRowLines(m.deps.Theme, m.agentsInv.agents, newAgentsInvLayout(m.deps.Theme, m.helpKeyMarkings(), m.width, m.vp.Height()).bodyWidth)
	height := m.agentsInv.viewport.Height()

	updated, _, handled := m.onAgentsInvKey(tea.KeyPressMsg{Code: 'd'})
	if !handled {
		t.Fatal("remapped Down was not handled")
	}
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != 1 {
		t.Fatalf("remapped Down offset = %d, want one physical line", got)
	}

	updated, _, handled = m.onAgentsInvKey(tea.KeyPressMsg{Code: 'n'})
	if !handled {
		t.Fatal("remapped ScrollD was not handled")
	}
	m = updated.(Model)
	if got, want := m.agentsInv.viewport.Offset(), 1+height; got != want {
		t.Fatalf("remapped ScrollD offset = %d, want one physical page after Down (%d)", got, want)
	}

	updated, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: 'p'})
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != 1 {
		t.Fatalf("remapped ScrollU offset = %d, want one physical page up", got)
	}
	updated, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: 'u'})
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("remapped Up offset = %d, want top", got)
	}

	updated, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: 'b'})
	m = updated.(Model)
	wantEnd := len(rows) - height
	if got := m.agentsInv.viewport.Offset(); got != wantEnd {
		t.Fatalf("remapped ScrollBottom offset = %d, want %d", got, wantEnd)
	}
	updated, _, _ = m.onAgentsInvKey(tea.KeyPressMsg{Code: 't'})
	if got := updated.(Model).agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("remapped ScrollTop offset = %d, want 0", got)
	}
}

func TestMecatuiAgentInventoryBoundedViewport_Scenario3_WheelOwnershipAndCompactIsolation(t *testing.T) {
	m := newAgentsInvModel(t, scrollAgents(40), client.Capabilities{Agents: true})
	m.vp.SetContent(strings.Repeat("conversation\n", 100))
	opened, cmd := m.openAgentsInv()
	m = feedCmd(t, opened.(Model), cmd)
	m.configureAgentsInvViewport()
	beforeConversation, beforeSelection := m.vp.YOffset(), m.sel

	updated, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 1, Y: 1})
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != 0 {
		t.Fatalf("wheel at top offset = %d, want consumed endpoint 0", got)
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("wheel at top leaked to conversation: got %d, want %d", got, beforeConversation)
	}

	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != 1 {
		t.Fatalf("wheel down offset = %d, want one physical line", got)
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("wheel moved hidden conversation from %d to %d", beforeConversation, got)
	}
	if !reflect.DeepEqual(m.sel, beforeSelection) {
		t.Fatalf("wheel changed selection from %#v to %#v", beforeSelection, m.sel)
	}

	layout := newAgentsInvLayout(m.deps.Theme, m.helpKeyMarkings(), m.width, m.vp.Height())
	rows := agentsInvRowLines(m.deps.Theme, m.agentsInv.agents, layout.bodyWidth)
	m.agentsInv.viewport.Move(bounded.End, len(rows))
	atEnd := m.agentsInv.viewport.Offset()
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	if got := m.agentsInv.viewport.Offset(); got != atEnd {
		t.Fatalf("wheel at end offset = %d, want consumed endpoint %d", got, atEnd)
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("wheel at endpoint leaked to conversation: got %d, want %d", got, beforeConversation)
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 10, Height: 3})
	m = updated.(Model)
	_ = m.View()
	if m.agentsInv.viewport != nil {
		t.Fatalf("compact fallback retained viewport: %#v", m.agentsInv.viewport)
	}
	updated, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 1, Y: 1})
	m = updated.(Model)
	if m.agentsInv.viewport != nil {
		t.Fatal("compact fallback wheel constructed a viewport")
	}
	if got := m.vp.YOffset(); got != beforeConversation {
		t.Fatalf("compact wheel leaked to conversation: got %d, want %d", got, beforeConversation)
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
