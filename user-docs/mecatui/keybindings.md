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

Use **Up/Down** to move one line, **Page Up/Page Down** to move one page, and
**Home/End** to jump to the beginning or end of a long help overlay.

## Everyday keys

| Key | Action |
| --- | --- |
| `enter` | Send a prompt; while work is running, steer when the server supports it or queue a follow-up otherwise. |
| `shift+enter`, `ctrl+j`, `ctrl+enter`, or `alt+enter` | Insert a newline. Your terminal decides which of these chords it can send; see [Newline chords and your terminal](#newline-chords-and-your-terminal). |
| `↑` | With empty input, bring queued follow-ups back for editing. |
| `ctrl+u` | Clear the unsent draft, including staged attachments and large-paste placeholders (`ClearPrompt`; remappable). |
| physical `esc` twice within 500ms | While idle, clear the draft, including staged media and pastes. Requires enhanced key-event support and a release between presses; repeats do not count. Other views and active runs take precedence. Not remappable; use `ctrl+u` otherwise. |
| `esc` | Clear an active selection first. While work is running, cancel directly and preserve the draft, queued follow-ups, and steer. While idle with a paused queue, clear that queue but preserve the draft. |
| `ctrl+t` | Open `/toolcalls` for the current session; [approval requests](#approve-or-deny-a-request) use it for details. |
| `f9` | Reveal conversation details (reasoning, turn stats, changed files, permanent error details, and benign guardrail notices). Tool results stay in `/toolcalls`. |
| `shift+tab` | Cycle the current session permission mode: **default → plan → accept-edits → default**. In an MCP prompt argument form, it instead moves to the previous required field. |
| `ctrl+g` | Select all prompt text. |
| `ctrl+a` / `ctrl+e` | Move to the start / end of the current prompt line. |
| `ctrl+p` | Move to the previous prompt line. |
| `ctrl+y` | Copy the active prompt or conversation selection; no selection is a no-op. |
| `f6` / `f7` / `f8` | Open Agents / Effort / MCP Prompts. |
| Agents overlay | In a roster, `↑`/`↓` select, `pgup`/`pgdn` page, and `home`/`end` (`g`/`G`) jump to the ends. In focused detail, these keys scroll. `enter` focuses; `esc` goes back. Below 24 terminal rows, only `esc` works. |
| Models picker controls | The mouse wheel scrolls the visible model list without moving its cursor. A primary click on a visible model row moves the cursor without switching models; press `enter` to activate the cursor row. |
| `pgup` / `pgdn` | Scroll the conversation. |
| `home` / `end` | Jump to the top or bottom; `end` resumes auto-follow. |
| `/` | Open the slash-command palette. |
| `ctrl+c` | With text in the prompt, the first press clears the draft and staged media without quitting. With an empty prompt, press twice to quit. |
| `/quit` (or `/exit`) | Quit immediately and cancel an active run. `/quit` appears in the slash palette; `/exit` is a dispatch-only alias. |
| `ctrl+z` | Suspend to the shell; use `fg` to return. |

Suspending does not stop an embedded server or active run. Cancel the run first
if it should stop.

## Newline chords and your terminal

`Newline` answers to four chords because terminals differ in how much of a key
press they can report. A terminal sends `enter` as a single carriage-return byte
with no room for a modifier, so it can only distinguish `shift+enter` and
`ctrl+enter` from a plain `enter` when it implements the Kitty keyboard protocol
or xterm's `modifyOtherKeys`. `mecatui` requests both, and all four chords stay
bound whatever your terminal reports.

Two chords reach `mecatui` without either protocol:

- `ctrl+j` sends a line-feed byte, which every terminal can send. Use it when
  you are unsure.
- `alt+enter` sends an escape prefix followed by carriage return. It needs a
  terminal that sends Option or Alt as a Meta key. On macOS Terminal.app, turn
  on **Use Option as Meta Key** in your profile's Keyboard settings.

The prompt hint reads `shift+enter`, and changes to `ctrl+j` when a modified
`enter` is unconfirmed: either your terminal answers that it supports no
keyboard enhancements, or it does not answer the capability query at all.

Silence is not proof. `mecatui` reads the Kitty protocol's reply, and a terminal
that supports only `modifyOtherKeys` can deliver `shift+enter` while staying
silent here. So read the switch to `ctrl+j` as "here is a chord that works",
not as "`shift+enter` is broken". Try `shift+enter` anyway if you prefer it.
Press `?` to see every bound newline chord at once.

If you remap `Newline`, list your chords in preference order. The hint shows your
first chord, and falls back to the first chord that survives a terminal without
key disambiguation. That fallback is conservative: a chord qualifies only when
its encoding is unambiguous on a legacy terminal, which means an unmodified key,
`ctrl` plus a letter other than `h`, `i`, or `m`, or either of those behind a
single `alt`. A chord such as `ctrl+shift+x` does not qualify, because a legacy
terminal encodes it as a plain `ctrl+x`.

If none of your chords qualify, the hint keeps naming your first one. It has
nothing better to offer, and naming no chord at all would be worse.

### Make shift+enter work on a terminal that cannot encode it

Bind `shift+enter` in the terminal itself to send an escape prefix followed by
carriage return, which reaches `mecatui` as `alt+enter`. In Visual Studio Code,
add this to `keybindings.json`:

```json
{
  "key": "shift+enter",
  "command": "workbench.action.terminal.sendSequence",
  "args": { "text": "\u001b\r" },
  "when": "terminalFocus"
}
```

Zed uses `{ "context": "Terminal", "bindings": { "shift-enter": ["terminal::SendText", "\u001b\r"] } }`,
and Alacritty takes a `[[keyboard.bindings]]` entry with `key = "Return"`,
`mods = "Shift"`, and `chars = "\u001B\r"`. On macOS Terminal.app, turn on **Use
Option as Meta Key** and press `option+enter`.

Inside tmux, turn on extended keys so tmux passes a modified `enter` through
instead of collapsing it to a plain `enter`:

```tmux
set -s extended-keys on
set -as terminal-features 'xterm*:extkeys'
```

tmux answers the capability query on its own behalf, so the prompt hint can name
`shift+enter` while tmux still collapses it. Use `ctrl+j` if a newline chord
submits your prompt inside tmux.

## Approve or deny a request

In a permission modal, inspect the request before choosing:

|Key|Action|
|-|-|
|`a`, `y`, or `enter`|Allow this call once.|
|`w`|Allow this exact main-agent call for the current session when the option is offered.|
|`d`, `n`, or `esc`|Deny.|
|`tab` or `←` / `→`|Move between buttons.|

`Allow always` is session-scoped, applies only to the exact main-agent action,
and never overrides configured policy. In ordinary tool approvals, the
`Toolcalls` binding (`ctrl+t` by default) opens request details instead of
`/toolcalls`; in plan approvals it does nothing.

## Remap actions

Put client-owned bindings in `$XDG_CONFIG_HOME/mecatui/settings.yaml` (normally
`~/.config/mecatui/settings.yaml`):

```yaml
keymap:
  Toolcalls: ctrl+f10
  ExpandConversation: ctrl+f9
```

Or override an action for one launch:

```sh
bin/mecatui --keymap Toolcalls=ctrl+f10 --keymap ExpandConversation=ctrl+f9
```

Bindings resolve per action in this order, from lowest to highest precedence:

1. `$XDG_CONFIG_HOME/mecatui/settings.yaml`.
1. A `--keymap` override for the named action.

Restart `mecatui` after changing the settings file.

Action names are exact, including `Toolcalls` and `ExpandConversation`. Global
actions require a modified or special chord so normal typing remains
available; approval and overlay actions can use bare letters. `mecatui` fails startup with a `keymap:` error for invalid names, empty
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
opens tool-call inspection and `ctrl+v` handles paste. `ctrl+g` selects all only
in the prompt; in the models picker it sets the global default. Remapping an
action to a textarea chord gives the client-owned action precedence.

## Related information

- [Work in the TUI](./using-the-tui.md) for steering, approvals, and common
  conversation controls.
