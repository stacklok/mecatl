# The API surface

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the gRPC `HarnessService`, HTTP/SSE mirror, and ACP (stdio) surfaces that expose the same domain `session.Event` stream; the engine-as-library stability contract; and session operations including `ForkSession` and manual compaction.

**Prerequisites:** [the agent loop](agent-loop.md) — the behavior the API drives and streams.

**Follow-on:** [observability](observability.md) and [deployment & hardening](deployment-and-hardening.md) — the persistence, telemetry, and operational seams the API surfaces depend on.

One `Service` (`service.go`) backs two surfaces, both relaying the same domain
`session.Event` mapped to one proto `Event` by `toProto` (`mapper.go`). The
service owns session lifecycle (`CreateSession`, `GetSession`), starts runs
(`StartRun`), and registers/deregisters in-flight `*agent.Run` so approve/cancel
reach the right run.

**gRPC (`harness.proto`, `grpc.go`)** — `HarnessService`:
- `CreateSession(CreateSessionRequest) → CreateSessionResponse` — carries an
  OPTIONAL per-session `provider_id` / `model_id` selector (multi-provider Phase 0; see [multi-provider](providers.md))
  AND an OPTIONAL `profile` (enum-as-string: `""` = default, `"no-fs"`).

  Placement is server-owned ([ADR 0291](../adr/0291-server-owned-session-placement.md)).
  Create has no workspace/cwd/placement-id/selector field: omitted profile binds the
  trusted deployment default and `"no-fs"` binds explicit attenuation. Every session
  receives a valid exact `EnvironmentRef{Kind,ID,Revision}` before persistence. Public
  responses and inventories carry bounded `PlacementMetadata`, never that private ref
  or a filesystem path. Run entry exactly reattaches the persisted ref with no
  current-default fallback.

  The no-FS session always routes through the per-session engine factory. Its catalog is
  default minus {Read, ListDir, Edit, Write, Copy, Move, Remove, Grep, Glob, Shell, ShellStatus, Parallel, SkillDraft};
  WebFetch/WebSearch, memory, MCP, and file-less delegation remain. Its Workspace is the
  honest `engine/adapter/nofs` implementation and its valid no-FS ref survives restart.
- `GetSession(GetSessionRequest) → GetSessionResponse` — path-free snapshot with
  bounded placement metadata; exact private refs remain storage-only.
- `ClearSession` creates a distinct empty-history successor; `ForkSession` creates a
  history-carrying successor. Both inherit the source's exact placement unless given a
  fresh caller/source-scoped worktree selector from `ListWorktrees(session_id)`. The
  server reauthorizes, exactly reattaches, serializes/leasing the source, resolves
  placement/model changes, then publishes atomically. Failure leaves the source and
  client binding unchanged. Selectors expire on restart and are accepted nowhere else.
- `CompactSession(CompactSessionRequest) → CompactSessionResponse` applies one
  configured compaction pass without creating a model turn. It accepts an owned
  main chat only at an idle or terminal boundary, serializes with run entry, rejects
  a live run or pending approval, and acquires the configured mutation lease. A
  changed result is saved before the existing compaction notice/archive events are
  appended; `compacted=false` is a successful no-op. The additive
  `ServerCapabilities.manual_compaction` bit lets old servers degrade safely. See
  `internal/adapter/server/service.go` (`CompactSession`) and
  [context management](context-and-compaction.md).
- `SetMode(SetModeRequest) → SetModeResponse` — changes an existing session's
  permission posture through `Service.SetMode`; mid-turn changes are rejected by
  the session aggregate as `InvalidArgument`, so clients that want "next prompt"
  semantics defer and retry once idle. **`resolved_model` is fixed per TURN, not
  per session**: when an operator has bound a `plan` model slot ([ADR 0030](../adr/0030-model-selection-heuristics.md)
  Layer 3, the opusplan pattern), a plan↔execute mode switch re-resolves the
  effective model **between turns** at the run-entry seam (within the same
  provider). The `SetMode` response still echoes the pre-rebuild model (the model
  is fixed for the current turn); a client re-reads the new model from `GetSession`
  (or the next run's echo) **after** the mode change. With no plan slot a mode flip
  changes nothing.
- `ListModels(ListModelsRequest) → ListModelsResponse` — the selectable-model
  inventory: every AVAILABLE provider's catalog models projected to public metadata
  (`ModelInfo{id, provider_id, display_name, image, reasoning, context_limit}`), no
  secrets, (provider_id, id)-sorted. Gated by `ServerCapabilities.model_selection`
  (true iff ≥1 provider is available). See [multi-provider](providers.md).
- `Converse(stream ConverseRequest) → stream ConverseResponse)` — bidirectional.
  The first frame **must** be `prompt` or `retry`; later frames may carry
  `resume_approval`, `cancel`, `cancel_child`, `steer`, or `steer_cancel` controls.
  A received second start frame is rejected with `InvalidArgument`. The server starts
  the run, reads controls on a side goroutine (`readControl`), and relays `Event`s
  until the terminal result closes the stream. A control still in transit at that
  boundary may instead observe normal EOF.
- The full service is wider than this core. Session lifecycle adds
  `CloseSession`; the read-only inventories are `ListAgents`, `ListCommands`,
  `ListSkills`, `GetSoul`, `GetUserModel`; MCP passthrough is
  `ListMcpResources` / `ReadMcpResource` / `ListMcpPrompts` / `GetMcpPrompt` /
  `ListMcpSources` / `ListToolHiveGroups`; and the team family is `CreateTeam`
  / `SpawnTeammate` / `SendTeammateMessage` / `CancelTeammate` (cancel one
  member of a running team) / `RunTeam` (a server-streamed
  `TeamEvent` sequence) / `ListTeam` / `CleanupTeam`.
  `CreateTeamRequest.max_team_tokens` carries the tighten-only team-wide token
  budget ([parallelism](parallelism.md)).

`ConverseResponse` wraps one `Event`. The proto `Event` mirrors `session.Event`
one-for-one: string `type` plus `ToolCall`, `ToolResult`, `PermissionAsk`,
`Result`, `Usage` submessages. `PermissionAsk.ask_id` is echoed back in
`ResumeApproval.ask_id`. Required-field annotations use `buf.validate.field`;
v1 enforces required checks in the Go server (protovalidate runtime is deferred).

**HTTP/SSE (`http.go`)** — the thin mirror, since grpc-gateway cannot map bidi:

| HTTP | Maps to | Notes |
|---|---|---|
| `POST /v1/sessions` | `CreateSession` | JSON body → `session_id`; optional `provider_id`/`model_id` selector + `profile` (`"no-fs"`) |
| `GET /v1/sessions/{id}` | `GetSession` | JSON snapshot |
| `POST /v1/sessions/{id}/mode` | `SetMode` | change permission mode; mid-turn rejection is surfaced to the client |
| `POST /v1/sessions/{id}/compact` | `CompactSession` | bodyless forced compaction at an idle/terminal boundary; `{"compacted":true}` when history changed, false for a no-op |
| `POST /v1/sessions/{id}/clear` | `ClearSession` | empty-history successor; optional ephemeral `worktree_selector` |
| `POST /v1/sessions/{id}/fork` | `ForkSession` | history-carrying successor; optional ephemeral worktree selector and model overrides |
| `GET /v1/models` | `ListModels` | JSON selectable-model inventory (available providers only, secret-free) |
| `POST /v1/sessions/{id}/prompt` | start a run | `text/event-stream`; each event is `data: <proto Event as JSON>` |
| `POST /v1/sessions/{id}/approve` | `Run.Approve` | resolves the paused ask (verdict or legacy `allow`) |
| `POST /v1/sessions/{id}/cancel` | `Run.Cancel` | cancels the in-flight run |
| `POST /v1/sessions/{id}/cancel-child` | `Run.CancelChild` | cancels ONE child of the in-flight run |
| `DELETE /v1/sessions/{id}` | `CloseSession` | frees the per-session engine slot |
| `GET /v1/agents` · `/v1/skills` · `/v1/commands` · `/v1/soul` · `/v1/usermodel` | the inventory RPCs | read-only snapshots |
| `GET /v1/mcp/resources` · `/v1/mcp/resources/read` · `/v1/mcp/prompts` · `POST /v1/mcp/prompts/get` · `GET /v1/mcp/sources` · `/v1/mcp/toolhive/groups` | MCP passthrough | mirrors the gRPC MCP family |
| `POST /v1/teams` · `POST /v1/teams/{id}/members` · `POST /v1/teams/{id}/messages` · `POST /v1/teams/{id}/members/cancel` · `POST /v1/teams/{id}/run` · `GET /v1/teams/{id}` · `DELETE /v1/teams/{id}` | the team family | `/run` streams `TeamEvent`s over SSE; `/members/cancel` cancels one member of a running team |

Closing either stream cancels the run: the SSE handler watches
`r.Context().Done()` and calls `run.Cancel()`; the gRPC relay cancels on a send
error. `Run.Cancel` first arms the run's sticky `hardAbort` signal (a short
grace timer, armed before the ctx cancel), so every guarded send — the loop's
`emit`, the child registry's `emitOrAbort`, the team supervisor's member
forward — gives up instead of parking forever behind a consumer that stopped
draining (the explicit unwedge; a cancelled run with a *draining* consumer
still delivers its in-flight events — the grace covers even a backlogged one).
Both relays are drain-to-discard after the FIRST Send/Write error: they record
the error, cancel the run, and keep ranging `run.Events()` (discarding, no
further writes) until the channel closes — a busy run never wedges in its own
emits behind a dead client.

**ACP (`internal/adapter/acp`)** — a THIRD wire surface alongside gRPC and
HTTP/SSE: the Agent Client Protocol, JSON-RPC 2.0 over **stdio**, lets an ACP
editor (Zed, and others) spawn mecatl as a subprocess (wired in `cmd/mecated`
behind `--acp`) and drive the SAME surface-agnostic `server.Service`. It
carries its own JSON — it never imports `contracts/gen` — projecting domain
`session.Event`s onto `session/update` notifications and resolving permission
asks via the outbound `session/request_permission` request (the adapter is
both JSON-RPC server and client; the bidirectional codec lives in `conn.go`).
Prompt content is **multimodal + capability-gated**: every block becomes text,
a `session.Content` part, or a loud `codeInvalidParams` — never a silent drop —
routed through the single `session.NewContent`/`ValidateMediaParts` choke
point and gated on `Service.ProviderCapabilities()`. (This is the harness
speaking an editor protocol delivered over its own stdin/stdout; the project's
no-stdio rule is about MCP servers, which are never `os/exec`-spawned.) The
design decisions behind this adapter — framing, the per-session client MCP
mount, fs/\* delegation, and learned permissions — are recorded in
[ADR 0001 — the ACP adapter](../adr/0001-acp-adapter.md).

> **The wire is one surface; the engine library is another.** The proto/HTTP/ACP
> surface above is the way a *client process* drives mecatl. An *embedding Go
> consumer* instead imports the `engine/` module directly, whose STABLE public
> surface is the exported identifiers of the **seven core packages** (`session`,
> `governance`, `tool`, `prompt`, `port`, `team`, `agent`). That surface is
> governed by [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md) and the
> `api-compat` freshness gate (`internal/apicheck`, `task api:check`), which fails
> CI on any unflagged change to the committed `engine/api/*.txt` baselines (#114,
> [ADR 0037](../adr/0037-engine-stability-contract.md)). See
> [extensibility](extensibility.md) for the engine-as-library framing.

## Prerequisites

- [The agent loop behind the API](agent-loop.md)

## Follow-on reading

- [Observability & persistence](observability.md)
- [Deployment & server hardening](deployment-and-hardening.md)

## Related

- [Hooks & guardrails](hooks-and-guardrails.md)
- [Extensibility — MCP & tool meta-surfaces](extensibility.md)

---

[← Architecture guide](../architecture.md)
