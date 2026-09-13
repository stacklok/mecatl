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

## Provider is not configured or credentials are unavailable

For an embedded local server, run `mecatui providers` to inspect provider state
without revealing credentials. Run `mecatui providers setup` for guided setup, or
use `mecatui providers add PROVIDER` to define a custom provider and
`mecatui providers login PROVIDER` to add locally managed credentials. For an
OIDC provider on a host that cannot open a browser, use
`mecatui providers login PROVIDER --no-browser` and complete the displayed flow.

If the command reports an unknown provider, run `mecatui providers` and use the
exact configured name. If settings reject `llm:` as legacy configuration, remove
that mapping and recreate each provider with `mecatui providers add`, or migrate
it to equivalent operator `providers` and `credential_store` entries. Re-enroll
OIDC providers with `mecatui providers login PROVIDER`; migration does not silently
reuse a legacy provider or select a fallback. Check `mecatui providers` before
restarting the server.

`credential_store.oidc` is shared OIDC credential custody. With an environment
key, confirm that `credential_store.oidc.key.key_env` names a value provisioned to
both the login process and the server; restoring the original value is required to
read existing encrypted credentials. If it cannot be restored, use a new credential
home and re-enroll providers rather than overwriting an unreadable record. The
[provider configuration guide](/building/deployment/mecated.md#configure-providers)
and [configuration reference](/reference/configuration.md#credential_store) describe
the supported schema.

API-key credentials can come from the environment or a provider-credentials YAML
file selected by `--api-key-file`. Do not put provider secrets in command-line
arguments, settings YAML, prompts, or logs.

A connected client cannot enroll a remote server's providers. `mecatui login
ADDRESS` authenticates the client to that remote server; ask its operator to
configure the server's providers. ToolHive is separate and owns its LLM credential
lifecycle, so use `thv llm` tooling for ToolHive setup.

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
