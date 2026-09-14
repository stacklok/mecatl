---
sidebar_position: 6
title: Keybindings
description:
  Use mecatui keyboard shortcuts to send prompts, navigate chats, and inspect
  activity.
---

# Keybindings

Press `?` on an empty prompt to open the live help overlay. It shows the active
bindings after remapping and greys out features the connected server does not
provide.

## Everyday keys

|Key|Action|
|-|-|
|`enter`|Send a prompt; while work is running, steer when the server supports it or queue a follow-up otherwise.|
|`shift+enter` or `ctrl+j`|Insert a newline.|
|`↑`|With empty input, bring queued follow-ups back for editing.|
|`ctrl+u`|Clear the unsent draft, including staged attachments and large-paste placeholders (`ClearPrompt`; remappable).|
|physical `esc` twice within 500ms|While idle, clear a focused draft, including staged attachments, large-paste content, and pending media. The first press is silent and only arms the gesture; an `esc` key release must follow, then a later non-repeat press completes it — key repeat cannot. Another key, an active selection, palette, approval, overlay, modal, or running-turn owners take precedence and disarm it. This requires enhanced key-event support and is not remappable. Use `ClearPrompt` (`ctrl+u`) in terminals that do not support it.|
|`esc`|Clear an active selection first. While work is running, cancel directly and preserve the draft, queued follow-ups, and steer. While idle with a paused queue, clear that queue but preserve the draft.|
|`ctrl+t`|Expand a focused tool card or approval details. For a collapsed tool card, the expanded view shows the complete tool arguments and result; press it again to return to the width-bounded preview.|
|`shift+tab`|Cycle the current session permission mode: **default → plan → accept-edits → default**. In an MCP prompt argument form, it instead moves to the previous required field.|
|`ctrl+g`|Select all prompt text.|
|`ctrl+a` / `ctrl+e`|Move to the start / end of the current prompt line.|
|`ctrl+p`|Move to the previous prompt line.|
|`ctrl+y`|Copy the active prompt or conversation selection; no selection is a no-op.|
|`f6` / `f7` / `f8`|Open Agents / Effort / MCP Prompts.|
|`pgup` / `pgdn`|Scroll the conversation.|
|`home` / `end`|Jump to the top or bottom; `end` resumes auto-follow.|
|`/`|Open the slash-command palette.|
|`ctrl+c` twice on an empty prompt|Quit safely.|
|`/quit` (or `/exit`)|Quit immediately and cancel an active run. `/quit` appears in the slash palette; `/exit` is a dispatch-only alias.|
|`ctrl+z`|Suspend to the shell; use `fg` to return.|

Suspending does not stop an embedded server or active run. Cancel the run first
if it should stop.

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

Bindings resolve per action. The deprecated
`$XDG_CONFIG_HOME/mecatl/settings.yaml` keymap has the lowest precedence, the
client file overrides it, and `--keymap` wins for the named action. Restart
`mecatui` after changing the settings file.

Action names are exact. Global actions need a modified or special chord so
normal typing stays available; approval and overlay actions can use bare
letters. Invalid names, empty chords, conflicting bindings, a shared
submit/newline key, or an unsafe approval collision fail startup with a
`keymap:` error.

### Input editing caveat

The prompt textarea has editing keys that are not remappable through `--keymap`,
apart from `SelectAll`, `CopySelection`, and `ClearPrompt`. For example,
`ctrl+a`/`ctrl+e` move to the start or end of the current line,
`ctrl+b`/`ctrl+f` move by character, `ctrl+w` deletes a word, and `ctrl+k`
deletes to the end of the line.

Use `shift+arrow` for keyboard selection. On the alternate screen, drag over
prompt text for mouse selection. Starting a prompt selection clears a
conversation selection and vice versa. Releasing the mouse does not copy the
prompt; use `ctrl+y` or right-click to copy the active selection. Copying mirrors
the text into both the system clipboard and, on X11 and Wayland, the primary
selection, so a selection copied inside mecatui can be middle-click pasted
elsewhere (install `wl-clipboard` or `xclip` for the primary-selection mirror on
terminals that do not honour OSC52). With `--no-mouse`, the terminal retains
native mouse selection, while keyboard selection remains available.

Client-owned actions take precedence over textarea chords. For example, `ctrl+t`
expands details and `ctrl+v` handles paste. `ctrl+g` selects all only in the
prompt; in the models picker it sets the global default. Remapping an action to
a textarea chord gives the client-owned action precedence.

For every action name, editing chord, overlay key, mouse behavior, and
validation rule, see the
[exhaustive `docs/tui.md` key reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#keys).
