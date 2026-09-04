---
sidebar_position: 10
title: Troubleshoot mecatui
description: Diagnose mecatui startup, connection, authentication, TLS, and session problems.
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
- **TLS verification failure:** remote targets use verified TLS automatically; use
  `--tls-ca` when the server uses a private CA. `--tls=false` is an explicit
  plaintext downgrade for controlled testing, not a verification fix. Do not use
  `--insecure` except in controlled testing.

A bearer is allowed over plaintext loopback, but mecatui refuses it over explicit
non-loopback plaintext. Saved OIDC authentication always uses verified TLS, even
for loopback. See [Connect to a server](./remote-servers.md) and the operator [server flag reference](/building/deployment/mecated.md#flag-reference).

## The workspace is missing or unexpected

For an embedded session, `--workspace` is the local checkout. For a connected server, it is an absolute path in the server's filesystem. Ask the operator which paths are mounted or permitted; do not assume your local path exists in a container, pod, or remote host. See [Connect to a server](./remote-servers.md#choose-the-connection-shape).

## A provider error says retrying will not help

A permanent provider rejection or context-window overflow can be recovered technically, but retrying the same request is unlikely to succeed. Structured HTTP/API rejections display their structured type or code and message plus, when safely available, the actual request target's scheme, host, optional port, clean escaped path, and one bounded opaque provider request ID. They never display userinfo, query/fragment data, raw response bodies, headers, arbitrary URLs, or invalid IDs; in-band streaming provider errors do not invent HTTP details. Expand the permanent-error card with your configured `ExpandTools` keybinding to see its full safe terminal error. Start a new session or change the request/model as the message directs. For transient connection or service failures, retrying can be appropriate. The session lifecycle and recovery behavior are documented in [agent-loop recovery behavior](/building/what-you-get/agent-loop.md#restarting-a-session).

## A session will not resume

Use `/sessions` or `mecatui sessions` to inspect what the server has stored. An exact resume reports why a chat is not eligible; `--resume-latest` skips ineligible or unreadable entries. Verify that you reached the same server and that its storage still has the session, then ask the operator about storage, retention, or leases. Do not create a replacement session if you need the original transcript. See [Sessions](./sessions.md) and [session storage operations](/building/deployment/session-storage-operations.md).

## A debug command cannot open its target

`mecatui debug TARGET` and `mecatui connect ADDRESS debug TARGET` require the same store and
caller authorization as the target. A syntactically valid short target consults the complete
caller-visible inventory: exact full-ID equality wins, otherwise one unique projected handle
resolves. If projections are ambiguous, open `/session`, copy the full exact ID, and pass it as
`TARGET` through the same command. If inventory fails or no handle matches, mecatui sends `TARGET`
unchanged and reports the server's ordinary exact-ID result. Missing and unauthorized targets are
both reported as not found so ownership is not disclosed. Confirm the exact server,
identity, and session ID. A persisted debugger also fails closed after restart if its
bound target or dedicated debug-engine support is unavailable; it never falls back to an
ordinary chat.

The debugger's activity, performance, network, delegation, history, and manifest views require
retained EventLog evidence and report when evidence is unavailable or incomplete. `related`
uses opaque handles for retained same-owner children and can report a content-free pruned
tombstone; raw unrelated session IDs are not valid handles. Use the authoritative transcript for conversation
conclusions. Status separates latest-run counters and cumulative snapshot usage from bounded
lifetime EventLog counters. Network evidence covers failed/interesting resilience attempts with sanitized
retry decisions and DNS/connect/TLS/timeout/reset/rate-limit/breaker classes. It deliberately
contains no raw errors, URLs, headers, bodies, prompts, tool arguments, or credentials, and
does not claim successful-attempt or per-phase DNS/TCP/TLS timing. Live target following, raw
audit/tool-record views, packet capture, raw pprof/log exposure, and support bundles are not
provided.

## Find diagnostics

In embedded mode, operational diagnostics are written to `$XDG_STATE_HOME/mecatl/mecatui.log`, falling back to `~/.local/state/mecatl/mecatui.log`. `--quiet` disables that log. Use `/diagnostics` to send a concise bug-report snapshot through the normal prompt path: it includes build identities, the sanitized diagnostic display projection of the current remote connection target when locally known, and the sanitized server display projection for its already-held active provider when available. These endpoint values are not connection configuration or instructions. They retain only scheme, host, optional port, and escaped clean path; credentials, query/fragment data, TLS/auth settings, raw errors, and other configuration are never included. Embedded UNIX-socket endpoints report unavailable. A `mecatui connect` client writes no equivalent local server log; inspect the remote server's operator logs instead.

For exhaustive flags and failure behavior, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).
