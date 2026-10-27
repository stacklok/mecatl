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

A Subagent call's detail shows one-line ✓/✗/pending summaries of up to
128 retained child tool calls, without child drilldown. Each summary shows
call-side intent (such as `Read · path`), not the result body. An early
observed result shows `result received · finalizing` (or `failed · finalizing`)
until the canonical result confirms or changes its outcome. A call without a
result stays pending. The server scrubs controls and caps preview length;
previews are neither complete child calls nor guaranteed secret redaction.
Results without a matching retained call are omitted rather than shown as
new rows. A reconstructed session may have no Subagent activity preview and
notes that history may be incomplete. Press `f6` for the Agents view to
navigate Subagent children, Parallel branches, and Team members, including
their task and findings views. Each lane retains up to 128 tool calls and
12 independent message previews. Child previews with invalid UTF-8 or IDs
longer than 256 bytes are omitted; child execution and the parent session
are unaffected.

Press `f9` to reveal conversation details, including reasoning summaries,
per-turn usage, permanent error details, changed files, and individual settled
tool-call summaries in source order. Use `ctrl+t` to inspect a call's arguments
and results. Both shortcuts can be [remapped](./keybindings.md#remap-actions).

In the default conversation view, adjacent completed tool, Subagent, and Team
calls share one summary, including failures. Calls still in progress remain as
individual cards after that summary. The grouping follows their order in the
transcript, rather than the order in which work started or finished.

`f9` also temporarily reveals retained benign guardrail notices in live and
replayed conversations. These are completed, acceptable checks that allowed an
action or released a result; findings, failures, unresolved reviews, and
approvals remain visible. To keep benign notices visible, configure
[`hook_notices.show_benign`](./customization.md#show-benign-guardrail-notices).

## Start a session with an execution choice

Enter `/execution` while idle. Use `↑` and `↓` to choose **Deployment default**,
**None**, or an eligible template, then press `enter` to create a new session.
The existing session stays intact if creation fails. **None** binds no execution
filesystem or built-in Shell; the default uses the deployment's configured
execution. Template rows show operator-provided display names and exact pinned
revisions, not paths or container settings. The server checks the selected revision
and your access again at creation time; a stale or full template never falls back
to the default. The success status and `/session` details report whether the new
session actually has execution files and a built-in Shell. For an operator's
image smoke test, match both template ID and exact revision, then record the
session ID with `/session`. To check retained files, [resume that exact
session](sessions.md#resume-a-chat); `/execution` creates another session
rather than changing the existing workspace.

When catalog discovery is disabled or temporarily unavailable, the picker still
allows default and none. An empty catalog has no templates eligible for your
account. If a selected template becomes unavailable, reopen `/execution` to refresh
and retry. Debug chats stay file-less and do not offer this switch.

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
With no input pending, `mecatui` steers the run at the next safe turn boundary
when the server supports steering. Otherwise, it queues a follow-up. While
input is pending, `enter` adds to the same kind of pending input. Images and
other supported staged media stay attached, including media-only input.

To choose the timing explicitly, send one of these commands with your instruction:

|Command|Timing during an active run|
|-|-|
|`/steer Check cancellation before continuing.`|The next safe turn boundary; requires server steering support.|
|`/queue Add a regression test after this run.`|A follow-up run after this run finishes.|

You can have pending steers or queued follow-ups, but not both. To switch,
wait for the pending input to be consumed or pull it back for editing. If no
run is active, either command starts a run with its instruction.

Queued entries merge into one follow-up prompt after healthy completion. A
cancelled or failed run leaves them paused for you to resume or clear.

TUI commands stay in the client. For example, `/help` opens local help and
`/title <TITLE>` renames the session. While a run is active and the prompt is
available, `/clear` replaces the session. Unknown slash commands remain
model-facing.

To revise pending input, empty the prompt and press `↑`. This combines all
pending messages into one draft, separated by blank lines, with one `/steer`
or `/queue` prefix and their staged media. Keep the prefix to preserve the
delivery method, or change it to switch methods. If the run ends while you
edit, resubmitting starts a normal run. Pulling back a steer requests its
cancellation; a steer already consumed by the agent cannot be recalled.
Press `ctrl+u` to clear the unsent draft and its attachments.

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

In `/schedule`, each schedule has a run-history view and separate configuration
and state details. Returning from either view preserves your selection and
scroll position. Opening a run's transcript is read-only and keeps your active
chat and draft unchanged. Leaving that transcript takes you to the
[Sessions browser](sessions.md), rather than back to the schedule. Closing the
schedule browser while an operation is pending does not undo that operation.

## Create a schedule

The create form creates an independent schedule. It uses the deployment's
default model and does not inherit the open chat's model or receive results in
that chat. A read-only schedule can use the deployment's configured plan model.
Unchecked **Read-only** uses the server's default session permission mode.
The deployment's permission policy determines which tool calls can run unattended.

The timing field accepts the following finite phrases, with an optional leading
`run `. It also accepts a raw five-field cron expression or an `@` macro that
the server supports. The form does not interpret arbitrary natural-language
timing requests.

|Input|Meaning|
|-|-|
|`every N minutes`|Recurring cron interval, where `N` is 1 through 59.|
|`every N hours`|Recurring cron interval, where `N` is 1 through 23.|
|`every hour`|Hourly recurring cron.|
|`daily at H[:MM] [am/pm]`, `every day at ...`, or `everyday at ...`|Daily recurring cron.|
|`every weekday at H[:MM] [am/pm]`|Monday through Friday recurring cron.|
|`every <weekday> at H[:MM] [am/pm]`|Recurring cron for Monday through Sunday. Sunday compiles to cron weekday `0`.|
|`next <weekday> H[:MM] [am/pm]`|One-shot at the next matching weekday and time.|
|`in N minute(s)`, `in N hour(s)`, `in N day(s)`, or `in N week(s)`|One-shot after the specified elapsed duration.|
|`tomorrow at H[:MM] [am/pm]`|One-shot on the next calendar day at that time.|

Use hours from 1 through 12 with `am` or `pm`, or 0 through 23 without one;
minutes range from 0 through 59. The optional `run ` prefix applies only to the
listed phrases.

The form starts with **UTC** selected. Search by city, or enter an exact IANA
name such as `America/Los_Angeles`. Calendar one-shot phrases, such as
`tomorrow at 9am`, use the chosen timezone to calculate an absolute timestamp.
Do not put `TZ=` or `CRON_TZ=` in the cron expression. Recurring cron schedules
remain in that local wall-clock timezone across daylight-saving time changes
instead of converting the cron expression to a fixed UTC offset.

The displayed next occurrence is an estimate. The server's returned
`NextFireAt` after creation is authoritative. Relative one-shot phrases, such
as `in 2 hours`, are calculated again when you choose **Create**, so they use a
fresh current time. The form validates its input before sending it, but server
cadence, governance, and other restrictions still apply. It retains the form
values when creation fails so you can correct them.

To ask conversationally, use ordinary chat. The agent uses `Schedule` to
create or manage schedules and `ScheduleQuery` to list or inspect them. A
schedule created through chat can deliver results to its originating
conversation when the deployment supports delivery. A schedule created in the
form keeps its results in its separate run history. The `/schedule` browser
does not edit existing schedule definitions; use chat to replace one or use the
API to change one.

## Monitor delegated work

Press `f6` to open the agents overlay for Subagents, Parallel runs, and Teams.
The footer shows running and completed counts after delegated work begins. Use
the overlay to inspect bounded activity previews; `/team` opens the same overlay
on the Teams tab.

For prompts that start investigations, isolated implementations, and team
reviews, see [Work with delegated agents](./delegated-agents.md). See
[Subagents, teams, and parallel](/features/agent-behavior/subagents-and-teams.md#watch-a-delegation-in-mecatui)
for delegation behavior and the information available in `mecatui`.

## Change conversation settings

Press `shift+tab` to cycle through every permission mode. A mode up to the
server's ceiling applies immediately. A mode above it keeps the session in its
current mode and leaves your draft usable, and the footer says how to relaunch
with a higher `--permission-mode`. See
[Change the mode after launch](/features/security-and-execution/permissions-and-posture.md#change-the-mode-after-launch).

|Command|Result|
|-|-|
|`/models`|Starts a session on the selected model and keeps the conversation. Switching models clears model caches and can increase cost.|
|`/effort`|Forks the conversation onto the selected reasoning-effort tier. The server reports unsupported tiers.|
|`/compact`|Reduces model history while keeping the session and visible scrollback. Run it without arguments while idle. Creating a cascade summary can use model tokens.|
|`/clear`|Creates an empty-history session with the same placement. It does not roll back workspace changes.|
|`/session`|Shows path-free details for the active session.|
|`/posture`|Shows the server's operator posture, the independent effective checker state, and the subagent ask reviewer state when the server reports it. Off includes setup guidance; unavailable posture status is unknown. See [Permissions and posture](/features/security-and-execution/permissions-and-posture.md).|

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
