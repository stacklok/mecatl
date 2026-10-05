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

### Show benign guardrail notices

`mecatui` retains contextual guardrail summaries and live review details. It
hides a completed, acceptable `execute` or `release_result` review while
conversation details are collapsed. Findings, outages, unresolved reviews,
approval requests, withheld results, denied actions, warnings, and unknown
outcomes remain visible.

Press your configured `ExpandTools` keybinding (`ctrl+t` by default) to show
retained benign notices temporarily. To keep them visible while details are
collapsed, add this setting to `$XDG_CONFIG_HOME/mecatui/settings.yaml`:

```yaml
hook_notices:
  show_benign: true
```

This client-owned setting applies to embedded and remote sessions. Project and
server settings do not control it.

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
