---
sidebar_position: 5
title: Use mecatui
description: Use the mecatl terminal UI to work with sessions, models, tools, and approvals.
---

# Use mecatui

`mecatui` is mecatl's interactive terminal client. It is a skin over the shared
agent/server core: bare `mecatui` hosts an embedded `mecated` server in the same
process, while `mecatui connect ADDRESS` displays and controls a server that is
already running.

Use it to work with sessions, switch models, approve actions, inspect tool calls,
and monitor delegated work. The detailed mecatui section owns the client
workflow and controls; this page is the feature-level entry point.

## Choose how to connect

- **Local work:** run `mecatui` to start an embedded server over a private local
  socket. The embedded server uses local credentials, workspace, storage, and
  policy configuration.
- **Client/server deployment:** run `mecatui connect ADDRESS` when an operator
  has already started `mecated` or `mecak8s`. The remote server owns the
  workspace, credentials, storage, capabilities, and policy; local embedded-server
  settings do not apply.

A loopback connect can select an absolute path interpreted on the server host. For a
non-loopback target, mecatui sends no cwd and rejects `--workspace`; the server's
listener authority chooses its configured root or no-FS profile. Neither mode uploads
or shares a checkout from the computer running the TUI.

Start here for connection ownership, TLS, bearer authentication, and remote
workspace rules: [Connect to a server](../mecatui/remote-servers.md).

## Common workflow

```sh
# Start an embedded session in the current checkout.
mecatui --workspace "$PWD"

# Or seed its first prompt while keeping the session interactive.
mecatui --workspace "$PWD" \
  --prompt "Summarize the failing tests in this repository"
```

The TUI can browse and continue stored sessions, switch models without losing the
visible conversation, approve permission requests, steer a running session, and launch
a dedicated debugger when the connected server supports it:

```sh
mecatui debug SESSION_ID
mecatui connect ADDRESS debug SESSION_ID
```

Debug invocation accepts the full opaque session ID or the exact 12-byte ID shown
in the TUI header; an ambiguous short ID creates nothing and requires the full ID. It
creates a separate no-filesystem analysis session and is explicit consent
to send bounded stored-session evidence—which may include secrets—to the selected model.
It never resumes or mutates the target. Its bounded network view can correlate persisted,
sanitized retry/transport evidence to that target without exposing raw errors or request data.
See [Sessions](../mecatui/sessions.md#diagnose-a-stored-session).

Use the dedicated guides for those workflows:

- [Getting started](../mecatui/getting-started.md) — launch a local session and
  submit a first prompt.
- [Sessions](../mecatui/sessions.md) — browse, inspect, continue, fork, and
  maintain chats.
- [Using the TUI](../mecatui/using-the-tui.md) — streaming, steering, approvals,
  and model switching.
- [Commands and memory](../mecatui/commands-and-memory.md) — learning,
  reflections, and memory-maintenance commands.

## Configuration ownership

Client settings such as themes, keymaps, terminal rendering, and mouse behavior
belong to mecatui. Provider selection, posture, workspace trust, tools, storage,
and other agent behavior belong to the embedded or connected server.

For embedded-server flags, see [Run mecated standalone](../building/deployment/mecated.md).
For model selection, see [Choose models and providers](./choose-models.md). For
permissions and trust, see [Permissions and posture](./permissions-and-posture.md).

## Limitations

- `connect` never discovers or starts a server and does not fall back to embedded
  mode.
- A connected server may expose different tools, models, media capabilities, and
  storage features than an embedded server.
- Remote clients cannot use the TUI host's local files unless those files are
  available in the server's workspace namespace.
- TLS, authentication, and server-side policy are configured at the server
  boundary; mecatui cannot override them locally.

## Next steps

- [Mecatui guide](../mecatui/index.md)
- [Capability and deployment matrix](./capability-matrix.md)
- [Start and resume sessions](./start-and-resume-sessions.md)
