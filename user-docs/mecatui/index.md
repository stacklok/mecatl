---
sidebar_position: 1
title: Use Mecatl
description:
  Use mecatui to run local or remote sessions, inspect tools, and manage
  approvals.
---

# Use Mecatl

`mecatui` is Mecatl's interactive terminal client. Use it to work with an agent,
inspect tool activity, respond to permission requests, and resume sessions.

Start with [Run your first local session](./getting-started.md), which runs a
private Mecatl server in the same process.

## Choose how to connect

- Run `mecatui` to start a private, embedded server for your local workspace.
- Run `mecatui connect ADDRESS` to use a separately deployed `mecated` or
  `mecak8s` server. The server controls the workspace, model credentials,
  storage, and permissions.
- Run `mecatui sessions` to browse stored sessions before opening one.
- Run `mecatui login ADDRESS` to enroll with a remote server's OIDC issuer. This
  is separate from local provider configuration and credentials.

For local model access, follow [Choose models and providers](/features/sessions/choose-models.md#set-up-a-local-provider).
For an operator-provided deployment, follow [Connect to a remote deployment](./remote-servers.md).

## Guides

- [Run your first local session](./getting-started.md)
- [Install `mecatui`](./installation.md)
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
[`mecated`](/operating/mecated.md) and [`mecak8s`](/operating/mecak8s.md).
