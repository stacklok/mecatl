package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// Tests for the /soul persona INSPECTION panel: a read-only, idle-only, SCROLLABLE
// overlay that fires GetSoul and renders the resolved persona (metadata line +
// scroll-windowed content). The soul is agent-read-only; the panel never edits it.

// newSoulModel builds an idle, sized Model wired to the given fakeSoul and caps,
// ready to open the /soul panel. It mirrors newSkillsModel/newAgentsInvModel.
func newSoulModel(t *testing.T, sf client.SoulFetcher, caps client.Capabilities) Model {
	t.Helper()
	recv := &fakeRecver{script: nil, gate: make(chan struct{})}
	send := &fakeSender{}
	conv := &fakeConv{recv: recv, send: send, caps: caps}
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Soul:        sf,
		Theme:       theme.New("aztec", theme.AztecPalette()),
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
	return m
}

func sampleSoul() *fakeSoul {
	return &fakeSoul{soul: client.Soul{
		Content:    "You are terse and direct.\nYou prefer Go.",
		SizeBytes:  40,
		SHA256:     "abc123def4567890",
		Present:    true,
		Provenance: client.SoulProvenanceUser,
		Trusted:    true,
	}}
}

// soulActive returns the open soul modal (or nil) off the Model, so a test can
// read the migrated state without holding a soulState field. It asserts the modal
// IS a *soulState, which pins the open path too.
func soulActive(m Model) *soulState {
	if m.modal == nil {
		return nil
	}
	s, ok := m.modal.(*soulState)
	if !ok {
		return nil
	}
	return s
}

// TestRunSoulOpensPanel asserts runSoul opens the panel, blurs the input, fires
// GetSoul, and renders the persona once the result lands.
func TestRunSoulOpensPanel(t *testing.T) {
	fs := sampleSoul()
	m := newSoulModel(t, fs, client.Capabilities{Soul: true})

	mm, cmd := m.runSoul()
	m = mm.(Model)
	st := soulActive(m)
	if st == nil || st.view != soulPanel {
		t.Fatalf("modal surface = %v, want a *soulState at soulPanel", m.modal)
	}
	if !st.loading {
		t.Error("panel should be loading until the RPC result lands")
	}
	if m.prompt.Focused() {
		t.Error("opening the panel should blur the textarea")
	}
	if cmd == nil {
		t.Fatal("runSoul should fire the GetSoul RPC command")
	}

	m = feedCmd(t, m, cmd)
	if fs.calls != 1 {
		t.Errorf("GetSoul calls = %d, want 1", fs.calls)
	}
	if !strings.Contains(m.View().Content, "terse and direct") {
		t.Errorf("rendered panel missing soul content:\n%s", m.View().Content)
	}
}

// TestSoulOpenGuards asserts the panel only opens while idle with a soul fetcher.
func TestSoulOpenGuards(t *testing.T) {
	// nil fetcher → no-op.
	m := newSoulModel(t, nil, client.Capabilities{Soul: true})
	m.deps.Soul = nil
	mm, cmd := m.runSoul()
	if m.modal != nil || cmd != nil {
		t.Error("runSoul with no fetcher must be a no-op")
	}
	_ = mm
}

// keyPress routes a key through the ACTIVE soul surface's HandleKey (the new
// routing; onOverlayKey's m.modal arm), returning the handled/closed flags.
// It mirrors the old m.onSoulKey test helper but goes through the surface.
func keySoul(m Model, msg tea.KeyPressMsg) (Model, tea.Cmd, bool, bool) {
	if m.modal == nil {
		return m, nil, false, false
	}
	s := m.modal
	cmd, handled, closed := s.HandleKey(msg)
	if handled && closed {
		s.Close()
		m.modal = nil
		return m, tea.Batch(cmd, m.prompt.Focus()), true, true
	}
	return m, cmd, handled, closed
}

// TestSoulScroll asserts the scroll keys move (and clamp) the content window AND
// that the rendered window content + the "lines X–Y of N" indicator actually shift.
// The lines are UNIQUE ("line-00".."line-29") so a render bug that ignored st.scroll
// (always showing the first window) would be caught — an identical-line fixture would
// pass such a bug silently.
func TestSoulScroll(t *testing.T) {
	// 30 unique lines so the content exceeds soulBodyLines (12) and each window is
	// distinguishable.
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	fs := &fakeSoul{soul: client.Soul{Content: strings.Join(lines, "\n"), Present: true, Provenance: client.SoulProvenanceUser, Trusted: true, SizeBytes: 100}}
	m := newSoulModel(t, fs, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)

	if s := soulActive(m); s == nil || s.scroll != 0 {
		t.Fatalf("initial scroll want 0, got %+v", s)
	}
	// At the top: window is lines 1–12 (line-00..line-11); the indicator reflects that
	// and the last lines are NOT yet visible.
	top := stripANSIstr(m.View().Content)
	if !strings.Contains(top, "line-00") || strings.Contains(top, "line-29") {
		t.Errorf("top window should show line-00 and NOT line-29, got:\n%s", top)
	}
	if !strings.Contains(top, "lines 1–12 of 30") {
		t.Errorf("top indicator should read 'lines 1–12 of 30', got:\n%s", top)
	}

	// Page down once: the window shifts by one. line-00 leaves the top, line-12 enters.
	m, _, _, _ = keySoul(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s := soulActive(m); s == nil || s.scroll != 1 {
		t.Errorf("scroll after pgdown want 1, got %+v", s)
	}
	pd := stripANSIstr(m.View().Content)
	if strings.Contains(pd, "line-00") {
		t.Errorf("after pgdown the window should no longer show line-00, got:\n%s", pd)
	}
	if !strings.Contains(pd, "line-12") {
		t.Errorf("after pgdown the window should reveal line-12, got:\n%s", pd)
	}
	if !strings.Contains(pd, "lines 2–13 of 30") {
		t.Errorf("after pgdown the indicator should read 'lines 2–13 of 30', got:\n%s", pd)
	}

	// Jump to bottom; max scroll = 30 - 12 = 18. The window shows the LAST lines and the
	// indicator ends at 30.
	m, _, _, _ = keySoul(m, tea.KeyPressMsg{Code: tea.KeyEnd})
	if s := soulActive(m); s == nil || s.scroll != 18 {
		t.Errorf("scroll after End want 18 (30-12), got %+v", s)
	}
	bot := stripANSIstr(m.View().Content)
	if !strings.Contains(bot, "line-29") || strings.Contains(bot, "line-00") {
		t.Errorf("bottom window should show line-29 and NOT line-00, got:\n%s", bot)
	}
	if !strings.Contains(bot, "lines 19–30 of 30") {
		t.Errorf("bottom indicator should read 'lines 19–30 of 30', got:\n%s", bot)
	}

	// Pgdown past the end clamps (offset + window + indicator unchanged).
	m, _, _, _ = keySoul(m, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if s := soulActive(m); s == nil || s.scroll != 18 {
		t.Errorf("scroll clamps at 18, got %+v", s)
	}
	if !strings.Contains(stripANSIstr(m.View().Content), "lines 19–30 of 30") {
		t.Error("clamped window indicator should still read 'lines 19–30 of 30'")
	}

	// Page up moves back (pins the ScrollU arm — removing it must fail here).
	m, _, _, _ = keySoul(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if s := soulActive(m); s == nil || s.scroll != 17 {
		t.Errorf("scroll after pgup want 17, got %+v", s)
	}

	// Home returns to the top: window + indicator reset.
	m, _, _, _ = keySoul(m, tea.KeyPressMsg{Code: tea.KeyHome})
	if s := soulActive(m); s == nil || s.scroll != 0 {
		t.Errorf("scroll after Home want 0, got %+v", s)
	}
	home := stripANSIstr(m.View().Content)
	if !strings.Contains(home, "line-00") || !strings.Contains(home, "lines 1–12 of 30") {
		t.Errorf("after Home the window should reset to line-00 / 'lines 1–12 of 30', got:\n%s", home)
	}
}

// TestSoulScrollViaUpdate routes a scroll key through the REAL m.Update path
// (onKey → onOverlayKey → dispatchSurfaceKey → HandleKey), NOT the keySoul helper
// that calls HandleKey directly and bypasses the dispatchSurfaceKey routing. It
// pins that a scroll key reaches the open modal through the full dispatch and
// moves the window without closing the panel. (TestSoulScroll above exercises the
// full key set via the direct helper; this one owns the m.Update coverage.)
func TestSoulScrollViaUpdate(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	fs := &fakeSoul{soul: client.Soul{Content: strings.Join(lines, "\n"), Present: true, Provenance: client.SoulProvenanceUser, Trusted: true, SizeBytes: 100}}
	m := newSoulModel(t, fs, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	if s := soulActive(m); s == nil || s.scroll != 0 {
		t.Fatalf("initial scroll want 0, got %+v", s)
	}

	mm2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = mm2.(Model)
	if s := soulActive(m); s == nil {
		t.Fatal("pgdown through m.Update must not close the panel")
	} else if s.scroll != 1 {
		t.Errorf("scroll after pgdown via m.Update want 1, got %d", s.scroll)
	}
	pd := stripANSIstr(m.View().Content)
	if strings.Contains(pd, "line-00") {
		t.Errorf("after pgdown the window should no longer show line-00, got:\n%s", pd)
	}
	if !strings.Contains(pd, "line-12") {
		t.Errorf("after pgdown the window should reveal line-12, got:\n%s", pd)
	}
}

// TestSoulWheelConsumedWhileOpen asserts a mouse wheel while the soul modal is
// open is CONSUMED by the modal (the default-consume contract: handled=true),
// never delegates to the conversation viewport. It pins the onMouseWheel
// m.modal routing arm (update.go): handled=true consumes, and the panel stays
// open while the viewport stays put.
func TestSoulWheelConsumedWhileOpen(t *testing.T) {
	m := newSoulModel(t, sampleSoul(), client.Capabilities{Soul: true})
	// Long transcript so the viewport WOULD be scrollable if the wheel fell
	// through; start stuck at the bottom like a fresh stream.
	m.conv.addUser("show me a long answer")
	m.conv.appendAssistant(strings.Repeat("line of streamed output\n", 120))
	m.phase = phaseIdle
	m.conversationView.mode = followTail
	m.refreshView()
	if !m.vp.AtBottom() {
		t.Fatal("precondition: viewport should start at the bottom")
	}

	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	if soulActive(m) == nil {
		t.Fatal("precondition: soul panel should be open")
	}

	// Wheel up through the REAL Update path (onMouseMsg → onMouseWheel → the
	// m.modal.HandleWheel arm → CONSUMED by soul).
	mm2, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = mm2.(Model)
	if soulActive(m) == nil {
		t.Fatal("a consumed wheel must not close the soul panel")
	}
	if m.conversationView.mode != followTail {
		t.Error("a consumed wheel-up must NOT unstick the view (the modal eats it)")
	}
	if !m.vp.AtBottom() {
		t.Error("a consumed wheel-up must NOT scroll the viewport (the modal eats it)")
	}
}

// TestSoulDriftedLabel asserts a drifted user or project soul explains that it
// changed since Mecatl last recorded it.
func TestSoulDriftedLabel(t *testing.T) {
	for _, provenance := range []client.SoulProvenance{client.SoulProvenanceUser, client.SoulProvenanceProject} {
		fs := &fakeSoul{soul: client.Soul{
			Content:    "You are terse.",
			SizeBytes:  14,
			Present:    true,
			Provenance: provenance,
			Trusted:    true,
			Drifted:    true,
		}}
		m := newSoulModel(t, fs, client.Capabilities{Soul: true})
		mm, cmd := m.runSoul()
		m = feedCmd(t, mm.(Model), cmd)
		if !strings.Contains(stripANSIstr(m.View().Content), "changed since Mecatl last recorded it") {
			t.Errorf("a drifted %v soul should explain that it changed, got:\n%s", provenance, stripANSIstr(m.View().Content))
		}
	}
}

// TestSoulUntrustedProjectLabel asserts a dropped untrusted project soul (present
// false, trusted false) explains that the project is not trusted — the trust-gate
// state the user must see (the soulTrustLabel untrusted-project branch).
func TestSoulUntrustedProjectLabel(t *testing.T) {
	fs := &fakeSoul{soul: client.Soul{
		SizeBytes:  120,
		Present:    false,
		Provenance: client.SoulProvenanceProject,
		Trusted:    false,
	}}
	m := newSoulModel(t, fs, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	body := stripANSIstr(m.View().Content)
	if !strings.Contains(body, "not loaded because this workspace is not trusted") {
		t.Errorf("a dropped untrusted project soul should explain why it is unavailable, got:\n%s", body)
	}
}

// TestSoulKeySwallowsNonEsc asserts a non-esc, non-scroll key while the panel is
// open is swallowed (handled=true) so it never leaks into idle input.
func TestSoulKeySwallowsNonEsc(t *testing.T) {
	m := newSoulModel(t, sampleSoul(), client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)

	_, _, handled, _ := keySoul(m, tea.KeyPressMsg{Code: 'x'})
	if !handled {
		t.Error("a non-esc key while the panel is open should be swallowed (handled=true)")
	}
}

// TestSoulEscClosesPanel asserts esc closes the panel via the REAL m.Update path
// (onKey → onOverlayKey → dispatchSurfaceKey → HandleKey → closed), restoring idle
// input. It mirrors TestAgentsInvEscClosesPanel but — unlike the agents close path,
// which calls m.prompt.Focus() directly inside the handler — the surface close path
// returns the focusInput cmd from Close, so feedCmd must run it for focus to land.
func TestSoulEscClosesPanel(t *testing.T) {
	m := newSoulModel(t, sampleSoul(), client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	if soulActive(m) == nil {
		t.Fatal("precondition: panel should be open")
	}

	mm2, escCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = mm2.(Model)
	// Close calls Focus synchronously; escCmd only schedules the delayed widget blink.
	_ = escCmd
	if m.modal != nil {
		t.Fatalf("esc did not close the panel: modal=%v", m.modal)
	}
	if !m.prompt.Focused() {
		t.Error("esc should restore focus to the textarea")
	}
}

// TestSoulError asserts a GetSoul error renders distinctly.
func TestSoulError(t *testing.T) {
	fs := &fakeSoul{err: errors.New("boom")}
	m := newSoulModel(t, fs, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	st := soulActive(m)
	if st == nil || st.err == nil {
		t.Fatal("a GetSoul error should be recorded on the open modal")
	}
	if !strings.Contains(m.View().Content, "boom") {
		t.Errorf("error not surfaced in the panel:\n%s", m.View().Content)
	}
}

// --- goldens ---------------------------------------------------------------

// TestSoulPanelGolden locks the populated, scrollable persona panel.
func TestSoulPanelGolden(t *testing.T) {
	m := newSoulModel(t, sampleSoul(), client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	if s := soulActive(m); s == nil || s.view != soulPanel {
		t.Fatalf("modal surface = %v, want a *soulState at soulPanel", m.modal)
	}
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "soul.golden", got)
}

// TestSoulPanelEmptyDisabledGolden locks the "soul not enabled" empty state
// (caps.Soul false; no soul present).
func TestSoulPanelEmptyDisabledGolden(t *testing.T) {
	m := newSoulModel(t, &fakeSoul{}, client.Capabilities{Soul: false})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "soul_empty_disabled.golden", got)
}

// TestSoulPanelEmptyEnabledGolden locks the "enabled but no soul present" empty
// state (caps.Soul true, no soul present).
func TestSoulPanelEmptyEnabledGolden(t *testing.T) {
	m := newSoulModel(t, &fakeSoul{}, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "soul_empty_enabled.golden", got)
}

// TestSoulPanelProjectGolden locks the provenance-aware project copy.
func TestSoulPanelProjectGolden(t *testing.T) {
	project := &fakeSoul{soul: client.Soul{
		Content:    "Follow this repository's conventions.",
		SizeBytes:  37,
		SHA256:     "fedcba9876543210",
		Present:    true,
		Provenance: client.SoulProvenanceProject,
		Trusted:    true,
	}}
	m := newSoulModel(t, project, client.Capabilities{Soul: true})
	mm, cmd := m.runSoul()
	m = feedCmd(t, mm.(Model), cmd)
	got := stripANSI([]byte(m.View().Content))
	compareGolden(t, "soul_project.golden", got)
}

func TestSoulPanelOwnershipCopyFollowsProvenance(t *testing.T) {
	hk := helpKeys{scroll: "pgup/pgdn", closeOnly: "esc"}
	for _, tc := range []struct {
		name, footer string
		soul         client.Soul
	}{
		{"user", "you control this soul", client.Soul{Provenance: client.SoulProvenanceUser}},
		{"project", "controlled by this project", client.Soul{Provenance: client.SoulProvenanceProject, Present: true, Trusted: true}},
		{"untrusted project", "controlled by this project", client.Soul{Provenance: client.SoulProvenanceProject, Present: false, Trusted: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := soulPanelFooter(tc.soul, hk); !strings.Contains(got, tc.footer) {
				t.Fatalf("footer = %q, want %q", got, tc.footer)
			}
		})
	}
	if got := soulTrustLabel(client.Soul{Provenance: client.SoulProvenanceProject, Present: false, Trusted: false}); got != "project · not loaded because this workspace is not trusted" {
		t.Fatalf("untrusted project label = %q", got)
	}
}
