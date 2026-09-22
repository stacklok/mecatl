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

Expand an error card with your configured `ExpandTools` keybinding to see its
complete sanitized message.

## Embedded startup says no provider is available

Embedded mode detects provider credentials from its environment. Set one
supported provider credential, or use the explicit offline path while learning
the UI:

```sh
mecatui --mock --workspace "$PWD"
```

Do not put provider secrets in command-line flags. For provider credentials and
server-side selection, use
[Run mecated standalone](/operating/mecated.md#provider-and-model).

## Provider is not configured or credentials are unavailable

### Inspect local provider state

For an embedded local server, run `mecatui providers` to inspect provider state
without revealing credentials. Run `mecatui providers setup` for guided setup,
or use `mecatui providers add PROVIDER` to define a custom provider and
`mecatui providers login PROVIDER` to add locally managed credentials. For an
OIDC provider on a host that cannot open a browser, use
`mecatui providers login PROVIDER --no-browser` and complete the displayed flow.

If the command reports an unknown provider, run `mecatui providers` and use the
exact configured ID. Custom provider IDs contain 1 to 63 lowercase letters,
numbers, or hyphens. They start with a letter, end with a letter or number, and
cannot use a built-in ID. For example, use `local-gateway` instead of `Local`,
`local_gateway`, or `local-`. Run `mecatui providers add local-gateway` to
define it.

After configuring a custom provider, select it for the embedded server:

```sh
mecatui providers set-default <PROVIDER_ID> <MODEL_ID>
```

Use the exact model ID accepted by the gateway. The provider ID and model ID are
different values.

### Distinguish provider and server login

`mecatui providers setup` configures model access. The built-in Anthropic,
OpenAI, OpenRouter, and OpenCode providers require API keys. ChatGPT Plus or Pro
subscriptions cannot sign in to the built-in OpenAI provider.

Custom-provider OIDC works only with an operator-configured gateway that exposes
the required OIDC details. `mecatui login ADDRESS` instead authenticates the
client to a remote Mecatl server; it does not configure that server's model
provider.

### A custom model is listed but requests fail

Listing confirms only that the gateway advertised the model. Confirm that:

- the provider uses the API flavor implemented by the gateway;
- the selected model ID exactly matches the gateway's identifier;
- the credential grants access to that model; and
- the model and gateway support the request features they receive.

Run `mecatui providers status <PROVIDER_ID>` to check local configuration, then
inspect the gateway logs for its rejection. If the provider cannot report a
context window, configure the exact value under
`models.context_windows.<provider-id>.<model-id>` or restore live model
discovery. See
[Choose models and providers](/features/sessions/choose-models.md#set-up-a-local-provider).

Chat can fail with `context window unavailable` even when the same credential
works for chat completions: a custom provider's context window is learned by
fetching its live model list, and some OpenAI-compatible gateways authorize or
implement that listing endpoint differently from the completion endpoint.
Check the server's startup log for `live model fetch failed` and its reported
state, and confirm the credential against the listing endpoint directly, for
example `curl -H "Authorization: Bearer <key>" <base_url>/models`.

### Recover OIDC credentials

`credential_store.oidc` is shared OIDC credential custody. With an environment
key, `credential_store.oidc.key.key_env` must name a value available to both the
login process and the server. You need the original value to read existing
encrypted credentials. If you cannot restore it, use a new credential home and
enroll the providers again. Do not overwrite the unreadable record.

For other OIDC failures, check the provider configuration, credential-store home
and key, issuer trust, and network and TLS settings:

- A callback conflict uses localhost port `8666`.
- An authorization failure requires a new browser flow.
- A rejected token requires checking its audience and scopes.
- During logout, an unavailable enrollment requires checking the provider
  configuration and `mecatui providers status PROVIDER`.

The
[provider configuration guide](/operating/mecated.md#configure-providers)
and [credential store reference](/reference/configuration.md#credential_store)
describe the supported schema.

### Protect API keys

API-key credentials can come from the environment or a provider-credentials YAML
file selected by `--api-key-file`. Do not put provider secrets in command-line
arguments, settings YAML, prompts, or logs.

A connected client cannot enroll a remote server's providers.
`mecatui login ADDRESS` authenticates the client to that server; ask its
operator to configure provider credentials. ToolHive manages its own LLM
credentials through the `thv llm` commands.

## Server connection or login fails

Identify the failure before changing the client configuration:

- **Connection failure:** confirm the address, network path, and that the
  operator started the server.
- **Authentication failure:** obtain the right bearer token or identity
  credential from the operator; changing a local client setting cannot change
  server auth.
- **TLS verification failure:** remote targets use verified TLS automatically;
  use `--tls-ca` when the server uses a private CA. `--tls=false` is an explicit
  plaintext downgrade for controlled testing, not a verification fix. Do not use
  `--insecure` except in controlled testing.

A bearer token is allowed over plaintext loopback, but `mecatui` refuses it over
explicit non-loopback plaintext. Saved OIDC authentication always uses verified
TLS, even for loopback. See [Connect to a server](./remote-servers.md) and the
operator
[server flag reference](/operating/mecated.md#flag-reference).

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

A permanent provider rejection or context-window overflow will not succeed when
you retry the same request unchanged. Start a new session, or change the request
or model as directed. Retry transient connection and service failures. For
recovery details, see
[Session states and continuation](/features/sessions/start-and-resume-sessions.md#session-states-and-continuation).

## A session will not resume

Use `/sessions` or `mecatui sessions` to inspect what the server has stored. An
exact resume reports why a chat is not eligible; `--resume-latest` skips
ineligible or unreadable entries. Verify that you reached the same server and
that its storage still has the session, then ask the operator about storage,
retention, or leases. Do not create a replacement session if you need the
original transcript. See [Sessions](./sessions.md) and
[session storage operations](/operating/session-storage-operations.md).

## A debug command cannot open its target

`mecatui debug TARGET` and `mecatui connect ADDRESS debug TARGET` require the
same store and caller authorization as the target. If a short handle is
ambiguous, open `/session`, copy the full ID, and use it as `TARGET`. Missing
and unauthorized targets are both reported as not found. Confirm the server,
identity, and session ID. A stored debug session also fails if its target or
debug support is unavailable after a restart.

Debug views depend on retained event-log evidence and report when evidence is
unavailable or incomplete. Use the transcript for conclusions about the
conversation. Network evidence contains sanitized failure categories and retry
decisions instead of raw errors, URLs, headers, bodies, prompts, tool arguments,
or credentials.

## Enable client debug surfaces

Start `mecatui` with `--debug`, or set `MECATUI_DEBUG=1` when the flag is
omitted. Debug mode enables the mouse-coordinate footer, steer correlation,
keymap-resolution diagnostics at startup, and debug-only local commands such as
`/debug-ask`. These surfaces are off by default.

An explicit `--debug=false` overrides the environment. Debug mode is client-only
and does not change server configuration or the operational log level.

## Find diagnostics

In embedded mode, `mecatui` writes operational diagnostics to
`$XDG_STATE_HOME/mecatl/mecatui.log`, falling back to
`~/.local/state/mecatl/mecatui.log`. One process holds that log at a time. A
second instance writes to a per-process sibling beside it, named with its
process ID as in `mecatui.4821.log`, and prints that path to stderr at startup
so no instance loses its diagnostics. Use `--diagnostics-log` to name each
instance's file yourself, or `--quiet` to disable the log. At startup, `mecatui`
reduces an oversized log to its most recent 10 MiB. An unsafe path disables
logging without changing the existing file.

Use `/diagnostics` to send a concise, sanitized bug-report snapshot through the
normal prompt path. It includes build identities and available display
information for the connection target and active provider. It excludes
credentials, TLS and authentication settings, raw errors, and other
configuration. A `mecatui connect` client does not write an equivalent local
server log; inspect the remote server's operator logs instead.

For exhaustive flags and failure behavior, see
[`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).

## Related information

- [Connect to a server](./remote-servers.md) for authentication and TLS options.
- [Manage sessions](./sessions.md) for resume and debug workflows.
