---
sidebar_position: 10
title: Embed the engine directly
description: Embed the Mecatl engine in your Go service and wire its adapters in process.
---

# Embed the engine directly

Start with [Build your first agent](/building/getting-started/first-agent.md) for
the shortest copyable path. This page is the detailed reference for embedders:
You own the binary. The agent loop runs in-process, wired alongside your existing service code. No gRPC server, no separate process, no TLS handshake — just a Go `import` and a constructor call.

This is the right choice when Mecatl needs to live inside a larger service you already operate, when you want fine-grained control over every dependency in your build graph, or when the overhead of standing up a `mecated` process is more than you want to carry.

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

The engine is a separate Go module: `github.com/stacklok/mecatl/engine`. Its runtime dependency closure includes `doublestar`, `robfig/cron/v3`, `github.com/goccy/go-yaml`, `golang.org/x/net`, and `golang.org/x/sync`; `go.uber.org/goleak` is test-only:

| Package | Role |
|---|---|
| `golang.org/x/sync` | `errgroup` for concurrent tool dispatch |
| `github.com/bmatcuk/doublestar/v4` | Glob matching for permission patterns in `engine/adapter/memfs` |
| `github.com/goccy/go-yaml` | YAML parsing used by core configuration/value handling |
| `golang.org/x/net` | HTML parsing used by core web-content handling |
| `github.com/robfig/cron/v3` | Cron expression parsing in `engine/adapter/cronparse` |
| `go.uber.org/goleak` | Test-only leaked-goroutine detection; never enters a production build |

Nothing from Mecatl's heavy require cone — no OpenAI/Anthropic SDKs, no gRPC, no Bubble Tea TUI, no `k8s.io/client-go` — enters your build graph. A `go get github.com/stacklok/mecatl/engine` does not transitively pull the root module.

---

## Adding the dependency

```sh
go get github.com/stacklok/mecatl/engine@latest
```

That's the only step. The engine module is self-contained; it does not require any other `mecatl` module.

---

## Minimum wiring

The engine is built from an `agent.Deps` struct — a bag of injected ports and configuration. A useful run supplies an LLM provider, tool catalog, permission policy, and model identifier; hooks, persistence, timing, and prompt helpers are optional and have documented defaults.

Here is the minimum viable wiring, modelled after `cmd/mecademo/demo.go`:

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/stacklok/mecatl/engine/adapter/memfs"
    "github.com/stacklok/mecatl/engine/adapter/memledger"
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
        // Hooks are optional; nil uses the engine's no-op behavior.
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

    // 4. Create a session and a workspace. The ref identifies the same
    //    environment for the session and the live tool environment.
    ref := session.EnvironmentRef{
        Kind: session.EnvKindMem, ID: "/workspace", Revision: "example-v1",
    }
    sess := session.New(
        "my-session-id",
        session.ModeDefault,
        ref,
        session.Limits{MaxTurns: 20, MaxToolCalls: 50, MaxConsecutiveFailures: 3},
        time.Now(),
    )
    ws := memfs.NewWorkspace("/workspace")

    // 5. Start the run and drain events.
    env := tool.MustEnvironment(ref, ws, memledger.New(), nil)
    run := eng.Run(ctx, sess, env, agent.RunRequest{Text: "Summarize the project."})
    for ev := range run.Events() {
        fmt.Printf("%s %v\n", ev.Type, ev.Text)

        // Approve any permission asks (or surface them to your own UI).
        if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
            run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
        }
    }
}
```

The call to `eng.Run` returns a `*Run` immediately; the loop drives in a background goroutine. Drain `run.Events()` to consume the event stream — the channel closes when the run terminates. See [The agent loop](/building/what-you-get/agent-loop.md) for the full event taxonomy and permission flow.

---

## Ports and configuration

`agent.Deps` has a small set of required runtime seams for a meaningful run. The
remaining fields are optional — zero values or nil engage documented defaults.

| Field | Type | Required? | Reference adapter | Notes |
|---|---|---|---|---|
| `LLM` | `port.LLMProvider` | **yes** | `engine/adapter/mockllm` for tests; bring your own for production | Implement `Stream` + `Capabilities`. See `engine/port/llm.go`. |
| `Catalog` | `*tool.Catalog` | **yes** | `tool.NewCatalog()` + `cat.MustRegister(...)` | Register only the tools your agent should use. |
| `Policy` | `port.PermissionPolicy` | **yes** | `engine/adapter/permpolicy` + `engine/adapter/permstore` | `permpolicy.NewPolicy(rules, permstore.New())` is the standard wiring. |
| `Hooks` | `port.HookRunner` | no | — | Nil hooks are supported and use the engine's no-op behavior. |
| `Store` | `port.SessionStore` | no | `engine/adapter/memstore` | nil disables persistence. Use `memstore.New()` for in-process persistence. |
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
| `MaxRunTokens` | `0` — no per-engine token budget. Set a positive value to cap an engine session's cumulative spend. The same ceiling is inherited by subagents, Parallel branches, team members, and lead synthesis, but each engine enforces it against its own session usage; child spend is excluded from the parent, so a delegation tree can exceed it. |
| `Instructions` | `prompt.RootAssembler` — looks for `AGENTS.md` / `CLAUDE.md` at the workspace root. |

### Token budgets with delegation

`Deps.MaxRunTokens` is an independently enforced ceiling for each engine, not a
shared delegation-tree allowance. A main engine, Subagent, Parallel branch, team
member, and lead synthesis inherit the configured value, while each checks only its
own session usage. Child spend is excluded from parent usage, so a delegation tree
can exceed that ceiling.

For teams, `agent.WithTeamTokenBudget` configures the separate aggregate
`MaxTeamTokens` equivalent on the `agent.Supervisor`. It is checked between rounds:
when crossed, it prevents another round, while the current round and lead synthesis
complete. It is distinct from and composes with `Deps.MaxRunTokens`; it does not
provide a cross-tree aggregate outside that team.

---

## Optional evidence reflection

Embedders use `learning.MaterializeEvidence` as the storage-neutral selection boundary.
A `learning.MaterializationRequest` carries the owned trajectory, eligible events, verified
mandatory span, signals, and explicit limits. The selected result contains one bounded
`learning.Input`, canonical bytes, and an immutable aggregate manifest; abstained or skipped
results carry only a closed, content-free reason. Hosts persist the complete manifest with any
staged proposal and re-materialize its exact original coordinates for detail or approval instead
of rerunning ranking. New records use `reflection-evidence/v1`; historical input-local evidence
ordinals remain explicitly `reflection-evidence/legacy-v0`.

The compatibility structural signal detector and current-span-scoped detector operate on the
bounded input. `learning.ThresholdPolicy` is the pure standard admission policy over closed sensitivity,
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
injected repository, validator, evaluator, and atomic publisher. Its `ActivationPolicy` zero value is
`evaluated` for source-compatible PASS-only behavior. A host may select `validated` only when its
repository implements the optional `learning.ValidatedSkillActivator`, whose atomic contract accepts
non-legacy, evidence-backed, accepted/exact, staged ABSTAIN versions under owner/partition/CAS.
Evaluator FAIL remains deny-dominant. Evaluator infrastructure errors durably reject with a generic
ERROR verdict before the original error is returned; raw error detail is neither persisted nor logged.
`engine/adapter/skillfs.AtomicCatalog`
supplies a complete-generation live Skill tool while preserving existing snapshot sources. Standard
Mecatl composition wires these into its caller-partitioned gRPC/HTTP review surface; an embedder may
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

The engine is a separate Go module inside the Mecatl monorepo, connected via `go.work`. If you develop against a local checkout of Mecatl rather than the published module, set up a `go.work` in your own repo's parent:

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

- [The agent loop](/building/what-you-get/agent-loop.md) — event taxonomy, permission pause/resume, compaction, and terminal states.
- [Permissions & guardrails](/building/what-you-get/permissions.md) — how to configure rules, posture, and the model-backed guardrail layer.
- [API stability](/building/api-stability.md) — what's guaranteed not to break in the engine module you just imported, and how a breaking change is classified and surfaced.
- [Run mecated standalone](mecated.md) — if you want the composition done for you (auth, TLS, gRPC, Prometheus).
- [Cloud-native k8s with mecak8s](mecak8s.md) — stateless Kubernetes deployment backed by Redis and k8s leases.
