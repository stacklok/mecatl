---
sidebar_position: 6
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

For bearer authentication, set `MCP_<NAME>_TOKEN`, where `<NAME>` is the
uppercased server name. Mecatl sends the value in the `Authorization` header and
does not log it. Names must match `[A-Za-z0-9_]+` and must be unique without
regard to case.

A token-bearing URL must use HTTPS, except for loopback HTTP. The repeatable
`--mcp-server-insecure-http <name>` flag permits one named server to use
off-host HTTP. Use it only when network controls and short-lived tokens make
cleartext transport acceptable.

```sh
export MCP_GITHUB_TOKEN=<TOKEN>
mecated serve --mcp-server github=https://mcp.example.com/github
```

### OAuth operator profiles

For OAuth, configure an operator `mcp.servers` profile. Authorize a mutable
local profile once:

```sh
mecated mcp login SERVER [--no-browser] [--permission-config PATH ...]
```

Serving restores the encrypted record at startup and persists refresh-token
rotation. It never opens a browser. Environment-backed profiles are read-only;
update their Secret and restart the process to rotate them. See
[MCP OAuth and credentials](/features/mcp-oauth-and-credentials.md#configure-a-profile)
for profile configuration and recovery.

### ToolHive discovery

Mecatl discovers running ToolHive MCP servers by default, so they do not need
`--mcp-server` entries. Use `--toolhive=false` to disable discovery or
`--toolhive-group <group>` to select a group.

## Tool namespacing

Every remote tool is registered as `mcp__<server>__<tool>`. The namespace
prevents a remote tool from shadowing a built-in tool.

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

The startup catalog remains stable across a reconnect. Restart Mecatl to adopt a
changed tool list. Operator logs report when a reconnect starts, succeeds, or
fails.

## Resources and prompts

Mecatl enables two optional MCP capabilities by default:

- `--mcp-resource-tools=true` registers `ListMcpResources` and `ReadMcpResource`
  when a server exposes resources.
- `--mcp-prompts=true` exposes named prompts as
  `/mcp__<server>__<prompt> key=value` commands. Prompts can steer the model, so
  enable them only for trusted servers.

## Typed tool results

MCP results can contain text, images, audio, embedded resources, resource links,
and structured JSON. Mecatl preserves those content types. The active provider
and model determine whether image and audio blocks can reach the model; text,
resource links, embedded resources, and structured content always can.

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

When a server sends `tools/list_changed`, `prompts/list_changed`, or
`resources/list_changed`, Mecatl marks that list as stale. It refreshes the list
the next time a session reads it instead of making a network call in the
notification handler.

New sessions receive the refreshed catalog. An in-flight session keeps its
existing catalog, so calling a tool that the server removed returns an error
rather than changing the session's tools while it runs.

## Authentication and credentials

[MCP OAuth and credentials](/features/mcp-oauth-and-credentials.md) covers OAuth
login, encrypted credential storage, rotation, and Kubernetes provisioning.

## Global vs per-session MCP servers

|Server type|Lifecycle|Availability|
|-|-|-|
|Global|Configured at process startup and shared by all sessions|`--mcp-server`, operator profiles, or ToolHive discovery|
|Per-session|Created with one session and closed with it|Accepted only by deployments that advertise `mcp_servers_on_create`|

Per-session servers are added to the global catalog. The server limits how many
per-session engines can remain open, so close sessions you no longer need with
`CloseSession` or `DELETE /v1/sessions/{id}`.

## What's next

- [Tool catalog extension point](/building/extension-points/tool-catalog.md) to
  add custom tools and control the catalog exposed to the model.
- [MCP OAuth and credentials](/features/mcp-oauth-and-credentials.md) to
  configure authentication and rotation.
