package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// skillsView is the active skills overlay (none = closed). Like the MCP panel it
// is a read-only inventory layered over the conversation: it does not change the
// run phase, opens only while idle, and is dismissed with esc. Unlike MCP there
// is no resource/prompt picker — the inventory is the whole surface, since skill
// activation is a run-path concern the model drives, not a TUI action.
type skillsView int

const (
	// skillsNone is the zero value of skillsView and represents "overlay closed".
	// It is NOT read by production routing code (the closed state is
	// m.modal == nil — the surface is created with view: skillsPanel and torn
	// down by nil-ing m.modal). It is kept because the iota zero value is
	// load-bearing for Render's/HandleKey's defense-in-depth: a zero/uninitialized
	// skillsState renders empty and swallows nothing rather than posing as an open
	// panel (the same discipline soulNone's doc comment records).
	skillsNone   skillsView = iota // overlay closed
	skillsPanel                    // inventory (external + learned lifecycle)
	skillsDetail                   // learned-skill bounded detail and actions
)

type skillsFocus uint8

const (
	skillsFocusExternal skillsFocus = iota
	skillsFocusLearned
)

// skillsState is the skills overlay's state: it is constructed at Open
// (runSkills) and lives ONLY inside the Model's one `modal surface` interface
// field — never a pre-declared Model field. The skills slice is replaced
// wholesale on each RPC result (never mutated in place). skillsState implements
// `surface` on POINTER receivers.
type skillsState struct {
	view     skillsView
	loading  bool  // the ListSkills RPC is in flight
	err      error // the ListSkills error, rendered distinctly (nil on success)
	skills   []client.Skill
	filtered []client.Skill // subset matching filter.Value(); recomputed on each key (mirror models.filtered)
	filter   textinput.Model
	viewport *bounded.Viewport // pointer-owned external physical-row viewport
	focus    skillsFocus
	// view cache: refreshed from the offered geometry at the top of Render.
	normal, compact                   bool
	bodyWidth, bodyRows, externalRows int
	learnedRows, learnedStart         int
	scroll, width                     int // compatibility mirrors for focused legacy tests
	learned                           []client.LearnedSkill
	cursor                            int
	detail                            *client.LearnedSkill
	diff                              string
	project                           string
	generations                       map[string]uint64 // independent publication/catalog epoch per global/project partition
	requestID                         uint64

	deps             surfaceDeps               // the shared ambient base (incl. ctx), set once at Open
	learnedLifecycle client.LearnedSkillClient // nil unless the Skills collaborator carries the lifecycle half
	nextEpoch        func() uint64             // bumps the Model-owned skillsEpoch; correlation survives close/reopen
}

// runSkills is the Open transition for the skills surface. It validates (idle +
// lister wired), blurs the textarea, bumps the Model-owned request epoch
// (correlation must survive close/reopen), constructs the state dynamically
// constructs the state dynamically, installs it on m.modal, and batches the
// ListSkills + (when wired) ListLearnedSkills RPCs. Render focuses the filter
// only after normal geometry has been measured.
func (m Model) runSkills() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Skills == nil {
		return m, nil
	}
	m.prompt.Blur() // modal owns the keyboard while open
	m.skillsEpoch++
	lifecycle, _ := m.deps.Skills.(client.LearnedSkillClient) // nil unless Skills carries the lifecycle half
	ti := textinput.New()
	ti.Placeholder = "filter skills…"
	ti.SetWidth(40)
	m.modal = &skillsState{
		view:             skillsPanel,
		loading:          true,
		project:          m.deps.Workspace,
		requestID:        m.skillsEpoch,
		filter:           ti,
		filtered:         nil,
		deps:             (&m).surfaceDeps(),
		learnedLifecycle: lifecycle,
		nextEpoch:        func() uint64 { m.skillsEpoch++; return m.skillsEpoch },
	}
	requestID := m.skillsEpoch
	cmds := []tea.Cmd{func() tea.Msg {
		return skillsResultMsg{requestID: requestID, result: client.ListSkillsCmd(m.deps.Ctx, m.deps.Skills)().(client.SkillsMsg)}
	}, textinput.Blink}
	if lifecycle != nil {
		cmds = append(cmds, client.ListLearnedSkillsCmd(m.deps.Ctx, lifecycle, m.deps.Workspace, m.skillsEpoch))
	}
	return m, tea.Batch(cmds...)
}

type skillsResultMsg struct {
	requestID uint64
	result    client.SkillsMsg
}

func (*skillsState) modalPlacement() modalPlacement { return modalPlacementFill }

// Render measures and renders the complete overlay from one offered geometry.
func (s *skillsState) Render(width, height int) (string, []ClickableRegion) {
	s.width = width
	s.normal, s.compact = false, false
	s.bodyWidth, s.bodyRows, s.externalRows, s.learnedRows, s.learnedStart = 0, 0, 0, 0, 0
	if s.view == skillsNone || width <= 0 || height <= 0 {
		s.viewport = nil
		s.filter.Blur()
		return "", nil
	}
	if s.filter.Placeholder == "" {
		value := s.filter.Value()
		s.filter = textinput.New()
		s.filter.Placeholder = "filter skills…"
		s.filter.SetValue(value)
	}
	layout := newSkillsLayout(s.deps.theme, s.deps.marks, width, height, len(s.learned) > 0)
	if !layout.normal {
		s.compact = true
		s.viewport = nil
		s.filter.Blur()
		s.cursor, s.detail, s.diff = 0, nil, ""
		s.view = skillsPanel
		return renderSkillsCompact(s.deps.theme, s.deps.marks, width), nil
	}
	s.normal = true
	s.width = layout.bodyWidth
	s.bodyWidth, s.bodyRows = layout.bodyWidth, layout.bodyRows
	s.externalRows, s.learnedRows = layout.regionRows()
	if s.focus == skillsFocusLearned && len(s.learned) == 0 {
		s.focus = skillsFocusExternal
	}
	if s.focus == skillsFocusExternal {
		s.filter.Focus()
	} else {
		s.filter.Blur()
	}
	if s.view == skillsDetail && s.detail != nil {
		body := renderLearnedSkillDetail(s.deps.theme, *s.detail, s.diff, layout.bodyWidth)
		if s.err != nil {
			body += "\n" + s.deps.theme.Style("errorText").Render(terminaltext.Sanitize(s.err.Error())) + "\npress esc, then enter to refresh"
		}
		body = strings.Join(firstLines(body, max(1, height-layout.frameHeight)), "\n")
		return centerSkillsCard(s.deps.theme, body, layout.outerWidth, width, height), nil
	}
	body := renderSkillsNormalBody(s, layout)
	return centerSkillsCard(s.deps.theme, body, layout.outerWidth, width, height), nil
}

const skillsMaxOuterWidth = 128

const skillsFooter = "the agent uses skills when relevant · tab changes region · %s scroll · %s clear filter / close"

type skillsLayout struct {
	outerWidth, bodyWidth, frameHeight int
	title, footer                      []string
	bodyRows                           int
	hasLearned, normal                 bool
}

func newSkillsLayout(th theme.Theme, hk helpKeys, width, height int, hasLearned bool) skillsLayout {
	var l skillsLayout
	if width <= 0 || height <= 0 {
		return l
	}
	card := th.Style("askCard")
	l.outerWidth = min(skillsMaxOuterWidth, width)
	l.bodyWidth = l.outerWidth - card.GetHorizontalFrameSize()
	l.frameHeight = card.GetVerticalFrameSize()
	l.hasLearned = hasLearned
	if l.bodyWidth <= 0 {
		return l
	}
	l.title = skillsTextLines(th.Style("askTitle"), "Skills inventory", l.bodyWidth)
	l.footer = skillsTextLines(th.Style("muted"), fmt.Sprintf(skillsFooter, hk.scroll, hk.closeOnly), l.bodyWidth)
	fixed := l.frameHeight + len(l.title) + 1 + len(l.footer) // title, filter, footer
	if hasLearned {
		fixed++ // learned-region label
	}
	l.bodyRows = height - fixed
	required := 1
	if hasLearned {
		required = 2
	}
	l.normal = l.bodyRows >= required
	return l
}

func (l skillsLayout) regionRows() (external, learned int) {
	if !l.normal {
		return 0, 0
	}
	if !l.hasLearned {
		return l.bodyRows, 0
	}
	return (l.bodyRows + 1) / 2, l.bodyRows / 2
}

func skillsTextLines(style lipgloss.Style, text string, width int) []string {
	if width <= 0 {
		return nil
	}
	parts := strings.Split(ansi.Hardwrap(text, width, true), "\n")
	for i := range parts {
		parts[i] = style.Render(parts[i])
	}
	return parts
}

func firstLines(text string, n int) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return lines
}

func centerSkillsCard(th theme.Theme, body string, outerWidth, width, height int) string {
	card := th.Style("askCard").Width(outerWidth).Render(body)
	rows := strings.Split(card, "\n")
	for i, row := range rows {
		rows[i] = strings.Repeat(" ", max(0, (width-lipgloss.Width(row))/2)) + row
	}
	card = strings.Join(rows, "\n")
	remaining := max(0, height-lipgloss.Height(card))
	return strings.Repeat("\n", remaining/2) + card + strings.Repeat("\n", remaining-remaining/2)
}

func renderSkillsCompact(th theme.Theme, hk helpKeys, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Cut(th.Style("muted").Render(hk.closeOnly+" close"), 0, width) + "\x1b[0m"
}

// HandleKey routes all normal-panel navigation to the focused region.
//
//nolint:gocyclo // lifecycle detail and the two focused regions share one router
func (s *skillsState) HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	if s.view == skillsNone {
		return nil, false, false
	}
	if s.compact {
		if key.Matches(msg, s.deps.keys.Close) {
			return nil, true, true
		}
		return nil, true, false
	}
	if s.view == skillsDetail {
		if key.Matches(msg, s.deps.keys.Close) {
			s.view, s.detail = skillsPanel, nil
			return nil, true, false
		}
		if s.learnedLifecycle == nil || s.detail == nil {
			return nil, true, false
		}
		action := ""
		switch msg.String() {
		case "a":
			action = "activate"
		case "x":
			action = "reject"
		case "d":
			action = "archive"
		case "v":
			if s.detail.Supersedes != "" {
				s.requestID = s.nextEpoch()
				return client.DiffLearnedSkillCmd(s.deps.ctx, s.learnedLifecycle, *s.detail, s.requestID), true, false
			}
		case "r":
			if s.detail.Supersedes != "" {
				s.requestID = s.nextEpoch()
				return client.RollbackLearnedSkillCmd(s.deps.ctx, s.learnedLifecycle, *s.detail, s.requestID), true, false
			}
		}
		if action != "" {
			s.requestID = s.nextEpoch()
			return client.MutateLearnedSkillCmd(s.deps.ctx, s.learnedLifecycle, action, *s.detail, s.requestID), true, false
		}
		return nil, true, false
	}
	if msg.String() == "tab" && len(s.learned) > 0 {
		if s.focus == skillsFocusExternal {
			s.focus = skillsFocusLearned
			s.filter.Blur()
		} else {
			s.focus = skillsFocusExternal
			s.filter.Focus()
		}
		return nil, true, false
	}
	if key.Matches(msg, s.deps.keys.Close) {
		if s.focus == skillsFocusExternal && s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
			return nil, true, false
		}
		return nil, true, true
	}
	if s.focus == skillsFocusLearned {
		if msg.String() == "enter" && len(s.learned) > 0 {
			if s.learnedLifecycle == nil {
				return nil, true, false
			}
			selected := s.learned[min(s.cursor, len(s.learned)-1)]
			s.requestID = s.nextEpoch()
			return client.GetLearnedSkillCmd(s.deps.ctx, s.learnedLifecycle, selected, s.requestID), true, false
		}
		s.moveLearned(msg)
		return nil, true, false
	}
	rows := s.externalPhysicalRows()
	if s.viewport != nil && msg.Text != "j" && msg.Text != "k" {
		move, ok := s.navigationMove(msg)
		if ok {
			s.viewport.Move(move, len(rows))
			s.scroll = s.viewport.Offset()
			return nil, true, false
		}
	}
	s.filter, cmd = s.filter.Update(msg)
	s.syncFilter()
	return cmd, true, false
}

func (s *skillsState) navigationMove(msg tea.KeyPressMsg) (bounded.Move, bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Down):
		return bounded.LineDown, true
	case key.Matches(msg, s.deps.keys.Up):
		return bounded.LineUp, true
	case key.Matches(msg, s.deps.keys.ScrollD):
		return bounded.PageDown, true
	case key.Matches(msg, s.deps.keys.ScrollU):
		return bounded.PageUp, true
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		return bounded.End, true
	case key.Matches(msg, s.deps.keys.ScrollTop):
		return bounded.Top, true
	default:
		return 0, false
	}
}

func (s *skillsState) moveLearned(msg tea.KeyPressMsg) {
	move, ok := s.navigationMove(msg)
	if !ok || len(s.learned) == 0 {
		return
	}
	page := max(1, s.learnedRows)
	switch move {
	case bounded.LineDown:
		s.cursor++
	case bounded.LineUp:
		s.cursor--
	case bounded.PageDown:
		s.cursor += page
	case bounded.PageUp:
		s.cursor -= page
	case bounded.Top:
		s.cursor = 0
	case bounded.End:
		s.cursor = len(s.learned) - 1
	}
	s.cursor = clampBounded(s.cursor, len(s.learned))
	s.clampLearnedWindow()
}

func (s *skillsState) clampLearnedWindow() {
	if s.learnedRows <= 0 || len(s.learned) == 0 {
		s.learnedStart = 0
		return
	}
	if s.cursor < s.learnedStart {
		s.learnedStart = s.cursor
	}
	if s.cursor >= s.learnedStart+s.learnedRows {
		s.learnedStart = s.cursor - s.learnedRows + 1
	}
	s.learnedStart = clampScroll(s.learnedStart, len(s.learned), s.learnedRows)
}

func (s *skillsState) HandleWheel(msg tea.MouseWheelMsg) (cmd tea.Cmd, handled bool) {
	if s.view == skillsNone {
		return nil, false
	}
	if s.compact {
		return nil, true
	}
	down := msg.Mouse().Button != tea.MouseWheelUp
	if s.focus == skillsFocusLearned {
		if down {
			s.cursor++
		} else {
			s.cursor--
		}
		s.cursor = clampBounded(s.cursor, len(s.learned))
		s.clampLearnedWindow()
		return nil, true
	}
	if s.viewport != nil {
		move := bounded.LineDown
		if !down {
			move = bounded.LineUp
		}
		s.viewport.Move(move, len(s.externalPhysicalRows()))
		s.scroll = s.viewport.Offset()
	}
	return nil, true
}

// HandleMsg reduces the four RPC-backed message types into the surface state,
// with the requestID/generation/row-identity staleness checks on surface state.
// Each fires no follow-up command (the panel is a single-shot read). A
// client.SkillChangesMsg is deliberately NOT consumed: it is a Model-owned
// status receipt (writes m.statusMsg from m.skillChangeLast), so it passes
// through to the Model's slim updateSkillChangesMsg reducer. Anything else
// (blink ticks, stream events) passes too.
//
//nolint:gocyclo // inventory, lifecycle detail, and diff messages share one reducer
func (s *skillsState) HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool, closed bool) {
	if diff, ok := msg.(client.SkillDiffMsg); ok {
		if diff.RequestID != s.requestID || s.detail == nil || diff.Project != s.detail.Project || diff.SkillID != s.detail.ID || diff.Version != s.detail.Version {
			return nil, true, false
		}
		if diff.Err != nil {
			s.err = diff.Err
		} else {
			s.diff = diff.Diff
			s.err = nil
		}
		return nil, true, false
	}
	if learned, ok := msg.(client.LearnedSkillsMsg); ok {
		if learned.RequestID != s.requestID || learned.Project != s.project {
			return nil, true, false
		}
		for partition, generation := range learned.Generations {
			if generation < s.generations[partition] {
				return nil, true, false
			}
		}
		if learned.Err != nil {
			s.err = learned.Err
		} else {
			s.learned = learned.Skills
			s.cursor = 0
			s.generations = cloneSkillGenerations(learned.Generations)
			s.err = nil
		}
		return nil, true, false
	}
	if detail, ok := msg.(client.LearnedSkillMsg); ok {
		if detail.RequestID != s.requestID || detail.Generation < s.generations[detail.Project] {
			return nil, true, false
		}
		if s.detail != nil && (detail.Project != s.detail.Project || detail.SelectedSkillID != s.detail.ID || detail.SelectedOwnerAgent != "" && detail.SelectedOwnerAgent != s.detail.OwnerAgent || detail.SelectedVersion != s.detail.Version || detail.ExpectedRevision != "" && detail.ExpectedRevision != s.detail.Revision) {
			return nil, true, false
		}
		targetVersion := detail.SelectedVersion
		if detail.Action == "rollback" {
			targetVersion = detail.TargetVersion
		}
		if detail.Err == nil && detail.Skill != nil && (detail.Skill.Project != detail.Project || detail.Skill.ID != detail.SelectedSkillID || detail.SelectedOwnerAgent != "" && detail.Skill.OwnerAgent != detail.SelectedOwnerAgent || detail.Skill.Version != targetVersion) {
			return nil, true, false
		}
		if detail.Err != nil {
			s.err = detail.Err
			return nil, true, false
		}
		if detail.PublicationError != "" {
			s.err = fmt.Errorf("publication %s: %s", detail.PublicationStatus, detail.PublicationError)
		}
		if detail.Skill != nil {
			value := *detail.Skill
			s.detail, s.view = &value, skillsDetail
			s.generations = cloneSkillGenerations(s.generations)
			s.generations[detail.Project] = detail.Generation
			if detail.PublicationError == "" {
				s.err = nil
			}
			for i := range s.learned {
				if s.learned[i].Project == value.Project && s.learned[i].ID == value.ID && (s.learned[i].Version == value.Version || detail.Action == "rollback" && s.learned[i].Version == detail.SelectedVersion) {
					s.learned[i] = value
				}
			}
		}
		return nil, true, false
	}
	var sm client.SkillsMsg
	switch result := msg.(type) {
	case skillsResultMsg:
		if result.requestID != s.requestID {
			return nil, true, false
		}
		sm = result.result
	case client.SkillsMsg: // compatibility for direct reducer tests
		sm = result
	default:
		return nil, false, false
	}
	s.loading = false
	if sm.Err != nil {
		s.err = sm.Err
		s.skills, s.filtered, s.viewport = nil, nil, nil
		s.scroll = 0
		return nil, true, false
	}
	s.err = nil
	s.skills = sm.Skills
	if s.viewport != nil {
		s.viewport.Reset()
	}
	s.scroll = 0
	s.syncFilter()
	return nil, true, false
}

// Close tears the surface down; teardown is a no-op for skills (the surface
// holds no resource). The parent dispatchSurfaceKey/dispatchSurfaceMsg closed
// path runs Close, nils m.modal, and batches m.prompt.Focus() itself — the refocus
// is parent-authored. A skills RPC that lands after close falls through
// HandleMsg's handled=false and is dropped at the Model.
func (*skillsState) Close() {}

// rowTotal is the rendered body-row count over the FILTERED inventory — the
// clamp target HandleKey/HandleWheel/Render use, so the scroll window and the
// clamp can never disagree about the line count. With an empty filter
// filtered == skills, so this doubles as the full-inventory total. The width is
// the view-cache copy Render refreshes (HandleKey receives no width argument;
// geometry stays pure Render input).
func (s *skillsState) externalPhysicalRows() []string {
	if len(s.filtered) == 0 {
		return nil
	}
	return skillsRowLines(s.deps.theme, s.filtered, s.bodyWidth)
}

func (s *skillsState) rowTotal(th theme.Theme, width int) int {
	budget := s.bodyWidth
	if budget <= 0 {
		budget = cardTextWidth(width)
	}
	return len(skillsRowLines(th, s.filtered, budget))
}

// filterSkills returns the skills whose Name OR Description CONTAIN q
// (case-insensitive), preserving input order (the server's name-sort). An empty q
// returns the full list. Mirrors filterModels.
func filterSkills(skills []client.Skill, q string) []client.Skill {
	if q == "" {
		return skills
	}
	lq := strings.ToLower(q)
	out := make([]client.Skill, 0, len(skills))
	for _, s := range skills {
		if strings.Contains(strings.ToLower(s.Name), lq) ||
			strings.Contains(strings.ToLower(s.Description), lq) {
			out = append(out, s)
		}
	}
	return out
}

// syncFilter recomputes the filtered slice from the current filter value and
// clamps the scroll offset against the filtered row total (the skills panel has
// no cursor, so scroll is the only thing to clamp). Mirrors syncModelsFilter.
// Called on every input-feeding key and once when the list lands.
func (s *skillsState) syncFilter() {
	s.filtered = filterSkills(s.skills, s.filter.Value())
	if len(s.filtered) == 0 {
		s.viewport = nil
		s.scroll = 0
		return
	}
	if s.viewport != nil {
		s.viewport.Clamp(len(s.externalPhysicalRows()))
		s.scroll = s.viewport.Offset()
	}
}

func cloneSkillGenerations(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for partition, generation := range in {
		out[partition] = generation
	}
	return out
}

func renderLearnedSkillDetail(th theme.Theme, skill client.LearnedSkill, diff string, width int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Learned skill") + "\n\n")
	state := skill.State
	if state == "active" && len(skill.Evaluations) > 0 && skill.Evaluations[len(skill.Evaluations)-1].Verdict == "abstain" {
		state = "active(validated)"
	}
	if state == "active" {
		for i := len(skill.Receipts) - 1; i >= 0; i-- {
			switch skill.Receipts[i].Operation {
			case "activate_validated":
				state = "active(validated)"
			case "activate":
				state = "active(evaluated)"
			}
			if state != "active" {
				break
			}
		}
	}
	budget := cardTextWidth(width)
	write := func(line string) { b.WriteString(wrapCardText(line, budget) + "\n") }
	for _, line := range []string{"name: " + skill.Name, "created by: " + skill.OwnerAgent, "status: " + state, "version: " + skill.Version, "latest change: " + skill.Revision, "supporting examples: " + strconv.Itoa(skill.EvidenceCount), "what it does: " + skill.Description, "instructions: " + skill.Body} {
		write(line)
	}
	if len(skill.Evaluations) > 0 {
		e := skill.Evaluations[len(skill.Evaluations)-1]
		write("latest evaluation: " + e.Verdict + " examples=" + strings.Join(e.FixtureIDs, ","))
		if e.Baseline != "" {
			write("compared with: " + e.Baseline)
		}
		if e.Treatment != "" {
			write("candidate: " + e.Treatment)
		}
	}
	write("change history: " + strconv.Itoa(len(skill.Receipts)))
	for _, receipt := range skill.Receipts {
		write("  " + receipt.Operation + " " + receipt.FromState + " → " + receipt.ToState)
	}
	if diff != "" {
		b.WriteString("\nchanges from this version:\n" + wrapCardText(diff, budget) + "\n")
	}
	b.WriteString("\n" + wrapCardText("esc back · v show changes · a activate · x reject · d archive · r restore previous version", budget))
	return b.String()
}

// cardTextWidth is the column budget for wrapping server-derived overlay text
// (skill/agent descriptions, metadata lines, error lines) to a centred card's
// inner width: the terminal width minus the askCard chrome (border + horizontal
// padding) and a centering margin, capped so lines stay readable on very wide
// terminals. A non-positive or very narrow terminal returns 0, which disables
// wrapping so an unknown size renders the bare, content-sized card like the other
// overlays do. Shared by the /skills and /agents inventory panels.
func cardTextWidth(width int) int {
	const (
		chrome = 6   // askCard border(2) + horizontal padding(2*2)
		margin = 4   // breathing room so the centered card isn't flush to the edge
		maxW   = 100 // cap so prose stays readable on very wide terminals
		minW   = 20  // below this, don't wrap (degrade to the bare card)
	)
	w := width - chrome - margin
	if w > maxW {
		w = maxW
	}
	if w < minW {
		return 0
	}
	return w
}

// wrapCardText sanitizes a server-derived plain-text row before wrapping it to
// the offered card-body budget. Callers apply styles only after this step;
// assistant Markdown remains on its separate glamour rendering path.
func wrapCardText(text string, budget int) string {
	text = terminaltext.Sanitize(text)
	if budget > 0 {
		return ansi.Wrap(text, budget, "")
	}
	return text
}

// focusCardTextWidth returns the usable card body width for a focus overlay. Unlike
// cardTextWidth, a known positive viewport must never disable trace wrapping: even
// a viewport narrower than the card chrome gets a one-cell budget so a long token
// cannot make the centred card wider.
func focusCardTextWidth(width int) int {
	if budget := cardTextWidth(width); budget > 0 || width <= 0 {
		return budget
	}
	return max(1, width-6) // askCard border (2) + horizontal padding (2*2)
}

// wrapFocusMetadataAtWidth wraps a complete focus-card row to its supplied body budget.
// A zero budget remains deliberately unbounded, matching the other overlays.
func wrapFocusMetadataAtWidth(s string, budget int) string {
	if budget > 0 {
		return ansi.Wrap(s, budget, "")
	}
	return s
}

// hangingIndentWrap wraps a detail row to the text budget while indenting continuation
// rows deeper than its parent lane. It applies the continuation's narrower width to every
// line, which guarantees both indentation forms fit the card without dropping content.
func hangingIndentWrap(s, firstIndent string, budget int) string {
	const continuationIndent = "    "
	if budget <= 0 {
		return firstIndent + s
	}
	continuationWidth := lipgloss.Width(continuationIndent)
	if continuationWidth >= budget {
		return ansi.Wrap(s, budget, "")
	}
	lines := strings.Split(ansi.Wrap(s, budget-continuationWidth, ""), "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = firstIndent + line
		} else {
			lines[i] = continuationIndent + line
		}
	}
	return strings.Join(lines, "\n")
}

// indentWrap word-wraps s to the text budget and indents every resulting line by
// two spaces (the inventory's description indent) so continuation lines align
// under the first. A budget <= 0 falls back to a single indented line (unknown
// size). ansi.Wrap breaks over-long tokens too, so a space-free string can't
// overflow the card.
func indentWrap(s string, budget int) string {
	const indent = "  "
	if budget <= 0 {
		return indent + s
	}
	if budget <= len(indent) {
		return ansi.Wrap(s, budget, "")
	}
	wrapped := ansi.Wrap(s, budget-len(indent), "")
	lines := strings.Split(wrapped, "\n")
	for i, ln := range lines {
		lines[i] = indent + ln
	}
	return strings.Join(lines, "\n")
}

// skillsDisabledNote is the empty-inventory copy when skills are NOT enabled on
// the connected server (caps.Skills == false), with the remedy. Like the MCP
// panel, the relayed caps let the ui distinguish "not enabled" from "enabled but
// empty", which a ui-local guess never could for an external server.
const skillsDisabledNote = "Skills are not enabled on this server.\n" +
	"Start or connect to a server with skills enabled."

// skillsEmptyCopy returns the empty-state line: the "not enabled" note (with
// remedy) when caps.Skills is false, else the "enabled but empty" note.
func skillsEmptyCopy(caps client.Capabilities) string {
	if !caps.Skills {
		return skillsDisabledNote
	}
	return "No skills are available on this server."
}

// skillsRowLines builds the rendered (ANSI-carrying) inventory body rows: per
// skill, a name line plus the indented, word-wrapped description lines. EVERY
// server-derived string is terminal-sanitized BEFORE styling, so the rows are
// safe inputs for windowRenderedLines (which must not re-sanitize — that would
// strip the styling). The multi-line description render is split per line
// (lipgloss emits complete per-line SGR sequences) so the scroll window can
// slice anywhere without severing an escape.
func skillsRowLines(th theme.Theme, skills []client.Skill, budget int) []string {
	var lines []string
	for _, s := range skills {
		name := terminaltext.Sanitize(s.Name)
		if s.AgentOwned {
			name += "  [agent-owned · active " + terminaltext.Sanitize(s.ActiveVersion) + " · " + terminaltext.Sanitize(s.OwnerAgent) + "]"
		}
		lines = append(lines, strings.Split(renderToolCardText(th.Style("toolName"), name, budget), "\n")...)
		if s.Description != "" {
			desc := th.Style("toolArgs").Render(indentWrap(terminaltext.Sanitize(s.Description), budget))
			lines = append(lines, strings.Split(desc, "\n")...)
		}
	}
	return lines
}

func renderSkillsNormalBody(s *skillsState, layout skillsLayout) string {
	lines := append([]string(nil), layout.title...)
	filter := ansi.Cut(s.filter.View(), 0, layout.bodyWidth) + "\x1b[0m"
	lines = append(lines, filter)

	var external []string
	switch {
	case s.loading:
		s.viewport = nil
		external = skillsTextLines(s.deps.theme.Style("muted"), "loading…", layout.bodyWidth)
	case s.err != nil:
		s.viewport = nil
		external = skillsTextLines(s.deps.theme.Style("errorText"), "list skills: "+terminaltext.Sanitize(s.err.Error()), layout.bodyWidth)
	case len(s.skills) == 0:
		s.viewport = nil
		external = skillsTextLines(s.deps.theme.Style("muted"), skillsEmptyCopy(s.deps.caps), layout.bodyWidth)
	case len(s.filtered) == 0:
		s.viewport = nil
		external = skillsTextLines(s.deps.theme.Style("muted"), "no skills match "+strconv.Quote(s.filter.Value())+" — "+s.deps.marks.closeOnly+" to clear", layout.bodyWidth)
	default:
		rows := s.externalPhysicalRows()
		viewportHeight := s.externalRows
		showIndicator := len(rows) > viewportHeight && viewportHeight > 1
		if showIndicator {
			viewportHeight--
		}
		if s.viewport == nil {
			s.viewport = &bounded.Viewport{}
		}
		s.viewport.SetGeometry(layout.bodyWidth, viewportHeight, 0, bounded.Clip)
		view := s.viewport.View(rows)
		external = append(external, view.Rows...)
		if showIndicator {
			indicator := fmt.Sprintf("lines %d–%d of %d", view.Above+1, len(rows)-view.Below, len(rows))
			external = append(external, ansi.Cut(s.deps.theme.Style("muted").Render(indicator), 0, layout.bodyWidth)+"\x1b[0m")
		}
		s.scroll = s.viewport.Offset()
	}
	if len(external) > s.externalRows {
		external = external[:s.externalRows]
	}
	for len(external) < s.externalRows {
		external = append(external, "")
	}
	lines = append(lines, external...)

	if layout.hasLearned {
		label := "Learned skills"
		if s.focus == skillsFocusLearned {
			label += " (focused)"
		}
		lines = append(lines, ansi.Cut(s.deps.theme.Style("muted").Render(label+":"), 0, layout.bodyWidth)+"\x1b[0m")
		s.cursor = clampBounded(s.cursor, len(s.learned))
		s.clampLearnedWindow()
		end := min(len(s.learned), s.learnedStart+s.learnedRows)
		for i := s.learnedStart; i < end; i++ {
			mark := "  "
			if i == s.cursor {
				mark = "> "
			}
			skill := s.learned[i]
			text := mark + terminaltext.Sanitize(skill.Name) + " [" + terminaltext.Sanitize(skill.State) + " · " + terminaltext.Sanitize(skill.OwnerAgent) + "]"
			lines = append(lines, ansi.Cut(s.deps.theme.Style("toolName").Render(text), 0, layout.bodyWidth)+"\x1b[0m")
		}
		for len(lines) < len(layout.title)+1+s.externalRows+1+s.learnedRows {
			lines = append(lines, "")
		}
	}
	lines = append(lines, layout.footer...)
	return strings.Join(lines, "\n")
}

// renderSkillsPanel is the unframed compatibility seam for focused tests.
func renderSkillsPanel(th theme.Theme, st skillsState, caps client.Capabilities, hk helpKeys, width int) string {
	st.deps.theme, st.deps.caps, st.deps.marks = th, caps, hk
	layout := newSkillsLayout(th, hk, width, 1<<20, len(st.learned) > 0)
	st.bodyWidth, st.bodyRows = layout.bodyWidth, layout.bodyRows
	st.externalRows, st.learnedRows = layout.regionRows()
	st.normal = true
	return renderSkillsNormalBody(&st, layout)
}
