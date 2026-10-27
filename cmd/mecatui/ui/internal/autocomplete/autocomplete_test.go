package autocomplete

import (
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func TestSearchRetainsSelectionUntilTypedAndPreservesInvalidDraft(t *testing.T) {
	m := testModel()
	m.SetValue("America/Los_Angeles")
	m.Search()
	focus(&m)
	if m.Query() != "" || m.Value() != "America/Los_Angeles" || !m.Open() {
		t.Fatal("fresh search did not retain selected value separately")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.Value() != "America/Los_Angeles" {
		t.Fatal("accepting untouched search lost selected identity")
	}
	m.Search()
	m.Focus()
	paste(&m, "New York")
	if m.Value() != "" || m.Query() != "New York" || !m.Open() {
		t.Fatal("direct paste appended to accepted label or retained a hidden value")
	}
	m.Blur()
	m.Search()
	m.Focus()
	if m.Query() != "New York" || m.Value() != "" {
		t.Fatal("reentry discarded an unaccepted query")
	}
}

func TestQueryMatchesCityAndIdentifierWithoutAcceptingHighlight(t *testing.T) {
	m := testModel()
	focus(&m)
	paste(&m, "Los_Angeles")

	if got, want := m.matches[0].Value, "America/Los_Angeles"; got != want {
		t.Fatalf("city match = %q, want %q", got, want)
	}
	if got := m.Value(); got != "" {
		t.Fatalf("highlighted value = %q, want empty until accepted", got)
	}
	if _, handled := m.Update(tea.KeyPressMsg{Code: tea.KeyDown}); !handled {
		t.Fatal("down was not handled")
	}
	if got := m.Value(); got != "" {
		t.Fatalf("navigated value = %q, want empty until accepted", got)
	}
	if _, handled := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}); !handled {
		t.Fatal("remapped choose was not handled")
	}
	if got, want := m.Value(), "America/Los_Angeles"; got != want {
		t.Fatalf("accepted value = %q, want %q", got, want)
	}
}

func TestTypedIdentifierRequiresExactCaseOrAcceptance(t *testing.T) {
	m := testModel()
	focus(&m)
	paste(&m, "america/new_york")
	if m.Value() != "" || !m.Open() || m.Query() != "america/new_york" {
		t.Fatal("case-folded search must not implicitly validate the typed identifier")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.Value() != "America/New_York" || m.Query() != "America/New York" {
		t.Fatal("explicit acceptance did not use the exact caller-owned value and label")
	}

	m = testModel()
	focus(&m)
	paste(&m, "America/Los_Angeles")
	if got, want := m.Value(), "America/Los_Angeles"; got != want {
		t.Fatalf("typed identifier = %q, want %q", got, want)
	}
}

func TestEditingInvalidatesAcceptedValueAndTabDoesNotAccept(t *testing.T) {
	m := testModel()
	focus(&m)
	paste(&m, "Los Angeles")
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if got := m.Value(); got == "" {
		t.Fatal("accepted value is missing")
	}

	if _, handled := m.Update(tea.KeyPressMsg{Code: tea.KeyTab}); handled {
		t.Fatal("tab was handled by autocomplete")
	}
	if got, want := m.Value(), "America/Los_Angeles"; got != want {
		t.Fatalf("tab changed accepted value to %q, want %q", got, want)
	}
	m.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
	if got := m.Value(); got != "" {
		t.Fatalf("edited value = %q, want no hidden accepted value", got)
	}
}

func TestEnterAcceptsOpenSuggestionWithoutSubmitting(t *testing.T) {
	m := testModel()
	m.keys.Choose = key.NewBinding(key.WithKeys("enter"))
	focus(&m)
	paste(&m, "Los Angeles")
	if _, handled := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter}); !handled {
		t.Fatal("enter was not captured for an open suggestion")
	}
	if got, want := m.Value(), "America/Los_Angeles"; got != want {
		t.Fatalf("enter value = %q, want %q", got, want)
	}
	m.SetGeometry(30, 2)
	if m.Open() {
		t.Fatal("acceptance or subsequent resize reopened suggestions")
	}
	if _, handled := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"}); handled {
		t.Fatal("Close after acceptance should reach the enclosing form")
	}
}

func TestSetValueHighlightsExactIdentityOnRefocus(t *testing.T) {
	m := New([]Option{{Value: "Etc/UTC", Label: "Etc/UTC"}, {Value: "UTC", Label: "UTC"}}, KeyMap{Choose: key.NewBinding(key.WithKeys("enter"))})
	m.SetValue("UTC")
	focus(&m)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Value() != "UTC" || m.Open() {
		t.Fatalf("accepting unchanged query changed identity: %q, open=%v", m.Value(), m.Open())
	}
}

func TestInputEditsUseNormalCursorBehavior(t *testing.T) {
	m := testModel()
	focus(&m)
	for _, r := range "Los" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m.Update(tea.KeyPressMsg{Code: '!', Text: "!"})
	if got, want := m.Query(), "Lo!s"; got != want {
		t.Fatalf("cursor edit = %q, want %q", got, want)
	}
}

func TestPasteBlurResizeAndZeroGeometryPreserveDraft(t *testing.T) {
	m := testModel()
	focus(&m)
	paste(&m, "Los")
	m.Blur()
	if m.Open() {
		t.Fatal("blur left suggestions open")
	}
	m.SetGeometry(32, 3)
	if got, want := m.Query(), "Los"; got != want {
		t.Fatalf("resize query = %q, want %q", got, want)
	}
	focus(&m)
	if !m.Open() {
		t.Fatal("refocus did not restore suggestions")
	}

	m.SetGeometry(0, 0)
	if _, handled := m.Update(tea.PasteMsg{Content: " Angeles"}); !handled {
		t.Fatal("zero geometry did not consume paste")
	}
	if got, want := m.Query(), "Los"; got != want {
		t.Fatalf("hidden paste changed query to %q, want %q", got, want)
	}
	m.SetGeometry(32, 3)
	if got, want := m.Query(), "Los"; got != want {
		t.Fatalf("restored query = %q, want %q", got, want)
	}
}

func TestCloseDismissesOnceAndUsesCapturedBindings(t *testing.T) {
	m := testModel()
	m.keys.Choose = key.NewBinding(key.WithKeys("z", "x"))
	m.keys.Up = key.NewBinding(key.WithKeys("z", "up"))
	focus(&m)
	paste(&m, "Los")
	if _, handled := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"}); !handled {
		t.Fatal("remapped close was not handled")
	}
	if m.Open() || m.Value() != "" {
		t.Fatal("close did not dismiss suggestions without accepting")
	}
	if _, handled := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"}); handled {
		t.Fatal("second close should reach the enclosing form")
	}
}

func TestNavigationRevealsMultipleMatchesWithCapturedKeys(t *testing.T) {
	options := make([]Option, 8)
	for i := range options {
		letter := string(rune('A' + i))
		options[i] = Option{Value: "Zone/" + letter, Label: "City " + letter, Search: "shared city"}
	}
	m := New(options, KeyMap{
		Up:     key.NewBinding(key.WithKeys("ctrl+p")),
		Down:   key.NewBinding(key.WithKeys("ctrl+n")),
		Choose: key.NewBinding(key.WithKeys("enter")),
		Close:  key.NewBinding(key.WithKeys("esc")),
	})
	m.SetGeometry(24, 3)
	m.Focus()
	paste(&m, "SHARED_city")
	check := func(want string) {
		t.Helper()
		// The real caller offers geometry again before each key and render.
		m.SetGeometry(24, 3)
		view := m.View()
		rows := len(view.Rows)
		if view.Above > 0 {
			rows++
		}
		if view.Below > 0 {
			rows++
		}
		if rows > 3 {
			t.Fatal("suggestions escaped their offered height")
		}
		found := false
		for _, row := range view.Rows {
			if row.ID == want && row.Selected && row.CursorMarker {
				found = true
			}
		}
		if !found || m.Query() != "SHARED_city" || m.Value() != "" {
			t.Fatalf("navigation failed to reveal %s or committed/mutated the query: %+v", want, view)
		}
	}
	check("Zone/A")
	for i := 1; i <= 6; i++ {
		if _, handled := m.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl}); !handled {
			t.Fatal("remapped Down was not handled")
		}
		check(options[i].Value)
	}
	if m.list.Offset() == 0 {
		t.Fatal("fixture did not move beyond the initial viewport")
	}
	for i := 5; i >= 2; i-- {
		if _, handled := m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl}); !handled {
			t.Fatal("remapped Up was not handled")
		}
		check(options[i].Value)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	check("Zone/C")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Value() != "Zone/C" || m.Query() != "City C" || m.Open() {
		t.Fatalf("accepted wrong stable identity after scrolling back: %q / %q", m.Value(), m.Query())
	}
}

func testModel() Model {
	return New([]Option{
		{Value: "UTC", Label: "UTC", Search: "utc"},
		{Value: "America/Los_Angeles", Label: "America/Los Angeles", Search: "Los Angeles America/Los_Angeles"},
		{Value: "America/New_York", Label: "America/New York", Search: "New York America/New_York alias"},
	}, KeyMap{
		Up:     key.NewBinding(key.WithKeys("up")),
		Down:   key.NewBinding(key.WithKeys("down")),
		Choose: key.NewBinding(key.WithKeys("x")),
		Close:  key.NewBinding(key.WithKeys("z")),
	})
}

func focus(m *Model) {
	m.SetGeometry(32, 3)
	m.Focus()
}

func paste(m *Model, text string) {
	m.Update(tea.PasteMsg{Content: text})
}
