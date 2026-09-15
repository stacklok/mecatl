package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestChordSurvivesLegacyEncoding pins the allowlist that decides which newline
// chord the prompt can safely fall back to on a terminal with no key
// disambiguation. Only encodings a legacy terminal carries unambiguously qualify:
// a bare key, ctrl+<letter> (minus h/i/m, whose control bytes ARE
// backspace/tab/Enter), and either of those behind a single alt (ESC prefix).
//
// The cases that matter are the false ones. A modified Enter has nowhere to put
// its modifier in a CR byte, and — the reason this is an allowlist and not a list
// of Enter variants — a legacy terminal encodes ctrl+shift+x as the same control
// byte as ctrl+x, so a binding on ctrl+shift+x is equally undeliverable.
func TestChordSurvivesLegacyEncoding(t *testing.T) {
	for _, tc := range []struct {
		chord string
		want  bool
	}{
		{"ctrl+j", true},    // a plain LF byte
		{"alt+enter", true}, // ESC CR survives legacy encoding
		{"enter", true},     // unmodified; deliverable everywhere
		{"alt+b", true},     // ESC + a control byte
		{"f2", true},        // an unmodified named key
		{"shift+enter", false},
		{"ctrl+enter", false},
		{"shift+ctrl+enter", false},
		{"alt+shift+enter", false}, // more than alt alone: legacy cannot carry it
		{"ctrl+shift+x", false},    // collides with ctrl+x in legacy encoding
		{"shift+a", false},         // indistinguishable from a bare uppercase A
		{"ctrl+m", false},          // the CR byte: arrives as Enter, not as this chord
		{"ctrl+i", false},          // the tab byte
		{"ctrl+h", false},          // the backspace byte
		{"ctrl+f2", false},         // ctrl on a non-letter has no legacy encoding
		{"super+enter", false},
		{"CTRL+J", true}, // chords are matched case-insensitively
		{"", false},
	} {
		if got := chordSurvivesLegacyEncoding(tc.chord); got != tc.want {
			t.Errorf("chordSurvivesLegacyEncoding(%q) = %v, want %v", tc.chord, got, tc.want)
		}
	}
}

// TestNewlineHintChordNamesPreferredChordUntilUnconfirmed pins the optimistic
// default: the hint reads shift+enter, and falls back only once the terminal's
// support for a modified Enter is unconfirmed. Unconfirmed, not disproved — see
// keyboardProbeDeadline for why the two are treated alike.
func TestNewlineHintChordNamesPreferredChordUntilUnconfirmed(t *testing.T) {
	km := defaultKeys()
	if got := newlineHintChord(km, false); got != "shift+enter" {
		t.Errorf("hint while the terminal may still be capable = %q, want shift+enter", got)
	}
	if got := newlineHintChord(km, true); got != "ctrl+j" {
		t.Errorf("hint once disambiguation is unconfirmed = %q, want ctrl+j", got)
	}
}

// TestNewlineHintChordHonoursKeymapRebind keeps the hint driven by the live
// binding rather than acquiring a hardcoded chord of its own.
func TestNewlineHintChordHonoursKeymapRebind(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		chords                    []string
		wantPreferred, wantLegacy string
	}{
		{"default order falls back past the disambiguation-only chord", []string{"shift+enter", "ctrl+j"}, "shift+enter", "ctrl+j"},
		{"a single disambiguation-only chord has no fallback to offer", []string{"shift+enter"}, "shift+enter", "shift+enter"},
		{"alt+enter survives legacy encoding", []string{"alt+enter"}, "alt+enter", "alt+enter"},
		{"the FIRST legacy-safe chord wins the fallback", []string{"ctrl+enter", "ctrl+j", "alt+enter"}, "ctrl+enter", "ctrl+j"},
		// A modified Enter is not the only chord legacy encoding cannot carry: a
		// preferred chord that COLLIDES with another in legacy encoding must lose the
		// fallback to the configured ctrl+j just as shift+enter does, or the hint
		// keeps advertising a chord that never arrives.
		{"a colliding ctrl+shift chord yields to the configured fallback", []string{"ctrl+shift+x", "ctrl+j"}, "ctrl+shift+x", "ctrl+j"},
		{"a colliding chord with no fallback configured keeps itself", []string{"ctrl+shift+x"}, "ctrl+shift+x", "ctrl+shift+x"},
		{"ctrl on a non-letter has no legacy encoding either", []string{"ctrl+f2", "ctrl+j"}, "ctrl+f2", "ctrl+j"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			km := applyKeyOverrides(defaultKeys(), map[string][]string{"Newline": tc.chords})
			if got := newlineHintChord(km, false); got != tc.wantPreferred {
				t.Errorf("preferred hint = %q, want %q", got, tc.wantPreferred)
			}
			if got := newlineHintChord(km, true); got != tc.wantLegacy {
				t.Errorf("fallback hint = %q, want %q", got, tc.wantLegacy)
			}
		})
	}
}

func newlineHintModel(t *testing.T) Model {
	t.Helper()
	return newTestModelFromDeps(Deps{Theme: theme.New("aztec", theme.AztecPalette()), NoAltScreen: true})
}

// TestPromptHintStartsOnPreferredChord is the state every capable terminal stays
// in, so it must hold with no capability traffic at all.
func TestPromptHintStartsOnPreferredChord(t *testing.T) {
	if got := newlineHintModel(t).prompt.Placeholder(); !strings.Contains(got, "shift+enter for newline") {
		t.Errorf("initial placeholder should advertise shift+enter: %q", got)
	}
}

// TestPromptHintKeepsPreferredChordWhenTerminalConfirms covers the capable
// terminal end to end: a positive reply must leave the hint alone, and must also
// settle the probe so the later deadline cannot downgrade it.
func TestPromptHintKeepsPreferredChordWhenTerminalConfirms(t *testing.T) {
	m := newlineHintModel(t)

	mm, _ := m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m = mm.(Model)
	if !m.keyboardProbeSettled {
		t.Error("a capability reply should settle the probe")
	}

	// The deadline still fires afterwards; it must not second-guess the reply.
	mm, _ = m.Update(keyboardProbeDeadlineMsg{})
	m = mm.(Model)
	if got := m.prompt.Placeholder(); !strings.Contains(got, "shift+enter for newline") {
		t.Errorf("placeholder after a confirmed terminal = %q, want shift+enter", got)
	}
}

// TestPromptHintFallsBackWhenTerminalStaysSilent is the issue-#1500 case: macOS
// Terminal.app and friends never answer the query, so the deadline is the only
// signal there is — and the hint must then name a chord that works regardless of
// how the silence is explained.
func TestPromptHintFallsBackWhenTerminalStaysSilent(t *testing.T) {
	m := newlineHintModel(t)

	mm, _ := m.Update(keyboardProbeDeadlineMsg{})
	m = mm.(Model)

	got := m.prompt.Placeholder()
	if !strings.Contains(got, "ctrl+j for newline") {
		t.Errorf("placeholder after an unanswered probe = %q, want ctrl+j", got)
	}
	if strings.Contains(got, "shift+enter") {
		t.Errorf("placeholder should stop advertising an unconfirmed chord: %q", got)
	}
}

// TestPromptHintFallsBackWhenTerminalReportsNoEnhancements covers the terminal
// that does answer, but reports nothing: it needs the fallback immediately rather
// than waiting out the deadline.
func TestPromptHintFallsBackWhenTerminalReportsNoEnhancements(t *testing.T) {
	m := newlineHintModel(t)

	mm, _ := m.Update(tea.KeyboardEnhancementsMsg{Flags: 0})
	m = mm.(Model)

	if got := m.prompt.Placeholder(); !strings.Contains(got, "ctrl+j for newline") {
		t.Errorf("placeholder after an empty capability reply = %q, want ctrl+j", got)
	}
}

// TestLateCapabilityReplyRecoversTheHint covers the ordering nobody plans for: a
// reply that loses the race with the deadline must still correct the hint, since
// the chord it names does work.
func TestLateCapabilityReplyRecoversTheHint(t *testing.T) {
	m := newlineHintModel(t)

	mm, _ := m.Update(keyboardProbeDeadlineMsg{})
	m = mm.(Model)
	mm, _ = m.Update(tea.KeyboardEnhancementsMsg{Flags: 1})
	m = mm.(Model)

	if got := m.prompt.Placeholder(); !strings.Contains(got, "shift+enter for newline") {
		t.Errorf("placeholder after a late positive reply = %q, want shift+enter", got)
	}
}

// TestInitArmsTheKeyboardProbeDeadline proves the deadline is wired into the REAL
// startup path: without it the hint would trust its optimistic default forever on
// a silent terminal, which is exactly the bug.
func TestInitArmsTheKeyboardProbeDeadline(t *testing.T) {
	var armedFor time.Duration
	probe := func(after time.Duration) tea.Cmd {
		armedFor = after
		return func() tea.Msg { return keyboardProbeDeadlineMsg{} }
	}

	m := newTestModelFromDeps(Deps{
		Theme:                   theme.New("aztec", theme.AztecPalette()),
		NoAltScreen:             true,
		ProbeKeyboardCapability: true,
	})
	m.keyboardProbeTimer = probe
	if cmd := m.Init(); cmd == nil {
		t.Fatal("Init returned no command, so nothing armed the probe deadline")
	}
	if armedFor != keyboardProbeDeadline {
		t.Errorf("probe armed for %v, want %v", armedFor, keyboardProbeDeadline)
	}

	// Without a terminal to answer, nothing is armed and the hint keeps the chord
	// it started on: there is no silence to interpret on redirected output.
	armedFor = 0
	m = newlineHintModel(t)
	m.keyboardProbeTimer = probe
	m.Init()
	if armedFor != 0 {
		t.Errorf("probe armed for %v without a TTY, want unarmed", armedFor)
	}
}

// TestCorrectedHintIsActuallyPainted is the regression test for the defect the
// model-level tests could not see: the corrected chord reached the model, but
// renderInput's memoized output kept the stale line because the placeholder was
// documented as fixed per process and had no slot in inputRenderKey. The
// correction arrives on a timer with no other model change behind it, so nothing
// else re-keyed the cache and the user kept reading the chord that does not work.
//
// It asserts the RENDERED region, not the model field, because that is the layer
// the bug lived in.
func TestCorrectedHintIsActuallyPainted(t *testing.T) {
	m := applyAll(newlineHintModel(t), tea.WindowSizeMsg{Width: 120, Height: 30})

	before := stripANSIstr(m.renderInput())
	if !strings.Contains(before, "shift+enter for newline") {
		t.Fatalf("rendered input should start on the preferred chord: %q", before)
	}

	mm, _ := m.Update(keyboardProbeDeadlineMsg{})
	m = mm.(Model)

	after := stripANSIstr(m.renderInput())
	if !strings.Contains(after, "ctrl+j for newline") {
		t.Errorf("rendered input did not repaint the corrected chord: %q", after)
	}
	if strings.Contains(after, "shift+enter") {
		t.Errorf("rendered input still shows the unconfirmed chord: %q", after)
	}
}

// TestEveryNewlineChordInsertsNewline pins the full chord set against the real
// key-dispatch path. shift+enter and ctrl+enter arrive only from a terminal that
// disambiguates; ctrl+j and alt+enter also survive legacy encoding. All four are
// bound unconditionally — the capability probe gates only what gets ADVERTISED.
//
// The byte sequences these decode from (ESC[13;2u, ESC[27;2;13~, 0x0A, ESC CR)
// are Bubble Tea's contract and are verified there; this test owns the mapping
// from decoded chord to newline insertion.
func TestEveryNewlineChordInsertsNewline(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
		{"ctrl+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}},
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.key.String(); got != tc.name {
				t.Fatalf("Bubble Tea reports this key as %q, but the binding names %q", got, tc.name)
			}
			m, _ := newQueueModel(t)
			m.prompt.Rewrite("first")
			mm, _ := m.Update(tc.key)
			m = mm.(Model)
			if got := m.prompt.Value(); got != "first\n" {
				t.Fatalf("prompt value = %q, want %q", got, "first\n")
			}
		})
	}
}

// TestRemappedNewlineChordInsertsNewline closes the gap between what the hint
// ADVERTISES under a --keymap rebind and what the key dispatcher actually
// accepts. TestNewlineHintChordHonoursKeymapRebind proves the selection logic
// names the rebound chord; this proves the rebound chord reaches the textarea
// through the real Model.Update path, and that the default it replaced no longer
// does. Without both, the hint could confidently name a chord nothing dispatches.
func TestRemappedNewlineChordInsertsNewline(t *testing.T) {
	m := newTestModelFromDeps(Deps{
		Theme:        theme.New("aztec", theme.AztecPalette()),
		Ctx:          context.Background(),
		NoAltScreen:  true,
		KeyOverrides: map[string][]string{"Newline": {"ctrl+b", "ctrl+j"}},
	})
	m = applyAll(m, tea.WindowSizeMsg{Width: 100, Height: 30}, client.SessionReadyMsg{SessionID: "sess-test-0001"})

	// The hint names the rebound chord, and the rebound chord is what dispatches.
	if got := m.prompt.Placeholder(); !strings.Contains(got, "ctrl+b for newline") {
		t.Errorf("placeholder under a Newline rebind = %q, want ctrl+b", got)
	}
	m.prompt.Rewrite("first")
	mm, _ := m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	m = mm.(Model)
	if got := m.prompt.Value(); got != "first\n" {
		t.Fatalf("the rebound chord did not insert a newline: prompt value = %q", got)
	}

	// The default it replaced is inert, which is what makes the assertion above
	// about the rebind rather than about the default still being bound.
	mm, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = mm.(Model)
	if got := m.prompt.Value(); got != "first\n" {
		t.Errorf("the replaced default chord still edited the prompt: %q", got)
	}
}

// TestPlainEnterStillSubmits guards the other side of the pair: widening the
// newline chord set must not turn a bare Enter into a newline.
func TestPlainEnterStillSubmits(t *testing.T) {
	m, _ := newQueueModel(t)
	m.prompt.Rewrite("send me")
	mm, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = mm.(Model)
	if strings.Contains(m.prompt.Value(), "\n") {
		t.Fatalf("plain enter inserted a newline: %q", m.prompt.Value())
	}
	if m.prompt.Value() == "send me" {
		t.Fatalf("plain enter left the draft in the prompt instead of submitting it")
	}
}
