# mecatl

<p align="center">
  <img src="./assets/mecatito.png" alt="Mecatito, the mecatl mascot" width="320">
</p>

A **headless agentic coding harness** in Go — the system around a model that lets it
actually finish a software task: a streaming agent loop, a core tool kit, an enforced
permission model, deterministic hooks, one-shot subagents, prompt caching, and the
production plumbing around them (auth, resilience, observability). It speaks the OpenAI
**Responses API** and the native **Anthropic Messages API** (plus OpenRouter, and
OpenCode Go over the **Chat Completions API**) behind a
provider-agnostic port, and is driven over a gRPC + HTTP/SSE API — or over stdio via the
Agent Client Protocol (`mecated acp`) for editors. An optional terminal UI, **`mecatui`**,
ships as a client of the same API.

> *A decent model with a great harness beats a great model with a bad harness.* The
> leverage is in the harness. mecatl is a small, strict, well-tested implementation of
> that idea, continuously validated by the BDD e2e suite in [`e2e/`](./e2e/) (nightly CI against
> real models via OpenRouter).

## Features

**The loop & tools**
- **Streaming agent loop** with pause, resume, and cancel — every step is a typed `session.Event` (the loop yields events over a channel; the provider port streams `iter.Seq2`).
- **Core tool kit** — Read (line-numbered), Edit (read-before-edit / exact-match / uniqueness invariants), Write, Grep, Glob, **WebSearch** (Exa / Brave / SearXNG backends, degrades gracefully), and a WebFetch stub; plus an **optional** Bash (behind a `CommandRunner` seam, so the harness runs shell-less in a locked-down pod). Plus opt-in tools: **memory** (Remember/Recall/SearchMemory, with a user-scoped trio alongside), **skills** (Skill + a writable SkillDraft quarantine), the **Parallel** delegation tool (N isolated branches), and **ToolSearch** for progressive disclosure.
- **Read-parallel / mutate-serial dispatch** — read-only tools run concurrently; mutating tools never do (a correctness guarantee, not an optimization).
- **Delegation — one-shot, parallel, or a crew.** Three tools share an isolated read-only child loop: **Subagent** runs one isolated child and returns its result (plus an agentId trailer); **Parallel** fans out N isolated branches in forked workspaces and joins them (all / first / judge), returning the winner or all results with the preserved fork-workspace paths; **Team** coordinates a crew over a shared task list, findings ledger, and mailbox. All three honour an opt-in shared token budget (`--max-run-tokens`, default unlimited), a child-concurrency cap, per-call limits/model overrides, and opt-in structured output; Team also has an opt-in team-wide token budget (`--max-team-tokens`, default unlimited).

**Safety & governance**
- **Permission model** — `deny → ask → allow` across merged scopes; a deny in any scope wins; compound-bash and command-substitution aware; plan mode hard-denies mutations. Optional **model-based layer-2 risk classifier** (monotonic, fail-safe). A **child-scoped axis** (`permissions.subagent.{allow,ask,deny}`) tunes delegated children separately from the main session — the operator's knob between "interactive ask" and "auto-deny".
- **Permission pause/resume over the wire** — an `ask` suspends the loop and surfaces on the stream; the client approves and the loop continues. A child's ask surfaces to the interactive parent; in a **headless** deployment an optional bounded **LLM ask-reviewer** can adjudicate borderline child asks (fail-safe, allow-once, deny circuit-breaker) instead of blanket auto-deny.
- **Deterministic hooks** — the full lifecycle (SessionStart, UserPromptSubmit, Pre/PostToolUse, Stop, SubagentStop, plus the team-coordination phases TeammateIdle, TaskCreated, TaskCompleted), JSON event on stdin, exit-code `0` allow / `2` block.
- **Model-backed guardrails** — an optional quarantined checker model inspects tool I/O on configured (phase, tool) matchers: PostToolUse for prompt-injection in inbound web/MCP results, PreToolUse for secret/exfil in outbound args — block / sanitize / advisory, fail-safe, operator-tier-only config.
- **Workspace containment** — file tools are scoped to the session root via `os.Root` (symlink/`..`-escape safe).

**Provider & context**
- **Multi-provider** — OpenAI Responses (stateless `store:false`, reasoning items preserved across turns, cache-stable prompt prefix; any OpenAI-compatible endpoint via a base-URL override), the native Anthropic Messages API, OpenRouter, and OpenCode Go (over the Chat Completions API; any OpenAI-compatible `/v1/chat/completions` endpoint), over an embedded models.dev catalog with per-session provider/model routing, live model listing, and capability intersection.
- **Layered model selection** — semantic model **aliases** as the spine; per-function **slots** that route the internal lightweight calls (compaction summary, ask-reviewer, guardrail checker) to a cheaper model; a **plan slot** that swaps the model on entering/leaving plan mode (the *opusplan* pattern — fixed per turn, re-resolved between turns); **project-overridable** bindings capped by a non-wideable operator **allowlist**; and a **semantic subagent router** — enabled by defining an operator-defined category taxonomy (e.g. large/medium/small engineering → three models) — that classifies a task into a category and mints the child on that category's model — fail-soft, decide-once, same-provider. Composition-only. See [`docs/adr/0030`](docs/adr/0030-model-selection-heuristics.md) + [`0031`](docs/adr/0031-subagent-model-router.md) + [`0042`](docs/adr/0042-taxonomy-gated-model-router.md).
- **Resilience** — retry/backoff + circuit breaker around the provider (never replays a partially-streamed turn); provider errors surface to clients.
- **Context management** — two-layer cache-stable prompt; a pluggable `Compactor` (single-summary default + a tiered snip→strip→collapse→summarize cascade) with a `TokenCounter` seam (heuristic or offline tiktoken).
- **Memory** — conservative tiered memory (Remember/Recall) + optional background "dream" consolidation.
- **Persona / soul** — an optional user-scoped, **agent-read-only** persona fragment (`~/.config/mecatl/soul.md`) injected as turn-0 context; injection-scanned, byte-capped, and fail-soft (no tool can write it).
- **MCP client** — connect to MCP servers over **streaming-HTTP transport only** (stdio is not supported); their tools register namespaced `mcp__server__tool`.

**Interfaces & operations**
- **Three API surfaces, one event model** — a bidi gRPC `Converse` stream, an HTTP/SSE mirror, and ACP over stdio for editors (`mecated acp`), all over the same domain `Event`.
- **Single-shot CI runner & a GitHub Action that implements issues** — `mecatequi` is a headless, forge-agnostic binary: one prompt against the same engine → a git-diff patch + a machine-readable summary + an exit code. A reusable `workflow_call` workflow (a ~15-line caller) — backed by composite actions, with a hand-rolled split-privilege workflow as the escape hatch — turns an issue (label `mecatequi` / comment `@mecatequi`) into a pull request; the agent job holds only the rotatable LLM key and **no** write token, while a separate, agent-code-free job applies the patch as data and opens the PR. See [`docs/adr/0028-mecatequi.md`](docs/adr/0028-mecatequi.md).
- **Auth & limits** — bearer token + optional TLS/mTLS, per-client + global rate limiting, `/healthz`+`/readyz` + gRPC health, graceful shutdown, session auto-resume from a store. Reusable OIDC caller verification is available as the opt-in `github.com/stacklok/mecatl/authn/oidc` module; the engine itself accepts only an already-verified `session.Principal`.
- **Observability** — Prometheus metrics (`/metrics`), OpenTelemetry spans with an OTLP exporter, per-tool-call logging, and an append-only JSONL replay store.
- **Deployment** — `ko`-built static distroless image, PSS-restricted manifests, and a signed release (cosign + SBOM + SLSA provenance).
- **Strict hexagonal/DDD** — the domain and the loop depend only on ports; the OpenAI client, the servers, the filesystem, and the tools are adapters wired only at the composition root.
- **Importable engine core** — `engine/` is its own Go module (`github.com/stacklok/mecatl/engine`) with a tiny dependency closure (`x/sync` + `doublestar` + test-only `goleak`), so an external project can embed the loop and inject its own adapters without dragging in mecatl's heavy require cone. A published **API compatibility contract** (`engine/COMPATIBILITY.md` + a CI api-compat gate) governs breaking changes, and an **event-sourced rehydration** reference fold (`engine/adapter/eventsource`) reconstructs a `Session` from an append-only event log for a consumer whose system-of-record is events rather than snapshots.

## Quick start

Requires **Go 1.26.5** (the `go` directive in `go.mod` auto-fetches it) and
[go-task](https://taskfile.dev). [golangci-lint](https://golangci-lint.run) for linting,
[buf](https://buf.build) only to regenerate the proto.

```sh
task build          # compile → bin/mecated, bin/mecatui, bin/mecademo, and bin/mecatequi
task install        # install mecated + mecatui into GOBIN/GOPATH/bin
task test           # full suite, with -race
task lint           # golangci-lint (parallel-safe) + go vet
```

### Run the demo (fully offline)

`mecademo` drives a real agent loop against a scripted mock provider — no network, no API
key — to show the whole shape: a tool call, a permission prompt with approval, and a final
result with usage accounting.

```sh
go run ./cmd/mecademo
```

```text
=== mecatl demo (offline / mockllm) ===
[001] turn=0 session.init
[002] turn=0 turn.start
[003] turn=0 message.delta  text="I'll read the greeting file first."
[004] turn=0 turn.end
[005] turn=0 tool.call      tool=Read args={"path":"greeting.txt"}
[006] turn=0 tool.result    error=false result="     1\thello from the mecatl demo workspace"
[007] turn=1 turn.start
[008] turn=1 message.delta  text="Now I'll save a short note, which needs your approval."
[009] turn=1 turn.end
[010] turn=1 tool.call      tool=Write args={"path":"note.txt","content":"reviewed the greeting\n"}
[011] turn=1 permission.ask ASK tool=Write reason="approval required by rule for Write (note.txt)"  -> client auto-approves
[012] turn=1 approval
[013] turn=1 tool.result    error=false result="wrote \"note.txt\" (22 bytes)"
[014] turn=2 turn.start
[015] turn=2 message.delta  text="Done: I read greeting.txt and saved note.txt."
[016] turn=2 turn.end
[017] turn=0 result         stop=end_turn text="Done: I read greeting.txt and saved note.txt."
      usage: in=4100 out=125 cacheRead=3600 cacheWrite=0 cacheHitRate=0.88
```

Two more sections follow in the same run: an offline **team** demo (a lead + worker
coordinate and the lead synthesises the consolidated report) and a **background
subagent** demo (detach, harness notice at the turn boundary, collect via
`SubagentStatus`).

### Run the server

```sh
export OPENAI_API_KEY=sk-...
go run ./cmd/mecated serve --openai     # gRPC on 127.0.0.1:8080, HTTP/SSE on 127.0.0.1:8081
```

The server binds loopback by default and is unauthenticated unless you turn auth on.
Common flags (full list in the [`mecated` flag reference](./docs/usage.md)):

```sh
go run ./cmd/mecated serve --openai \
  --openai-base-url https://openrouter.ai/api/v1 --model anthropic/claude-sonnet-4.5 \
  --auth-token "$TOKEN" \                 # bearer auth; --tls-cert/--tls-key/--client-ca for (m)TLS
  --rate-limit 10 \                       # per-client + global token bucket
  --store-dir ./sessions \                # jsonl store → sessions survive restart
  --memory-dir ./mem --memory-consolidate-interval 1h \
  --otlp-endpoint localhost:4317 \        # export OTel traces; /metrics is always on --metrics-addr
  --compaction cascade --tokenizer tiktoken \
  --enable-parallel \                     # default on; --no-bash for shell-less
  --commands-dir .mecatl/commands            # slash-command templates
```

If you bind a non-loopback address without auth, `mecated` logs a prominent warning — put a
token or mTLS (or a NetworkPolicy) in front of it. See the [usage & operator
guide](./docs/usage.md) for every flag, the gRPC `Converse` flow, and `curl`
examples for the HTTP/SSE routes.

### Run the TUI

```sh
ANTHROPIC_API_KEY=... go run ./cmd/mecatui     # hosts an embedded mecated in-process
go run ./cmd/mecatui connect 127.0.0.1:8080  # or point it at a running server
```

See the [mecatui terminal-UI guide](./docs/tui.md) for keybindings, slash commands, and themes.

## Architecture at a glance

Dependencies point inward only. The domain and the application (the loop) know nothing of
OpenAI, gRPC, or the filesystem — those are adapters behind ports, wired together only in
the composition layer (`internal/app`, called from the `cmd/` mains).

```
 driving adapters            domain + application                 driven adapters
 ┌─────────────┐      ┌──────────────────────────────┐      ┌────────────────────┐
 │ gRPC server │─────▶│ agent (loop, dispatch,        │◀─────│ OpenAI / Anthropic │
 │ HTTP / SSE  │      │        pause/resume, subagent)│      │ mock LLM           │
 │ ACP (stdio) │      │   ↓ depends only on ports     │      │ os / in-mem FS     │
 └─────────────┘      │ session · governance · tool · │◀─────│ permission policy  │
 ┌─────────────┐      │ prompt   (domain)             │      │ shell hooks        │
 │ mecatui,demo │────▶│                               │      │ session stores     │
 └─────────────┘      └──────────────────────────────┘      └────────────────────┘
```

- **[Architecture guide](./docs/architecture.md)** — the system in depth: layers, the loop, ports, sequence diagrams, extension points.
- **[Usage & operator guide](./docs/usage.md)** — build/run, the demo, `mecated` flags, the gRPC + HTTP/SSE APIs with examples, permissions, hooks, troubleshooting.
- **[Documentation index](./docs/README.md)** — a short front door routing each audience (contributor, operator, library consumer, researcher) to the right living guide.
- **[`docs/design/PRODUCTION-READINESS.md`](./docs/design/PRODUCTION-READINESS.md)** — the live status tracker (what's done, what's deferred).
- **[mecatui terminal-UI guide](./docs/tui.md)** — the optional `mecatui` terminal client.
- **[`docs/design/README.md`](./docs/design/README.md)** — the indexed catalog of design rationale per feature (`MULTI-PROVIDER.md`, `AGENT-TEAMS-SPIKE.md`, `DIAGNOSTICS.md`, `DRIVERS.md`, `BACKGROUND-SUBAGENTS.md`, `MECATEQUI.md`, the dense per-subsystem `IMPLEMENTATION-NOTES.md`, and the historical spikes).
- **[`AGENTS.md`](./AGENTS.md)** — the coding-agent contract for this repo (layering rules, per-package gotchas, workflow conventions). **If you're an AI agent working in this codebase, start here.**

## Project layout

`engine/` is the importable core (the domain, the ports, the agent loop, and the
in-tree reference adapters), fully self-contained — tests included — and importable as a
library by external consumers with a **published compatibility contract** for its seven
core packages (see [`engine/COMPATIBILITY.md`](engine/COMPATIBILITY.md): a change to the
exported surface fails the `api-compat` gate until the baseline + changelog are updated);
`internal/` holds the heavy adapters and the composition layer.

`engine/` is its **own Go module** (`github.com/stacklok/mecatl/engine`), kept in this
repo as a monorepo via a committed `go.work`. The reusable OIDC adapter is another
opt-in module, `github.com/stacklok/mecatl/authn/oidc`; it carries the token-validation
dependencies so engine-only consumers do not. The root and submodule manifests are
maintained independently. An
external project imports the core directly, e.g.
`import "github.com/stacklok/mecatl/engine/agent"`, and pulls in only that small closure,
not mecatl's heavy dependency cone. See [ADR 0036](docs/adr/0036-engine-module.md).

| Path | Contents |
|---|---|
| `go.work`, `go.mod`, `engine/go.mod`, `authn/oidc/go.mod` | the committed Go workspace and the root, importable-engine, and opt-in OIDC module manifests |
| `authn/oidc` | reusable OIDC bearer validation that projects verified claims into `session.Principal` without exposing ToolHive types ([ADR 0103](docs/adr/0103-oidc-authn-module.md)) |
| `engine/COMPATIBILITY.md`, `engine/CHANGELOG.md`, `engine/api/*.txt` | the engine public-API stability contract: policy, change record, and committed surface snapshots (the `api-compat` gate, [ADR 0037](docs/adr/0037-engine-stability-contract.md)) |
| `engine/session`, `engine/governance`, `engine/tool`, `engine/prompt` | the domain (aggregate, permission/hook types, tool catalog + FS interfaces, prompt assembly) |
| `engine/port` | the port interfaces the loop consumes |
| `engine/agent`, `engine/team` | the agent loop, dispatch, permission pause/resume, compaction, the Subagent/Parallel/Team delegation tools |
| `engine/adapter/*` | reference adapters (stdlib + engine only): `mockllm`, `memfs`, `memstore`, `sessnap`, `permpolicy`, `permstore`, `wallclock`, `nofs`, `search` (a fake search backend), and the five conformance suites (`fsconformance`, `memconformance`, `storeconformance`, `sourceconformance`, `eventlogconformance`) |
| `internal/adapter/*` | heavy adapters: `openai`, `anthropic`, `openrouter`, `llmresilience`, `osfs`, `acp`, `skills`, `agents`, `workspacetrust`, `grpcdriver`, `store/jsonlstore`, `server`, and more — see the directory |
| `internal/app` | the composition layer (`app.Build`): provider registry, catalog assembly, per-session routing |
| `contracts/proto`, `contracts/gen` | gRPC contract + the driver protocol (source of truth) and generated Go |
| `cmd/mecated`, `cmd/mecatui`, `cmd/mecademo`, `cmd/mecatequi` | the server (composition root), the optional TUI client, the demo, and the single-shot headless CI/batch runner |
| `perf/` | the offline scenario perf harness (`task perf:scenarios`), the `allocsgate` CI gate, and `perfconvert`; never imports `internal/` |
| `e2e/` | the live BDD suite (Ginkgo) against real models via OpenRouter (`task e2e`; needs `OPENROUTER_API_KEY`) |
| `.github/actions/mecatequi*`, `.github/workflows/mecatequi*.yml` | the forge glue: three composite actions (build+run, extract-prompt, publish) + the reusable `workflow_call` workflow (the recommended adoption path) + the split-privilege example workflow that runs `mecatequi` against an issue and opens a PR |

## Status

The harness is **effectively production-ready bar one deliberately-deferred item** (an
OS-level process sandbox). The loop, the tools, permissions, hooks, subagents, the API,
auth, resilience, observability, memory, context management, MCP, and a signed release
pipeline are all built, green under tests and `-race` lint, and the build is
continuously live-validated against real frontier models by the e2e suite. See
[`docs/design/PRODUCTION-READINESS.md`](./docs/design/PRODUCTION-READINESS.md) for the
itemized status.

**Deferred / optional** (seams left in place): an **OS-level sandbox** (the `CommandRunner`
port is the seam; Bash is also fully optional, so shell-less deploys avoid the surface).
**MCP is supported over streaming-HTTP transport only — stdio MCP is not, by design.**

## Development

New here? Start with [`AGENTS.md`](./AGENTS.md) (the layering rules, per-package gotchas,
and workflow conventions) and the [architecture guide](./docs/architecture.md) (how the
loop, ports, and adapters fit together). Then:

```sh
task            # list tasks
task ci         # tidy → fmt → lint → test → build
task generate   # regenerate contracts/gen from contracts/proto (needs buf); also refreshes llms.txt
cd engine && go test ./agent/ -run TestFullCycle   # a single engine test (engine/ is its own module — run from engine/)
```

`engine/` is a separate Go module (monorepo via `go.work`), so a `go test ./...` from the
repo root does **not** cover it — run engine tests from `engine/`. `task test` does both,
plus the `GOWORK=off` standalone hygiene proof.

Tests are offline by design (a scripted mock provider + an in-memory filesystem); CI never
needs a live model or network. Changed any Markdown? Run `task docs` before committing — the
generated `llms.txt` and the doc-link gate will otherwise fail CI.
