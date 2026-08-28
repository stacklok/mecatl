package ui

import (
	"strings"
	"testing"
)

// TestHelpOverlayReflectsPlatformMarking proves the model's injected platform
// marking reaches the real help-overlay render path.
func TestHelpOverlayReflectsPlatformMarking(t *testing.T) {
	m := helpModel(t, allOnCaps(), func(deps *Deps) {
		deps.scrollKeysMarking = func() string { return "fn+↑/fn+↓ (pgup/pgdn)" }
	})
	out := stripANSIstr(m.View().Content)

	if !strings.Contains(out, "fn+↑/fn+↓") {
		t.Errorf("help overlay did not reflect the injected scroll-key marking; want substring %q:\n%s", "fn+↑/fn+↓", out)
	}
	if !strings.Contains(out, "pgup/pgdn") {
		t.Errorf("help overlay lost the canonical pgup/pgdn name:\n%s", out)
	}
}
