// Package agentfs implements configurable AGENT DEFINITIONS — named subagent
// specialists (a prompt persona + a scoped tool allowlist + a model + a
// permission mode + run limits) discovered from operator-controlled markdown
// files. It is the agents analogue of the skills Source seam
// (internal/adapter/skills): a pluggable Source over precedence-ordered
// directories, a forgiving frontmatter parser, and a name-indexed Registry the
// composition root threads into BOTH the Subagent tool and (in a later slice)
// the team-member factory.
//
// This package graduated from internal/adapter/agents into the importable
// engine module (engine/adapter/agentfs) per #328; the root package re-exports
// it via alias.
//
// ONE DEFINITION, TWO CONSUMERS. A `<name>.md` file under a conventional dir is
// reusable as a Subagent delegate (Subagent(agent="<name>")) and, in a following slice,
// as a team-member role (MemberSpec.AgentType). This package only PRODUCES the
// definitions; the registry→engine translation lives in internal/app, exactly
// where buildChildEngine/buildMemberEngine already live.
//
// LAYERING: this is an ADAPTER. It reads files (discovery is an adapter concern)
// and may import os/yaml and the domain (session). NOTHING here is imported by a
// domain package, by engine/agent, or by internal/app's hot path — the agent
// loop receives only plain map[string]*Engine + metadata structs, never this
// package's types, preserving the no-adapter-import-from-agent layering rule.
//
// TRUST BOUNDARY: an agent-definition body is OPERATOR-CONTROLLED content (like a
// SKILL.md / AGENTS.md) — it legitimately steers the model and belongs in the
// system prompt, NOT the untrusted-user channel. Conventional dirs are therefore
// strict opt-in (mirroring skills), and there is no model-writable agent-draft
// path in this tier.
package agentfs

import "github.com/stacklok/mecatl/engine/tool"

// AgentDef is the pure value object for one agent definition. Phase C2 moved
// the type WHOLESALE (minus the old Path locator, plus the Origin tier label)
// to engine/tool as the payload of the tool.AgentDefSource port; this alias
// keeps every existing literal and signature in this adapter and its
// consumers compiling unmodified. Where a def was discovered is now the
// adapter-private detail channel (Discovered.Detail / Registry.Detail), never
// a field on the value object.
type AgentDef = tool.AgentDef

// AgentMCPServer is one entry of a def's `mcpServers` (reference or inline
// streamable-HTTP server). Moved to engine/tool alongside AgentDef; aliased
// here for compatibility (see AgentDef).
type AgentMCPServer = tool.AgentMCPServer
