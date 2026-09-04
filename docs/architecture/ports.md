# The ports (`engine/port`)

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the port interfaces the loop consumes (`LLMProvider`, `SessionStore`, `PermissionPolicy`, `HookRunner`, `EventSink`, `EventLog`, `ToolCallRecorder`, `Diagnostics`, `Clock`, `SessionLease`), the `LLMRequest`/`Chunk` stream types, and the `tool.Workspace`/`FileSystem`/`ReadLedger`/`CommandRunner` seam (which lives in `engine/tool` to break a port↔tool cycle).

**Prerequisites:** [the domain model](domain-model.md) — the value objects the ports carry.

**Follow-on:** [the agent loop](agent-loop.md) — the loop that consumes these ports.

Small interfaces, `context.Context` first. Each has a fake adapter so the loop
runs with no network and no disk.

| Port | Responsibility | Signature (verbatim) |
|---|---|---|
| `LLMProvider` (`llm.go`) | provider-agnostic model call; streams neutral chunks | `Stream(ctx context.Context, req LLMRequest) (iter.Seq2[Chunk, error], error)` · `Capabilities() ProviderCapabilities` (multimodal-input flags; decorators must forward the inner provider's) |
| `SessionStore` (`store.go`) | persist/retrieve session state | `Save(ctx context.Context, s *session.Session) error` · `Load(ctx context.Context, id session.SessionID) (*session.Session, error)` — a store may additionally implement the optional `PrunableStore` (`List`/`Delete`) for retention ([observability & persistence](observability.md)). `Load` carries a documented **event-sourced reconstruction contract** (the `store.go` doc-comment + `engine/COMPATIBILITY.md` "Session reconstruction contract"): a host whose system of record is an append-only event log may implement `Load` by FOLDING its `EventLog` (plus out-of-band creation metadata, `engine/adapter/eventsource.SessionMeta`) into a `*session.Session` via `engine/adapter/eventsource.Fold`, the reference implementation. The one residual gap is REPLAY FIDELITY — the opaque assistant-replay fields (`Message.Reasoning`, `Message.ProviderPhase`, `ToolCall.ItemID`) are not on the event stream, so a pure fold is byte-identical-replay faithful only for non-reasoning (plain-chat) providers (#115, ADR 0038) |
| `HookRunner` (`hookrunner.go`) | run a lifecycle hook → outcome (`hookexec` maps an external process exit code; `modelhook` maps a quarantined checker model's verdict — block/sanitize/advisory) | `Run(ctx context.Context, ev governance.HookEvent) (governance.HookOutcome, error)` |
| `PermissionPolicy` (`permission.go`) | deny→ask→allow across merged scopes; per-session learned allows | `Evaluate(ctx context.Context, sessionID session.SessionID, mode session.PermissionMode, c session.ToolCall, ws tool.WorkspaceReader) governance.PermissionDecision` (ws is the READ-ONLY discovery root for file-based permission config, issue #13; nil = no project config) · `Learn(sessionID session.SessionID, c session.ToolCall)` (the allow-**always** verdict; lowest scope, never overrides a deny or plan mode) |
| `EventSink` (`log.go`) | relay loop events to the API stream (mirrors live) | `Emit(ctx context.Context, ev session.Event)` |
| `EventLog` (`eventlog.go`) | DURABLE per-session event timeline, distinct from `EventSink` — a later consumer reads it back (cloud-native Phase 3). The loop NEVER calls it; persistence lives at the relay. It also records the log-only `EvUserPrompt` so user-role turns reconstruct under the event-sourced fold | `Append(ctx, id, ev) error` (must be durable before returning nil; at-most-once, no dedup) · `Read(ctx, id) iter.Seq2[session.Event, error]` (append order, streamable) |
| `ToolCallRecorder` (`log.go`) | structured per-tool AUDIT (distinct from `Diagnostics`) | `ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration)` |
| `PermissionStore` (`permission.go`) | persist/replay per-session learned allow-always verdicts | `Save`/`Load` of learned rules (powers the verdict-replay consumer, [observability & persistence](observability.md)) |
| `Diagnostics` (`diagnostics.go`) | injected operational-logging seam (NO global slog in `engine/` or `internal/`) | `Log(ctx, level Level, msg string, args ...any)` · `With(args ...any) Diagnostics` |
| `Clock` (`clock.go`) | abstract wall clock | `Now() time.Time` — the engine core is now FULLY clock-injectable: every core wall-clock read flows through this port (`Engine.now()` for the loop), enforced by the AST guard `engine/arch/clock_test.go` that forbids `time.Now`/`Since`/`Until` in `CorePackages`, so an embedding host can drive the engine deterministically (#116) |
| `SessionLease` (`lease.go`) | OPTIONAL cross-process single-writer seam (multi-replica readiness, cloud-native Phase 4) — discovered by type assertion like `PrunableStore`, nil otherwise (byte-identical no-lease default); the loop never imports it | `Acquire(ctx, id, owner) (Lease, error)` · `Renew(ctx, l) (Lease, error)` · `Release(ctx, l) error` (`ErrLeaseHeld` = a live competitor, `ErrLeaseUnsupported` = backend can't lease) |

`SessionStore` may additionally implement `PrunableStore` (`List`/`Delete`) for child-session retention ([observability & persistence](observability.md)). The interfaces above are the ones the loop consumes directly; several also have **optional capability seams** (`PrunableStore`, `ScheduleStore`, the `PermissionStore`/`EventLog`/`SessionLease` siblings) discovered by type assertion and nil-safe when absent, so the count of *required* ports stays small and a backend wires only what it needs. The loop consumes every port through injection only.

The model-call request and stream types (`llm.go`):

```go
type LLMRequest struct {
    System   prompt.Layered    // stable prefix + volatile suffix
    Messages []session.Message
    Tools    []tool.ToolSpec
    Model    string
}

type ChunkKind int
const (
    ChunkText ChunkKind = iota
    ChunkReasoning     // display-only reasoning summary delta
    ChunkReasoningItem // opaque reasoning REPLAY blob → Message.Reasoning
    ChunkToolCall
    ChunkUsage
    ChunkDone
    ChunkPhase         // opaque phase marker → Message.ProviderPhase (issue #46)
)

type Chunk struct {
    Kind     ChunkKind
    Text     string             // text / reasoning summary / replay blob / phase marker, per Kind
    ToolCall *session.ToolCall  // on ChunkToolCall
    Usage    *session.Usage     // on ChunkUsage
    Stop     session.StopReason // on ChunkDone
}
```

The `ChunkReasoning` (display summary) vs `ChunkReasoningItem` (replay blob)
split is deliberate and provider-neutral — Anthropic's thinking delta maps to
the former, its `(thinking,signature)` replay token to the latter; they must
never be conflated. `ChunkPhase` follows the same opaque-replay discipline.

### The tool contract and the FS seam (`engine/tool`)

```go
type Tool interface {
    Spec() ToolSpec
    ReadOnly() bool
    Execute(ctx context.Context, in session.ToolCall, env Environment) (session.ToolResult, error)
}
```

`ToolSpec{Name, Description, Schema json.RawMessage}` is what the model sees;
descriptions are documentation (gauntlet #10). `Catalog` (`catalog.go`) is a
name→Tool registry with `Register`/`MustRegister`/`Lookup`/`Tools`. Its
`Specs(mode)` and `Available(mode)` apply **plan-mode filtering at the catalog
level**: in `ModePlan` only `ReadOnly()` tools are exposed, ordered by name.

`FileSystem`, `Workspace`, `ReadLedger`, and `Environment` live here (not in `port`) to break
the `port↔tool` cycle. `Tool.Execute` takes a `tool.Environment` (ADR 0211) — an
immutable capability bundle carrying a content-only `Workspace` (`env.Workspace()`),
a separately selected non-null `ReadLedger` (`env.ReadLedger()`), an optional bound
`CommandRunner` (`env.CommandRunner()`; nil when the namespace has no shell), and a
backend identity ref (`env.Ref()`). File-system tools obtain the Workspace and ledger;
the Bash tool obtains the runner and surfaces `ErrNoShell` when it is nil.
`Workspace` scopes all paths to one root, rejects escapes, exposes the read/search
surface, and carries the versioned content-mutation protocol from
[ADR 0208](../adr/0208-execution-environment.md). It exposes no ledger operation.
`ReadVersion` returns content plus an opaque `FileVersion`; the narrow persistence
codec rejects an invalid zero version while preserving valid empty opaque tokens.
Agent-facing Read records the exact version in `env.ReadLedger()` under the I/O-free
lexical `LedgerKey`. Lookup distinguishes a found token, ordinary absence, and an
unavailable/corrupt backend. Edit and existing-file Write fail closed on lookup errors,
compare found evidence with a current version-bearing read, then finish with conditional
`ReplaceFile`; new-file Write uses create-only `CreateFile`. Public `Workspace` has no
unconditional Write capability. A successful create/replace records its returned version;
if that record fails, the tool reports the successful content mutation and that no new
evidence was persisted. Existing evidence retains only its ordinary exact-version meaning.
Default Environment composition supplies a fresh `engine/adapter/memledger`; durable
selection does not change the content backend. Every child receives a fresh ledger:
isolated children pair it with the fork Workspace, while direct-write/base-sharing
children retain the exact parent content backend and runner through any stricter
child-authority Workspace view; storage is never reconstructed from `Root()`. Redisstore provides an optional
durable ledger as one validated hash per session, borrowing the Store lifecycle; both
canonical session-deletion scripts remove it atomically with the other sidecars, and
`DeleteReadLedger` remains an idempotent ledger-only reset. It is not wired as the
production default. See [ADR 0296](../adr/0296-persistent-read-before-write-ledgers.md).
Restarting the process loses in-memory overrides; a restarted session
re-derives its Environment through the same rehydration path (no-fs profile,
ACP adapter reconnect). As of ADR 0214, `EnvironmentRef` is a DURABLE snapshot
field: a non-in-tree ref persists and reattaches a live `Environment` at run
entry through `server.Config.EnvironmentResolver` (the in-tree Kinds never reach
it; a nil/mismatch/nil-Workspace result fails loudly). A legacy zero ref is
stamped from the first resolved live Environment on the next save.

`CreateFile` and the compare-plus-mutation in `ReplaceFile` are atomic for
concurrent calls through the same live Workspace/backend handle. ACP's
instance-local mutex satisfies that base contract. osfs deliberately provides a
stronger process-wide guarantee: it canonicalizes the physical target (or the
physical parent plus basename for a missing create), so symlink aliases share a
lock stripe across Workspace instances, and performs create through confined
`O_CREATE|O_EXCL`. Arbitrary POSIX writers that bypass the Workspace seam do not
participate, so local osfs is not kernel-level atomic replacement. ACP removes
the exact confined requested path from structured editor errors, then accepts only
anchored normalized absence shapes; generic and structured non-absence failures
fail closed. A future remote backend must
provide true backend CAS.

**Command execution is a separate seam, bound to one namespace at construction.**
`tool.CommandRunner` (`Run(ctx, command) (CommandResult, error)`) is the only
chokepoint for shell execution; the agent loop never references it, and only the
Bash tool depends on it. A runner is BOUND to a single namespace at construction
(no per-call `workdir` — the command's cwd always matches the `Workspace` the tool
executes against). That makes Bash — and therefore *all* command
execution — optional in the catalog: `NewBashTool()` is registered only
when a runner is configured, and `tools.Register`
deliberately excludes it. The `osfs` adapter ships a local `/bin/sh`
`CommandRunner` (output-capped, context-bounded, process-group-killed on
cancel); a runner may also execute remotely or refuse with `tool.ErrNoShell`. A
shell-less deployment simply omits Bash, and an OS sandbox would wrap this seam.
[ADR 0211](../adr/0211-execution-environment-runtime-seam.md) implements the
runtime seam: a coding agent runs in an execution environment (`tool.Environment`)
whose `Workspace`, separately selected `ReadLedger`, and bound `CommandRunner` address one namespace and evidence scope. The
`tool.Environment` carries identity (`session.EnvironmentRef`) plus those three
capabilities; the forker/merger are `tool.EnvironmentForker`/
`tool.EnvironmentMerger` (returning/receiving complete `Environment`s), and
governance remains outside. `EnvironmentRef` is an in-process identity in phase 2
— snapshot persistence and remote transport are deferred to phase 3. [ADR 0214](../adr/0214-environment-persistence.md)
implements the phase-3 persistence/reattachment half: `EnvironmentRef` is a durable
snapshot field, and `server.Config.EnvironmentResolver` reattaches a live
`Environment` for a non-in-tree Kind (the `internal/adapter/remoteenv` reference
fake proves the contract). The
version-aware file-mutation foundation is [ADR 0208](../adr/0208-execution-environment.md).

`tool.MemoryStore` and `tool.EnvironmentForker` live alongside it for the same
layering reason (the tools that need them depend on the interface, not a
`port`).

**The Bash tool itself is the agent loop's own** (`engine/agent/bashtool.go`,
`agent.NewBashTool`), not the fstools adapter's: the foreground half is
byte-identical to the fstools body, and `background: true` detaches the command
as a run-scoped background job on the parent run's child registry — an
agent-package type fstools cannot import. It registers under the literal name
`"Bash"` (`tool.BashToolName`) because the permission evaluator special-cases
that name (the compound-command split, the plan-mode read-only gate, rule
learning) — a second tool name would silently bypass the bash gate, so any shell
affordance must register under the gated name or extend the gate. A background
call returns immediately with a `bashcmd-<callID>` job id and runs detached in
the REAL workspace (no isolation — its effects may interleave with the model's
own edits, and the description says so); the read-only **`BashStatus`** tool
(`engine/agent/bashstatus.go`, registered iff Bash is, never in child catalogs)
is the sole status/collect/cancel channel — no args → the run's job roster (ids
+ state + stop only), `job_id` → the command + retained output tail (live) or
the exactly-once collected result (done), `wait_ms` (≤120s) parks, `cancel`
signals the job's context. Permissions are identical to foreground Bash (the
start is the ask; nothing re-asks mid-run), and a job still live at run end is
cancelled and joined by the same drain the background subagents use — a job is
RUN-scoped, never session-scoped. Streaming the job's output is the OPTIONAL
`tool.CommandStreamer` capability (`RunStreaming(ctx, command, out
io.Writer) (exitCode int, err error)` — the osfs runner implements it over the
same spawn/wait tail as `Run`; a runner without it declines background calls
honestly): the job streams interleaved stdout+stderr into a bounded 64 KiB tail
ring (`engine/agent/tailbuffer.go`), so `BashStatus` shows the RECENT output a
head-capped capture would have lost. See
[ADR 0201](../adr/0201-background-bash.md) and
[subagents & teams](subagents-and-teams.md) for the registry family mechanics.

## Prerequisites

- [The domain model — what the ports carry](domain-model.md)

## Follow-on reading

- [The agent loop — the ports' consumer](agent-loop.md)
- [Providers — the LLMProvider port's adapters](providers.md)

---

[← Architecture guide](../architecture.md)
