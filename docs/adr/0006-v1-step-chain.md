# ADR 0006 — v1 Implementation step-chain

- Status: Historical
- Date: 2026
- Scope: v1 build sequencing — work package decomposition and parallelism plan for the initial harness delivery

## Context

Building mecatl in parallel across multiple engineers required a sequencing plan that prevented interface collisions. The central risk was parallel work targeting concretions rather than frozen ports, causing merge conflicts and rework. The dependency graph had one hard bottleneck (the domain types and port interfaces) and a wide parallel middle (FS, LLM adapters, governance, prompt, store) before converging at the loop and then the API.

## Decision

Freeze all shared types and port interfaces first (WP1), then execute Wave B (five work packages in parallel against frozen ports), then the loop integrator (WP8), then fan out to Subagent and API (WP9/WP10 parallel), then close at the composition root (WP11). Critical path: WP1 → WP2 → WP7 → WP8 → WP10 → WP11. All adapters target ports; nothing targets a concretion except the composition root.

## Consequences

The plan was fully executed; all eleven work packages shipped. The frozen interface set proved sufficient — no mid-build port breaks required. The architecture established here is the one that remains in production. Current behaviour is in `docs/architecture.md`; shipped and deferred items are in `docs/design/PRODUCTION-READINESS.md`.

---

Companion to `ARCHITECTURE.md`. Work packages (WPs) sized for one expert engineer
each. The sequencing rule (doc 08 discipline): **freeze the shared contracts first**,
then build adapters/tools/loop/API in parallel against frozen interfaces.

---

## 0. The frozen contracts (must land before any parallel work)

Parallel work collides unless the **domain types** and the **ports** are frozen up
front. The minimal set that must be stable before WP2+ start:

- Value objects: `ToolCall`, `ToolResult`, `Usage`, `Message`, `Event`, `PendingAsk`,
  `StopReason`.
- Aggregate surface: `Session` method signatures (`BeginTurn`, `RecordAssistant`,
  `RecordToolResults`, `PauseForApproval`, `ResumeWith`, `Cancel`, `StopReason`).
- Ports: `LLMProvider`/`Chunk`/`LLMRequest`, `Tool`/`ToolSpec`,
  `PermissionPolicy`/`PermissionDecision`, `HookRunner`/`HookEvent`/`HookOutcome`,
  `SessionStore`, `EventSink`, `Clock`, `Logger`. (`FileSystem`/`Workspace` ended
  up in `engine/tool`, NOT `engine/port` — moving them to `port` creates a
  `port↔tool` cycle; see the CLAUDE.md gotcha.)

These all live in **WP1**. Everything else depends on WP1 and nothing else depends on
the *implementations* — only on these interfaces.

---

## 1. Dependency DAG

```
                         ┌───────────────────────────────┐
                         │ WP1  Domain core + ports       │   (FOUNDATION — blocks all)
                         │  session, prompt, governance,  │
                         │  tool, port  (types+interfaces)│
                         └───────────────┬───────────────┘
        ┌──────────────┬─────────────────┼─────────────────┬───────────────┐
        ▼              ▼                 ▼                 ▼               ▼
  ┌───────────┐  ┌───────────┐    ┌────────────┐   ┌────────────┐  ┌────────────┐
  │ WP2 FS +  │  │ WP3 Mock  │    │ WP4 Govern │   │ WP5 Prompt │  │ WP6 Store  │
  │ Workspace │  │ LLM +     │    │ -ance impl │   │ assembly + │  │ (mem+jsonl)│
  │ (osfs,    │  │ openai    │    │ (perm,bash │   │ env/CLAUDE │  │            │
  │  memfs)   │  │ adapter   │    │  hooks)    │   │  discovery │  │            │
  └─────┬─────┘  └─────┬─────┘    └─────┬──────┘   └─────┬──────┘  └─────┬──────┘
        │              │                │                │               │
        ▼              │                │                │               │
  ┌───────────┐        │                │                │               │
  │ WP7 Tools │        │                │                │               │
  │ Read/Edit/│        │                │                │               │
  │ Write/Bash│        │                │                │               │
  │ Grep/Glob │        │                │                │               │
  └─────┬─────┘        │                │                │               │
        └──────────────┴────────┬───────┴────────────────┴───────────────┘
                                ▼
                    ┌───────────────────────────┐
                    │ WP8  Agent loop + dispatch │   (needs all ports' fakes; uses
                    │  + permission pause/resume │    WP3 mock, WP7 tools, WP4 gov,
                    │  + compaction seam         │    WP2 memfs, WP6 store)
                    └─────────────┬──────────────┘
                  ┌───────────────┴───────────────┐
                  ▼                                ▼
        ┌───────────────────┐          ┌───────────────────────┐
        │ WP9  Subagent/Task │          │ WP10 API: proto +      │
        │  (tool that runs   │          │  connect-go server +   │
        │   a child loop)    │          │  SSE event relay       │
        └─────────┬──────────┘          └───────────┬───────────┘
                  └───────────────┬─────────────────┘
                                  ▼
                       ┌────────────────────────┐
                       │ WP11  mecated + mecademo    │   (composition root + e2e demo)
                       └────────────────────────┘
```

**Parallelism plan:**
- **Wave A (sequential, blocking):** WP1 alone.
- **Wave B (5-wide parallel):** WP2, WP3, WP4, WP5, WP6 — all depend only on WP1, touch
  disjoint packages, honor frozen ports. WP7 starts as soon as WP2 lands (FS port).
- **Wave C:** WP8 (the loop) starts once WP3 (mock LLM), WP4, WP7 are mergeable; it
  needs their *fakes*, not their full polish.
- **Wave D (2-wide parallel):** WP9 and WP10 both build on WP8's loop entry-point.
- **Wave E:** WP11 wires everything; depends on WP9 + WP10.

Critical path: **WP1 → WP2 → WP7 → WP8 → WP10 → WP11**.

---

## 2. Work packages

### WP1 — Domain core + ports (FOUNDATION)
- **Goal:** Freeze every shared type and interface. No behavior beyond pure aggregate
  methods and value-object constructors. This WP is the contract all others build on.
- **Owns:** `engine/session/*`, `engine/prompt/prompt.go` (types only),
  `engine/governance/*.go` (types only: `PermissionDecision`, `HookEvent`,
  `HookOutcome`, `Effect`, `Scope`, `HookPhase`), `engine/tool/tool.go`,
  `engine/port/*`.
- **Honors:** ARCHITECTURE.md §4–5 signatures verbatim.
- **Tests:** `Session` state-machine table tests (idle→running→awaiting→running→
  completed; cancel from each state; stop-condition trips on Limits). `Usage.CacheHitRate`.
  Value-object immutability (no setters; construct-and-freeze).
- **Done:** `go build ./...` green; ports have godoc; **no `adapter`, `os`, or third-party
  import anywhere under `session/prompt/governance/tool/port`** (CI `depguard` rule added
  here). A `doc.go` per package states allowed imports.

### WP2 — FileSystem + Workspace adapters
- **Goal:** Real-OS and in-memory implementations of `FileSystem`, plus the `Workspace`
  wrapper that scopes paths to a session root and carries the Edit **read-ledger**.
- **Owns:** `internal/adapter/osfs/`, `engine/adapter/memfs/`, the `Workspace` impl.
- **Honors:** `tool.FileSystem`, `tool.Workspace` from WP1 (they live in
  `engine/tool`, not `engine/port` — the `port↔tool` cycle).
- **Tests:** memfs round-trips; path-escape attempts (`../`) are rejected by Workspace;
  read-ledger records reads and detects on-disk change (mtime/hash) for Edit invariant #1.
- **Done:** memfs passes the same conformance suite as osfs (shared `fstest`-style table).

### WP3 — Mock LLM + OpenAI Responses adapter
- **Goal:** (a) `mockllm` — a scripted `LLMProvider` that replays a list of programmed
  turns (text, tool calls, usage) with no network, the backbone of loop testing. (b)
  `openai` — the real adapter translating the Responses API (SSE, function_call /
  function_call_output items, reasoning items, prompt caching, ctx-cancel) into `Chunk`s.
- **Owns:** `engine/adapter/mockllm/`, `provider/openai/`.
- **Honors:** `port.LLMProvider`, `port.Chunk`, `port.LLMRequest` from WP1. **Coordinate
  with the concurrent OpenAI-Responses research agent** — the adapter targets their API
  details but exposes only the frozen port.
- **Tests:** mockllm determinism; openai adapter against recorded SSE fixtures (golden
  files), incl. mid-stream `ctx` cancel and a usage/cache chunk. No live network in CI.
- **Done:** loop (WP8) can run end-to-end on mockllm; openai adapter passes fixture replay.

### WP4 — Governance: permission + bash parsing + hooks
- **Goal:** `PermissionPolicy.Evaluate` (deny→ask→allow across merged `Scope`s), compound-
  Bash splitting + process-wrapper canonicalization (closed list), and the shell-exec
  `HookRunner` (stdin JSON, exit 0=allow/2=block).
- **Owns:** `engine/governance/permission.go`, `governance/bash.go`,
  `internal/adapter/hookexec/`.
- **Honors:** `port.PermissionPolicy`, `port.HookRunner` from WP1.
- **Tests:** deny-beats-allow across scopes (gauntlet #8); `git status && rm -rf` splits
  into two rules (gauntlet, doc 08 #10); `timeout`/`nice` stripped but `docker exec`/`npx`
  **not** stripped; hook exit-2 → `Block` (gauntlet #5); plan-mode tool filter rejects
  Edit/Write/non-RO Bash (gauntlet #3).
- **Done:** the permission/bash table tests cover every doc-08 #10 case.

### WP5 — Prompt assembly + AGENTS.md/CLAUDE.md discovery
- **Goal:** Build the two-layer system prompt: cache-stable prefix (role, tone, tool
  inventory, safety) + volatile suffix (`<env>` block). Discover AGENTS.md/CLAUDE.md and
  return it as a **user message** (not system role — doc 08 #5).
- **Owns:** `engine/prompt/prompt.go` (builder behavior), `prompt/env.go`.
- **Honors:** `prompt.Layered` consumed by `port.LLMRequest`.
- **Tests:** prefix is byte-stable across turns given fixed config (cache invariant,
  gauntlet #6); env block injects cwd/os/model/date; CLAUDE.md emitted as RoleUser.
- **Done:** changing only the date/env does not mutate the cached prefix.

### WP6 — SessionStore adapters
- **Goal:** `memstore` (default, fast) and `jsonlstore` (append-only per-tool-call replay
  log to disk — doc 08 observability seam).
- **Owns:** `internal/adapter/store/`.
- **Honors:** `port.SessionStore`, `port.Logger` from WP1.
- **Tests:** save/load round-trip preserves Conversation + Counters + State; jsonlstore is
  append-only and replayable into an equal Session.
- **Done:** a session can be saved mid-`awaiting` and reloaded to resume.

### WP7 — The 7-tool core kit  *(starts after WP2)*
- **Goal:** Read (line-numbered output), Edit (3 invariants), Write, Bash, Grep, Glob;
  WebFetch stub. Each carries a doc-quality `ToolSpec.Description` and a correct
  `ReadOnly()` flag.
- **Owns:** `internal/adapter/tools/`.
- **Honors:** `tool.Tool`, `port.Workspace` from WP1/WP2.
- **Tests (against memfs):** Read prefixes line numbers; Edit fails on unread file
  (gauntlet #2), on non-match, on non-unique without `replace_all`, succeeds with
  `replace_all`; Write triggers read-before-edit on existing path; Bash output truncation
  + timeout; Grep/Glob result caps; `ReadOnly()` correct per tool (drives WP8 dispatch).
- **Done:** every Edit invariant has a red-then-green test.

### WP8 — Agent loop + dispatch + permission pause/resume + compaction seam
- **Goal:** The use-case heart. `Run(ctx, *Session) <-chan Event`: model call →
  dispatch tools (read-only parallel, mutating serial) → fold results → repeat to stop
  condition. PreToolUse/PostToolUse hook calls around each tool. On `Ask`, emit
  `permission.ask`, enter `StateAwaiting`, block until `ResumeWith`. Single-summary
  `Compactor` at ~80% window.
- **Owns:** `engine/agent/loop.go`, `dispatch.go`, `permission.go`, `compaction.go`.
- **Honors:** all ports; imports **only** domain + `port` (no adapters — CI enforced).
- **Tests (mockllm + memfs + fakes):** full turn cycle; read-parallel/mutate-serial
  ordering (gauntlet #4 — assert two writes never interleave); pause→approve→resume;
  pause→deny→model gets denial; ctx-cancel mid-turn yields `result=cancelled` (gauntlet
  #1); stop on max_turns/max_tool_calls/max_consecutive_failures; compaction preserves
  file paths/plan and drops bodies.
- **Done:** the loop runs a scripted multi-turn session with a tool call and a permission
  ask entirely offline; gauntlet items 1,4,5 demonstrably pass.

### WP9 — Subagent  *(after WP8)*
- **Goal:** A `Subagent` tool that spins up a **child loop** with a fresh Conversation, a
  scoped tool subset, its own Limits, runs to completion, and returns **only its final
  string** as one `ToolResult`. SubagentStop hook fires.
- **Owns:** `engine/agent/subagent.go`, the `Subagent` tool registration.
- **Honors:** reuses WP8's `Run`; `tool.Tool` interface.
- **Tests:** parent sees one ToolResult, never the child's intermediate ToolCalls
  (gauntlet #7); child tool-scope enforced; child cancellation bounded by parent ctx.
- **Done:** gauntlet #7 passes.

### WP10 — API: proto + connect-go server + SSE relay  *(after WP8)*
- **Goal:** `api/proto/mecatl/v1/mecatl.proto`, generated `api/gen`, and the connect-go server
  adapter implementing `Harness`: CreateSession/SendPrompt(stream)/Approve/Cancel/
  GetSession. Relay loop `Event`s to the gRPC server-stream and to SSE for HTTP clients.
- **Owns:** `api/`, `internal/adapter/server/`.
- **Honors:** `session.Event` (serialize, never expose OpenAI types); `port.SessionStore`.
- **Tests:** buf-lint + breaking-change check on proto; connect server test driving a fake
  loop: SendPrompt streams events, Approve resolves a paused ask, Cancel closes the turn;
  HTTP/JSON + SSE round-trip via `connect`'s test client.
- **Done:** a unit test approves a permission prompt over the API and sees the loop resume.

### WP11 — mecated + mecademo (composition root + e2e demo)
- **Goal:** `cmd/mecated` wires concrete adapters to ports (the only place this happens).
  `cmd/mecademo` drives a full session — proving the loop, a tool call, a permission ask
  +approval, and a final result — against the **fake provider by default**, real OpenAI
  with `--openai`/`OPENAI_API_KEY`.
- **Owns:** `cmd/mecated/`, `cmd/mecademo/`.
- **Honors:** imports everything; this is the composition root.
- **Tests:** an e2e test runs `mecademo` against mockllm and asserts the printed event
  sequence contains turn.start, tool.call, tool.result, permission.ask, result=success.
- **Done:** `go run ./cmd/mecademo` prints a full streamed session offline; with `--openai`
  it runs against the live Responses API; all 10 gauntlet items have a passing test
  somewhere in the tree.

---

## 3. Summary

- **One foundation WP (WP1)** freezes domain + ports — the single thing that must land
  before parallel work.
- **Wave B is 5-wide** (WP2–WP6), WP7 follows WP2.
- **WP8 is the integrator**, then **WP9/WP10 fan out**, **WP11 closes**.
- The frozen interface set (WP1) is exactly what keeps the parallel WPs from colliding:
  adapters target ports, the loop targets ports, nobody targets a concretion except the
  composition root in `cmd/`.


---

*Part of the [design docs](../design/README.md). Related: [mecatl — Architecture](0004-v1-architecture.md), [Implementation Notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md).*
