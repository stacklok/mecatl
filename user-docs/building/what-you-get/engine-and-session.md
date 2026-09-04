---
sidebar_position: 0
title: Engine & session model
description: Understand the engine, session, and run objects that make up a Mecatl agent.
---

# Engine & session model

Mecatl has no `Agent` type. The word *agent* describes the **behaviour** that emerges when an `Engine` runs a `Session` — not a single Go object. Understanding the three objects you hold and the three objects you pass in is the fastest way to orient yourself before reading anything else.

---

## The three objects you hold

### `*agent.Engine`

The engine is the **loop runner**. It is long-lived and reusable — one engine per `(provider, model)` pair, shared across as many sessions as you like. It wires together the LLM adapter, tool catalog, permission policy, and hooks at construction time and exposes a single entry point:

```go
env := tool.MustEnvironment(session.EnvironmentRef{}, workspace, nil)
run := eng.Run(ctx, sess, env, agent.RunRequest{Text: "your prompt here"})
```

`Engine` is created once with `agent.NewEngine(agent.Deps{...})`. The `Deps` struct is the complete wiring surface — every capability the loop needs is injected there. The engine itself owns nothing stateful; state lives in the session.

### `*session.Session`

The session is the **conversation state**. It holds the message history, the current state-machine state (`idle → running → completed`, etc.), counters (turns, tool calls), and limits. It is the unit of persistence: save a session, restore it later, and the conversation picks up exactly where it left off.

A session is created separately from the engine and passed in at run time:

```go
sess := session.New(
    "my-session-id",
    session.ModeDefault,
    "/workspace/root",
    session.Limits{MaxTurns: 20, MaxToolCalls: 60},
    time.Now(),
)
```

The same engine can run different sessions. The same session can be reopened and run again (after it completes) by the same or a different engine.

Legacy snapshots whose producer kind is unknown remain inspect-only. An authenticated
server can explicitly adopt an eligible owned legacy snapshot as a **new** main chat:
it preflights the complete authoritative transcript, requires explicit workspace/environment
and provider/model bindings, and publishes an idempotent copy with a source audit link.
It never relabels or rewrites the legacy source, never adopts in bulk, and never accepts a
client-uploaded transcript. In mecatui's **Sessions → Other** tab these rows are labelled
**Legacy session — inspect only**. The **Adopt as chat** action appears only after an
authenticated server preflight accepts the explicitly selected workspace/environment and
provider/model. Review the new-chat target and tool-write warning before confirming; success
opens the server-refetched new chat, while cancel or failure leaves the inventory/review stable.

### `*agent.Run`

`engine.Run(...)` returns a `*Run` immediately. The loop starts in a background goroutine; the `Run` handle is your interface to it while it's live:

```go
for ev := range run.Events() {
    // observe the loop
    if ev.Type == session.EvPermissionAsk {
        run.Approve(ev.Ask.AskID, session.VerdictAllowAlways)
    }
}
// channel closed = run is done
```

`Run.Events()` is a read-only channel that carries every observable event, in order, closed exactly once when the run terminates. `Run.Approve(askID, verdict)` sends a permission verdict. `Run.Cancel()` cancels the run's context. That is the entire client surface.

---

## The things you pass in

| What | Type | What it does |
|---|---|---|
| **Environment** | `tool.Environment` | Binds the non-nil filesystem workspace, optional command runner, and environment identity for this run. |
| **Catalog** | `*tool.Catalog` (on `Deps`) | The tool registry. Holds `Read`, `Write`, `Edit`, `Bash`, MCP servers, and any custom tools you register. The engine reads `Specs(mode)` to tell the model what it can do. |
| **LLMProvider** | `port.LLMProvider` (on `Deps`) | The model backend. The engine calls `Stream(ctx, LLMRequest)` and receives a neutral chunk stream. The OpenAI and Anthropic adapters ship out of the box; implement this interface to bring your own. |

---

## What "subagent" and "team" mean

Neither is a new object type. A **subagent** gets its own pre-built child `Engine` — wired at composition time with a scoped-down catalog (read-only tools by default, no Bash), its own permission policy, and optionally a different model. The parent holds a handle to the child's run through the `SubagentStatus` tool. A **team** is a `Supervisor` coordinating a set of member engines, each similarly pre-built with its own scoped engine. In all cases the individual unit of work is still `Engine.Run(session)` — the structure is the same, but each child runs through its own engine, not the parent's.

---

## How it fits together

```
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

The engine never owns the session. The session never owns the engine. Both are passed around explicitly, which is what makes the system testable with fakes (`mockllm`, `memfs`, `memstore`) and why the same session can be resumed by a process that had no part in starting it.

---

## What's next

- [The agent loop](agent-loop.md) — turn structure, dispatch, compaction, and cancellation in detail.
- [Permissions & guardrails](permissions.md) — how `Policy.Evaluate` decides what tools can run.
- [Extension points](/building/extension-points/index.md) — implement a port interface to replace any capability without touching the loop.
