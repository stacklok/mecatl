---
sidebar_position: 8
title: Themes
---

# Themes

mecatui ships with three themes:

- **aztec** — the default: jade, turquoise, and gold on obsidian.
- **mono** — neutral greys with a blue accent.
- **solar** — a warm, light-leaning variant.

Select one at launch:

```sh
bin/mecatui --theme mono
```

You can also set `MECATUI_THEME=mono`. Use `--list-themes` to print the themes available to this launch.

## Add a partial palette

Theme files are JSON. A palette extends the Aztec base, so it only needs the slots it changes:

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

Save the file in one of these locations. Later locations take precedence when names collide:

1. `$XDG_CONFIG_HOME/mecatui/themes/` (normally `~/.config/mecatui/themes/`)
2. `<workspace>/.mecatui/themes/`
3. `<cwd>/.mecatui/themes/`
4. a directory passed with `--theme-dir`

Then choose it with `--theme midnight` or `MECATUI_THEME=midnight`. Themes are discovered at startup; restart mecatui after adding or changing a file.

For all palette slots and the complete theming reference, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#theming).
