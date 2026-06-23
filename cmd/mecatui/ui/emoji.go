package ui

import (
	"os"
	"strings"
)

// emojiCapable reports whether the terminal is likely to render an emoji-presentation
// glyph (a VS16-promoted, width-2 cluster) faithfully. It is deliberately CONSERVATIVE
// and ENV-BASED — no terminal round-trip — mirroring welcome/kitty.go's detectKitty: a
// MISS falls back to the width-stable TEXT variant, which is always correct, so a false
// negative only costs the fancier glyph, never correctness (the whole reason the emoji
// is opt-in by detection rather than the default). A false positive at worst shows the
// text-presentation glyph on a terminal that could have rendered the emoji — also benign.
//
// Two override envs gate testing and user control (opt-out wins):
//   - MECATUI_NO_EMOJI=1 forces incapable (false) and WINS over force.
//   - MECATUI_FORCE_EMOJI=1 forces capable (true).
//
// Positive detection (any one is sufficient):
//   - TERM_PROGRAM in {ghostty/Ghostty, WezTerm/wezterm, iTerm.app, Apple_Terminal,
//     vscode} — modern terminals with reliable emoji-presentation rendering.
//   - TERM contains "kitty" — kitty renders VS16 emoji presentation.
//   - KITTY_WINDOW_ID set — kitty even when TERM is rewritten (tmux/ssh).
//   - KONSOLE_VERSION set — Konsole renders emoji presentation.
//   - any GHOSTTY_* env present — Ghostty even when TERM_PROGRAM is stripped (tmux).
//   - COLORTERM=truecolor — a modern-terminal proxy: a terminal advertising 24-bit
//     colour is overwhelmingly a recent emoji-capable emulator. The weakest signal,
//     last, and still conservative (a non-emoji truecolor terminal merely sees the
//     text glyph).
func emojiCapable() bool {
	return detectEmoji(osEmojiLookup, osEmojiEnviron)
}

// emojiEnvLookup / the environ lister abstract the environment for testability —
// production passes the os-backed pair, tests pass maps (mirrors kitty.go's seam).
type emojiEnvLookup func(key string) (string, bool)

func osEmojiLookup(key string) (string, bool) { return os.LookupEnv(key) }

func osEmojiEnviron() []string { return os.Environ() }

// detectEmoji is the pure core of emojiCapable, taking its environment as injectable
// functions so the truth table is unit-testable without mutating the process env.
func detectEmoji(look emojiEnvLookup, environ func() []string) bool {
	if v, ok := look("MECATUI_NO_EMOJI"); ok && truthyEnv(v) {
		return false // explicit opt-out wins over everything.
	}
	if v, ok := look("MECATUI_FORCE_EMOJI"); ok && truthyEnv(v) {
		return true
	}
	switch tp, _ := look("TERM_PROGRAM"); tp {
	case "ghostty", "Ghostty", "WezTerm", "wezterm", "iTerm.app", "Apple_Terminal", "vscode":
		return true
	}
	if term, ok := look("TERM"); ok && strings.Contains(strings.ToLower(term), "kitty") {
		return true
	}
	if _, ok := look("KITTY_WINDOW_ID"); ok {
		return true
	}
	if _, ok := look("KONSOLE_VERSION"); ok {
		return true
	}
	// Any GHOSTTY_* env present is a strong signal even when TERM_PROGRAM is unset
	// (tmux strips it).
	for _, kv := range environ() {
		if strings.HasPrefix(kv, "GHOSTTY_") {
			return true
		}
	}
	// COLORTERM=truecolor — the weakest, last signal: a modern-terminal proxy.
	if ct, ok := look("COLORTERM"); ok && strings.EqualFold(strings.TrimSpace(ct), "truecolor") {
		return true
	}
	return false
}

// truthyEnv reports whether an env value means "on". (Sibling of welcome/kitty.go's
// truthy, which lives in a different package — kept local to keep emoji.go
// self-contained, like the kitty precedent keeps its own.)
func truthyEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
