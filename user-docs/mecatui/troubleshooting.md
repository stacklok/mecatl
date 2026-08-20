---
sidebar_position: 10
title: Troubleshoot mecatui
---

# Troubleshoot mecatui

Start by identifying whether you are running embedded `mecatui` or `mecatui connect ADDRESS`. The first owns a local server; the second only displays and controls the server it reaches.

## Embedded startup says no provider is available

Embedded mode detects provider credentials from its environment. Set one supported provider credential, or use the explicit offline path while learning the UI:

```sh
bin/mecatui --mock --workspace "$PWD"
```

Do not put provider secrets in command-line flags. For provider credentials and server-side selection, use [Run mecated standalone](/building/deployment/mecated.md#provider-and-model).

## Cannot connect, authenticate, or verify TLS

These are distinct failures:

- **Connection failure:** confirm the address, network path, and that the operator started the server.
- **Authentication failure:** obtain the right bearer token or identity credential from the operator; changing a local client setting cannot change server auth.
- **TLS verification failure:** use `--tls`; when the server uses a private CA, obtain its CA bundle and pass `--tls-ca`. Do not bypass verification except in controlled testing.

A bearer is allowed over plaintext loopback, but mecatui refuses to send it to a non-loopback server without TLS. See [Connect to a server](./remote-servers.md) and the operator [TLS and authentication guide](/building/deployment/grpc-http.md#tls-and-mtls).

## The workspace is missing or unexpected

For an embedded session, `--workspace` is the local checkout. For a connected server, it is an absolute path in the server's filesystem. Ask the operator which paths are mounted or permitted; do not assume your local path exists in a container, pod, or remote host. See [Connect to a server](./remote-servers.md#choose-the-connection-shape).

## A provider error says retrying will not help

A permanent provider rejection or context-window overflow can be recovered technically, but retrying the same request is unlikely to succeed. Start a new session or change the request/model as the message directs. For transient connection or service failures, retrying can be appropriate. The session lifecycle and recovery behavior are documented in [agent-loop recovery behavior](/building/what-you-get/agent-loop.md#restarting-a-session).

## A session will not resume

Use `/sessions` or `mecatui sessions` to inspect what the server has stored. An exact resume reports why a chat is not eligible; `--resume-latest` skips ineligible or unreadable entries. Verify that you reached the same server and that its storage still has the session, then ask the operator about storage, retention, or leases. Do not create a replacement session if you need the original transcript. See [Sessions](./sessions.md) and [session storage operations](/building/deployment/session-storage-operations.md).

## Find diagnostics

In embedded mode, operational diagnostics are written to `$XDG_STATE_HOME/mecatl/mecatui.log`, falling back to `~/.local/state/mecatl/mecatui.log`. `--quiet` disables that log. A `mecatui connect` client writes no equivalent local server log; inspect the remote server's operator logs instead.

For exhaustive flags and failure behavior, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).
