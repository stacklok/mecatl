---
sidebar_position: 1
title: Features
description: Explore Mecatl features for sessions, models, permissions, tools, and project guidance.
---

# Features

This section documents Mecatl capabilities by the task they help you complete.
Most features belong to the shared agent/server core; deployment pages explain
how `mecated`, `mecak8s`, and mecatui expose that core in different operating
environments.

## Get oriented

- [Capability and deployment matrix](./capability-matrix.md) explains the
  operational differences among the server and terminal deployment surfaces.
- [Use mecatui](./use-mecatui.md) covers the terminal UI, keymaps, panels, and
  interactive workflows.

## Run and maintain sessions

- [Start and resume sessions](./start-and-resume-sessions.md) covers creating,
  selecting, and continuing sessions.
- [Choose models and providers](./choose-models.md) covers provider, model, and
  reasoning-effort selection.
- [Multimodal input](./multimodal-input.md) covers image and other content
  blocks supported by the selected provider.
- [Context windows](./context-windows.md) covers model context-window
  resolution and overrides.
- [Session continuity](./session-continuity.md) covers persistence, recovery,
  retention, and maintenance.
- [Scheduled tasks](./scheduled-tasks.md) covers recurring and one-shot runs.

## Configure agent behavior

- [Define named agents](./named-agents.md) covers agent definitions and
  specialist delegation.
- [Skills, commands, and soul](./skills-commands-and-soul.md) covers the
  reusable instructions and persona that shape a run.
- [Project instructions and rules](./project-instructions-and-rules.md) covers
  trusted project guidance and `.claude/rules`.
- [Learning](./learning.md) covers evidence-backed learning, reflection, and
  learned-skill admission.
- [Dreaming and memory consolidation](./dreaming.md) covers periodic
  consolidation and manual review.

## Secure connections and execution

- [Permissions and posture](./permissions-and-posture.md) covers permission
  modes, trust, guardrails, and autonomous operation.
- [Caller identity and OIDC](./caller-identity.md) covers authenticated caller
  identity and ownership separation.
- [MCP OAuth and credentials](./mcp-oauth-and-credentials.md) covers OAuth
  profiles and credential storage.
- [Execution environments](./execution-environments.md) covers workspaces,
  shells, forks, and environment persistence. Background Shell is documented in
  [Core tools](/building/what-you-get/core-tools.md#background-commands).
