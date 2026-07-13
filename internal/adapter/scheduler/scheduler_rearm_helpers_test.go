package scheduler_test

import (
	"github.com/stacklok/mecatl/engine/port"
)

// noReArmStore wraps a port.ScheduleStore and HIDES the optional
// ScheduleOneShotReArmer interface (by not forwarding ReArmOneShot). A store that
// does not implement ScheduleOneShotReArmer degrades to at-most-once — the
// re-arm scan must be a nil-safe type-assertion no-op, never a panic. It is the
// fixture for TestOneShotReArmStoreWithoutInterface.
type noReArmStore struct {
	port.ScheduleStore
}

// Compile-time assertion that noReArmStore satisfies port.ScheduleStore (the
// embedded interface forwards the required methods). It deliberately does NOT
// forward ReArmOneShot, so it does NOT satisfy port.ScheduleOneShotReArmer — the
// runtime type assertion in maybeReArmOneShots yields ok=false. (A negative
// compile-time check can't be expressed in Go, so the test asserts that at
// runtime.)
var _ port.ScheduleStore = (*noReArmStore)(nil)
