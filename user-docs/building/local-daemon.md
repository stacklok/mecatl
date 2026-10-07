---
title: Host a private daemon from an application
description: Own mecated startup, readiness, and shutdown from an editor or wrapper.
sidebar_position: 6
---

# Host a private daemon from an application

Start a private `mecated` child process when your editor or application needs
the supplied server composition while owning its lifetime. For TypeScript,
[SDK spawn and query](typescript-sdk/local-daemon.md) manage these resources
for you. This guide covers the hosting contract for another language or wrapper.

Install [mecated](/operating/mecated.md#install-mecated) before spawning it.
The parent owns its runtime directory, child process, connection, and cleanup;
Mecatl's lifetime channel makes child shutdown follow parent loss.

## Start a private child process

Create an owner-only runtime directory and a pipe or connected UNIX socket
pair in your parent process. Pass its read endpoint as inherited descriptor 3
to the child, keep the other endpoint open, and start `mecated` with these
arguments. Replace `<RUNTIME_DIR>` with the absolute private directory:

```sh
mecated serve \
  --grpc-unix-socket <RUNTIME_DIR>/mecated.sock \
  --http-addr "" \
  --ready-file <RUNTIME_DIR>/ready.json \
  --lifetime-pipe-fd 3
```

`--grpc-unix-socket` serves gRPC without opening a TCP port. Dial the example as
`unix://<RUNTIME_DIR>/mecated.sock`. Mecatl creates the socket
owner-only, rejects unsafe parent-directory permissions, removes stale sockets,
and refuses to replace a live listener. Configuring both the socket and
`--grpc-addr` is an error. An empty `--http-addr` disables both HTTP/SSE and the
admin listener, so it cannot be combined with `--perf-mcp`. Keep the full socket
path within 103 bytes on macOS or 107 bytes on Linux.

`--ready-file` is published atomically after every listener is bound. A freshly
published file means the parent can dial the daemon:

```json
{
  "schema": "mecated-ready/1",
  "pid": 5821,
  "transport": "unix",
  "grpc_address": "<RUNTIME_DIR>/mecated.sock",
  "socket_path": "<RUNTIME_DIR>/mecated.sock",
  "api_major": 1,
  "features": ["mcp_servers_on_create", "server_info"],
  "deployment": "eu-west-1 staging"
}
```

Use `api_major` and `features` to check compatibility before the first RPC. The
owner-only file contains no credentials and remains after shutdown, so check
that `pid` belongs to the child you launched before trusting an existing record.
If startup fails before readiness, stop waiting and report the child exit;
clean up the runtime directory after the process exits.

`--lifetime-pipe-fd` accepts a pipe read end or a connected UNIX stream socket.
Keep the other endpoint open. EOF triggers graceful shutdown and persistence.
The socket, readiness-file, and lifetime-pipe flags are off by default.

`--lifetime-stdin` uses piped stdin for the same parent-liveness signal. A
parent using `Deno.Command` sets `stdin: "piped"` and holds the writer open.
Closing the writer or exiting closes the channel and triggers graceful shutdown.
The daemon discards any bytes received. Regular files and terminals are
rejected, and the flag cannot be combined with `--lifetime-pipe-fd`. The
[Deno SDK integration](/building/typescript-sdk/local-daemon.md#start-a-daemon-from-deno)
manages this channel for you.

## Add MCP servers per session

Clients can add per-session MCP servers only when `mecated` uses a UNIX socket
and HTTP is disabled:

|Listener topology|`mcp_servers`|
|-|-|
|`--grpc-unix-socket` **and** `--http-addr ""`|accepted|
|Anything else, including loopback TCP|refused with `UNIMPLEMENTED` / HTTP 501 and `client_mcp_unsupported`|

Clients can check for `mcp_servers_on_create` through `GetCompatibilityInfo`.
The general HTTP compatibility endpoint is `GET /v1/compatibility`, but HTTP
is disabled in this private topology. Per-session entries support streaming HTTP without
redirects. Server names must use 1-64 characters from `[A-Za-z0-9._-]`, cannot
contain `__`, and must be unique. Put credentials in `headers`; Mecatl redacts
them and rejects URLs containing user information.

If any requested server is unreachable, session creation fails atomically with
`client_mcp_unreachable` and no session is created.

## Next steps

- [Connect with gRPC or HTTP](grpc-http.md) for sessions and stream controls.
- [Inspect server compatibility](typescript-sdk/server-discovery.md) before creating a session.
- [Configure MCP](/features/security-and-execution/mcp-client.md) for shared runtime behavior.
