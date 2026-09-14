---
sidebar_position: 9
title: Customize mecatui
description:
  Customize mecatui's client settings while keeping embedded server settings
  separate.
---

# Customize mecatui

## Know which settings you own

`mecatui` client settings control local presentation and input: themes,
keybindings, and similar UI behavior. They apply whether the client uses an
embedded or remote server.

Bare `mecatui` also starts an embedded server. Its server flags control local
provider selection, storage, and policy. `mecatui connect` rejects those flags
because the remote server controls credentials, available providers and models,
workspace policy, storage, retention, and learning.

## Customize the client

- [Keybindings](./keybindings.md) explains default keys, precedence, and safe
  remapping.
- [Themes](./themes.md) explains built-in themes, custom palette locations, and
  selection.

### Status line

`$XDG_CONFIG_HOME/mecatui/settings.yaml` may customize the local header and
footer with `status_customization`. Project files and remote servers cannot
configure this client-only setting. Choose either a responsive template set or a
local executable. The client sends command input only through standard input.

See [Status line customization](./status-line.md) for the complete schema, input
reference, StatusML grammar and escaping rules, safety limits, and copyable
template and executable examples.

For server ownership boundaries and secure remote connections, see
[Connect to a server](./remote-servers.md). Operators should use
[Run mecated standalone](/building/deployment/mecated.md).
