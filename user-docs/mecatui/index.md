---
sidebar_position: 1
title: Use mecatui
---

# Use mecatui

`mecatui` is mecatl's interactive terminal client. It streams the agent's work, shows tool activity and permission requests, and keeps chats available for later continuation.

## Choose how to connect

- Run **`mecatui`** for a private, embedded server. This is the quickest way to work in a local checkout: no separate daemon or port is needed.
- Run **`mecatui connect ADDRESS`** when a `mecated` server is already running. The client does not start or discover a server in this mode; the selected server owns the workspace, credentials, storage, and policy.

Start with [Getting started](./getting-started.md). If you need to run, secure, containerize, or configure a server, see the builder guides for [mecated](/building/deployment/mecated.md) and the [mecatui container image](/building/deployment/mecatui.md).

## Everyday guide

- [Getting started](./getting-started.md) — launch a local or remote session.
- [Sessions](./sessions.md) — resume, browse, inspect, fork, and maintain chats.
- [Using the TUI](./using-the-tui.md) — steer a run, inspect tools, approve work, and switch models.
- [Commands and memory](./commands-and-memory.md) — learning, reflections, and memory-maintenance commands.
- [Customization](./customization.md) — key bindings, themes, and settings ownership.

For exhaustive flags, all key bindings, and troubleshooting details, use the [full `docs/tui.md` reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).
