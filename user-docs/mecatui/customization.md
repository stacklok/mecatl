---
sidebar_position: 6
title: Customize mecatui
---

# Customize mecatui

## Remap keys

Put client key bindings in `$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally `~/.config/mecatui/settings.yaml`):

```yaml
keymap:
  Agents: ctrl+f12
  Effort: ctrl+f5,ctrl+f6
```

Or override one action for this launch:

```sh
bin/mecatui --keymap Agents=ctrl+f12 --keymap Effort=ctrl+f5
```

CLI bindings override the client file per action. The legacy `keymap:` entry in `$XDG_CONFIG_HOME/mecatl/settings.yaml` still works, but is deprecated. Restart mecatui after changing settings. Use `?` to verify the active bindings; global actions need modified or special chords so ordinary typing remains safe.

## Pick or add a theme

Choose a built-in or installed theme:

```sh
bin/mecatui --theme mono
```

A custom JSON theme can live in `$XDG_CONFIG_HOME/mecatui/themes/`, `<workspace>/.mecatui/themes/`, the current directory's `.mecatui/themes/`, or a directory passed with `--theme-dir`. Later locations take precedence. A partial palette extends the default Aztec theme:

```json
{
  "name": "midnight",
  "palette": {
    "accent": "#7C5CFF",
    "primary": "#5CC8FF",
    "error": "#FF5C7C"
  }
}
```

Then select it with `--theme midnight` (or `MECATUI_THEME=midnight`).

## Know which settings you own

`mecatui` client settings control local presentation and input, such as key bindings and themes. Embedded mode also starts a local server, so flags such as `--mock`, provider selection, and local storage affect that embedded server.

With `mecatui connect`, the client does not apply embedded-server flags or local server settings. Credentials, provider/model availability, learning settings, workspace policy, storage, and retention belong to the remote server host. Change those only through that server's approved configuration path. For exhaustive flags, action names, and theme palette fields, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#remapping-keys).
