package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func scenarioPaletteCommands(n int) []client.Command {
	commands := make([]client.Command, n)
	for i := range commands {
		commands[i] = client.Command{Name: fmt.Sprintf("command-%02d", i), Description: "description that remains readable when it wraps"}
	}
	return commands
}

func scenarioPaletteState(commands []client.Command) paletteState {
	st := paletteState{open: true, filtered: commands}
	st.syncList()
	return st
}

func TestMecatuiSlashPaletteBoundedList_Scenario1_GeometryAndIndicators(t *testing.T) {
	for _, width := range []int{24, 48, 80} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			st := scenarioPaletteState(scenarioPaletteCommands(12))
			got := renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", width, maxPaletteRows)
			if got == "" {
				t.Fatal("usable geometry suppressed the palette")
			}
			view := st.list.ViewWithIndicators(maxPaletteRows, false)
			if len(view.Rows) > maxPaletteRows {
				t.Fatalf("content rows = %d, want at most %d", len(view.Rows), maxPaletteRows)
			}
			if view.Below == 0 || !strings.Contains(ansi.Strip(got), "below") {
				t.Fatalf("missing below-overflow indication: view=%+v\n%s", view, ansi.Strip(got))
			}
			for row, line := range strings.Split(got, "\n") {
				if cells := ansi.StringWidth(line); cells > width {
					t.Fatalf("row %d width = %d, offered %d: %q", row, cells, width, ansi.Strip(line))
				}
			}
		})
	}

	st := scenarioPaletteState(scenarioPaletteCommands(3))
	if got := renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", 40, 0); got != "" {
		t.Fatalf("degenerate body rendered a card: %q", ansi.Strip(got))
	}
	if st.list.Valid() {
		t.Fatal("suppressed palette retained valid list geometry")
	}

	m := newPaletteModel(t, sampleCommands())
	m.conv.addUser("settled conversation")
	m.refreshView()
	m = typeRune(t, m, '/')
	m = applyAll(m, tea.WindowSizeMsg{Width: 40, Height: 18})
	frame := m.View().Content
	if rows := lipglossHeight(frame); rows > m.height {
		t.Fatalf("short frame height = %d, offered %d vp=%d visible=%t:\n%s", rows, m.height, m.vp.Height(), m.paletteVisible(), ansi.Strip(frame))
	}
	if m.paletteVisible() {
		t.Fatal("short frame should suppress the complete palette card")
	}
}

func TestMecatuiSlashPaletteBoundedList_Scenario1_SelectionAnchors(t *testing.T) {
	commands := []client.Command{
		{Name: "alpha", Description: strings.Repeat("wrapped segment ", 12)},
		{Name: "beta", Description: "second"},
		{Name: "gamma", Description: "third"},
	}
	st := scenarioPaletteState(commands)
	_ = renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", 24, 2)
	firstID := st.list.CursorID()
	st.list.Move(bounded.PageDown)
	if st.list.CursorID() != firstID || st.list.Offset() == 0 {
		t.Fatalf("first page move skipped wrapped selected content: id=%q offset=%d", st.list.CursorID(), st.list.Offset())
	}
	for st.list.CursorID() == firstID {
		before := st.list.Offset()
		st.list.Move(bounded.PageDown)
		if st.list.CursorID() == firstID && st.list.Offset() <= before {
			t.Fatalf("wrapped page did not advance contiguously: %d -> %d", before, st.list.Offset())
		}
	}
	if st.list.Cursor() != 1 {
		t.Fatalf("page traversal moved to command %d, want next command", st.list.Cursor())
	}
	st.list.Move(bounded.LineDown)
	if st.list.Cursor() != 2 {
		t.Fatalf("Down cursor = %d, want one logical command", st.list.Cursor())
	}
	st.list.Move(bounded.LineUp)
	if st.list.Cursor() != 1 {
		t.Fatalf("Up cursor = %d, want one logical command", st.list.Cursor())
	}

	m := newPaletteModel(t, sampleCommands())
	m = typeRune(t, m, '/')
	for m.palette.selected().Name != "review" {
		m.palette.list.Move(bounded.LineDown)
	}
	m.prompt.Rewrite("/r")
	m, _ = m.syncPalette()
	if got := m.palette.selected().Name; got != "review" {
		t.Fatalf("stable selection after refilter = %q, want review", got)
	}
	m.prompt.Rewrite("/refa")
	m, _ = m.syncPalette()
	if got := m.palette.selected().Name; got != "refactor" {
		t.Fatalf("replacement after selected row disappeared = %q, want refactor", got)
	}
	m.prompt.Rewrite("/r")
	m, _ = m.syncPalette()
	if got := m.palette.selected().Name; got != "refactor" {
		t.Fatalf("removed selection snapped back after widening filter: %q", got)
	}
}

func TestMecatuiSlashPaletteBoundedList_Scenario1_StandardRowPresentation(t *testing.T) {
	registry := themeRegistry()
	for _, name := range registry.List() {
		t.Run(name, func(t *testing.T) {
			th, _ := registry.Get(name)
			st := scenarioPaletteState([]client.Command{{Name: "alpha", Description: "readable description"}, {Name: "beta", Description: "other"}})
			got := renderPaletteSized(th, st, client.Capabilities{}, "/", 60, 4)
			plain := ansi.Strip(got)
			if !strings.Contains(plain, "▶ /alpha  readable description") || !strings.Contains(plain, "  /beta  other") {
				t.Fatalf("rows do not have equal one-cell selection gutters:\n%s", plain)
			}
			if strings.ContainsAny(plain, "╭╮╰╯") {
				t.Fatalf("permission-button border leaked into palette rows:\n%s", plain)
			}
			selectedPrefix := strings.TrimSuffix(th.Style("spinner").Render("▶"), "\x1b[m")
			if !strings.Contains(got, selectedPrefix) {
				t.Fatal("selected row did not use the palette selected-row style")
			}
			unselectedPrefix := strings.TrimSuffix(th.Style("toolArgs").Render("  /beta"), "\x1b[m")
			if !strings.Contains(got, unselectedPrefix) {
				t.Fatal("unselected row did not use the palette unselected-row style")
			}
		})
	}
}

func TestMecatuiSlashPaletteBoundedList_Scenario1_PreservesInteractionOwnership(t *testing.T) {
	m := newPaletteModel(t, sampleCommands())
	m = typeRune(t, m, '/')
	_ = m.View()
	if !m.paletteVisible() {
		t.Fatal("precondition: palette should be visible")
	}
	for _, keyMsg := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyUp}, {Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}} {
		mm, _, handled := m.onPaletteKey(keyMsg)
		if !handled {
			t.Fatalf("visible palette did not claim %q", keyMsg.String())
		}
		m = mm.(Model)
	}
	for _, keyMsg := range []tea.KeyPressMsg{{Code: tea.KeyHome}, {Code: tea.KeyEnd}} {
		if _, _, handled := m.onPaletteKey(keyMsg); handled {
			t.Fatalf("palette unexpectedly claimed %q", keyMsg.String())
		}
	}
	cursor := m.palette.list.Cursor()
	clicked, _ := m.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 1, Y: m.height / 2})
	m = clicked.(Model)
	if m.palette.list.Cursor() != cursor {
		t.Fatal("mouse click moved the palette cursor")
	}

	filtered := newPaletteModel(t, sampleCommands())
	filtered = typeRune(t, filtered, '/')
	filtered = typeRune(t, filtered, 'r')
	if filtered.prompt.Value() != "/r" || len(filtered.palette.filtered) != 3 {
		t.Fatalf("typing did not edit and filter: prompt=%q rows=%d", filtered.prompt.Value(), len(filtered.palette.filtered))
	}

	workspace := newPaletteModel(t, sampleCommands())
	workspace = typeRune(t, workspace, '/')
	workspace.palette.list.SetCursor(7)
	completed, _, handled := workspace.onPaletteKey(tea.KeyPressMsg{Code: tea.KeyTab})
	workspace = completed.(Model)
	if !handled || workspace.prompt.Value() != "/fix " || workspace.palette.open {
		t.Fatalf("workspace completion changed: handled=%t prompt=%q open=%t", handled, workspace.prompt.Value(), workspace.palette.open)
	}

	builtin := newPaletteModel(t, nil)
	builtin = typeRune(t, builtin, '/')
	builtin.palette.list.SetCursor(1) // /help
	dispatched, _, handled := builtin.onPaletteKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	builtin = dispatched.(Model)
	if !handled || !builtin.showHelp {
		t.Fatal("eligible built-in was not dispatched from the palette")
	}

	dismissed := newPaletteModel(t, sampleCommands())
	dismissed = typeRune(t, dismissed, '/')
	dismissedModel, _, handled := dismissed.onPaletteKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	dismissed = dismissedModel.(Model)
	if !handled || dismissed.palette.open || !dismissed.palette.dismissed {
		t.Fatal("idle Escape did not dismiss the visible palette")
	}

	commander := sampleCommands()
	fetched := newRawPaletteModel(t, commander)
	fetchedModel, fetch := fetched.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	fetched = feedCommandsMsg(t, fetchedModel.(Model), fetch)
	fetched = typeRune(t, fetched, 'r')
	if commander.calls != 1 || fetched.prompt.Value() != "/r" {
		t.Fatalf("command discovery calls = %d, prompt=%q; want one call and /r", commander.calls, fetched.prompt.Value())
	}

	running, _ := newQueueModel(t)
	running = startRunning(t, running, "first")
	running.prompt.Rewrite("/")
	running, _ = running.syncPalette()
	_ = running.View()
	mm, _ := running.onRunningKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	running = mm.(Model)
	if running.statusMsg != "cancelling…" || !running.palette.open {
		t.Fatalf("running Escape did not remain run-cancel: status=%q open=%t", running.statusMsg, running.palette.open)
	}

	short := newPaletteModel(t, sampleCommands())
	short = typeRune(t, short, '/')
	short = applyAll(short, tea.WindowSizeMsg{Width: 40, Height: 6})
	_ = short.View()
	if short.paletteVisible() {
		t.Fatal("precondition: short geometry should suppress palette")
	}
	for _, keyMsg := range []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyUp}, {Code: tea.KeyPgDown}, {Code: tea.KeyPgUp}, {Code: tea.KeyTab}, {Code: tea.KeyEnter}, {Code: tea.KeyEsc}} {
		if _, _, handled := short.onPaletteKey(keyMsg); handled {
			t.Fatalf("suppressed palette claimed %q", keyMsg.String())
		}
	}
	short.keyboardEventTypes = true
	if !short.doubleEscapeEligible() {
		t.Fatal("suppressed palette kept ordinary idle Escape handling from receiving the key")
	}
}

func lipglossHeight(s string) int { return len(strings.Split(s, "\n")) }

func themeRegistry() *theme.Registry { return theme.NewRegistry() }
