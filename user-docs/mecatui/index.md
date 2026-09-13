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

`setup` guides first-time configuration. To define a custom provider explicitly,
run `mecatui providers add NAME`; it collects its HTTPS endpoint, API flavor,
default model, and authentication method. Use `mecatui providers login NAME` or
`logout NAME` for locally managed credentials, and `mecatui providers set-default
NAME [MODEL]` to select the embedded default. The [standalone deployment guide](/building/deployment/mecated.md#configure-providers)
documents the operator `providers` and `credential_store` schema, including
`--api-key-file` for provider-credentials YAML.

`mecatui login ADDRESS` is different: it authenticates this client to a remote
server. It neither configures nor enrolls that server's providers. ToolHive is
external and owns its LLM credentials and lifecycle; use `thv llm` tooling for
ToolHive setup rather than treating it as a locally managed provider credential.

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
