---
sidebar_position: 8
title: Themes
description:
  Choose and configure mecatui's built-in terminal themes for light or dark
  displays.
---

# Themes

Choose a built-in theme at launch, or add a JSON palette for your terminal.

`mecatui` includes three themes:

- **aztec**: the default, with jade, turquoise, and gold on obsidian
- **mono**: neutral greys with a blue accent
- **solar**: a warm, light-leaning variant

Select one at launch:

```sh
mecatui --theme mono
```

You can also set `MECATUI_THEME=mono`. Use `--list-themes` to print the themes
available to this launch.

## Let `mecatui` choose for your terminal

When you do not set `--theme` or `MECATUI_THEME`, `mecatui` asks an interactive
terminal for its background color. It selects **solar** for a reported light
background. A dark background, no response, or redirected output uses the
default **aztec** theme.

Passing `--theme` or `MECATUI_THEME`, including `--theme aztec`, skips this
detection. Pin a theme to disable automatic selection.

## Add a partial palette

Theme files are JSON. A palette extends the Aztec base, so it only needs the
slots it changes:

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

Save the file in one of these locations. If the same theme name appears more
than once, the later location wins:

1. `$XDG_CONFIG_HOME/mecatui/themes/` (normally `~/.config/mecatui/themes/`)
2. `<workspace>/.mecatui/themes/`
3. `<cwd>/.mecatui/themes/`
4. a directory passed with `--theme-dir`

Then choose it with `--theme midnight` or `MECATUI_THEME=midnight`. Themes are
discovered at startup, so restart `mecatui` after adding or changing a file.

For all palette slots and the complete theming reference, see
[`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#theming).

## Next steps

- [Customize keybindings](./keybindings.md#remap-actions).
- [Customize the status line](./status-line.md).
