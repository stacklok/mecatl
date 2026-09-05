package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// embeddedCaps is the embedded-default server's capability set: memory + teams +
// bash on; mcp + slash-commands + skills off. It is the most important fixture —
// it is what a user running the single `mecatui` binary sees, and it exercises
// both the available and the [not enabled] annotation paths.
func embeddedCaps() client.Capabilities {
	return client.Capabilities{Memory: true, Teams: true, Bash: true}
}

// allOnCaps is an external mecated with every optional feature wired.
func allOnCaps() client.Capabilities {
	return client.Capabilities{
		MCP: true, SlashCommands: true, Memory: true, Skills: true, Teams: true, Bash: true,
		ModelSelection: true, Scheduling: true,
	}
}

// helpModel builds a connected, sized idle model with the given caps and opens
// the "?" help overlay, ready for a View() golden.
func helpModel(t *testing.T, caps client.Capabilities, tweak ...func(*Deps)) Model {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	deps := Deps{
		Session:           conv,
		Conv:              conv,
		Theme:             aztec(),
		Server:            "127.0.0.1:8080",
		Workspace:         "/workspace",
		Mode:              "default",
		Model:             "mock-model",
		Ctx:               context.Background(),
		NoAltScreen:       true,
		emojiCapable:      func() bool { return false },
		kittyCapable:      func() bool { return false },
		scrollKeysMarking: func() string { return "pgup/pgdn" },
	}
	for _, fn := range tweak {
		fn(&deps)
	}
	m := newTestModelFromDeps(deps)
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: caps},
	)
	// Seed a user block so the zero-state card doesn't render under the overlay,
	// isolating the help golden to the overlay itself.
	m.conv.addUser("hello")
	m.refreshView()
	m = applyAll(m, qmark())
	if !m.showHelp {
		t.Fatal("help overlay did not open on '?' with empty input")
	}
	return m
}

// qmark is a printable "?" key press.
func qmark() tea.KeyPressMsg { return tea.KeyPressMsg{Code: '?', Text: "?"} }

// TestHelpOverlayEmbeddedGolden locks the help overlay under embedded defaults:
// mcp/commands/skills rows carry [not enabled]; memory/teams/bash are available.
func TestHelpOverlayEmbeddedGolden(t *testing.T) {
	m := helpModel(t, embeddedCaps())
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "help_embedded.golden", got)
}

// TestHelpOverlayAllOnGolden locks the help overlay under an all-on external
// server: every row available, no [not enabled] tags, plus the commands +
// memory prose lines.
func TestHelpOverlayAllOnGolden(t *testing.T) {
	m := helpModel(t, allOnCaps())
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "help_all_on.golden", got)
}

// TestHelpAnnotationsTrackCaps asserts the annotations follow caps without
// pinning exact layout: under embedded defaults the MCP/commands/skills features
// are tagged not-enabled and memory/teams are not.
func TestHelpAnnotationsTrackCaps(t *testing.T) {
	for _, body := range []string{stripANSIstr(m_helpBody(embeddedCaps())), stripANSIstr(m_helpBody(allOnCaps()))} {
		if !strings.Contains(body, "/quit") || !strings.Contains(body, "alias: /exit") {
			t.Errorf("help must document /quit and /exit alias:\n%s", body)
		}
	}
	embedded := stripANSIstr(m_helpBody(embeddedCaps()))
	allOn := stripANSIstr(m_helpBody(allOnCaps()))

	if !strings.Contains(embedded, notEnabledTag) {
		t.Errorf("embedded help should carry %q tags:\n%s", notEnabledTag, embedded)
	}
	if strings.Contains(allOn, notEnabledTag) {
		t.Errorf("all-on help should carry NO %q tags:\n%s", notEnabledTag, allOn)
	}
	// The commands prose line is gated on caps.SlashCommands.
	if strings.Contains(embedded, "Type / to browse") {
		t.Errorf("embedded help should NOT advertise the / palette (commands off):\n%s", embedded)
	}
	if !strings.Contains(allOn, "Type / to browse") {
		t.Errorf("all-on help SHOULD advertise the / palette:\n%s", allOn)
	}
	steer := stripANSIstr(m_helpBody(client.Capabilities{Steer: true}))
	if !strings.Contains(steer, "steer the current run") {
		t.Errorf("steer-capable help should describe mid-run steering:\n%s", steer)
	}
	if !strings.Contains(steer, "bare built-ins stay local") {
		t.Errorf("steer-capable help should explain local built-ins:\n%s", steer)
	}
	if strings.Contains(steer, "queue a follow-up (sends when the turn ends)") {
		t.Errorf("steer-capable help should not describe the fallback queue:\n%s", steer)
	}
	// The skills clarification is always present.
	if !strings.Contains(embedded, "Skills run automatically") {
		t.Errorf("help should always carry the skills clarification:\n%s", embedded)
	}
	// The usage legend decoding BOTH the token arrows AND the cache percentage is
	// always present (decision 4 + UX-1: the percentage is the number behind a
	// surprisingly large prompt).
	for _, sub := range []string{"↑ input", "↓ output", "⊕ cache write", "cache N%", "served from cache"} {
		if !strings.Contains(embedded, sub) {
			t.Errorf("help should carry the usage legend %q:\n%s", sub, embedded)
		}
	}
	// The permission-modal group (issue #488) is always present: the verdict
	// chords, the ctrl+t full-args row, and the raw-args toggle row.
	for _, sub := range []string{
		"While the permission modal is open",
		"allow once",
		"always allow (this session; main-agent asks only)",
		"full-screen args (non-diff asks)",
		"raw args in the full view",
	} {
		if !strings.Contains(embedded, sub) {
			t.Errorf("help should carry the permission-modal row %q:\n%s", sub, embedded)
		}
	}
}

// m_helpBody renders just the help body for caps (no centering), for content
// assertions. Uses the default keys since the tests don't wire custom keymaps.
func m_helpBody(caps client.Capabilities) string {
	return helpBody(aztec(), caps, defaultHelpKeys())
}

func TestSelectionShortcutsRespectDepsKeyOverridesEndToEnd(t *testing.T) {
	const (
		selectAll     = "ctrl+alt+s"
		copySelection = "ctrl+alt+c"
	)
	m := New(Deps{
		Theme:             aztec(),
		Ctx:               context.Background(),
		NoAltScreen:       true,
		KeyOverrides:      map[string][]string{"SelectAll": {selectAll}, "CopySelection": {copySelection}},
		emojiCapable:      func() bool { return false },
		kittyCapable:      func() bool { return false },
		scrollKeysMarking: func() string { return "pgup/pgdn" },
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-key-override"},
	)
	m.prompt.Rewrite("copy me")

	mm, _ := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl | tea.ModAlt})
	m = mm.(Model)
	if got := m.prompt.SelectedText(); got != "copy me" {
		t.Fatalf("overridden SelectAll via Model.Update selected %q, want %q", got, "copy me")
	}
	mm, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl | tea.ModAlt})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("overridden CopySelection via Model.Update returned no copy command")
	}
	footer := stripANSIstr(m.renderFooter())
	for _, marker := range []string{selectAll + " select all", copySelection + " copy"} {
		if !strings.Contains(footer, marker) {
			t.Errorf("footer missing overridden marker %q:\n%s", marker, footer)
		}
	}
	for _, marker := range []string{"ctrl+g select all", "ctrl+shift+c copy"} {
		if strings.Contains(footer, marker) {
			t.Errorf("footer retained default marker %q:\n%s", marker, footer)
		}
	}

	m.prompt.Rewrite("")
	mm, _ = m.Update(qmark())
	m = mm.(Model)
	help := stripANSIstr(m.View().Content)
	for action, marker := range map[string]string{
		"select all prompt text":                           selectAll,
		"copy the active prompt or conversation selection": copySelection,
	} {
		found := false
		for _, line := range strings.Split(help, "\n") {
			if strings.Contains(line, action) && strings.Contains(line, marker) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help missing overridden marker %q for %q:\n%s", marker, action, help)
		}
	}
	for _, marker := range []string{"ctrl+g", "ctrl+shift+c"} {
		if strings.Contains(help, marker) {
			t.Errorf("help retained default marker %q:\n%s", marker, help)
		}
	}
}

// TestSelectionShortcutsRenderDefaults proves the prompt-selection bindings are
// visible on both persistent UI surfaces with their default chords.
func TestSelectionShortcutsRenderDefaults(t *testing.T) {
	const (
		selectAll     = "ctrl+g select all"
		copySelection = "ctrl+shift+c copy"
	)

	t.Run("help overlay", func(t *testing.T) {
		got := stripANSIstr(m_helpBody(allOnCaps()))
		rows := map[string]string{
			"select all prompt text":                           "ctrl+g",
			"copy the active prompt or conversation selection": "ctrl+shift+c",
		}
		for action, chord := range rows {
			found := false
			for _, line := range strings.Split(got, "\n") {
				line = strings.TrimSpace(line)
				if strings.Contains(line, action) {
					found = true
					if !strings.HasPrefix(line, chord) {
						t.Errorf("help row %q should lead with %q: %q", action, chord, line)
					}
				}
			}
			if !found {
				t.Errorf("help overlay should contain %q:\n%s", action, got)
			}
		}
	})

	t.Run("footer", func(t *testing.T) {
		m, _, _ := newTestModel(t, aztec())
		m = applyAll(m,
			tea.WindowSizeMsg{Width: 120, Height: 30},
			client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
		)
		got := stripANSIstr(m.renderFooter())
		for _, want := range []string{selectAll, copySelection} {
			if !strings.Contains(got, want) {
				t.Errorf("footer should contain %q: %q", want, got)
			}
		}
	})
}

// TestHelpReflectsKeyOverride proves the help overlay reads the LIVE bindings:
// EVERY rebindable row in the help body must lead with its overridden chord,
// and the replaced default chord must not still label that row. The test builds
// the keymap via applyKeyOverrides directly (bypassing the validator), so the
// sentinel chords only need to be distinct, not validator-legal.
func TestHelpReflectsKeyOverride(t *testing.T) {
	// Every rebindable action that appears as a help-body row, overridden to a
	// distinct sentinel chord. The scroll pair uses one sentinel per half so the
	// "<up>/<down>" join is exercised.
	overrides := map[string][]string{
		"Submit":        {"ctrl+f1"},
		"Newline":       {"ctrl+f2"},
		"Paste":         {"ctrl+f3"},
		"SelectAll":     {"ctrl+f31"},
		"CopySelection": {"ctrl+f32"},
		"Cancel":        {"ctrl+f4"},
		"ClearPrompt":   {"ctrl+f33"},
		"Effort":        {"ctrl+f5"},
		"MCPPanel":      {"ctrl+f6"},
		"Resources":     {"ctrl+f7"},
		"Prompts":       {"ctrl+f8"},
		"Agents":        {"ctrl+f9"},
		"ModeSwitch":    {"ctrl+f10"},
		"ExpandTools":   {"ctrl+f11"},
		"Help":          {"ctrl+f12"},
		"Quit":          {"ctrl+f13"},
		"ScrollU":       {"ctrl+f14"},
		"ScrollD":       {"ctrl+f15"},
		"Close":         {"ctrl+f16"},
	}
	km := applyKeyOverrides(defaultKeys(), overrides)
	body := stripANSIstr(helpBody(aztec(), allOnCaps(), keyMarkings(km)))

	// Each row is matched by its unique action text; want is the exact leading
	// chord column; absent is the default chord that must no longer label it.
	rows := []struct {
		name   string
		match  string
		want   string
		absent string
		occurs int // rows whose action text appears on this many lines
	}{
		{name: "Submit", match: "send the prompt", want: "ctrl+f1", absent: "enter", occurs: 1},
		{name: "Submit queued", match: "queue a follow-up", want: "ctrl+f1", absent: "enter", occurs: 1},
		{name: "Newline", match: "newline", want: "ctrl+f2", absent: "shift+enter", occurs: 1},
		{name: "Paste", match: "paste a clipboard image", want: "ctrl+f3", absent: "ctrl+v", occurs: 1},
		{name: "SelectAll", match: "select all prompt text", want: "ctrl+f31", absent: "ctrl+g", occurs: 1},
		{name: "CopySelection", match: "copy the active prompt or conversation selection", want: "ctrl+f32", absent: "ctrl+shift+c", occurs: 1},
		{name: "Cancel turn", match: "cancel the running turn", want: "ctrl+f4", absent: "esc", occurs: 1},
		{name: "ClearPrompt", match: "clear the unsent prompt", want: "ctrl+f33", absent: "ctrl+u", occurs: 2},
		{name: "Cancel running", match: "cancel run", want: "ctrl+f4", absent: "esc", occurs: 1},
		{name: "MCPPanel", match: "MCP inventory", want: "ctrl+f6", absent: "ctrl+o", occurs: 1},
		{name: "Resources", match: "MCP resources", want: "ctrl+f7", absent: "ctrl+r", occurs: 1},
		{name: "Prompts", match: "MCP prompts", want: "ctrl+f8", absent: "ctrl+p", occurs: 1},
		{name: "Agents", match: "agents overlay", want: "ctrl+f9", absent: "ctrl+a", occurs: 1},
		{name: "Effort", match: "reasoning-effort picker", want: "ctrl+f5", absent: "ctrl+e", occurs: 1},
		{name: "ModeSwitch", match: "cycle permission mode", want: "ctrl+f10", absent: "alt+m", occurs: 1},
		{name: "ExpandTools", match: "expand/collapse details", want: "ctrl+f11", absent: "ctrl+t", occurs: 1},
		{name: "Help", match: "this help (on an empty prompt)", want: "ctrl+f12", absent: "?", occurs: 1},
		{name: "Quit", match: "quit (press twice", want: "ctrl+f13", absent: "ctrl+c", occurs: 1},
		{name: "Scroll", match: "scroll the conversation", want: "ctrl+f14/ctrl+f15", absent: "pgup", occurs: 1},
		{name: "Close hint", match: " close", want: "ctrl+f16 or ctrl+f12", absent: "esc or ?", occurs: 1},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			found := 0
			for _, line := range strings.Split(body, "\n") {
				trim := strings.TrimSpace(line)
				if !strings.Contains(trim, row.match) {
					continue
				}
				found++
				if !strings.HasPrefix(trim, row.want) {
					t.Errorf("%s row should lead with the overridden %q chord: %q", row.name, row.want, trim)
				}
				if strings.Contains(trim, row.absent) {
					t.Errorf("%s row still shows the default %q after override: %q", row.name, row.absent, trim)
				}
			}
			if found != row.occurs {
				t.Errorf("%s: matched %d lines on %q, want %d", row.name, found, row.match, row.occurs)
			}
		})
	}
}

// TestHelpKeyOverrideEndToEnd closes the New()→View() wiring seam: overrides
// carried on Deps.KeyOverrides must reach the rendered help overlay when it is
// opened through the real update path (the "?" keypress).
func TestHelpKeyOverrideEndToEnd(t *testing.T) {
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: allOnCaps()}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       aztec(),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
		KeyOverrides: map[string][]string{
			"Effort":   {"ctrl+f5"},
			"MCPPanel": {"ctrl+f6"},
		},
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Capabilities: allOnCaps()},
	)
	m.conv.addUser("hello")
	m.refreshView()
	m = applyAll(m, qmark())
	if !m.showHelp {
		t.Fatal("help overlay did not open on '?' with empty input")
	}

	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 100})
	view := stripANSIstr(m.View().Content)
	var effortRow, mcpRow string
	for _, line := range strings.Split(view, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.Contains(trim, "reasoning-effort picker"):
			effortRow = trim
		case strings.Contains(trim, "MCP inventory"):
			mcpRow = trim
		}
	}
	// The rows render inside the centred card's border, so the chord is not at
	// column 0 — assert the row carries the override chord and not the default.
	if !strings.Contains(effortRow, "ctrl+f5") {
		t.Errorf("effort row should carry the overridden ctrl+f5 via Deps.KeyOverrides: %q", effortRow)
	}
	if strings.Contains(effortRow, "ctrl+e") {
		t.Errorf("effort row still shows the default ctrl+e: %q", effortRow)
	}
	if !strings.Contains(mcpRow, "ctrl+f6") {
		t.Errorf("MCP-inventory row should carry the overridden ctrl+f6 via Deps.KeyOverrides: %q", mcpRow)
	}
	if strings.Contains(mcpRow, "ctrl+o") {
		t.Errorf("MCP-inventory row still shows the default ctrl+o: %q", mcpRow)
	}
}

// TestKeyMarkingsEmptyBindingFallsBack pins the firstKey fallback contract: a
// binding overridden to an EMPTY chord list (reachable only by bypassing the
// validator, which rejects empty chords) must render the row's DEFAULT marking,
// never a blank key column.
func TestKeyMarkingsEmptyBindingFallsBack(t *testing.T) {
	km := applyKeyOverrides(defaultKeys(), map[string][]string{"Submit": {}})
	if keys := km.Submit.Keys(); len(keys) != 0 {
		t.Fatalf("precondition: Submit override to empty chords should yield an empty binding, got %v", keys)
	}
	if got := keyMarkings(km).submit; got != "enter" {
		t.Errorf("empty Submit binding should fall back to the default marking %q, got %q", "enter", got)
	}
}
