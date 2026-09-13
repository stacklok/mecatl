---
sidebar_position: 1
title: Use mecatui
description: Use mecatui to run local or remote sessions, inspect tools, and manage approvals.
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
  This is separate from local LLM endpoint authentication.

Configure a native endpoint without hand-editing YAML, then manage its local credentials:

```sh
mecatui llm config set corp \
  --gateway-url https://gateway.example/v1 \
  --issuer https://issuer.example \
  --client-id mecatl \
  --default-model corp-model
mecatui llm login corp
```

`config set` updates only the named endpoint in the operator-global Mecatl
`settings.yaml`, preserving unrelated settings and other endpoints. By default it creates an
owner-only (`0700`) credential home at `$XDG_STATE_HOME/mecatl/provider-oidc` (falling back
to `~/.local/state/mecatl/provider-oidc`) and stores its canonical absolute path. To use a
custom location, pass `--credential-home ABSOLUTE_PATH`; custom directories must already
exist, be owned by the current user, and have mode `0700`. The credential home is shared by
all native endpoints, so later updates must retain the configured home until credentials are
migrated; omitting the flag never changes an existing custom home. `config set` does not choose
`models.default_provider` or start login. Public CA trust is the default. For private
PKI, add `--issuer-ca-bundle PATH` and/or `--gateway-ca-bundle PATH`. Add repeatable
`--scope VALUE` flags to replace the default `openid` and `offline_access` scopes, and
use `--resource-audience VALUE` only when the issuer requires one.

Manage credentials for a locally configured native LLM endpoint with
`mecatui llm login ENDPOINT`; add `--no-browser` to print the authorization URL
to stderr and wait at the fixed ToolHive-compatible redirect
`http://localhost:8666/callback` when the current environment cannot launch a browser.
This reuses the redirect registered for the existing ToolHive client ID; Mecatl still stores
native credentials in its isolated credential home and never reads or copies ToolHive credentials.
Inspect credentials with `mecatui llm status [ENDPOINT]`,
and remove them with `mecatui llm logout ENDPOINT`. ToolHive remains compatible
through the reserved `toolhive` endpoint and keeps its established
`mecatui llm login toolhive --skip-browser` spelling; `--no-browser` is for native
endpoints, not ToolHive. The one-release bare `mecatui llm login` alias warns
and remains ToolHive-only. Login confirmations go to stderr, and Mecatl never
prints access or refresh tokens, authorization codes, credential paths, or other
credential material; stdout consumers must use ToolHive's explicit
`thv llm token` tooling.

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
