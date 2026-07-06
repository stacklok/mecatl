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
func helpModel(t *testing.T, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{gate: make(chan struct{})}
	conv := &fakeConv{recv: recv, send: &fakeSender{}, caps: caps}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       aztec(),
		Server:      "127.0.0.1:8080",
		Workspace:   "/workspace",
		Mode:        "default",
		Model:       "mock-model",
		Ctx:         context.Background(),
		NoAltScreen: true,
	})
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
}

// m_helpBody renders just the help body for caps (no centering), for content
// assertions. Uses the default keys since the tests don't wire custom keymaps.
func m_helpBody(caps client.Capabilities) string {
	return helpBody(aztec(), caps, "ctrl+a", "home/end")
}
