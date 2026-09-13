---
sidebar_position: 6
title: Keybindings
description:
  Use mecatui keyboard shortcuts to send prompts, navigate chats, and inspect
  activity.
---

# Keybindings

Press `?` on an empty prompt to open the live help overlay. The overlay shows
your active bindings and dims features that the connected server does not
provide.

## Everyday keys

| Key | Action |
| --- | --- |
| `enter` | Send a prompt; while work is running, steer when the server supports it or queue a follow-up otherwise. |
| `shift+enter` or `ctrl+j` | Insert a newline. |
| `↑` | With empty input, bring queued follow-ups back for editing. |
| `ctrl+u` | Clear the unsent draft, including staged attachments and large-paste placeholders (`ClearPrompt`; remappable). |
| physical `esc` twice within 500ms | While idle with a focused draft containing text, staged attachments, large-paste content, or pending media, clear it through `ClearPrompt`; attachment-only drafts qualify. This gesture requires enhanced key-event support, so it is unavailable in terminals that do not report it. The first press is silent and only arms; an `esc` key release must establish a distinct press before a second non-repeat press can clear. A key repeat or another press before release cannot complete it. Selection, palette, mention, approval, overlay, modal, and running-turn owners take precedence and disarm it, as do another key or expiry. This physical gesture is not remappable; use the universal remappable `ClearPrompt` / `ctrl+u` alternative. |
| `esc` | Clear an active selection first. While work is running, cancel directly and preserve the draft, queued follow-ups, and steer. While idle with a paused queue, clear that queue but preserve the draft. |
| `ctrl+t` | Expand a focused tool card or approval details. For a collapsed tool card, the expanded view shows the complete tool arguments and result; press it again to return to the width-bounded preview. |
| `shift+tab` | Cycle the current session permission mode: **default → plan → accept-edits → default**. In an MCP prompt argument form, it instead moves to the previous required field. |
| `ctrl+g` | Select all prompt text. |
| `ctrl+a` / `ctrl+e` | Move to the start / end of the current prompt line. |
| `ctrl+p` | Move to the previous prompt line. |
| `ctrl+y` | Copy the active prompt or conversation selection; no selection is a no-op. |
| `f6` / `f7` / `f8` | Open Agents / Effort / MCP Prompts. |
| Agents overlay controls | In the normal-height F6 Agents overlay, remappable `Up`, `Down`, `ScrollU`, `ScrollD`, `JumpTop`, and `JumpEnd` move the selected roster item or traverse overflowed activity; `enter` focuses a roster item and `esc` goes back or closes. On terminals below 24 rows, F6 shows a compact context line and only `esc` is active. |
| `pgup` / `pgdn` | Scroll the conversation. |
| `home` / `end` | Jump to the top or bottom; `end` resumes auto-follow. |
| `/` | Open the slash-command palette. |
| `ctrl+c` twice on an empty prompt | Quit safely. |
| `/quit` (or `/exit`) | Quit immediately and cancel an active run. `/quit` appears in the slash palette; `/exit` is a dispatch-only alias. |
| `ctrl+z` | Suspend to the shell; use `fg` to return. |

Suspending does not stop an embedded server or active run. Cancel the run first
if it should stop.

The double-`esc` gesture requires two separate key presses and releases; key
repeat does not trigger it. A selection, palette, approval, overlay, modal,
active run, or another key disarms the gesture. Use `ClearPrompt` (`ctrl+u`) in
terminals without enhanced key-event support.

## Approve or deny a request

In a permission modal, inspect the request before choosing:

|Key|Action|
|-|-|
|`a`, `y`, or `enter`|Allow this call once.|
|`w`|Allow this exact main-agent call for the current session when the option is offered.|
|`d`, `n`, or `esc`|Deny.|
|`tab` or `←` / `→`|Move between buttons.|

`Allow always` is session-scoped, applies only to the exact main-agent action,
and never overrides configured policy. Long arguments can be scrolled; `ctrl+t`
opens a full-screen view when offered.

## Remap actions

Put client-owned bindings in `$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally
`~/.config/mecatui/settings.yaml`):

```yaml
keymap:
  Agents: f6
  Effort: f7
  Prompts: f8
  SelectAll: ctrl+g
  CopySelection: ctrl+y
  ClearPrompt: ctrl+u
  ModeSwitch: alt+m # optional override for the default shift+tab
  Allow: y
  Deny: n
```

Or override an action for one launch:

```sh
bin/mecatui --keymap Agents=f10 --keymap Effort=f11 --keymap Prompts=f12
```

Bindings resolve per action in this order, from lowest to highest precedence:

1. The deprecated `$XDG_CONFIG_HOME/mecatl/settings.yaml` keymap.
1. `$XDG_CONFIG_HOME/mecatui/settings.yaml`.
1. A `--keymap` override for the named action.

Restart `mecatui` after changing the settings file.

Action names are exact. Global actions require a modified or special chord so
normal typing remains available; approval and overlay actions can use bare
letters. `mecatui` fails startup with a `keymap:` error for invalid names, empty
chords, conflicts, a shared submit and newline key, or an unsafe approval
collision.

### Input editing caveat

The prompt editor provides fixed editing keys. Only `SelectAll`,
`CopySelection`, and `ClearPrompt` can be remapped with `--keymap`.

|Key|Editing action|
|-|-|
|`ctrl+a` / `ctrl+e`|Move to the start or end of the current line.|
|`ctrl+b` / `ctrl+f`|Move backward or forward by one character.|
|`ctrl+w`|Delete the previous word.|
|`ctrl+k`|Delete to the end of the line.|

Use `shift+arrow` for keyboard selection. On the alternate screen, drag over
prompt text for mouse selection. Starting a prompt selection clears a
conversation selection and vice versa. Releasing the mouse does not copy the
prompt; use `ctrl+y` or right-click to copy the active selection. Copying writes
the text to the system clipboard and, on X11 and Wayland, the primary selection.
Install `wl-clipboard` or `xclip` when your terminal does not support the
primary-selection mirror through OSC 52. With `--no-mouse`, the terminal retains
native mouse selection and keyboard selection remains available.

Client-owned actions take precedence over textarea chords. For example, `ctrl+t`
expands details and `ctrl+v` handles paste. `ctrl+g` selects all only in the
prompt; in the models picker it sets the global default. Remapping an action to
a textarea chord gives the client-owned action precedence.

For every action name, editing chord, overlay key, mouse behavior, and
validation rule, see the
[exhaustive `docs/tui.md` key reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#keys).

## Related information

- [Work in the TUI](./using-the-tui.md) for steering, approvals, and common
  conversation controls.
