---
sidebar_position: 5
title: Work in the TUI
sidebar_label: Use the TUI
description:
  Work in the mecatui terminal interface, steer runs, review tools, and approve
  actions.
---

# Work in the TUI

Press `ctrl+t` to open `/toolcalls` and inspect top-level calls during a run.
Use `↑`/`↓` to choose a row, then `enter` or click it to open its detail;
press `esc` to return to the list. The inspector also works when revisiting a
session transcript. Running Edit and Write cards show their request diffs inline;
settled calls keep their full arguments and results in the inspector. When
`mecatui` receives a result before its final update, it keeps the card open
and shows `result received · finalizing`.

A Subagent call's detail includes bounded previews of currently retained child
tool activity. A pending child call has no safely associated result yet. The
server scrubs controls and caps preview length, but these previews are not
complete child calls or guaranteed secret redaction. A reconstructed session
can show a Subagent call without activity; its detail notes that history may
be incomplete. Press `f6` for the Agents view to navigate Subagent children,
Parallel branches, and Team members, including their task and findings views.

Press `f9` to reveal conversation details, including reasoning summaries,
per-turn usage, permanent error details, and changed files. Tool results stay in
`/toolcalls`. Both shortcuts can be [remapped](./keybindings.md#remap-actions).

`f9` also temporarily reveals retained benign guardrail notices in live and
replayed conversations. These are completed, acceptable checks that allowed an
action or released a result; findings, failures, unresolved reviews, and
approvals remain visible. To keep benign notices visible, configure
[`hook_notices.show_benign`](./customization.md#show-benign-guardrail-notices).

## Attach a local file

Type `@` to find a file on the machine running `mecatui`. Use `↑` or `↓` to
select a result, then `tab` or `enter` to insert its path. While completion is
open, `enter` selects a path and `esc` dismisses the list.

|Mention prefix|Search location|
|-|-|
|`@path` or `@./path`|The client workspace.|
|`@../path`|A parent of the client workspace; repeated `../` components reach higher parents.|
|`@~/path`|The client process's home directory, including when no client workspace is configured.|

Completion preserves the prefix, lists files only, excludes hidden files and
directories, and limits the number of results. Without a client workspace,
non-home completion has no current-directory fallback. After `~/`, leading `./`
and `../` components are also supported.

When you send the prompt, `mecatui` reads mentioned regular files and uploads
their content. Text is inlined; supported media becomes an attachment. The file
stays on the client machine. Its path does not become a server workspace path,
and uploading it grants no filesystem or command access. This also applies to
remote, containerized, and no-filesystem sessions. Check what you are sharing,
especially when selecting home-directory files.

Only a literal leading `~/` expands to the client home directory. `~user` is an
ordinary workspace-relative path when that file exists. Quoted paths, embedded
tildes, and paths containing whitespace remain prose rather than a single
mention. If the client cannot determine its home directory, `@~/…` also remains
prose.

## Keep working while a run is active

Type your next instruction while the agent is running, then press `enter`.
`mecatui` steers the run at the next safe turn boundary when the server supports
steering. Otherwise, it queues the instruction as a follow-up. Images and other
supported staged media stay attached, including media-only input. Multiple
queued lines become one prompt.

Bare TUI commands stay in the client. For example, `/help` opens local help.
While a run is active and the prompt is available, `/clear` replaces the
session. Unknown slash commands, workspace commands, and built-in commands with
arguments go to the model.

To revise queued input, empty the prompt and press `↑`. This restores the
pending steer or queued follow-up with its staged media. Press `ctrl+u` to clear
the unsent draft and its attachments.

Pressing `esc` clears an active selection first. During a run, it cancels the
run but preserves your draft and queued input. When the session is idle with a
paused queue, it clears the queue and preserves the draft.

## Add files and images

File mentions can attach supported image or audio files as media. The selected
model must support the media type; otherwise, the client keeps the draft and
reports the unsupported attachment.

Press `ctrl+v` to paste an image from the clipboard. If the clipboard does not
contain an image, `ctrl+v` pastes its text. Large text pastes appear as compact
placeholders in the editor and expand when you send the prompt.

See [Multimodal input](/features/sessions/multimodal-input.md) for model
capability and validation behavior.

## When a model stream fails

The server recovers transient failures before model output becomes visible,
within its configured [recovery limits](/features/sessions/choose-models.md#a-provider-error-ended-a-model-step).
If recovery ends in a terminal failure, use `/retry`; `mecatui` does not start
another run automatically. The same command retries a `retryable + visible`
failure, including one reopened from storage.

`/retry` preserves the prompt textarea and queued prompts. For visible failures,
scrollback marks the failed partial output as superseded. If no eligible failure
is pending, the command reports that fact and makes no changes.

## Review approvals

During a permission request, the active `Toolcalls` binding (`ctrl+t` by
default) opens that request's details instead of `/toolcalls`. See
[Approve or deny a request](./keybindings.md#approve-or-deny-a-request) for
approval keys and the scope of "allow always".

## Get editor notifications

Run `mecatui` in a terminal provided by a supported editor and the editor can
tell you when the agent needs an approval and when a run ends. You configure
nothing in `mecatui` for this.

`mecatui` reports four lifecycle points to the editor's agent hook: the start of
a turn, an approval request from the main session, the operator's answer to that
request, and the run's terminal state. The answer returns the editor's status to
working immediately instead of leaving the approval notification active until
the run ends. The editor decides how to present these reports. Approval requests
raised by a subagent stay out of the report, so delegated work does not compete
with the main session for your attention.

Superset is the supported editor. The reports reach it only once Superset
registers `mecatl` as a hook-emitting agent. Until that registration ships,
`mecatui` sends nothing and no editor notifications appear. In any other
terminal, `mecatui` skips the report and its behavior is unchanged.

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
|Browse MCP servers, resources, and prompts|`/mcp`; press `f8` to open MCP prompts directly|[MCP client](/features/security-and-execution/mcp-client.md)|
|Refresh direct MCP tools or broker workspace services|`/mcp-refresh`|[Use learning and memory commands](./commands-and-memory.md#workspace-service-enrollment)|
|Inspect named agent definitions|`/agents`|[Named agents](/features/agent-behavior/named-agents.md)|
|Inspect available skills and the active soul|`/skills` and `/soul`|[Skills, commands, and soul](/features/agent-behavior/skills-commands-and-soul.md)|
|Inspect saved memory|`/memory`|[Memory](/features/agent-behavior/memory.md)|
|Manage recurring and one-shot tasks|`/schedule`|[Scheduled tasks](/features/sessions/scheduled-tasks.md)|
|Review learning and maintain memory|`/learning`, `/reflections`, `/reflect`, and `/dream`|[Use learning and memory commands](./commands-and-memory.md)|

The palette also includes workspace-defined slash commands. See
[Skills, commands, and soul](/features/agent-behavior/skills-commands-and-soul.md)
for how the server discovers and expands them.

## Monitor delegated work

Press `f6` to open the agents overlay for Subagents, Parallel runs, and Teams.
The footer shows running and completed counts after delegated work begins. Use
the overlay to inspect bounded activity previews; `/team` opens the same overlay
on the Teams tab.

See
[Subagents, teams, and parallel](/features/agent-behavior/subagents-and-teams.md#watch-a-delegation-in-mecatui)
for delegation behavior and the information available in `mecatui`.

## Change conversation settings

Press `shift+tab` to switch the active permission mode. See
[Choose a permission mode](/features/security-and-execution/permissions-and-posture.md#choose-a-permission-mode)
for the available modes and their behavior.

|Command|Result|
|-|-|
|`/models`|Starts a session on the selected model and keeps the conversation. Switching models clears model caches and can increase cost.|
|`/effort`|Forks the conversation onto the selected reasoning-effort tier. The server reports unsupported tiers.|
|`/compact`|Reduces model history while keeping the session and visible scrollback. Run it without arguments while idle. Creating a cascade summary can use model tokens.|
|`/clear`|Creates an empty-history session with the same placement. It does not roll back workspace changes.|
|`/session`|Shows path-free details for the active session.|
|`/posture`|Shows the server's operator posture and the independent effective checker state. Off includes setup guidance; unavailable or older-server status is unknown. See [Permissions and posture](/features/security-and-execution/permissions-and-posture.md).|

If `/clear` cancels an active run or approval and then fails to create the
replacement, the original session remains selected and may be cancelled. Wait
for it to settle, then retry `/clear`.

## A short key reference

Use `?` on an empty prompt for the live help overlay. The everyday defaults are
`enter` to send or steer, `shift+enter` or `ctrl+j` to insert a newline,
`ctrl+t` to inspect tool calls, `f9` to reveal conversation details,
`pgup`/`pgdn` to scroll, and `/` to open commands.
If the server does not support steering, `enter` queues a follow-up while a run
is active. See [Keybindings](./keybindings.md) for approval controls, remapping,
and the complete reference.

## Inspect the broker catalogue

On a broker-only `mecak8s` connection, `/mcp` shows the current session's
enrollment state, connector names, catalogue state, and tool counts. The panel
requires a verified authenticated caller who owns the session. Opening it or
refreshing its display reads local state; it does not probe upstream services,
refresh credentials, or enroll connectors.

When the session is idle and the operator has enabled enrollment, `/mcp` (or
`ctrl+o`) offers **Connect tools**. Use it for a new session or to reconnect
after completed turns, a previous connection, a failed attempt, or a broker
restart. After a restart, saved connector names do not prove connectivity: the
panel reports broker state unavailable and protected tools remain unavailable
until you explicitly refresh that session.

### Reconnect tools

Run `/mcp-refresh` to start broker setup. Reconnection withdraws the existing
broker tools for the whole bundle. Cancellation or failure leaves them
unavailable. Setup shows **Setup in progress** and **Cancel setup**; prompts and
a second refresh are blocked until it settles. A running session or one awaiting
approval cannot refresh. The browser authorization flow continues without a
reopen-browser action.

`/tools-connect` is a deprecated broker-only alias for `/mcp-refresh`.
`/tools-cancel` cancels pending setup.

ToolHive owns upstream OAuth authorization, callback state, credentials, token
refresh, and grant reuse. Mecatl handles enrollment through opaque references.
The operator configures
[MCP network access and credentials](/operating/mecak8s/identity-and-client-access.md#maintain-mcp-network-access-and-credentials).

Broker-only and direct-MCP configurations are mutually exclusive. Broker-only
sessions do not expose direct MCP resources, prompts, or groups. The catalogue
is an inventory, not an upstream health check.

## Next steps

- [Manage sessions](./sessions.md) to resume, inspect, fork, or clear a chat.
- [Use learning and memory commands](./commands-and-memory.md) when the server
  provides learning features.
