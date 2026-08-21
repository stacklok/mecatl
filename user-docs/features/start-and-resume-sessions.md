---
sidebar_position: 2
title: Start and resume sessions
description: Create, select, and resume mecatl sessions from the terminal or a client.
---

# Start and resume sessions

A session is the server-side conversation and execution context that receives
prompts. It has an opaque ID and retains its workspace, model selection,
permission mode, and transcript according to the server's storage
configuration.

This page separates the two ways to use sessions:

- the **mecatui flow**, where the terminal client creates or selects a session;
- the **API flow**, where another client calls the server directly.

For the rest of the interactive terminal workflow, see [Use mecatui](./use-mecatui.md).

## Availability

Session creation and continuation are available in:

- `mecatui`, with its embedded server or an explicit `mecated`/`mecak8s`
  connection;
- the gRPC `HarnessService`; and
- the HTTP/SSE API exposed by `mecated` and `mecak8s`.

`mecatequi` is a one-shot composition. It creates a fresh session for its
prompt but does not provide an interactive session browser or resume flow.

A durable server-side session store is required for continuity after a server
restart. An in-memory store only lasts for the lifetime of that process.

## Mecatui flow

### Start a new session

Run mecatui in the workspace you want to use:

```sh
mecatui
```

When `--workspace` is omitted, mecatui uses its current directory for a new
session and resolves it to an absolute path. To use a running server instead,
provide its address explicitly:

```sh
mecatui connect 127.0.0.1:8080
```

In connect mode, the workspace is a path on the server host. It does not upload
or share a checkout from the TUI host. See [Use mecatui](./use-mecatui.md) for
transport, prompt, model, and workspace examples.

### Select a stored session

Open the session browser before creating or continuing a chat:

```sh
mecatui sessions
mecatui connect 127.0.0.1:8080 sessions
```

Select a row and press `enter`. Mecatui uses the capabilities advertised by the
server to distinguish a chat that can be continued from a session that can only
be inspected.

### Resume by exact ID

Use `--resume SESSION_ID` when you know the server-issued ID:

```sh
mecatui --resume SESSION_ID
mecatui connect 127.0.0.1:8080 --resume SESSION_ID
```

Mecatui loads the authoritative stored transcript and adopts that session. It
does not create a replacement session. If the session cannot be continued,
for example because its transcript is incomplete, it reports the reason rather
than silently creating a new chat.

### Resume the newest eligible session

Use `--resume-latest` to select the newest eligible owned main chat:

```sh
mecatui --resume-latest
mecatui connect 127.0.0.1:8080 --resume-latest
```

Mecatui considers stored rows in modification-time order and chooses the first
one with a complete authoritative transcript. It skips active, awaiting,
child, scheduled, unknown, and inspect-only sessions. If a candidate's
transcript cannot be loaded, it tries the next candidate. `--resume` and
`--resume-latest` are mutually exclusive.

The server still performs ownership, authorization, active-run, and lease
checks when the resumed session receives its first new prompt. An exact ID is
not a capability by itself.

### Preserve the ID

Inside mecatui, run `/session` and press `c` to copy the active session ID. On a
normal exit, mecatui also prints the final active ID to stderr:

```text
mecatui: final-session-id="SESSION_ID"
```

Save the ID when you need deterministic continuation. The line is not emitted
when no session was established or startup was interrupted before normal
shutdown.

## API flow

An API client normally:

1. creates a session and receives an opaque `session_id`;
2. retains that ID; and
3. sends prompts to that session until it is closed or no longer usable.

The session keeps its server-side workspace and configuration between prompts.
A client must not infer ownership, session kind, or permissions from the ID's
spelling.

### gRPC

The gRPC `HarnessService` provides the primary streaming interface:

- `CreateSession` allocates a session;
- `GetSession` reads its current snapshot;
- `Converse` streams prompts and events for a run; and
- `ListSessions` returns stored-session inventory for clients that provide their
  own picker.

Use gRPC when you want generated protocol bindings and bidirectional streaming.
The full request and event reference is in [Drive via gRPC / HTTP](../deployment/grpc-http.md).

### HTTP/SSE

There is also an HTTP API; it is not a second session implementation. It is an
HTTP/SSE adapter over the same server-side service and session store. The
server's default HTTP listener is `127.0.0.1:8081` (`--http-addr`). It provides
JSON request/response endpoints for session operations and Server-Sent Events
for streamed runs.

Use HTTP/SSE when the client is a browser or another language with an HTTP
library rather than generated gRPC bindings. The HTTP and gRPC surfaces share
the same session lifecycle, authorization, approval behavior, and event model.
See [Drive via gRPC / HTTP](../deployment/grpc-http.md) and the [HTTP/SSE API
reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/http-sse-api.md)
for endpoint details.

## Session states and continuation

A session may be idle, running, awaiting an action, completed, cancelled, or
failed. Clients normally only start a new prompt against an idle or eligible
terminal main session.

Completed, cancelled, and failed sessions can be used for a follow-up prompt.
At run entry, the server reopens or recovers the terminal state before
recording the new user message. A failed session is eligible for another
attempt, although recovery does not fix the original provider or tool failure.

An awaiting session is paused at an action that still needs resolution. It is
not treated as an idle session by the startup selectors, so `--resume-latest`
and the session browser do not adopt it as a new chat. Use the approval flow of
the client or API that owns the pending run instead.

A session persisted as `running` after a process crash is handled separately.
The server only repairs a crash-orphaned run after it has acquired the actual
run-entry lock and any configured session lease; it does not reset a genuinely
live run based only on a persisted snapshot.

## Limitations

- Continuity requires a durable server-side store.
- A workspace is a path on the server host, not a file transfer mechanism.
- Resume is subject to server-side ownership and authorization checks.
- Active, awaiting, child, scheduled, unknown, and incomplete sessions are not
  interchangeable with an idle main chat.
- `mecatequi` creates a new one-shot session and does not provide interactive
  resume selection.

## Next steps

- [Session continuity](./session-continuity.md) for storage, recovery, and
  maintenance.
- [Use mecatui](./use-mecatui.md) for the interactive terminal workflow.
- [Drive via gRPC / HTTP](../deployment/grpc-http.md) for client integrations.
- [Capability and deployment matrix](./capability-matrix.md) for deployment
  availability.
