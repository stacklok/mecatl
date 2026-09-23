# ADR 0004 — v1 Architecture: hexagonal DDD harness

- Status: Historical
- Date: 2026
- Scope: the entire mecatl system shape — package layout, domain model, ports, adapters, API surface

> This records the v1 shape as it was DECIDED, not the current system — the scope
> line above spans the whole codebase, so it's easy to mistake for a live
> reference. For how mecatl actually works today, see
> [`docs/architecture.md`](../architecture.md) and `docs/architecture/*.md`.

## Context

mecatl needed a shape that would keep the agent loop provider-agnostic, unit-testable without a network, and extensible without touching the core. The primary risk was coupling: the LLM provider type, gRPC types, and `os` imports bleeding into the domain. A secondary risk was picking the wrong API surface (connect-go vs grpc-go) before the bidi pause/resume flow was understood.

## Decision

Adopt hexagonal architecture with a DDD core. Dependencies point inward only: domain packages (`session`, `prompt`, `governance`, `tool`) never import adapters, LLM SDKs, gRPC, or `os`. Ports live where consumed (`engine/port`). Adapters meet ports only at the composition root. The API is grpc-go + buf (bidi `Converse` stream for pause/resume) with a thin hand-rolled SSE adapter for HTTP clients. Server-side session state is held by the harness (not the provider). Two machine-checked enforcement mechanisms — a depguard allowlist and a whole-graph DAG test — prevent layer violations.

## Consequences

The domain is fully unit-testable offline via reference adapters (`mockllm`, `memfs`, etc.). New LLM providers, tools, and storage backends slot in without touching the loop. The `FileSystem`/`Workspace` types must live in `engine/tool` (not `engine/port`) to avoid a `port↔tool` import cycle. The composition root (`internal/app`, `cmd/`) is the only place adapters meet ports. Current behaviour is in `docs/architecture.md`; shipped and deferred items are in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

mecatl is a **headless agentic coding-harness**: a service/library that runs the
agent loop, executes coding tools, enforces permissions and hooks, and streams typed
events. There is no TUI. Clients drive it over gRPC or HTTP.

The whole design exists to satisfy one shape (doc 08 TL;DR): *a single streaming
agent loop, ~7 core tools, an enforced plan/act gate, one-shot subagents with isolated
context, deterministic hooks, a permission system that is deny → ask → allow across
merged scopes, and prompt caching at every stable boundary* — with the **LLM provider,
the servers, the filesystem, and the tools all behind ports** so the core is
provider-agnostic and unit-testable against fakes.

---

## 1. Architectural style

**Hexagonal (ports & adapters) with a DDD core.** Dependencies point inward only:

```
                         ┌─────────────────────────────────────────────┐
   ADAPTERS (driving)    │                  DOMAIN                      │   ADAPTERS (driven)
                         │  (no infra imports, no provider imports)     │
  ┌───────────────┐      │                                             │   ┌────────────────────┐
  │ gRPC server   │──┐   │   agent  (the loop / use-cases)             │   │ OpenAI Responses   │
  │ (grpc-go)     │  │   │   ├─ orchestrates Session aggregate         │◀──│ adapter  (LLMPort) │
  └───────────────┘  │   │   ├─ depends on PORTS only:                 │   └────────────────────┘
  ┌───────────────┐  ├──▶│   │    LLMProvider, Tool, PermissionPolicy, │   ┌────────────────────┐
  │ HTTP/SSE      │──┘   │   │    HookRunner, SessionStore, Clock,     │◀──│ tools adapter      │
  │ (hand-rolled) │      │   │    Logger, EventSink                    │   │ (Read/Edit/Bash..) │
  └───────────────┘      │   │                                         │   └────────────────────┘
  ┌───────────────┐      │   session  (aggregate: Session, Conversation│   ┌────────────────────┐
  │ demo CLI      │─────▶│   │           Turn, Message, ToolCall,      │◀──│ filesystem adapter │
  │ (cmd/mecademo) │      │   │           Permission, Hook, Usage)      │   │ (OS fs / mem fake) │
  └───────────────┘      │                                             │   └────────────────────┘
                         │   prompt  (system-prompt assembly,          │   ┌────────────────────┐
                         │            cache-stable prefix + suffix)    │◀──│ session store      │
                         └─────────────────────────────────────────────┘   │ (mem / jsonl)      │
                                                                            └────────────────────┘
```

The **domain** (`session`, `prompt`) holds entities, value objects, and the *port
interfaces*. The **application** (`agent`) is the use-case layer: it is the agent loop
and knows only ports. **Adapters** implement ports and depend inward on the domain;
nothing in the domain imports an adapter, the OpenAI SDK, grpc-go, or `os`.

Ports are defined **where they are consumed** (in `engine/port`, imported by `agent`),
per Go idiom "accept interfaces". Adapters return concrete structs.

---

## 2. Bounded contexts

This is a small system; over-contexting it would be layer hypertrophy. There are
**three** bounded contexts, with one ubiquitous language each.

| Context | Owns | Language |
|---|---|---|
| **Agent Session** | the run lifecycle, conversation, turns, the loop, stop conditions, compaction seam | Session, Conversation, Turn, Message, ToolCall, ToolResult, Usage, Event |
| **Governance** | permission evaluation, hooks, plan-mode gating | PermissionDecision (deny/ask/allow), Scope, Rule, HookEvent, HookOutcome |
| **Tooling** | the tool catalog and its execution invariants | Tool, ToolSpec, ReadOnly, Edit invariants, Workspace/FileSystem |

The LLM provider is **not** a bounded context — it is a port the Agent Session context
depends on. The two servers are driving adapters, also not contexts.

**Cross-context rule:** Governance and Tooling never import Agent Session internals;
they exchange the shared value objects (`ToolCall`, `ToolResult`, `HookEvent`) defined
in the domain `session`/`tool` packages. There is no `billing.User` / `auth.User`
style duplication risk here because all three contexts share one `ToolCall` type — that
is correct, because a ToolCall *is* the same concept across all three.

---

## 3. Go package layout

`engine/` holds the importable core — the domain packages, the port interfaces, the
agent loop, and the in-tree reference adapters (`engine/adapter/*`) — fully
self-contained (tests included) and intended to be importable as a library by external
consumers. `internal/` holds everything not meant as a stable public API (the heavy
adapters and the composition layer). `pkg/` is **not** used — the proto/HTTP API
remains the primary public surface.

```
github.com/stacklok/mecatl
├── contracts/
│   ├── proto/mecatl/v1/harness.proto      # gRPC service + messages (source of truth)
│   └── gen/go/                          # generated Go (grpc-go + protobuf, buf)
├── cmd/
│   ├── mecated/                           # the server binary (gRPC + HTTP/SSE)
│   ├── mecatui/                          # the optional gRPC client TUI (embeds app.Build w/o --server)
│   ├── mecatequi/                        # single-shot headless runner (one prompt -> patch + summary + exit code)
│   └── mecademo/                        # the demo driver (fake provider default)
├── engine/                             # the importable core: domain + ports + agent loop + reference adapters
│   ├── session/                        # DOMAIN: Session aggregate + value objects
│   │   ├── session.go                  #   Session (root), state machine, StopReason
│   │   ├── conversation.go             #   Conversation, Message, Turn
│   │   ├── toolcall.go                 #   ToolCall, ToolResult (shared value objects)
│   │   ├── usage.go                    #   Usage (value object)
│   │   └── event.go                    #   Event taxonomy + ResultPayload (now has Error)
│   ├── prompt/                         # DOMAIN: two-layer system prompt assembly + seams
│   │   ├── prompt.go                   #   Layered (stable prefix + volatile suffix)
│   │   ├── builder.go                  #   Build; AGENTS.md/CLAUDE.md discovery
│   │   ├── env.go                      #   <env> block
│   │   ├── instructions.go             #   InstructionAssembler seam + RootAssembler
│   │   └── command.go                  #   CommandExpander seam (slash commands) + DirCommandExpander
│   ├── governance/                     # DOMAIN: permission + hook + plan-mode logic
│   │   ├── permission.go               #   PermissionDecision, Effect (deny/ask/allow)
│   │   ├── evaluator.go                #   Evaluator: Rule, Scope precedence, merge/eval
│   │   ├── bash.go                     #   compound-command split + wrapper canonicalization
│   │   └── hookevent.go                #   HookEvent, HookOutcome, lifecycle phases
│   ├── tool/                           # DOMAIN: Tool contract + seams + catalog
│   │   ├── tool.go                     #   Tool, ToolSpec, Disclosable, FileSystem,
│   │   │                               #     Workspace, CommandRunner, MemoryStore
│   │   ├── catalog.go                  #   Catalog (name→Tool), plan-mode filtering
│   │   ├── toolsearch.go               #   Search (the ToolSearch progressive-disclosure tool)
│   │   └── isolation.go                #   WorkspaceForker seam (fork-join)
│   ├── port/                           # PORTS: interfaces the agent consumes
│   │   ├── llm.go                      #   LLMProvider + stream chunk types
│   │   ├── store.go                    #   SessionStore
│   │   ├── hookrunner.go               #   HookRunner
│   │   ├── permission.go               #   PermissionPolicy
│   │   ├── clock.go                    #   Clock
│   │   └── log.go                      #   Logger, EventSink
│   ├── adapter/                        # REFERENCE adapters (stdlib + engine only; travel with the core)
│   │   ├── mockllm/                    #   scripted fake LLMProvider (no network)
│   │   ├── memfs/                      #   in-memory FileSystem fake
│   │   ├── fsconformance/              #   shared FileSystem conformance suite
│   │   ├── memstore/                   #   in-memory SessionStore (default)
│   │   ├── sessnap/                    #   session Snapshot DTO (memstore + jsonlstore share it)
│   │   ├── permpolicy/                 #   PermissionPolicy over governance.Evaluator
│   │   └── permstore/                  #   in-memory always-allow rule store
│   └── agent/                          # APPLICATION: the loop (use-case layer)
│       ├── loop.go                     #   Engine/Deps, Run(...) streaming the Event channel
│       ├── dispatch.go                 #   read-parallel / mutate-serial dispatch + hooks
│       ├── permission.go               #   askRegistry: ask-pause/resume handshake
│       ├── hooks.go                    #   SessionStart / UserPromptSubmit / Stop lifecycle
│       ├── compaction.go               #   Compactor seam + HeuristicCompactor (default)
│       ├── cascade.go                  #   CascadeCompactor (snip→strip→collapse→summarize)
│       ├── tokencount.go               #   TokenCounter seam + HeuristicTokenCounter
│       ├── subagent.go                 #   Task: fresh context, scoped tools, one-shot
│       └── parallel.go                 #   ParallelTool: fork-join fan-out (NewParallelTool)
├── internal/
│   └── adapter/                        # HEAVY ADAPTERS: implement ports / seams
│       ├── openai/                     #   LLMProvider over OpenAI Responses API (SSE)
│       ├── llmresilience/              #   retry/backoff + circuit-breaker decorator (LLMProvider)
│       ├── permclassify/               #   model-based layer-2 risk classifier (PermissionPolicy)
│       ├── tools/                      #   Read, Edit, Write, Grep, Glob, WebFetch, WebSearch + optional Bash
│       ├── toolkit/                    #   shared tool mechanics (arg parse, output cap, schema)
│       ├── osfs/                       #   FileSystem (os.Root-confined) + CommandRunner
│       ├── store/                      #   jsonlstore (append-only JSONL replay log)
│       ├── hookexec/                   #   shell-exec HookRunner (stdin JSON, exit-code)
│       ├── memory/                     #   file-backed MemoryStore + Remember/Recall tools
│       ├── dream/                      #   opt-in memory-consolidation (sleep) service
│       ├── forker/                     #   WorkspaceForker (git-worktree / copy isolation)
│       ├── tokenizer/                  #   offline tiktoken TokenCounter
│       ├── mcp/                        #   MCP client (streaming-HTTP only) → namespaced tools
│       ├── telemetry/                  #   EventSink/Logger → Prometheus, OTel spans, OTLP
│       └── server/                     #   gRPC + HTTP/SSE service, auth/mTLS, health, rate limit
└── docs/design/                        # this file + STEP-CHAIN.md + PRODUCTION-READINESS.md
```

**Allowed-imports matrix** (the contract; **machine-enforced** — see "Layering enforcement" below):

| Package | May import |
|---|---|
| `session`, `prompt`, `governance`, `tool` (domain) | stdlib, other domain packages. **Never** `adapter`, `agent`, `contracts`, `os`, OpenAI SDK, grpc-go. |
| `port` | domain packages + stdlib (`context`, `io`, `iter`, `time`). Nothing else. (`port` imports `tool` and `prompt` because `LLMRequest` carries `[]tool.ToolSpec` and `prompt.Layered`.) |
| `team` (domain) | `session` + stdlib. The agent-team value types. |
| `agent` (application) | domain + `port` + `team` + stdlib + `golang.org/x/sync/errgroup`. **Never** `adapter` or `contracts`. (Tests may import adapters — see the `$test` carve-out below.) |
| `adapter/*` | domain + `port` + the specific external lib it adapts. The loop never imports an adapter; two driven adapters legitimately reference `agent` types they implement/drive — `tokenizer` satisfies `agent.TokenCounter`, and `server` drives `agent.Engine`/`agent.Run`. |
| `contracts/gen` | generated; protobuf + grpc-go runtime only. |
| `cmd/*` | everything — this is the composition root where wiring happens. |

The only place concrete adapters meet ports is `cmd/` (dependency injection by hand;
no DI framework — explicit constructors, doc 03 "pass dependencies explicitly").

### Layering enforcement (two mechanisms)

The allowed-imports matrix above is enforced by two complementary, machine-checked
mechanisms — both run under `task lint` / `task test`, neither relies on review:

1. **depguard allowlist** (`.golangci.yml`, per-file). One `list-mode: strict` rule per
   core tier (`core-domain-leaf`, `core-tool`, `core-prompt`, `core-port`, `core-team`,
   `core-agent`), each allowing only `$gostd` + the exact mecatl-core packages that tier
   legitimately imports, plus an explicit `os` **deny** (depguard applies deny over allow,
   patching the gap that `os` is stdlib but banned in core). It is an **allowlist**, not a
   denylist of known adapters: a *new* heavy adapter import is rejected by default. depguard
   sees one file's direct imports at a time.
2. **DAG-assertion test** (`engine/arch/layering_test.go`, whole-graph). Walks the
   **non-test** import graph of the seven core packages and asserts (a) no core package
   transitively reaches an adapter / `contracts/gen` / `internal/app` / an LLM SDK / grpc,
   (b) no import **cycle** among the core packages (the property a per-file linter cannot
   observe), and (c) `port` / `agent` direct imports stay within their allow-sets. Hermetic
   and offline (resolves from the on-disk module via `go/build`; no network, no `go.mod`
   change), mirroring `engine/prompt/layering_test.go`.

**Carve-outs** these encode deliberately: `port.PermissionPolicy` is implemented in the
`permpolicy` **adapter** (engine/adapter, not `governance`, which can't import `session`);
`FileSystem` / `Workspace` live in `tool` (moving them to `port` makes a `port↔tool`
cycle); and **core test files may import the `engine/adapter/*` reference adapters**
(`memfs` / `mockllm` / …) to run the loop offline — so the core depguard rules exclude
`$test` and the DAG test reads non-test imports only. The engine tree is self-contained
including tests (nothing under `engine/` imports `internal/...`); integration tests that
need a heavy adapter live next to that adapter under `internal/adapter/`.

---

## 4. Domain model

### 4.1 Session aggregate

`Session` is the aggregate root. Outside code holds a `SessionID`, never an inner
entity — "reach through the root." All mutation of the Conversation goes through Session
methods so invariants (turn counting, stop conditions, state transitions) hold.

```go
// engine/session
type SessionID string

type State string
const (
    StateIdle      State = "idle"      // created, no turn running
    StateRunning   State = "running"   // a turn is in flight
    StateAwaiting  State = "awaiting"  // paused on a permission "ask"
    StateCompleted State = "completed"
    StateFailed    State = "failed"
    StateCancelled State = "cancelled"
)

type PermissionMode string
const (
    ModeDefault PermissionMode = "default"
    ModePlan    PermissionMode = "plan"        // read-only toolset enforced
    ModeAccept  PermissionMode = "acceptEdits"
)

type Session struct {
    ID            SessionID
    State         State
    Mode          PermissionMode
    Conversation  *Conversation
    Limits        Limits          // value object: stop conditions
    Counters      Counters        // turns, toolCalls, consecutiveFailures
    Workspace     string          // root dir for tools (cwd)
    CreatedAt     time.Time
    pending       *PendingAsk     // set iff State==Awaiting
}

type Limits struct {                  // doc 08: "no stop conditions" anti-pattern
    MaxTurns             int
    MaxToolCalls         int
    MaxConsecutiveFailures int
}
```

`Session` exposes intention-revealing methods, not setters:
`BeginTurn()`, `RecordAssistant(Message)`, `RecordToolResults([]ToolResult)`,
`PauseForApproval(PendingAsk)`, `ResumeWith(PermissionDecision)`, `Cancel()`,
`StopReason() (StopReason, bool)`.

### 4.2 Conversation / Turn / Message

```go
type Role string
const (RoleSystem Role="system"; RoleUser Role="user"; RoleAssistant Role="assistant"; RoleTool Role="tool")

type Message struct {
    Role      Role
    Text      string
    ToolCalls []ToolCall    // assistant messages requesting tools
    ToolResult *ToolResult  // tool-role messages
    Reasoning  string       // provider reasoning item, opaque, replayed verbatim
}

type Conversation struct { Messages []Message }       // model-visible history

type Turn struct {                                     // one model call + its tools
    Index     int
    Assistant Message
    Results   []ToolResult
    Usage     Usage
}
```

### 4.3 Shared value objects (cross-context)

```go
type ToolCallID string
type ToolCall struct {                 // immutable; produced by LLM, consumed by tool+gov
    ID   ToolCallID
    Name string
    Args json.RawMessage               // tool-specific, validated by the Tool
}

type ToolResult struct {               // immutable; paired to ToolCall by ID
    CallID  ToolCallID
    Content string                     // already token-shaped/truncated by the tool
    IsError bool
}

type Usage struct {                    // value object, doc 07 §12 accounting
    InputTokens, OutputTokens          int
    CacheReadTokens, CacheWriteTokens  int
}
func (u Usage) CacheHitRate() float64
```

### 4.4 Governance value objects

```go
// engine/governance
type Effect string
const (Deny Effect="deny"; Ask Effect="ask"; Allow Effect="allow")

type PermissionDecision struct {       // result of evaluating a ToolCall across scopes
    Effect Effect
    Reason string                      // teaches the model on deny (doc 07 §11)
}

type Scope int  // Managed > CLI > LocalProject > SharedProject > User  (doc 03 precedence)

// HookEvent / lifecycle  (doc 03 hook table). All six phases now fire: the
// per-tool PreToolUse/PostToolUse in agent/dispatch.go, and SessionStart /
// UserPromptSubmit / Stop / SubagentStop in agent/hooks.go + agent/subagent.go.
type HookPhase string
const (
    PhaseSessionStart    HookPhase="SessionStart"
    PhaseUserPromptSubmit HookPhase="UserPromptSubmit"
    PhasePreToolUse      HookPhase="PreToolUse"
    PhasePostToolUse     HookPhase="PostToolUse"
    PhaseStop            HookPhase="Stop"
    PhaseSubagentStop    HookPhase="SubagentStop"
)
type HookEvent struct { Phase HookPhase; Tool string; Input json.RawMessage; SessionID string }
type HookOutcome struct { Block bool; Message string; Mutated json.RawMessage } // exit 0=allow,2=block
```

---

## 5. Ports

Small interfaces, `context.Context` first, `error` last (where applicable). Each port
has a fake in `internal/adapter/*` so the loop is unit-testable with no network and no
disk.

### 5.1 LLMProvider — the provider-agnostic seam

The loop must never see an OpenAI type. The provider yields a stream of **provider-
neutral chunks**; the loop assembles them into a domain `Message`. The OpenAI Responses
API specifics (function_call / function_call_output items, reasoning items, automatic
prompt caching, SSE framing) live entirely inside `adapter/openai`. The same port is
the seam the `llmresilience` decorator (§5.5) and the `permclassify` classifier wrap.

```go
// engine/port
type LLMRequest struct {
    System    prompt.Layered        // stable prefix + volatile suffix (for cache breakpoints)
    Messages  []session.Message     // conversation history
    Tools     []tool.ToolSpec       // schemas; stable across turns for caching
    Model     string
}

type ChunkKind int
const (
    ChunkText ChunkKind = iota   // assistant text delta
    ChunkReasoning               // reasoning item delta (opaque, replayed back)
    ChunkToolCall                // a fully-formed tool call (emitted once assembled)
    ChunkUsage                   // terminal usage/cache accounting
    ChunkDone                    // end of stream; carries StopReason
)

type Chunk struct {
    Kind      ChunkKind
    Text      string
    ToolCall  *session.ToolCall
    Usage     *session.Usage
    Stop      session.StopReason
}

// LLMProvider streams chunks until ctx is cancelled or the model stops.
// Cancellation is via ctx — this is how the API "cancel" verb interrupts a turn.
type LLMProvider interface {
    Stream(ctx context.Context, req LLMRequest) (iter.Seq2[Chunk, error], error)
}
```

(Go 1.26 `iter.Seq2` gives us the async-generator shape doc 08 #1 demands, natively.)

### 5.2 Tool — the catalog contract

```go
// engine/tool
type ToolSpec struct {                 // what the model sees (doc 07 §9: descriptions are docs)
    Name        string
    Description string                 // when-to-use / when-not / example / limits
    Schema      json.RawMessage        // JSON schema for Args
}

type Tool interface {
    Spec() ToolSpec
    ReadOnly() bool                    // drives parallel-vs-serial dispatch (doc 08 #4)
    Execute(ctx context.Context, in ToolCall, ws Workspace) (ToolResult, error)
}
```

`Workspace` is the injected FS seam (real OS fs / mem fake), so every tool is testable:

```go
// engine/tool  (FileSystem/Workspace live here, scoped to a session root;
// kept in the Tooling context — not engine/port — to avoid a port↔tool import cycle)
type FileSystem interface {
    Read(ctx context.Context, path string) ([]byte, error)
    Write(ctx context.Context, path string, data []byte) error
    Stat(ctx context.Context, path string) (FileInfo, error)
    Glob(ctx context.Context, pattern string) ([]string, error)
}

// Workspace is the session-scoped seam every Tool executes against (Root, Read,
// Write, Stat, Glob, Grep + the Edit read-ledger via RecordRead/WasReadUnchanged).
// NOTE: Workspace no longer exposes RunCommand — command execution moved out to
// the CommandRunner seam (below) so the whole surface is optional.
```

**Command execution is a separate, optional seam** (`tool.CommandRunner`, *not*
`engine/port`). The agent loop never references it; only the Bash tool depends on
it, which is what makes Bash — and therefore any command execution — optional in
the catalog. The osfs adapter ships a local `/bin/sh` runner; an implementation may
also run remotely or refuse with `tool.ErrNoShell`. A shell-less deploy simply omits
the Bash tool. An OS sandbox (Landlock/seccomp/Seatbelt) slots in here as a wrapping
`CommandRunner` adapter without touching the loop.

```go
// engine/tool
type CommandRunner interface {
    Run(ctx context.Context, command string) (CommandResult, error) // exit code in result; ErrNoShell if none
}
```

Edit's three invariants (read-before-edit, exact-match, uniqueness — doc 08 #3) are
enforced **inside the Edit tool** against the per-session read-ledger the `Workspace`
carries (`RecordRead`/`WasReadUnchanged`).

### 5.3 Remaining ports

```go
type SessionStore interface {                      // server-side state (decision: stateful)
    Save(ctx context.Context, s *session.Session) error
    Load(ctx context.Context, id session.SessionID) (*session.Session, error)
}

type HookRunner interface {                        // exit 0 allow / 2 block (doc 03)
    Run(ctx context.Context, ev governance.HookEvent) (governance.HookOutcome, error)
}

type PermissionPolicy interface {                  // deny→ask→allow across merged scopes
    Evaluate(ctx context.Context, mode session.PermissionMode, c session.ToolCall) governance.PermissionDecision
}

type EventSink interface { Emit(Event) }           // loop → API stream
type Clock interface { Now() time.Time }
type Logger interface { ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration) }
```

### 5.4 Application seams (in `engine/agent` / `engine/prompt` / `engine/tool`)

Beyond the seven core ports, the loop and the prompt layer expose small
**default-on, swap-in** interfaces so each harness pattern is pluggable without a
core change. Each ships a network-free default implementation:

| Seam | Where | Default | Swap-in |
|---|---|---|---|
| `Compactor` | `agent/compaction.go` | `HeuristicCompactor` (single-summary) | `CascadeCompactor` (tiered) |
| `TokenCounter` | `agent/tokencount.go` | `HeuristicTokenCounter` (chars/4) | `tokenizer.Counter` (offline tiktoken) |
| `InstructionAssembler` | `prompt/instructions.go` | `RootAssembler` (AGENTS.md/CLAUDE.md discovery) | custom scoped-context assembly (pattern 2) |
| `CommandExpander` | `prompt/command.go` | `NoopExpander` | `DirCommandExpander` (slash commands, pattern 9-ish) |
| `Disclosable` + `tool.Search` (`ToolSearch`) | `tool/tool.go`, `tool/toolsearch.go` | always-listed tools | hide-until-searched progressive disclosure (pattern 9) |
| `CommandRunner` | `tool/tool.go` | osfs local `/bin/sh` | remote / sandboxed / `ErrNoShell` |
| `MemoryStore` | `tool/tool.go` | (off) | `memory.Store` (Remember/Recall, pattern 3) |
| `WorkspaceForker` | `tool/isolation.go` | (off) | `forker` (git-worktree / copy, pattern 8) |

### 5.5 Adapter inventory & the decorator idiom

Adapters fall into three shapes:

- **Port implementations** — `openai`/`mockllm` (`LLMProvider`), `osfs`/`memfs`
  (`FileSystem`+`CommandRunner`), `memstore` (engine/adapter) + `jsonlstore` (`SessionStore`,
  `jsonlstore` also `Logger`), `hookexec` (`HookRunner`), `permpolicy`
  (`PermissionPolicy`), `telemetry` (`EventSink`+`Logger`), the `tools` catalog,
  `memory`/`forker` (tools/seams).
- **Decorators over a port** — both wrap an inner port and return the *same*
  interface, so they compose transparently at the composition root:
  - `llmresilience.Wrap(inner port.LLMProvider, cfg) port.LLMProvider` — retry/
    exponential backoff + a circuit breaker. Key invariant: **no replay after the
    first chunk** — it only retries while *establishing* the stream (connect +
    first chunk); once bytes flow it never re-issues, so the model never sees a
    duplicated partial turn. Surfaces `BreakerError`/`ExhaustedError`.
  - `permclassify.Wrap(inner port.PermissionPolicy, llm, cfg) port.PermissionPolicy`
    — an optional model-based **layer-2** risk classifier. It is **monotonic**
    (can only tighten: an inner Allow may be raised to Ask/Deny, never relaxed) and
    **fail-safe** (a classifier error keeps the inner decision). Layer-1 is the
    deterministic `governance` rules; this is the second opinion on top.
- **Background services** — `dream.Consolidator` (opt-in sleep/consolidation over a
  `MemoryStore`: merges duplicates, drops stale entries, never invents keys,
  fail-safe; `RunPeriodically`).

---

## 6. The streaming event model

`Event` is **domain-owned** (`engine/session/event.go`) and shared by the loop and the
API. The API serializes it to proto; it is never an OpenAI type. This is the single
event taxonomy doc 08 #1 calls for.

```go
type EventType string
const (
    EvSessionInit  EventType = "session.init"
    EvTurnStart    EventType = "turn.start"
    EvMessageDelta EventType = "message.delta"   // streamed assistant text
    EvToolCall     EventType = "tool.call"       // a tool is about to run
    EvToolResult   EventType = "tool.result"
    EvPermissionAsk EventType = "permission.ask" // loop paused; client must approve/deny
    EvHook         EventType = "hook"            // hook fired (PreToolUse blocked, etc.)
    EvCompaction   EventType = "compaction"      // compaction boundary crossed
    EvResult       EventType = "result"          // terminal: success / max_turns / error / cancelled
)

type Event struct {
    Type     EventType
    Seq      int64
    Turn     int
    Text     string
    ToolCall *ToolCall
    ToolResult *ToolResult
    Ask      *PendingAsk        // on permission.ask: id, tool, args, reason
    Result   *ResultPayload     // on result: StopReason + final text + Usage
    Usage    *Usage
}
```

The loop runs as a producer goroutine writing `Event`s to a channel; the server adapter
relays them to the gRPC server-stream / HTTP SSE. On `permission.ask` the loop blocks in
`StateAwaiting` until the client calls `Approve`/`Deny`, which `ResumeWith` unblocks.
Cancellation is a `ctx` cancel propagated to `LLMProvider.Stream` and the running tool.

---

## 7. The API surface

**Decision (revised — the downstream consumer convention):** use **grpc-go + buf + protovalidate**,
mirroring `a downstream consumer`'s `contracts/proto` + `contracts/gen/go` layout and its
`AgentLoopService.Converse` bidi pattern. Rationale: house style, and the bidi stream is
the cleanest expression of pause/resume/cancel — the approval frame returns on the *same*
stream that is emitting events, so no out-of-band correlation is needed. (This supersedes
the earlier connect-go sketch.)

**Decision: server-side conversation state.** Permission "ask" pause/resume and mid-turn
cancellation require the harness to suspend the loop across frames; the client holds only
a `session_id` and drives the run over the stream.

**Two surfaces, one domain `Event`:**
- **gRPC (primary):** a bidi `Converse` stream (the downstream consumer's shape) carries the whole run.
- **HTTP (pragmatic):** grpc-gateway cannot map *bidi*, so HTTP is served by a thin hand-
  rolled SSE adapter over the same domain `Event` + the same application service — a
  server-streaming `Prompt` (SSE) plus unary `Approve`/`Cancel`. Both surfaces call the
  identical `agent` use-case; neither sees an OpenAI type.

### 7.1 Proto sketch (`contracts/proto/mecatl/v1/harness.proto`, go_package → `contracts/gen/go`)

```proto
syntax = "proto3";
package mecatl.v1;
import "buf/validate/validate.proto";

service HarnessService {
  // Unary setup/inspection.
  rpc CreateSession (CreateSessionRequest) returns (CreateSessionResponse);
  rpc GetSession    (GetSessionRequest)    returns (Session);

  // Converse — drive one run. First frame MUST be Prompt; then zero or more
  // ResumeApproval / Cancel frames. Server streams Events until result.
  rpc Converse (stream ConverseRequest) returns (stream ConverseResponse);
}

message ConverseRequest {
  oneof kind {
    Prompt          prompt          = 1;   // mandatory first frame
    ResumeApproval  resume_approval = 10;  // resolves a permission.ask
    Cancel          cancel          = 11;  // aborts the in-flight turn
  }
}
message ConverseResponse { Event event = 1; }

message Prompt {
  string session_id = 1 [(buf.validate.field).required = true];
  string text       = 2 [(buf.validate.field).required = true];
}
message ResumeApproval { string ask_id = 1 [(buf.validate.field).required = true]; bool allow = 2; }
message Cancel {}

message Event {
  string type = 1;            // mirrors session.EventType
  int64  seq  = 2;
  int32  turn = 3;
  string text = 4;
  ToolCall   tool_call   = 5;
  ToolResult tool_result = 6;
  PermissionAsk ask      = 7; // carries ask_id echoed back in ResumeApproval
  Result result          = 8;
  Usage  usage           = 9;
}
```

### 7.2 HTTP/SSE mapping (thin adapter, same domain Event)

| HTTP | Maps to | Notes |
|---|---|---|
| `POST /v1/sessions` | CreateSession | JSON body → session_id |
| `GET  /v1/sessions/{id}` | GetSession | JSON snapshot |
| `POST /v1/sessions/{id}/prompt` | Converse(Prompt …) | response is `text/event-stream` of Events |
| `POST /v1/sessions/{id}/approve` | Converse(ResumeApproval …) | resolves the paused ask out-of-band on the SSE run |
| `POST /v1/sessions/{id}/cancel` | Converse(Cancel) | cancels the in-flight turn (ctx cancel) |

The bidi gRPC stream and the HTTP-SSE-plus-unary surface are two adapters over the same
`agent` application service and the same `session.Event`; closing either stream cancels
the run `ctx`, which the loop observes.

---

## 8. Where each gauntlet item is enforced (doc 08 closing test)

| # | Gauntlet check | Enforced in |
|---|---|---|
| 1 | Loop can pause/resume/cancel | `agent/loop.go` (channel producer) + `agent/permission.go` (StateAwaiting) + `ctx` to `LLMProvider.Stream` |
| 2 | Edit errors if file not read this session | `adapter/tools` Edit tool against the per-session read-ledger in `Workspace` |
| 3 | Plan mode denies Edit/Write/non-RO Bash at harness level | `tool/catalog.go` plan-mode filter + `governance` PreToolUse, before dispatch |
| 4 | Compaction preserves paths/decisions, drops file bodies | `Compactor` seam (`agent/compaction.go`): `HeuristicCompactor` (default) preserves goal + touched paths; `CascadeCompactor` (`agent/cascade.go`) adds the tiered snip→strip→collapse→summarize cascade |
| 5 | PreToolUse hooks fire and block on exit 2 | `agent/dispatch.go` calls `HookRunner` before each tool; `adapter/hookexec` maps exit 2 → Block. Full lifecycle (SessionStart/UserPromptSubmit/Stop/SubagentStop) fires from `agent/hooks.go` + `agent/subagent.go` |
| 6 | Hour-long session stays cheap (cache hit > 0.7) | `prompt.Layered` stable prefix/volatile suffix + `adapter/openai` breakpoint placement; `Usage.CacheHitRate()` metric |
| 7 | Subagent returns only its final string | `agent/subagent.go` — child loop, only final text folded as one ToolResult |
| 8 | Permission denies across merged scopes | `governance/permission.go` `Evaluate` (deny→ask→allow, Scope precedence) |
| 9 | Sandbox is a separate layer | **Seam in place** — the `tool.CommandRunner` interface (in `engine/tool`, *not* `engine/port`) is the command-execution chokepoint where an OS-sandbox adapter wraps later; the OS sandbox itself is the one deliberately-deferred item (§9) |
| 10 | Tool-description bug is diagnosable by reading it | `tool.ToolSpec.Description` convention (doc 07 §9); descriptions reviewed as onboarding docs |

Also enforced: **read-parallel / mutate-serial** (doc 08 #4) in `agent/dispatch.go`,
keyed off `Tool.ReadOnly()`; **stop conditions** (`Limits`/`Counters` on Session);
**compound-Bash + wrapper canonicalization** in `governance/bash.go`.

---

## 9. What was deferred at v1 — and where it landed

Most of the v1 "designed-in seams, not built" list is now built behind the seam it
was designed for. The authoritative tracker is
[Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md); the summary:

| Originally deferred | Status | Where it landed |
|---|---|---|
| Four-tier compaction cascade | **Done** | `CascadeCompactor` (`agent/cascade.go`) behind the `Compactor` seam — tiered snip→strip→collapse→summarize with trigger/target hysteresis. Default stays `HeuristicCompactor`; opt in with `--compaction=cascade`. |
| Real tokenizer | **Done** | `TokenCounter` seam (`agent/tokencount.go`); offline tiktoken adapter `internal/adapter/tokenizer`. Default stays the heuristic counter. |
| MCP client (**streaming-HTTP transport ONLY — stdio MCP is explicitly NOT supported, ever**) | **Done** | `internal/adapter/mcp`: streaming-HTTP transport only (no `os/exec`-spawned stdio server is ever created); registers remote tools into `tool.Catalog` namespaced `mcp__<server>__<tool>`. |
| Repo map / embeddings | **Removed (repo map); embeddings unbuilt** | The Aider-style repo-map tool (`internal/adapter/repomap`, tree-sitter + PageRank) was **retired and removed** — the WASM tree-sitter binding leaked and hung after ~160 files. See `docs/adr/0029-repomap-tree-sitter.md`. May be reintroduced later from a clean design. Embeddings remain unbuilt. |
| Persistent cross-session memory | **Done** | `tool.MemoryStore` seam + file-backed `internal/adapter/memory` (Remember/Recall tools, per-project), plus opt-in `dream` consolidation. The `SessionStore` + AGENTS.md/CLAUDE.md discovery still cover the file-as-memory case. |
| Slash commands / skills | **Done (commands)** | `prompt.CommandExpander` seam + `DirCommandExpander` (`.mecatl/commands` / `.claude/commands` templates). Skill packaging remains future. |
| Fork-join parallelism (pattern 8) | **Done** | `tool.WorkspaceForker` seam + `internal/adapter/forker` (git-worktree / copy isolation) + `agent.NewParallelTool`. |
| Multi-vendor model routing | Optional, unbuilt | `LLMProvider` port already abstracts it; a router would be a convenience adapter. |
| **OS-level sandbox (Landlock/seccomp/Seatbelt)** | **Deliberately deferred** | The `tool.CommandRunner` seam is the chokepoint; a Landlock(+seccomp) wrapper drops in as a `CommandRunner` adapter without touching the loop. Bash is also fully optional (shell-less deploys avoid the surface entirely), so this is not a blocker for those. |

The guiding restraint (doc 08) still holds: build the *shape*, instrument it, and
resist features before the loop, tools, permissions, hooks, and cache all work — the
post-v1 work above only extended seams that the v1 shape already exposed.


---

*Part of the [design docs](../design/README.md). Related: [Driver seams — ports, the gRPC driver protocol, and conformance](0005-driver-seams.md), [Implementation Notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md), [mecatl — Implementation Step-Chain (v1)](0006-v1-step-chain.md).*
