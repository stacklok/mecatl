---
sidebar_position: 10
title: Troubleshoot mecatui
description:
  Diagnose mecatui startup, connection, authentication, TLS, and session
  problems.
---

# Troubleshoot mecatui

Start by identifying whether you are running embedded `mecatui` or
`mecatui connect ADDRESS`. The first owns a local server; the second only
displays and controls the server it reaches.

## Embedded startup says no provider is available

Embedded mode detects provider credentials from its environment. Set one
supported provider credential, or use the explicit offline path while learning
the UI:

```sh
mecatui --mock --workspace "$PWD"
```

Do not put provider secrets in command-line flags. For provider credentials and
server-side selection, use
[Run mecated standalone](/building/deployment/mecated.md#provider-and-model).

## Native LLM endpoint is not enrolled or unavailable

Native **LLM endpoint** lifecycle is local embedded-mode operator work: run
`mecatui llm status ENDPOINT` to inspect configured state without a browser,
refresh, or network request, then run `mecatui llm login ENDPOINT` from embedded
mecatui if the operator must enroll it. In Toolbx, SSH, or another environment
that cannot launch a browser, use `mecatui llm login ENDPOINT --no-browser`:
Mecatui prints the authorization URL to stderr and waits up to five minutes at
the fixed ToolHive-compatible redirect `http://localhost:8666/callback`. Open
the URL in a browser that can return that callback to port 8666 on the machine
running Mecatui. If port 8666 is occupied, stop the process using it and retry.
The registered redirect is shared for client compatibility, but native Mecatl
credentials remain isolated: Mecatui does not read, copy, or reuse ToolHive
credentials. ToolHive is separate and retains
`mecatui llm login toolhive --skip-browser`; do not swap the two flags. A
connected client cannot enroll the remote server. Confirm that the remote server
operator configured the exact endpoint and its explicit credential home; do not
add a token to client flags or expect a fallback to ToolHive. A missing record
is `not-enrolled`/unavailable, and a configured default without a usable record
prevents startup. If bare `mecatui llm status` reports that no native endpoints
are configured, add the endpoint under the operator-tier `llm.endpoints`
settings first. If a command reports an unknown endpoint, use the configured ID
list in the error or rerun bare status, then retry with the exact ID; the
command never guesses a hostname, model, or default. Status and errors
deliberately contain no tokens, codes, authorization URLs, record keys, or trust
paths.

Use a dedicated deployment/service gateway identity. The gateway identity,
quota, gateway-side audit/retention posture, and model availability are shared
by all callers admitted to that deployment. Caller login does not grant a
personal upstream gateway credential: mecatl drops inbound bearer material after
verification and never forwards or retains it. Use separate deployments for
mutually untrusted or per-user upstream authorization until a future explicit
forwarded-token or RFC 8693 token-exchange contract exists.

### Native credential storage failures

Check `llm.credential_key.source` in the **server operator's** settings before
trying to repair `storage-unavailable` or `corrupt` state. These statuses are
not proof of a keyring failure:

- **Keyring mode (the default):** if the OS keyring is unavailable or locked,
  enable or unlock it for the account running login and the server. There is no
  automatic fallback to an environment key. Switching the source does not
  migrate existing records.
- **Environment-key mode, missing or malformed variable:** the OS keyring is not
  used. Check that `llm.credential_key.key_env` names the variable actually
  provisioned to both processes; an interactive shell export alone does not
  configure a service environment. Restore the original secret-manager value and
  correct provisioning without printing it. It must be canonical padded standard
  base64 for exactly 32 bytes, without whitespace or line breaks.
  Missing/malformed values fail before OAuth or credential mutation; do not
  generate a replacement key to fix a provisioning mistake.
- **Environment-key mode, lost or replaced key:** a well-formed but different
  value cannot decrypt the existing record and can report `corrupt`. Restore the
  original key if it is available. Login refuses to overwrite unreadable
  records, and logout cannot read them to revoke their tokens. If the original
  key cannot be restored, re-enrollment is required; repeated login attempts or
  switching back to keyring will not recover the record.
- **New environment-key enrollment:** an encrypted namespace that has not been
  initialized reports `storage-unavailable`. After verifying the key and the
  owner-only credential home, run `mecatui llm login ENDPOINT` to initialize it.
  Status and serving never create it. For other storage failures, check
  ownership and permissions of the configured `llm.credential_home`, not the
  connected client's authentication directory.

If re-enrollment with a new key is unavoidable, stop all processes sharing the
credential home and preserve the old encrypted home with its owner-only
permissions; do not delete or overwrite it as a troubleshooting shortcut. Have
the operator revoke the old authorization at the identity provider, create a new
empty owner-owned directory with mode `0700`, and explicitly update
`llm.credential_home` in operator settings to its canonical absolute path.
Provision the new stable key and re-enroll **every required endpoint** using the
[keyring-free enrollment procedure](/building/deployment/mecated.md#keyring-free-encrypted-credentials).
The key source and home are shared across endpoints; this is a fresh enrollment,
not a migration. Verify local status before restarting the server with the same
settings and key.

Never paste keys, access tokens, or refresh tokens into **CLI arguments,
settings YAML, prompts, or logs**. Settings hold only an environment-variable
reference; refresh tokens remain encrypted, never in a plaintext file. Do not
dump the process environment to diagnose this issue. See the
[`llm` configuration reference](/reference/configuration.md#llm) for the
supported key-source fields.

## Server connection or login fails

These are distinct failures:

- **Connection failure:** confirm the address, network path, and that the
  operator started the server.
- **Authentication failure:** obtain the right bearer token or identity
  credential from the operator; changing a local client setting cannot change
  server auth.
- **TLS verification failure:** remote targets use verified TLS automatically;
  use `--tls-ca` when the server uses a private CA. `--tls=false` is an explicit
  plaintext downgrade for controlled testing, not a verification fix. Do not use
  `--insecure` except in controlled testing.

A bearer is allowed over plaintext loopback, but mecatui refuses it over
explicit non-loopback plaintext. Saved OIDC authentication always uses verified
TLS, even for loopback. See [Connect to a server](./remote-servers.md) and the
operator
[server flag reference](/building/deployment/mecated.md#flag-reference).

If `mecatui login` reports `storage_unavailable`, follow the stage-specific
action in the same message. An issuer CA read failure means checking the login
`--tls-ca` path and file permissions. A keyring failure means unlocking or
enabling the OS keyring. Registry, encrypted-store, or config-directory failures
mean checking the ownership and permissions of the Mecatl authentication
directory under your XDG config home.

## The workspace is missing or unexpected

For an embedded session, `--workspace` is the local checkout. For a connected
session, the server configures the workspace in its own filesystem. Ask the
operator which paths are available. See
[Connect the client](./remote-servers.md#connect-the-client).

## A provider error says retrying will not help

A permanent provider rejection or context-window overflow is unlikely to succeed
if you retry the same request. Expand the error card with your configured
`ExpandTools` keybinding to see its full sanitized error. Start a new session,
or change the request or model as directed. Retry transient connection and
service failures. For recovery details, see
[Agent-loop recovery behavior](/building/what-you-get/agent-loop.md#restarting-a-session).

## A session will not resume

Use `/sessions` or `mecatui sessions` to inspect what the server has stored. An
exact resume reports why a chat is not eligible; `--resume-latest` skips
ineligible or unreadable entries. Verify that you reached the same server and
that its storage still has the session, then ask the operator about storage,
retention, or leases. Do not create a replacement session if you need the
original transcript. See [Sessions](./sessions.md) and
[session storage operations](/building/deployment/session-storage-operations.md).

## A debug command cannot open its target

`mecatui debug TARGET` and `mecatui connect ADDRESS debug TARGET` require the
same store and caller authorization as the target. If a short handle is
ambiguous, open `/session`, copy the full ID, and use it as `TARGET`. Missing
and unauthorized targets are both reported as not found. Confirm the server,
identity, and session ID. A stored debug session also fails if its target or
debug support is unavailable after a restart.

The activity, performance, network, delegation, history, and manifest views
depend on retained event-log evidence. They report when evidence is unavailable
or incomplete. Use the transcript for conclusions about the conversation.
Network evidence reports sanitized failure categories and retry decisions
without exposing raw errors, URLs, headers, bodies, prompts, tool arguments, or
credentials.

## Enable client debug surfaces

Start mecatui with `--debug`, or set `MECATUI_DEBUG=1` when the flag is omitted.
Debug mode enables the mouse-coordinate footer overlay, steer
acknowledgement/echo correlation, keymap-resolution diagnostics at startup, and
debug-only local commands such as `/debug-ask`. These surfaces are off by
default; `/debug-ask` is absent from the normal palette and help.

An explicit `--debug=false` wins over the environment. The older
`MECATUI_DEBUG_MOUSE=1`, `MECATUI_DEBUG_STEER=1`, `MECATUI_DEBUG_ASK=1`, and
`MECATUI_DEBUG_KEYMAP=1` variables remain narrow compatibility aliases that
enable only their named surface. Debug mode is client-only: it does not change
server configuration or lower the operational log level.

## Find diagnostics

In embedded mode, operational diagnostics are written to
`$XDG_STATE_HOME/mecatl/mecatui.log`, falling back to
`~/.local/state/mecatl/mecatui.log`. One process holds the default log lock; a
second instance disables that shared sink instead of replacing an active log.
Use `--diagnostics-log` to give concurrent instances separate files, or
`--quiet` to disable the log. At startup, an oversized log is atomically reduced
to its most recent 10 MiB. An unsafe path disables the sink without altering the
existing file.

Use `/diagnostics` to send a concise, sanitized bug-report snapshot through the
normal prompt path. It includes build identities and available display
information for the connection target and active provider. It excludes
credentials, TLS and authentication settings, raw errors, and other
configuration. A `mecatui connect` client does not write an equivalent local
server log; inspect the remote server's operator logs instead.

For exhaustive flags and failure behavior, see
[`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).
