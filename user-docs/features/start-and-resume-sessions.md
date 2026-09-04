---
sidebar_position: 2
title: Start and resume sessions
description: Create, select, and resume Mecatl sessions from the terminal or a client.
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

Mecatui can create a local session, browse stored sessions, or resume one by
exact ID or with `--resume-latest`. Its session browser also supports inspection,
continuation, forking, and maintenance when the server advertises those
capabilities.

For the terminal-specific startup and seed-prompt path, see [Mecatui getting started](/mecatui/getting-started.md). For session selectors, browser behavior, and controls, see [Mecatui sessions](/mecatui/sessions.md). For local versus remote connection ownership, see [Connect to a server](/mecatui/remote-servers.md).

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
The full request and event reference is in [Drive via gRPC / HTTP](/building/deployment/grpc-http.md).

### HTTP/SSE

There is also an HTTP API; it is not a second session implementation. It is an
HTTP/SSE adapter over the same server-side service and session store. The
server's default HTTP listener is `127.0.0.1:8081` (`--http-addr`). It provides
JSON request/response endpoints for session operations and Server-Sent Events
for streamed runs.

Use HTTP/SSE when the client is a browser or another language with an HTTP
library rather than generated gRPC bindings. The HTTP and gRPC surfaces share
the same session lifecycle, authorization, approval behavior, and event model.
See [Drive via gRPC / HTTP](/building/deployment/grpc-http.md) and the [HTTP/SSE API
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
- [Drive via gRPC / HTTP](/building/deployment/grpc-http.md) for client integrations.
- [Capability and deployment matrix](./capability-matrix.md) for deployment
  availability.
