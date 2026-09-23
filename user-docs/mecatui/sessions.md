---
sidebar_position: 4
title: Manage sessions
sidebar_label: Sessions
description:
  Resume, browse, inspect, fork, and maintain Mecatl sessions from mecatui.
---

# Manage sessions

Sessions preserve the conversation, tool history, placement, mode, model, and
capabilities. Embedded `mecatui` stores sessions locally by default. A connected
client uses the remote server's storage.

## Resume a chat

Pass an exact session ID, or resume the newest eligible main chat:

```sh
mecatui --resume 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 --resume-latest
```

On servers that provide session activity inventory, `--resume-latest` considers
only active main chats and verifies each candidate's authoritative transcript.
It does not select drafts or unknown legacy rows automatically. Older servers
use their mixed inventory but still reject an empty authoritative transcript. If
no chat qualifies, `mecatui` starts a new one. Storage and listing failures
still return an error. You can combine either resume option with `--prompt` to
send a task after the transcript loads.

A resumed chat is the stored chat, not a copy. It keeps its model and exact
server-owned placement. See [Session continuity](/features/session-continuity.md)
for the storage and recovery behavior behind resume.

To get the active session ID, run `/session` and press `c` to copy it. On a
normal exit, `mecatui` also writes a machine-readable handoff to standard error:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
```

## Inspect the active session during a run

Run `/session` after a session is bound to open its read-only details overlay,
including while the agent is responding or waiting on a tool. It shows whether
`mecatui` is using its embedded server or a remote target. Press `c` to copy the
full session ID. Press `esc` to close the overlay and return focus to the
conversation. Opening the overlay does not cancel, pause, or steer the run.

## Browse and maintain stored sessions

Open the session inventory without creating a session:

```sh
mecatui sessions
mecatui connect 127.0.0.1:8080 sessions
```

You can also run `/sessions` from an open chat. On servers that provide session
activity inventory, known empty main sessions appear in **Drafts** as
`New — no messages`. **Chats** contains active and unknown main sessions. You
can continue a draft by selecting it or using its exact ID. Older servers hide
**Drafts** and keep all main sessions in **Chats**.

Scheduled runs, child runs, and other sessions have separate tabs. Search and
pagination help with large stores. **Delete** permanently removes a session when
the server permits it; **Close** only closes the local inventory.

Available actions depend on the session and server:

- **Continue** attaches to an eligible main chat.
- **Inspect** opens a scheduled, child, or other run without attaching to it.
- **Fork** creates a chat from an eligible main chat.
- **Copy**, **Rename**, and **Delete** manage the stored record when the server
  permits the action.

The server checks active-session and lease protections before every action.
Caller identity records ownership when enabled, but does not isolate sessions
between authenticated callers.

When the server provides storage management, the inventory can also offer
**Clean up sessions**. Cleanup is destructive and requires confirmation. The
server operator controls availability and retention.

## Name the active chat

Run `/title <TEXT>` to set a title. `mecatui` updates the label immediately,
then restores the stored title if the server rejects the change. A manually set
title uses `operator` provenance and disables automatic title generation for
that session. Run `/title` without an argument to display the title and its
provenance.

Automatic titles require the server operator to configure a compatible
`models.slots.title` binding. Without it, the server makes no title-generation
model call. Generated titles use up to three early prompts after a successful
exchange and have separately recorded `session_title` token usage.

## Start with empty model history

Run `/clear` to create a distinct, empty-history session with the same exact
placement, owner, mode, model, reasoning effort, and limits. You can clear while
idle, during a response, or while an approval is open.

Clearing a session does not undo completed workspace or tool changes. The source
session remains stored and available through `/sessions`.

During a clear, `mecatui` cancels the active run or approval and waits for it to
settle before creating the replacement. If replacement fails after cancellation,
the source remains selected but may be cancelled. Wait for the local stream to
settle, then retry `/clear`.

If the server rejects `/clear` before cancellation begins, the active run or
approval remains unchanged.

## Reduce model history

Run `/compact` without arguments while the session is idle and close to its
context limit. The server reduces the persisted history sent to the model while
keeping the session and visible scrollback. The command creates no chat turn,
but a cascade summary can use model tokens.

The command appears only when the server supports manual compaction.
See [Context windows](/features/context-windows.md) for automatic compaction,
window resolution, and operator configuration.

## Move a chat to a worktree

Run `/worktrees` to create a history-carrying successor in an eligible
server-owned worktree. The client receives safe display metadata and a
short-lived selector; it never receives the path or private environment
reference.

Selectors expire when the server restarts. `mecatui` relists expired selectors,
and a relist or fork failure leaves the source chat active. Sessions without a
filesystem cannot move to a worktree through this command.

## Diagnose a stored session

The debug command creates a separate analysis session and leaves the target
unchanged:

```sh
mecatui debug 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 debug 01JOPAQUESESSIONID

# Ask a focused initial question.
mecatui debug 01JOPAQUESESSIONID \
  --prompt "Why did the final tool call fail?"

# Add an already configured reporting server.
mecatui debug 01JOPAQUESESSIONID --debug-mcp github
```

`TARGET` can be the full ID or the short displayed handle. The handle is sanitized for terminal display and copy/paste as a debug `TARGET`; it is not an alternate server identity. If it is ambiguous, open `/session`, copy the full ID, and try again.
Use the embedded command for an embedded store and `connect ADDRESS` for the server that owns the
target. The optional `--prompt` value replaces the default diagnosis objective.

When the debugger opens, `mecatui` keeps a visible privacy disclosure in the TUI stating that the selected model will receive bounded target evidence. That evidence can contain prompts, model output, tool arguments and results, paths, and secrets. Invoking the command is the consent gesture; the default diagnostic prompt is then submitted automatically.

The analysis session has no filesystem or shell access. By default, it can only
use `InspectSession` to read bounded retained evidence. It cannot resume,
approve, cancel, steer, or otherwise change the target. `/clear`, `/sessions`,
`/models`, `/effort`, and `/worktrees` are unavailable in debug mode because
they would replace the target binding. Open `/session` and press `t` to copy the
exact target session ID.

Repeat `--debug-mcp NAME` to expose direct tools from an already configured,
server-global streaming HTTP MCP server. URLs, headers, client or inline MCP
servers, stdio servers, and resource or query meta-tools are not accepted.
Unknown, disconnected, and tool-empty server names fail. Mutating MCP tools
always require one-call approval, including in yolo mode.

Debug views report when retained evidence is incomplete. Network views expose
sanitized failure categories instead of raw errors, URLs, headers, bodies,
prompts, tool arguments, or credentials. The debug conversation is stored as a
separate durable session.

For local process diagnostics, `--perf` starts a private `admin.sock`. Its raw
metrics and pprof data are available to the operator and are not added to model
context.

## Privacy and maintenance

The embedded store is owner-only, but stores prompts, model output, tool
arguments, and results as plaintext. Protect its state directory and backups.
Use `--no-store` only when you want a non-persistent, in-memory session.

## Next steps

- [Work in the TUI](./using-the-tui.md) to steer runs and review approvals.
- [Troubleshoot session resume](./troubleshooting.md#a-session-will-not-resume).

## Related information

- [Operate local session storage](/operating/session-storage-operations.md)
  for daemon retention, backup, and restore procedures.
- [Continue a chat at startup](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#continue-a-chat-at-startup)
  for the exhaustive eligibility and overlay behavior.
