// Package toolkit holds the small, tool-agnostic helpers shared by the
// adapter-layer tool packages (internal/adapter/tools and
// internal/adapter/memory). It exists to keep a single source of truth for the
// cross-cutting mechanics every tool repeats — argument parsing, output
// truncation, and the JSON-schema literal wrapper — so they cannot drift apart.
//
// In particular the output cap lives here as the single MaxOutputBytes constant:
// previously each package carried its own 25_000 literal, a latent drift bug.
//
// What deliberately does NOT belong here: per-tool argument validation and the
// per-tool descriptions/schemas. Those are intentionally authored inline in each
// tool because they are the tool's own contract, not shared mechanics.
package toolkit

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

// MaxOutputBytes caps the byte length of a single tool's textual result. It is
// the one shared output cap for the adapter layer; tools append a truncation
// marker (see Truncate) when they trim to it. Keeping it here prevents the cap
// from drifting between tool packages.
const MaxOutputBytes = 25_000

// TruncationMarker is the suffix Truncate appends when it trims s to the byte cap.
// It is exported so a caller that needs to reserve room for additional content
// AFTER a truncated body (e.g. the Shell tool's timeout/cancel trailer, which must
// survive the cap) can account for the marker's worst-case length without
// hard-coding the literal. Truncate is the only writer of it.
const TruncationMarker = "\n... [output truncated: exceeded 25000 bytes]"

// ParseArgs unmarshals a tool call's JSON arguments into dst. An empty payload
// leaves dst at its zero value so tools with all-optional arguments work without
// an explicit "{}". On malformed JSON it returns a model-facing error string (not
// a Go error) and false; on success it returns "" and true.
//
// It delegates to session.ParseArgs — the single canonical implementation of this
// mechanic — preserving toolkit's existing exported signature for its callers.
func ParseArgs(in session.ToolCall, dst any) (string, bool) {
	return session.ParseArgs(in, dst)
}

// Schema wraps a static JSON-schema literal as json.RawMessage for a ToolSpec.
// The literals are authored by hand and are valid JSON; this is just a typed
// convenience for the Spec methods.
func Schema(s string) json.RawMessage { return json.RawMessage(s) }

// Truncate trims s to at most maxBytes, appending a clear truncation marker
// when it does. It cuts on a rune boundary so the result is never invalid UTF-8.
// Most callers pass MaxOutputBytes.
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

// TruncateRunes trims s to at most maxBytes on a rune boundary and appends a
// single-character ellipsis ("…"). Unlike Truncate (which appends a verbose,
// byte-count marker for tool output), this is the compact form used to cap
// always-in-context metadata such as agent/skill descriptions and bodies. When s
// already fits within maxBytes it is returned unchanged.
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

// SplitFrontmatter separates a leading YAML frontmatter block, delimited by a
// line containing only "---" at the very start and a matching closing "---" line,
// from the markdown body that follows. It returns the frontmatter text (without
// the delimiters), the body, and whether a well-formed frontmatter block was
// found. A leading UTF-8 BOM is tolerated and CRLF line endings are normalised so
// the delimiter match is line-ending agnostic. It is the single source of truth
// for the agents/skills frontmatter parsers.
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
