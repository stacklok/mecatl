package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestWindowTitle is the table test over windowTitle(): each phase mapping, the
// empty-title fallback, the 40-rune clamp + ellipsis, the newline/tab collapse,
// escape-sequence sanitization, and the NoWindowTitle gate.
func TestWindowTitle(t *testing.T) {
	t.Parallel()

	// A title of exactly windowTitleRunes (40) runes must NOT be clamped.
	exact := strings.Repeat("x", windowTitleRunes)
	// A title one rune over the cap must clamp to 40 runes incl. the ellipsis.
	over := strings.Repeat("y", windowTitleRunes+1)

	cases := []struct {
		name   string
		model  Model
		want   string
		reason string
	}{
		{
			name: "running phase with title",
			model: Model{
				phase:        phaseRunning,
				sessionTitle: "fix the login bug",
			},
			want:   "fix the login bug — Working mecatui",
			reason: "phaseRunning → 'Working' word",
		},
		{
			name: "awaiting approval phase with title",
			model: Model{
				phase:        phaseAwaitingApproval,
				sessionTitle: "fix the login bug",
			},
			want:   "fix the login bug — ⚠ mecatui",
			reason: "phaseAwaitingApproval → '⚠' glyph",
		},
		{
			name: "connecting phase with title",
			model: Model{
				phase:        phaseConnecting,
				sessionTitle: "fix the login bug",
			},
			want:   "fix the login bug — Connecting mecatui",
			reason: "phaseConnecting → 'Connecting' word",
		},
		{
			name: "fatal phase with title",
			model: Model{
				phase:        phaseFatal,
				sessionTitle: "fix the login bug",
			},
			want:   "fix the login bug — ✗ mecatui",
			reason: "phaseFatal → '✗' glyph",
		},
		{
			name: "idle phase with title",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: "fix the login bug",
			},
			want:   "fix the login bug — mecatui",
			reason: "phaseIdle → no status word (title + 'mecatui')",
		},
		{
			name: "replay phase with title",
			model: Model{
				phase:        phaseReplay,
				sessionTitle: "fix the login bug",
			},
			want:   "fix the login bug — mecatui",
			reason: "phaseReplay → no status word (title + 'mecatui')",
		},
		{
			name: "running phase no title → bare app + status",
			model: Model{
				phase:        phaseRunning,
				sessionTitle: "",
			},
			want:   "Working mecatui",
			reason: "empty title + status → '<status> mecatui'",
		},
		{
			name: "idle phase no title → bare 'mecatui'",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: "",
			},
			want:   "mecatui",
			reason: "empty title + no status → bare 'mecatui'",
		},
		{
			name: "connecting phase no title → 'Connecting mecatui'",
			model: Model{
				phase:        phaseConnecting,
				sessionTitle: "",
			},
			want:   "Connecting mecatui",
			reason: "connecting (the launch phase) with no title still shows the word",
		},
		{
			name: "whitespace-only title → bare 'mecatui' at idle",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: "   \t\n  ",
			},
			want:   "mecatui",
			reason: "whitespace-only title collapses to empty → bare 'mecatui'",
		},
		{
			name: "exact 40-rune title not clamped",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: exact,
			},
			want:   exact + " — mecatui",
			reason: "40 runes fits the cap with no ellipsis",
		},
		{
			name: "over-cap title clamped to 40 runes incl ellipsis",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: over,
			},
			want:   strings.Repeat("y", windowTitleRunes-1) + "…" + " — mecatui",
			reason: "41 runes clamps to 40 (39 y's + ellipsis)",
		},
		{
			name: "newline/tab collapse to single spaces",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: "fix\nthe\t\tlogin\n\nbug",
			},
			want:   "fix the login bug — mecatui",
			reason: "newlines/tabs collapse to single spaces (one-line title)",
		},
		{
			name: "escape-sequence sanitization (OSC title injection)",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: "pwn\x1b]0;evil\x07title",
			},
			want:   "pwn]0;eviltitle — mecatui",
			reason: "ESC (0x1b) and BEL (0x07) stripped (C0/ESC/DEL removed by sanitizeTerminal); the remaining printable chars are inert",
		},
		{
			name: "NoWindowTitle gate collapses to bare 'mecatui' even with a title",
			model: Model{
				phase:        phaseRunning,
				deps:         Deps{NoWindowTitle: true},
				sessionTitle: "fix the login bug",
			},
			want:   "mecatui",
			reason: "NoWindowTitle=true → always bare 'mecatui'",
		},
		{
			name: "multibyte title clamp is rune-safe",
			model: Model{
				phase:        phaseIdle,
				sessionTitle: strings.Repeat("é", windowTitleRunes+2),
			},
			want:   strings.Repeat("é", windowTitleRunes-1) + "…" + " — mecatui",
			reason: "rune-safe clamp (no mid-character split on multibyte)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.model.windowTitle()
			if got != tc.want {
				t.Errorf("windowTitle() = %q, want %q (%s)", got, tc.want, tc.reason)
			}
		})
	}

	// Verify the over-cap clamp is rune-safe (the ellipsis counts toward the cap).
	got := (Model{phase: phaseIdle, sessionTitle: over}).windowTitle()
	titleSeg := strings.TrimSuffix(got, " — mecatui")
	if n := utf8.RuneCountInString(titleSeg); n != windowTitleRunes {
		t.Errorf("clamped title segment = %d runes, want %d (cap incl. ellipsis)", n, windowTitleRunes)
	}
}

// TestWindowTitleSeedsSetOnce asserts submitPrompt seeds the session title
// set-once: the FIRST prompt sticks, a SECOND prompt does NOT overwrite it.
func TestWindowTitleSeedsSetOnce(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-title-0001"},
	)
	// First prompt seeds the title.
	m = sendText(t, m, "refactor the auth module")
	if m.sessionTitle != "refactor the auth module" {
		t.Fatalf("after first prompt sessionTitle = %q, want the sent text", m.sessionTitle)
	}
	// A second prompt must NOT overwrite the set-once title.
	m = sendText(t, m, "now add tests")
	if m.sessionTitle != "refactor the auth module" {
		t.Errorf("after second prompt sessionTitle = %q, want the FIRST prompt to stick (set-once)", m.sessionTitle)
	}
}

// TestWindowTitleClearedByResetSession asserts resetSession clears the session
// title (a /clear wipes the session-derived label).
func TestWindowTitleClearedByResetSession(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-reset-0001"},
	)
	m = sendText(t, m, "original task")
	if m.sessionTitle != "original task" {
		t.Fatalf("setup: sessionTitle = %q, want 'original task'", m.sessionTitle)
	}
	m = m.resetSession()
	if m.sessionTitle != "" {
		t.Errorf("after resetSession sessionTitle = %q, want empty", m.sessionTitle)
	}
}

// TestWindowTitleAdoptsResolvedModelMsgTitle asserts onResolvedModelMsg adopts
// the msg's Title ONLY when the local title is still empty (self-heal), and
// drops a stale-session msg (SessionID mismatch).
func TestWindowTitleAdoptsResolvedModelMsgTitle(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-heal-0001"},
	)
	// No local title yet → adopt the server's stored title.
	m = applyAll(m, client.ResolvedModelMsg{
		SessionID: "sess-heal-0001",
		Title:     "carryover task from a fork",
	})
	if m.sessionTitle != "carryover task from a fork" {
		t.Fatalf("after self-heal sessionTitle = %q, want the msg title", m.sessionTitle)
	}
	// A second msg with a different title must NOT overwrite the set-once.
	m = applyAll(m, client.ResolvedModelMsg{
		SessionID: "sess-heal-0001",
		Title:     "different server title",
	})
	if m.sessionTitle != "carryover task from a fork" {
		t.Errorf("after second heal sessionTitle = %q, want the FIRST to stick (set-once)", m.sessionTitle)
	}
}

// TestWindowTitleDropsStaleSessionResolvedModelMsg asserts a ResolvedModelMsg
// whose SessionID no longer matches the current session is dropped (a stale
// refetch must not seed a stale title).
func TestWindowTitleDropsStaleSessionResolvedModelMsg(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-live-0001"},
	)
	// A stale-session msg (different SessionID) must be dropped entirely.
	m = applyAll(m, client.ResolvedModelMsg{
		SessionID: "sess-OTHER-0001",
		Title:     "stale title from a dead session",
	})
	if m.sessionTitle != "" {
		t.Errorf("stale-session msg seeded sessionTitle = %q, want empty (dropped)", m.sessionTitle)
	}
}

// TestWindowTitleSwitchToSessionAdoptsTitle asserts switchToSession adopts the
// picker's stored title verbatim (the full unclamped title).
func TestWindowTitleSwitchToSessionAdoptsTitle(t *testing.T) {
	fl := &fakeSessionLister{sessions: []client.SessionListItem{
		{ID: "sess-stored-0001", ModifiedAt: nowMinusMinutes(2), State: "completed", Turns: 2, ModelID: "m"},
	}}
	fr := &fakeSessionReplayer{stream: client.NewFakeEventStream()}
	conv := newSessionsConv()
	m := newSessionsModel(t, conv, fl, fr)

	longTitle := strings.Repeat("z", windowTitleRunes+10) // over the render cap
	fl.sessions[0].Title = longTitle
	mm, _, _ := m.switchToSession(fl.sessions[0])
	m = mm.(Model)
	if m.sessionTitle != longTitle {
		t.Errorf("after switchToSession sessionTitle = %q, want the full stored title verbatim", m.sessionTitle)
	}
	// And the render path clamps it (the stored title is unclamped; windowTitle clamps).
	got := m.windowTitle()
	if !strings.Contains(got, "…") {
		t.Errorf("windowTitle() = %q, want it to clamp the over-cap stored title with an ellipsis", got)
	}
}

// TestWindowTitleHealRefetchRoundTrip proves the applySessionReady self-heal
// END-TO-END: a SessionReadyMsg with no local title fires a GetSession refetch,
// and feeding the result back adopts the server's stored title. It mirrors
// TestFooterHealRaceThenHeal's cmd-execution pattern (execute the batched cmd
// tree, assert the fake's GetSession ran, feed the msg back through the
// reducer).
func TestWindowTitleHealRefetchRoundTrip(t *testing.T) {
	conv := &fakeConv{
		recv: &fakeRecver{}, send: &fakeSender{},
		getSessionTitle: "forked carryover task",
	}
	m := New(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         t.Context(),
		NoAltScreen: true,
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 120, Height: 30})

	// 1) A session-ready with NO local title must fire the heal refetch.
	mm, cmd := m.Update(client.SessionReadyMsg{SessionID: "sess-fork-0001"})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("applySessionReady with an empty title emitted no command — the heal refetch did not fire")
	}
	drainBatch(t, cmd())
	if n := conv.getSessionCalls(); n != 1 {
		t.Fatalf("GetSession called %d times after session-ready with empty title, want 1", n)
	}

	// 2) Feed the refetch result back: the title heals from the server's stored one.
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "sess-fork-0001", Title: "forked carryover task"})
	if m.sessionTitle != "forked carryover task" {
		t.Fatalf("after heal round-trip sessionTitle = %q, want the server's stored title", m.sessionTitle)
	}
	if got := m.windowTitle(); got != "forked carryover task — mecatui" {
		t.Errorf("windowTitle() = %q, want the healed title at idle", got)
	}

	// 3) A SECOND session-ready (a rebind) with the title now set must NOT refetch.
	before := conv.getSessionCalls()
	_, cmd = m.Update(client.SessionReadyMsg{SessionID: "sess-fork-0001"})
	if cmd != nil {
		drainBatch(t, cmd())
	}
	if n := conv.getSessionCalls(); n != before {
		t.Errorf("GetSession called again (total %d) with the title already set, want no refetch", n)
	}
}

// TestWindowTitleView asserts View() wires windowTitle() into the tea.View —
// deleting view.go's v.WindowTitle assignment would silently kill the feature
// while every helper-level test stays green. Driving through New + the real
// View (not the helper directly) pins the wiring.
func TestWindowTitleView(t *testing.T) {
	m, _, _ := newTestModel(t, theme.New("aztec", theme.AztecPalette()))
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 120, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-view-0001"},
	)
	// Before any prompt: bare app name.
	if got := m.View().WindowTitle; got != "mecatui" {
		t.Fatalf("View().WindowTitle = %q, want bare 'mecatui' with no title yet", got)
	}
	// Submit a prompt: the phase flips to running and the title leads.
	m = sendText(t, m, "investigate the flaky test")
	if got, want := m.View().WindowTitle, "investigate the flaky test — Working mecatui"; got != want {
		t.Errorf("View().WindowTitle = %q, want %q after first prompt (running)", got, want)
	}
	// Back at idle the status word drops but the title stays.
	m.phase = phaseIdle
	if got, want := m.View().WindowTitle, "investigate the flaky test — mecatui"; got != want {
		t.Errorf("View().WindowTitle = %q, want %q at idle", got, want)
	}
	// The opt-out pins the bare app name regardless.
	m.deps.NoWindowTitle = true
	if got := m.View().WindowTitle; got != "mecatui" {
		t.Errorf("View().WindowTitle = %q, want bare 'mecatui' under NoWindowTitle", got)
	}
}

// sendText types text into the textarea and submits it, exercising the
// submitPrompt reducer path (the same path a real enter-press takes).
func sendText(t *testing.T, m Model, text string) Model {
	t.Helper()
	m.ta.SetValue(text)
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return mm.(Model)
}
