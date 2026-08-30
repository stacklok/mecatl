---
sidebar_position: 9
title: Customize mecatui
---

# Customize mecatui

## Know which settings you own

`mecatui` client settings control local presentation and input: themes, keybindings, and similar UI behavior. They apply whether the client uses an embedded or remote server.

Bare `mecatui` also starts an embedded server, so its embedded-server flags can control local provider selection, storage, and policy for that server. With `mecatui connect`, those flags are rejected: credentials, provider/model availability, workspace policy, storage, retention, and learning belong to the remote server host. Change remote behavior only through that server's approved configuration path.

## Customize the client

- [Keybindings](./keybindings.md) explains default keys, precedence, and safe remapping.
- [Themes](./themes.md) explains built-in themes, custom palette locations, and selection.

### Status line

`$XDG_CONFIG_HOME/mecatui/settings.yaml` may customize the local header and footer
with `status_customization`. It is a client-only setting: project files and remote
servers cannot configure it. Choose exactly one responsive template set or a local
direct executable; the client receives raw command input only on stdin and never
runs a configured shell string.

See [Status line customization](./status-line.md) for the complete schema, input
reference, StatusML grammar and escaping rules, safety limits, and copyable
template and executable examples.

For server ownership boundaries and secure remote connections, see [Connect to a server](./remote-servers.md). Operators should use [Run mecated standalone](/building/deployment/mecated.md).
