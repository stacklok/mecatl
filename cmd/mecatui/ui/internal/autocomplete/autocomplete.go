// Package autocomplete provides a small searchable single-line selection control.
package autocomplete

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

// Option is one caller-owned choice. Value identifies the choice, Label is its
// presentation, and Search is additional text that should find it.
type Option struct {
	Value, Label, Search string
}

// KeyMap supplies the active surface's bindings. Tab is deliberately not an
// autocomplete action: callers retain it for focus navigation.
type KeyMap struct {
	Up, Down, Choose, Close key.Binding
}

// Model combines a Bubbles text input with a bounded match list. It owns query,
// selection, acceptance, and geometry; callers own options and presentation.
type Model struct {
	input     textinput.Model
	options   []Option
	matches   []Option
	list      bounded.List
	keys      KeyMap
	accepted  string
	acceptFor string
	open      bool
	dismissed bool
	width     int
	height    int
}

// New constructs a control with the supplied stable options and active bindings.
func New(options []Option, keys KeyMap) Model {
	input := textinput.New()
	// The built-in suggestion behavior is prefix-only and binds Tab. This control
	// owns substring matching and leaves Tab to the enclosing form.
	input.ShowSuggestions = false
	input.KeyMap.AcceptSuggestion.SetEnabled(false)
	input.KeyMap.NextSuggestion.SetEnabled(false)
	input.KeyMap.PrevSuggestion.SetEnabled(false)
	m := Model{input: input, keys: keys}
	m.SetOptions(options)
	return m
}

// SetStyles configures the caller-owned input presentation.
func (m *Model) SetStyles(styles textinput.Styles) { m.input.SetStyles(styles) }

// InputView returns the Bubbles input presentation.
func (m Model) InputView() string { return m.input.View() }

// Query returns the current draft query.
func (m Model) Query() string { return m.input.Value() }

// Value returns the accepted value, or the exact stable value matching a fully
// typed identifier. A highlighted row alone never produces a value.
func (m Model) Value() string {
	if m.accepted != "" && m.input.Value() == m.acceptFor && m.optionByValue(m.accepted).Value != "" {
		return m.accepted
	}
	return m.exactValue(m.input.Value())
}

// Focus focuses the input and opens its matches when geometry permits.
func (m *Model) Focus() tea.Cmd {
	m.dismissed = false
	cmd := m.input.Focus()
	m.sync()
	return cmd
}

// Search starts a fresh query without discarding an accepted selection. Typing
// replaces that selection; an untouched empty search still retains its value.
func (m *Model) Search() {
	if value := m.Value(); value != "" {
		m.accepted, m.acceptFor = value, ""
		m.input.Placeholder = value
		m.input.SetValue("")
		m.sync()
	}
}

// Blur hides suggestions without changing the query or accepted value.
func (m *Model) Blur() {
	m.input.Blur()
	m.open = false
}

// SetValue selects an option by stable value. It returns false for an unknown value.
func (m *Model) SetValue(value string) bool {
	option := m.optionByValue(value)
	if option.Value == "" {
		return false
	}
	m.accept(option)
	return true
}

// SetOptions replaces caller-owned choices while preserving a compatible accepted
// value and list cursor identity.
func (m *Model) SetOptions(options []Option) {
	m.options = append([]Option(nil), options...)
	m.sync()
}

// SetGeometry configures the visible match-list area. Invalid geometry suppresses
// input and actions until a later usable offer, preserving the draft unchanged.
func (m *Model) SetGeometry(width, height int) {
	m.width, m.height = width, height
	m.input.SetWidth(max(1, width))
	m.input.SetCursor(m.input.Position())
	m.sync()
}

// Open reports whether suggestions are currently visible and actionable.
func (m Model) Open() bool { return m.open && m.list.Valid() }

// View returns the current bounded suggestion projection for caller rendering.
func (m *Model) View() bounded.ListView {
	if !m.Open() {
		return bounded.ListView{}
	}
	return m.list.ViewWithIndicators(m.height, m.list.RevealPending())
}

// Update routes input, cursor, paste, and active action messages. handled is false
// for unclaimed keys such as Tab so the enclosing form can route them normally.
func (m *Model) Update(msg tea.Msg) (cmd tea.Cmd, handled bool) {
	if !m.list.Valid() {
		return nil, true
	}
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if m.Open() {
			switch {
			case key.Matches(keyMsg, m.keys.Close):
				m.open, m.dismissed = false, true
				return nil, true
			case key.Matches(keyMsg, m.keys.Up):
				m.list.Move(bounded.LineUp)
				return nil, true
			case key.Matches(keyMsg, m.keys.Down):
				m.list.Move(bounded.LineDown)
				return nil, true
			case key.Matches(keyMsg, m.keys.Choose):
				if len(m.matches) > 0 {
					m.accept(m.matches[m.list.Cursor()])
				}
				return nil, true
			}
		}
		// The surrounding form owns focus traversal, Back, and any action when no
		// suggestion is visible. In particular, Tab cannot accept a suggestion.
		if keyMsg.Code == tea.KeyTab || key.Matches(keyMsg, m.keys.Up, m.keys.Down, m.keys.Choose, m.keys.Close) {
			return nil, false
		}
	}

	before := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.input.Placeholder = ""
		m.accepted, m.acceptFor, m.dismissed = "", "", false
		m.sync()
	}
	return cmd, true
}

func (m *Model) accept(option Option) {
	m.accepted, m.acceptFor = option.Value, option.Label
	m.input.SetValue(option.Label)
	m.input.CursorEnd()
	m.open, m.dismissed = false, true
	m.sync()
	for i, match := range m.matches {
		if match.Value == option.Value {
			m.list.SetCursor(i)
			break
		}
	}
}

func (m *Model) sync() {
	query := normalized(m.input.Value())
	m.matches = m.matches[:0]
	items := make([]bounded.ListItem, 0, len(m.options))
	for _, option := range m.options {
		if query != "" && !strings.Contains(normalized(option.Value+" "+option.Label+" "+option.Search), query) {
			continue
		}
		m.matches = append(m.matches, option)
		items = append(items, bounded.ListItem{ID: option.Value, Text: terminaltext.SanitizeSingleLine(option.Label)})
	}
	m.list.SetItems(items)
	m.list.SetGeometry(m.width, m.height, 1, bounded.Clip)
	m.open = m.input.Focused() && !m.dismissed && len(m.matches) > 0 && m.list.Valid()
}

func (m Model) exactValue(query string) string {
	for _, option := range m.options {
		if query == option.Value {
			return option.Value
		}
	}
	return ""
}

func (m Model) optionByValue(value string) Option {
	for _, option := range m.options {
		if option.Value == value {
			return option
		}
	}
	return Option{}
}

func normalized(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(s), "_", " ")), " ")
}
