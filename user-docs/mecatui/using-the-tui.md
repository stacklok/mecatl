---
sidebar_position: 5
title: Work in the TUI
sidebar_label: Use the TUI
description:
  Work in the mecatui terminal interface, steer runs, review tools, and approve
  actions.
---

# Work in the TUI

Assistant text streams into the conversation as it arrives. Tool calls appear as
compact cards with previews that fit the terminal width. Focus a card and press
`ctrl+t` to view its complete output and arguments. Press `ctrl+t` again to
return to the preview. Edit and Write cards show their diff.

## Keep working while a run is active

You can type while the agent is running. Press `enter` to steer the current run;
the message is applied at the next safe turn boundary. Images and other
supported staged media travel with the steer, including media-only input. If the
server does not support steering, it becomes a queued follow-up instead. Several
queued lines become one next prompt. Bare recognized TUI commands are
intercepted by the client; `/help` is local UI, while `/clear` can cancel and
replace the current session at any point, including during an approval. Unknown
slash commands, workspace commands, and built-ins with arguments remain
model-facing input.

With an empty input, press `↑` to bring a pending steer or queued follow-up back
for editing together with its staged media. `ctrl+u` clears the unsent draft and
its staged attachments/placeholders. `esc` first clears an active selection;
otherwise, while a run is active it cancels directly and preserves the draft,
queued follow-ups, and pending steer. When idle with a paused queue, `esc`
clears that queue while preserving the draft.

## When a model stream fails

When the server reports `retryable + precommit`, `mecatui` repeats the failed
model step once. The retry adds no prompt and preserves queued messages. If it
fails again, use `/retry` to retry the step manually. Also use `/retry` for a
`retryable + visible` failure, including an eligible retry-pending session
reopened from storage.

`/retry` preserves the prompt textarea and queued prompts. For visible failures,
scrollback marks the failed partial output as superseded. If no eligible failure
is pending, the command reports that fact and makes no changes.

## Review approvals

When a tool needs permission, a modal shows what it wants to do. Read the
request, then allow it once, allow the exact action for this session when
offered, or deny it. Long arguments can be scrolled; `ctrl+t` opens a
full-screen view when needed. Mouse buttons activate the same choices as their
displayed keys.

## Complete browser authorization

When workspace-service enrollment or an MCP tool opens a browser authorization,
complete consent there and return to the TUI. `mecatui` observes the pending
request automatically; do not press a refresh/recheck key. Each
workspace-service connect, check, retry, or cancel attempt is bounded to 30
seconds. If that deadline expires, the server may have changed state even though
mecatui did not receive the response, so mecatui stops automatic checks rather
than guessing or retrying. Follow the displayed recovery: run `/clear`, then run
`/tools-connect` in the replacement session. This does not claim that the
timed-out server operation completed.

You can still cancel a pending MCP authorization from its card. Presentation
links are opened or copied only for that interaction and are not retained in the
conversation.

## Change the conversation settings

- `/models` starts a new session on the selected model and keeps the
  conversation. **Switching models is expensive as it clears caches.**
- `/compact` asks a capable server to compact the current session's model
  history once. Use the bare command with no arguments while idle. It sends no
  prompt, keeps visible scrollback, and reports changed or already compact; a
  cascade summary may still cost model tokens.
- `/effort` forks the conversation onto the chosen reasoning-effort tier.
  Unsupported tiers are reported rather than silently applied.
- `/clear` asks the server for a distinct empty-history successor that inherits
  exact placement. If replacement fails after an active run or approval is
  cancelled, no successor is created and the source stays selected but may now
  be cancelled; retry `/clear` after it settles. Workspace changes are not
  rolled back. `/session` shows path-free active-session details.

## A short key reference

Use `?` on an empty prompt for the live help overlay. The everyday defaults are
`enter` to send or steer, `shift+enter` to insert a newline, `ctrl+t` to inspect
details, `pgup`/`pgdn` to scroll, and `/` to open commands. If the server does
not support steering, `enter` queues a follow-up while a run is active. See
[Keybindings](./keybindings.md) for approval controls, remapping, and the
complete reference.
