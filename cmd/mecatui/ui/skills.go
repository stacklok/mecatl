package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
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

// skillsBodyLines is the fixed number of inventory rows the panel shows at once
// (the scroll window). A fixed budget keeps the panel — and its goldens —
// deterministic regardless of terminal height. A long inventory scrolls; a
// short one shows in full with no scroll indicator.
const skillsBodyLines = 14

// skillsState is the skills overlay's state: it is constructed at Open
// (runSkills) and lives ONLY inside the Model's one `modal surface` interface
// field — never a pre-declared Model field. The skills slice is replaced
// wholesale on each RPC result (never mutated in place). skillsState implements
// `surface` on POINTER receivers.
type skillsState struct {
	view        skillsView
	loading     bool  // the ListSkills RPC is in flight
	err         error // the ListSkills error, rendered distinctly (nil on success)
	skills      []client.Skill
	filtered    []client.Skill  // subset matching filter.Value(); recomputed on each key (mirror models.filtered)
	filter      textinput.Model // the type-to-filter input; focused while the panel is open
	scroll      int             // view cache: first visible rendered body row (clamped in Render; reset on each RPC result)
	width       int             // view cache: last Render width, read by HandleKey clamp targets (Render refreshes it every frame)
	learned     []client.LearnedSkill
	cursor      int
	detail      *client.LearnedSkill
	diff        string
	project     string
	generations map[string]uint64 // independent publication/catalog epoch per global/project partition
	requestID   uint64

	deps             surfaceDeps               // the shared ambient base (incl. ctx), set once at Open
	learnedLifecycle client.LearnedSkillClient // nil unless the Skills collaborator carries the lifecycle half
	nextEpoch        func() uint64             // bumps the Model-owned skillsEpoch; correlation survives close/reopen
}

// runSkills is the Open transition for the skills surface. It validates (idle +
// lister wired), blurs the textarea, bumps the Model-owned request epoch
// (correlation must survive close/reopen), constructs the state dynamically
// with the filter focused for immediate narrowing, installs it on m.modal, and
// batches the ListSkills + (when the lifecycle half is wired) ListLearnedSkills
// RPCs with the filter's blink. Only registered when caps.Skills && the skills
// collaborator is wired.
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
	ti.Focus()
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
	cmds := []tea.Cmd{client.ListSkillsCmd(m.deps.Ctx, m.deps.Skills), textinput.Blink}
	if lifecycle != nil {
		cmds = append(cmds, client.ListLearnedSkillsCmd(m.deps.Ctx, lifecycle, m.deps.Workspace, m.skillsEpoch))
	}
	return m, tea.Batch(cmds...)
}

// Render returns the panel (or detail) body sized from the offered width. The
// view-cache width and the scroll clamp are re-derived AT THE TOP from the
// fresh width (the scroll clamp bounds against the wrapped row total at this
// width), so HandleKey/HandleWheel read a current clamp target. Regions are nil
// (read-only, not clickable). The height param goes unused by design: the
// panel's scroll window is the FIXED skillsBodyLines row budget (deterministic
// goldens, terminal-height-independent).
func (s *skillsState) Render(width, _ int) (string, []ClickableRegion) {
	s.width = width
	s.scroll = clampScroll(s.scroll, s.rowTotal(s.deps.theme, width), skillsBodyLines)
	if s.view == skillsNone {
		return "", nil
	}
	if s.view == skillsDetail && s.detail != nil {
		body := renderLearnedSkillDetail(s.deps.theme, *s.detail, s.diff, width)
		if s.err != nil {
			body += "\n\n" + s.deps.theme.Style("errorText").Render(sanitizeTerminal(s.err.Error())) + "\npress esc, then enter to refresh"
		}
		return body, nil
	}
	return renderSkillsPanel(s.deps.theme, *s, s.deps.caps, s.deps.marks, width), nil
}

// HandleKey routes key presses while the skills overlay is open. The filter
// input is FOCUSED: nav keys are intercepted first, everything else feeds the
// input (read-only narrowing; the learned list's cursor + enter→detail is the
// one selection path). The detail view discriminates FIRST: esc steps back to
// the panel (never closes); the lifecycle actions (a = activate, x = reject,
// d = archive, v = diff, r = rollback) each mint a fresh requestID via
// s.nextEpoch() and fire the RPC cmd over s.deps.ctx + s.learnedLifecycle.
//
// The sharp edge: keys.Up/keys.Down also bind "k"/"j" (keys.go), so matching
// them with key.Matches would hijack a typed query like "kotlin"/"java". List
// nav matches the ARROW keys by msg.String() ONLY; the other nav keys
// (pgup/pgdown/home/end) and esc are non-printable, so key.Matches against
// ScrollU/ScrollD/ScrollTop/ScrollBottom/Close is safe.
//
// esc is TWO-STAGE: a non-empty filter is cleared first (the panel stays open);
// an empty filter closes the panel. Every key is handled=true. After any
// input-feeding key the filter is re-synced (recompute + scroll clamp) and the
// input's cmd returned for the cursor blink.
//
//nolint:gocyclo // panel, lifecycle detail, and filter keys remain in one visible router
func (s *skillsState) HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	if s.view == skillsNone {
		return nil, false, false
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
	if msg.String() == "enter" && len(s.learned) > 0 {
		if s.learnedLifecycle == nil {
			return nil, true, false
		}
		selected := s.learned[min(s.cursor, len(s.learned)-1)]
		s.requestID = s.nextEpoch()
		return client.GetLearnedSkillCmd(s.deps.ctx, s.learnedLifecycle, selected, s.requestID), true, false
	}
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
			return nil, true, false
		}
		return nil, true, true
	case msg.String() == keyMenuDown:
		if len(s.learned) > 0 {
			s.cursor = min(s.cursor+1, len(s.learned)-1)
		}
		s.scroll = clampScroll(s.scroll+1, s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
		return nil, true, false
	case msg.String() == keyMenuUp:
		s.cursor = max(0, s.cursor-1)
		s.scroll = clampScroll(s.scroll-1, s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
		return nil, true, false
	case key.Matches(msg, s.deps.keys.ScrollD):
		s.scroll = clampScroll(s.scroll+1, s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
		return nil, true, false
	case key.Matches(msg, s.deps.keys.ScrollU):
		s.scroll = clampScroll(s.scroll-1, s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
		return nil, true, false
	case key.Matches(msg, s.deps.keys.ScrollBottom):
		s.scroll = maxScrollOffset(s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
		return nil, true, false
	case key.Matches(msg, s.deps.keys.ScrollTop):
		s.scroll = 0
		return nil, true, false
	}
	// Everything else feeds the focused filter input (printable runes, backspace,
	// ←/→, …); recompute the filtered slice + clamp the scroll afterwards.
	s.filter, cmd = s.filter.Update(msg)
	s.syncFilter()
	return cmd, true, false
}

// HandleWheel scrolls the panel: wheel up/down move s.scroll, clamped to the
// row window, and the event is consumed (handled=true). The clamp target uses
// the view-cache width Render refreshes every frame.
func (s *skillsState) HandleWheel(msg tea.MouseWheelMsg) (cmd tea.Cmd, handled bool) {
	if s.view == skillsNone {
		return nil, false
	}
	delta := 1
	if msg.Mouse().Button == tea.MouseWheelUp {
		delta = -1
	}
	s.scroll = clampScroll(s.scroll+delta, s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
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
	sm, ok := msg.(client.SkillsMsg)
	if !ok {
		return nil, false, false
	}
	s.loading = false
	if sm.Err != nil {
		s.err = sm.Err
		return nil, true, false
	}
	s.err = nil
	s.skills = sm.Skills
	s.scroll = 0
	// Derive the filtered slice (+ clamp the scroll) from the current filter
	// value; on a fresh open the filter is empty, so filtered == skills.
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
func (s *skillsState) rowTotal(th theme.Theme, width int) int {
	return len(skillsRowLines(th, s.filtered, cardTextWidth(width)))
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
	s.scroll = clampScroll(s.scroll, s.rowTotal(s.deps.theme, s.width), skillsBodyLines)
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
	for _, line := range []string{"name: " + skill.Name, "owner: " + skill.OwnerAgent, "state: " + state, "version: " + skill.Version, "revision: " + skill.Revision, "evidence: " + strconv.Itoa(skill.EvidenceCount), "description: " + skill.Description, "body: " + skill.Body} {
		write(line)
	}
	if len(skill.Evaluations) > 0 {
		e := skill.Evaluations[len(skill.Evaluations)-1]
		write("evaluation: " + e.Verdict + " fixtures=" + strings.Join(e.FixtureIDs, ","))
		if e.Baseline != "" {
			write("baseline: " + e.Baseline)
		}
		if e.Treatment != "" {
			write("treatment: " + e.Treatment)
		}
	}
	write("history receipts: " + strconv.Itoa(len(skill.Receipts)))
	for _, receipt := range skill.Receipts {
		write("  " + receipt.Operation + " " + receipt.FromState + " → " + receipt.ToState)
	}
	if diff != "" {
		b.WriteString("\nversion diff:\n" + wrapCardText(diff, budget) + "\n")
	}
	b.WriteString("\nesc back   v diff   a activate   x reject   d archive   r rollback")
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
	text = sanitizeTerminal(text)
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

// wrapFocusMetadata wraps a complete focus-card row to its positive body budget.
// A zero viewport remains deliberately unbounded, matching the other overlays.
func wrapFocusMetadata(s string, width int) string {
	if budget := focusCardTextWidth(width); budget > 0 {
		return ansi.Wrap(s, budget, "")
	}
	return s
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
	"Run a mecated with a skills dir configured (or pass --server to one) to use them."

// skillsEmptyCopy returns the empty-state line: the "not enabled" note (with
// remedy) when caps.Skills is false, else the "enabled but empty" note.
func skillsEmptyCopy(caps client.Capabilities) string {
	if !caps.Skills {
		return skillsDisabledNote
	}
	return "No skills configured on this server."
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
		name := sanitizeTerminal(s.Name)
		if s.AgentOwned {
			name += "  [agent-owned · active " + sanitizeTerminal(s.ActiveVersion) + " · " + sanitizeTerminal(s.OwnerAgent) + "]"
		}
		lines = append(lines, renderToolCardText(th.Style("toolName"), name, budget))
		if s.Description != "" {
			desc := th.Style("toolArgs").Render(indentWrap(sanitizeTerminal(s.Description), budget))
			lines = append(lines, strings.Split(desc, "\n")...)
		}
	}
	return lines
}

// renderSkillsPanel renders the read-only inventory: one row per skill (name +
// description), name-sorted by the server, scroll-windowed to skillsBodyLines
// with a "lines X–Y of N" indicator when the inventory overflows. EVERY
// server-derived string is terminal-sanitized.
func renderSkillsPanel(th theme.Theme, st skillsState, caps client.Capabilities, hk helpKeys, width int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Skills inventory") + "\n\n")
	// The filter input row (focused while the panel is open) sits ABOVE the body
	// window, inside the card chrome,
	// so a user who knows one panel knows the other.
	b.WriteString(st.filter.View() + "\n\n")

	budget := cardTextWidth(width)
	switch {
	case st.loading:
		b.WriteString(th.Style("muted").Render("loading…") + "\n")
	case st.err != nil:
		line := "list skills: " + sanitizeTerminal(st.err.Error())
		if budget > 0 {
			line = ansi.Wrap(line, budget, "")
		}
		b.WriteString(th.Style("errorText").Render(line) + "\n")
	case len(st.skills) == 0 && len(st.learned) == 0:
		// Server-side empty/disabled (no inventory at all) — distinct from a filter
		// that matched nothing.
		b.WriteString(th.Style("muted").Render(skillsEmptyCopy(caps)) + "\n")
	case len(st.filtered) == 0:
		// The filter matched nothing (the inventory is non-empty). A clear, distinct
		// note with a recovery hint; the scroll is safe (clamped to 0).
		b.WriteString(th.Style("muted").Render("no skills match "+strconv.Quote(st.filter.Value())+" — "+hk.closeOnly+" to clear") + "\n")
	default:
		b.WriteString(windowRenderedLines(th, skillsRowLines(th, st.filtered, budget), st.scroll, skillsBodyLines))
	}
	if len(st.learned) > 0 {
		b.WriteString("\n" + th.Style("muted").Render("Learned skills (↑/↓ select, enter inspect):") + "\n")
		for i, skill := range st.learned {
			mark := "  "
			if i == st.cursor {
				mark = "> "
			}
			b.WriteString(renderToolCardText(th.Style("toolName"), mark+skill.Name+" ["+skill.State+" · "+skill.OwnerAgent+"]", budget) + "\n")
		}
	}

	// The "↑/↓" nav stays literal: the skills handler scrolls on the BARE up/down
	// keys (msg.String, not key.Matches), so they are genuinely fixed. The pgup/pgdn
	// scroll pair and the close chord ARE keymap-bound (ScrollU/ScrollD/Close) so
	// they read the LIVE markings (issue #457).
	b.WriteString("\n" + th.Style("muted").Render("skills activate automatically when relevant · type to filter · ↑/↓/"+hk.scroll+" scroll · "+hk.closeOnly+" clear filter / close"))
	return b.String()
}
