package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/timezones"
	"github.com/stacklok/mecatl/cmd/mecatui/schedparse"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/autocomplete"
)

// Widget commands include private clipboard receipts as well as cursor ticks.
// Tag their provenance instead of claiming arbitrary messages before root reducers.
type scheduleEditorMsg struct {
	owner      *scheduleState
	generation uint64
	view       scheduleView
	msg        tea.Msg
}

func (scheduleEditorMsg) isSurfaceInputMsg() {}

func (s *scheduleState) editorCmd(cmd tea.Cmd) tea.Cmd {
	return (scheduleEditorMsg{owner: s, generation: s.editorGeneration, view: s.view}).wrap(cmd)
}

func (input scheduleEditorMsg) wrap(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for i, child := range batch {
				batch[i] = input.wrap(child)
			}
			return batch
		}
		input.msg = msg
		return input
	}
}

func (s *scheduleState) scheduleNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *scheduleState) openCreate() {
	if !s.form.initialized {
		s.actionErr = ""
		input := func(placeholder string) textinput.Model {
			ti := textinput.New()
			ti.Placeholder = placeholder
			return ti
		}
		prompt := textarea.New()
		prompt.Placeholder = "Task to run on each fire"
		prompt.ShowLineNumbers = false
		prompt.Prompt = "  "
		prompt.CharLimit, prompt.MaxHeight, prompt.MaxWidth = 0, 0, 0
		prompt.SetVirtualCursor(true)
		options := make([]autocomplete.Option, 0)
		for _, name := range timezones.Names() {
			options = append(options, autocomplete.Option{Value: name, Label: name, Search: strings.ReplaceAll(name, "_", " ")})
		}
		zone := autocomplete.New(options, autocomplete.KeyMap{Up: s.deps.keys.Up, Down: s.deps.keys.Down, Choose: s.deps.keys.Choose, Close: s.deps.keys.Close})
		zone.SetValue("UTC")
		s.form = scheduleForm{name: input("Schedule name"), prompt: prompt, trigger: input("every day at 9am"), timezone: zone, initialized: true}
	}
	s.view = scheduleCreate
	s.focusField()
}

func (s *scheduleState) focusField() tea.Cmd {
	s.editorGeneration++
	f := &s.form
	f.name.Blur()
	f.prompt.Blur()
	f.trigger.Blur()
	f.timezone.Blur()
	switch f.focusIdx {
	case scheduleName:
		return s.editorCmd(f.name.Focus())
	case schedulePrompt:
		return s.editorCmd(f.prompt.Focus())
	case scheduleTiming:
		return s.editorCmd(f.trigger.Focus())
	case scheduleTimezone:
		return s.editorCmd(f.timezone.Focus())
	}
	return nil
}

func (s *scheduleState) leaveCreate() {
	s.form.name.Blur()
	s.form.prompt.Blur()
	s.form.trigger.Blur()
	s.form.timezone.Blur()
	s.form = scheduleForm{}
	s.editorGeneration++
	s.actionErr = ""
	s.view = schedulePanel
}

func (s *scheduleState) createKey(msg tea.KeyPressMsg) tea.Cmd {
	// Back remains available while pending, but cannot undo the operation.
	if s.pendingAction != "" {
		if key.Matches(msg, s.deps.keys.Close) {
			s.leaveCreate()
		}
		return nil
	}
	// Suggestions own their action keys before form Back or focus traversal.
	if s.form.focusIdx == scheduleTimezone && s.form.timezone.Open() && key.Matches(msg, s.deps.keys.Up, s.deps.keys.Down, s.deps.keys.Choose, s.deps.keys.Close) {
		cmd, _ := s.form.timezone.Update(msg)
		return cmd
	}
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		s.leaveCreate()
	case key.Matches(msg, s.deps.keys.Choose):
		switch s.form.focusIdx {
		case scheduleSubmit:
			return s.submitCreate()
		case scheduleCancel:
			s.leaveCreate()
		case schedulePrompt:
			// A remapped activation key is not a form-wide submit shortcut.
			if msg.Code == tea.KeyEnter {
				return s.updateCreateField(msg)
			}
		case scheduleName, scheduleTiming:
			s.form.focusIdx++
			return s.focusField()
		}
	case msg.String() == "tab":
		s.form.focusIdx = (s.form.focusIdx + 1) % scheduleControlCount
		return s.focusField()
	case msg.String() == "shift+tab":
		s.form.focusIdx = (s.form.focusIdx + scheduleControlCount - 1) % scheduleControlCount
		return s.focusField()
	case s.form.focusIdx == scheduleReadOnly && msg.String() == "space":
		s.form.mutating = !s.form.mutating
	default:
		return s.updateCreateField(msg)
	}
	return nil
}

func (s *scheduleState) updateCreateField(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch s.form.focusIdx {
	case scheduleName:
		s.form.name, cmd = s.form.name.Update(msg)
	case schedulePrompt:
		s.form.prompt, cmd = s.form.prompt.Update(msg)
	case scheduleTiming:
		s.form.trigger, cmd = s.form.trigger.Update(msg)
	case scheduleTimezone:
		cmd, _ = s.form.timezone.Update(msg)
	}
	return s.editorCmd(cmd)
}

// Both rendering and submission use this validation, with a fresh clock at Create.
func (s *scheduleState) validateCreate() (schedparse.Preview, error) {
	f := &s.form
	if strings.TrimSpace(f.name.Value()) == "" {
		return schedparse.Preview{}, fmt.Errorf("name: name is required")
	}
	if strings.TrimSpace(f.prompt.Value()) == "" {
		return schedparse.Preview{}, fmt.Errorf("prompt: prompt is required")
	}
	return schedparse.Validate(f.trigger.Value(), f.timezone.Value(), s.scheduleNow())
}

func (s *scheduleState) submitCreate() tea.Cmd {
	if s.pendingAction != "" || s.compact {
		return nil
	}
	preview, err := s.validateCreate()
	if err != nil {
		s.actionErr = err.Error()
		return nil
	}
	f := &s.form
	f.submitted = preview
	spec := client.ScheduleSpec{Name: f.name.Value(), Prompt: f.prompt.Value(), Trigger: client.ScheduleTrigger{Cron: preview.Cron, OneShot: preview.OneShot}, Timezone: preview.Timezone, Mode: f.permissionMode(), Mutating: f.mutating, Singleton: true}
	return s.mutate(client.CreateScheduleCmd(s.deps.ctx, s.lister, spec), "create", spec.Name)
}

func (s *scheduleState) createTraversalAvailable(msg tea.KeyPressMsg) bool {
	if key.Matches(msg, s.deps.keys.Close, s.deps.keys.Choose) {
		return false
	}
	return s.form.focusIdx != scheduleTimezone || !s.form.timezone.Open() || !key.Matches(msg, s.deps.keys.Up, s.deps.keys.Down)
}

func (s *scheduleState) createHints() string {
	if s.pendingAction != "" {
		return s.deps.marks.closeOnly + ": back (operation continues)"
	}
	hints := []string{}
	if s.createTraversalAvailable(tea.KeyPressMsg{Code: tea.KeyTab}) {
		hints = append(hints, "tab: next")
	}
	if s.createTraversalAvailable(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}) {
		hints = append(hints, "shift+tab: previous")
	}
	hints = append(hints, s.createFieldHints()...)
	back := ": back"
	if s.form.focusIdx == scheduleTimezone && s.form.timezone.Open() {
		back = ": dismiss suggestions; again: back"
	}
	hints = append(hints, s.deps.marks.closeOnly+back)
	return strings.Join(hints, "  ")
}

func (s *scheduleState) createFieldHints() []string {
	hints := []string{}
	choose := s.chooseKey()
	switch s.form.focusIdx {
	case schedulePrompt:
		if !key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, s.deps.keys.Close) {
			hints = append(hints, "enter: newline")
		}
		if !key.Matches(tea.KeyPressMsg{Code: tea.KeyUp}, s.deps.keys.Close, s.deps.keys.Choose) && !key.Matches(tea.KeyPressMsg{Code: tea.KeyDown}, s.deps.keys.Close, s.deps.keys.Choose) {
			hints = append(hints, "arrows: edit")
		}
	case scheduleTimezone:
		return s.createTimezoneHints()
	case scheduleReadOnly:
		if !key.Matches(tea.KeyPressMsg{Code: tea.KeySpace}, s.deps.keys.Close, s.deps.keys.Choose) {
			hints = append(hints, "space: toggle read-only")
		}
	case scheduleSubmit, scheduleCancel:
		if choose != "" {
			hints = append(hints, choose+": activate button")
		}
	default:
		if choose != "" {
			hints = append(hints, choose+": next field")
		}
	}
	return hints
}

func (s *scheduleState) createTimezoneHints() []string {
	if !s.form.timezone.Open() {
		return nil
	}
	hints := []string{}
	upBlocked := s.deps.keys.Close.Enabled() && slices.Contains(s.deps.keys.Close.Keys(), firstKey(s.deps.keys.Up, ""))
	downBlocked := s.deps.keys.Close.Enabled() && slices.Contains(s.deps.keys.Close.Keys(), firstKey(s.deps.keys.Down, ""))
	if s.deps.keys.Up.Enabled() && s.deps.keys.Down.Enabled() && !upBlocked && !downBlocked {
		hints = append(hints, s.deps.marks.navUp+"/"+s.deps.marks.navDown+": suggestions")
	}
	if choose := s.chooseKey(); choose != "" {
		hints = append(hints, choose+": accept timezone")
	}
	return hints
}

func scheduleCreateLines(width int, text string) []string {
	return strings.Split(ansi.Hardwrap(text, width, true), "\n")
}

func scheduleCreateText(width int, text string, rows int) []string {
	lines := scheduleCreateLines(width, terminaltext.SanitizeSingleLine(text))
	if len(lines) > rows {
		lines = lines[:rows]
		lines[rows-1] = ansi.Truncate(lines[rows-1], width-1, "") + "…"
	}
	return lines
}

func (s *scheduleState) createLabel(idx int, text string) string {
	if s.form.focusIdx == idx {
		return s.deps.theme.Style("spinner").Render("▶ " + text)
	}
	return "  " + text
}

func (s *scheduleState) createFields(width int) (top, middle []string, promptVisible bool) {
	th, f := s.deps.theme, &s.form
	f.name.SetWidth(width - 12)
	f.name.SetCursor(f.name.Position())
	f.trigger.SetWidth(width - 14)
	f.trigger.SetCursor(f.trigger.Position())
	f.prompt.SetWidth(width)
	f.timezone.SetGeometry(width-16, 3)
	fields := []string{"Name", "Prompt", "Timing", "Timezone", "Read-only", "Create", "Cancel"}
	top = scheduleCreateLines(width, th.Style("title").Render(fmt.Sprintf("create schedule — %s (%d/%d)", fields[f.focusIdx], f.focusIdx+1, scheduleControlCount)))
	// Keep the focused editor in view; the final controls show the draft for review.
	switch f.focusIdx {
	case scheduleName:
		top = append(top, s.createLabel(scheduleName, "name: ")+f.name.View())
	case schedulePrompt:
		top = append(top, s.createLabel(schedulePrompt, "prompt (multiline task):"))
		promptVisible = true
	case scheduleTiming:
		top = append(top, s.createLabel(scheduleTiming, "timing: ")+f.trigger.View())
	case scheduleTimezone:
		top = append(top, s.createLabel(scheduleTimezone, "timezone: ")+f.timezone.InputView())
	default:
		top = append(top, "  name: "+f.name.View(), "  prompt: (preview)")
		promptVisible = true
		middle = append(middle, "  timing: "+f.trigger.View(), "  timezone: "+f.timezone.InputView())
	}
	return top, middle, promptVisible
}

func (s *scheduleState) createStatus(width int) []string {
	f := &s.form
	preview, err := s.validateCreate()
	if s.pendingAction == "create" {
		preview, err = f.submitted, nil
	}
	bottom := []string{}
	if err != nil {
		field := "Create disabled"
		if e, ok := err.(*schedparse.ValidationError); ok {
			field += " — " + e.Field
		}
		bottom = append(bottom, scheduleCreateText(width, field+": "+err.Error(), 2)...)
	} else {
		trigger := preview.Cron
		if trigger == "" {
			trigger = "instant " + preview.OneShot.Format(time.RFC3339)
		}
		bottom = append(bottom, scheduleCreateText(width, "Estimate: "+trigger, 1)...)
		bottom = append(bottom, scheduleCreateText(width, "Next: "+preview.Next.Format("2006-01-02 15:04:05 -07:00")+" · "+preview.Timezone, 1)...)
	}
	bottom = append(bottom, scheduleCreateLines(width, s.createGuidance())...)
	if s.actionErr != "" {
		bottom = append(bottom, scheduleCreateText(width, "Last attempt: "+s.actionErr, 1)...)
	}
	checked := "[x]"
	if f.mutating {
		checked = "[ ]"
	}
	button := "[Create]"
	if err != nil || s.pendingAction != "" {
		button = "[Create disabled]"
	}
	bottom = append(bottom, scheduleCreateLines(width, s.createLabel(scheduleReadOnly, checked+" Read-only")+"  permission mode: "+f.permissionMode()+"  "+s.createLabel(scheduleSubmit, button)+"  "+s.createLabel(scheduleCancel, "[Cancel]"))...)
	return bottom
}

func (s *scheduleState) renderCreate(width, height int) (string, []ClickableRegion) {
	if width < 40 {
		return s.renderCompact(width)
	}
	f := &s.form
	top, middle, promptVisible := s.createFields(width)
	bottom := s.createStatus(width)
	footer := scheduleCreateLines(width, s.createHints())
	// Budget wrapped chrome before allocating the focused field viewport.
	available := height - len(top) - len(middle) - len(bottom) - len(footer)
	suggestionRows := 0
	if f.focusIdx == scheduleTimezone {
		suggestionRows = 3
	}
	promptRows := 0
	if promptVisible {
		promptRows = 2
	}
	if available < promptRows+suggestionRows {
		f.timezone.SetGeometry(0, 0)
		return s.renderCompact(width)
	}
	rows := top
	if f.focusIdx == schedulePrompt {
		f.prompt.SetHeight(min(6, available))
		rows = append(rows, strings.Split(f.prompt.View(), "\n")...)
	} else if promptVisible {
		// Review the task, not the editor's scrolled cursor window (which may be blank).
		summary := scheduleCreateText(width, strings.ReplaceAll(f.prompt.Value(), "\n", " ↵ "), 2)
		rows = append(rows, summary...)
		for range 2 - len(summary) {
			rows = append(rows, "")
		}
	}
	rows = append(rows, middle...)
	rows = append(rows, s.createSuggestions(suggestionRows)...)
	rows = append(rows, bottom...)
	for len(rows)+len(footer) < height {
		rows = append(rows, "")
	}
	rows = append(rows, footer...)
	if len(rows) > height {
		return s.renderCompact(width)
	}
	s.compact = false
	return strings.Join(rows, "\n"), nil
}

func (s *scheduleState) createSuggestions(rows int) []string {
	if rows == 0 {
		return nil
	}
	th := s.deps.theme
	matches := s.form.timezone.View()
	suggestions := []string{}
	if matches.Above > 0 {
		suggestions = append(suggestions, fmt.Sprintf("↑ %d more", matches.Above))
	}
	for _, row := range matches.Rows {
		presented := presentListRow(row, th.Style("spinner"), th.Style("muted"))
		suggestions = append(suggestions, presented.Style.Render(presented.Text))
	}
	if matches.Below > 0 {
		suggestions = append(suggestions, fmt.Sprintf("↓ %d more", matches.Below))
	}
	for len(suggestions) < rows {
		suggestions = append(suggestions, "")
	}
	return suggestions
}

func (s *scheduleState) createGuidance() string {
	if s.pendingAction != "" {
		return "operation pending (closing does not undo it)"
	}
	switch s.form.focusIdx {
	case scheduleName:
		return "Name: identify this task. Other fields: Prompt, Timing, Timezone, Read-only."
	case schedulePrompt:
		return "Task runs in separate sessions; results in schedule run history, not this chat."
	case scheduleTiming:
		return "Timing: every 30 minutes; every day at 9am; five-field cron / @daily.\nAlso: every weekday at 9am; tomorrow at 9am; in 2 hours; next monday 9am."
	case scheduleTimezone:
		return "Timezone: search a city or type an exact IANA identifier; choose a match."
	case scheduleReadOnly:
		return "Unchecked: normal permissions, not automatic approval.\nModel: server default; read-only may use the configured plan model."
	case scheduleSubmit:
		return "Review before Create. Separate run sessions; results in schedule run history."
	case scheduleCancel:
		return scheduleChatHint
	}
	return ""
}

const scheduleChatHint = "Chat can manage schedules too: ‘Schedule a read-only review every weekday at 9am America/Los_Angeles.’"
