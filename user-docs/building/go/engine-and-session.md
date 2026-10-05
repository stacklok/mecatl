---
slug: /building/what-you-get/engine-and-session
title: Engine, session, run, and environment
description: Understand the Go objects that configure, persist, and execute an agent.
sidebar_position: 3
---

# Engine, session, run, and environment

An agent run combines a reusable engine, a session containing conversation
state, and an environment where tools execute. The returned run handle lets
your application observe progress and answer approval requests. These objects
have different lifetimes, so you can persist a conversation without keeping
the engine process alive.

<span id="the-three-objects-you-hold" />

## The engine supplies the loop

[`agent.Engine`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/agent#Engine)
contains the model provider, catalog, permission policy, hooks, and other
dependencies supplied through
[`agent.Deps`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/agent#Deps).
Reuse an engine for sessions that use compatible dependencies. Changing provider
or model can require rebuilding model-dependent adapters and prompt configuration;
construct a compatible engine rather than replacing only the model name.

The catalog determines which tools are available. The provider streams model
output, and the policy decides whether a proposed call runs, asks for approval,
or is denied. The [agent loop](/features/sessions/agent-loop.md) explains how
these parts interact during a turn.

## The session holds durable state

[`session.Session`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/session#Session)
contains the conversation, lifecycle state, usage, limits, and pending approval.
It is created separately from the engine and passed to each run. A configured
session store persists this aggregate; a later process can load it and assemble
a compatible engine to continue the conversation.

A session's terminal state and a closed event stream have different meanings.
Your host must use the session lifecycle methods when reopening completed work,
and preserve a pending approval when resuming paused work. See
[SessionStore and EventLog](extension-points/session-store.md) for persistence
integration and [session continuity](/features/sessions/session-continuity.md)
for the shared recovery behavior.

<span id="the-things-you-pass-in" />

## The environment binds execution

[`tool.Environment`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/tool#Environment)
binds an exact `EnvironmentRef` to a workspace, read ledger, and optional command
runner. The session and environment must carry the same kind, ID, and revision.
Treat the ID as opaque; each provider defines its meaning.

A valid environment always has a workspace and a read ledger. The ledger holds
read-before-write evidence independently of the content backend. Use
`tool.NewEnvironment` when construction can fail, or `tool.MustEnvironment` when
missing dependencies are a programming error. A nil command runner represents
an environment without shell execution.

Delegation constructs another environment for the child namespace rather than
changing the parent's environment. The [tool integration guide](extension-points/tool-catalog.md)
covers workspace and execution interfaces.

<span id="what-subagent-and-team-mean" />

## The run is a live control handle

[`agent.Run`](https://pkg.go.dev/github.com/stacklok/mecatl/engine/agent#Run)
represents one active prompt. `Engine.Run(ctx, sess, env, request)` returns
immediately while the loop executes in the background. Consume `Run.Events()`
until its channel closes, route ordinary permission asks through `Run.Approve`,
and call `Run.Cancel` to stop the work.

Channel closure ends that run's event stream. A run can also park for external
authorization without completing the session; `Run.Outcome()` distinguishes
that case. Keep the session state as the source of lifecycle information.
Approval kinds with additional identity requirements, including guardrail result
release, need the dedicated resolution methods described in
[permission integration](extension-points/permission-policy.md).

Subagents use child engines with their own catalog, policy, and optional model.
Teams add a supervisor that coordinates member engines. Each member still runs a
session through the same loop; authority and environment isolation bound its work.

## Related information

- [Embed the engine directly](embed-engine.md)
- [Permissions and posture](/features/security-and-execution/permissions-and-posture.md)
- [Go API compatibility](api-stability.md)
