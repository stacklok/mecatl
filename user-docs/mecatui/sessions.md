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

A resumed chat is the stored chat, not a copy. It keeps its stored workspace and model. You can combine a resume selector with `--prompt` to send one next task after the transcript loads.

While a chat is open, `/session` shows its full ID and `c` copies it. On a normal exit, mecatui also writes a machine-readable handoff to stderr:

```text
mecatui: final-session-id="01JOPAQUESESSIONID"
```

Save that value and use it with `--resume`.

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
