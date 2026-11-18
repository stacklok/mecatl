// Package agentdef is the agent definition format: the fields a definition
// file carries, the parser that reads one, and the canonical form a definition
// digest covers. It depends on nothing else in Mecatl, so the identity broker
// and the harness can read definitions without one depending on the other.
//
// A definition file is a flat <name>.md file: a YAML frontmatter block between
// two "---" lines, then a markdown body. Parse reads the bytes of one file.
// Finding files, and deciding which locations count, is the caller's job.
//
// The engine's agentfs adapter parses with this package too, so the harness and
// the broker read every file the same way.
package agentdef

import "strings"

// FileExt is the extension of an agent definition file. A definition lives at
// <dir>/<name>.md, a flat file rather than a <name>/AGENT.md directory.
const FileExt = ".md"

// MaxDescriptionBytes caps Definition.Description. Parse truncates a longer
// description on a rune boundary, appends an ellipsis, and reports a note. It
// equals the engine's tool.MaxAgentDescriptionBytes.
const MaxDescriptionBytes = 2000

// MaxBodyBytes caps Definition.Body. Parse truncates a longer body on a rune
// boundary, appends an ellipsis, and reports a note. It equals the engine's
// tool.MaxAgentBodyBytes.
const MaxBodyBytes = 32 * 1024

// shellToolName is the name a legacy "Bash" entry in tools or disallowedTools
// is read as. It equals the engine's tool.ShellToolName.
const shellToolName = "Shell"

// Definition is one parsed agent definition. It carries what the file says and
// nothing about where the file came from. The json tags name the keys of the
// canonical form (see Canonical).
type Definition struct {
	// Name identifies the definition. It is required and trimmed.
	Name string `json:"name"`
	// Description says what the definition is for. It is required, trimmed,
	// and at most MaxDescriptionBytes.
	Description string `json:"description"`
	// Tools is the optional allowlist of tool names. Empty means the caller's
	// default set. A legacy "Bash" entry reads as "Shell".
	Tools []string `json:"tools,omitempty"`
	// DisallowedTools is the optional list of tool names removed after Tools
	// or the default set. A legacy "Bash" entry reads as "Shell".
	DisallowedTools []string `json:"disallowedTools,omitempty"`
	// Model is the optional model selector, such as an alias or a full ID.
	Model string `json:"model,omitempty"`
	// Provider is the optional provider selector.
	Provider string `json:"provider,omitempty"`
	// PermissionMode is the optional session permission mode hint.
	PermissionMode string `json:"permissionMode,omitempty"`
	// MaxTurns is the optional per-run turn cap; zero means the caller's.
	MaxTurns int `json:"maxTurns,omitempty"`
	// MaxToolCalls is the optional per-run tool call cap; zero means the
	// caller's.
	MaxToolCalls int `json:"maxToolCalls,omitempty"`
	// Color is an optional display hint.
	Color string `json:"color,omitempty"`
	// Skills names optional skills to preload.
	Skills []string `json:"skills,omitempty"`
	// MCPServers lists optional MCP servers, by reference or inline.
	MCPServers []MCPServer `json:"mcpServers,omitempty"`
	// Hooks maps a lifecycle phase to a shell command. The parser does not
	// check phase names.
	Hooks map[string]string `json:"hooks,omitempty"`
	// Memory is the optional memory tier: "", "user", or "project".
	Memory string `json:"memory,omitempty"`
	// Body is the markdown instructions after the frontmatter, trimmed and at
	// most MaxBodyBytes.
	Body string `json:"body,omitempty"`
}

// MCPServer is one MCP server a definition names. With no URL it refers to a
// server configured elsewhere; with a URL it is an inline streamable-HTTP
// server.
type MCPServer struct {
	// Name identifies the server.
	Name string `json:"name"`
	// URL is the inline server's endpoint; empty for a reference.
	URL string `json:"url,omitempty"`
	// Headers are the inline server's request headers. The values can be
	// secrets: never log or display them.
	Headers map[string]string `json:"headers,omitempty"`
}

// IsReference reports whether s refers to a server configured elsewhere rather
// than defining an inline one.
func (s MCPServer) IsReference() bool { return strings.TrimSpace(s.URL) == "" }
