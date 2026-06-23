package ui

import "testing"

// mapEmojiLookup adapts a map to the emojiEnvLookup signature.
func mapEmojiLookup(m map[string]string) emojiEnvLookup {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func mapEmojiEnviron(m map[string]string) func() []string {
	return func() []string {
		out := make([]string, 0, len(m))
		for k, v := range m {
			out = append(out, k+"="+v)
		}
		return out
	}
}

// TestDetectEmojiTruthTable exercises the conservative env-based detection plus the
// force/no override envs (mirrors welcome/kitty_test.go's TestDetectKittyTruthTable).
// A default miss is false — the width-stable text fallback, always correct.
func TestDetectEmojiTruthTable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"empty", map[string]string{}, false},
		{"plain xterm", map[string]string{"TERM": "xterm-256color"}, false},
		// Positive signals.
		{"ghostty term_program lower", map[string]string{"TERM_PROGRAM": "ghostty"}, true},
		{"ghostty term_program cap", map[string]string{"TERM_PROGRAM": "Ghostty"}, true},
		{"wezterm", map[string]string{"TERM_PROGRAM": "WezTerm"}, true},
		{"wezterm lower", map[string]string{"TERM_PROGRAM": "wezterm"}, true},
		{"iterm", map[string]string{"TERM_PROGRAM": "iTerm.app"}, true},
		{"apple terminal", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, true},
		{"vscode", map[string]string{"TERM_PROGRAM": "vscode"}, true},
		{"TERM kitty", map[string]string{"TERM": "xterm-kitty"}, true},
		{"kitty window id", map[string]string{"KITTY_WINDOW_ID": "1"}, true},
		{"konsole", map[string]string{"KONSOLE_VERSION": "220400"}, true},
		{"ghostty env", map[string]string{"GHOSTTY_RESOURCES_DIR": "/x"}, true},
		{"colorterm truecolor", map[string]string{"COLORTERM": "truecolor"}, true},
		{"colorterm truecolor mixed case", map[string]string{"COLORTERM": "TrueColor"}, true},
		// COLORTERM with surrounding whitespace is still detected (the TrimSpace path).
		{"colorterm truecolor padded", map[string]string{"COLORTERM": "  truecolor\t"}, true},
		{"colorterm 256 not enough", map[string]string{"COLORTERM": "256color"}, false},
		// DECISION: the detector matches only the canonical COLORTERM value "truecolor";
		// "24bit" is a less-common synonym some terminals export but is NOT in the
		// documented signal list, so it does NOT count (a 24bit-only terminal falls back to
		// the always-correct text glyph). Pin that decision here.
		{"colorterm 24bit not matched", map[string]string{"COLORTERM": "24bit"}, false},
		// Overrides.
		{"force on", map[string]string{"MECATUI_FORCE_EMOJI": "1"}, true},
		{"no wins over force", map[string]string{"MECATUI_FORCE_EMOJI": "1", "MECATUI_NO_EMOJI": "1"}, false},
		{"no over detection", map[string]string{"KITTY_WINDOW_ID": "1", "MECATUI_NO_EMOJI": "true"}, false},
		{"no over colorterm proxy", map[string]string{"COLORTERM": "truecolor", "MECATUI_NO_EMOJI": "yes"}, false},
		// Opt-out short-circuits BEFORE the TERM_PROGRAM switch: a recognised terminal
		// plus MECATUI_NO_EMOJI is still false.
		{"no over recognised term_program", map[string]string{"TERM_PROGRAM": "ghostty", "MECATUI_NO_EMOJI": "1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detectEmoji(mapEmojiLookup(tc.env), mapEmojiEnviron(tc.env))
			if got != tc.want {
				t.Errorf("detectEmoji(%v) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}
