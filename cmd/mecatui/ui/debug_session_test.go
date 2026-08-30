package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func debugUIModel(target string, width int) Model {
	m := New(Deps{Theme: theme.New("aztec", theme.AztecPalette()), Ctx: context.Background(), DebugTarget: target})
	m.width = width
	m.height = 20
	return m
}

func TestDebugRailIsPersistentSanitizedAndNarrowRecognizable(t *testing.T) {
	m := debugUIModel("target\x1b[31m\nopaque", 80)
	for _, p := range []phase{phaseConnecting, phaseIdle, phaseRunning, phaseAwaitingApproval, phaseFatal} {
		m.phase = p
		var rendered string
		if p == phaseFatal {
			rendered = m.View().Content
		} else {
			rendered = m.renderHeader()
		}
		plain := stripANSIstr(rendered)
		if !strings.Contains(plain, "DEBUG target: target[31m opaque") || strings.Contains(rendered, "\x1b[31m") {
			t.Fatalf("phase %v debug identity unsafe/missing: %q", p, plain)
		}
	}
	m.width = 7
	if got := stripANSIstr(m.renderDebugRail()); !strings.HasPrefix(got, "DEBUG") {
		t.Fatalf("narrow rail lost debug identity: %q", got)
	}
}

func TestDebugWindowTitleStartsWithStableDigestAcrossPhases(t *testing.T) {
	m := debugUIModel("target-session", 80)
	prefix := "DEBUG " + sessionDigest("target-session")[:8]
	for _, p := range []phase{phaseConnecting, phaseIdle, phaseRunning, phaseAwaitingApproval, phaseFatal} {
		m.phase = p
		if got := m.windowTitle(); !strings.HasPrefix(got, prefix) {
			t.Fatalf("phase %v title = %q", p, got)
		}
	}
}

func TestDebugBuiltinFilterHidesOnlyBindingBreakingControls(t *testing.T) {
	caps := client.Capabilities{MCP: true, Agents: true, Teams: true, Skills: true, Soul: true, UserModel: true, ModelSelection: true, Worktrees: true, Scheduling: true, Posture: "auto"}
	wired := wiredCollaborators{MCP: true, Agents: true, Skills: true, Soul: true, UserModel: true, Models: true, Worktrees: true, Scheduling: true, Sessions: true, Learning: true, DebugSession: true}
	var names []string
	for _, b := range builtinCommands(caps, wired) {
		names = append(names, b.name)
	}
	joined := "," + strings.Join(names, ",") + ","
	for _, forbidden := range []string{"clear", "models", "effort", "worktrees", "sessions"} {
		if strings.Contains(joined, ","+forbidden+",") {
			t.Fatalf("debug commands expose binding-breaking /%s: %v", forbidden, names)
		}
	}
	for _, harmless := range []string{"help", "session", "retry", "mcp", "agents", "schedule", "learning", "learning-sensitivity", "posture"} {
		if !strings.Contains(joined, ","+harmless+",") {
			t.Fatalf("debug commands hid harmless /%s: %v", harmless, names)
		}
	}
	m := debugUIModel("target", 80)
	m.phase = phaseIdle
	before := m.desiredMode()
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'm', Mod: tea.ModAlt})
	if got := mm.(Model).desiredMode(); got != before {
		t.Fatalf("mode shortcut changed dedicated debugger mode: %q -> %q", before, got)
	}
}
