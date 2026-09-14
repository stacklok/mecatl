---
sidebar_position: 5
title: Work in the TUI
sidebar_label: Use the TUI
description:
  Work in the mecatui terminal interface, steer runs, review tools, and approve
  actions.
---

# Work in the TUI

Use the conversation view to follow the response, inspect tool calls, and steer
the agent without waiting for the current run to finish. Assistant text streams
as it arrives. Tool calls appear as compact cards; Edit and Write cards include
their diff.

Focus a tool card and press `ctrl+t` to view its complete arguments and output.
Press `ctrl+t` again to return to the preview.

## Keep working while a run is active

Type your next instruction while the agent is running, then press `enter`.
`mecatui` steers the run at the next safe turn boundary when the server supports
steering. Otherwise, it queues the instruction as a follow-up. Images and other
supported staged media stay attached, including media-only input. Multiple
queued lines become one prompt.

Bare TUI commands stay in the client. For example, `/help` opens local help and
`/clear` can replace the session during a run or approval. Unknown slash
commands, workspace commands, and built-in commands with arguments go to the
model.

To revise queued input, empty the prompt and press `↑`. This restores the
pending steer or queued follow-up with its staged media. Press `ctrl+u` to clear
the unsent draft and its attachments.

Pressing `esc` clears an active selection first. During a run, it cancels the
run but preserves your draft and queued input. When the session is idle with a
paused queue, it clears the queue and preserves the draft.

## Add files and images

Type `@` to find a file in the workspace, then select it with `enter` or `tab`.
`mecatui` inserts text files into the prompt and attaches supported image or
audio files as media. The selected model must support the media type; otherwise,
the client keeps the draft and reports the unsupported attachment.

Press `ctrl+v` to paste an image from the clipboard. If the clipboard does not
contain an image, `ctrl+v` pastes its text. Large text pastes appear as compact
placeholders in the editor and expand when you send the prompt.

See [Multimodal input](/features/multimodal-input.md) for model capability and
validation behavior.

## When a model stream fails

When the server reports a `retryable + precommit` failure, `mecatui` retries the
model step once without adding a prompt or removing queued messages. If the
automatic retry fails, use `/retry`. The same command retries a
`retryable + visible` failure, including one reopened from storage.

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

When workspace-service enrollment or an MCP tool opens a browser, complete the
authorization and return to the TUI. `mecatui` checks the pending request
automatically.

Each workspace-service request has a 30-second deadline. After a timeout, the
server's state is uncertain, so `mecatui` stops checking. Run `/clear`, then run
`/tools-connect` in the replacement session as the displayed message directs.

You can still cancel a pending MCP authorization from its card. Presentation
links are opened or copied only for that interaction and are not retained in the
conversation.

## Browse available capabilities

Open the slash-command palette with `/`. `mecatui` shows only the panels and
commands supported by the connected server.

|Task|Open in `mecatui`|More information|
|-|-|-|
|Browse MCP servers, resources, and prompts|`/mcp`; press `f8` to open MCP prompts directly|[MCP client](/building/what-you-get/mcp-client.md)|
|Inspect named agent definitions|`/agents`|[Named agents](/features/named-agents.md)|
|Inspect available skills and the active soul|`/skills` and `/soul`|[Skills, commands, and soul](/features/skills-commands-and-soul.md)|
|Inspect the user model|`/usermodel`|[Memory](/building/what-you-get/memory.md)|
|Manage recurring and one-shot tasks|`/schedule`|[Scheduled tasks](/features/scheduled-tasks.md)|
|Review learning and maintain memory|`/learning`, `/reflections`, `/reflect`, and `/dream`|[Use learning and memory commands](./commands-and-memory.md)|

The palette also includes workspace-defined slash commands. See
[Skills, commands, and soul](/features/skills-commands-and-soul.md) for how the
server discovers and expands them.

## Monitor delegated work

Press `f6` to open the agents overlay for Subagents, Parallel runs, and Teams.
The footer shows running and completed counts after delegated work begins. Use
the overlay to inspect bounded activity previews; `/team` opens the same overlay
on the Teams tab.

See
[Subagents, teams, and parallel](/building/what-you-get/subagents-teams-parallel.md#watch-a-delegation-in-mecatui)
for delegation behavior and the information available in `mecatui`.

## Change conversation settings

Press `shift+tab` to switch the active permission mode. See
[Choose a permission mode](/features/permissions-and-posture.md#choose-a-permission-mode)
for the available modes and their behavior.

|Command|Result|
|-|-|
|`/models`|Starts a session on the selected model and keeps the conversation. Switching models clears model caches and can increase cost.|
|`/effort`|Forks the conversation onto the selected reasoning-effort tier. The server reports unsupported tiers.|
|`/compact`|Reduces model history while keeping the session and visible scrollback. Run it without arguments while idle. Creating a cascade summary can use model tokens.|
|`/clear`|Creates an empty-history session with the same placement. It does not roll back workspace changes.|
|`/session`|Shows path-free details for the active session.|
|`/posture`|Shows the server's operator posture and active defenses. See [Permissions and posture](/features/permissions-and-posture.md).|

If `/clear` cancels an active run or approval and then fails to create the
replacement, the original session remains selected and may be cancelled. Wait
for it to settle, then retry `/clear`.

## A short key reference

Use `?` on an empty prompt for the live help overlay. The everyday defaults are
`enter` to send or steer, `shift+enter` to insert a newline, `ctrl+t` to inspect
details, `pgup`/`pgdn` to scroll, and `/` to open commands. If the server does
not support steering, `enter` queues a follow-up while a run is active. See
[Keybindings](./keybindings.md) for approval controls, remapping, and the
complete reference.

## Next steps

- [Manage sessions](./sessions.md) to resume, inspect, fork, or clear a chat.
- [Use learning and memory commands](./commands-and-memory.md) when the server
  provides learning features.
