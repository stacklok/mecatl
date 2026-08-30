---
sidebar_position: 4
title: Sessions
---

# Manage sessions

Sessions preserve a conversation, its tool history, workspace, mode, model, and capabilities. Embedded mecatui stores them locally by default; a connected server uses that server's storage.

## Resume a chat

Pass an exact session ID, or let mecatui choose the newest eligible main chat:

```sh
bin/mecatui --resume 01JOPAQUESESSIONID
bin/mecatui connect 127.0.0.1:8080 --resume-latest
```

`--resume-latest` continues the newest eligible chat and, when none exists, starts a fresh chat instead of failing — the "continue where I left off, otherwise begin" launch. (A genuine storage/list failure still surfaces rather than being masked as a new chat.)

A resumed chat is the stored chat, not a copy. It keeps its stored workspace and model. You can combine a resume selector with `--prompt` to send one next task after the transcript loads.

While a chat is open, `/session` shows its full ID and `c` copies it. On a normal exit, mecatui also writes a machine-readable handoff to stderr:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
```

Save that value and use it with `--resume`.

## Diagnose a stored session

A debugger is intentionally different from continuing a chat:

```sh
mecatui debug 01JOPAQUESESSIONID
mecatui connect 127.0.0.1:8080 debug 01JOPAQUESESSIONID
```

It creates a separate durable no-filesystem analysis session, warns that the target's
prompts, outputs, tool arguments/results, paths, and secrets may be sent to the selected
model, and submits a default diagnosis prompt. Invocation is your consent to that
disclosure. The target stays unchanged and unleased; the analysis has one target-bound
read-only tool and cannot switch targets. Its transcript evidence is authoritative;
activity/performance evidence is optional and may be incomplete. The persistent DEBUG
rail/title helps prevent confusing the debugger with the original chat.

## Start fresh with `/clear`

Use `/clear` when you want a fresh session and empty context while staying in the
current workspace and model. The old conversation remains stored and discoverable
through `/sessions`; mecatui releases its old runtime resources only on a best-effort
basis after the replacement session is ready.

## Reduce model history with `/compact`

If a long chat is close to its context limit, use bare `/compact` while the session
is idle. The command keeps the same session and visible scrollback but asks the server
to reduce the persisted history sent to the model. It creates no chat turn and reports
whether anything changed. The command appears only when the connected server advertises
manual compaction; older servers hide it. A cascade summary can still use model tokens.

## Browse, continue, inspect, or fork

Open the inventory without creating a session:

```sh
bin/mecatui sessions
bin/mecatui connect 127.0.0.1:8080 sessions
```

Choose a main chat to **Continue**, or open scheduled, child, and other runs to **Inspect** their authoritative transcript without attaching to them. Eligible main chats can also be **Forked** into a new chat. The inventory can copy an ID, rename a session, and—when the server permits it—delete it. Actions are checked again by the server, so an old inventory row cannot bypass active-session or lease protections. Caller identity, where enabled, records ownership but does not isolate sessions between authenticated callers.

Use `/sessions` from an open chat for the same inventory. It has separate tabs for chats, scheduled runs, child runs, and other rows; search and pagination keep large inventories usable.

## Privacy and maintenance

The embedded store is owner-only, but its conversation, model output, tool arguments, and results are plaintext. Keep its state directory and backups private. Use `--no-store` only when you deliberately want an in-memory, non-persistent session.

When the connected server advertises storage management, the inventory may offer **Optimize storage** (non-destructive) and **Clean up sessions** (destructive, confirmation required). Availability and retention rules belong to the server operator. For daemon retention, backups, or restore procedures, see [Operate local session storage](/building/deployment/session-storage-operations.md).

For resume eligibility and the full overlay behavior, see [`docs/tui.md`](https://github.com/stacklok/mecatl/blob/main/docs/tui.md#continue-a-chat-at-startup). If continuation fails, start with [Troubleshooting](./troubleshooting.md#a-session-will-not-resume).
