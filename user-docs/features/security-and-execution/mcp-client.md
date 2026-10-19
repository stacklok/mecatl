---
slug: /features/mcp-client
sidebar_position: 315
title: MCP client
description:
  Connect Mecatl to streaming-HTTP MCP servers and expose their tools to the
  agent.
---

# MCP client

Mecatl connects agents to
[Model Context Protocol](https://modelcontextprotocol.io) servers. Connected
tools enter the catalog as `mcp__<server>__<tool>` and use the same permission,
dispatch, guardrail, and audit paths as built-in tools.

## Transport

Mecatl supports the streaming-HTTP (streamable-HTTP JSON-RPC) transport. It does
not start stdio MCP servers as subprocesses. To use a stdio server, place it
behind an HTTP proxy such as ToolHive.

## Configuration

Add a global server with the repeatable `--mcp-server name=URL` flag:

```sh
mecated serve \
  --mcp-server github=https://mcp.example.com/github \
  --mcp-server linear=https://mcp.example.com/linear \
  --workspace /path/to/workspace
```

`mecated`, `mecatequi`, and `mecak8s` accept this flag. Embedded `mecatui`
servers instead read operator profiles from `~/.config/mecatl/settings.yaml`.

<span id="authentication-and-credentials" />

### Authenticate a configured server

Use an operator `mcp.servers` profile for OAuth or deployment-managed
credentials, or the bearer-token convention for a flag-configured server. See
[MCP OAuth and credentials](/features/security-and-execution/mcp-oauth-and-credentials.md)
for setup, HTTPS requirements, login, encrypted custody, rotation, and recovery.
Authentication identifies the MCP connection; namespaced tool permissions still
control what each session may call.

### ToolHive discovery

Mecatl polls ToolHive discovery through one bounded loop and publishes each
complete MCP runtime as an immutable revision. It publishes additions and
removals together after validating the new runtime. If discovery or validation
fails, Mecatl keeps the last usable runtime and marks the cached source status
stale. Use `--toolhive=false` to disable discovery or `--toolhive-group <group>`
to select a group.

## Tool namespacing

Every remote tool is registered as `mcp__<server>__<tool>`. The namespace
prevents a remote tool from shadowing a built-in tool. The complete catalog name
must be valid UTF-8, contain no control characters, and fit within 256 bytes.
Mecatl rejects malformed names rather than rewriting their identity.

|Server|Remote tool|Catalog name|
|-|-|-|
|`github`|`create_issue`|`mcp__github__create_issue`|
|`linear`|`search_issues`|`mcp__linear__search_issues`|
|`exa`|`web_search_exa`|`mcp__exa__web_search_exa`|

Use the complete catalog name in permission rules. A prefix such as
`mcp__github__*` matches every tool from that server.

```yaml
permissions:
  allow:
    - mcp__github__create_issue
  deny:
    - mcp__linear__delete_issue
```

## Reconnect behavior

Mecatl reconnects once after a connection drop and retries the interrupted
operation. Concurrent operations share that reconnect attempt. If reconnecting
fails, the model receives `MCP server "<name>" unavailable after reconnect`.

Mecatl does not automatically replay a server-declared failure, including
structured JSON-RPC 400/404 responses and HTTP 429/502/503/504 responses. This
avoids running a mutating operation twice when its first response is ambiguous.

A reconnect keeps the operation's pinned runtime revision. Source reconciliation
publishes a replacement with the servers that connected and listed their tools,
resources, and prompts successfully; failures are reported in source diagnostics.
If all servers fail during a refresh, the last usable runtime stays active.

## Resources and prompts

Mecatl enables two optional MCP capabilities by default:

- `--mcp-resource-tools=true` registers `ListMcpResources` and `ReadMcpResource`
  when a server exposes resources.
- `--mcp-prompts=true` exposes named prompts as
  `/mcp__<server>__<prompt> key=value` commands. Prompts can steer the model, so
  enable them only for trusted servers.

## Typed tool results

MCP results can contain text, images, audio, embedded resources, resource links,
and structured JSON. Mecatl preserves typed blocks in `ToolResult.Parts`; older
string-only results leave `Parts` empty. The active provider and model determine
whether image and audio blocks can reach the model; text, resource links,
embedded resources, and structured content always can.

An MCP server's `Audience` value is a display hint, not an access control.
Mecatl still sends every block to the model because the remote server is not
trusted to suppress model-visible content.

Mecatl does not automatically follow a `resource_link`. For HTTPS resources, the
model can call `FetchMcpResource`, which rejects private and metadata addresses
and revalidates redirects. For other URI schemes, use `ReadMcpResource` with the
server that owns the resource.

## Large and structured results

Mecatl truncates oversized plain-text results before they enter context. It
returns an error for oversized structured results because truncating JSON can
make it invalid. A result is structured when the tool declares an
`outputSchema`, returns `structuredContent`, or returns a JSON content block.
Remote tool errors remain plain text and use normal bounded truncation.

To reduce a structured result, use the remote tool's pagination or filtering
arguments. You can also call the tool through `CallMcpWithQuery`, which applies
a jq expression before the result enters context:

- `server` and `tool` select the remote operation.
- `args` contains the remote tool arguments.
- `jq_filter` selects the required JSON fields.

The filter runs in memory without file, standard input, or environment access.
Compute, input, and output limits bound its resource use. Broker sessions use
their existing attachment and authorization without a second upstream
connection.

## Server-initiated notifications

When a current server sends `tools/list_changed`, `prompts/list_changed`, or
`resources/list_changed`, Mecatl reconciles all three contract lists through the
same bounded path as ToolHive polling. Notifications from a retired runtime do
not affect the current publication.

A run or direct team operation keeps the runtime revision it started with. The
next operation pins the current publication, so it sees a successful addition or
removal without mixing old schemas with new dispatch targets.

## Refresh MCP tools for a session

Automatic reconciliation updates availability but does not grant newly added
names to an existing session. Run `/mcp-refresh` in `mecatui`, call
`RefreshMcpSources`, or send a bodyless `POST` to
`/v1/sessions/<SESSION_ID>/mcp-refresh` while the owned root session is idle or
completed. The operation adds currently active direct MCP names to that
session's existing name authority. It preserves completed state and does not
reopen the conversation.

A name that disappears is unavailable but remains in the session's durable name
authority. If the exact name returns, the existing grant applies again. A
refresh response reports the runtime revision pinned for that operation, which
may no longer be the latest revision when the response arrives, and whether that
operation's reconciliation cycle or authority union changed anything. Inspect
`ListMcpSources` for the cached current revision, source diagnostics, stale, and
reconciliation status; the status call does not probe upstream servers.

Refresh failures use generic client messages. Inspect `ListMcpSources` to decide
whether reconciliation is stale or still running, then retry after the reported
condition clears. A save or transport failure can be ambiguous once persistence
starts: retry the same refresh. The stable union is idempotent, so a retry
either confirms the committed names or applies the still-missing names without
removing existing authority.

## Global vs per-session MCP servers

|Server type|Lifecycle|Availability|
|-|-|-|
|Global|Configured at process startup and shared by all sessions|`--mcp-server`, operator profiles, or ToolHive discovery|
|Per-session|Created with one session and closed with it|Accepted only by deployments that advertise `mcp_servers_on_create`|

Mecatl connects each global MCP server independently. If one server cannot
initialize or list its tools, resources, or prompts, the available servers still
provide their tools. Check the MCP source inventory for a diagnostic naming the
unavailable server; connection errors have credential-bearing URL parts removed.
Refresh the MCP inventory to retry failed connections. If no server can reconnect,
Mecatl keeps the last published runtime until a usable replacement is ready.

Per-session servers are added to the global catalog. The server limits how many
per-session engines can remain open, so close sessions you no longer need with
`CloseSession` or `DELETE /v1/sessions/{id}`.

## Next steps

- [Tool catalog extension point](/building/go/extension-points/tool-catalog.md)
  to add custom tools and control the catalog exposed to the model.
- [MCP OAuth and credentials](/features/security-and-execution/mcp-oauth-and-credentials.md)
  to configure authentication and rotation.
