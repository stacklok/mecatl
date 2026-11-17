package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ModelSettings retains explicit prepared controls. Nil intentionally omits a
// control, never inherits defaults. Assignment shares pointers; Clone detaches them.
type ModelSettings = sessionformat.ModelSettings

// ModelSettingsView borrows immutable controls for its lifetime. Navigation is
// O(1) without copying sibling payloads; zero has neither optional control.
//
//dtogen:include
type ModelSettingsView struct {
	value *sessionformat.ModelSettings
}
