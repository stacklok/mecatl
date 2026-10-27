package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/schedparse"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/autocomplete"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

type scheduleView int

const (
	schedulePanel scheduleView = iota
	scheduleInspect
	scheduleDetails
	scheduleConfirm
	scheduleCreate
)

// scheduleState owns one open lifetime; raw names and fire IDs address the lists.
type scheduleState struct {
	now             func() time.Time
	deps            surfaceDeps
	lister          client.ScheduleLister
	transcriptWired bool

	view                 scheduleView
	loading              bool
	err                  error
	schedules, filtered  []client.Schedule
	filter               textinput.Model
	list, runs           bounded.List
	details              bounded.Viewport
	confirm, inspect     client.Schedule
	fires                []client.ScheduleFire
	firesLoading         bool
	firesErr, inspectErr error
	actionErr, notice    string
	form                 scheduleForm

	listRequest                uint64
	inspectRequest             uint64
	mutationRequest            uint64
	editorGeneration           uint64
	specPending                bool
	pendingName, pendingAction string
	intent                     surfaceIntent
	closed                     bool

	compact    bool                       // view cache: refreshed on every geometry offer
	offers     [scheduleCreate + 1][2]int // view cache: each view reveals after a hidden resize
	rowBudgets [scheduleInspect + 1]int   // view cache: capacity before bounded overflow indicators
	detailRows int                        // view cache: physical detail content rows
}

type scheduleForm struct {
	name, trigger textinput.Model
	prompt        textarea.Model
	timezone      autocomplete.Model
	mutating      bool
	focusIdx      int
	initialized   bool
	submitted     schedparse.Preview
}

const (
	scheduleName = iota
	schedulePrompt
	scheduleTiming
	scheduleTimezone
	scheduleReadOnly
	scheduleSubmit
	scheduleCancel
	scheduleControlCount
)

func (f scheduleForm) permissionMode() string {
	if f.mutating {
		return client.ModeDefaultString
	}
	return "plan"
}

// Read and mutation sequences are independent: navigation must not lose an owned
// mutation completion. Starting AND completing a mutation invalidate read snapshots.
type scheduleResultMsg struct {
	owner      *scheduleState
	request    uint64
	kind, name string
	result     tea.Msg
}
type scheduleTranscriptIntent struct{ sessionID string }

func (scheduleTranscriptIntent) isSurfaceIntent() {}
func (s *scheduleState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (m Model) openSchedule() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.modal != nil || m.deps.Sched == nil {
		return m, nil
	}
	ti := textinput.New()
	ti.Placeholder = "filter schedules…"
	s := &scheduleState{deps: (&m).surfaceDeps(), lister: m.deps.Sched, transcriptWired: m.deps.Transcript != nil, filter: ti, compact: true}
	m.modal = s
	m.prompt.Blur()
	return m, s.listCmd()
}
func (*scheduleState) modalPlacement() modalPlacement { return modalPlacementFill }
func (s *scheduleState) Close() {
	s.closed = true
	s.invalidateReads()
	s.filter.Blur()
	s.intent = nil
}
func (s *scheduleState) wrap(cmd tea.Cmd, kind, name string, request uint64) tea.Cmd {
	return func() tea.Msg {
		return scheduleResultMsg{owner: s, request: request, kind: kind, name: name, result: cmd()}
	}
}
func (s *scheduleState) listCmd() tea.Cmd {
	s.listRequest++
	s.loading, s.err = true, nil
	return s.wrap(client.ListSchedulesCmd(s.deps.ctx, s.lister), "list", "", s.listRequest)
}
func (s *scheduleState) inspectCmd() tea.Cmd {
	s.inspectRequest++
	s.specPending, s.firesLoading, s.firesErr, s.inspectErr = true, true, nil, nil
	name := s.inspect.Spec.Name
	return tea.Batch(
		s.wrap(client.GetScheduleCmd(s.deps.ctx, s.lister, name), "spec", name, s.inspectRequest),
		s.wrap(client.ListFiresCmd(s.deps.ctx, s.lister, name), "fires", name, s.inspectRequest),
	)
}
func (s *scheduleState) invalidateReads() {
	s.listRequest++
	s.inspectRequest++
	s.loading, s.specPending, s.firesLoading = false, false, false
}
func (s *scheduleState) mutate(cmd tea.Cmd, action, name string) tea.Cmd {
	if s.pendingAction != "" {
		return nil
	}
	s.invalidateReads()
	s.mutationRequest++
	s.pendingAction, s.pendingName = action, name
	s.actionErr, s.notice = "", ""
	return s.wrap(cmd, action, name, s.mutationRequest)
}
func (s *scheduleState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	r, ok := msg.(scheduleResultMsg)
	if !ok {
		return s.handleScheduleMessage(msg)
	}
	if r.owner != s || s.closed {
		return nil, true, false
	}
	switch r.kind {
	case "list":
		s.handleListResult(r)
	case "spec":
		s.handleSpecResult(r)
	case "fires":
		s.handleFiresResult(r)
	default:
		return s.handleMutationResult(r)
	}
	return nil, true, false
}

func (s *scheduleState) handleScheduleMessage(msg tea.Msg) (tea.Cmd, bool, bool) {
	switch input := msg.(type) {
	case scheduleEditorMsg:
		if input.owner != s || input.generation != s.editorGeneration || input.view != s.view || s.closed {
			return nil, true, false
		}
		msg = input.msg
	case tea.PasteMsg, cursor.BlinkMsg:
	default:
		return nil, false, false
	}
	if s.view == scheduleCreate {
		if s.compact || s.pendingAction != "" {
			return nil, true, false
		}
		return s.updateCreateField(msg), true, false
	}
	if s.view != schedulePanel || s.compact {
		return nil, false, false
	}
	before := s.filter.Value()
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	changed := s.filter.Value() != before
	if changed {
		s.syncFilter()
	}
	return s.editorCmd(cmd), changed || cmd != nil, false
}

func (s *scheduleState) handleListResult(r scheduleResultMsg) {
	if r.request != s.listRequest || !s.loading {
		return
	}
	result, ok := r.result.(client.SchedulesMsg)
	if !ok {
		return
	}
	s.loading, s.err = false, result.Err
	if result.Err == nil {
		s.schedules = result.Schedules
		s.syncFilter()
	}
}

func (s *scheduleState) validInspectResult(r scheduleResultMsg) bool {
	return r.request == s.inspectRequest && r.name == s.inspect.Spec.Name &&
		(s.view == scheduleInspect || s.view == scheduleDetails)
}

func (s *scheduleState) handleSpecResult(r scheduleResultMsg) {
	if !s.validInspectResult(r) {
		return
	}
	result, ok := r.result.(client.ScheduleMsg)
	if !ok || !s.specPending || result.Err == nil && result.Schedule.Spec.Name != r.name {
		return
	}
	s.specPending, s.inspectErr = false, result.Err
	if result.Err != nil {
		return
	}
	s.inspect = result.Schedule
	for i, row := range s.schedules {
		if row.Spec.Name == r.name {
			s.schedules[i] = result.Schedule
			break
		}
	}
	s.syncFilter()
}

func (s *scheduleState) handleFiresResult(r scheduleResultMsg) {
	if !s.validInspectResult(r) {
		return
	}
	result, ok := r.result.(client.ScheduleFiresMsg)
	if !ok || !s.firesLoading {
		return
	}
	for _, fire := range result.Fires {
		if fire.ScheduleName != r.name {
			return
		}
	}
	s.firesLoading, s.firesErr = false, result.Err
	if result.Err == nil {
		s.fires = result.Fires
		s.syncRuns()
	}
}

func (s *scheduleState) handleMutationResult(r scheduleResultMsg) (tea.Cmd, bool, bool) {
	if r.request != s.mutationRequest || r.kind != s.pendingAction || r.name != s.pendingName || s.pendingAction == "" {
		return nil, true, false
	}
	ok, err := s.mutationResultError(r)
	if !ok {
		return nil, true, false
	}
	s.pendingAction, s.pendingName = "", ""
	s.invalidateReads()
	if err != nil {
		s.actionErr = err.Error()
	} else {
		s.actionErr = ""
		if r.kind == "create" {
			s.form = scheduleForm{}
			if s.view == scheduleCreate {
				s.view = schedulePanel
			}
		}
	}
	cmds := []tea.Cmd{s.listCmd()}
	if s.view == scheduleInspect || s.view == scheduleDetails {
		cmds = append(cmds, s.inspectCmd())
	}
	return tea.Batch(cmds...), true, false
}

func (s *scheduleState) mutationResultError(r scheduleResultMsg) (bool, error) {
	switch result := r.result.(type) {
	case client.ScheduleMsg:
		if r.kind != "create" || result.Err == nil && result.Schedule.Spec.Name != r.name {
			return false, nil
		}
		if result.Err == nil {
			s.notice = "created " + r.name + " — next run (server): " + formatScheduleTime(result.Schedule.State.NextFireAt) + " UTC"
		}
		return true, result.Err
	case client.ScheduleActionMsg:
		if result.Name != r.name || result.Action != r.kind {
			return false, nil
		}
		if result.Err == nil && result.FireID != "" {
			s.notice = "fired " + result.Name + " — fire id: " + result.FireID
		}
		return true, result.Err
	default:
		return false, nil
	}
}
func (s *scheduleState) syncFilter() {
	s.filtered = filterSchedules(s.schedules, s.filter.Value())
	items := make([]bounded.ListItem, len(s.filtered))
	for i, row := range s.filtered {
		items[i] = bounded.ListItem{ID: row.Spec.Name, Text: scheduleRowText(row)}
	}
	s.list.SetItems(items)
}
func (s *scheduleState) syncRuns() {
	items := make([]bounded.ListItem, len(s.fires))
	for i, f := range s.fires {
		items[i] = bounded.ListItem{ID: f.ID, Text: renderScheduleFireLine(f)}
	}
	s.runs.SetItems(items)
}
func (s *scheduleState) selectedSchedule() (client.Schedule, bool) {
	for _, row := range s.filtered {
		if row.Spec.Name == s.list.CursorID() {
			return row, true
		}
	}
	return client.Schedule{}, false
}
func (s *scheduleState) openInspect() tea.Cmd {
	row, ok := s.selectedSchedule()
	if !ok || s.loading || s.err != nil {
		return nil
	}
	if row.Spec.Name != s.inspect.Spec.Name {
		s.runs = bounded.List{}
		s.details.Reset()
		s.fires = nil
	}
	s.inspect, s.view = row, scheduleInspect
	return s.inspectCmd()
}

// Details is a local action, chosen outside the captured navigation/activation
// bindings (and transcript's bare t). Its actual chord is rendered in the footer.
func (s *scheduleState) detailsKey() string {
	for _, chord := range []string{"d", "i", "ctrl+o", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12"} {
		if !s.reservedScheduleKey(chord, true) {
			return chord
		}
	}
	return ""
}

func (s *scheduleState) reservedScheduleKey(chord string, includeChoose bool) bool {
	bindings := []key.Binding{s.deps.keys.Close, s.deps.keys.Up, s.deps.keys.Down, s.deps.keys.ScrollU, s.deps.keys.ScrollD, s.deps.keys.ScrollTop, s.deps.keys.ScrollBottom}
	if includeChoose {
		bindings = append(bindings, s.deps.keys.Choose)
	}
	for _, binding := range bindings {
		if binding.Enabled() {
			for _, k := range binding.Keys() {
				if k == chord {
					return true
				}
			}
		}
	}
	return false
}

func (s *scheduleState) chooseKey() string {
	if !s.deps.keys.Choose.Enabled() {
		return ""
	}
	for _, chord := range s.deps.keys.Choose.Keys() {
		if !s.reservedScheduleKey(chord, false) {
			if chord == firstKey(s.deps.keys.Choose, "") {
				return s.deps.marks.choose
			}
			return chord
		}
	}
	return ""
}
func (s *scheduleState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if s.compact {
		return nil, true, key.Matches(msg, s.deps.keys.Close)
	}
	switch s.view {
	case scheduleCreate:
		return s.createKey(msg), true, false
	case scheduleConfirm:
		return s.confirmKey(msg)
	case schedulePanel:
		if s.filter.Focused() {
			return s.filterKey(msg)
		}
	}
	return s.activeViewKey(msg)
}

func (s *scheduleState) confirmKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		s.view = schedulePanel
	case key.Matches(msg, s.deps.keys.Choose) && s.pendingAction == "":
		s.view = schedulePanel
		return s.mutate(client.DeleteScheduleCmd(s.deps.ctx, s.lister, s.confirm.Spec.Name), "deleted", s.confirm.Spec.Name), true, false
	}
	return nil, true, false
}

func (s *scheduleState) filterKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if key.Matches(msg, s.deps.keys.Close, s.deps.keys.Choose) {
		s.filter.Blur()
		return nil, true, false
	}
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	s.syncFilter()
	return s.editorCmd(cmd), true, false
}

func (s *scheduleState) activeViewKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	move, nav := boundedNavigationMove(msg, s.deps.keys)
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		return s.closeViewKey()
	case nav:
		s.moveActiveView(move)
	case key.Matches(msg, s.deps.keys.Choose):
		return s.chooseActiveView()
	default:
		return s.activeViewAction(msg)
	}
	return nil, true, false
}

func (s *scheduleState) closeViewKey() (tea.Cmd, bool, bool) {
	switch s.view {
	case scheduleDetails:
		s.view = scheduleInspect
	case scheduleInspect:
		s.view = schedulePanel
		s.inspectRequest++
		s.specPending, s.firesLoading = false, false
	default:
		if s.filter.Value() == "" {
			return nil, true, true
		}
		s.filter.SetValue("")
		s.syncFilter()
	}
	return nil, true, false
}

func (s *scheduleState) moveActiveView(move bounded.Move) {
	switch s.view {
	case schedulePanel:
		s.list.Move(move)
	case scheduleInspect:
		s.runs.Move(move)
	case scheduleDetails:
		s.details.Move(move, s.detailRows)
	}
}

func (s *scheduleState) chooseActiveView() (tea.Cmd, bool, bool) {
	switch s.view {
	case schedulePanel:
		return s.openInspect(), true, false
	case scheduleInspect:
		s.jumpTranscript()
	}
	return nil, true, false
}

func (s *scheduleState) activeViewAction(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	switch s.view {
	case scheduleInspect:
		if chord := s.detailsKey(); chord != "" && msg.String() == chord {
			s.view = scheduleDetails
		} else if msg.String() == "t" {
			s.jumpTranscript()
		}
	case schedulePanel:
		return s.panelAction(msg), true, false
	}
	return nil, true, false
}
func (s *scheduleState) panelAction(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "/" {
		return s.filter.Focus()
	}
	if s.pendingAction != "" {
		return nil
	}
	if msg.String() == "c" {
		s.openCreate()
		return nil
	}
	row, ok := s.selectedSchedule()
	if !ok || s.loading || s.err != nil {
		return nil
	}
	name := row.Spec.Name
	switch msg.String() {
	case "p":
		return s.mutate(client.PauseScheduleCmd(s.deps.ctx, s.lister, name), "paused", name)
	case "r":
		return s.mutate(client.ResumeScheduleCmd(s.deps.ctx, s.lister, name), "resumed", name)
	case "f":
		return s.mutate(client.FireNowCmd(s.deps.ctx, s.lister, name), "fired", name)
	case "d":
		s.confirm, s.view = row, scheduleConfirm
	}
	return nil
}
func (s *scheduleState) jumpTranscript() {
	if !s.transcriptWired {
		s.notice = "transcript inspection unavailable"
		return
	}
	if !s.firesLoading && s.firesErr == nil {
		for _, fire := range s.fires {
			if fire.ID == s.runs.CursorID() && fire.SessionID != "" {
				s.intent = scheduleTranscriptIntent{sessionID: fire.SessionID}
				return
			}
		}
	}
	s.notice = "fire has no session id"
}
func (s *scheduleState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if move, ok := boundedWheelMove(msg); ok && !s.compact {
		switch s.view {
		case schedulePanel:
			s.list.Scroll(move)
		case scheduleInspect:
			s.runs.Scroll(move)
		case scheduleDetails:
			s.details.Move(move, s.detailRows)
		}
	}
	return nil, true
}
func (s *scheduleState) Render(width, height int) (string, []ClickableRegion) {
	reveal := s.offers[s.view] != [2]int{width, height}
	s.offers[s.view], s.compact = [2]int{width, height}, true
	if width <= 0 || height <= 0 {
		return "", nil
	}
	s.filter.SetWidth(max(1, width-2))
	var body string
	var regions []ClickableRegion
	switch s.view {
	case scheduleConfirm, scheduleCreate:
		body, regions = s.renderOverlay(width, height)
	default:
		body, regions = s.renderSurface(width, height, reveal)
	}
	return lipgloss.NewStyle().Height(height).Render(body), regions
}

func (s *scheduleState) renderCompact(width int) (string, []ClickableRegion) {
	return renderSkillsCompact(s.deps.theme, s.deps.marks, width), nil
}

func (s *scheduleState) renderOverlay(width, height int) (string, []ClickableRegion) {
	th, hk := s.deps.theme, s.deps.marks
	if s.view == scheduleCreate {
		return s.renderCreate(width, height)
	}
	body := renderScheduleConfirm(th, *s, hk, width, height)
	lines := strings.Split(ansi.Hardwrap(body, width, true), "\n")
	if width < 16 || len(lines) > height {
		return s.renderCompact(width)
	}
	s.compact = false
	return strings.Join(lines, "\n"), nil
}

func (s *scheduleState) renderSurface(width, height int, reveal bool) (string, []ClickableRegion) {
	th := s.deps.theme
	title, footer := s.surfaceChrome()
	prefix := s.surfacePrefix(th, title, width)
	prefix = s.appendSurfaceStatus(th, prefix, width)
	foot := skillsTextLines(th.Style("muted"), footer, width)
	rows := height - len(prefix) - len(foot)
	if width < 4 || rows < 1 {
		return s.renderCompact(width)
	}
	if s.view == scheduleDetails {
		return s.renderDetails(th, prefix, foot, width, rows)
	}
	return s.renderList(th, title, prefix, foot, width, rows, reveal)
}

func (s *scheduleState) surfaceChrome() (string, string) {
	closeHint := s.deps.marks.closeOnly
	choose := s.chooseKey()
	if s.view == schedulePanel {
		if s.filter.Focused() {
			hints := []string{}
			if s.deps.keys.Choose.Enabled() {
				hints = append(hints, s.deps.marks.choose+": done")
			}
			hints = append(hints, closeHint+": done")
			return "schedules", strings.Join(hints, "  ")
		}
		hints := []string{}
		if choose != "" {
			hints = append(hints, choose+": runs")
		}
		for _, hint := range []struct{ key, label string }{{"c", "create"}, {"p", "pause"}, {"r", "resume"}, {"f", "fire-now"}, {"d", "delete"}, {"/", "filter"}} {
			if !s.reservedScheduleKey(hint.key, true) {
				hints = append(hints, hint.key+": "+hint.label)
			}
		}
		hints = append(hints, closeHint+": close")
		return "schedules", strings.Join(hints, "  ")
	}
	title := "schedule — " + terminaltext.SanitizeSingleLine(s.inspect.Spec.Name)
	footer := closeHint + ": back"
	if s.view == scheduleInspect {
		if chord := s.detailsKey(); chord != "" {
			footer = chord + ": details  " + footer
		}
		if s.transcriptWired {
			hints := []string{}
			if choose != "" {
				hints = append(hints, choose)
			}
			if !s.reservedScheduleKey("t", true) {
				hints = append(hints, "t")
			}
			if len(hints) > 0 {
				footer = strings.Join(hints, "/") + ": open transcript  " + footer
			}
		}
		return title, footer
	}
	return title, s.deps.marks.scroll + ": scroll  " + footer
}

func (s *scheduleState) surfacePrefix(th theme.Theme, title string, width int) []string {
	prefix := skillsTextLines(th.Style("title"), title, width)
	switch s.view {
	case schedulePanel:
		prefix = append(prefix, skillsTextLines(th.Style("muted"), scheduleChatHint, width)...)
		if s.filter.Focused() {
			return append(prefix, ansi.Cut(s.filter.View(), 0, width))
		}
		if s.filter.Value() != "" {
			return append(prefix, skillsTextLines(th.Style("muted"), "filter: "+terminaltext.SanitizeSingleLine(s.filter.Value()), width)...)
		}
	case scheduleInspect:
		return append(prefix, skillsTextLines(th.Style("muted"), scheduleLifecycle(s.inspect), width)...)
	}
	return prefix
}

func (s *scheduleState) appendSurfaceStatus(th theme.Theme, prefix []string, width int) []string {
	if s.view != schedulePanel && s.inspectErr != nil {
		prefix = append(prefix, skillsTextLines(th.Style("errorText"), "could not inspect: "+terminaltext.Sanitize(s.inspectErr.Error()), width)...)
	}
	if s.actionErr != "" {
		prefix = append(prefix, skillsTextLines(th.Style("errorText"), "action failed: "+terminaltext.Sanitize(s.actionErr), width)...)
	}
	if s.notice != "" {
		prefix = append(prefix, skillsTextLines(th.Style("muted"), terminaltext.Sanitize(s.notice), width)...)
	}
	if s.pendingAction != "" {
		prefix = append(prefix, skillsTextLines(th.Style("muted"), "operation pending (closing does not undo it)", width)...)
	}
	return prefix
}

func (s *scheduleState) renderDetails(th theme.Theme, prefix, foot []string, width, rows int) (string, []ClickableRegion) {
	if rows < 2 {
		return s.renderCompact(width)
	}
	lines := skillsTextLines(th.Style("muted"), scheduleDetailText(th, s.inspect), width)
	s.details.SetGeometry(width, rows-1, 0, bounded.Clip)
	s.detailRows = len(lines)
	view := s.details.View(lines)
	prefix = append(prefix, ansi.Cut(fmt.Sprintf("↑ %d · ↓ %d more", view.Above, view.Below), 0, width))
	prefix = append(prefix, view.Rows...)
	s.compact = false
	return strings.Join(append(prefix, foot...), "\n"), nil
}

func (s *scheduleState) renderList(th theme.Theme, title string, prefix, foot []string, width, rows int, reveal bool) (string, []ClickableRegion) {
	list, loading, empty, err := s.activeScheduleList()
	reveal = reveal || s.rowBudgets[s.view] != rows
	s.rowBudgets[s.view] = rows
	list.SetGeometry(width, rows, 1, bounded.Wrap)
	state := scheduleListState(list, loading, err, empty)
	if state != "" {
		lines := skillsTextLines(th.Style("muted"), state, width)
		if len(lines) > rows {
			return s.renderCompact(width)
		}
		prefix = append(prefix, lines...)
	} else {
		prefix = renderScheduleListRows(th, prefix, title, list, width, rows, reveal)
	}
	s.compact = false
	return strings.Join(append(prefix, foot...), "\n"), nil
}

func (s *scheduleState) activeScheduleList() (*bounded.List, bool, string, error) {
	if s.view == scheduleInspect {
		s.syncRuns()
		empty := "no runs recorded"
		if s.inspect.State.LastFireSessionID == "pending" && s.inspect.State.LastFireStartedAt.IsZero() {
			empty = "waiting to start (session pending)"
		}
		return &s.runs, s.firesLoading, empty, s.firesErr
	}
	if s.filter.Value() != "" {
		return &s.list, s.loading, "no matches — clear filter to see all", s.err
	}
	return &s.list, s.loading, "no schedules yet — press c to create one", s.err
}

func scheduleListState(list *bounded.List, loading bool, err error, empty string) string {
	switch {
	case loading:
		return "loading…"
	case err != nil:
		return "could not list: " + terminaltext.Sanitize(err.Error())
	case list.CursorID() == "":
		return empty
	default:
		return ""
	}
}

func renderScheduleListRows(th theme.Theme, prefix []string, title string, list *bounded.List, width, rows int, reveal bool) []string {
	view := list.ViewWithIndicators(rows, reveal || list.RevealPending())
	if view.Above > 0 && rows > 1 {
		prefix = append(prefix, ansi.Cut(fmt.Sprintf("↑ %d more", view.Above), 0, width))
	}
	if rows == 1 && (view.Above > 0 || view.Below > 0) {
		prefix[0] = ansi.Cut(fmt.Sprintf("↑ %d ↓ %d · %s", view.Above, view.Below, title), 0, width)
	}
	for _, row := range view.Rows {
		presented := presentListRow(row, th.Style("spinner"), th.Style("muted"))
		prefix = append(prefix, ansi.Cut(presented.Style.Render(presented.Text), 0, width))
	}
	if view.Below > 0 && rows > 1 {
		prefix = append(prefix, ansi.Cut(fmt.Sprintf("↓ %d more", view.Below), 0, width))
	}
	return prefix
}
func scheduleLifecycle(s client.Schedule) string {
	state := "enabled"
	if !s.State.Enabled {
		state = "paused"
	}
	if s.State.DeletionPending {
		state += " · cleanup pending · d: retry delete from inventory"
	}
	if !s.State.LastFireStartedAt.IsZero() {
		state += " · in-flight"
		if !s.State.FireDeadline.IsZero() && !time.Now().Before(s.State.FireDeadline) {
			state += " (overdue/degraded; termination unconfirmed)"
		}
	} else if s.State.LastFireSessionID == "pending" {
		state += " · claimed (session pending; termination unconfirmed)"
	}
	return state
}
func scheduleRowText(s client.Schedule) string {
	return terminaltext.SanitizeSingleLine(s.Spec.Name) + "  " + terminaltext.SanitizeSingleLine(triggerSummary(s.Spec)) + "  " + scheduleLifecycle(s) + "  next:" + formatScheduleTime(s.State.NextFireAt) + "  last:" + formatScheduleTime(s.State.LastFireAt) + "  fires:" + strconv.Itoa(int(s.State.FireCount))
}
func scheduleDetailText(th theme.Theme, s client.Schedule) string {
	var b strings.Builder
	muted := th.Style("muted")
	renderScheduleSpecBlock(&b, muted, s.Spec)
	b.WriteString("\nstate\n" + scheduleLifecycle(s) + "\n")
	b.WriteString("enabled: " + boolStr(s.State.Enabled) + "  deletion_pending: " + boolStr(s.State.DeletionPending) + "  runs: " + strconv.Itoa(int(s.State.FireCount)) + "\n")
	if s.State.DeletionPending {
		b.WriteString("placement cleanup is pending — go back and press d to retry delete\n")
	}
	b.WriteString("next run: " + formatScheduleTime(s.State.NextFireAt) + "\nlast run: " + formatScheduleTime(s.State.LastFireAt) + "\n")
	if s.State.LastFireSessionID != "" {
		b.WriteString("last run session: " + terminaltext.Sanitize(s.State.LastFireSessionID) + "\n")
	}
	if !s.State.LastFireStartedAt.IsZero() || !s.State.LastFireProgressAt.IsZero() || !s.State.FireDeadline.IsZero() {
		b.WriteString("running: started " + formatScheduleTime(s.State.LastFireStartedAt) + "  last update " + formatScheduleTime(s.State.LastFireProgressAt) + "  deadline " + formatScheduleTime(s.State.FireDeadline) + "\n")
	}
	if s.State.LastFireSessionID == "pending" && s.State.LastFireStartedAt.IsZero() {
		b.WriteString("pending admission: ownership/termination unconfirmed; elapsed timeout does not prove termination\n")
	}
	return b.String()
}
func filterSchedules(scheds []client.Schedule, q string) []client.Schedule {
	if q == "" {
		return scheds
	}
	needle := strings.ToLower(q)
	var out []client.Schedule
	for _, s := range scheds {
		if strings.Contains(strings.ToLower(s.Spec.Name), needle) || strings.Contains(strings.ToLower(triggerSummary(s.Spec)), needle) {
			out = append(out, s)
		}
	}
	return out
}
func renderScheduleConfirm(th theme.Theme, st scheduleState, hk helpKeys, _, _ int) string {
	return th.Style("title").Render("delete schedule") + "\n\ndelete " + terminaltext.Sanitize(st.confirm.Spec.Name) + "? This cannot be undone.\n\n" + hk.choose + ": delete  " + hk.closeOnly + ": back"
}
func renderScheduleSpecBlock(b *strings.Builder, muted lipgloss.Style, spec client.ScheduleSpec) {
	b.WriteString(muted.Render("trigger: ") + terminaltext.Sanitize(triggerSummary(spec)) + "\n")
	if spec.Prompt != "" {
		b.WriteString(muted.Render("prompt: ") + terminaltext.Sanitize(truncate(spec.Prompt, 120)) + "\n")
	}
	if spec.Selector.ProviderID != "" || spec.Selector.ModelID != "" {
		b.WriteString(muted.Render("model: ") + terminaltext.Sanitize(spec.Selector.ProviderID+"/"+spec.Selector.ModelID) + "\n")
	}
	if spec.Profile != "" {
		b.WriteString(muted.Render("workspace access: ") + scheduleWorkspaceAccessText(spec.Profile) + "\n")
	}
	if spec.Mode != "" {
		b.WriteString(muted.Render("permission mode: ") + terminaltext.Sanitize(spec.Mode) + "\n")
	}
	b.WriteString(muted.Render("may change workspace: ") + boolStr(spec.Mutating) + "  run overlap: " + scheduleOverlapText(spec.Singleton) + "\n")
	b.WriteString(muted.Render("missed run: ") + scheduleMisfireText(spec.Misfire) + "  run limit: " + scheduleRunLimitText(spec) + "\n")
	if spec.Timezone != "" {
		b.WriteString(muted.Render("timezone: ") + terminaltext.Sanitize(spec.Timezone) + "\n")
	}
	if spec.FireTimeout > 0 {
		b.WriteString(muted.Render("run timeout: ") + spec.FireTimeout.String() + "\n")
	}
}
func scheduleWorkspaceAccessText(profile string) string {
	if profile == "no-fs" {
		return "no workspace files (no-fs)"
	}
	return terminaltext.Sanitize(profile)
}
func scheduleOverlapText(singleton bool) string {
	if singleton {
		return "skip overlapping runs"
	}
	return "allowed"
}
func scheduleMisfireText(policy string) string {
	if policy == "skip" {
		return "skip and wait for the next one"
	}
	return "run once now"
}
func scheduleRunLimitText(spec client.ScheduleSpec) string {
	if !spec.Trigger.OneShot.IsZero() {
		return "one run"
	}
	if spec.MaxFires == 0 {
		return "unlimited"
	}
	return strconv.Itoa(int(spec.MaxFires))
}
func renderScheduleFireLine(f client.ScheduleFire) string {
	stop := terminaltext.Sanitize(f.Stop)
	if stop == "" {
		stop = "in-flight"
		if !f.Deadline.IsZero() && !time.Now().Before(f.Deadline) {
			stop = "in-flight (overdue/degraded; termination unconfirmed)"
		}
	}
	line := terminaltext.Sanitize(f.ID) + "  " + formatScheduleTime(f.FiredAt) + "  " + stop
	if f.Stop == "" {
		line += "  started " + formatScheduleTime(f.StartedAt) + "  last update " + formatScheduleTime(f.ProgressAt) + "  deadline " + formatScheduleTime(f.Deadline)
	} else if f.Err != "" {
		line += "  err: " + terminaltext.Sanitize(f.Err)
	}
	return line
}
func triggerSummary(spec client.ScheduleSpec) string {
	if spec.Trigger.Cron != "" {
		return "cron: " + spec.Trigger.Cron
	}
	if !spec.Trigger.OneShot.IsZero() {
		return "one-shot: " + formatScheduleTime(spec.Trigger.OneShot)
	}
	return "one-shot: (unset)"
}
func formatScheduleTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("2006-01-02 15:04")
}
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
