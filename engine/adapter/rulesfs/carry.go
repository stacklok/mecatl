// Package rulesfs carries small, tool-agnostic helpers this adapter needs that
// previously lived in root-module adapters (internal/adapter/toolkit,
// internal/adapter/xdgconfig). The engine module must not import the root
// module (the fstools precedent, #269), so the EXACT bodies are carried here
// and pinned byte-identical to their origins. Keep them in sync with the
// originals; do not diverge behaviour.
package rulesfs

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// SplitFrontmatter separates a leading YAML frontmatter block, delimited by a
// line containing only "---" at the very start and a matching closing "---" line,
// from the markdown body that follows. It returns the frontmatter text (without
// the delimiters), the body, and whether a well-formed frontmatter block was
// found. A leading UTF-8 BOM is tolerated and CRLF line endings are normalised so
// the delimiter match is line-ending agnostic. It is the single source of truth
// for the agents/skills frontmatter parsers.
//
// mirrors internal/adapter/toolkit.SplitFrontmatter EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
func SplitFrontmatter(s string) (fm, body string, ok bool) {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") && s != "---" {
		return "", "", false
	}
	rest := strings.TrimPrefix(s, "---\n")
	idx := IndexClosingDelim(rest)
	if idx < 0 {
		return "", "", false
	}
	fm = rest[:idx]
	after := rest[idx:]
	after = strings.TrimPrefix(after, "---")
	after = strings.TrimPrefix(after, "\n")
	return fm, after, true
}

// IndexClosingDelim returns the byte offset, within s, of the start of the first
// line that is exactly "---" (the closing frontmatter delimiter), or -1 if none.
//
// mirrors internal/adapter/toolkit.IndexClosingDelim EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
func IndexClosingDelim(s string) int {
	offset := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		trimmed := strings.TrimSuffix(line, "\n")
		if trimmed == "---" {
			return offset
		}
		offset += len(line)
	}
	return -1
}

// TruncateRunes trims s to at most maxBytes on a rune boundary and appends a
// single-character ellipsis ("…"). Unlike Truncate (which appends a verbose,
// byte-count marker for tool output), this is the compact form used to cap
// always-in-context metadata such as agent/skill descriptions and bodies. When s
// already fits within maxBytes it is returned unchanged.
//
// mirrors internal/adapter/toolkit.TruncateRunes EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
// The rune-boundary check is stdlib utf8.RuneStart (byte-identical to the old
// carried UTF8RuneStart helper, deleted per the #328 panel review).
func TruncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const ellipsis = "…"
	cut := maxBytes - len(ellipsis)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// ResolveEnv abstracts the process environment so a resolver is testable with a
// fake home / XDG, without touching the real one. The composition root binds
// OSEnv (the real os funcs); tests pass a fake.
//
// trimmed to the fields this adapter uses; the root xdgconfig.ResolveEnv also
// carries ReadFile for permconfig/soul.
//
// mirrors internal/adapter/xdgconfig.ResolveEnv EXACTLY (minus ReadFile) — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
type ResolveEnv struct {
	Getenv      func(string) string
	UserHomeDir func() (string, error)
}

// OSEnv binds a resolver to the real process environment + filesystem.
//
// mirrors internal/adapter/xdgconfig.OSEnv EXACTLY (minus ReadFile) — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
var OSEnv = ResolveEnv{Getenv: os.Getenv, UserHomeDir: os.UserHomeDir}

// UserConfigDir returns the XDG config base for a user-level config location: the
// value of $XDG_CONFIG_HOME when set, else ~/.config. It returns "" when neither
// can be resolved (the caller then skips the user-level source). This preserves
// the exact semantics the four adapters shared before extraction.
//
// mirrors internal/adapter/xdgconfig.UserConfigDir EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
func UserConfigDir(env ResolveEnv) string {
	if base := env.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	if home, err := env.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config")
	}
	return ""
}
