---
sidebar_position: 1
title: Use mecatui
description: Use mecatui to run local or remote sessions, inspect tools, and manage approvals.
---

# Use mecatui

`mecatui` is Mecatl's interactive terminal client. Use it to work with an agent,
inspect tool activity, respond to permission requests, and resume sessions.

Start with [Run your first local session](./getting-started.md). It runs a private
Mecatl server in the same process, so you can learn the client before deploying
a separate server.

## Choose how to connect

- Run `mecatui` to start a private, embedded server for your local workspace.
- Run `mecatui connect ADDRESS` to use a separately deployed `mecated` or
  `mecak8s` server. The server controls the workspace, model credentials,
  storage, and permissions.
- Run `mecatui sessions` to browse stored sessions before opening one.

Follow [Connect to a server](./remote-servers.md) when you are ready to move the
server out of your local process.

## Guides

- [Run your first local session](./getting-started.md)
- [Connect to a server](./remote-servers.md)
- [Manage sessions](./sessions.md)
- [Use the TUI](./using-the-tui.md)
- [Keybindings](./keybindings.md)
- [Commands and memory](./commands-and-memory.md)
- [Customize `mecatui`](./customization.md)
- [Themes](./themes.md)
- [Customize the status line](./status-line.md)
- [Troubleshooting](./troubleshooting.md)

To deploy or operate the server, see the guides for
[`mecated`](/building/deployment/mecated.md) and
[`mecak8s`](/building/deployment/mecak8s.md).
