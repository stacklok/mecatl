---
sidebar_position: 2
title: Embed the engine directly
---

# Embed the engine directly

You own the binary. The agent loop runs in-process, wired alongside your existing service code. No gRPC server, no separate process, no TLS handshake — just a Go `import` and a constructor call.

This is the right choice when mecatl needs to live inside a larger service you already operate, when you want fine-grained control over every dependency in your build graph, or when the overhead of standing up a `mecated` process is more than you want to carry.

---

## When to choose this

Choose in-process embedding when:

- You are building a product (a code review service, a CI orchestrator, an IDE backend) and want the agent loop as a library component, not a sidecar.
- You need to keep the binary's dependency footprint small — specifically, you want to avoid pulling in the OpenAI/Anthropic SDKs, gRPC, the TUI, or `client-go`.
- You already have a runtime (HTTP server, worker loop, queue consumer) and want the agent to live inside it.
- You want to supply your own `port.LLMProvider` implementation — a custom model proxy, a router over internal endpoints, or a mock for tests.

Choose a pre-built binary (`mecated`, `mecak8s`, `mecatequi`) when you want the composition done for you, or when you need the full operator surface (auth, TLS, Prometheus, posture flags, gRPC clients).

---

## Dependency footprint

The engine is a separate Go module: `github.com/stacklok/mecatl/engine`. Its `go.mod` requires three packages at runtime plus one test-only package:

| Package | Role |
|---|---|
| `golang.org/x/sync` | `errgroup` for concurrent tool dispatch |
| `github.com/bmatcuk/doublestar/v4` | Glob matching for permission patterns in `engine/adapter/memfs` |
| `github.com/robfig/cron/v3` | Cron expression parsing in `engine/adapter/cronparse` (scheduled tasks); itself a dependency-free module |
| `go.uber.org/goleak` | Test-only (leaked-goroutine detection); never enters a production build |

Nothing from mecatl's heavy require cone — no OpenAI/Anthropic SDKs, no gRPC, no Bubble Tea TUI, no `k8s.io/client-go` — enters your build graph. A `go get github.com/stacklok/mecatl/engine` does not transitively pull the root module.

---

## Adding the dependency

```sh
go get github.com/stacklok/mecatl/engine@latest
```

That's the only step. The engine module is self-contained; it does not require any other `mecatl` module.

---

## Minimum wiring

The engine is built from a `agent.Deps` struct — a bag of injected ports and configuration. Exactly three fields are required at startup; the rest are optional and have documented defaults.

Here is the minimum viable wiring, modelled after `cmd/mecademo/demo.go`:

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/stacklok/mecatl/engine/adapter/memfs"
    "github.com/stacklok/mecatl/engine/adapter/memstore"
    "github.com/stacklok/mecatl/engine/adapter/mockllm"
    "github.com/stacklok/mecatl/engine/adapter/permpolicy"
    "github.com/stacklok/mecatl/engine/adapter/permstore"
    "github.com/stacklok/mecatl/engine/agent"
    "github.com/stacklok/mecatl/engine/governance"
    "github.com/stacklok/mecatl/engine/prompt"
    "github.com/stacklok/mecatl/engine/session"
    "github.com/stacklok/mecatl/engine/tool"
)

func main() {
    ctx := context.Background()

    // 1. Build a tool catalog. Register the tools you want the model to use.
    //    tools.All() (from internal/adapter/tools) is the full production set;
    //    for embedding, register only what you need.
    cat := tool.NewCatalog()
    // cat.MustRegister(myTool)

    // 2. Build a permission policy. Rules decide whether each tool call runs,
    //    asks the operator, or is denied. ScopeManaged is the highest-trust scope.
    policy := permpolicy.NewPolicy([]governance.Rule{
        {Scope: governance.ScopeManaged, Tool: "Read",  Effect: governance.Allow},
        {Scope: governance.ScopeManaged, Tool: "Write", Effect: governance.Ask},
    }, permstore.New())

    // 3. Wire the engine.
    eng := agent.NewEngine(agent.Deps{
        LLM:     mockllm.New(mockllm.TextTurn("Hello, world.")), // replace with your provider
        Catalog: cat,
        Policy:  policy,
        Hooks:   noopHooks{}, // or hookexec.New(nil) for a no-op shell hook runner
        Store:   memstore.New(),
        PromptConfig: prompt.Config{
            Env: prompt.Env{
                Cwd:   "/workspace",
                OS:    "linux",
                Model: "my-model",
                Date:  time.Now().Format("2006-01-02"),
                Mode:  string(session.ModeDefault),
            },
        },
        Model: "my-model",
    })

    // 4. Create a session and a workspace.
    sess := session.New(
        "my-session-id",
        session.ModeDefault,
        "/workspace",
        session.Limits{MaxTurns: 20, MaxToolCalls: 50, MaxConsecutiveFailures: 3},
        time.Now(),
    )
    ws := memfs.NewWorkspace("/workspace")

    // 5. Start the run and drain events.
    run := eng.Run(ctx, sess, ws, "Summarize the project.")
    for ev := range run.Events() {
        fmt.Printf("%s %v\n", ev.Type, ev.Text)

        // Approve any permission asks (or surface them to your own UI).
        if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
            run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
        }
    }
}
```

The call to `eng.Run` returns a `*Run` immediately; the loop drives in a background goroutine. Drain `run.Events()` to consume the event stream — the channel closes when the run terminates. See [The agent loop](../what-you-get/agent-loop.md) for the full event taxonomy and permission flow.

---

## The ports you must satisfy

`agent.Deps` has three required fields. The rest are optional — zero values or nil engage documented defaults.

| Field | Type | Required? | Reference adapter | Notes |
|---|---|---|---|---|
| `LLM` | `port.LLMProvider` | **yes** | `engine/adapter/mockllm` for tests; bring your own for production | Implement `Stream` + `Capabilities`. See `engine/port/llm.go`. |
| `Catalog` | `*tool.Catalog` | **yes** | `tool.NewCatalog()` + `cat.MustRegister(...)` | Register only the tools your agent should use. |
| `Policy` | `port.PermissionPolicy` | **yes** | `engine/adapter/permpolicy` + `engine/adapter/permstore` | `permpolicy.NewPolicy(rules, permstore.New())` is the standard wiring. |
| `Hooks` | `port.HookRunner` | **yes (but no-op works)** | `hookexec.New(nil)` (from `internal/adapter/hookexec`) | A nil `Hooks` will panic; use the no-op constructor if you have no hooks. |
| `Store` | `port.SessionStore` | no | `engine/adapter/memstore` | nil disables persistence. `memstore.New()` is the in-process default. |
| `Clock` | `port.Clock` | no | `engine/adapter/wallclock` | nil → no tool-call timing. |
| `Model` | `string` | **yes** | — | Sent on every `LLMRequest`. Must match your provider's model identifier. |
| `PromptConfig` | `prompt.Config` | no | — | Seeds the stable system prompt prefix. `Env.Cwd`, `Env.Model`, `Env.Date`, `Env.Mode` are the meaningful fields for most embeddings. |

Optional fields with non-trivial defaults:

| Field | Default behaviour |
|---|---|
| `Compactor` | `HeuristicCompactor` — trims the conversation to the context window threshold. |
| `TokenCounter` | `HeuristicTokenCounter` — character-based estimate. |
| `ContextWindow` | nil → compaction disabled. Wire a closure that returns the model's window in tokens to enable it. |
| `MaxNoProgressNudges` | `2` — the loop injects up to two continuation nudges when the model produces an empty/reasoning-only turn before terminating with `StopNoProgress`. |
| `MaxRunTokens` | `0` — no per-run token budget. Set to a positive value to cap cumulative spend. |
| `Instructions` | `prompt.RootAssembler` — looks for `AGENTS.md` / `CLAUDE.md` at the workspace root. |

---

## Optional evidence reflection

Embedders can use `engine/learning` to construct an owned, bounded reflection input and
run its compatibility structural signal detector or the current-span-scoped detector.
`learning.ThresholdPolicy` is the pure standard admission policy over closed sensitivity,
class, reason, request, and decision contracts; `AlwaysPolicy` and `NeverPolicy` are simple
host alternatives. `Trajectory` additively carries session kind, run counters, and a verified
current message span. `learning.Activity` is the closed content-free metrics projection.
`agent.NewEvidenceReflector` adds an optional
single-call model-backed reflector over an injected provider, selected model, token
counter, and explicit limits. It has no tools or filesystem access and returns only a
strictly evidence-backed proposal set or explicit abstention.

The module also exposes replaceable, CAS-only learned-skill lifecycle contracts. Hosts can
validate body-only agent-owned bundles, store content-addressed versions with bounded
provenance and evaluations, and explicitly link a historical deferred procedure proposal to a
draft. `engine/adapter/memskill` is the in-memory reference,
`engine/adapter/skillvalidation` is the logical admission validator, and
`engine/adapter/skillmaterialize` is the recoverable proposal-to-draft linker. The host repository
ships `internal/adapter/skillstore` as a durable single-host flock/manifest implementation with
immutable content-addressed `SKILL.md` files. Procedure materialization uses recoverable
create-then-CAS-link semantics, so retry after a crash does not duplicate a draft.
`engine/adapter/skilllifecycle.Pipeline` supplies the synchronous off/review/auto policy over an
injected repository, validator, evaluator, and atomic publisher; `engine/adapter/skillfs.AtomicCatalog`
supplies a complete-generation live Skill tool while preserving existing snapshot sources. Standard
mecatl composition wires these into its caller-partitioned gRPC/HTTP review surface; an embedder may
replace every seam.

The engine library seam itself does not choose persistence, schedule jobs, or expose a transport. Hosts
that consume proposals own review, authorization, and persistence. See the
[architecture guide](https://github.com/stacklok/mecatl/blob/main/docs/architecture.md#evidence-backed-reflection)
for the evidence and output-validation contract.

---

## What you do not get

In-process embedding is the engine and nothing else. You are responsible for everything outside it:

| Capability | Status |
|---|---|
| HTTP / gRPC server | Not included. Wire your own transport and relay events to it. |
| Auth (token, mTLS) | Not included. Your binary; your auth layer. |
| TLS | Not included. |
| Prometheus metrics | Not included. Wire `port.ToolCallRecorder` and `port.Diagnostics` to your own observability stack. |
| Kubernetes manifests | Not included. The engine has no concept of k8s. |
| CLI flag surface (`--posture`, `--store-dir`, …) | Not included. You set `Deps` fields in code. |
| OpenAI / Anthropic provider adapters | Not included in the engine module — but they ARE importable as opt-in submodules. Import `github.com/stacklok/mecatl/provider/openai` (Responses API), `github.com/stacklok/mecatl/provider/openaichat` (Chat Completions API), or `github.com/stacklok/mecatl/provider/anthropic` (native Messages API) and you pull only that provider's SDK plus the engine module, never the root module (see [ADR 0093](https://github.com/stacklok/mecatl/blob/main/docs/adr/0093-provider-modules.md)). |
| Session store backends (JSONL, Redis) | Not included in the engine module. `memstore` is. For durable or Redis-backed storage, import the root module's adapters. |

If you need several of those capabilities, `mecated` (or the `internal/app` composition layer) assembles them for you. See [Run mecated standalone](mecated.md).

---

## go.work for monorepo development

The engine is a separate Go module inside the mecatl monorepo, connected via `go.work`. If you develop against a local checkout of mecatl rather than the published module, set up a `go.work` in your own repo's parent:

```sh
# In your project root (where your go.mod lives):
go work init .
go work use /path/to/mecatl/engine
```

The resulting `go.work` file:

```
go 1.26

use .
use /path/to/mecatl/engine
```

Now `go build` and `go test` resolve `github.com/stacklok/mecatl/engine` from the local checkout rather than the module proxy. Commit `go.work.sum` alongside `go.work` if others on your team check out both repos.

:::note[go.work is local-only]

`go.work` files are for local development. Published modules should use a `replace` directive in `go.mod` for the same effect, or simply depend on a tagged release. Do not commit `go.work` to a repository that others will `go get` from.

:::

---

## What's next

- [The agent loop](../what-you-get/agent-loop.md) — event taxonomy, permission pause/resume, compaction, and terminal states.
- [Permissions & guardrails](../what-you-get/permissions.md) — how to configure rules, posture, and the model-backed guardrail layer.
- [API stability](../api-stability.md) — what's guaranteed not to break in the engine module you just imported, and how a breaking change is classified and surfaced.
- [Run mecated standalone](mecated.md) — if you want the composition done for you (auth, TLS, gRPC, Prometheus).
- [Cloud-native k8s with mecak8s](mecak8s.md) — stateless Kubernetes deployment backed by Redis and k8s leases.
