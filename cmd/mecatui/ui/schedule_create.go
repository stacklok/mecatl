package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
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
	s.actionErr = ""
	keys := huh.NewDefaultKeyMap()
	// The surface owns traversal and actions; fields only edit their values.
	for _, binding := range []*key.Binding{&keys.Input.Next, &keys.Input.Prev, &keys.Input.Submit, &keys.Text.Next, &keys.Text.Prev, &keys.Text.Submit, &keys.Text.Editor} {
		binding.SetEnabled(false)
	}
	keys.Text.NewLine.SetKeys("enter", "ctrl+m", "ctrl+j")
	name := huh.NewInput().Title("Name: ").Inline(true).Placeholder("Schedule name")
	prompt := huh.NewText().Title("Prompt").Placeholder("Task to run on each fire").Lines(3).CharLimit(0).ExternalEditor(false)
	trigger := huh.NewInput().Title("When (e.g. every day at 9am): ").Inline(true)
	styles := huh.ThemeBase(true)
	fieldTheme := huh.ThemeFunc(func(bool) *huh.Styles { return styles })
	for _, field := range []huh.Field{name, prompt, trigger} {
		field.WithKeyMap(keys).WithTheme(fieldTheme)
		field.Init()
	}
	options := make([]autocomplete.Option, 0)
	for _, name := range timezones.Names() {
		options = append(options, autocomplete.Option{Value: name, Label: name})
	}
	zone := autocomplete.New(options, autocomplete.KeyMap{Up: s.deps.keys.Up, Down: s.deps.keys.Down, Choose: s.deps.keys.Choose, Close: s.deps.keys.Close})
	zone.SetValue("UTC")
	s.form = scheduleForm{name: name, prompt: prompt, trigger: trigger, styles: styles, timezone: zone}
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
		f.timezone.Search()
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

func (s *scheduleState) createActivation(msg tea.KeyPressMsg) bool {
	if key.Matches(msg, s.deps.keys.Choose) {
		return true
	}
	return (s.form.focusIdx == scheduleCancel || s.form.focusIdx == scheduleSubmit) &&
		msg.String() == "space" && key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, s.deps.keys.Choose) &&
		!key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, s.deps.keys.Close)
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
		if !key.Matches(msg, s.deps.keys.Close, s.deps.keys.Up, s.deps.keys.Down) && key.Matches(msg, s.deps.keys.Choose) && s.form.timezone.Value() != "" {
			s.form.focusIdx = scheduleReadOnly
			return s.focusField()
		}
		return s.editorCmd(cmd)
	}
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		s.leaveCreate()
	case s.createActivation(msg):
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
	if press, ok := msg.(tea.KeyPressMsg); ok && s.form.focusIdx == schedulePrompt {
		switch press.String() {
		case "enter", "ctrl+m", "ctrl+j":
			// Huh exposes no MaxHeight setting. Insert through the same editor's
			// paste path to avoid Bubbles' legacy 99-line newline guard.
			msg = tea.PasteMsg{Content: "\n"}
		}
	}
	if paste, ok := msg.(tea.PasteMsg); ok {
		paste.Content = strings.ReplaceAll(paste.Content, "\r\n", "\n")
		msg = paste
	}
	var cmd tea.Cmd
	switch s.form.focusIdx {
	case scheduleName:
		_, cmd = s.form.name.Update(msg)
	case schedulePrompt:
		_, cmd = s.form.prompt.Update(msg)
	case scheduleTiming:
		_, cmd = s.form.trigger.Update(msg)
	case scheduleTimezone:
		cmd, _ = s.form.timezone.Update(msg)
	}
	return s.editorCmd(cmd)
}

// Both rendering and submission use this validation, with a fresh clock at Create.
func (s *scheduleState) validateCreate() (schedparse.Preview, error) {
	f := &s.form
	if strings.TrimSpace(f.name.GetValue().(string)) == "" {
		return schedparse.Preview{}, fmt.Errorf("name: name is required")
	}
	if strings.TrimSpace(f.prompt.GetValue().(string)) == "" {
		return schedparse.Preview{}, fmt.Errorf("prompt: prompt is required")
	}
	return schedparse.Validate(f.trigger.GetValue().(string), f.timezone.Value(), s.scheduleNow())
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
	spec := client.ScheduleSpec{Name: f.name.GetValue().(string), Prompt: f.prompt.GetValue().(string), Trigger: client.ScheduleTrigger{Cron: preview.Cron, OneShot: preview.OneShot}, Timezone: preview.Timezone, Mode: f.permissionMode(), Mutating: f.mutating, Singleton: true}
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
			if key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, s.deps.keys.Choose) && !key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, s.deps.keys.Close) && !key.Matches(tea.KeyPressMsg{Code: tea.KeySpace}, s.deps.keys.Close) && choose != "space" {
				choose += "/space"
			}
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

func (s *scheduleState) createFields(width, height, suggestionRows int) (fields [][]string) {
	th, f := s.deps.theme, &s.form
	text := lipgloss.NewStyle().Foreground(lipgloss.Color(th.Palette.Text))
	muted := th.Style("muted")
	for _, styles := range []*huh.FieldStyles{&f.styles.Focused, &f.styles.Blurred} {
		styles.Title = text.Bold(true)
		styles.TextInput.Text = text
		styles.TextInput.Placeholder = muted
		styles.TextInput.Prompt = text
		styles.TextInput.Cursor = text.Background(lipgloss.Color(th.Palette.Text))
	}
	f.styles.Focused.Base = f.styles.Focused.Base.BorderForeground(lipgloss.Color(th.Palette.BorderActive))
	for _, field := range []huh.Field{f.name, f.prompt, f.trigger} {
		field.WithWidth(width)
	}
	f.prompt.Lines(2)
	if height >= 28 {
		f.prompt.Lines(5)
	} else if height >= 18 {
		f.prompt.Lines(3)
	}
	zoneStyles := textinput.DefaultStyles(true)
	zoneStyles.Focused.Text, zoneStyles.Blurred.Text = text, text
	zoneStyles.Focused.Placeholder, zoneStyles.Blurred.Placeholder = text, text
	zoneStyles.Cursor.Color = lipgloss.Color(th.Palette.Text)
	f.timezone.SetStyles(zoneStyles)
	f.timezone.SetGeometry(width-5, suggestionRows)
	name, timing := f.name.View(), f.trigger.View()
	// Huh v2.0.3 only applies Input text styles to its focused Bubbles input.
	// Project inactive values with the same theme rather than its fixed palette.
	if f.focusIdx != scheduleName {
		name = text.Render("  Name: " + terminaltext.SanitizeSingleLine(f.name.GetValue().(string)))
	}
	if f.focusIdx != scheduleTiming {
		timing = text.Render("  When (e.g. every day at 9am): " + terminaltext.SanitizeSingleLine(f.trigger.GetValue().(string)))
	}
	fields = append(fields, []string{ansi.Truncate(name, width, "…")}, strings.Split(f.prompt.View(), "\n"), []string{ansi.Truncate(timing, width, "…")})
	zone := []string{s.createLabel(scheduleTimezone, "Timezone"), "  " + f.timezone.InputView()}
	if f.focusIdx == scheduleTimezone {
		if height >= 24 {
			zone = append(zone, "")
		}
		zone = append(zone, s.createSuggestions(suggestionRows)...)
		if height >= 24 {
			zone = append(zone, "")
		}
	}
	fields = append(fields, zone)
	checked := "[x]"
	if f.mutating {
		checked = "[ ]"
	}
	mode := f.permissionMode()
	if mode == "" {
		mode = "server default"
	}
	fields = append(fields, scheduleCreateLines(width, s.createLabel(scheduleReadOnly, checked+" Read-only")+" permission mode: "+mode))
	return fields
}

func (s *scheduleState) createStatus(width int) []string {
	f := &s.form
	preview, err := s.validateCreate()
	if s.pendingAction == "create" {
		preview, err = f.submitted, nil
	}
	button := "[Create]"
	if err != nil || s.pendingAction != "" {
		button = "[Create disabled]"
	}
	bottom := scheduleCreateLines(width, s.createLabel(scheduleCancel, "[Cancel]")+s.createLabel(scheduleSubmit, button))
	bottom = append(bottom, "")
	var information []string
	if err != nil {
		field := "Create disabled"
		if e, ok := err.(*schedparse.ValidationError); ok {
			field += " — " + e.Field
		}
		information = append(information, scheduleCreateText(width, field+": "+err.Error(), 2)...)
	} else {
		trigger := preview.Cron
		if trigger == "" {
			trigger = "instant " + preview.OneShot.Format(time.RFC3339)
		}
		information = append(information, scheduleCreateText(width, "Estimate: "+trigger, 1)...)
		information = append(information, scheduleCreateText(width, "Next: "+preview.Next.Format("2006-01-02 15:04:05 -07:00")+" · "+preview.Timezone, 1)...)
	}
	information = append(information, scheduleCreateLines(width, s.createGuidance())...)
	for _, line := range information {
		bottom = append(bottom, s.deps.theme.Style("muted").Render(line))
	}
	if s.actionErr != "" {
		for _, line := range scheduleCreateText(width, "Last attempt: "+s.actionErr, 1) {
			bottom = append(bottom, s.deps.theme.Style("errorText").Render(line))
		}
	}
	return bottom
}

func (s *scheduleState) renderCreate(width, height int) (string, []ClickableRegion) {
	if width < 40 {
		return s.renderCompact(width)
	}
	f := &s.form
	names := []string{"Name", "Prompt", "When", "Timezone", "Read-only", "Cancel", "Create"}
	top := scheduleCreateLines(width, s.deps.theme.Style("title").Render(fmt.Sprintf("create schedule — %s (%d/%d)", names[f.focusIdx], f.focusIdx+1, scheduleControlCount)))
	if height >= 18 {
		top = append(top, "")
	}
	// Restore suggestion availability before deriving hints after a compact render.
	f.timezone.SetGeometry(width-5, 6)
	bottom := s.createStatus(width)
	footer := scheduleCreateLines(width, s.createHints())
	available := height - len(top) - len(bottom) - len(footer)
	if available < 3 {
		f.timezone.SetGeometry(0, 0)
		return s.renderCompact(width)
	}
	listOverhead := 2 // Timezone label and input.
	if height >= 24 {
		listOverhead += 2 // Space above and below suggestions.
	}
	fields := s.createFields(width, height, min(6, max(3, available-listOverhead)))
	var content []string
	start, end := 0, 0
	for i, field := range fields {
		if i > 0 && height >= 24 {
			content = append(content, "")
		}
		if i == f.focusIdx {
			start, end = len(content), len(content)+len(field)
		}
		content = append(content, field...)
	}
	rows := min(available, len(content))
	if f.focusIdx >= scheduleCancel {
		start, end = len(content)-len(fields[scheduleReadOnly]), len(content)
	}
	if end-start > rows {
		f.timezone.SetGeometry(0, 0)
		return s.renderCompact(width)
	}
	f.scroll = min(f.scroll, max(0, len(content)-rows))
	if start < f.scroll {
		f.scroll = start
	} else if end > f.scroll+rows {
		f.scroll = min(start, end-rows)
	}
	// Scroll the persistent field stack minimally; actions stay adjacent beneath it.
	body := append(top, content[f.scroll:f.scroll+rows]...)
	body = append(body, bottom...)
	body = append(body, footer...)
	s.compact = false
	return strings.Join(body, "\n"), nil
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
		return "Unchecked: server default permission mode; deployment policy applies.\nModel: server default; read-only may use the configured plan model."
	case scheduleSubmit:
		return "Review before Create. Separate run sessions; results in schedule run history."
	case scheduleCancel:
		return scheduleChatHint
	}
	return ""
}

const scheduleChatHint = "Chat can manage schedules too: ‘Schedule a read-only review every weekday at 9am America/Los_Angeles.’"
