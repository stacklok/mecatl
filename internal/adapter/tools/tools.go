// Package tools composes the model-facing tool catalog of the mecatl kit. The
// correctness- and security-critical filesystem tool bodies — Read, Edit, Write,
// Grep, Glob, and the optional Bash — now live in the importable engine module
// (engine/adapter/fstools) so external consumers of engine/agent get them, and
// their enforced invariants, by import; this package re-exports them via alias.go
// and adds the host-repo-coupled tools that CANNOT live in the engine module:
// FetchMcpResource (MCP-coupled). WebFetch and WebSearch are re-exported from
// their importable engine reference adapters.
//
// All() and Register() cover the always-available tools that need only a
// Workspace — the fstools filesystem tools plus WebFetch and FetchMcpResource,
// which need no extra dependency. Two tools are NOT in All() because they need an
// injected dependency and are constructed/registered separately by the
// composition root: Bash needs a tool.CommandRunner (NewBashTool(), an
// alias for fstools.NewBashTool; a deployment with no shell simply omits it), and
// WebSearch needs a search provider (NewWebSearchTool(provider)).
//
// Each tool parses its session.ToolCall.Args (JSON), runs against the Workspace
// seam, and returns a session.ToolResult. Recoverable, model-addressable
// failures (a missing argument, a failed Edit invariant, a non-existent file)
// are returned as an *error* ToolResult via session.NewToolError so the model
// can read and recover from them; the Go error return is reserved for
// harness-level faults the model cannot act on.
//
// Every tool carries a documentation-quality ToolSpec.Description: the
// description is the model's onboarding manual for the tool (gauntlet #10), so
// it states when to use the tool, when not to, one worked example, and its
// limits. Each tool also reports a correct ReadOnly() value, which drives the
// agent loop's read-parallel / mutate-serial dispatch (gauntlet #4).
package tools

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// All returns the always-available core tools as a fresh slice, ready for
// registration in the composition root. The order is the canonical catalog
// order: the filesystem tools from engine/adapter/fstools (Read, Edit, Write,
// Grep, Glob) followed by the host-repo web/MCP reads. Bash is NOT included: it
// requires a tool.CommandRunner and is optional — add it separately via
// NewBashTool when a runner is configured.
//
// FetchMcpResource (issue #223 Phase 2) is an outbound read like WebFetch, so
// it rides in BOTH profiles via All() and NoFS().
func All() []tool.Tool {
	return append(fstools.All(),
		NewWebFetchTool(),
		FetchMcpResourceTool{},
	)
}

// NoFS returns the core tools available in a NO-filesystem session (the "no-fs"
// session profile): WebFetch + FetchMcpResource. Every file-touching core tool
// — Read, Edit, Write, Grep, Glob (and the separately-constructed Bash) — is
// deliberately absent: a no-FS session has no workspace, so offering them
// would only generate honest-but-useless not-exist errors and burn turns. The
// composition root (internal/app registerCoreTools) selects NoFS() vs All() per
// the session's catalog profile; this is the single definition of the no-FS
// core surface. FetchMcpResource (issue #223 Phase 2) is an outbound read that
// needs no filesystem, so it stays in the no-FS profile alongside WebFetch.
func NoFS() []tool.Tool {
	return []tool.Tool{
		NewWebFetchTool(),
		FetchMcpResourceTool{},
	}
}

// Register adds the always-available core tools (everything in All(), i.e. NOT
// Bash) to cat. It returns the first registration error (e.g. a name collision)
// encountered, or nil on success. To enable command execution, additionally
// register NewBashTool(), e.g.
// cat.MustRegister(tools.NewBashTool()).
func Register(cat *tool.Catalog) error {
	for _, t := range All() {
		if err := cat.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// parseArgs unmarshals a tool call's JSON arguments into dst, delegating to the
// shared toolkit helper. It returns a model-facing error string (not a Go
// error) describing a malformed payload. Used by the host-repo web/MCP tools;
// the filesystem tools carry their own copy in engine/adapter/fstools.
func parseArgs(in session.ToolCall, dst any) (string, bool) {
	return toolkit.ParseArgs(in, dst)
}

// truncateBytes trims s to at most toolkit.MaxOutputBytes on a rune boundary,
// appending a marker when it does. It delegates to the shared toolkit helper.
func truncateBytes(s string) string {
	return toolkit.Truncate(s, toolkit.MaxOutputBytes)
}

// schema wraps a static JSON-schema literal as json.RawMessage for a ToolSpec,
// delegating to the shared toolkit helper.
func schema(s string) json.RawMessage { return toolkit.Schema(s) }
