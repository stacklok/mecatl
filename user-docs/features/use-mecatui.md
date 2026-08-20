---
sidebar_position: 5
title: Use mecatui
description: Use the mecatl terminal UI to work with sessions, models, tools, and approvals.
---

# Use mecatui

`mecatui` is mecatl's interactive terminal client. It can host an embedded
`mecated` server in the same process, or connect to a running external server.
Use it to work with sessions, switch models, approve actions, inspect tool
calls, and monitor delegated work.

:::info[Availability]

`mecatui` runs as a local terminal application on macOS, Linux, and Windows.
Bare `mecatui` hosts an embedded server over a private local socket. The
`connect` command dials an existing `mecated` or `mecak8s` endpoint. The server
interprets the requested workspace path, checks its own available tools and
capabilities, and applies its own trust, authentication, and authorization
rules. The TUI host's local files and capabilities are not automatically
available to the connected server.

:::

## Start mecatui

The bare invocation starts an embedded server. By default, a new session uses
the current directory as its workspace, resolved to an absolute path. The
provider is selected from configured credentials:

```sh
mecatui
```

For an offline smoke test, explicitly select the mock provider:

```sh
mecatui --mock
```

The mock provider is intended for offline testing and does not make network
requests. It is not a production model backend.

The embedded server does not probe for an already-running `mecated`. To connect
to an existing server, provide the address explicitly:

```sh
mecatui connect 127.0.0.1:8080 --workspace "$PWD"
```

If `--workspace` is omitted, mecatui sends its current directory as the
workspace for a new session and resolves it to an absolute path. In `connect`
mode, the remote server interprets that path on its own host. Continuing an
existing session keeps the workspace stored with that session.

`connect` accepts a `host:port`, not a URL. Plaintext is the default; mecatui
does not infer TLS from the address or automatically upgrade a connection. Use
`--tls` for TLS, `--tls-ca FILE` for a private certificate authority, and
`--insecure` only for testing when you need to skip certificate verification.
A bearer token requires TLS for non-loopback targets. Loopback is the deliberate
plaintext-bearer exception.

```sh
mecatui connect mecated.example.com:443 \
  --tls \
  --auth-token "$MECATL_AUTH_TOKEN"
```

Use `mecatui sessions` to open the stored-session browser before creating or
continuing a chat. The same browser is available remotely:

```sh
mecatui sessions
mecatui connect 127.0.0.1:8080 sessions
```

Press `n` to create a new chat, `enter` to continue or inspect the selected
row, and `esc` to leave the startup browser without creating a session.

## Start with a prompt or continue a chat

Use `--prompt` or `--prompt-file` to submit one seed prompt after the first
session becomes ready. The TUI remains interactive for follow-up messages:

```sh
mecatui --workspace "$PWD" \
  --prompt "Summarize the failing tests in this repository"

mecatui --workspace "$PWD" --prompt-file task.md
```

If you provide both flags, the literal `--prompt` text comes first and the
file contents follow it. The seed is submitted once, even if you later switch
models or clear the conversation.

Resume an exact owned main session with `--resume`, or let mecatui choose the
newest eligible one with `--resume-latest`:

```sh
mecatui --resume SESSION_ID
mecatui connect 127.0.0.1:8080 --resume-latest
```

The two flags are mutually exclusive. An adopted session keeps its stored
workspace, mode, model, and capabilities. `--resume-latest` skips active,
awaiting, scheduled, child, unknown, and incomplete sessions. It does not create
a throwaway session.

Inside the TUI, use `/session` and press `c` to copy the exact active session
ID. On a normal exit, mecatui also writes the final active ID to stderr:

```text
mecatui: final-session-id="SESSION_ID"
```

## Work in the interface

The prompt accepts ordinary messages and slash commands. Common commands
include:

| Command | Action |
| --- | --- |
| `/models` | Choose a model and switch the conversation to it. |
| `/effort` | Choose the reasoning-effort tier for a new conversation fork. |
| `/sessions` | Search sessions, inspect transcripts, continue chats, or manage storage when supported. |
| `/session` | Show metadata for the active session. |
| `/clear` | Start a fresh conversation. |
| `/help` | Open the current command and keybinding help. |

Model switches preserve the conversation. Switching providers can discard
provider-private reasoning state, while the visible conversation remains.
The available models and capabilities come from the connected server.

When a run is streaming, press `enter` to steer it with a new message. The
message is added at the next turn boundary after in-flight tool calls settle.
If the server has steering disabled, the message is queued for the next turn
instead. `esc` is context-sensitive: it clears a draft, retracts pending input,
cancels an active run, denies a permission prompt, or closes the active
overlay. In the startup session browser it exits without creating a session.

Useful controls include:

- `ctrl+t` expands a focused tool or delegation card.
- `ctrl+a` opens the agents overlay for Subagent and Parallel activity.
- `ctrl+c` on an empty prompt, or `ctrl+d`, requires a second press to quit.
- `?` opens help with the active key bindings.

When the agent needs permission, the approval modal lets you allow the action
once, always allow it for the session, or deny it. Plan reviews use the same
modal interaction. Tool arguments can be expanded and scrolled before you
approve them.

## Configure the terminal experience

These flags control the client itself:

| Flag | Purpose |
| --- | --- |
| `--theme NAME` | Select a theme. |
| `--theme-dir DIR` | Load additional JSON themes. |
| `--list-themes` | List available themes and exit. |
| `--inline` | Render in the normal terminal buffer instead of the alternate screen. |
| `--no-mouse` | Leave mouse selection to the terminal instead of capturing it. |
| `--no-banner` | Hide the welcome splash. |
| `--terminal-title off` | Disable the dynamic terminal title. |
| `--keymap ACTION=CHORD` | Rebind an action; repeat the flag for multiple actions. |

Use `--help` for the common flags and `--help-all` for the exhaustive
reference. The full TUI reference covers overlays, keymaps, themes, transport,
and startup behavior in more detail:

[Read the full mecatui reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md)

## Configure the embedded server

These options apply when mecatui hosts the embedded server. They do not apply
to `mecatui connect`:

| Flag | Purpose |
| --- | --- |
| `--model MODEL` | Select the embedded server's initial model. |
| `--default-provider ID` | Set the deployment-wide default provider. |
| `--default-model MODEL` | Set the deployment-wide default model. |
| `--reasoning-effort LEVEL` | Set the default reasoning-effort tier. |
| `--no-bash` | Disable the Bash tool. |
| `--no-memory` | Disable cross-session memory. |
| `--no-store` | Use an in-memory session store instead of persisting sessions. |
| `--trust-project` | Admit trusted project instructions and project grants. |
| `--posture LEVEL` | Set the operator posture, such as `strict`, `trusted`, `auto`, or `yolo`. |

The embedded server stores sessions under a workspace-specific state directory
by default. The durable store contains the raw conversation, model output, tool
arguments, and tool results in plaintext. The directory is created for the
owner only, but protect it as sensitive data.

## Limitations

- `mecatui connect` does not use embedded-server provider flags or local
  credentials. Configure those on the external server.
- In connect mode, `--workspace` identifies a workspace on the server host; it
  does not grant the server access to a local checkout on the TUI host.
- `--resume` and `--resume-latest` continue eligible owned main sessions.
  Active, awaiting, child, scheduled, unknown, and incomplete sessions require
  inspection or a different workflow.
- Image and other multimodal input depends on the connected provider's
  advertised capabilities.
- `--insecure` disables TLS certificate verification and is intended for
  testing only.

## Next steps

- [Start and resume sessions](./start-and-resume-sessions.md) for session
  lifecycle details.
- [Choose models and providers](./choose-models.md) for provider and model
  selection.
- [Define named agents](./named-agents.md) for specialist agent definitions.
- [Session continuity](./session-continuity.md) for persistence and storage
  maintenance.

