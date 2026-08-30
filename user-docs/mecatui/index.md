---
sidebar_position: 1
title: Use mecatui
---

# Use mecatui

`mecatui` is mecatl's interactive terminal client. It streams the agent's work, shows tool activity and permission requests, and keeps chats available for later continuation.

## Choose how to connect

- Run **`mecatui`** for a private, embedded server. This is the quickest way to work in a local checkout: no separate daemon or port is needed.
- Run **`mecatui connect ADDRESS`** when a `mecated` server is already running. The client does not start or discover a server in this mode; that server owns the workspace, credentials, storage, and policy.
- Run **`mecatui sessions`** to open the stored-session browser before creating a chat, or **`mecatui login`** for the ToolHive LLM gateway OIDC browser flow. `login` does not authenticate to a remote `mecated` server.

`mecatui --help`, `mecatui -h`, and `mecatui help` show this concise command index. `mecatui help sessions`, `mecatui help connect`, and `mecatui help login` alias the corresponding command-specific help; direct `sessions --help`, `connect --help`, and `login --help` remain available. Use bare `mecatui --help-flags` for common embedded-mode flags; `--help-all` is available with bare `mecatui`, `sessions`, and `connect`. `login` supports only standard help and `--skip-browser`.

Read [Connect to a server](./remote-servers.md) before using a remote endpoint. If you need to run, secure, containerize, or configure a server, see the builder guides for [mecated](/building/deployment/mecated.md) and the [mecatui container image](/building/deployment/mecatui.md).

## Everyday guide

- [Getting started](./getting-started.md) — launch a local session.
- [Connect to a server](./remote-servers.md) — choose embedded or remote mode and connect safely.
- [Sessions](./sessions.md) — resume, browse, inspect, fork, and maintain chats.
- [Using the TUI](./using-the-tui.md) — steer a run, inspect tools, approve work, and switch models.
- [Keybindings](./keybindings.md) — everyday keys, approval controls, and remapping.
- [Commands and memory](./commands-and-memory.md) — learning, reflections, and memory-maintenance commands.
- [Themes](./themes.md) — select or add a color palette.
- [Status line customization](./status-line.md) — configure responsive status templates or a local executable.
- [Customization](./customization.md) — understand which settings belong to the client or server.
- [Troubleshooting](./troubleshooting.md) — take safe next steps when something fails.

For exhaustive flags and behavior, use the [full `docs/tui.md` reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md).
