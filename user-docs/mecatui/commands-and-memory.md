---
sidebar_position: 7
title: Use learning and memory commands
sidebar_label: Commands and memory
description:
  Use mecatui commands to review learning, reflections, dreams, and stored
  memory.
---

# Use learning and memory commands

These slash commands appear only when the connected server supports them.
Availability can differ between an embedded session and a remote server because
the commands use server-side storage and policy.

## Workspace service enrollment

When the connected server advertises bundled protected workspace services,
`/tools-connect` starts or rechecks the bundle and `/tools-cancel` cancels a
pending connection. Both commands are absent when the server does not support
this capability. The prompt stays usable while browser consent is pending. If
the server requires the connection before accepting a prompt, `mecatui` submits
the retained prompt after the connection succeeds.

## Completed-session learning

`/learning` cycles the server's completed-trajectory learning mode: **Off**,
**Review**, or **Auto**. `/learning-sensitivity` cycles **Conservative**,
**Balanced**, or **Eager**. They change pending settings and require a restart;
they do not alter a separately configured consolidation schedule.

In embedded mode, these settings live with the embedded server. In `connect`
mode, ask the remote server operator to change the settings and restart the
server. For configuration details, see
[Memory and knowledge](/building/what-you-get/memory.md).

## Review and maintain memory

- `/dream` proposes maintenance for the selected project-memory or user-model
  store. Review the bounded plan before applying it: generating a plan sends
  selected memory values to the configured model and spends tokens. Confirming
  applies the whole plan; dismissing changes nothing. Plans are short-lived and
  cannot be recovered after a server restart.
- `/reflections` opens staged learning proposals. Review their evidence and
  approve or reject each proposal when the server allows it.
- `/reflect` submits the current completed session for synchronous reflection.
  It can work while automatic learning is off, provided the server supports it.
  The status remains in progress until the call completes, then shows either a
  proposal count, a stable no-evidence/bounds abstention, or a typed failure; an
  abstention does not open proposal review or imply that anything was staged.

If a command is absent or unavailable, the connected server does not support it
under its current configuration and policy. For learning, consolidation, and
authorization behavior, see [Learning](/features/learning.md) and
[Dreaming and memory consolidation](/features/dreaming.md). Builders configuring
stores, retention, or learning should use the
[memory guide](/building/what-you-get/memory.md).
