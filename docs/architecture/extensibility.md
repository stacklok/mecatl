# Extensibility

> Part of the [Mecatl architecture guide](../architecture.md).

MCP servers add remote tools, skills add instructions that load on demand, slash
commands expand reusable prompts, and web tools reach the internet, all without
changing the agent loop. Remote tools join the same `tool.Catalog` as built-in
ones, so the same permission, dispatch, hook, guardrail, and audit paths apply.

## MCP

`internal/adapter/mcp` wraps the official Go SDK and turns each remote tool into
an ordinary `tool.Tool`. It speaks only streamable HTTP and never spawns a
server. A stdio server is a program started from configuration, which can come
from a client's create request or an agent definition, so spawning it would run
code outside the Shell permission path and secret scrubbing. An HTTP server stays
a network peer the operator runs and isolates, for example with ToolHive.
`mcp.PartitionClientServers` rejects stdio-shaped entries unconditionally.

### Where servers come from

| Source | Lifetime | Notes |
|-|-|-|
| Global (direct) | Process | Flags, operator `mcp.servers` profiles (the only direct OAuth path), or ToolHive discovery through `mcp/source.Source` |
| Per-session | One session | Client-supplied: HTTPS or loopback HTTP, no URL userinfo, no redirects, static headers only |
| Agent definition | Child engine | A reference to a global server, or an inline server whose tools never enter the parent conversation |
| Broker | One session | Owner-authorized catalogue through ToolHive; exclusive with global servers |

### Admission and naming

A remote tool's catalog name is `mcp__<server>__<tool>`. Server names cannot
contain `__`, so a remote tool can't shadow a built-in or another server's tool.
The name must be valid UTF-8 without control characters, at most 256 bytes.
Mecatl rejects a bad name rather than repairing it, because permission rules and
session grants match that exact name. A tool is read-only, and may run in
parallel, only if the server sets `readOnlyHint`. Direct registration is
first-wins: global tools register first, so a colliding per-session tool is
skipped with a warning.

Each session holds a durable set of MCP names it may call. Reconciliation changes
which tools are available, never what a session may call. Only an explicit owner
refresh (`RefreshMcpSources`) adds active direct names to an idle or completed
session. A lost connection gets one bounded reconnect and retry, but an ambiguous
send or server-declared failure is never replayed, since it may have mutated.

ToolHive polling, server `list_changed` notifications, and explicit refreshes
feed one serialized reconciler in `internal/app`. Each cycle builds a complete,
immutable runtime and publishes it as one revision. A run, team operation, or
resource/prompt call pins its starting revision, so schemas and dispatch targets
never mix. A failed cycle keeps the last usable runtime and marks it stale, and
`ListMcpSources` reports that cached status without probing upstreams.

### The session-scoped broker

Broker mode (`internal/adapter/mcpbroker`) is one in-process runtime owned by
`app.Built`, and `app.Build` rejects a configuration that also sets global
`MCPServers`. ToolHive runs each upstream's OAuth and injects backend tokens;
Mecatl holds only an opaque outer broker credential. `mecated` and `mecak8s`
mount the broker's fixed `HandlerBundle` and an operator-configured callback on
their primary HTTP mux; see [deployment and hardening](deployment-and-hardening.md).

A session gets its catalogue one of two ways:

- **Lazy authorization.** Trusted static `tools:` declarations appear at once as
  placeholders. The first call parks while the user authorizes the whole bundle,
  authenticated discovery replaces the placeholders, and the call resumes.
- **Pre-prompt enrollment.** Every backend is queried and results are staged
  privately. Each definition must pass `validateAuthenticatedRoute` (the same
  naming rules plus size and collision checks). One frozen catalogue then
  replaces the placeholders atomically, and any failure admits nothing.

The catalogue stays frozen until the owner explicitly refreshes. Broker state is
process-local: a persisted binding is never treated as live after a restart, so
broker tools stay unavailable until the owner enrolls again, and the Helm chart
requires `replicaCount: 1` with broker callbacks. The owner-scoped connector
inventory (mecatui's `/mcp` panel) reports what was published, not health.

### Secret-shaped headers

Headers on per-session, inline agent-definition, and bearer-profile servers
usually carry credentials. Mecatl sends them unchanged and keeps them out of
logs, errors, events, inventories, snapshots, and configuration projections. URL
userinfo is rejected because `net/http` would turn it into an `Authorization`
header that bypasses those rules, and logged URLs are redacted.

## Skills

A skill is a `SKILL.md` file (frontmatter `name` and `description`, then a body)
plus optional bundled files. The read-only core is `engine/adapter/skillfs`.

### Discovery and trust

Skills cross the `tool.SkillSource` port as logical bundles: metadata, a body,
and assets addressed by relative name, never a path. Under the port,
`skillfs.MultiSource` composes sources in order, and an earlier source wins a
name collision: explicit directories, then the project tier (`.mecatl/skills`,
`.claude/skills`), then the user tier. The conventional tiers are opt-in, and
composition never builds the project tier for an untrusted workspace. A remote
driver source replaces local discovery. Malformed skills are skipped with a
diagnostic, and descriptions are capped so one can't bloat every request.

### The Skill tool

`Skill` is registered only when a skill exists. Its description lists each name
and description, the cheap and cache-stable layer. `{name}` returns the body and
a list of asset names. `{name, asset}` returns one capped textual asset.

Skills expose logical assets, not extra workspace or execution roots: nothing is
materialized, and `Read` and `Shell` gain no access. A skill may live in a driver
or outside the workspace, so a path would create a read or execute root outside
confinement and trust. Being read-only, `Skill` works in plan mode and
no-filesystem sessions. A skill's `allowed-tools` field is advisory; the
permission evaluator never reads it.

The same inventory backs `/<skill-name>`, which loads the body and asset list but
not asset content. `buildCommandExpander` (`internal/app/build.go`) chains
file-backed commands, skills, driver commands, then MCP prompts; the first match
wins. Untrusted project skills therefore never become commands.

`SkillDraft` (`internal/adapter/skills`) lets the model propose a skill but never
activate it in the writing session. With a learned-skill store, a draft goes live
only after validation and evaluation (see [memory](memory.md)). Otherwise drafts
land in a quarantine outside the workspace and apart from active skill
directories until an operator runs `mecated skills promote`. `Shell` can still
write anywhere, so Mecatl warns when both tools are enabled.

## Project instructions

`AGENTS.md` and `CLAUDE.md` pass the same trust gate: `prompt.RootAssembler`
reads only an admitted source and its subtree. Nested files load lazily: when a
structured file tool (Read, Edit, Write, Remove, Copy, or Move) reaches a new
directory, that scope's instructions arrive with the next request. Shell and
search tools don't activate nested scopes. The [agent loop](agent-loop.md) covers
where instructions sit in each request.

## Progressive disclosure

Skills are the progressive disclosure that ships. For tool schemas, a
`tool.Disclosable` tool advertises a metadata-only spec and the model loads the
full schema with `ToolSearch`. `agent.Deps.ProgressiveTools` gates this and
defaults to off; shipped hosts leave it off. Wrappers such as frozen broker tools
forward `Advertised()` so wrapping never widens what the model sees.

## Built-in web retrieval

`WebSearch` uses a `tool.SearchProvider`. The default calls Exa's anonymous
endpoint through a minimal client rather than the MCP manager: a search is a
per-call lookup, not a set of catalog tools, and the client never follows OAuth
discovery, so it can't park on a browser flow. Each provider has its own timeout
and concurrency limit, because read-parallel dispatch can fan out many searches.

`WebFetch` (`engine/adapter/webfetch`) reads one public text resource. It
validates every resolved address and pins it into the dial, again on each of at
most five redirects. It uses no proxy, cookies, or caller headers, caps raw and
decompressed bodies at 5 MiB, parses HTML without running it, and fences at most
25,000 bytes as untrusted. Both tools stay available to no-filesystem children.

## Extending the engine as a library

An embedder of the `engine` module registers `tool.Tool` values and can supply a
`tool.SkillSource`, `prompt.CommandSource`, `prompt.InstructionAssembler`, or
`tool.SearchProvider`. `skillfs`, `webfetch`, and `search` ship in the engine
module; the MCP client is root-module `internal/`, so an embedder brings its own.
See [extension points](../../user-docs/building/go/extension-points/index.md) and
the [API surface](api-surface.md) for the compatibility contract.

## Related

- [Ports](ports.md)
- [Governance](governance.md)
- [Memory](memory.md)
- [MCP client](../../user-docs/features/security-and-execution/mcp-client.md)
- [Skills, commands, and soul](../../user-docs/features/agent-behavior/skills-commands-and-soul.md)
