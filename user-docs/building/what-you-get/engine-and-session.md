---
sidebar_position: 0
title: Engine and session model
description:
  Understand the engine, session, and run objects that make up a Mecatl agent.
---

# Engine and session model

Build an agent by running a persisted `Session` with a reusable `Engine`. The
returned `Run` lets your application follow progress, answer permission
requests, or cancel the work.

## The three objects you hold

### `*agent.Engine`

The engine runs the agent loop. Reuse one engine across sessions that use the
same provider and model. Construct it with the model adapter, tool catalog,
permission policy, and hooks, then start a run:

```go
env := tool.MustEnvironment(
    session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "example-v1"},
    workspace,
    memledger.New(),
    nil,
)
run := eng.Run(ctx, sess, env, agent.RunRequest{Text: "your prompt here"})
```

Create the engine with `agent.NewEngine(agent.Deps{...})`. The example uses the
`memledger` reference adapter for the environment's required read ledger.

### `*session.Session`

The session holds conversation history, state, usage, counters, and limits. It
is the unit of persistence, so a restored session can continue where it stopped.

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

The same engine can run different sessions. After a session completes, you can
reopen it and use the same engine or another compatible engine.

Legacy snapshots with an unknown producer remain read-only. An authenticated
server can copy an eligible, owned snapshot into a new session after the
operator selects its environment, provider, and model. The original snapshot
remains unchanged.

### `*agent.Run`

`engine.Run(...)` starts the loop in the background and returns immediately:

```go
for ev := range run.Events() {
    // observe the loop
    if ev.Type == session.EvPermissionAsk {
        run.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
    }
}
// channel closed = run is done
```

`Run.Events()` returns ordered events and closes when the run ends.
`Run.Approve(askID, verdict)` answers a permission request. `Run.Cancel()`
cancels the run.

## The things you pass in

|What|Type|What it does|
|-|-|-|
|**Environment**|`tool.Environment`|Binds a non-null workspace, an optional command runner, and the environment identity.|
|**Catalog**|`*tool.Catalog` on `Deps`|Registers built-in, MCP, and custom tools. `Specs(mode)` defines what the model can use.|
|**LLM provider**|`port.LLMProvider` on `Deps`|Streams provider-neutral model output. Mecatl includes OpenAI and Anthropic adapters.|

---

## What "subagent" and "team" mean

Neither is a separate agent type. A subagent uses a child engine with its own
catalog, policy, and optional model. A team uses a `Supervisor` to coordinate
member engines. Each child still runs a session through `Engine.Run`.

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

The engine and session remain independent. You can test them with the reference
adapters and resume a session in a process that did not start it.

## What's next

- [The agent loop](agent-loop.md) for turn structure, dispatch, compaction, and
  cancellation in detail.
- [Permissions and guardrails](permissions.md) for how `Policy.Evaluate` decides
  what tools can run.
- [Extension points](/building/extension-points/index.md) to implement a port
  interface to replace any capability without touching the loop.
