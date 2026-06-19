package agent

import (
	"testing"
	"time"
)

// fixedClockForNow is a minimal port.Clock returning a constant instant, used to
// prove Engine.now() returns the INJECTED clock's value (issue #116) rather than
// reading the wall clock. It is distinct from the package-test scriptedClock
// (package agent_test), which lives in the external test package.
type fixedClockForNow struct{ t time.Time }

func (c fixedClockForNow) Now() time.Time { return c.t }

// TestEngineNow is the behavioural complement to engine/arch's
// TestNoWallClockInEngineCore: the arch guard proves the core makes NO direct
// time.Now() call; this proves the helper they were rerouted to actually returns
// the injected Clock's time, and degrades to the zero time (never time.Now(),
// never a panic) on the two no-value paths.
func TestEngineNow(t *testing.T) {
	want := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)

	t.Run("injected clock is the source", func(t *testing.T) {
		e := &Engine{deps: Deps{Clock: fixedClockForNow{t: want}}}
		if got := e.now(); !got.Equal(want) {
			t.Errorf("now() = %v, want the injected clock's %v", got, want)
		}
	})

	t.Run("no clock yields the zero time", func(t *testing.T) {
		e := &Engine{}
		if got := e.now(); !got.IsZero() {
			t.Errorf("now() with no Clock = %v, want the zero time", got)
		}
	})

	t.Run("nil receiver yields the zero time, never panics", func(t *testing.T) {
		var e *Engine
		if got := e.now(); !got.IsZero() {
			t.Errorf("now() on a nil *Engine = %v, want the zero time", got)
		}
	})
}

// TestChildSerialDistinctUnderFixedClock guards the exact regression childSerial
// exists to prevent: the ephemeral child-session ids (guardrail checker, fork
// judge, ask reviewer, model router) used to be derived from
// time.Now().UnixNano(), which ALIASES two children minted at the same instant —
// e.g. under a fake/fixed clock, or two near-simultaneous mints. The atomic
// counter must hand out a distinct discriminator every time regardless of the
// clock (issue #116).
func TestChildSerialDistinctUnderFixedClock(t *testing.T) {
	const n = 100
	seen := make(map[int64]bool, n)
	for i := 0; i < n; i++ {
		v := childSerial.Add(1)
		if seen[v] {
			t.Fatalf("childSerial returned a duplicate discriminator %d: child-session ids would collide (the time.Now().UnixNano() bug this counter replaced)", v)
		}
		seen[v] = true
	}
}
