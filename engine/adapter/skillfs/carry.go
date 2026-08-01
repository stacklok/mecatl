// Package skillfs carries small, tool-agnostic helpers this adapter needs that
// previously lived in root-module adapters (internal/adapter/toolkit,
// internal/adapter/xdgconfig, internal/adapter/osfs). The engine module must not
// import the root module (the fstools precedent, #269), so the EXACT bodies are
// carried here and pinned byte-identical to their origins. Keep them in sync with
// the originals; do not diverge behaviour.
package skillfs

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

// MaxOutputBytes caps the byte length of a single tool's textual result. It is
// byte-identical to toolkit.MaxOutputBytes (and fstools.MaxOutputBytes); kept
// here so the carried Truncate and the body-oversize warning share the one cap.
const MaxOutputBytes = 25_000

// TruncationMarker is the suffix Truncate appends when it trims s to the byte cap.
//
// mirrors internal/adapter/toolkit.TruncationMarker EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
const TruncationMarker = "\n... [output truncated: exceeded 25000 bytes]"

// ParseArgs unmarshals a tool call's JSON arguments into dst. An empty payload
// leaves dst at its zero value so tools with all-optional arguments work without
// an explicit "{}". On malformed JSON it returns a model-facing error string (not
// a Go error) and false; on success it returns "" and true.
//
// It delegates to session.ParseArgs — the single canonical implementation of this
// mechanic — preserving toolkit.ParseArgs's exported signature for its callers.
//
// mirrors internal/adapter/toolkit.ParseArgs EXACTLY (a one-line delegate to session.ParseArgs) — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
func ParseArgs(in session.ToolCall, dst any) (string, bool) {
	return session.ParseArgs(in, dst)
}

// Schema wraps a static JSON-schema literal as json.RawMessage for a ToolSpec.
// The literals are authored by hand and are valid JSON; this is just a typed
// convenience for the Spec methods.
//
// mirrors internal/adapter/toolkit.Schema EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
func Schema(s string) json.RawMessage { return json.RawMessage(s) }

// Truncate trims s to at most maxBytes, appending a clear truncation marker
// when it does. It cuts on a rune boundary so the result is never invalid UTF-8.
// Most callers pass MaxOutputBytes.
//
// mirrors internal/adapter/toolkit.Truncate EXACTLY — keep byte-identical; carried because engine must not import the root module (fstools precedent, #269).
func Truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + TruncationMarker
}

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

// resolveRoot makes root absolute and evaluates symlinks where possible so that
// later escape checks compare canonical paths. When the path itself does not
// exist yet (e.g. a SkillsDraftDir validated before it is created), EvalSymlinks
// fails on the leaf; we then resolve the deepest EXISTING ancestor and re-append
// the non-existent tail. Without this, a non-existent path under a symlinked
// root (macOS /var/folders -> /private/var/folders) keeps the unresolved form
// while an existing sibling resolves through the symlink, so two paths referring
// to the same on-disk location compare unequal — defeating the dirsOverlap
// containment check in validateSkillDraftConfig (a security-boundary bypass).
//
// mirrors internal/adapter/osfs.resolveRoot EXACTLY — the read-root allowlist an osfs Workspace enforces is derived from skillBaseDir's output, so the two must agree byte-for-byte across a symlinked home (/home → /var/home).
func resolveRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	// The leaf does not exist (or is otherwise unresolvable). Canonicalize the
	// longest existing prefix and re-append the non-existent tail, so a
	// not-yet-created dir under a symlinked root lands in the same canonical
	// form as its existing parent.
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			// An ambiguous stat error: best-effort — fall back to the cleaned
			// absolute form rather than failing (resolveRoot has no error
			// sentinel for "unverifiable" and callers treat error as fatal).
			return filepath.Clean(abs), nil
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached the filesystem root without an existing ancestor; nothing
			// to canonicalize against. Cleaned abs is the best we can do.
			return filepath.Clean(abs), nil
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return filepath.Clean(abs), nil
	}
	if len(tail) == 0 {
		return resolved, nil
	}
	return filepath.Join(append([]string{resolved}, tail...)...), nil
}
