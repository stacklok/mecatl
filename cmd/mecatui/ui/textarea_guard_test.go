package ui

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// TestWordLeftWouldHang is the predicate truth table, exercised directly on a
// bare textarea.Model. SetValue leaves the cursor at the end of the inserted
// text; SetCursorColumn repositions within the current row.
func TestWordLeftWouldHang(t *testing.T) {
	cases := []struct {
		name string
		set  func(ta *textarea.Model)
		want bool
	}{
		{"empty at origin", func(_ *textarea.Model) {}, true},
		{"all spaces at end", func(ta *textarea.Model) { ta.SetValue("   ") }, true},
		{"all spaces mid-line", func(ta *textarea.Model) {
			ta.SetValue("   ")
			ta.SetCursorColumn(1)
		}, true},
		// The subtle origin-space case: the rune UNDER the origin cursor is a
		// space, so the immediate-break escape does not apply.
		{"leading space word at origin", func(ta *textarea.Model) {
			ta.SetValue(" foo")
			ta.SetCursorColumn(0)
		}, true},
		// Safe origin: non-space under the cursor breaks the loop immediately.
		{"word at origin", func(ta *textarea.Model) {
			ta.SetValue("foo")
			ta.SetCursorColumn(0)
		}, false},
		{"indented word at end", func(ta *textarea.Model) { ta.SetValue("  foo") }, false},
		{"word at end", func(ta *textarea.Model) { ta.SetValue("foo") }, false},
		{"blank first row, cursor at row 1 col 0", func(ta *textarea.Model) {
			ta.SetValue("   \nbar")
			ta.SetCursorColumn(0)
		}, true},
		{"word on earlier row, spaces under cursor", func(ta *textarea.Model) { ta.SetValue("foo\n   ") }, false},
		{"empty first row, word on row 1", func(ta *textarea.Model) { ta.SetValue("\nx") }, false},
		// Multibyte whitespace (U+3000 ideographic space, 3 UTF-8 bytes):
		// unicode.IsSpace is true so upstream genuinely spins — but a byte-indexing
		// mutant (byte-slicing the lines instead of rune-slicing) sees the 0xE3
		// lead byte as non-space and would NOT swallow, failing toward SHIPPING the
		// hang. These two rows kill that mutant on both predicate paths.
		{"ideographic space before word at origin", func(ta *textarea.Model) {
			ta.SetValue("　foo")
			ta.SetCursorColumn(0)
		}, true},
		{"ideographic space at end", func(ta *textarea.Model) { ta.SetValue("　") }, true}, // col=1 is a RUNE index
		// Cursor ON the word's first rune: everything strictly before is space, so
		// upstream still spins (off-origin it inspects [0,col), never the rune
		// under the cursor). Doubles as the kill for a clamped-INCLUSIVE slice
		// mutant (cur[:col+1] would see the 'x' and not swallow).
		{"cursor on word first rune after spaces", func(ta *textarea.Model) {
			ta.SetValue("   x")
			ta.SetCursorColumn(3)
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ta := textarea.New()
			tc.set(&ta)
			if got := wordLeftWouldHang(ta); got != tc.want {
				t.Fatalf("wordLeftWouldHang(%q @(%d,%d)) = %v, want %v",
					ta.Value(), ta.Line(), ta.Column(), got, tc.want)
			}
		})
	}
}

// TestSwallowWordLeftHangKeyMatch checks the key gate: only the textarea's own
// WordBackward binding (alt+left / alt+b by default) is ever swallowed, and
// only when the predicate fires.
func TestSwallowWordLeftHangKeyMatch(t *testing.T) {
	ta := textarea.New() // empty buffer: wordLeftWouldHang is true
	if swallowWordLeftHang(ta, tea.KeyPressMsg{Code: 'a', Text: "a"}) {
		t.Fatal("plain 'a' must never be swallowed")
	}
	if !swallowWordLeftHang(ta, tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt}) {
		t.Fatal("alt+b over an empty textarea must be swallowed")
	}
	if !swallowWordLeftHang(ta, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}) {
		t.Fatal("alt+left over an empty textarea must be swallowed")
	}
	// Hang-state buffer, NON-WordBackward arrows must still forward: an
	// over-matching guard (e.g. a mod-insensitive KeyLeft match) would swallow
	// plain left over whitespace and permanently stick the cursor.
	if swallowWordLeftHang(ta, tea.KeyPressMsg{Code: tea.KeyLeft}) {
		t.Fatal("plain left must never be swallowed (only WordBackward is guarded)")
	}
	if swallowWordLeftHang(ta, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}) {
		t.Fatal("ctrl+left must never be swallowed (only WordBackward is guarded)")
	}
}

// TestSwallowWordLeftHangFollowsRebind pins the "matches the instance's OWN
// keymap" promise: a guard hardcoding {alt+left, alt+b} is observationally
// identical until someone rebinds WordBackward — then it guards the wrong key
// and lets the rebound one hang.
func TestSwallowWordLeftHangFollowsRebind(t *testing.T) {
	ta := textarea.New() // empty buffer: wordLeftWouldHang is true
	ta.KeyMap.WordBackward = key.NewBinding(key.WithKeys("ctrl+left"))
	if !swallowWordLeftHang(ta, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModCtrl}) {
		t.Fatal("rebound WordBackward (ctrl+left) must be swallowed")
	}
	if swallowWordLeftHang(ta, tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}) {
		t.Fatal("alt+left is no longer WordBackward after the rebind and must forward")
	}
}

// TestBubblesVersionPinForWordLeftGuard is the version-pin sentinel for the
// wordLeft workaround (the role TestBubblesSpinnerDedupesDuplicateChains plays
// for the spinner fix): the removal condition in textarea_guard.go is
// documentation-only, so without this nothing fires on a bubbles upgrade and
// the guard silently outlives its purpose — and if upstream's fix makes "no
// word to the left" MOVE the cursor, the swallow starts suppressing FIXED
// behavior.
func TestBubblesVersionPinForWordLeftGuard(t *testing.T) {
	const (
		modPath = "charm.land/bubbles/v2"
		pinned  = "v2.1.0"
	)
	got, src := resolvedBubblesVersion(t, modPath)
	if got == "" {
		// Loud skip, never a silent pass: neither source could resolve the dep.
		t.Skipf("%s version unavailable (build info carries no deps in `go test` binaries and go.mod was not found) — bubbles version pin NOT verified; check the dep against the textarea_guard.go removal condition manually", modPath)
	}
	if got != pinned {
		t.Fatalf("bubbles upgraded (%s %s → %s, via %s) — check textarea.wordLeft for an in-loop boundary guard; if fixed, delete textarea_guard.go + the two update.go call sites + the IMPLEMENTATION-NOTES paragraph; if not, re-verify the predicate and bump this pin",
			modPath, pinned, got, src)
	}
}

// resolvedBubblesVersion resolves modPath's version, preferring the test
// binary's build info (authoritative when present) and falling back to the
// module's go.mod require/replace line — `go test` binaries embed NO Deps
// (bi.Deps is empty; only bi.Main is set), so without the fallback this
// sentinel would always skip and never fire on an upgrade.
func resolvedBubblesVersion(t *testing.T, modPath string) (version, source string) {
	t.Helper()
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path != modPath {
				continue
			}
			if dep.Replace != nil {
				dep = dep.Replace
			}
			return dep.Version, "build info"
		}
	}
	// Walk up from the package dir (the test cwd) to the module root's go.mod.
	dir, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	for range 10 {
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				f := strings.Fields(line)
				if len(f) < 2 || f[0] != modPath {
					continue
				}
				if strings.Contains(line, "=>") {
					// A replace directive: the effective version is the last
					// field (a directory replace has none — surface the raw
					// tail so the mismatch message says what happened).
					return f[len(f)-1], "go.mod replace"
				}
				return f[1], "go.mod"
			}
			return "", "" // module root reached, dep line absent
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

// newGuardModel builds a connected idle Model (the coalesce_test pattern: a
// window size plus SessionReadyMsg drive phaseConnecting → phaseIdle).
func newGuardModel(t *testing.T) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         t.Context(),
		NoAltScreen: true,
	})
	return applyAll(m,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		client.SessionReadyMsg{SessionID: "sess-guard-0001"},
	)
}

// updateGuarded runs m.Update(msg) under a watchdog. LOUD WARNING: a FAILING
// run of this test leaks one goroutine spinning at 100% CPU until the test
// process exits — upstream's wordLeft loop is unstoppable from outside, and
// this repo has no subprocess-isolation convention to contain it. The PASS
// path never hangs. t.Fatalf fires from the TEST goroutine, never the worker.
func updateGuarded(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	done := make(chan tea.Model, 1)
	go func() {
		mm, _ := m.Update(msg)
		done <- mm
	}()
	select {
	case mm := <-done:
		return mm.(Model)
	case <-time.After(scaleWait(2 * time.Second)):
		t.Fatalf("Update(%v) wedged: textarea wordLeft() guard missing (bubbles#1652); a 100%%-CPU goroutine is now leaked until process exit", msg)
		return m
	}
}

// TestWordBackwardDoesNotWedgeReducer drives the REAL Model reducer through
// both key phases (idle and running) with WordBackward presses that would hit
// upstream's unbounded loop, and confirms working word-navigation still
// forwards. No t.Parallel(): a failure leaks a hot goroutine (see
// updateGuarded) and must not degrade sibling tests.
func TestWordBackwardDoesNotWedgeReducer(t *testing.T) {
	altLeft := tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}
	altB := tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt}

	for _, phase := range []struct {
		name string
		prep func(m Model) Model
	}{
		{"idle", func(m Model) Model { return m }},
		{"running", func(m Model) Model { m.phase = phaseRunning; return m }},
	} {
		t.Run(phase.name, func(t *testing.T) {
			// (a) empty input + alt+left → swallowed: value unchanged, col 0.
			m := phase.prep(newGuardModel(t))
			m = updateGuarded(t, m, altLeft)
			if m.ta.Value() != "" || m.ta.Column() != 0 {
				t.Fatalf("empty+alt+left: value=%q col=%d, want \"\" col 0", m.ta.Value(), m.ta.Column())
			}

			// (b) all-whitespace input + alt+left → swallowed: cursor stays at end.
			m = phase.prep(newGuardModel(t))
			m.ta.SetValue("   ")
			m = updateGuarded(t, m, altLeft)
			if got := m.ta.Column(); got != 3 {
				t.Fatalf("whitespace+alt+left: col=%d, want 3 (swallowed no-op)", got)
			}

			// (c) a real word to the left MUST FORWARD: the guard must not break
			// working word-navigation.
			m = phase.prep(newGuardModel(t))
			m.ta.SetValue("  foo")
			m = updateGuarded(t, m, altLeft)
			if got := m.ta.Column(); got != 2 {
				t.Fatalf("\"  foo\"+alt+left: col=%d, want 2 (must forward to textarea)", got)
			}

			// (d) safe origin (non-space under cursor) must forward; cursor stays.
			m = phase.prep(newGuardModel(t))
			m.ta.SetValue("foo")
			m.ta.SetCursorColumn(0)
			m = updateGuarded(t, m, altLeft)
			if got := m.ta.Column(); got != 0 {
				t.Fatalf("\"foo\"@0+alt+left: col=%d, want 0", got)
			}

			// (e) the alternate binding alt+b, empty input → swallowed.
			m = phase.prep(newGuardModel(t))
			m = updateGuarded(t, m, altB)
			if m.ta.Value() != "" || m.ta.Column() != 0 {
				t.Fatalf("empty+alt+b: value=%q col=%d, want \"\" col 0", m.ta.Value(), m.ta.Column())
			}
		})
	}
}
