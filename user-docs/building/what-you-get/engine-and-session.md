---
sidebar_position: 0
title: Engine & session model
description:
  Understand the engine, session, and run objects that make up a Mecatl agent.
---

# Engine & session model

Mecatl has no `Agent` type. An agent is the behavior that emerges when an
`Engine` runs a `Session`. Builders work with three main objects: the reusable
engine, the persisted session, and the live run handle.

---

## The three objects you hold

### `*agent.Engine`

The engine runs the agent loop. It is long-lived and reusable across sessions
for the same `(provider, model)` pair. Construct it with the LLM adapter, tool
catalog, permission policy, and hooks, then call its `Run` method:

```go
env := tool.MustEnvironment(
    session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "example-v1"},
    workspace,
    memledger.New(),
    nil,
)
run := eng.Run(ctx, sess, env, agent.RunRequest{Text: "your prompt here"})
```

Create an `Engine` with `agent.NewEngine(agent.Deps{...})`. `Deps` contains the
capabilities the loop needs. The engine owns no conversation state; that state
lives in the session.

The example uses the `memledger` reference adapter for the environment's
required read ledger.

### `*session.Session`

The session is the **conversation state**. It holds the message history, the
current state-machine state (`idle → running → completed`, etc.), counters
(turns, tool calls), and limits. It is the unit of persistence: save a session,
restore it later, and the conversation picks up exactly where it left off.

A session is created separately from the engine and passed in at run time:

```go
sess := session.New(
    "my-session-id",
    session.ModeDefault,
    session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace/root", Revision: "example-v1"},
    session.Limits{MaxTurns: 20, MaxToolCalls: 60},
    time.Now(),
)
```

The same engine can run different sessions. The same session can be reopened and
run again (after it completes) by the same or a different engine.

Legacy snapshots with an unknown producer kind remain inspect-only. An
authenticated server can adopt an eligible, owned snapshot as a new chat after
the operator selects its environment, provider, and model. Adoption copies the
authoritative transcript and leaves the legacy snapshot unchanged.

### `*agent.Run`

`engine.Run(...)` returns a `*Run` immediately. The loop starts in a background
goroutine; the `Run` handle is your interface to it while it's live:

```go
for ev := range run.Events() {
    // observe the loop
    if ev.Type == session.EvPermissionAsk {
        run.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
    }
}
// channel closed = run is done
```

`Run.Events()` is a read-only channel that carries every observable event, in
order, closed exactly once when the run terminates.
`Run.Approve(askID, verdict)` sends a permission verdict. `Run.Cancel()` cancels
the run's context. That is the entire client surface.

---

## The things you pass in

|What|Type|What it does|
|-|-|-|
|**Environment**|`tool.Environment`|Binds the non-nil filesystem workspace, optional command runner, and environment identity for this run.|
|**Catalog**|`*tool.Catalog` (on `Deps`)|The tool registry. Holds `Read`, `Write`, `Edit`, `Shell`, MCP servers, and any custom tools you register. The engine reads `Specs(mode)` to tell the model what it can do.|
|**LLMProvider**|`port.LLMProvider` (on `Deps`)|The model backend. The engine calls `Stream(ctx, LLMRequest)` and receives a neutral chunk stream. The OpenAI and Anthropic adapters ship out of the box; implement this interface to bring your own.|

---

## What "subagent" and "team" mean

Neither is a new object type. A **subagent** runs on a child `Engine` with its
own tool catalog, permission policy, and optional model. A **team** is a
`Supervisor` coordinating several member engines. Each child still performs its
work through `Engine.Run(session)`.

---

## How it fits together

```text
agent.Deps{LLM, Catalog, Policy, Hooks, Store, ...}
          │
          ▼
    *agent.Engine          ← long-lived, reusable per (provider, model)
          │
          │  .Run(ctx, sess, env, req)
          ▼
      *agent.Run           ← live handle; one per active run
     /           \
 .Events()     .Approve()
 (chan Event)  (send verdict)

*session.Session           ← conversation state; passed in, saved by Store
  ├── Conversation         ← ordered []Message history
  ├── State                ← idle / running / awaiting / completed / failed / cancelled
  ├── Limits               ← MaxTurns, MaxToolCalls, MaxConsecutiveFailures
  └── Usage                ← cumulative token spend
```

The engine never owns the session. The session never owns the engine. Both are
passed around explicitly, which is what makes the system testable with fakes
(`mockllm`, `memfs`, `memstore`) and why the same session can be resumed by a
process that had no part in starting it.

---

## What's next

- [The agent loop](agent-loop.md) — turn structure, dispatch, compaction, and
  cancellation in detail.
- [Permissions & guardrails](permissions.md) — how `Policy.Evaluate` decides
  what tools can run.
- [Extension points](/building/extension-points/index.md) — implement a port
  interface to replace any capability without touching the loop.
