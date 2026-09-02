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
# Exact ID or displayed short handle: both use the same TARGET grammar.
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
   `mecatui: final-session-id=...` line printed when its TUI exits as `TARGET`. You can instead
   pass the displayed 12-column short handle unchanged: safe `[A-Za-z0-9._-]` bytes are literal
   except that a leading `-` is encoded as `%2D`; other UTF-8 bytes are uppercase `%HH` atoms,
   and only complete atoms that fit are shown. It has no leading `#`. A syntactically valid short
   target consults the complete visible inventory. Exact full-ID equality wins; otherwise one
   unique projected match resolves. If projections are ambiguous, open `/session`, copy the full
   exact ID, and pass it as `TARGET` through the same command. If inventory fails or no handle
   matches, mecatui sends `TARGET` unchanged and reports the ordinary server exact-ID result.
2. Run `mecatui debug TARGET` against the same embedded store, or use
   `mecatui connect ADDRESS debug TARGET` against the server that owns the target.
3. mecatui prints a privacy disclosure before entering the alternate screen.
   Running the command is consent to send bounded target evidence—which may
   include prompts, model output, tool arguments/results, paths, and secrets—to
   the selected model.
4. The server authorizes the target and creates a **different**, durable,
   no-filesystem analysis session. The normal padded header shows amber/bold
   `DEBUG target <handle>` after `mecatui`, and the terminal title carries the same
   handle. `/session` shows the safely quoted exact target ID and copies it with `t`.
5. The debugger submits one first user turn ordered as your diagnosis objective, the required
   status/transcript/pagination workflow, the expected report sections, and finally a delimited
   sanitized current-debugger client/server runtime block. `--prompt` replaces only the
   objective. Runtime context is compatibility/transport context, not target evidence; a
   safely classified remote lookup failure does not block launch or reveal its raw error.
6. Ask follow-up questions normally. The debugger can inspect bounded status,
   transcript, activity, performance, network, related, delegation, history, and manifest
   views for the target and retained related handles. Related rows distinguish retained
   children from pruned tombstones without accepting arbitrary session IDs. History includes
   compaction archives; status separates latest-run, cumulative snapshot, and lifetime counters.
   Event-derived views report availability/completeness. Network includes sanitized failed/interesting
   attempt decisions and classes, never raw errors, URLs, headers, bodies, prompts, tool
   arguments, or credentials; successful-attempt and per-phase DNS/TCP/TLS timing are not measured.
7. Quit normally when finished. The target remains unchanged and unleased; the
   debug conversation is stored separately.

The debugger cannot switch targets, browse the filesystem, run a shell, or act
on the target. By default it has only `InspectSession`. Repeatable `--debug-mcp NAME`
selects direct tools from already-configured server-global streaming-HTTP MCP servers;
unknown/disconnected/tool-empty names fail, and no URL, headers, inline/client MCP,
stdio, or resource/query meta-tools are accepted. Mutating MCP tools always ask for a
one-call interactive approval (including under yolo); Deny still wins, headless mutation
is refused, and Allow Always applies only to the current call and is not learned. Ask the debugger to draft an
issue/message first, then explicitly request publication in a later prompt and approve the
resulting call once. A second mutation asks again.

It never resumes, approves, cancels, steers, or mutates the
original session. `/clear`, `/sessions`, `/models`, `/effort`, and `/worktrees`
are hidden in debug mode because they could replace the analysis binding.

For local process-level investigation, adding `--perf` starts an owner-private,
per-instance `admin.sock`. That raw metrics/pprof surface is for the human
operator and is **not** placed in model context; the debugger's performance view
is the bounded event-derived projection.

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
