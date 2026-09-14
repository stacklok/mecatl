---
sidebar_position: 20
title: Use mecatui
description:
  Use the Mecatl terminal UI to work with sessions, models, tools, and
  approvals.
---

# Use mecatui

`mecatui` is Mecatl's interactive terminal client. Run bare `mecatui` to host an
embedded server, or run `mecatui connect ADDRESS` to control a server that is
already running.

Use it to manage sessions, switch models, approve actions, inspect tool calls,
and monitor delegated work.

## Availability

`mecatui` can host an embedded server or connect to `mecated` and `mecak8s` over
gRPC. The connected server determines which features are available.

## Choose how to connect

- **Local work:** run `mecatui` to start an embedded server over a private local
  socket. The embedded server uses local credentials, workspace, storage, and
  policy configuration.
- **Client/server deployment:** run `mecatui connect ADDRESS` when an operator
  has already started `mecated` or `mecak8s`. The remote server owns the
  workspace, credentials, storage, capabilities, and policy; local
  embedded-server settings do not apply.

A loopback connect can select an absolute path interpreted on the server host.
For a non-loopback target, `mecatui` sends no cwd and rejects `--workspace`; the
server's listener authority chooses its configured root or no-FS profile.
Neither mode uploads or shares a checkout from the computer running the TUI.

See [Connect to a server](../mecatui/remote-servers.md) for TLS, authentication,
and remote workspace rules.

## Common workflow

```sh
# Start an embedded session in the current checkout.
mecatui --workspace "$PWD"

# Or seed its first prompt while keeping the session interactive.
mecatui --workspace "$PWD" \
  --prompt "Summarize the failing tests in this repository"
```

The TUI can continue stored sessions, switch models, approve requests, steer a
run, and launch a debugger when the server supports it:

```sh
mecatui debug TARGET
mecatui connect ADDRESS debug TARGET
```

`TARGET` accepts the full opaque session ID printed on exit or the displayed
12-column short handle. When a short handle is ambiguous, open `/session`, copy
the full ID, and pass it to the same command. If the session inventory is
unavailable or no short handle matches, `mecatui` treats `TARGET` as a full ID
and reports the server's authorization or not-found result.

Debugging creates a separate no-filesystem analysis session and sends bounded
stored-session evidence to the selected model. That evidence may include
secrets. The debugger does not resume or modify the target session. See
[Sessions](../mecatui/sessions.md#diagnose-a-stored-session).

Use the dedicated guides for those workflows:

- [Getting started](../mecatui/getting-started.md) - launch a local session and
  submit a first prompt.
- [Sessions](../mecatui/sessions.md) - browse, inspect, continue, fork, and
  maintain chats.
- [Using the TUI](../mecatui/using-the-tui.md) - streaming, steering, approvals,
  and model switching.
- [Commands and memory](../mecatui/commands-and-memory.md) - learning,
  reflections, and memory-maintenance commands.

## Keyboard help

Press `?` on an empty prompt to open the keys-and-features overlay. When the
overlay is taller than the conversation area, use **Up/Down** to move one line,
**Page Up/Page Down** to move a page, and **Home/End** to jump to the beginning
or end. The overlay shows its current line range; press `?` or **Esc** to close
it. The displayed key labels and capability availability reflect the active
client keymap and connected server.

## Configuration ownership

Client settings such as themes, keymaps, terminal rendering, and mouse behavior
belong to `mecatui`. Provider selection, posture, workspace trust, tools,
storage, and other agent behavior belong to the embedded or connected server.

For embedded-server flags, see
[Run mecated standalone](../building/deployment/mecated.md). For model
selection, see [Choose models and providers](./choose-models.md). For
permissions and trust, see
[Permissions and posture](./permissions-and-posture.md).

## Limitations

- `connect` never discovers or starts a server and does not fall back to
  embedded mode.
- A connected server may expose different tools, models, media capabilities, and
  storage features than an embedded server.
- Remote clients cannot use the TUI host's local files unless those files are
  available in the server's workspace namespace.
- TLS, authentication, and server-side policy are configured at the server
  boundary; `mecatui` cannot override them locally.

## Next steps

- [Mecatui guide](../mecatui/index.md)
- [Start and resume sessions](./start-and-resume-sessions.md)
- [Choose models and providers](./choose-models.md)
