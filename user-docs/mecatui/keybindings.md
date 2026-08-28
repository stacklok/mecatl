---
sidebar_position: 6
title: Keybindings
---

# Keybindings

Press `?` on an empty prompt to open the live help overlay. It shows the active bindings after remapping and greys out features the connected server does not provide.

## Everyday keys

| Key | Action |
| --- | --- |
| `enter` | Send a prompt; while work is running, steer when the server supports it or queue a follow-up otherwise. |
| `shift+enter` or `ctrl+j` | Insert a newline. |
| `↑` | With empty input, bring queued follow-ups back for editing. |
| `esc` | Clear staged input, then the queue, then cancel the active run. |
| `ctrl+t` | Expand a tool card or approval details. |
| `pgup` / `pgdn` | Scroll the conversation. |
| `home` / `end` | Jump to the top or bottom; `end` resumes auto-follow. |
| `/` | Open the slash-command palette. |
| `ctrl+c` twice on an empty prompt | Quit safely. |
| `ctrl+z` | Suspend to the shell; use `fg` to return. |

Suspending does not stop an embedded server or an active run. Cancel first if you want the work to stop.

## Approve or deny a request

In a permission modal, inspect the request before choosing:

| Key | Action |
| --- | --- |
| `a`, `y`, or `enter` | Allow this call once. |
| `w` | Allow this exact main-agent call for the current session when the option is offered. |
| `d`, `n`, or `esc` | Deny. |
| `tab` or `←` / `→` | Move between buttons. |

`Allow always` is session-scoped, applies only to the exact main-agent action, and never overrides configured policy. Long arguments can be scrolled; `ctrl+t` opens a full-screen view when offered.

## Remap actions

Put client-owned bindings in `$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally `~/.config/mecatui/settings.yaml`):

```yaml
keymap:
  Agents: ctrl+f12
  Effort: ctrl+f5,ctrl+f6
  Allow: y
  Deny: n
```

Or override an action for one launch:

```sh
bin/mecatui --keymap Agents=ctrl+f12 --keymap Effort=ctrl+f5
```

Bindings resolve per action: the deprecated legacy `$XDG_CONFIG_HOME/mecatl/settings.yaml` keymap is lowest precedence, the client file overrides it, and `--keymap` wins for the named action. Settings are read at startup, so restart after changing them.

Action names are exact. Global actions need a modified or special chord so normal typing stays available; approval and overlay actions can use bare letters. Invalid names, empty chords, conflicting bindings, a shared submit/newline key, or an unsafe approval collision fail startup with a `keymap:` error.

### Input editing caveat

The prompt textarea has its own editing keys and they are not remappable through `--keymap`. For example, `ctrl+b`/`ctrl+f` move by character, `ctrl+w` deletes a word, and `ctrl+u`/`ctrl+k` kill to the start/end of a line. On the alternate screen, clicking prompt text also places the caret; mouse selection/copy within the prompt is not supported yet. Some defaults intentionally take precedence: `ctrl+a` opens Agents, `ctrl+e` opens Effort, `ctrl+t` expands details, and `ctrl+v` handles paste. Remapping an action away frees its chord for the textarea.

For every action name, editing chord, overlay key, mouse behavior, and validation rule, see the [exhaustive `docs/tui.md` key reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#keys).
