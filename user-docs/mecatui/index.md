---
sidebar_position: 1
title: Use mecatui
description:
  Use mecatui to run local or remote sessions, inspect tools, and manage
  approvals.
---

# Use mecatui

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
- Run `mecatui login ADDRESS` to enroll with a remote server's OIDC issuer.
  This is separate from local provider configuration and credentials.

Configure providers for an embedded local server with the `providers` command:

```sh
mecatui providers setup
mecatui providers
```

`setup` configures an embedded server; `add NAME` defines a custom provider, and
`login`, `logout`, and `set-default` manage it. For credential sources, secret
safety, and provider selection, see
[Choose models and providers](/features/choose-models.md). The [standalone
deployment guide](/building/deployment/mecated.md#configure-providers) owns the
operator `providers` and `credential_store` schema.

`mecatui login ADDRESS` authenticates this client to a remote server; it does
not configure that server's providers. ToolHive is external and owns its LLM
credentials and lifecycle; use `thv llm` tooling for ToolHive setup.

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
