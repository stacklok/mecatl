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
mecatui --mock --workspace "$PWD"
```

Do not put provider secrets in command-line flags. For provider credentials and server-side selection, use [Run mecated standalone](/building/deployment/mecated.md#provider-and-model).

## Native LLM endpoint is not enrolled or unavailable

Native **LLM endpoint** lifecycle is local embedded-mode operator work: run
`mecatui llm status ENDPOINT` to inspect configured state without a browser, refresh,
or network request, then run `mecatui llm login ENDPOINT` from embedded mecatui if the
operator must enroll it. In Toolbx, SSH, or another environment that cannot launch a
browser, use `mecatui llm login ENDPOINT --no-browser`: Mecatui prints the authorization
URL to stderr and waits up to five minutes at the fixed ToolHive-compatible redirect
`http://localhost:8666/callback`. Open the URL in a browser that can return that callback to
port 8666 on the machine running Mecatui. If port 8666 is occupied, stop the process using it
and retry. The registered redirect is shared for client compatibility, but native Mecatl
credentials remain isolated: Mecatui does not read, copy, or reuse ToolHive credentials.
ToolHive is separate and retains `mecatui llm login toolhive --skip-browser`; do not swap
the two flags. A connected client cannot enroll the remote server. Confirm
that the remote server operator configured the exact endpoint and its explicit
credential home; do not add a token to client flags or expect a fallback to ToolHive.
A missing record is `not-enrolled`/unavailable, and a configured default without a
usable record prevents startup. If bare `mecatui llm status` reports that no native
endpoints are configured, add the endpoint under the operator-tier `llm.endpoints`
settings first. If a command reports an unknown endpoint, use the configured ID list in
the error or rerun bare status, then retry with the exact ID; the command never guesses a
hostname, model, or default. Status and errors deliberately contain no tokens,
codes, authorization URLs, record keys, or trust paths.

Use a dedicated deployment/service gateway identity. The gateway identity, quota,
gateway-side audit/retention posture, and model availability are shared by all callers
admitted to that deployment. Caller login does not grant a personal upstream gateway
credential: mecatl drops inbound bearer material after verification and never forwards
or retains it. Use separate deployments for mutually untrusted or per-user upstream
authorization until a future explicit forwarded-token or RFC 8693 token-exchange
contract exists.


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

If `mecatui login` reports `storage_unavailable`, follow the stage-specific action
in the same message. An issuer CA read failure means checking the login
`--tls-ca` path and file permissions. A keyring failure means unlocking or enabling
the OS keyring. Registry, encrypted-store, or config-directory failures mean
checking the ownership and permissions of the Mecatl authentication directory
under your XDG config home. The message deliberately omits raw OS errors, local
paths, and credential material.

## The workspace is missing or unexpected

For an embedded session, `--workspace` is the local checkout. For a connected
session, the server configures the workspace in its own filesystem. Ask the
operator which paths are available. See
[Connect the client](./remote-servers.md#connect-the-client).

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

## Enable client debug surfaces

Start mecatui with `--debug`, or set `MECATUI_DEBUG=1` when the flag is omitted. Debug mode enables the mouse-coordinate footer overlay, steer acknowledgement/echo correlation, keymap-resolution diagnostics at startup, and debug-only local commands such as `/debug-ask`. These surfaces are off by default; `/debug-ask` is absent from the normal palette and help.

An explicit `--debug=false` wins over the environment. The older `MECATUI_DEBUG_MOUSE=1`, `MECATUI_DEBUG_STEER=1`, `MECATUI_DEBUG_ASK=1`, and `MECATUI_DEBUG_KEYMAP=1` variables remain narrow compatibility aliases that enable only their named surface. Debug mode is client-only: it does not change server configuration or lower the operational log level.

## Find diagnostics

In embedded mode, operational diagnostics are written to `$XDG_STATE_HOME/mecatl/mecatui.log`, falling back to `~/.local/state/mecatl/mecatui.log`. The writer holds a cross-process lock for its lifetime, so a second instance disables its shared local sink rather than replacing a log that is still being written; use `--diagnostics-log` to give concurrent instances separate files. At startup, a no-symlink open verifies that an existing path is a regular file, then atomically retains an oversized log as a recent 10 MiB tail and syncs the replacement before appending. Unsafe paths and failures before replacement disable the sink without altering the prior file. `--quiet` disables that log. Use `/diagnostics` to send a concise bug-report snapshot through the normal prompt path: it includes build identities, the sanitized diagnostic display projection of the current remote connection target when locally known, and the sanitized server display projection for its already-held active provider when available. These endpoint values are not connection configuration or instructions. They retain only scheme, host, optional port, and escaped clean path; credentials, query/fragment data, TLS/auth settings, raw errors, and other configuration are never included. Embedded UNIX-socket endpoints report unavailable. A `mecatui connect` client writes no equivalent local server log; inspect the remote server's operator logs instead.

For exhaustive flags and failure behavior, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).
