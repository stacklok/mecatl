---
sidebar_position: 5
title: Use the TUI
description: Work in the mecatui terminal interface, steer runs, review tools, and approve actions.
---

# Work in the TUI

Assistant text streams into the conversation as it arrives. Tool calls appear as compact cards rather than raw JSON; focus a card and press `ctrl+t` to expand its arguments and result. Edit and Write cards show their diff.

## Keep working while a run is active

You can type while the agent is running. Press `enter` to steer the current run; the message is applied at the next safe turn boundary. Images and other supported staged media travel with the steer, including media-only input. If the server does not support steering, it becomes a queued follow-up instead. Several queued lines become one next prompt. Bare recognized TUI commands are intercepted by the client; `/help` is local UI, while `/clear` can cancel and replace the current session at any point, including during an approval. Unknown slash commands, workspace commands, and built-ins with arguments remain model-facing input.

With an empty input, press `↑` to bring a pending steer or queued follow-up back for editing together with its staged media. `ctrl+u` clears the unsent draft and its staged attachments/placeholders. `esc` first clears an active selection; otherwise, while a run is active it cancels directly and preserves the draft, queued follow-ups, and pending steer. When idle with a paused queue, `esc` clears that queue while preserving the draft.

## When a model stream fails

Mecatui automatically repeats the exact failed model step once only when the server explicitly reports `retryable + precommit`. The retry adds no prompt and keeps any queued future messages in place. If that automatic retry fails again, use **`/retry`** to retry the same step manually. `/retry` is also the required action for a `retryable + visible` failure, including an eligible retry-pending session reopened from storage. It sends no synthetic prompt or user card, does not reset the textarea, and leaves queued prompts untouched. A visible retry adds a scrollback notice that the failed partial output is superseded; a precommit retry does not claim that output was visible. If no eligible failure is pending, `/retry` reports that fact and does nothing. Historical transcript replay alone never starts an automatic retry.

## Review approvals

When a tool needs permission, a modal shows what it wants to do. Read the request, then allow it once, allow the exact action for this session when offered, or deny it. Long arguments can be scrolled; `ctrl+t` opens a full-screen view when needed. Mouse buttons activate the same choices as their displayed keys.

## Change the conversation settings

- `/models` starts a new session on the selected model and keeps the conversation. **Switching models is expensive as it clears caches.**
- `/compact` asks a capable server to compact the current session's model history once. Use the bare command with no arguments while idle. It sends no prompt, keeps visible scrollback, and reports changed or already compact; a cascade summary may still cost model tokens.
- `/effort` forks the conversation onto the chosen reasoning-effort tier. Unsupported tiers are reported rather than silently applied.
- `/clear` asks the server for a distinct empty-history successor that inherits exact placement. If replacement fails after an active run or approval is cancelled, no successor is created and the source stays selected but may now be cancelled; retry `/clear` after it settles. Workspace changes are not rolled back. `/session` shows path-free active-session details.

## A short key reference

Use `?` on an empty prompt for the live help overlay. The everyday defaults are `enter` to send—while work is running, steer when the server supports it or queue a follow-up otherwise—`shift+enter` to insert a newline, `ctrl+t` to inspect details, `pgup`/`pgdn` to scroll, and `/` to open commands. See [Keybindings](./keybindings.md) for approval controls, remapping, input-editing caveats, and the complete reference.
