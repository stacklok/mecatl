// Package fstools implements the correctness- and security-critical filesystem
// tool bodies of the mecatl kit — Read, Edit, Write, Grep, Glob, and an OPTIONAL
// Bash — as tool.Tool values executing against an injected tool.Workspace (and,
// for Bash, an injected tool.CommandRunner). It travels WITH the importable
// engine module so an external consumer of engine/agent gets these tools — and
// their enforced invariants — by import, not by re-deriving them:
//
//   - Edit's three invariants: read-before-edit (+ unchanged-since), exact match,
//     and uniqueness-unless-replace_all.
//   - Write's read-before-overwrite (Edit invariant #1 for existing files).
//   - Read/Grep/Glob output caps with clear truncation markers.
//   - Bash's partial-output-preserving timeout/cancel handling and the trailer
//     that always survives the output cap.
//
// These bodies depend only on engine/session + engine/tool (+ stdlib). They never
// touch the real OS: Read/Edit/Write/Grep/Glob go through the tool.Workspace seam,
// and Bash goes through the injected tool.CommandRunner — so a consumer picks the
// FileSystem/Workspace and shell backend. The reference in-memory Workspace is
// engine/adapter/memfs; the honest no-op is engine/adapter/nofs.
//
// # Opt-in / opt-out
//
// The catalog is composed, not fixed. A consumer may:
//   - take everything: register All() (Read/Edit/Write/Grep/Glob) via Register,
//     then add NewBashTool(runner) only when a shell is configured;
//   - take a subset: register only the tool values it wants;
//   - swap a tool by name: register its own Tool under the same Spec().Name in
//     place of one of these;
//   - ignore the package entirely and supply its own tools.
//
// Bash is deliberately NOT in All(): it needs a tool.CommandRunner and command
// execution is optional. A deployment with no shell simply never constructs one.
//
// # Recoverable vs harness errors
//
// Each tool parses its session.ToolCall.Args (JSON), runs against the seam, and
// returns a session.ToolResult. Recoverable, model-addressable failures (a
// missing argument, a failed Edit invariant, a non-existent file) are returned as
// an *error* ToolResult via session.NewToolError so the model can read and
// recover from them; the Go error return is reserved for harness-level faults the
// model cannot act on.
//
// Every tool carries a documentation-quality ToolSpec.Description (the model's
// onboarding manual, gauntlet #10) and a correct ReadOnly() value, which drives
// the agent loop's read-parallel / mutate-serial dispatch (gauntlet #4).
//
// # Output cap
//
// MaxOutputBytes (25,000 bytes) mirrors internal/adapter/toolkit.MaxOutputBytes
// EXACTLY — keep those two byte-identical. It is redefined locally (not imported)
// because engine/ is its own module and toolkit lives under the host repo's
// internal/adapter tree, which the engine module must not import. The related
// domain bound engine/session.MaxToolResultTextBytes (25 KiB = 25,600 bytes) is a
// slightly LARGER upper bound on any tool-result text block that these tool caps
// sit under — deliberately not byte-identical.
package fstools

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Output-shaping limits shared across the tools. These keep a single tool result
// from blowing the model's context window; each tool appends a clear truncation
// marker when it trims output.
const (
	// maxReadLines caps how many lines the Read tool returns in one call.
	maxReadLines = 2000
	// maxGrepMatches caps how many Grep hits are returned in one call.
	maxGrepMatches = 200
	// maxGlobResults caps how many paths Glob returns in one call.
	maxGlobResults = 1000
)

// MaxOutputBytes caps the byte length of a single tool's textual result. It is
// the fstools output cap; tools append a truncation marker (see truncate) when
// they trim to it. It mirrors internal/adapter/toolkit.MaxOutputBytes EXACTLY
// (both 25,000 bytes); the domain block bound engine/session.MaxToolResultTextBytes
// (25 KiB = 25,600 bytes) is a larger upper bound these caps sit under.
const MaxOutputBytes = 25_000

// TruncationMarker is the suffix truncate appends when it trims a body to the
// byte cap. It is exported so a caller that must reserve room for content AFTER a
// truncated body (Bash's timeout/cancel trailer, which must survive the cap) can
// account for the marker's length without hard-coding the literal.
const TruncationMarker = "\n... [output truncated: exceeded 25000 bytes]"

// All returns the always-available filesystem tools as a fresh slice, in the
// canonical catalog order. Bash is NOT included: it requires a tool.CommandRunner
// and is optional — add it separately via NewBashTool when a runner is configured.
func All() []tool.Tool {
	return []tool.Tool{
		ReadTool{},
		EditTool{},
		WriteTool{},
		GrepTool{},
		GlobTool{},
	}
}

// Register adds the always-available filesystem tools (everything in All(), i.e.
// NOT Bash) to cat. It returns the first registration error (e.g. a name
// collision) encountered, or nil on success. To enable command execution,
// additionally register NewBashTool(runner), e.g.
// cat.MustRegister(fstools.NewBashTool(runner)).
func Register(cat *tool.Catalog) error {
	for _, t := range All() {
		if err := cat.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// parseArgs unmarshals a tool call's JSON arguments into dst. It delegates to the
// canonical session.ParseArgs, returning a model-facing error string (not a Go
// error) describing a malformed payload.
func parseArgs(in session.ToolCall, dst any) (string, bool) {
	return session.ParseArgs(in, dst)
}

// truncateBytes trims s to at most MaxOutputBytes on a rune boundary, appending
// TruncationMarker when it does.
func truncateBytes(s string) string {
	return truncate(s, MaxOutputBytes)
}

// truncate trims s to at most maxBytes, appending TruncationMarker when it does.
// It cuts on a rune boundary so the result is never invalid UTF-8.
func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + TruncationMarker
}

// schema wraps a static JSON-schema literal as json.RawMessage for a ToolSpec.
// The literals are authored by hand and are valid JSON.
func schema(s string) json.RawMessage { return json.RawMessage(s) }
