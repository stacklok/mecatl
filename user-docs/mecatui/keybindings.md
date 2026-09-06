---
sidebar_position: 6
title: Keybindings
description: Use mecatui keyboard shortcuts to send prompts, navigate chats, and inspect activity.
---

# Keybindings

Press `?` on an empty prompt to open the live help overlay. It shows the active bindings after remapping and greys out features the connected server does not provide.

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
| `ctrl+shift+c` | Copy the active prompt or conversation selection; no selection is a no-op. |
| `pgup` / `pgdn` | Scroll the conversation. |
| `home` / `end` | Jump to the top or bottom; `end` resumes auto-follow. |
| `/` | Open the slash-command palette. |
| `ctrl+c` twice on an empty prompt | Quit safely. |
| `/quit` (or `/exit`) | Quit immediately and cancel an active run. `/quit` appears in the slash palette; `/exit` is a dispatch-only alias. |
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
  SelectAll: ctrl+g
  CopySelection: ctrl+shift+c
  ClearPrompt: ctrl+u
  ModeSwitch: alt+m # optional override for the default shift+tab
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

The prompt textarea has its own editing keys and they are not remappable through `--keymap`, apart from the client-owned `SelectAll`, `CopySelection`, and `ClearPrompt` actions. `ClearPrompt` defaults to `ctrl+u` and clears the entire unsent draft, including staged attachments and large-paste placeholders. For example, `ctrl+b`/`ctrl+f` move by character, `ctrl+w` deletes a word, and `ctrl+k` kills to the end of a line. It supports upstream keyboard selection, including `shift+arrow`; typing, text paste, newline insertion, and deletion act on an active selection. On the alternate screen, drag over prompt text to select it visibly. Starting a prompt selection clears a conversation selection and vice versa. Prompt mouse release does not copy; use `ctrl+shift+c` or right-click to copy the active prompt or conversation selection through Mecatl's clipboard transport. Prompt selection survives permission prompts, overlays, and other non-content changes, and clears when the prompt changes or another surface starts a selection. With `--no-mouse`, mouse gestures are disabled so the terminal retains native selection, while keyboard prompt selection remains available. Some defaults intentionally take precedence: `ctrl+a` opens Agents, `ctrl+e` opens Effort, `ctrl+t` expands details, and `ctrl+v` handles paste. `ctrl+g` selects all only in the prompt; it remains `SetGlobalDefault` in the models picker. Remapping an action away frees its chord for the textarea.

For every action name, editing chord, overlay key, mouse behavior, and validation rule, see the [exhaustive `docs/tui.md` key reference](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#keys).
