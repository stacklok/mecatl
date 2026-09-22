---
sidebar_position: 1
title: Capabilities
description:
  Understand Mecatl capabilities, shared behavior, and deployment availability.
---

# Capabilities

Each page in this section owns the shared behavior and availability of one
Mecatl capability. Client, operator, and builder guides link here instead of
repeating the same explanation for each way of running Mecatl.

## Get oriented

- [Capability and deployment matrix](./get-oriented/capability-matrix.md) explains the
  operational differences among server and terminal deployments.
- [What is a cloud-native harness?](/cloud-native-harness.md) explains how
  Mecatl separates the agent process from durable state and execution.
- [Ask Mecatl about itself](./get-oriented/ask-about-mecatl.md) covers the account of Mecatl
  that every agent carries and how it answers questions about the product.

## Run and maintain sessions

- [The agent loop](./sessions/agent-loop.md) explains turns, events, tool dispatch,
  permission pauses, and terminal states.
- [Start and resume sessions](./sessions/start-and-resume-sessions.md) covers creating,
  selecting, and continuing sessions.
- [Choose models and providers](./sessions/choose-models.md) covers provider, model, and
  reasoning-effort selection.
- [Multimodal input](./sessions/multimodal-input.md) covers image and other content
  blocks supported by the selected provider.
- [Context windows](./sessions/context-windows.md) covers model context-window resolution
  and overrides.
- [Session continuity](./sessions/session-continuity.md) covers persistence, recovery,
  retention, and maintenance.
- [Scheduled tasks](./sessions/scheduled-tasks.md) covers recurring and one-shot runs.
- [Core tools](./sessions/tools.md) describes the built-in catalog and execution model.

## Configure agent behavior

- [Subagents, teams, and parallel work](./agent-behavior/subagents-and-teams.md) compares the
  delegation tools and their workspace behavior.
- [Define named agents](./agent-behavior/named-agents.md) covers agent definitions and
  specialist delegation.
- [Skills, commands, and soul](./agent-behavior/skills-commands-and-soul.md) covers reusable
  instructions and persona.
- [Project instructions and rules](./agent-behavior/project-instructions-and-rules.md) covers
  trusted project guidance and `.claude/rules`.
- [Memory and user model](./agent-behavior/memory.md) explains project memory, the user model,
  and their lifecycle tools.
- [Learning](./agent-behavior/learning.md) covers evidence-backed learning, reflection, and
  learned-skill admission.
- [Dreaming and memory consolidation](./agent-behavior/dreaming.md) covers periodic
  consolidation and manual review.

## Secure connections and execution

- [Permissions and posture](./security-and-execution/permissions-and-posture.md) covers permission
  modes, trust, guardrails, and autonomous operation.
- [Caller identity and OIDC](./security-and-execution/caller-identity.md) covers authenticated caller
  identity and ownership separation.
- [MCP OAuth and credentials](./security-and-execution/mcp-oauth-and-credentials.md) covers OAuth
  profiles and credential storage.
- [MCP client](./security-and-execution/mcp-client.md) covers transports, tool names, resources,
  prompts, and reconnect behavior.
- [Execution environments](./security-and-execution/execution-environments.md) covers workspaces,
  shells, forks, and environment persistence.
- [Hook system](./security-and-execution/hooks.md) covers lifecycle hooks that observe, block, or
  transform agent activity.
