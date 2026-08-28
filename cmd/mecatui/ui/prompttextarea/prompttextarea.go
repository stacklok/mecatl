// Package prompttextarea owns the upstream textarea behind Mecatl's prompt-editor policy.
// It is deliberately narrow: it is not a general textarea wrapper.
package prompttextarea

import (
	"image/color"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// Config contains the fixed prompt-editor configuration supplied at construction.
type Config struct {
	Placeholder string
	SelectAll   key.Binding
}

// Position is a logical textarea position suitable for render-cache keys.
type Position struct {
	Row int
	Col int
}

// LineInfo is the cursor's soft-wrap location suitable for render-cache keys.
type LineInfo struct {
	RowOffset    int
	ColumnOffset int
}

// Editor is Mecatl's prompt editor. The upstream model is intentionally private so
// prompt mutation and selection policy cannot be bypassed by ui callers.
type Editor struct {
	model textarea.Model
}

// New builds the fixed Mecatl prompt editor.
func New(cfg Config) Editor {
	model := textarea.New()
	model.Prompt = ""
	model.ShowLineNumbers = false
	model.Placeholder = cfg.Placeholder
	model.SetHeight(3)
	model.KeyMap.SelectAll = cfg.SelectAll
	// Mecatl owns clipboard transport, so CopySelection is intercepted by ui.
	model.KeyMap.CopySelection = key.NewBinding()
	model.Focus()
	return Editor{model: model}
}

// UpdateUserInput applies an upstream keyboard or paste operation, including its
// native selection behavior.
func (e *Editor) UpdateUserInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	e.model, cmd = e.model.Update(msg)
	return cmd
}

// InsertText replaces any selected text with user-provided text.
func (e *Editor) InsertText(s string) {
	e.model.DeleteSelection()
	e.model.InsertString(s)
}

// PasteText applies a user-provided paste with the same selection behavior as typing.
func (e *Editor) PasteText(s string) {
	e.InsertText(s)
}

// DeleteSelection deletes selected text, if any.
func (e *Editor) DeleteSelection() {
	e.model.DeleteSelection()
}

// InsertNewline replaces any selected text with a user-provided newline.
func (e *Editor) InsertNewline() {
	e.model.DeleteSelection()
	e.model.InsertRune('\n')
}

// Rewrite replaces prompt text for a host-owned operation and clears selection first.
func (e *Editor) Rewrite(s string) {
	e.model.ClearSelection()
	e.model.SetValue(s)
}

// Reset clears prompt text for a host-owned operation and clears selection first.
func (e *Editor) Reset() {
	e.model.ClearSelection()
	e.model.Reset()
}

// BeginSelection starts a mouse selection at editor-relative coordinates.
func (e *Editor) BeginSelection(x, y int) { e.model.BeginSelection(x, y) }

// ExtendSelection moves a mouse selection endpoint to editor-relative coordinates.
func (e *Editor) ExtendSelection(x, y int) { e.model.ExtendSelection(x, y) }

// EndSelection completes the current mouse selection.
func (e *Editor) EndSelection() { e.model.EndSelection() }

// SelectAll selects all prompt text.
func (e *Editor) SelectAll() { e.model.SelectAll() }

// ClearSelection retains prompt text and clears the selected range.
func (e *Editor) ClearSelection() { e.model.ClearSelection() }

// HasSelection reports whether the prompt has a non-empty selected range.
func (e Editor) HasSelection() bool { return e.model.HasSelection() }

// SelectedText returns the currently selected prompt text.
func (e Editor) SelectedText() string { return e.model.SelectedText() }

// Selection returns the normalized selected range for a render-cache key.
func (e Editor) Selection() (from, to Position, ok bool) {
	fromRaw, toRaw, ok := e.model.Selection()
	return Position{Row: fromRaw.Row, Col: fromRaw.Col}, Position{Row: toRaw.Row, Col: toRaw.Col}, ok
}

// Focus gives keyboard focus to the prompt and returns the upstream focus command.
func (e *Editor) Focus() tea.Cmd { return e.model.Focus() }

// Blur removes keyboard focus from the prompt.
func (e *Editor) Blur() { e.model.Blur() }

// Focused reports whether the prompt has keyboard focus.
func (e Editor) Focused() bool { return e.model.Focused() }

// Value returns the prompt text.
func (e Editor) Value() string { return e.model.Value() }

// Placeholder returns the configured empty-prompt hint.
func (e Editor) Placeholder() string { return e.model.Placeholder }

// Empty reports whether the prompt contains no text.
func (e Editor) Empty() bool { return e.model.Value() == "" }

// SetWidth updates the prompt's layout width.
func (e *Editor) SetWidth(n int) { e.model.SetWidth(n) }

// Width returns the prompt's layout width.
func (e Editor) Width() int { return e.model.Width() }

// Height returns the prompt's layout height.
func (e Editor) Height() int { return e.model.Height() }

// Line returns the cursor's logical line.
func (e Editor) Line() int { return e.model.Line() }

// Column returns the cursor's logical column.
func (e Editor) Column() int { return e.model.Column() }

// ScrollYOffset returns the prompt viewport's vertical offset.
func (e Editor) ScrollYOffset() int { return e.model.ScrollYOffset() }

// View renders the prompt.
func (e Editor) View() string { return e.model.View() }

// LineInfo returns the cursor's soft-wrap location.
func (e Editor) LineInfo() LineInfo {
	info := e.model.LineInfo()
	return LineInfo{RowOffset: info.RowOffset, ColumnOffset: info.ColumnOffset}
}

// SetColors applies Mecatl's mode-dependent prompt colors without exposing
// upstream style state to the ui package.
func (e *Editor) SetColors(accent, background, text color.Color) {
	styles := e.model.Styles()
	base := textarea.DefaultDarkStyles()
	styles.Focused.Placeholder = base.Focused.Placeholder.Foreground(accent)
	styles.Cursor.Color = accent
	for _, state := range []*textarea.StyleState{&styles.Focused, &styles.Blurred} {
		state.Base = state.Base.Background(background)
		state.Text = state.Text.Background(background).Foreground(text)
		state.CursorLine = state.CursorLine.Background(background).Foreground(text)
		state.EndOfBuffer = state.EndOfBuffer.Background(background)
		state.Placeholder = state.Placeholder.Background(background)
	}
	e.model.SetStyles(styles)
}
