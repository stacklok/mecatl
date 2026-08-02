package tool

import (
	"context"
	"strings"
)

// AgentOrigin classifies the ADMISSION TIER an agent definition entered the
// registry through. It is a tier label, NEVER a location: no implementation
// may put a path, directory, URL, or any locator in it (that is the adapter's
// private business). Enforcement of trust happens at SOURCE CONSTRUCTION time
// in the composition layer (an untrusted workspace's project tier is never
// constructed); Origin exists for observability and inspection only.
//
// NOTE: this deliberately mirrors SkillOrigin rather than sharing a type. The
// THIRD origin-bearing seam has since arrived (prompt.RuleOrigin, issue #329)
// and extraction was re-evaluated and DEFERRED: the label sets are not
// identical (RuleOrigin has no "explicit" tier — rules carry no operator-flag
// lane), so a shared type would force a superset one seam must never mint.
type AgentOrigin string

// The CLOSED admission-tier label set — implementations must never mint a new
// label (a consumer that does not recognise one normalizes to Driver).
const (
	AgentOriginExplicit AgentOrigin = "explicit" // operator-configured location/flag
	AgentOriginProject  AgentOrigin = "project"  // workspace-tier (trust-gated at construction)
	AgentOriginUser     AgentOrigin = "user"     // user-tier (never trust-gated)
	AgentOriginDriver   AgentOrigin = "driver"   // operator-configured remote driver
)

// MaxAgentDescriptionBytes caps AgentDef.Description. The description is the
// EXPENSIVE field: it is ALWAYS-IN-CONTEXT routing metadata (it rides the
// Subagent tool's Spec().Description tail on EVERY request, summed across ALL
// registered agents, and is part of the byte-stable prompt-cache prefix), so an
// unbounded one would inflate every prompt and break prefix caching. It
// therefore stays CONSERVATIVE — 2000 bytes is roomy for a routing sentence or
// two but still bounds the per-request, all-agents tax. It is the ONE canonical
// cap every source shares: the filesystem frontmatter parser truncates to it on
// discovery, and a remote-driver client re-truncates wire metadata to it
// defensively (the conformance suite asserts every listed def respects it).
//
// Changing this value is a ONE-TIME prompt-cache-prefix invalidation (the
// truncation point moves), but discovery stays deterministic — the same input
// always truncates to the same bytes.
const MaxAgentDescriptionBytes = 2000

// MaxAgentBodyBytes caps AgentDef.Body. The body is CHEAP relative to the
// description: it becomes a system-prompt layer that is in-context only for
// THAT ONE specialist engine's own turns (never summed across agents, never on
// the parent's requests), so it can afford generous headroom — 32 KiB lets a
// real specialist persona carry detailed instructions without losing the tail.
// (Contrast a skill body, which loads only on activation.) Same single-cap
// discipline as MaxAgentDescriptionBytes — one canonical cap every source
// truncates to. Changing it is the same one-time, deterministic prefix
// invalidation as the description cap.
const MaxAgentBodyBytes = 32 * 1024

// AgentDef is a pure value object: one agent definition's metadata and
// system-prompt body. It carries no behaviour, no infrastructure types, and NO
// path/dir/root concept — where a definition came from is the source
// implementation's private business (Origin is a tier label, never a locator).
//
// Only Name and Description are REQUIRED (they are the routing metadata the
// Subagent tool enumerates always-in-context). Everything else is optional: a
// def with only name+description+body is a pure prompt persona on the call
// site's default tool set and the parent model.
type AgentDef struct {
	// Name is the def's stable identifier. It is the value the model passes to
	// the Subagent tool's `agent` arg to route to this def, and the team-member
	// AgentType handle. Agent names live in their OWN namespace and are NOT
	// validated against the tool catalog (an agent may share a name with a tool
	// without conflict).
	Name string
	// Description is the one-line summary: the cheap, always-in-context metadata
	// that steers the model on WHEN to route to this def. Single-line and
	// byte-capped by the source.
	Description string
	// Tools is the OPTIONAL allowlist of catalog tool names. Absent => the call
	// site's default set.
	Tools []string
	// DisallowedTools is an OPTIONAL subtractive filter applied AFTER
	// Tools/default.
	DisallowedTools []string
	// Model is the OPTIONAL model selector: an alias (sonnet/opus/haiku), a full
	// id, or "inherit"/empty (=> parent model). Aliases are resolved ONLY in the
	// composition layer, never here.
	Model string
	// Provider is the OPTIONAL provider-id selector (e.g. "openai",
	// "openrouter"). Empty => inherit the parent/session provider. It is PURE
	// DATA, orthogonal to Model: the id => provider resolution happens ONLY in
	// the composition layer. Mirrors Model exactly — a hint the composition
	// layer resolves.
	Provider string
	// PermissionMode is the OPTIONAL session permission mode hint
	// (default|plan|acceptEdits). Stored as the raw string; the domain mode
	// value object is resolved in the composition layer.
	PermissionMode string
	// MaxTurns is the OPTIONAL per-run turn cap. Zero => the caller default.
	MaxTurns int
	// MaxToolCalls is the OPTIONAL per-run tool-call cap. Zero => the caller
	// default. It mirrors MaxTurns: the composition layer maps it into the def's
	// session limits so a def's child (and its team-member session) is bounded
	// by it; a zero field falls back to the call site's default limit.
	MaxToolCalls int
	// Color is an OPTIONAL UX hint only (e.g. a TUI tag colour); it NEVER
	// affects execution.
	Color string
	// Skills is an OPTIONAL list of skill names to PRELOAD into this def's
	// engine. The composition layer resolves each name against the active skills
	// and injects the matched skill's body into the def's system prompt, so the
	// specialist starts with those playbooks already in context. An unknown name
	// is a non-fatal composition-time diagnostic.
	Skills []string
	// MCPServers is an OPTIONAL list of MCP servers to scope to this def's
	// engine. Each entry is EITHER a REFERENCE (a bare server name — the def
	// gets that already-configured main server's tools) OR an INLINE
	// streamable-HTTP server spec (name + url + optional headers — the def
	// connects its OWN server, whose tools never enter the main conversation).
	// An inline entry with no URL collapses to a reference.
	MCPServers []AgentMCPServer
	// Hooks is an OPTIONAL phase → shell-command map scoping lifecycle hooks to
	// this def's engine. Keys are governance hook phase names; an unknown phase
	// is a non-fatal composition-time diagnostic (validated in composition, not
	// here — the value object is taxonomy-free).
	Hooks map[string]string
	// Memory is the OPTIONAL persistent per-agent memory TIER selector. It is a
	// raw string, NEVER a path or locator (same discipline as Origin/Model/
	// Provider): the composition layer resolves the tier to a concrete directory.
	//   ""        => no memory (cold start, today's behaviour);
	//   "user"    => a cross-project per-agent dir under the XDG config base;
	//   "project" => workspace-relative, trust-gated like other project-tier
	//                artifacts (read only when the workspace is trusted).
	// The dir's MEMORY.md head is injected (read-only in v1) into the def's
	// system prompt at startup, so the specialist accumulates domain knowledge
	// across sessions. A scoped write path is deliberately deferred; the
	// directory scheme is forward-compatible with adding it later.
	Memory string
	// Body is the markdown content of the definition: the specialist's full
	// instructions, composed into the engine's system prompt by the composition
	// layer.
	Body string
	// Origin is the admission tier this def entered through (observability
	// only; see AgentOrigin). A tier label, NEVER a location.
	Origin AgentOrigin
}

// AgentMCPServer is one entry of a def's MCPServers. It is EITHER a reference
// to an already-configured (main) server — Name set, URL empty — OR an inline
// streamable-HTTP server the def connects on its own — Name + URL (+ optional
// Headers). IsReference reports which. It carries no transport object and no
// infrastructure type.
type AgentMCPServer struct {
	// Name is the server's identifier. For a reference it must match a
	// configured main server's Name; for an inline server it becomes the
	// mcp__<name>__ tool namespace. Required for both forms.
	Name string
	// URL is the inline server's streamable-HTTP endpoint. Empty => this entry
	// is a REFERENCE to an already-configured main server (no new connection).
	URL string
	// Headers are extra HTTP headers for an inline server. SECRET-SHAPED (e.g.
	// Authorization): never logged and never projected into any inventory/
	// snapshot surface; it rides the driver wire only because driver dials
	// refuse non-local cleartext entirely. Ignored for a reference entry.
	Headers map[string]string
}

// IsReference reports whether this entry references an already-configured main
// server (URL empty) rather than describing an inline server to connect.
func (s AgentMCPServer) IsReference() bool { return strings.TrimSpace(s.URL) == "" }

// AgentDefSource is the read-only seam agent definitions cross into the
// harness. Where a definition comes from — directories, a database, a registry
// process — is entirely the implementation's private business; no path,
// directory, or root concept appears here, so the engine cannot tell a
// filesystem source from a remote one.
//
// Lifecycle: sources are SNAPSHOT-semantics — ListAgentDefs is stable for the
// life of the source (the harness resolves once at build; per-def child
// engines are built once, and the build-once trust-gate-completeness invariant
// depends on resolve-once).
type AgentDefSource interface {
	ListAgentDefs(ctx context.Context) ([]AgentDef, error) // sorted by Name, unique
}
