package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// adr0365Model is a live model bound to a session whose server reports posture.
func adr0365Model(t *testing.T, conv *fakeConv, posture string, embedded bool) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session:  conv,
		Conv:     conv,
		Theme:    theme.New("aztec", theme.AztecPalette()),
		Mode:     "default",
		Embedded: embedded,
		Ctx:      context.Background(),
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 140, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-test-0001", Mode: "default", Capabilities: client.Capabilities{Posture: posture}},
	)
}

// cycleMode presses the real ModeSwitch chord and feeds the resulting SetMode
// command back through Update, as the Bubble Tea runtime would.
func cycleMode(t *testing.T, m Model) Model {
	t.Helper()
	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("mode switch chord issued no SetMode command")
	}
	return applyAll(m, cmd())
}

// TestADR_0365_ModeSwitchCycleUnchanged pins AC5.1: the session mode cycle offers
// exactly the three session modes in the same order as before ADR 0365. No
// posture-bearing token becomes reachable from the keybinding.
func TestADR_0365_ModeSwitchCycleUnchanged(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := adr0365Model(t, conv, postureStrict, true)
	want := []string{"plan", "accept-edits", "default", "plan"}
	for range want {
		m = cycleMode(t, m)
	}
	got := conv.setModes()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SetMode cycle = %v, want %v", got, want)
	}
	if m.activeMode != "plan" {
		t.Fatalf("active mode after cycle = %q, want plan", m.activeMode)
	}
}

// TestADR_0365_HelpOverlayShowsVocabularyAndScopeSplit pins AC5.2: every token is
// listed with its posture half marked process-wide and its session half marked as
// the new-session default, with the exact invocation and the statement that
// cycling the session mode never changes the posture half.
func TestADR_0365_HelpOverlayShowsVocabularyAndScopeSplit(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := adr0365Model(t, conv, postureStrict, true)
	body := stripANSIstr(renderHelpOverlay(m.deps.Theme, m.caps, 200, 0, 0, m.helpKeyMarkings(), m.deps.Embedded))

	// The ADR 0365 table, written out independently of PermissionModeVocabulary.
	want := []struct{ token, posture, session string }{
		{"plan", "strict", "plan"},
		{"default", "strict", "default"},
		{"accept-edits", "strict", "accept-edits"},
		{"trusted", "trusted", "default"},
		{"trusted-accept-edits", "trusted", "accept-edits"},
		{"auto", "auto", "default"},
		{"yolo", "yolo", "default"},
	}
	for _, w := range want {
		row := "posture " + w.posture + " (process-wide) · new sessions start in " + w.session
		found := false
		for _, line := range strings.Split(body, "\n") {
			fields := strings.Fields(strings.Trim(line, "┃ "))
			if len(fields) > 0 && fields[0] == w.token && strings.Contains(line, row) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help overlay has no row for token %q with %q", w.token, row)
		}
	}
	for _, phrase := range []string{
		"--permission-mode",
		"The posture half applies to the whole server and is fixed when it starts.",
		"The session half is only the default for new sessions.",
		"Cycling the session mode (" + m.helpKeyMarkings().modeSwitch + ") never changes the posture half.",
		"mecatui --permission-mode <token>",
	} {
		if !strings.Contains(body, phrase) {
			t.Errorf("help overlay missing %q", phrase)
		}
	}
}

// TestADR_0365_HeaderShowsPostureWhenAboveStrict pins AC5.3: the header shows the
// session mode, and a posture badge for every tier above strict (trusted now
// included), sourced from the server-reported caps.Posture.
func TestADR_0365_HeaderShowsPostureWhenAboveStrict(t *testing.T) {
	cases := []struct {
		posture string
		badge   string // "" = no badge
	}{
		{postureStrict, ""},
		{postureTrusted, trustedBadgeText},
		{postureAuto, "⚠ auto"},
		{postureYolo, "YOLO"},
	}
	for _, tc := range cases {
		t.Run(tc.posture, func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
			m := adr0365Model(t, conv, tc.posture, true)
			header := stripANSIstr(m.renderHeader())
			if !strings.Contains(header, "mode default") {
				t.Errorf("header missing the session mode: %q", header)
			}
			_, _, present := m.postureBadgeRender()
			if tc.badge == "" {
				if present || strings.Contains(header, "posture") || strings.Contains(header, "strict") {
					t.Errorf("strict must render no posture badge, got %q", header)
				}
				return
			}
			if !present || !strings.Contains(header, tc.badge) {
				t.Errorf("posture %s: header missing badge %q: %q", tc.posture, tc.badge, header)
			}
		})
	}
}

// TestADR_0365_HeaderKeepsPostureAcrossModeCycle pins AC5.4: under auto, cycling
// the session mode through plan and back to default keeps the auto badge on every
// frame, including the pending frame before the server confirms each switch.
func TestADR_0365_HeaderKeepsPostureAcrossModeCycle(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := adr0365Model(t, conv, postureAuto, true)
	assertAuto := func(step, wantMode string) {
		t.Helper()
		header := stripANSIstr(m.renderHeader())
		if !strings.Contains(header, autoBadgeText) {
			t.Fatalf("%s: header lost the auto posture: %q", step, header)
		}
		if !strings.Contains(header, "mode "+wantMode) {
			t.Fatalf("%s: header mode = %q, want mode %s", step, header, wantMode)
		}
	}
	assertAuto("start", "default")
	for _, next := range []string{"plan", "accept-edits", "default"} {
		mm, cmd := m.Update(modeKey())
		m = mm.(Model)
		if cmd == nil {
			t.Fatalf("switch to %s issued no command", next)
		}
		assertAuto("pending "+next, next+" pending")
		m = applyAll(m, cmd())
		assertAuto("confirmed "+next, next)
	}
}

// TestADR_0365_HelpOverlayNamesWhoseRestart pins AC5.5: the embedded server's
// help gives the mecatui relaunch invocation; under `mecatui connect` it names
// the server operator's mecated restart and offers no local invocation.
func TestADR_0365_HelpOverlayNamesWhoseRestart(t *testing.T) {
	render := func(embedded bool) string {
		conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
		m := adr0365Model(t, conv, postureAuto, embedded)
		return stripANSIstr(renderHelpOverlay(m.deps.Theme, m.caps, 200, 0, 0, m.helpKeyMarkings(), m.deps.Embedded))
	}
	embedded := render(true)
	if !strings.Contains(embedded, "quit and relaunch: mecatui --permission-mode <token>") {
		t.Errorf("embedded help missing the mecatui relaunch invocation:\n%s", embedded)
	}
	if strings.Contains(embedded, "server operator") {
		t.Errorf("embedded help must not defer to a server operator:\n%s", embedded)
	}
	connect := render(false)
	if !strings.Contains(connect, "the server operator must change mecated's configuration and restart it") {
		t.Errorf("connect help missing the server-operator guidance:\n%s", connect)
	}
	if strings.Contains(connect, "mecatui --permission-mode") || strings.Contains(connect, "relaunch") {
		t.Errorf("connect help must offer no local invocation:\n%s", connect)
	}
}

// TestModeServerDefaultRequestsUnspecifiedAndReadsBack pins the ADR 0365 session
// half under ModeServerDefault: the first create requests no mode, so the server's
// default applies, and the header shows the mode the server reports rather than
// guessing "default".
func TestModeServerDefaultRequestsUnspecifiedAndReadsBack(t *testing.T) {
	// The fake keeps its current mode when a create requests none, so "plan" here
	// stands for a server whose configured default is plan.
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "plan"}
	m := newTestModelFromDeps(Deps{
		Session:           conv,
		Conv:              conv,
		Theme:             theme.New("aztec", theme.AztecPalette()),
		ModeServerDefault: true,
		Ctx:               context.Background(),
	})
	if got := m.desiredMode(); got != "" {
		t.Fatalf("desiredMode before any session = %q, want empty (server default)", got)
	}
	msg := m.createSessionCmd()()
	ready, ok := msg.(client.SessionReadyMsg)
	if !ok {
		t.Fatalf("create returned %T, want SessionReadyMsg", msg)
	}
	if ready.Mode != "plan" {
		t.Fatalf("ready mode = %q, want the server-reported plan", ready.Mode)
	}
	if conv.mode != "plan" {
		t.Fatalf("create overrode the server default with %q", conv.mode)
	}
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30}, ready)
	if header := stripANSIstr(m.renderHeader()); !strings.Contains(header, "mode plan") {
		t.Fatalf("header = %q, want mode plan", header)
	}
	// Once known, later creates carry the current mode explicitly, as before.
	if got := m.desiredMode(); got != "plan" {
		t.Fatalf("desiredMode after ready = %q, want plan", got)
	}
}
