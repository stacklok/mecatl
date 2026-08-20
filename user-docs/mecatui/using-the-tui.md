---
sidebar_position: 4
title: Use the TUI
---

# Work in the TUI

Assistant text streams into the conversation as it arrives. Tool calls appear as compact cards rather than raw JSON; focus a card and press `ctrl+t` to expand its arguments and result. Edit and Write cards show their diff.

## Keep working while a run is active

You can type while the agent is running. Press `enter` to steer the current run; the message is applied at the next safe turn boundary. If the server does not support steering, it becomes a queued follow-up instead. Several queued lines become one next prompt.

With an empty input, press `↑` to bring a pending steer or queued follow-up back for editing. `esc` clears staged text first, then the pending message, then cancels the in-flight run—so cancellation is not a single accidental keypress.

## Review approvals

When a tool needs permission, a modal shows what it wants to do. Read the request, then allow it once, allow the exact action for this session when offered, or deny it. Long arguments can be scrolled; `ctrl+t` opens a full-screen view when needed. Mouse buttons activate the same choices as their displayed keys.

## Change the conversation settings

- `/models` starts a new session on the selected model while carrying the conversation forward. A cross-provider change drops provider-private reasoning state, not the conversation text.
- `/effort` forks the conversation onto the chosen reasoning-effort tier. Unsupported tiers are reported rather than silently applied.
- `/clear` starts over; `/session` shows the active session details.

## Default key reference

Use `?` for the live, complete key map—especially after remapping. The useful defaults are:

| Key | Action |
| --- | --- |
| `enter` | Send a prompt; while running, steer or queue a follow-up |
| `shift+enter` | Insert a newline |
| `esc` | Back out staged input, then cancel a running turn |
| `ctrl+t` | Expand a tool card or approval details |
| `pgup` / `pgdn` | Scroll the conversation |
| `/` | Open slash commands |
| `ctrl+c` twice on an empty prompt | Quit safely |
| `ctrl+z` | Suspend to the shell; use `fg` to return |

Suspending does **not** stop the embedded server or an active run. Cancel first if you want the work to stop. The full list, including overlays, mouse behavior, and accessibility-relevant focus controls, is in [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#keys).
