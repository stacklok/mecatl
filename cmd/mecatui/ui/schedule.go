package ui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// schedule.go is the /schedule overlay (issue #234, Phase 3a) — a selecting
// overlay mirroring /worktrees' cursor+filter+confirm shape, with an added
// read-only inspect sub-view (full spec + state + fires) and per-row action keys
// (pause/resume/fire-now/delete). Phase 3a scope: list/inspect/pause/resume/
// fire-now/delete; no in-overlay Create form (author via CLI or settings.yaml).
//
// UNLIKE /worktrees and /models, the filter input is NOT focused on open — the
// panel's own single-letter action keys (p/r/f/d) would otherwise be
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
}

// openSchedule opens the picker and fires the ListSchedules RPC. Only callable
// while idle and when a schedule lister is wired; returns the model unchanged
// otherwise. The result arrives as a client.SchedulesMsg handled in
// updateScheduleMsg.
func (m Model) openSchedule() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sched == nil {
		return m, nil
	}
	m.ta.Blur()
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
	cmd := m.ta.Focus()
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
// enter or "t" jumps to the fire's transcript (issue #235), reusing the
// /sessions handoff (switchToSession). A fire is a top-level session with a
// `sched--` family id and always carries a terminal Stop, so the state gate
// (isSessionOpenable) naturally passes — no state derivation from fire.Stop is
// needed. Jump guards: Replayer nil → no-op; fireCursor out of range or the
// fire's SessionID empty → set a statusMsg, stay in inspect.
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

// jumpToFireTranscript opens a read-only transcript of the cursor fire's
// session, reusing the /sessions handoff. It clears the schedule overlay state
// (the transcript view owns the screen) and calls switchToSession with a
// SessionListItem carrying just the fire's session id (fire records are
// terminal, so the state gate passes). Guards: no Replayer → no-op; cursor out
// of range or empty SessionID → statusMsg, stay in inspect.
func (m Model) jumpToFireTranscript() (tea.Model, tea.Cmd, bool) {
	if m.deps.Replayer == nil {
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
	item := client.SessionListItem{ID: fire.SessionID}
	// Clear the schedule overlay state — the transcript view owns the screen now.
	m.schedule = scheduleState{}
	return m.switchToSession(item)
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

// renderScheduleOverlay draws the overlay, dispatching on the view. replayerWired
// gates the jump-to-fire footer hint in the inspect sub-view (#235).
func renderScheduleOverlay(th theme.Theme, st scheduleState, caps client.Capabilities, replayerWired bool, width, height int) string {
	switch st.view {
	case scheduleConfirm:
		return renderScheduleConfirm(th, st, width, height)
	case scheduleInspect:
		return renderScheduleInspect(th, st, replayerWired, width, height)
	default:
		return renderSchedulePanel(th, st, caps, width, height)
	}
}

// renderSchedulePanel renders the schedule list card.
func renderSchedulePanel(th theme.Theme, st scheduleState, _ client.Capabilities, _, _ int) string {
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
		b.WriteString("\n" + th.Style("muted").Render("esc: close"))
		return b.String()
	}
	if st.actionErr != "" {
		b.WriteString(th.Style("errorText").Render("action failed: "+sanitizeTerminal(st.actionErr)) + "\n\n")
	}
	if len(st.filtered) == 0 {
		if st.filter.Value() != "" {
			b.WriteString(th.Style("muted").Render("no matches — clear filter to see all"))
		} else {
			b.WriteString(th.Style("muted").Render("no schedules found (create via `mecated schedule create` or settings.yaml)"))
		}
		b.WriteString("\n" + th.Style("muted").Render("esc: close"))
		return b.String()
	}
	for i, s := range st.filtered {
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		state := "enabled"
		if !s.State.Enabled {
			state = "paused"
		}
		line := marker + sanitizeTerminal(s.Spec.Name) +
			"  " + sanitizeTerminal(triggerSummary(s.Spec)) +
			"  " + state +
			"  next:" + formatScheduleTime(s.State.NextFireAt) +
			"  last:" + formatScheduleTime(s.State.LastFireAt) +
			"  fires:" + strconv.Itoa(int(s.State.FireCount))
		if i == st.cursor {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render("enter: inspect  p: pause  r: resume  f: fire-now  d: delete  /: filter  esc: close"))
	return b.String()
}

// renderScheduleConfirm renders the delete-confirmation card.
func renderScheduleConfirm(th theme.Theme, st scheduleState, _, _ int) string {
	var b strings.Builder
	b.WriteString(th.Style("title").Render("delete schedule") + "\n\n")
	b.WriteString("delete " + th.Style("accent").Render(sanitizeTerminal(st.confirm.Spec.Name)) + "?\n")
	b.WriteString("\n" + th.Style("muted").Render("enter: delete  esc: back"))
	return b.String()
}

// renderScheduleInspect renders the full-spec + state + fires card. The fire
// list is cursor-navigable (#235): the cursor row is highlighted with the
// accent style + a ▶ marker, and the footer hint advertises the jump-to-fire
// action only when a replayer is wired.
func renderScheduleInspect(th theme.Theme, st scheduleState, replayerWired bool, _, _ int) string {
	s := st.inspect
	var b strings.Builder
	b.WriteString(th.Style("title").Render("schedule — "+sanitizeTerminal(s.Spec.Name)) + "\n\n")
	muted := th.Style("muted")
	spec := s.Spec
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
	if spec.Workspace != "" {
		b.WriteString(muted.Render("workspace: ") + sanitizeTerminal(spec.Workspace) + "\n")
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
	b.WriteString("\n" + muted.Render("state") + "\n")
	b.WriteString(muted.Render("enabled: ") + boolStr(s.State.Enabled) +
		"  fire_count: " + strconv.Itoa(int(s.State.FireCount)) + "\n")
	b.WriteString(muted.Render("next_fire: ") + formatScheduleTime(s.State.NextFireAt) + "\n")
	b.WriteString(muted.Render("last_fire: ") + formatScheduleTime(s.State.LastFireAt) + "\n")
	if s.State.LastFireSessionID != "" {
		b.WriteString(muted.Render("last_fire_session: ") + sanitizeTerminal(s.State.LastFireSessionID) + "\n")
	}
	b.WriteString("\n" + muted.Render("fires") + "\n")
	if st.firesLoading {
		b.WriteString(muted.Render("loading…"))
	} else if st.firesErr != nil {
		b.WriteString(th.Style("errorText").Render("could not list fires: " + sanitizeTerminal(st.firesErr.Error())))
	} else if len(st.fires) == 0 {
		b.WriteString(muted.Render("no fires recorded"))
	} else {
		for i, f := range st.fires {
			marker := "  "
			if i == st.fireCursor {
				marker = "▶ "
			}
			line := marker + sanitizeTerminal(f.ID) +
				"  " + formatScheduleTime(f.FiredAt) +
				"  " + sanitizeTerminal(f.Stop)
			if f.Err != "" {
				line += "  err: " + sanitizeTerminal(f.Err)
			}
			if i == st.fireCursor {
				line = th.Style("accent").Render(line)
			}
			b.WriteString(line + "\n")
		}
	}
	hint := "esc: back"
	if replayerWired {
		hint = "↑↓: select fire  enter/t: open transcript  esc: back"
	}
	b.WriteString("\n" + muted.Render(hint))
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
