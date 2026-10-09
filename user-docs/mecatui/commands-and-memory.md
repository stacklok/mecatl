---
sidebar_position: 7
title: Use learning and memory commands
sidebar_label: Commands and memory
description:
  Use mecatui commands to review learning, reflections, dreams, and stored
  memory.
---

# Use learning and memory commands

Use these commands to connect protected workspace services and maintain stored
learning. They appear only when the connected server supports them, so an
embedded session and a remote server can expose different commands.

## Workspace service enrollment

When the server provides protected workspace services, run `/mcp-refresh` to
start or recheck the connection. This broker path keeps the existing destructive
reconnection disclosure and browser consent flow. `/tools-connect` is a
deprecated broker-only alias. Run `/tools-cancel` to cancel a pending connection.
You can continue editing the prompt while browser consent is pending. If the
server requires the connection before accepting a prompt, `mecatui` sends the
retained prompt after the connection succeeds.

On a direct MCP server, `/mcp-refresh` performs the direct refresh without a
browser consent flow. `mecatui` enables exactly one path from server
capabilities. It refuses the command when both modes, neither mode, or the
matching client collaborator are unavailable.

## Completed-session learning

|Command|Available values|Effect|
|-|-|-|
|`/learning`|**Off**, **Review**, **Auto**|Sets how the server handles learning from completed sessions.|
|`/learning-sensitivity`|**Conservative**, **Balanced**, **Eager**|Sets the evidence threshold for learning proposals.|

Both commands change pending settings and require a server restart. They do not
change a separately configured consolidation schedule.

In embedded mode, these settings live with the embedded server. In `connect`
mode, ask the remote server operator to change the settings and restart the
server. For configuration details, see
[Memory and user model](/features/agent-behavior/memory.md).

## Review and maintain memory

- `/dream` proposes maintenance for the selected project-memory or user-model
  store. Choose the target with the arrow keys or by clicking an available row;
  use the mouse wheel to browse the target list without changing the selection.
  Generating sends the selected memory's full values and descriptions to the
  configured model and uses tokens. Choosing a target does not start generation;
  confirm the spend before the request is sent. Closing Dream closes the client
  view, but does not cancel a request that the server has already received.
  Review the plan before applying it. Use the arrow keys to read a long plan.
  Confirmation applies the whole plan; dismissal changes nothing. A server
  restart discards pending plans.
- If plan generation fails, retry it. For a server-reported generation failure,
  check server diagnostics. A connection or transport failure can happen before
  the request reaches the server, so it might not have a matching diagnostic.
  If a decision result is unknown or still in progress, retry the same decision
  to retrieve its authoritative receipt instead of choosing the opposite
  decision. When a plan has expired or already reached another terminal
  decision, generate a fresh plan only after confirming the additional token
  spend.
- `/reflections` opens staged learning proposals. Review their evidence and
  approve or reject each proposal when the server allows it.
- `/reflect` submits the completed session for immediate reflection, even when
  automatic learning is off. On completion, it reports a proposal count, an
  abstention because the evidence or bounds were insufficient, or a failure. An
  abstention does not stage a proposal.

If a command is absent or unavailable, the connected server does not support it
under its current configuration and policy. For learning, consolidation, and
authorization behavior, see [Learning](/features/agent-behavior/learning.md) and
[Dreaming and memory consolidation](/features/agent-behavior/dreaming.md).
Builders configuring stores, retention, or learning should use the
[memory guide](/features/agent-behavior/memory.md).

## Next steps

- [Review learning behavior](/features/agent-behavior/learning.md).
- [Configure memory stores and retention](/features/agent-behavior/memory.md).
