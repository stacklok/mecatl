package ui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/schedparse"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// schedule.go is the /schedule overlay (issue #234, Phase 3a) — a selecting
// overlay mirroring /worktrees' cursor+filter+confirm shape, with an added
// read-only inspect sub-view (full spec + state + fires), per-row action keys
// (pause/resume/fire-now/delete), and a Create form (Phase 3b, issue #236). The
// form's trigger field accepts EITHER raw cron OR a natural-language phrase
// (compiled client-side via cmd/mecatui/schedparse, stdlib-only).
//
// UNLIKE /worktrees and /models, the filter input is NOT focused on open — the
// panel's own single-letter action keys (p/r/f/d/c) would otherwise be
// unreachable (they'd always hit the focused filter instead of firing).
// Pressing "/" enters filter mode (focuses the input); esc or enter while
// filtering exits it (blur, value kept). See onScheduleKey.

// scheduleView is the active /schedule overlay (none = closed).
type scheduleView int

const (
	scheduleNone    scheduleView = iota // overlay closed
	schedulePanel                       // the flat, type-to-filter list
	scheduleConfirm                     // the post-d delete confirmation
	scheduleInspect                     // the read-only full-spec + fires view
	scheduleCreate                      // the in-overlay Create form (Phase 3b)
)

// scheduleState holds the /schedule overlay state on the Model. Value-embedded so
// the Model stays a plain struct Update copies. On List, schedules is replaced
// wholesale; on a Get-driven refresh (ScheduleMsg), the matching element is
// updated in place instead. filtered is always rebuilt from schedules by
// syncScheduleFilter.
type scheduleState struct {
	view         scheduleView
	loading      bool
	err          error
	schedules    []client.Schedule
	filtered     []client.Schedule
	filter       textinput.Model
	cursor       int
	confirm      client.Schedule
	inspect      client.Schedule
	fires        []client.ScheduleFire
	firesLoading bool
	firesErr     error
	actionErr    string
	fireCursor   int // cursor into fires in the inspect sub-view (jump-to-fire, #235)
	form         scheduleForm
}

// scheduleForm is the in-overlay Create form (Phase 3b, issue #236): a small,
// common-path authoring surface mirroring the per-row action keys. The CLI
// (mecated schedule create) covers the full flag surface; the form keeps it
// SIMPLE — name, prompt, trigger (cron OR NL), mutating. The trigger
// field accepts EITHER a raw cron expression OR a natural-language phrase; on
// submit, schedparse.Compile is tried first (compile to cron or one-shot), and
// only on no-match is the value treated verbatim as raw cron. Mode defaults to
// plan for non-mutating (the server enforces the mode↔mutating invariant);
// singleton defaults true. focusIdx is the cursor over the fields.
type scheduleForm struct {
	name     textinput.Model
	prompt   textinput.Model
	trigger  textinput.Model
	mutating bool
	focusIdx int
}

// openSchedule opens the picker and fires the ListSchedules RPC. Only callable
// while idle and when a schedule lister is wired; returns the model unchanged
// otherwise. The result arrives as a client.SchedulesMsg handled in
// updateScheduleMsg.
func (m Model) openSchedule() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sched == nil {
		return m, nil
	}
	m.prompt.Blur()
	m.schedule.view = schedulePanel
	m.schedule.loading = true
	m.schedule.err = nil
	m.schedule.cursor = 0
	m.schedule.actionErr = ""
	ti := textinput.New()
	ti.Placeholder = "filter schedules…"
	ti.SetWidth(40)
	m.schedule.filter = ti
	m.schedule.filtered = nil
	return m, client.ListSchedulesCmd(m.deps.Ctx, m.deps.Sched)
}

// closeSchedule dismisses the overlay and returns focus to the prompt input.
func (m Model) closeSchedule() (tea.Model, tea.Cmd) {
	m.schedule = scheduleState{}
	cmd := m.prompt.Focus()
	return m, cmd
}

// onScheduleKey routes key presses while the overlay is open. UNLIKE
// onWorktreesKey/onModelsKey, the filter is NOT focused by default — the panel
// has per-row action keys (p/r/f/d) that would otherwise be unreachable through
// a focused filter. Two modes, keyed off m.schedule.filter.Focused():
//
//   - Filter mode (focused): esc or enter blurs the input (exits filter mode,
//     keeps the value so the list stays narrowed); everything else feeds the
//     textinput.
//   - Action mode (blurred, the default): "/" focuses the filter (enters filter
//     mode); esc is two-stage (clear filter if set, else close); nav/inspect/
//     action keys behave as before; any other key is swallowed (never leaks
//     into the filter).
func (m Model) onScheduleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.schedule.view == scheduleNone {
		return m, nil, false
	}
	if m.schedule.view == scheduleConfirm {
		return m.onScheduleConfirmKey(msg)
	}
	if m.schedule.view == scheduleInspect {
		return m.onScheduleInspectKey(msg)
	}
	if m.schedule.view == scheduleCreate {
		return m.onScheduleCreateKey(msg)
	}
	if m.schedule.filter.Focused() {
		switch {
		case key.Matches(msg, m.keys.Close), key.Matches(msg, m.keys.Choose):
			m.schedule.filter.Blur()
			return m, nil, true
		}
		var cmd tea.Cmd
		m.schedule.filter, cmd = m.schedule.filter.Update(msg)
		m = m.syncScheduleFilter()
		return m, cmd, true
	}
	return m.onSchedulePanelActionKey(msg)
}

// onSchedulePanelActionKey routes bare-rune action keys while the panel is in
// action mode (filter not focused). It handles nav, filter-entry, and the
// per-row action keys (p/r/f/d/c).
func (m Model) onSchedulePanelActionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		if m.schedule.filter.Value() != "" {
			m.schedule.filter.SetValue("")
			m = m.syncScheduleFilter()
			return m, nil, true
		}
		mm, c := m.closeSchedule()
		return mm, c, true
	case msg.String() == "/":
		cmd := m.schedule.filter.Focus()
		return m, cmd, true
	case msg.String() == keyMenuUp:
		if m.schedule.cursor > 0 {
			m.schedule.cursor--
		}
		return m, nil, true
	case msg.String() == keyMenuDown:
		if m.schedule.cursor < len(m.schedule.filtered)-1 {
			m.schedule.cursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollTop):
		m.schedule.cursor = 0
		return m, nil, true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.schedule.cursor = clampModelsCursor(len(m.schedule.filtered)-1, len(m.schedule.filtered))
		return m, nil, true
	case key.Matches(msg, m.keys.Choose):
		return m.openScheduleInspect()
	case msg.String() == "p":
		return m.scheduleAction("pause")
	case msg.String() == "r":
		return m.scheduleAction("resume")
	case msg.String() == "f":
		return m.scheduleAction("fire")
	case msg.String() == "d":
		return m.openScheduleConfirm()
	case msg.String() == "c":
		return m.openScheduleCreate()
	}
	// Swallow anything unmatched — action mode never feeds the filter.
	return m, nil, true
}

// openScheduleInspect opens the inspect sub-view for the cursor row, firing
// GetSchedule + ListFires. A cursor past the list end is a no-op.
func (m Model) openScheduleInspect() (tea.Model, tea.Cmd, bool) {
	if m.schedule.cursor < 0 || m.schedule.cursor >= len(m.schedule.filtered) {
		return m, nil, true
	}
	m.schedule.inspect = m.schedule.filtered[m.schedule.cursor]
	m.schedule.view = scheduleInspect
	m.schedule.firesLoading = true
	m.schedule.firesErr = nil
	m.schedule.actionErr = ""
	m.schedule.fireCursor = 0
	name := m.schedule.inspect.Spec.Name
	return m, tea.Batch(
		client.GetScheduleCmd(m.deps.Ctx, m.deps.Sched, name),
		client.ListFiresCmd(m.deps.Ctx, m.deps.Sched, name),
	), true
}

// openScheduleConfirm opens the delete-confirmation sub-view for the cursor row.
func (m Model) openScheduleConfirm() (tea.Model, tea.Cmd, bool) {
	if m.schedule.cursor < 0 || m.schedule.cursor >= len(m.schedule.filtered) {
		return m, nil, true
	}
	m.schedule.confirm = m.schedule.filtered[m.schedule.cursor]
	m.schedule.view = scheduleConfirm
	return m, nil, true
}

// openScheduleCreate opens the in-overlay Create form (Phase 3b, issue #236).
// It mints a fresh scheduleForm with the focus on the name field and default
// values (singleton=true, mutating=false). The form is a common-path authoring
// surface — the CLI covers the full flag surface; the form keeps it simple.
func (m Model) openScheduleCreate() (tea.Model, tea.Cmd, bool) {
	newInput := func(placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.SetWidth(50)
		return ti
	}
	f := scheduleForm{
		name:     newInput("schedule name"),
		prompt:   newInput("prompt to run on each fire"),
		trigger:  newInput("cron (e.g. 0 9 * * *) or NL (e.g. every 30 minutes)"),
		mutating: false,
		focusIdx: 0,
	}
	f.name.Focus()
	m.schedule.form = f
	m.schedule.view = scheduleCreate
	m.schedule.actionErr = ""
	return m, nil, true
}

// scheduleFormFields is the ordered list of editable text fields for focus
// cycling. The mutating toggle is cycled separately (a bool, not a textinput).
const scheduleFormFieldCount = 3 // name, prompt, trigger

// focusScheduleField moves the focus to the field at form.focusIdx, blurring all
// others. Tab/↑↓ call this after incrementing/decrementing focusIdx.
func (m Model) focusScheduleField() Model {
	fields := []*textinput.Model{
		&m.schedule.form.name,
		&m.schedule.form.prompt,
		&m.schedule.form.trigger,
	}
	for i, f := range fields {
		if i == m.schedule.form.focusIdx {
			f.Focus()
		} else {
			f.Blur()
		}
	}
	return m
}

// onScheduleCreateKey routes keys while the Create form is open. tab/↑↓ cycle
// focus through the text fields + the mutating toggle; enter on the last field
// (the mutating toggle) submits; esc returns to the panel; "y"/"n" toggles
// mutating when the toggle is focused.
func (m Model) onScheduleCreateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	// When the mutating toggle (focusIdx == scheduleFormFieldCount) is focused,
	// handle the toggle keys; enter submits.
	if m.schedule.form.focusIdx == scheduleFormFieldCount {
		switch {
		case key.Matches(msg, m.keys.Close):
			m.schedule.view = schedulePanel
			m.schedule.form = scheduleForm{}
			return m, nil, true
		case msg.String() == "y":
			m.schedule.form.mutating = true
			return m, nil, true
		case msg.String() == "n":
			m.schedule.form.mutating = false
			return m, nil, true
		case key.Matches(msg, m.keys.Choose):
			return m.submitScheduleCreate()
		case msg.String() == "tab", msg.String() == keyMenuDown:
			m.schedule.form.focusIdx = 0
			return m.focusScheduleField(), nil, true
		case msg.String() == keyMenuUp:
			m.schedule.form.focusIdx = scheduleFormFieldCount - 1
			return m.focusScheduleField(), nil, true
		}
		return m, nil, true
	}
	// A text field is focused.
	switch {
	case key.Matches(msg, m.keys.Close):
		m.schedule.view = schedulePanel
		m.schedule.form = scheduleForm{}
		return m, nil, true
	case msg.String() == "tab", msg.String() == keyMenuDown:
		m.schedule.form.focusIdx++
		if m.schedule.form.focusIdx > scheduleFormFieldCount {
			m.schedule.form.focusIdx = 0
		}
		return m.focusScheduleField(), nil, true
	case msg.String() == keyMenuUp:
		m.schedule.form.focusIdx--
		if m.schedule.form.focusIdx < 0 {
			m.schedule.form.focusIdx = scheduleFormFieldCount
		}
		return m.focusScheduleField(), nil, true
	case key.Matches(msg, m.keys.Choose):
		// enter on a text field advances to the next field; on the last text
		// field it advances to the mutating toggle.
		m.schedule.form.focusIdx++
		if m.schedule.form.focusIdx > scheduleFormFieldCount {
			m.schedule.form.focusIdx = 0
		}
		return m.focusScheduleField(), nil, true
	}
	// Feed the textinput.
	var fields = []*textinput.Model{
		&m.schedule.form.name,
		&m.schedule.form.prompt,
		&m.schedule.form.trigger,
	}
	idx := m.schedule.form.focusIdx
	if idx >= 0 && idx < len(fields) {
		var cmd tea.Cmd
		updated, cmd := fields[idx].Update(msg)
		*fields[idx] = updated
		return m, cmd, true
	}
	return m, nil, true
}

// submitScheduleCreate validates the form, compiles the trigger (NL→cron via
// schedparse when applicable), builds a client.ScheduleSpec, and fires
// CreateScheduleCmd. On validation failure it sets actionErr and stays in the
// form. On success it returns to the panel (the ScheduleMsg reducer appends the
// new schedule on arrival).
func (m Model) submitScheduleCreate() (tea.Model, tea.Cmd, bool) {
	f := m.schedule.form
	m.schedule.actionErr = ""
	if f.name.Value() == "" {
		m.schedule.actionErr = "name is required"
		return m, nil, true
	}
	if f.prompt.Value() == "" {
		m.schedule.actionErr = "prompt is required"
		return m, nil, true
	}
	if f.trigger.Value() == "" {
		m.schedule.actionErr = "trigger (cron or NL) is required"
		return m, nil, true
	}
	// Try NL→cron first; fall back to raw cron.
	var trigger client.ScheduleTrigger
	if res, ok := schedparse.Compile(f.trigger.Value(), time.Now()); ok {
		if res.Cron != "" {
			trigger.Cron = res.Cron
		} else if !res.OneShot.IsZero() {
			trigger.OneShot = res.OneShot
		}
	} else {
		trigger.Cron = f.trigger.Value()
	}
	spec := client.ScheduleSpec{
		Name:      f.name.Value(),
		Prompt:    f.prompt.Value(),
		Trigger:   trigger,
		Mutating:  f.mutating,
		Singleton: true,
		Timezone:  "UTC",
	}
	// Return to the panel; the ScheduleMsg reducer appends the new row.
	m.schedule.view = schedulePanel
	m.schedule.form = scheduleForm{}
	return m, client.CreateScheduleCmd(m.deps.Ctx, m.deps.Sched, spec), true
}

// scheduleAction fires the per-row action RPC (pause/resume/fire) for the cursor
// row, clearing any prior actionErr. The result arrives as a ScheduleActionMsg.
func (m Model) scheduleAction(action string) (tea.Model, tea.Cmd, bool) {
	if m.schedule.cursor < 0 || m.schedule.cursor >= len(m.schedule.filtered) {
		return m, nil, true
	}
	name := m.schedule.filtered[m.schedule.cursor].Spec.Name
	m.schedule.actionErr = ""
	var cmd tea.Cmd
	switch action {
	case "pause":
		cmd = client.PauseScheduleCmd(m.deps.Ctx, m.deps.Sched, name)
	case "resume":
		cmd = client.ResumeScheduleCmd(m.deps.Ctx, m.deps.Sched, name)
	case "fire":
		cmd = client.FireNowCmd(m.deps.Ctx, m.deps.Sched, name)
	}
	return m, cmd, true
}

// onScheduleConfirmKey routes keys while the delete-confirmation overlay is open.
// enter deletes; esc returns to the panel. Any other key is swallowed.
func (m Model) onScheduleConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	chosen := m.schedule.confirm
	switch {
	case key.Matches(msg, m.keys.Choose): // enter — delete
		m.schedule.view = schedulePanel
		m.schedule.confirm = client.Schedule{}
		return m, client.DeleteScheduleCmd(m.deps.Ctx, m.deps.Sched, chosen.Spec.Name), true
	case key.Matches(msg, m.keys.Close): // esc — back to panel
		m.schedule.view = schedulePanel
		m.schedule.confirm = client.Schedule{}
		return m, nil, true
	}
	return m, nil, true
}

// onScheduleInspectKey routes keys while the inspect sub-view is open. esc
// returns to the panel; ↑/↓ move the fire cursor (clamped to len(fires));
// enter or "t" loads the selected fire's authoritative transcript for read-only
// inspection. Jump guards: Transcript nil → no-op; fireCursor out of range or
// the fire's SessionID empty → set a statusMsg and stay in inspect.
func (m Model) onScheduleInspectKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) {
		m.schedule.view = schedulePanel
		m.schedule.inspect = client.Schedule{}
		m.schedule.fires = nil
		m.schedule.firesErr = nil
		m.schedule.fireCursor = 0
		return m, nil, true
	}
	switch {
	case msg.String() == keyMenuUp:
		if m.schedule.fireCursor > 0 {
			m.schedule.fireCursor--
		}
		return m, nil, true
	case msg.String() == keyMenuDown:
		if m.schedule.fireCursor < len(m.schedule.fires)-1 {
			m.schedule.fireCursor++
		}
		return m, nil, true
	case key.Matches(msg, m.keys.Choose), msg.String() == "t":
		return m.jumpToFireTranscript()
	}
	return m, nil, true
}

// jumpToFireTranscript loads a schedule fire's authoritative transcript for
// read-only inspection. It never rebinds the active chat.
func (m Model) jumpToFireTranscript() (tea.Model, tea.Cmd, bool) {
	if m.deps.Transcript == nil {
		return m, nil, true
	}
	if m.schedule.fireCursor < 0 || m.schedule.fireCursor >= len(m.schedule.fires) {
		m.statusMsg = m.deps.Theme.Style("warning").Render("fire has no session id")
		return m, nil, true
	}
	fire := m.schedule.fires[m.schedule.fireCursor]
	if fire.SessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("fire has no session id")
		return m, nil, true
	}
	item := client.SessionListItem{
		ID: fire.SessionID, Kind: client.SessionKindScheduled,
		Capabilities: client.SessionInventoryCapabilities{Inspect: true},
	}
	m.schedule = scheduleState{}
	return m.loadSessionTranscript(item, true)
}

// syncScheduleFilter recomputes the filtered slice from the filter input and
// clamps the cursor.
func (m Model) syncScheduleFilter() Model {
	m.schedule.filtered = filterSchedules(m.schedule.schedules, m.schedule.filter.Value())
	if m.schedule.cursor >= len(m.schedule.filtered) {
		m.schedule.cursor = 0
	}
	return m
}

// updateScheduleMsg reduces the schedule-overlay msgs. Returns handled=false for
// any non-schedule msg.
func (m Model) updateScheduleMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case client.SchedulesMsg:
		m.schedule.loading = false
		if msg.Err != nil {
			m.schedule.err = msg.Err
			m.schedule.schedules = nil
			m.schedule.filtered = nil
			return m, nil, true
		}
		m.schedule.err = nil
		m.schedule.schedules = msg.Schedules
		m = m.syncScheduleFilter()
		return m, nil, true
	case client.ScheduleMsg:
		if msg.Err != nil {
			// A Create/Get failure (bad cron, duplicate name, validation error).
			// Surface it in the panel — the form has already closed to
			// schedulePanel on submit, so a silent discard would leave the user
			// with no feedback. Mirrors the ScheduleActionMsg error path.
			m.schedule.actionErr = msg.Err.Error()
			return m, nil, true
		}
		// A Get-driven refresh: update the matching row, or append if new (Create).
		s := msg.Schedule
		updated := false
		for i, ex := range m.schedule.schedules {
			if ex.Spec.Name == s.Spec.Name {
				m.schedule.schedules[i] = s
				updated = true
				break
			}
		}
		if !updated {
			m.schedule.schedules = append(m.schedule.schedules, s)
		}
		// If the inspect sub-view is showing this schedule, refresh it too.
		if m.schedule.view == scheduleInspect && m.schedule.inspect.Spec.Name == s.Spec.Name {
			m.schedule.inspect = s
		}
		m = m.syncScheduleFilter()
		return m, nil, true
	case client.ScheduleFiresMsg:
		m.schedule.firesLoading = false
		if msg.Err != nil {
			m.schedule.firesErr = msg.Err
			m.schedule.fires = nil
			return m, nil, true
		}
		m.schedule.firesErr = nil
		m.schedule.fires = msg.Fires
		return m, nil, true
	case client.ScheduleActionMsg:
		if msg.Err != nil {
			m.schedule.actionErr = msg.Err.Error()
			return m, nil, true
		}
		m.schedule.actionErr = ""
		// On a successful fire, surface the fire id in the status line.
		if msg.Action == "fired" && msg.FireID != "" {
			m.statusMsg = m.deps.Theme.Style("muted").Render("fired " + msg.Name + " — fire id: " + sanitizeTerminal(msg.FireID))
		}
		// Re-list to reflect the new state (enabled toggle, fire count, next fire).
		return m, client.ListSchedulesCmd(m.deps.Ctx, m.deps.Sched), true
	}
	return m, nil, false
}

// filterSchedules returns the schedules whose Name or trigger summary contains q
// (case-insensitive). An empty query returns the full list.
func filterSchedules(scheds []client.Schedule, q string) []client.Schedule {
	if q == "" {
		return scheds
	}
	needle := strings.ToLower(q)
	out := make([]client.Schedule, 0, len(scheds))
	for _, s := range scheds {
		if strings.Contains(strings.ToLower(s.Spec.Name), needle) ||
			strings.Contains(strings.ToLower(triggerSummary(s.Spec)), needle) {
			out = append(out, s)
		}
	}
	return out
}

// renderScheduleOverlay draws the overlay, dispatching on the view.
// transcriptWired gates the jump-to-fire footer hint in the inspect sub-view.
func renderScheduleOverlay(th theme.Theme, st scheduleState, caps client.Capabilities, transcriptWired bool, hk helpKeys, width, height int) string {
	switch st.view {
	case scheduleConfirm:
		return renderScheduleConfirm(th, st, hk, width, height)
	case scheduleInspect:
		return renderScheduleInspect(th, st, transcriptWired, hk, width, height)
	case scheduleCreate:
		return renderScheduleCreate(th, st, hk, width, height)
	default:
		return renderSchedulePanel(th, st, caps, hk, width, height)
	}
}

// renderSchedulePanel renders the schedule list card.
func renderSchedulePanel(th theme.Theme, st scheduleState, _ client.Capabilities, hk helpKeys, _, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("schedules") + "\n")
	b.WriteString(th.Style("muted").Render("browse & manage scheduled tasks") + "\n\n")
	switch {
	case st.filter.Focused():
		b.WriteString(st.filter.View() + "\n\n")
	case st.filter.Value() != "":
		b.WriteString(th.Style("muted").Render("filter: "+sanitizeTerminal(st.filter.Value())) + "\n\n")
	}
	if st.loading {
		b.WriteString(th.Style("muted").Render("loading…"))
		return b.String()
	}
	if st.err != nil {
		b.WriteString(th.Style("errorText").Render("could not list schedules: " + sanitizeTerminal(st.err.Error())))
		b.WriteString("\n" + th.Style("muted").Render(hk.closeOnly+": close"))
		return b.String()
	}
	if st.actionErr != "" {
		b.WriteString(th.Style("errorText").Render("action failed: "+sanitizeTerminal(st.actionErr)) + "\n\n")
	}
	if len(st.filtered) == 0 {
		if st.filter.Value() != "" {
			b.WriteString(th.Style("muted").Render("no matches — clear filter to see all"))
		} else {
			b.WriteString(th.Style("muted").Render("no schedules found (press c to create, or use `mecated schedule create` / settings.yaml)"))
		}
		b.WriteString("\n" + th.Style("muted").Render(hk.closeOnly+": close"))
		return b.String()
	}
	for i, s := range st.filtered {
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		state := "enabled"
		if s.State.DeletionPending {
			state = "cleanup pending · d: retry delete"
		} else if !s.State.Enabled {
			state = "paused"
		}
		inFlight := ""
		if !s.State.LastFireStartedAt.IsZero() {
			inFlight = "  in-flight"
		} else if s.State.LastFireSessionID == "pending" {
			inFlight = "  claimed"
		}
		line := marker + sanitizeTerminal(s.Spec.Name) +
			"  " + sanitizeTerminal(triggerSummary(s.Spec)) +
			"  " + state +
			"  next:" + formatScheduleTime(s.State.NextFireAt) +
			"  last:" + formatScheduleTime(s.State.LastFireAt) +
			"  fires:" + strconv.Itoa(int(s.State.FireCount)) +
			inFlight
		if i == st.cursor {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
	// The enter (Choose) and esc (Close) chords read the LIVE keyMap markings;
	// the c/p/r/f/d// action keys are BARE keys consumed via msg.String (NOT
	// keyMap bindings), so they stay literal (issue #457). With defaults the
	// hint is byte-identical to the historical literal.
	b.WriteString("\n" + th.Style("muted").Render(hk.choose+": inspect  c: create  p: pause  r: resume  f: fire-now  d: delete  /: filter  "+hk.closeOnly+": close"))
	return b.String()
}

// renderScheduleConfirm renders the delete-confirmation card.
func renderScheduleConfirm(th theme.Theme, st scheduleState, hk helpKeys, _, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("delete schedule") + "\n\n")
	b.WriteString("delete " + th.Style("accent").Render(sanitizeTerminal(st.confirm.Spec.Name)) + "?\n")
	b.WriteString("\n" + th.Style("muted").Render(hk.choose+": delete  "+hk.closeOnly+": back"))
	return b.String()
}

// renderScheduleInspect renders the full-spec + state + fires card. The fire
// list is cursor-navigable (#235): the cursor row is highlighted with the
// accent style + a ▶ marker, and the footer hint advertises the jump-to-fire
// action only when a replayer is wired.
//
// An IN-FLIGHT fire (#386) is rendered with an explicit "in-flight" marker plus
// its started/last-progress/deadline instants — it is NEVER silently rendered
// with an empty stop. A CLAIMED fire (the post-Claim, pre-RecordFireStart state
// where the state's LastFireSessionID is the "pending" sentinel and the run has
// not started) is rendered as an explicit "claimed (session pending)" line, so
// a claimed-but-not-yet-run fire never renders as "no fires recorded" (the
// genuinely-never-fired case).
func renderScheduleInspect(th theme.Theme, st scheduleState, replayerWired bool, hk helpKeys, _, _ int) string {
	s := st.inspect
	var b strings.Builder
	b.WriteString(th.Style("title").Render("schedule — "+sanitizeTerminal(s.Spec.Name)) + "\n\n")
	muted := th.Style("muted")
	renderScheduleSpecBlock(&b, muted, s.Spec)
	b.WriteString("\n" + muted.Render("state") + "\n")
	b.WriteString(muted.Render("enabled: ") + boolStr(s.State.Enabled) +
		"  deletion_pending: " + boolStr(s.State.DeletionPending) +
		"  fire_count: " + strconv.Itoa(int(s.State.FireCount)) + "\n")
	if s.State.DeletionPending {
		b.WriteString(th.Style("warning").Render("placement cleanup is pending — go back and press d to retry delete") + "\n")
	}
	b.WriteString(muted.Render("next_fire: ") + formatScheduleTime(s.State.NextFireAt) + "\n")
	b.WriteString(muted.Render("last_fire: ") + formatScheduleTime(s.State.LastFireAt) + "\n")
	if s.State.LastFireSessionID != "" {
		b.WriteString(muted.Render("last_fire_session: ") + sanitizeTerminal(s.State.LastFireSessionID) + "\n")
	}
	if !s.State.LastFireStartedAt.IsZero() || !s.State.LastFireProgressAt.IsZero() || !s.State.FireDeadline.IsZero() {
		b.WriteString(muted.Render("in-flight:") +
			" started " + formatScheduleTime(s.State.LastFireStartedAt) +
			"  last-progress " + formatScheduleTime(s.State.LastFireProgressAt) +
			"  deadline " + formatScheduleTime(s.State.FireDeadline) + "\n")
	}
	b.WriteString("\n" + muted.Render("fires") + "\n")
	if st.firesLoading {
		b.WriteString(muted.Render("loading…"))
	} else if st.firesErr != nil {
		b.WriteString(th.Style("errorText").Render("could not list fires: " + sanitizeTerminal(st.firesErr.Error())))
	} else if len(st.fires) == 0 {
		// A CLAIMED fire (LastFireSessionID == "pending", no run started) is
		// NOT the same as "no fires recorded" — render it explicitly so a
		// claimed-but-not-yet-run fire is never mistaken for never-fired.
		if s.State.LastFireSessionID == "pending" && s.State.LastFireStartedAt.IsZero() {
			b.WriteString(muted.Render("in-flight: claimed (session pending)"))
		} else {
			b.WriteString(muted.Render("no fires recorded"))
		}
	} else {
		for i, f := range st.fires {
			line := renderScheduleFireLine(f, i == st.fireCursor)
			if i == st.fireCursor {
				line = th.Style("accent").Render(line)
			}
			b.WriteString(line + "\n")
		}
	}
	// The esc (Close) and enter (Choose) chords read the LIVE keyMap markings;
	// the ↑↓ (bare keyMenuUp/Down) and t are BARE keys via msg.String (NOT
	// keyMap bindings), so they stay literal (issue #457).
	hint := hk.closeOnly + ": back"
	if replayerWired {
		hint = "↑↓: select fire  " + hk.choose + "/t: open transcript  " + hk.closeOnly + ": back"
	}
	b.WriteString("\n" + muted.Render(hint))
	return b.String()
}

// renderScheduleSpecBlock renders the spec-card fields (trigger/prompt/selector/
// profile/mode/mutating row/timezone/fire_timeout) for the inspect
// view. Extracted from renderScheduleInspect to keep its cyclomatic complexity
// in check (#386 added the fire_timeout branch).
func renderScheduleSpecBlock(b *strings.Builder, muted lipgloss.Style, spec client.ScheduleSpec) {
	b.WriteString(muted.Render("trigger: ") + sanitizeTerminal(triggerSummary(spec)) + "\n")
	if spec.Prompt != "" {
		b.WriteString(muted.Render("prompt: ") + sanitizeTerminal(truncate(spec.Prompt, 120)) + "\n")
	}
	if spec.Selector.ProviderID != "" || spec.Selector.ModelID != "" {
		b.WriteString(muted.Render("selector: ") + sanitizeTerminal(spec.Selector.ProviderID+"/"+spec.Selector.ModelID) + "\n")
	}
	if spec.Profile != "" {
		b.WriteString(muted.Render("profile: ") + sanitizeTerminal(spec.Profile) + "\n")
	}
	if spec.Mode != "" {
		b.WriteString(muted.Render("mode: ") + sanitizeTerminal(spec.Mode) + "\n")
	}
	b.WriteString(muted.Render("mutating: ") + boolStr(spec.Mutating) +
		"  singleton: " + boolStr(spec.Singleton) +
		"  misfire: " + spec.Misfire +
		"  max_fires: " + strconv.Itoa(int(spec.MaxFires)) + "\n")
	if spec.Timezone != "" {
		b.WriteString(muted.Render("timezone: ") + sanitizeTerminal(spec.Timezone) + "\n")
	}
	if spec.FireTimeout > 0 {
		b.WriteString(muted.Render("fire_timeout: ") + spec.FireTimeout.String() + "\n")
	}
}

// renderScheduleFireLine renders one fire row for the inspect view's fire list.
// An in-flight fire (Stop empty, #386) renders the "in-flight" marker + its
// started/last-progress/deadline instants; a terminal fire renders its stop
// reason + error. The cursor marker (▶) is prepended when selected.
func renderScheduleFireLine(f client.ScheduleFire, selected bool) string {
	marker := "  "
	if selected {
		marker = "▶ "
	}
	stop := sanitizeTerminal(f.Stop)
	if stop == "" {
		stop = "in-flight"
	}
	line := marker + sanitizeTerminal(f.ID) +
		"  " + formatScheduleTime(f.FiredAt) +
		"  " + stop
	if f.Stop == "" {
		line += "  started " + formatScheduleTime(f.StartedAt) +
			"  last-progress " + formatScheduleTime(f.ProgressAt) +
			"  deadline " + formatScheduleTime(f.Deadline)
	} else if f.Err != "" {
		line += "  err: " + sanitizeTerminal(f.Err)
	}
	return line
}

// renderScheduleCreate renders the in-overlay Create form (Phase 3b, issue #236).
// The focused field is highlighted with the accent style; the mutating toggle
// shows y/n when focused. The footer hint advertises the keybindings.
func renderScheduleCreate(th theme.Theme, st scheduleState, hk helpKeys, _, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("create schedule") + "\n\n")
	f := st.form
	muted := th.Style("muted")
	fields := []struct {
		label string
		val   string
	}{
		{"name", f.name.View()},
		{"prompt", f.prompt.View()},
		{"trigger", f.trigger.View()},
	}
	for i, fld := range fields {
		marker := "  "
		if i == f.focusIdx {
			marker = "▶ "
		}
		line := marker + muted.Render(fld.label+": ") + fld.val
		if i == f.focusIdx {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
	// Mutating toggle (focusIdx == scheduleFormFieldCount).
	mutMarker := "  "
	if f.focusIdx == scheduleFormFieldCount {
		mutMarker = "▶ "
	}
	mutLine := mutMarker + muted.Render("mutating: ")
	if f.mutating {
		mutLine += "yes"
	} else {
		mutLine += "no"
	}
	if f.focusIdx == scheduleFormFieldCount {
		mutLine = th.Style("accent").Render(mutLine)
	}
	b.WriteString(mutLine + "\n")
	if st.actionErr != "" {
		// sanitizeTerminal for defense-in-depth parity with the panel sink — a
		// server-sourced actionErr (e.g. a rejected cron) could carry control runes.
		b.WriteString("\n" + th.Style("errorText").Render(sanitizeTerminal(st.actionErr)) + "\n")
	}
	// The enter (Choose) and esc (Close) chords read the LIVE keyMap markings;
	// tab/↑↓/y/n are BARE keys via msg.String (NOT keyMap bindings), so they
	// stay literal (issue #457).
	b.WriteString("\n" + muted.Render("tab/↑↓: next  "+hk.choose+": advance/submit  y/n: toggle mutating  "+hk.closeOnly+": back"))
	return b.String()
}

// triggerSummary renders a compact one-line summary of a schedule's trigger:
// "cron: <expr>" for a cron trigger, "one-shot: <time>" for a one-shot, or
// "one-shot: (fired)" for a one-shot whose NextFireAt is zero (it has fired or
// is exhausted).
func triggerSummary(spec client.ScheduleSpec) string {
	if spec.Trigger.Cron != "" {
		return "cron: " + spec.Trigger.Cron
	}
	if !spec.Trigger.OneShot.IsZero() {
		return "one-shot: " + formatScheduleTime(spec.Trigger.OneShot)
	}
	return "one-shot: (unset)"
}

// formatScheduleTime renders a compact absolute timestamp (zero → "—").
func formatScheduleTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04")
}

// boolStr renders a bool as "true"/"false".
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
