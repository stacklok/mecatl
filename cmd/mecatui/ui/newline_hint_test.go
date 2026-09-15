package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestChordNeedsKeyDisambiguation pins the rule that decides which newline chord
// the prompt advertises: a modified Enter needs a terminal that can encode the
// modifier, because Enter's legacy byte (CR) has nowhere to put one. Alt is the
// exception — legacy encoding prefixes ESC.
func TestChordNeedsKeyDisambiguation(t *testing.T) {
	for _, tc := range []struct {
		chord string
		want  bool
	}{
		{"shift+enter", true},
		{"ctrl+enter", true},
		{"shift+ctrl+enter", true},
		{"alt+shift+enter", true}, // more than alt alone: legacy cannot carry it
		{"alt+enter", false},      // ESC CR survives legacy encoding
		{"ctrl+j", false},         // a plain LF byte
		{"enter", false},          // unmodified; deliverable everywhere
		{"ctrl+f2", false},        // not an Enter variant at all
		{"SHIFT+ENTER", true},     // chords are matched case-insensitively
		{"", false},
	} {
		if got := chordNeedsKeyDisambiguation(tc.chord); got != tc.want {
			t.Errorf("chordNeedsKeyDisambiguation(%q) = %v, want %v", tc.chord, got, tc.want)
		}
	}
}

// TestNewlineHintChordNamesPreferredChordUntilProvedOtherwise pins the optimistic
// default: the hint reads shift+enter, and only a terminal that has PROVED it
// cannot deliver a modified Enter gets the fallback.
func TestNewlineHintChordNamesPreferredChordUntilProvedOtherwise(t *testing.T) {
	km := defaultKeys()
	if got := newlineHintChord(km, false); got != "shift+enter" {
		t.Errorf("hint before the terminal proves otherwise = %q, want shift+enter", got)
	}
	if got := newlineHintChord(km, true); got != "ctrl+j" {
		t.Errorf("hint on a terminal that cannot disambiguate = %q, want ctrl+j", got)
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
		{"an unrelated chord is unaffected", []string{"ctrl+f2"}, "ctrl+f2", "ctrl+f2"},
		{"the FIRST legacy-safe chord wins the fallback", []string{"ctrl+enter", "ctrl+j", "alt+enter"}, "ctrl+enter", "ctrl+j"},
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
// signal that the advertised chord is undeliverable.
func TestPromptHintFallsBackWhenTerminalStaysSilent(t *testing.T) {
	m := newlineHintModel(t)

	mm, _ := m.Update(keyboardProbeDeadlineMsg{})
	m = mm.(Model)

	got := m.prompt.Placeholder()
	if !strings.Contains(got, "ctrl+j for newline") {
		t.Errorf("placeholder after an unanswered probe = %q, want ctrl+j", got)
	}
	if strings.Contains(got, "shift+enter") {
		t.Errorf("placeholder should stop advertising an undeliverable chord: %q", got)
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
		t.Errorf("rendered input still shows the undeliverable chord: %q", after)
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
