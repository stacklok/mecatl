package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// permissionModeTestModel is a live model bound to a session whose server reports posture.
func permissionModeTestModel(t *testing.T, conv *fakeConv, posture string, embedded bool) Model {
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
		client.SessionReadyMsg{SessionID: "sess-test-0001", Mode: "default", Capabilities: client.Capabilities{Posture: posture, SessionPermissionModes: true}},
	)
}

// statusMode is the session mode the header's status surface renders as "mode X".
func statusMode(m Model) string {
	return m.statusLineInput(time.Now()).Session.Mode
}

// pressModeSwitch presses the real ModeSwitch chord, feeds any resulting SetMode
// command back through Update as the Bubble Tea runtime would, and reports
// whether a command was issued.
func pressModeSwitch(t *testing.T, m Model) (Model, bool) {
	t.Helper()
	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		return m, false
	}
	return applyAll(m, cmd()), true
}

// modeCycleStep is one ModeSwitch press: the mode the cycle cursor lands on,
// whether it applies a session half (issues SetMode), and phrases the footer
// status must carry. stays is set when the mode is unavailable (above the server
// ceiling): the confirmed mode the header must keep showing.
type modeCycleStep struct {
	token   string
	setMode bool
	stays   string
	status  []string
}

// visibleModeText is the footer status with wrapping collapsed so a phrase can
// be matched across a wrapped line.
func visibleModeText(m Model) string {
	return strings.Join(strings.Fields(stripANSIstr(m.statusMsg)), " ")
}

func runModeCycle(t *testing.T, m Model, steps []modeCycleStep) Model {
	t.Helper()
	for i, s := range steps {
		var issued bool
		m, issued = pressModeSwitch(t, m)
		if issued != s.setMode {
			t.Fatalf("step %d (%s): SetMode issued = %v, want %v", i, s.token, issued, s.setMode)
		}
		if got := m.currentModeToken(); got != s.token {
			t.Fatalf("step %d: landed on %q, want %q", i, got, s.token)
		}
		wantHeader := s.token
		if s.stays != "" {
			wantHeader = s.stays
		}
		if got := statusMode(m); got != wantHeader {
			t.Fatalf("step %d: header mode = %q, want %q", i, got, wantHeader)
		}
		text := visibleModeText(m)
		if s.stays != "" {
			want := "mode " + s.token + " unavailable, staying in " + s.stays
			if !strings.Contains(text, want) {
				t.Fatalf("step %d (%s): status %q missing %q", i, s.token, text, want)
			}
		}
		for _, want := range s.status {
			if !strings.Contains(text, want) {
				t.Fatalf("step %d (%s): status %q missing %q", i, s.token, text, want)
			}
		}
	}
	return m
}

// TestModeSwitchCyclesEveryMode pins that under strict the cycle lands on all
// five modes. The posture-raising ones (auto, yolo) change nothing and say what
// to configure and restart; the strict ones apply live.
func TestModeSwitchCyclesEveryMode(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureStrict, true)
	m = runModeCycle(t, m, []modeCycleStep{
		{token: "plan", setMode: true, status: []string{"mode plan"}},
		{token: "accept-edits", setMode: true, status: []string{"mode accept-edits"}},
		{token: "auto", stays: "accept-edits", status: []string{"enforcing guardrails checker", "guardrails.model", "mecatui --permission-mode auto", "shift+tab next mode"}},
		{token: "yolo", stays: "accept-edits", status: []string{"mecatui --permission-mode yolo"}},
		{token: "default", setMode: true, status: []string{"mode default"}},
		{token: "plan", setMode: true},
	})
	want := []string{"plan", "accept-edits", "default", "plan"}
	if got := conv.setModes(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SetMode calls = %v, want %v", got, want)
	}
	if m.activeMode != "plan" {
		t.Fatalf("active mode after cycle = %q, want plan", m.activeMode)
	}
}

// TestModeSwitchUnderTrustedPosture pins that a server running with the legacy
// trusted posture (reachable via deprecated --posture trusted) handles the
// 5-token vocabulary correctly: strict-posture tokens apply their session half,
// posture-raising tokens (auto, yolo) are unavailable.
// Since "trusted" is no longer a named token, the start position is the session
// mode itself ("default"), and cycling visits all five tokens.
func TestModeSwitchUnderTrustedPosture(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureTrusted, true)
	// No vocabulary entry for posture=trusted; acceptedModeToken falls back to
	// the session mode string.
	if got := m.currentModeToken(); got != "default" {
		t.Fatalf("start position = %q, want default (no trusted token in vocabulary)", got)
	}
	runModeCycle(t, m, []modeCycleStep{
		{token: "plan", setMode: true},
		{token: "accept-edits", setMode: true},
		// auto and yolo raise posture above trusted → unavailable (need restart).
		{token: "auto", stays: "accept-edits", status: []string{"auto also needs an enforcing guardrails checker"}},
		{token: "yolo", stays: "accept-edits", status: []string{"mecatui --permission-mode yolo"}},
		// In the new architecture, switching to a lower-ceiling mode is fully effective:
		// the session mode drives allow-all, so "posture stays" no longer applies.
		{token: "default", setMode: true, status: []string{"mode default"}},
		{token: "plan", setMode: true},
	})
	want := []string{"plan", "accept-edits", "default", "plan"}
	if got := conv.setModes(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SetMode calls = %v, want %v", got, want)
	}
}

// TestModeSwitchConnectNamesServerRestart pins that under `mecatui connect` a
// posture-raising mode points at the remote server's operator, not a relaunch.
func TestModeSwitchConnectNamesServerRestart(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "accept-edits"}
	m := permissionModeTestModel(t, conv, postureStrict, false)
	m.activeMode = "accept-edits"
	m = runModeCycle(t, m, []modeCycleStep{
		{token: "auto", stays: "accept-edits", status: []string{"remote server's operator must restart mecated with --permission-mode auto and an enforcing guardrails checker"}},
		{token: "yolo", stays: "accept-edits", status: []string{"remote server's operator must restart mecated with --permission-mode yolo"}},
	})
	if text := visibleModeText(m); strings.Contains(text, "mecatui --permission-mode") {
		t.Fatalf("connect hint offers a local relaunch: %q", text)
	}
	if got := conv.setModes(); len(got) != 0 {
		t.Fatalf("SetMode calls = %v, want none", got)
	}
}

// TestUnavailableModeKeepsComposerUsable pins that landing on a mode above the
// server ceiling freezes nothing: at idle or mid-run, typing and paste still edit
// the draft, the input area still shows it, enter still reaches the send/queue
// path, the header keeps the confirmed mode, and the next press moves on.
func TestUnavailableModeKeepsComposerUsable(t *testing.T) {
	for _, phase := range []phase{phaseIdle, phaseRunning} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
			m := permissionModeTestModel(t, conv, postureStrict, true)
			m.prompt.Rewrite("keep this draft")
			m = runModeCycle(t, m, []modeCycleStep{
				{token: "plan", setMode: true},
				{token: "accept-edits", setMode: true},
				{token: "auto", stays: "accept-edits"},
			})
			m.phase = phase

			m = applyAll(m, tea.KeyPressMsg{Code: 'x', Text: "x"}, tea.PasteMsg{Content: " pasted"})
			if got := m.prompt.Value(); got != "keep this draftx pasted" {
				t.Fatalf("prompt = %q, want typing and paste to reach the composer", got)
			}
			if !strings.Contains(stripANSIstr(m.renderInput()), "keep this draftx pasted") {
				t.Fatalf("input area does not show the draft: %q", stripANSIstr(m.renderInput()))
			}
			if got := statusMode(m); got != "accept-edits" {
				t.Fatalf("header mode = %q, want the confirmed accept-edits", got)
			}

			mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			m = mm.(Model)
			if m.prompt.Value() != "" || (cmd == nil && len(m.queued) == 0) {
				t.Fatalf("enter did not send or queue the draft: prompt=%q queued=%v cmd=%v", m.prompt.Value(), m.queued, cmd != nil)
			}

			m = runModeCycle(t, m, []modeCycleStep{{token: "yolo", stays: "accept-edits"}})
			if got := conv.setModes(); strings.Join(got, ",") != "plan,accept-edits" {
				t.Fatalf("SetMode calls = %v, want only the two applied modes", got)
			}
		})
	}
}

// TestUnavailableModeDoesNotHoldQueuedPrompt pins that a run ending while the
// cycle sits on an unavailable mode still auto-sends the queued follow-up.
func TestUnavailableModeDoesNotHoldQueuedPrompt(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureStrict, true)
	m = runModeCycle(t, m, []modeCycleStep{
		{token: "plan", setMode: true},
		{token: "accept-edits", setMode: true},
		{token: "auto", stays: "accept-edits"},
	})
	m.queued = []string{"queued follow-up"}
	mm, cmd := m.drainQueue("end_turn")
	m = mm.(Model)
	if cmd == nil || m.queuePaused != "" || m.phase != phaseRunning {
		t.Fatalf("drain: cmd=%v paused=%q phase=%v, want the follow-up sent", cmd != nil, m.queuePaused, m.phase)
	}
}

// TestUnavailableModeHintNamesCheckerOnlyForAuto pins that only auto's
// unavailable hint mentions the guardrails checker; yolo needs only the restart.
func TestUnavailableModeHintNamesCheckerOnlyForAuto(t *testing.T) {
	for _, embedded := range []bool{true, false} {
		t.Run(fmt.Sprintf("embedded=%v", embedded), func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "accept-edits"}
			m := permissionModeTestModel(t, conv, postureStrict, embedded)
			m.activeMode = "accept-edits"
			m, _ = pressModeSwitch(t, m)
			if auto := visibleModeText(m); !strings.Contains(auto, "mode auto unavailable") || !strings.Contains(auto, "guardrails checker") {
				t.Fatalf("auto hint = %q, want it to name the guardrails checker", auto)
			}
			m, _ = pressModeSwitch(t, m)
			yolo := visibleModeText(m)
			if !strings.Contains(yolo, "mode yolo unavailable") || !strings.Contains(yolo, "--permission-mode yolo") {
				t.Fatalf("yolo hint = %q, want the restart with --permission-mode yolo", yolo)
			}
			if strings.Contains(yolo, "checker") || strings.Contains(yolo, "guardrails") {
				t.Fatalf("yolo hint = %q, must not mention a guardrails checker", yolo)
			}
		})
	}
}

// TestHelpOverlayShowsVocabularyAndScopeSplit pins that every token is
// listed with its posture half marked process-wide and its session half marked as
// the new-session default, with the exact invocation and the statement that
// cycling reaches every mode but a posture-raising one needs a restart.
func TestHelpOverlayShowsVocabularyAndScopeSplit(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureStrict, true)
	body := stripANSIstr(m.helpBody())

	// The token table, written out independently of PermissionModeVocabulary.
	// auto and yolo sessions start in their own named modes, not "default".
	want := []struct{ token, posture, session string }{
		{"plan", "strict", "plan"},
		{"default", "strict", "default"},
		{"accept-edits", "strict", "accept-edits"},
		{"auto", "auto", "auto"},
		{"yolo", "yolo", "yolo"},
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
		m.helpKeyMarkings().modeSwitch + " cycles every mode. A mode that raises the posture needs a restart",
		"and auto also an enforcing guardrails checker",
		"keeps the current mode and your draft",
		"mecatui --permission-mode <token>",
	} {
		if !strings.Contains(body, phrase) {
			t.Errorf("help overlay missing %q", phrase)
		}
	}
	for _, stale := range []string{"auto or yolo", "blocked", "holds prompts", "trusted-accept-edits"} {
		if strings.Contains(body, stale) {
			t.Errorf("help overlay carries stale %q", stale)
		}
	}
}

// TestHeaderShowsModeNotPosture pins that the header shows the session mode and
// no posture badge at any tier: the posture is not a header axis, only the
// mode the cycle lands on and (separately) project trust.
func TestHeaderShowsModeNotPosture(t *testing.T) {
	for _, posture := range []string{postureStrict, postureTrusted, postureAuto, postureYolo} {
		t.Run(posture, func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
			m := permissionModeTestModel(t, conv, posture, true)
			header := stripANSIstr(m.renderHeader())
			if got := statusMode(m); got != "default" {
				t.Errorf("status mode = %q, want default", got)
			}
			if badge, _ := m.postureBadgeRender(); badge != "" {
				t.Errorf("posture %s rendered a badge without reported trust: %q", posture, badge)
			}
			for _, gone := range []string{"YOLO", "⚠ auto", "trusted"} {
				if strings.Contains(header, gone) {
					t.Errorf("posture %s: header carries posture badge %q: %q", posture, gone, header)
				}
			}
		})
	}
}

// TestHeaderKeepsPostureAcrossModeCycle pins that under auto, cycling through
// yolo (unavailable: above the auto ceiling), then through lower modes keeps the
// trust badge on every frame, including the pending frames before the server
// confirms each switch.
//
// A session on an auto-posture server starts in "auto" mode. The cycle order
// from "auto" is: yolo (unavailable) → default → plan → accept-edits → auto → …
// Yolo is the only mode that needs a restart; every other landing sends a
// SetMode and shows "pending" until the server confirms.
func TestHeaderKeepsPostureAcrossModeCycle(t *testing.T) {
	// Session starts in "auto" mode — the natural default on an auto-posture server.
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "auto"}
	m := newTestModelFromDeps(Deps{
		Session:  conv,
		Conv:     conv,
		Theme:    theme.New("aztec", theme.AztecPalette()),
		Mode:     "auto",
		Embedded: true,
		Ctx:      context.Background(),
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 140, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-auto-cycle", Mode: "auto", Capabilities: client.Capabilities{Posture: postureAuto, ProjectTrust: client.ProjectTrusted, SessionPermissionModes: true}},
	)

	assertAuto := func(step, wantMode string) {
		t.Helper()
		header := stripANSIstr(m.renderHeader())
		if !strings.Contains(header, client.ProjectTrusted) {
			t.Fatalf("%s: header lost the trust badge: %q", step, header)
		}
		if got := statusMode(m); got != wantMode {
			t.Fatalf("%s: status mode = %q, want %q", step, got, wantMode)
		}
	}
	assertAuto("start", "auto")

	// yolo is above the auto ceiling → unavailable, no server round trip, and
	// the header keeps the confirmed auto.
	mm, cmd := m.Update(modeKey())
	m = mm.(Model)
	if cmd != nil {
		t.Fatalf("landing on yolo issued a command: should be unavailable, not switched")
	}
	assertAuto("landed yolo", "auto")

	// Switching to modes below auto (default, plan, accept-edits) all require
	// a SetMode round trip: the session is in "auto", switching to anything lower
	// is a genuine change.
	for _, next := range []string{"default", "plan", "accept-edits"} {
		mm, cmd := m.Update(modeKey())
		m = mm.(Model)
		if cmd == nil {
			t.Fatalf("switch to %s issued no command", next)
		}
		assertAuto("pending "+next, next+" pending")
		m = applyAll(m, cmd())
		assertAuto("confirmed "+next, next)
	}

	// Cycling back to auto from accept-edits: pendingMode="auto" != activeMode="accept-edits"
	// → genuine transition, shows "auto pending" until server confirms.
	mm, cmd = m.Update(modeKey())
	m = mm.(Model)
	if cmd == nil {
		t.Fatalf("cycling back to auto issued no command")
	}
	assertAuto("pending auto", "auto pending")
	m = applyAll(m, cmd())
	assertAuto("confirmed auto", "auto")
}

// TestHelpOverlayNamesWhoseRestart pins that the embedded server's
// help gives the mecatui relaunch invocation; under `mecatui connect` it names
// the server operator's mecated restart and offers no local invocation.
func TestHelpOverlayNamesWhoseRestart(t *testing.T) {
	render := func(embedded bool) string {
		conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
		m := permissionModeTestModel(t, conv, postureAuto, embedded)
		return stripANSIstr(m.helpBody())
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

// TestModeServerDefaultRequestsUnspecifiedAndReadsBack pins the session
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
	if got := statusMode(m); got != "plan" {
		t.Fatalf("status mode = %q, want plan", got)
	}
	// Once known, later creates carry the current mode explicitly, as before.
	if got := m.desiredMode(); got != "plan" {
		t.Fatalf("desiredMode after ready = %q, want plan", got)
	}
}

// legacyYoloModel is a session on an API-major-1 server that predates the
// session_permission_modes feature but advertises a yolo posture: its allow-all
// is process-wide and no session mode removes it.
func legacyYoloModel(t *testing.T, conv *fakeConv) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session: conv,
		Conv:    conv,
		Theme:   theme.New("aztec", theme.AztecPalette()),
		Mode:    "default",
		Ctx:     context.Background(),
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 140, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-legacy-yolo", Mode: "default", Capabilities: client.Capabilities{Posture: postureYolo}},
	)
}

// TestLegacyYoloServerNeverPresentsSessionModeAsEffective pins the legacy
// presentation: against an older yolo server the header names the server's
// posture next to every session mode, the cycle offers only the session modes
// that server understands (never auto or yolo), and landing on one says the
// server's yolo still applies.
func TestLegacyYoloServerNeverPresentsSessionModeAsEffective(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := legacyYoloModel(t, conv)
	if got := statusMode(m); got != "default (server yolo)" {
		t.Fatalf("start: status mode = %q, want the server posture named", got)
	}
	want := []string{"plan", "accept-edits", "default", "plan"}
	for i, mode := range want {
		var issued bool
		m, issued = pressModeSwitch(t, m)
		if !issued {
			t.Fatalf("step %d: no SetMode issued for %s", i, mode)
		}
		if got := statusMode(m); got != mode+" (server yolo)" {
			t.Fatalf("step %d: status mode = %q, want %q", i, got, mode+" (server yolo)")
		}
		if status := visibleModeText(m); !strings.Contains(status, "yolo posture still applies") {
			t.Fatalf("step %d: status = %q, want the posture caveat", i, status)
		}
	}
	for _, sent := range conv.setModes() {
		if sent == "auto" || sent == "yolo" {
			t.Fatalf("SetMode calls = %v: a legacy server has no %s session mode", conv.setModes(), sent)
		}
	}
}

// TestLegacyStrictServerShowsPlainSessionMode pins that the legacy label is
// only added for an allow-all posture: a strict older server's session mode is
// its effective mode.
func TestLegacyStrictServerShowsPlainSessionMode(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := legacyYoloModel(t, conv)
	m.caps.Posture = postureStrict
	if got := statusMode(m); got != "default" {
		t.Fatalf("status mode = %q, want default", got)
	}
}
