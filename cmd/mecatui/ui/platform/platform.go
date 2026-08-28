// Package platform reports host-platform facts the TUI adapts its presentation to.
// It is stdlib-only and lives under cmd/mecatui/ui so the ui package can import it
// without crossing the client/theme-only import boundary.
package platform

import (
	"os"
	"runtime"
)

// Platform is the host platform the TUI adapts its presentation to.
type Platform int

const (
	// PC is the default non-Mac desktop platform (Linux, Windows, etc.).
	PC Platform = iota
	// Mac is macOS (darwin): no dedicated PgUp/PgDn keys — the user presses
	// fn+↑/fn+↓ — so scroll-key markings adapt.
	Mac
)

// String renders the platform as the stable lowercase token used in env overrides
// and diagnostics: "pc" or "mac".
func (p Platform) String() string {
	switch p {
	case Mac:
		return "mac"
	default:
		return "pc"
	}
}

// Current returns the resolved platform. MECATUI_TEST_PLATFORM (values "pc" or
// "mac") can override detection; without it, the runtime platform is used. The
// environment is read at call time so production callers observe its current value.
func Current() Platform {
	return current(runtime.GOOS, os.Getenv)
}

func current(goos string, getenv func(string) string) Platform {
	switch getenv("MECATUI_TEST_PLATFORM") {
	case "mac":
		return Mac
	case "pc":
		return PC
	}
	if goos == "darwin" {
		return Mac
	}
	return PC
}

// ScrollKeysMarking returns the user-facing label for the page-up/page-down scroll
// chords, adapted to the current platform. Mac prepends the physical gesture
// (fn+↑/fn+↓) while keeping the canonical name in parens, so a user who has
// remapped their terminal still recognises it. Single source of truth — every site
// that renders a "pgup/pgdn scroll" hint reads this instead of hardcoding the literal.
func ScrollKeysMarking() string {
	return scrollKeysMarking(Current())
}

func scrollKeysMarking(p Platform) string {
	if p == Mac {
		return "fn+↑/fn+↓ (pgup/pgdn)"
	}
	return "pgup/pgdn"
}
