---
sidebar_position: 4
title: Manage sessions
sidebar_label: Sessions
description: Resume, browse, inspect, fork, and maintain Mecatl sessions from mecatui.
---

# Manage sessions

Sessions preserve a conversation, its tool history, workspace, mode, model, and capabilities. Embedded mecatui stores them locally by default; a connected server uses that server's storage.

## Resume a chat

Pass an exact session ID, or let mecatui choose the newest eligible main chat:

```sh
mecatui --resume 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 --resume-latest
```

`--resume-latest` continues the newest eligible chat. If none exists, it starts a
new chat. Storage and listing failures still produce an error.

A resumed chat is the stored chat, not a copy. It keeps its stored workspace and model. You can combine a resume selector with `--prompt` to send one next task after the transcript loads.

While a chat is open, `/session` shows its full ID and `c` copies it. On a normal exit, mecatui also writes a machine-readable handoff to stderr:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
```

Save that value and use it with `--resume`.

## Diagnose a stored session

Debugging a session creates a separate analysis chat instead of continuing the
target chat:

```sh
# TARGET can be an exact ID or a displayed short handle.
mecatui debug 01JOPAQUESES
mecatui connect 127.0.0.1:8080 debug 01JOPAQUESES
mecatui debug 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 debug 01JOPAQUESESSIONID

# Add one or more already-configured global reporting servers by name.
mecatui debug 01JOPAQUESES --debug-mcp github
mecatui connect 127.0.0.1:8080 debug 01JOPAQUESES --debug-mcp github

# Replace the automatic diagnosis question with a focused one.
mecatui debug 01JOPAQUESESSIONID \
  --prompt "Why did the final tool call fail?"
```

### What happens

1. Pass the full target ID from `/session`, `/sessions`, or the
   `mecatui: final-session-id=...` line printed when the TUI exits. You can also
   pass the displayed 12-column short handle. Exact full-ID equality wins;
   otherwise the handle must resolve to one session. If a short handle is
   ambiguous, open `/session`, copy the full ID, and use it as `TARGET`.
2. Run `mecatui debug TARGET` against the same embedded store, or use
   `mecatui connect ADDRESS debug TARGET` against the server that owns the target.
3. `mecatui` prints a privacy disclosure before entering the alternate screen.
   Running the command consents to sending bounded target evidence to the selected
   model. This evidence can include prompts, model output, tool arguments and
   results, paths, and secrets.
4. The server authorizes the target and creates a **different**, durable,
   no-filesystem analysis session. The normal padded header shows amber/bold
   `DEBUG target <handle>` after `mecatui`, and the terminal title carries the same
   handle. `/session` shows the safely quoted exact target ID and copies it with `t`.
5. The debugger submits an initial diagnosis request with a bounded, sanitized
   view of the current client and server runtime. `--prompt` replaces the default
   diagnosis objective.
6. Ask follow-up questions normally. The debugger can inspect bounded status,
   transcript, activity, performance, network, related, delegation, history, and manifest
   views for the target and retained related sessions. The views report when
   evidence is incomplete or unavailable. Network views contain sanitized failure
   categories rather than raw errors, URLs, headers, bodies, prompts, tool
   arguments, or credentials.
7. Quit normally when finished. The target remains unchanged and unleased; the
   debug conversation is stored separately.

The debugger cannot switch targets, browse the filesystem, run a shell, or act
on the target. By default it has only `InspectSession`. Repeatable `--debug-mcp NAME`
selects direct tools from already-configured server-global streaming-HTTP MCP servers;
unknown, disconnected, or tool-empty names fail. URLs, headers, inline or client MCP
servers, stdio servers, and resource or query meta-tools are not accepted. Mutating
MCP tools always require one-call interactive approval, including under yolo mode.
Ask the debugger to draft an issue or message first. Request publication in a later
prompt, then approve the call. Each later mutation requires another approval.

It never resumes, approves, cancels, steers, or mutates the
original session. `/clear`, `/sessions`, `/models`, `/effort`, and `/worktrees`
are hidden in debug mode because they could replace the analysis binding.

For local process-level investigation, `--perf` starts a private `admin.sock` for
the current instance. Its raw metrics and pprof data are available to the operator
but are not placed in model context.

## Name the active chat

Use `/title <text>` while an ordinary chat is open to set its title. `mecatui`
updates the label immediately while the server validates and saves it. If the
request is rejected, `mecatui` restores the stored title. A manual title stops
automatic title generation permanently and records `operator` provenance. Use
bare `/title` to display the title and whether it was generated or set by an
operator.

Automatic titles are optional. An operator enables them with an explicit compatible
`models.slots.title` binding; without that slot no extra model call occurs. The
server collects up to three early genuine prompts and may generate a
concise title after a successful chat exchange. It does not delay or rewrite the
chat, and its separately recorded `session_title` token usage does not consume the
chat's run budget. Generated-title updates normally appear live in an open `mecatui`;
a reconnect or reopened session refetches the authoritative snapshot, so a missed
live update is corrected.

## Start fresh with `/clear`

Use `/clear` when you want empty context without changing placement. You can issue it
while idle, while a response is streaming, or while an approval is open; you do not need to
press Esc first. It calls the server's `ClearSession` operation, which cancels the active run
or durable approval, waits for that exact lifecycle to settle, then creates a distinct
empty-history successor that inherits the source's exact server-owned placement, owner, mode,
model/effort, and limits. Previously completed workspace and tool mutations remain in place;
clear resets conversation history, not the workspace.

The source conversation remains stored and discoverable through `/sessions`. `mecatui` keeps
that source and transcript selected while the handoff is pending and blocks prompts,
approvals, and duplicate clears against it. It switches only after the correlated successor
creation succeeds. Cancelling an active run or approval is irreversible: if replacement
creation then fails, no successor is created and the source stays selected, but it may already
be shown as cancelled. Retry `/clear` once its local stream has settled. A failure detected
before cancellation, such as an invalid worktree selection, leaves an awaiting source and its
approval unchanged. Clear never rolls back workspace mutations.

## Reduce model history with `/compact`

If a long chat is close to its context limit, use bare `/compact` while the session
is idle. The command keeps the same session and visible scrollback but asks the server
to reduce the persisted history sent to the model. It creates no chat turn and reports
whether anything changed. The command appears only when the connected server advertises
manual compaction; older servers hide it. A cascade summary can still use model tokens.

## Browse, continue, inspect, or fork

Open the inventory without creating a session:

```sh
mecatui sessions
mecatui connect 127.0.0.1:8080 sessions
```

Choose a main chat to **Continue**, or open scheduled, child, and other runs to
**Inspect** their transcript without attaching to them. Eligible main chats can
also be **Forked** into a new chat. The inventory can copy an ID, rename a session,
and delete it when the server permits. The server rechecks every action against
active-session and lease protections. Caller identity records ownership when
enabled, but it does not isolate sessions between authenticated callers.

Use `/sessions` from an open chat for the same inventory. It has separate tabs for chats, scheduled runs, child runs, and other rows; search and pagination keep large inventories usable.

Use `/worktrees` to move a history-carrying successor to an eligible server-owned
worktree. The client sends the source session ID, receives only safe display metadata plus
a short-lived opaque selector, and passes that selector to `ForkSession`; no path or exact
private environment ref crosses the API. Selectors expire on server restart, so mecatui
relists. A stale selector, relist failure, or fork failure leaves the source chat active.
No-FS sessions cannot upgrade through this surface.

## Privacy and maintenance

The embedded store is owner-only, but its conversation, model output, tool arguments, and results are plaintext. Keep its state directory and backups private. Use `--no-store` only when you deliberately want an in-memory, non-persistent session.

When the connected server advertises storage management, the inventory may offer **Optimize storage** (non-destructive) and **Clean up sessions** (destructive, confirmation required). Availability and retention rules belong to the server operator. For daemon retention, backups, or restore procedures, see [Operate local session storage](/building/deployment/session-storage-operations.md).

For resume eligibility and the full overlay behavior, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#continue-a-chat-at-startup). If continuation fails, start with [Troubleshooting](./troubleshooting.md#a-session-will-not-resume).
