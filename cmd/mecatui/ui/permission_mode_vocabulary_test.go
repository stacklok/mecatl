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
		client.SessionReadyMsg{SessionID: "sess-test-0001", Mode: "default", Capabilities: client.Capabilities{Posture: posture}},
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

// modeCycleStep is one ModeSwitch press: the mode it lands on, whether it
// applies a session half (issues SetMode) or is blocked pending a restart, and
// phrases the footer status or the input area must carry.
type modeCycleStep struct {
	token   string
	setMode bool
	blocked bool
	status  []string
}

// visibleModeText is the footer status plus the input area, with wrapping and
// the rail border collapsed so a phrase can be matched across a wrapped line.
func visibleModeText(m Model) string {
	input := strings.ReplaceAll(stripANSIstr(m.renderInput()), "│", " ")
	return strings.Join(strings.Fields(stripANSIstr(m.statusMsg)+" "+input), " ")
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
		if got := m.modeBlocked(); got != s.blocked {
			t.Fatalf("step %d (%s): blocked = %v, want %v", i, s.token, got, s.blocked)
		}
		wantHeader := s.token
		if s.blocked {
			wantHeader += " blocked"
		}
		if got := statusMode(m); got != wantHeader {
			t.Fatalf("step %d: header mode = %q, want %q", i, got, wantHeader)
		}
		text := visibleModeText(m)
		for _, want := range s.status {
			if !strings.Contains(text, want) {
				t.Fatalf("step %d (%s): visible text %q missing %q", i, s.token, text, want)
			}
		}
	}
	return m
}

// TestModeSwitchCyclesEveryMode pins that under strict the cycle lands on all
// seven modes. The posture-raising ones change nothing and say what to
// configure and restart; the strict ones apply live.
func TestModeSwitchCyclesEveryMode(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureStrict, true)
	m = runModeCycle(t, m, []modeCycleStep{
		{token: "plan", setMode: true, status: []string{"mode plan"}},
		{token: "accept-edits", setMode: true, status: []string{"mode accept-edits"}},
		{token: "trusted", blocked: true, status: []string{"trusted needs a restart", "mecatui --permission-mode trusted", "esc back to accept-edits"}},
		{token: "trusted-accept-edits", blocked: true, status: []string{"mecatui --permission-mode trusted-accept-edits"}},
		{token: "auto", blocked: true, status: []string{"guardrails checker", "guardrails.model", "mecatui --permission-mode auto", "prompts and edits are paused"}},
		{token: "yolo", blocked: true, status: []string{"guardrails checker", "mecatui --permission-mode yolo"}},
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

// TestModeSwitchUnderTrustedPosture pins that modes matching the running
// posture apply fully, a mode below it applies its session half and says the
// posture stays, and a mode above it needs a restart.
func TestModeSwitchUnderTrustedPosture(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureTrusted, true)
	if got := m.currentModeToken(); got != "trusted" {
		t.Fatalf("start position = %q, want trusted", got)
	}
	runModeCycle(t, m, []modeCycleStep{
		{token: "trusted-accept-edits", setMode: true, status: []string{"mode trusted-accept-edits"}},
		{token: "auto", blocked: true, status: []string{"auto needs a guardrails checker", "esc back to trusted-accept-edits"}},
		{token: "yolo", blocked: true, status: []string{"yolo needs a guardrails checker"}},
		{token: "default", setMode: true, status: []string{"mode default · posture trusted stays until restart"}},
		{token: "plan", setMode: true},
		{token: "accept-edits", setMode: true},
		{token: "trusted", setMode: true, status: []string{"mode trusted"}},
	})
	want := []string{"accept-edits", "default", "plan", "accept-edits", "default"}
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
		{token: "trusted", blocked: true, status: []string{"remote server", "restart mecated with --permission-mode trusted"}},
		{token: "trusted-accept-edits", blocked: true},
		{token: "auto", blocked: true, status: []string{"--permission-mode auto and a guardrails checker"}},
	})
	if text := visibleModeText(m); strings.Contains(text, "mecatui --permission-mode") {
		t.Fatalf("connect hint offers a local relaunch: %q", text)
	}
	if got := conv.setModes(); len(got) != 0 {
		t.Fatalf("SetMode calls = %v, want none", got)
	}
}

// TestBlockedModeHoldsPromptUntilEscape pins that landing on a mode that needs
// a restart holds the prompt: typing, enter, and paste neither edit nor send the
// draft, at idle or mid-run, and esc returns to the accepted mode with the draft
// intact rather than cancelling the run.
func TestBlockedModeHoldsPromptUntilEscape(t *testing.T) {
	for _, phase := range []phase{phaseIdle, phaseRunning} {
		t.Run(fmt.Sprint(phase), func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
			m := permissionModeTestModel(t, conv, postureStrict, true)
			m.prompt.Rewrite("keep this draft")
			m = runModeCycle(t, m, []modeCycleStep{
				{token: "plan", setMode: true},
				{token: "accept-edits", setMode: true},
				{token: "trusted", blocked: true},
			})
			m.phase = phaseRunning
			if phase == phaseIdle {
				m.phase = phaseIdle
			}
			for _, msg := range []tea.Msg{
				tea.KeyPressMsg{Code: 'x', Text: "x"},
				tea.KeyPressMsg{Code: tea.KeyEnter},
				tea.PasteMsg{Content: "pasted"},
			} {
				mm, cmd := m.Update(msg)
				m = mm.(Model)
				if cmd != nil {
					t.Fatalf("%T while blocked issued a command", msg)
				}
			}
			if got := m.prompt.Value(); got != "keep this draft" {
				t.Fatalf("blocked prompt changed to %q", got)
			}
			if len(m.queued) != 0 || m.phase != phase {
				t.Fatalf("blocked enter queued %v / moved phase to %v", m.queued, m.phase)
			}
			if text := visibleModeText(m); !strings.Contains(text, "mode trusted is blocked") || strings.Contains(stripANSIstr(m.renderInput()), "keep this draft") {
				t.Fatalf("blocked input area = %q", text)
			}

			mm, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			m = mm.(Model)
			if cmd != nil || strings.Contains(stripANSIstr(m.statusMsg), "cancelling") {
				t.Fatalf("esc while blocked must only leave the mode, status %q", stripANSIstr(m.statusMsg))
			}
			if m.modeBlocked() || statusMode(m) != "accept-edits" {
				t.Fatalf("after esc: blocked=%v header=%q, want accept-edits", m.modeBlocked(), statusMode(m))
			}
			if !strings.Contains(stripANSIstr(m.renderInput()), "keep this draft") {
				t.Fatal("draft not shown again after esc")
			}
			m = applyAll(m, tea.KeyPressMsg{Code: '!', Text: "!"})
			if got := m.prompt.Value(); got != "keep this draft!" {
				t.Fatalf("prompt after esc = %q, want editable again", got)
			}
			if got := conv.setModes(); strings.Join(got, ",") != "plan,accept-edits" {
				t.Fatalf("SetMode calls = %v, want only the two applied modes", got)
			}
		})
	}
}

// TestBlockedModeHoldsQueuedPrompt pins that a run ending while the mode is
// blocked does not auto-send the queued follow-up.
func TestBlockedModeHoldsQueuedPrompt(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureTrusted, true)
	m = runModeCycle(t, m, []modeCycleStep{
		{token: "trusted-accept-edits", setMode: true},
		{token: "auto", blocked: true},
	})
	m.queued = []string{"queued follow-up"}
	mm, cmd := m.drainQueue("end_turn")
	m = mm.(Model)
	if cmd != nil || m.queuePaused != "mode" {
		t.Fatalf("drain while blocked: cmd=%v paused=%q, want held", cmd != nil, m.queuePaused)
	}
	if got := m.prompt.Value(); got != "queued follow-up" {
		t.Fatalf("held queue text = %q", got)
	}
}

// TestHelpOverlayShowsVocabularyAndScopeSplit pins that every token is
// listed with its posture half marked process-wide and its session half marked as
// the new-session default, with the exact invocation and the statement that
// cycling reaches every mode but a posture-raising one needs a restart.
func TestHelpOverlayShowsVocabularyAndScopeSplit(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureStrict, true)
	body := stripANSIstr(renderHelpOverlay(m.deps.Theme, m.caps, 200, 0, 0, m.helpKeyMarkings(), m.deps.Embedded))

	// The token table, written out independently of PermissionModeVocabulary.
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
		m.helpKeyMarkings().modeSwitch + " cycles every mode. A mode that raises the posture needs a restart",
		"mecatui --permission-mode <token>",
	} {
		if !strings.Contains(body, phrase) {
			t.Errorf("help overlay missing %q", phrase)
		}
	}
}

// TestHeaderShowsPostureWhenAboveStrict pins that the header shows the
// session mode, and a posture badge for every tier above strict (trusted now
// included), sourced from the server-reported caps.Posture.
func TestHeaderShowsPostureWhenAboveStrict(t *testing.T) {
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
			m := permissionModeTestModel(t, conv, tc.posture, true)
			header := stripANSIstr(m.renderHeader())
			if got := statusMode(m); got != "default" {
				t.Errorf("status mode = %q, want default", got)
			}
			badge, _ := m.postureBadgeRender()
			if tc.badge == "" {
				if badge != "" || strings.Contains(header, "posture") || strings.Contains(header, "strict") {
					t.Errorf("strict must render no posture badge, got %q", header)
				}
				return
			}
			if badge == "" || !strings.Contains(header, tc.badge) {
				t.Errorf("posture %s: header missing badge %q: %q", tc.posture, tc.badge, header)
			}
		})
	}
}

// TestHeaderKeepsPostureAcrossModeCycle pins that under auto, cycling
// through yolo (which needs a restart), default, plan, and accept-edits keeps the
// auto badge on every frame, including the pending frame before the server
// confirms each switch.
func TestHeaderKeepsPostureAcrossModeCycle(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
	m := permissionModeTestModel(t, conv, postureAuto, true)
	assertAuto := func(step, wantMode string) {
		t.Helper()
		header := stripANSIstr(m.renderHeader())
		if !strings.Contains(header, autoBadgeText) {
			t.Fatalf("%s: header lost the auto posture: %q", step, header)
		}
		if got := statusMode(m); got != wantMode {
			t.Fatalf("%s: status mode = %q, want %s", step, got, wantMode)
		}
	}
	assertAuto("start", "default")
	// yolo needs a restart, and default is the session half auto already runs:
	// neither press needs a server round trip.
	for _, step := range []struct{ token, header string }{{"yolo", "yolo blocked"}, {"default", "default"}} {
		mm, cmd := m.Update(modeKey())
		m = mm.(Model)
		if cmd != nil {
			t.Fatalf("landing on %s issued a command", step.token)
		}
		assertAuto("landed "+step.token, step.header)
	}
	for _, next := range []string{"plan", "accept-edits"} {
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

// TestHelpOverlayNamesWhoseRestart pins that the embedded server's
// help gives the mecatui relaunch invocation; under `mecatui connect` it names
// the server operator's mecated restart and offers no local invocation.
func TestHelpOverlayNamesWhoseRestart(t *testing.T) {
	render := func(embedded bool) string {
		conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, mode: "default"}
		m := permissionModeTestModel(t, conv, postureAuto, embedded)
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
