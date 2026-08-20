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

For server ownership boundaries and secure remote connections, see [Connect to a server](./remote-servers.md). Operators should use [Run mecated standalone](/building/deployment/mecated.md).
