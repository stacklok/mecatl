package ui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// agentPickState is the bare /agent picker: a filterable list of the server's
// agent definitions; Enter starts a session bound to the highlighted one. It is
// a modal surface modeled on the /models picker, without click regions.
type agentPickState struct {
	deps     surfaceDeps
	loading  bool
	err      error
	agents   []client.Agent
	filtered []client.Agent
	filter   textinput.Model
	list     *bounded.List
	intent   surfaceIntent
}

// agentPickResultMsg binds a ListAgents result to the picker instance that
// requested it; AgentsMsg itself carries no request identity.
type agentPickResultMsg struct {
	owner  *agentPickState
	result client.AgentsMsg
}

type agentPickSelectIntent struct{ name string }

func (agentPickSelectIntent) isSurfaceIntent() {}

// openAgentPicker opens the picker and starts the definition listing.
func (m Model) openAgentPicker() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Agents == nil {
		return m, nil
	}
	m.prompt.Blur()
	ti := textinput.New()
	ti.Placeholder = "filter agents…"
	ti.SetWidth(40)
	ti.Focus()
	st := &agentPickState{loading: true, filter: ti, list: new(bounded.List), deps: (&m).surfaceDeps()}
	m.modal = st
	list := client.ListAgentsCmd(m.deps.Ctx, m.deps.Agents)
	return m, tea.Batch(func() tea.Msg {
		return agentPickResultMsg{owner: st, result: list().(client.AgentsMsg)}
	}, textinput.Blink)
}

// applyAgentPickIntent starts the agent-bound session for a picked definition.
func (m Model) applyAgentPickIntent(intent surfaceIntent) (tea.Model, tea.Cmd, bool) {
	pick, ok := intent.(agentPickSelectIntent)
	if !ok {
		return m, nil, false
	}
	mm, cmd, _ := m.startAgentSession(pick.name)
	return mm, cmd, true
}

func (s *agentPickState) Render(width, height int) (string, []ClickableRegion) {
	th := s.deps.theme
	prefix := []string{th.Style("askTitle").Render("Agent definitions"), s.filter.View(), ""}
	switch {
	case s.loading:
		prefix = append(prefix, th.Style("muted").Render("loading…"))
	case s.err != nil:
		prefix = append(prefix, th.Style("errorText").Render("✗ list agents: "+terminaltext.Sanitize(s.err.Error())))
	case len(s.agents) == 0:
		prefix = append(prefix, th.Style("muted").Render(agentsInvEmptyCopy(s.deps.caps)))
	case len(s.filtered) == 0:
		prefix = append(prefix, th.Style("muted").Render("no agents match "+strconv.Quote(s.filter.Value())+" — "+s.deps.marks.closeOnly+" to clear"))
	}
	suffix := []string{"", th.Style("muted").Render("type to filter · ↑/↓ move · " + s.deps.marks.choose + " start session · " + s.deps.marks.closeOnly + " clear filter / close")}

	budget := max(0, height-len(prefix)-len(suffix))
	list := s.listControl()
	list.SetGeometry(width, budget, 3, bounded.Wrap)
	list.SetItems(agentPickItems(s.filtered))
	if list.CursorID() == "" {
		list.SetCursor(0)
	}
	view := list.ViewWithIndicators(budget, list.RevealPending())

	lines := append([]string(nil), prefix...)
	for _, row := range view.Rows {
		presentation := presentListRow(row, th.Style("spinner"), th.Style("muted"))
		lines = append(lines, presentation.Style.Render(presentation.Text))
	}
	lines = append(lines, suffix...)
	return strings.Join(lines, "\n"), nil
}

func (s *agentPickState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
			return nil, true, false
		}
		return nil, true, true
	case msg.String() == keyMenuUp:
		s.move(bounded.LineUp)
	case msg.String() == keyMenuDown:
		s.move(bounded.LineDown)
	case key.Matches(msg, s.deps.keys.ScrollU):
		s.move(bounded.PageUp)
	case key.Matches(msg, s.deps.keys.ScrollD):
		s.move(bounded.PageDown)
	case key.Matches(msg, s.deps.keys.ScrollTop):
		s.move(bounded.Top)
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		s.move(bounded.End)
	case key.Matches(msg, s.deps.keys.Choose):
		if chosen, ok := s.chosen(); ok {
			s.intent = agentPickSelectIntent{name: chosen.Name}
			return nil, true, true
		}
		return nil, true, false
	default:
		var cmd tea.Cmd
		s.filter, cmd = s.filter.Update(msg)
		s.syncFilter()
		return cmd, true, false
	}
	return nil, true, false
}

func (s *agentPickState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	result, ok := msg.(agentPickResultMsg)
	if !ok {
		return nil, false, false
	}
	if result.owner != s {
		return nil, true, false
	}
	s.loading, s.err = false, result.result.Err
	if s.err == nil {
		s.agents = result.result.Agents
		s.syncFilter()
	}
	return nil, true, false
}

func (s *agentPickState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if msg.Mouse().Button == tea.MouseWheelUp {
		s.move(bounded.LineUp)
	} else {
		s.move(bounded.LineDown)
	}
	return nil, true
}

func (*agentPickState) Close() {}

func (s *agentPickState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (s *agentPickState) listControl() *bounded.List {
	if s.list == nil {
		s.list = new(bounded.List)
	}
	return s.list
}

func (s *agentPickState) move(move bounded.Move) {
	if list := s.listControl(); list.Valid() {
		list.Move(move)
	}
}

func (s *agentPickState) syncFilter() {
	s.filtered = filterAgentPick(s.agents, s.filter.Value())
	list := s.listControl()
	list.SetItems(agentPickItems(s.filtered))
	if len(s.filtered) > 0 && list.CursorID() == "" {
		list.SetCursor(0)
	}
}

func (s *agentPickState) chosen() (client.Agent, bool) {
	id := s.listControl().CursorID()
	for _, a := range s.filtered {
		if a.Name == id {
			return a, true
		}
	}
	return client.Agent{}, false
}

func filterAgentPick(agents []client.Agent, q string) []client.Agent {
	if q == "" {
		return agents
	}
	lq := strings.ToLower(q)
	out := make([]client.Agent, 0, len(agents))
	for _, a := range agents {
		if strings.Contains(strings.ToLower(a.Name), lq) {
			out = append(out, a)
		}
	}
	return out
}

func agentPickItems(agents []client.Agent) []bounded.ListItem {
	items := make([]bounded.ListItem, 0, len(agents))
	for _, a := range agents {
		items = append(items, bounded.ListItem{ID: a.Name, Text: terminaltext.SanitizeSingleLine(a.Name)})
	}
	return items
}
