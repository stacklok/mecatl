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
			st := scenarioPaletteState(scenarioPaletteCommands(maxPaletteRows + 2))
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
			if cardWidth := ansi.StringWidth(strings.Split(got, "\n")[0]); cardWidth != min(128, width) {
				t.Fatalf("outer card width = %d, want %d", cardWidth, min(128, width))
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

	// A one-row body still exposes both sides of a middle physical row without
	// borrowing a second body row for indicator chrome.
	st = scenarioPaletteState(scenarioPaletteCommands(3))
	st.list.SetGeometry(20, 1, 1, bounded.Wrap)
	st.list.SetCursor(1)
	got := ansi.Strip(renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", 24, 1))
	if !strings.Contains(got, "above") || !strings.Contains(got, "below") {
		t.Fatalf("one-row body lost bidirectional overflow indicators:\n%s", got)
	}
	if rows := len(st.list.View().Rows); rows != 1 {
		t.Fatalf("list body rows = %d, want exactly one", rows)
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

	// Each admissible physical-row budget retains the prompt and footer while the
	// real frame renders the palette through its normal layout path.
	for rows := 1; rows <= maxPaletteRows; rows++ {
		t.Run(fmt.Sprintf("body-rows-%d", rows), func(t *testing.T) {
			model := newPaletteModel(t, sampleCommands())
			model.conv.addUser("settled conversation")
			model.refreshView()
			model = typeRune(t, model, '/')
			model = applyAll(model, tea.WindowSizeMsg{Width: 80, Height: rows + 19})
			frame := model.View().Content
			if !model.paletteVisible() || !model.palette.list.Valid() {
				t.Fatal("admissible geometry suppressed the palette")
			}
			if got := len(model.palette.list.View().Rows); got > maxPaletteRows {
				t.Fatalf("list rows = %d, budget = %d", got, rows)
			}
			if height := lipglossHeight(frame); height > model.height {
				t.Fatalf("frame height = %d, offered %d", height, model.height)
			}
			if !strings.Contains(ansi.Strip(frame), ansi.Strip(model.renderInput())) ||
				!strings.Contains(ansi.Strip(frame), ansi.Strip(model.renderFooter())) {
				t.Fatalf("palette displaced prompt or footer:\n%s", ansi.Strip(frame))
			}
		})
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

	// CommandsMsg is the discovery-refresh seam: preserve stable selection while
	// available, then adopt its replacement without resurrecting the old ID.
	m = newPaletteModel(t, sampleCommands())
	m = typeRune(t, m, '/')
	for m.palette.selected().Name != "review" {
		m.palette.list.Move(bounded.LineDown)
	}
	updated, _ := m.Update(client.CommandsMsg{Commands: []client.Command{
		{Name: "fix", Description: "fix a failing test"},
		{Name: "review", Description: "review refreshed"},
		{Name: "refactor", Description: "refactor a function"},
	}})
	m = updated.(Model)
	if got := m.palette.selected().Name; got != "review" {
		t.Fatalf("CommandsMsg lost stable selection: %q", got)
	}
	updated, _ = m.Update(client.CommandsMsg{Commands: []client.Command{
		{Name: "fix", Description: "fix a failing test"},
		{Name: "refactor", Description: "refactor a function"},
	}})
	m = updated.(Model)
	if got := m.palette.selected().Name; got != "refactor" {
		t.Fatalf("CommandsMsg missing selection replacement: %q", got)
	}
	updated, _ = m.Update(client.CommandsMsg{Commands: sampleCommands().cmds})
	m = updated.(Model)
	if got := m.palette.selected().Name; got != "refactor" {
		t.Fatalf("CommandsMsg resurrected removed selection: %q", got)
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
			selectedCommand := th.Style("spinner").Bold(true).Render("/alpha")
			if !strings.Contains(got, selectedCommand) {
				t.Fatal("selected command is not prominent and bold")
			}
			mutedPrefix := strings.Split(th.Style("muted").Render("x"), "x")[0]
			if !strings.Contains(got, mutedPrefix) {
				t.Fatal("description did not use the muted style")
			}
			if !strings.Contains(got, th.Style("toolArgs").Render("  ")) {
				t.Fatal("unselected row did not retain its standard gutter style")
			}
		})
	}
}

func TestMecatuiSlashPaletteBoundedList_Scenario1_PresentationBounds(t *testing.T) {
	st := scenarioPaletteState([]client.Command{
		{Name: strings.Repeat("command", 30), Description: "ignored when the command uses the row"},
		{Name: "details", Description: strings.Repeat("description words ", 24)},
	})
	first := renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", 160, maxPaletteRows)
	if got, want := ansi.StringWidth(strings.Split(first, "\n")[0]), 128; got != want {
		t.Fatalf("wide card width = %d, want %d", got, want)
	}
	st.list.Move(bounded.LineDown)
	afterScroll := renderPaletteSized(testTheme(), st, client.Capabilities{}, "/", 160, maxPaletteRows)
	if got, want := ansi.StringWidth(strings.Split(afterScroll, "\n")[0]), 128; got != want {
		t.Fatalf("scroll changed card width to %d, want %d", got, want)
	}

	view := st.list.View()
	linesByID := map[string][]bounded.ListRow{}
	for _, row := range view.Rows {
		linesByID[row.ID] = append(linesByID[row.ID], row)
	}
	for id, rows := range linesByID {
		if len(rows) > 3 {
			t.Fatalf("%s rendered %d physical lines, want at most three", id, len(rows))
		}
		for _, row := range rows {
			if ansi.StringWidth(row.Text) > 120 {
				t.Fatalf("%s row exceeds content width: %q", id, row.Text)
			}
		}
	}
	long := linesByID["workspace:"+strings.ToLower(strings.Repeat("command", 30))]
	if !strings.HasSuffix(ansi.Strip(long[0].Text), "…") {
		t.Fatalf("long command did not use ellipsis: %q", long[0].Text)
	}
	details := linesByID["workspace:details"]
	if len(details) != 3 || !strings.HasPrefix(ansi.Strip(details[1].Text), "│ ") || !strings.HasSuffix(ansi.Strip(details[2].Text), "…") {
		t.Fatalf("description did not use capped hanging continuation lines: %+v", details)
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
		updated, _ := m.Update(keyMsg)
		m = updated.(Model)
		if !m.paletteVisible() {
			t.Fatalf("visible palette relinquished %q", keyMsg.String())
		}
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
	for _, letter := range []rune{'j', 'k'} {
		t.Run("printable-"+string(letter), func(t *testing.T) {
			input := newPaletteModel(t, sampleCommands())
			input = typeRune(t, input, '/')
			// Model.Update is the production key-routing seam: default Up/Down
			// bindings include j/k, but the palette must leave them to the prompt.
			input = typeRune(t, input, letter)
			if got, want := input.prompt.Value(), "/"+string(letter); got != want {
				t.Fatalf("prompt = %q, want printable palette filter input %q", got, want)
			}
		})
	}

	paged := newPaletteModel(t, &fakeCommander{cmds: []client.Command{
		{Name: "alpha", Description: strings.Repeat("wrapped segment ", 24)},
		{Name: "beta", Description: "second"},
	}})
	paged = typeRune(t, paged, '/')
	paged = applyAll(paged, tea.WindowSizeMsg{Width: 24, Height: 21})
	_ = paged.View()
	paged.palette.list.SetCursor(7) // alpha follows the seven built-ins.
	paged.keys = applyKeyOverrides(paged.keys, map[string][]string{"ScrollU": {"p"}, "ScrollD": {"n"}})
	updated, _ := paged.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	paged = updated.(Model)
	pageOffset := paged.palette.list.Offset()
	if pageOffset == 0 || paged.palette.selected().Name != "alpha" {
		t.Fatalf("remapped ScrollD did not page wrapped selection: offset=%d selected=%q", pageOffset, paged.palette.selected().Name)
	}
	updated, _ = paged.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	paged = updated.(Model)
	if paged.palette.list.Offset() >= pageOffset || paged.palette.selected().Name != "alpha" {
		t.Fatalf("remapped ScrollU did not page back: offset=%d selected=%q", paged.palette.list.Offset(), paged.palette.selected().Name)
	}

	workspace := newPaletteModel(t, sampleCommands())
	workspace = typeRune(t, workspace, '/')
	workspace.palette.list.SetCursor(7)
	completed, _ := workspace.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	workspace = completed.(Model)
	if workspace.prompt.Value() != "/fix " || workspace.palette.open {
		t.Fatalf("workspace completion changed: prompt=%q open=%t", workspace.prompt.Value(), workspace.palette.open)
	}

	builtin := newPaletteModel(t, nil)
	builtin = typeRune(t, builtin, '/')
	builtin.palette.list.SetCursor(1) // /help
	dispatched, _ := builtin.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	builtin = dispatched.(Model)
	if !builtin.showHelp {
		t.Fatal("eligible built-in was not dispatched from the palette")
	}

	dismissed := newPaletteModel(t, sampleCommands())
	dismissed = typeRune(t, dismissed, '/')
	dismissedModel, _ := dismissed.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	dismissed = dismissedModel.(Model)
	if dismissed.palette.open || !dismissed.palette.dismissed {
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

	for _, keyMsg := range []tea.KeyPressMsg{{Code: tea.KeyTab}, {Code: tea.KeyEnter}} {
		t.Run("running-"+keyMsg.String(), func(t *testing.T) {
			runningPalette := newPaletteModel(t, sampleCommands())
			runningPalette = typeRune(t, runningPalette, '/')
			runningPalette.phase = phaseRunning
			_ = runningPalette.View()
			runningPalette.palette.list.SetCursor(7) // /fix workspace command
			updated, _ := runningPalette.Update(keyMsg)
			runningPalette = updated.(Model)
			if runningPalette.prompt.Value() != "/fix " || runningPalette.palette.open {
				t.Fatalf("running palette did not own %q: prompt=%q open=%t", keyMsg.String(), runningPalette.prompt.Value(), runningPalette.palette.open)
			}
		})
	}

	running, _ := newQueueModel(t)
	running = startRunning(t, running, "first")
	running.prompt.Rewrite("/")
	running, _ = running.syncPalette()
	_ = running.View()
	for _, keyMsg := range []tea.KeyPressMsg{{Code: tea.KeyUp}, {Code: tea.KeyDown}, {Code: tea.KeyPgUp}, {Code: tea.KeyPgDown}} {
		updated, _ := running.Update(keyMsg)
		running = updated.(Model)
		if !running.palette.open || running.prompt.Value() != "/" {
			t.Fatalf("running palette did not retain %q: open=%t prompt=%q", keyMsg.String(), running.palette.open, running.prompt.Value())
		}
	}
	mm, _ := running.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	updated, _ = short.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	short = updated.(Model)
	if !short.doubleEscapeArmed {
		t.Fatal("suppressed palette prevented ordinary idle Escape handling")
	}
}

func lipglossHeight(s string) int { return len(strings.Split(s, "\n")) }

func themeRegistry() *theme.Registry { return theme.NewRegistry() }
