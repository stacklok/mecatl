---
sidebar_position: 6
title: MCP client
description: Connect Mecatl to streaming-HTTP MCP servers and expose their tools to the agent.
---

# MCP client

Mecatl includes a built-in [Model Context Protocol](https://modelcontextprotocol.io)
client. Tools from connected MCP servers enter the agent's catalog as
`mcp__<server>__<tool>` and use the same dispatch, permission, and audit paths as
built-in tools.

---

## Streaming-HTTP only

Mecatl speaks the **streaming-HTTP (streamable-HTTP JSON-RPC) MCP transport only**. The stdio/subprocess transport is never used — the harness does not spawn external processes for MCP servers. This is a deliberate security constraint: running an MCP server as a child process would put arbitrary subprocess execution on the agent's critical path. If your MCP server currently speaks stdio, front it with an HTTP proxy (e.g. ToolHive's HTTP proxying layer, which Mecatl already integrates with).

---

## Configuration

Wire a server with `--mcp-server name=URL` (repeatable, one flag per server):

```sh
mecated serve \
  --mcp-server github=https://mcp.example.com/github \
  --mcp-server linear=https://mcp.example.com/linear \
  --workspace /path/to/workspace
```

The flag value is `<name>=<URL>` where `name` is the identifier that becomes the namespace prefix and `URL` is the streaming-HTTP endpoint. The same flag (and the token convention below) is accepted by all three headless binaries — `mecated`, `mecatequi`, and `mecak8s` — so a CI or scheduler-launched one-shot run can reach the same MCP endpoints as the daemon. The optional `mecatui` TUI has no `--mcp-server` flag; instead its embedded server reads operator-tier `mcp.servers` profiles from `~/.config/mecatl/settings.yaml` directly, with no flag required (a configured block with no loader wired surfaces a WARN).

**Auth token.** If the server requires a bearer token, set the environment variable `MCP_<NAME>_TOKEN` (uppercased name). Mecatl sends it in the `Authorization: Bearer …` header and never logs it. Server names must match `[A-Za-z0-9_]+` and be case-insensitively unique (the name derives the env var). By default, a token-bearing URL must use `https`, or `http` to a loopback host. `--mcp-server-insecure-http <name>` is an explicit per-server opt-in for non-loopback HTTP, for deployments whose network controls and short-lived token policy make that acceptable:

```sh
export MCP_GITHUB_TOKEN=ghp_…
mecated serve --mcp-server github=https://mcp.example.com/github …
```

## OAuth operator profiles

For servers that use OAuth, define an operator `mcp.servers` profile rather than putting
credentials in a URL or command line. A mutable local profile is authorized once with
`mecated mcp login SERVER [--no-browser] [--permission-config PATH ...]`; the repeatable
permission-config option selects trusted operator settings only and never carries OAuth values.
Normal serving then warm-restores the encrypted record,
refreshes lazily, persists refresh-token rotation, and remains warm after restart. Serving
and ACP never open a browser. Environment-backed profiles are read-only and require an
external Secret update plus process restart. Keep `static_bearer` as a rollback profile when
the server supports it. See the
[MCP OAuth and credentials](/features/mcp-oauth-and-credentials.md#configure-a-profile).

**ToolHive discovery.** If you run MCP servers with
[ToolHive](https://docs.stacklok.com/toolhive/), Mecatl discovers the running
servers automatically. You do not need the `--mcp-server` flag. ToolHive proxy
URLs use HTTP, which satisfies the streaming-HTTP requirement. Control discovery
with `--toolhive` (default `true`) and `--toolhive-group` (the default group when
empty).

---

## Tool namespacing

Every tool discovered from an MCP server is registered under `mcp__<server>__<tool>`. The double-underscore delimiter is part of the name — it prevents any remote tool from colliding with or shadowing a built-in.

Examples:

| Server name | MCP tool name | Catalog name |
|---|---|---|
| `github` | `create_issue` | `mcp__github__create_issue` |
| `linear` | `search_issues` | `mcp__linear__search_issues` |
| `exa` | `web_search_exa` | `mcp__exa__web_search_exa` |

The catalog name is what appears in permission rules. To allow or deny a specific MCP tool, use its full namespaced name:

```yaml
# settings.yaml
permissions:
  allow:
    - mcp__github__create_issue
  deny:
    - mcp__linear__delete_issue
```

A glob prefix like `mcp__github__*` matches all tools from the `github` server.

---

## Reconnect behavior

A concrete connection drop — the MCP server restarts, returns a plain HTTP 404
"session not found", closes the transport, reaches EOF, or refuses the connection —
does not take the server out for the rest of the run. The client reconnects
automatically. A server-declared call failure is different: structured JSON-RPC
400/404 responses and HTTP 429/502/503/504 responses are surfaced once on the
existing session and are **not replayed automatically**. This distinction prevents
a potentially mutating tool call from running twice after its response is rejected.

The root currently pins the official Go SDK to the exact unreleased revision
`v1.7.1-0.20260813084956-64e454e35c23` for these transport, cancellation, and
failed-connect lifecycle fixes. It will move to the first tagged release that is
verified to contain this revision or an equivalent successor; a merely newer tag
is not sufficient.

The reconnect logic sits on the server object (not on individual tool wrappers), so all tool calls, resource reads, and prompt expansions share one retry path:

1. Run the call against the live session.
2. On success, return.
3. On a connection drop, reconnect **once** and retry the call.
4. If the reconnect also fails, surface a clear terminal error to the model (`MCP server "<name>" unavailable after reconnect`) — never the raw transport string.

Concurrent calls that hit the same drop coalesce: the first one dials (holding a mutex), the rest wait and then receive the fresh session without dialing again. The dial is bounded by the server's configured timeout (default 30 s), so the mutex is never held indefinitely.

**Tool list is not re-fetched on reconnect.** The catalog snapshot taken at startup is preserved across a reconnect. If the server re-advertises a different tool set after restarting, the agent keeps the original specs until the next Mecatl process start. This is the expected v1 behavior.

Reconnect activity is logged through the standard diagnostics channel:

- `INFO mcp server reconnecting` — a drop was detected, dialing.
- `INFO mcp server reconnected` — the reconnect succeeded.
- `WARN mcp server reconnect failed` — the retry also failed; the call returns an error.

---

## Resources and prompts

Two optional behaviors are on by default:

**Resource meta-tools** (`--mcp-resource-tools`, default `true`). When a connected MCP server exposes resources, Mecatl registers `ListMcpResources` and `ReadMcpResource` meta-tools so the model can browse and read them. Disable with `--mcp-resource-tools=false`.

**Prompt expansion** (`--mcp-prompts`, default `true`). An MCP server's named prompts become expandable slash commands: `/mcp__<server>__<prompt> key=value`. The prompt spec is a static snapshot taken at connect time. An MCP prompt steers the model the same way a local slash command does — enable only for servers you trust.

---

## Typed tool results

An MCP tool result isn't always just text. The spec lets a server return a typed
content array — text, images, audio, embedded resources, `resource_link`
references — plus an optional structured JSON payload. Mecatl carries all of
that through instead of flattening it to a string.

The typed blocks ride on `ToolResult.Parts`, a `[]session.Content` field
alongside the existing `Content` string. It's additive: a legacy result with an
empty `Parts` is byte-identical to the pre-typed-results shape, so nothing
about older sessions or simpler servers changes.

**Capability gating decides what a block reaches the model.** Whether an image
or audio block is actually sent to the model depends on what the active
provider and model can accept — the same capability intersection (catalog ∩
adapter) that gates multimodal input elsewhere. Text, `resource_link`
references, embedded resources, and structured-content blocks always pass
through; only image and audio are gated on modality support.

**`Audience` is advisory display routing, never a suppression control.** A
content block can carry an `Audience` hint (e.g. `["user"]`) suggesting it's
meant for a human viewer rather than the model. Mecatl treats this as
advisory only — an MCP server is an untrusted supply-chain surface, and
trusting a server's own audience tag to *hide* content from the model would
let a malicious server smuggle a payload past the model's view (CWE-345). A
`["user"]`-tagged block may additionally render for a human-facing client;
the model always still gets its copy.

**`resource_link` URIs are never auto-dereferenced.** If a tool result points
at a resource by URI instead of embedding it, Mecatl does not fetch it
automatically — a server pointing at an internal or cloud-metadata host would
otherwise make Mecatl an SSRF proxy (CWE-918). The model can fetch it back
itself: an `https://` URI can be retrieved with the `FetchMcpResource` tool,
which validates the target through the same `ValidateMediaURL` check used
elsewhere (absolute HTTPS only, private/metadata IP ranges denied, redirects
re-validated). Non-`https` URIs are server-readonly — use `ReadMcpResource`
with the owning server name instead.

---

## Large and structured results: fail-closed truncation + CallMcpWithQuery

Every tool result is capped at a fixed output size before it enters context.
For plain text, truncating an oversized result with a marker is a reasonable
degrade — the model still gets a usable, if partial, string.

Truncation is not safe for **structured (JSON) results**: cutting a JSON blob
mid-token leaves an unparseable fragment the model can't do anything useful
with. Mecatl detects this case and fails closed instead of returning garbage.

A result counts as structured if any of these hold: the remote tool
advertised an `outputSchema`, the result carried `structuredContent`, or a
content block is JSON by MIME type or by parsing as JSON. When an oversized
result is structured, Mecatl returns an actionable tool error naming two ways
forward — narrow or paginate the call using the remote tool's own
filter/pagination parameters, or call it through **`CallMcpWithQuery`** with a
jq filter — rather than handing the model a truncated blob it can't parse.
Error results are exempt from this: a failed call's error text still
truncates as plain text, so the model can read what went wrong.

**`CallMcpWithQuery`** is a meta-tool that calls a remote MCP tool and filters
its JSON result through a [jq](https://jqlang.org) expression before the
result enters context, so a large response can be narrowed to just the
fields you need instead of being truncated. It takes the target `server` and
`tool` name, the remote tool's `args`, and a `jq_filter` expression. The
filter runs against a pure-Go jq implementation with file, stdin, and
environment access disabled — it can only see the JSON it's given — and is
bounded by a compute deadline and by input/output size limits, so a runaway
filter expression can't hang or blow up the context budget. Everything
happens in memory; nothing is spilled to disk, which is what keeps this tool
working the same way on a storage-free deployment as on a normal one. It's
read-only, so it participates in read-parallel dispatch like any other
read-only tool. Broker-only sessions expose the same tool only when their current
attachment has eligible frozen tools; its request, authorization, filtering, and
result stay on that attachment. The harness never bypasses the attachment with a
separate upstream connection. The concrete broker transport keeps its existing
routing (configured upstream URL for anonymous routes, broker endpoint for protected
routes), and a failed or ambiguous delivered call is not replayed.

---

## Server-initiated notifications

Mecatl keeps a persistent connection open to each MCP server so the server
can push notifications — most importantly `tools/list_changed`,
`prompts/list_changed`, and `resources/list_changed`, the server's signal
that its catalog has changed and should be re-fetched.

Receiving one of these doesn't trigger an immediate re-fetch. It marks the
corresponding list as stale; the next time that server's tools, prompts, or
resources are actually read, Mecatl re-fetches fresh and clears the staleness
flag. This keeps the notification handler itself cheap — it never blocks on a
network call — while still ensuring nothing is served stale forever.

One practical consequence: a new session created after a server announces a
change picks up the fresh tool set, but a tool catalog already assembled for
an in-flight session is not modified — Mecatl's tool catalog is append-only
within a session, so live catalog mutation for a running session isn't
supported yet. If a server drops a tool an existing session still has
registered, calling it surfaces an error the model can react to, rather than
the tool silently vanishing.

---

## Authentication and credentials

Authentication profiles, OAuth login, encrypted credential storage, rotation, and
managed-deployment credential provisioning are documented in [MCP OAuth and
credentials](/features/mcp-oauth-and-credentials.md). This page focuses on how
an authenticated MCP connection behaves once it is configured.

---

## Global vs per-session MCP servers

**Global servers** (`--mcp-server` / ToolHive discovery) are registered once at startup and shared across all sessions. Their tools are part of every session's catalog, including no-filesystem sessions. The global MCP manager is owned by the server process — it is never closed or reconnected per-session.

**Client (per-session) MCP servers** are wired by the API caller at session creation time, through the `CreateSession` request fields. They are set up for that session only and torn down when the session closes. A server caps the total number of live per-session engines; close sessions you are done with (`DELETE /v1/sessions/{id}` / `CloseSession` gRPC) to free slots. These are distinct from the globally-configured servers and are added on top of them, not instead.

---

## What's next

- [Extension points: tool catalog](/building/extension-points/tool-catalog.md) — add custom tools, configure skills, and control what the model can see.
