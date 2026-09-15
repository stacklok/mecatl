---
sidebar_position: 9
title: Customize mecatui
description:
  Customize mecatui's client settings while keeping embedded server settings
  separate.
---

# Customize mecatui

Client settings change how `mecatui` looks and handles input. Server settings
control model access, storage, and policy.

## Know which settings you can change

`mecatui` client settings control local presentation and input: themes,
keybindings, and similar UI behavior. They apply whether the client uses an
embedded or remote server.

Bare `mecatui` also starts an embedded server, so its server flags can control
the local provider, storage, and policy. With `mecatui connect`, the remote
server controls credentials, providers, models, workspace policy, storage,
retention, and learning. The client rejects server-only flags in this mode.

## Customize the client

- [Keybindings](./keybindings.md) explains default keys, precedence, and safe
  remapping.
- [Themes](./themes.md) explains built-in themes, custom palette locations, and
  selection.

## Customize the status line

`$XDG_CONFIG_HOME/mecatui/settings.yaml` may customize the local header and
footer with `status_customization`. Project files and remote servers cannot
configure this client-only setting. Choose either a responsive template set or a
local executable. The client sends command input only through standard input.

See [Status line customization](./status-line.md) for template and executable
examples, the input reference, StatusML syntax, and safety limits.

## Next steps

- [Connect to a server](./remote-servers.md) to use a remote deployment.
- [Run `mecated` standalone](/building/deployment/mecated.md) to configure the
  server itself.
