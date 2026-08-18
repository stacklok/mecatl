# The API surface

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the gRPC `HarnessService`, HTTP/SSE mirror, and ACP (stdio) surfaces that expose the same domain `session.Event` stream; the engine-as-library stability contract; and the `ForkSession` RPC.

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

  **The filesystem is OPTIONAL per session** (the `"no-fs"` profile, issue #55):
  the workspace requirement is profile-aware — default requires one, no-fs
  requires an EMPTY one (the contradictory combination is a loud
  `InvalidArgument`), and any unknown profile is rejected, never silently
  defaulted. A no-fs session always routes through the per-session engine
  factory (the shared engine has the FS tools baked in): its catalog is the
  default set MINUS exactly {Read, Edit, Write, Grep, Glob, Bash, BashStatus,
  Parallel, SkillDraft} (pinned by `TestNoFSCatalogProfile`), its workspace is the honest
  `engine/adapter/nofs` Workspace (reads fail `fs.ErrNotExist`, searches are
  empty, writes refuse loudly — deliberately NOT memfs, which would silently
  absorb writes nobody can read back), registered as the per-session workspace
  override AT CREATE TIME so the osfs factory never sees the empty root, and
  its Subagent/Team children run the same file-less surface (memory six +
  WebFetch + WebSearch + global MCP) with no forkers and no shell. The model is told via a
  system-prompt posture note and an honest Subagent spec. A REMOTE filesystem
  for such sessions is a future driver concern (`docs/adr/0005-driver-seams.md`), an
  explicit non-goal of the profile itself.
- `GetSession(GetSessionRequest) → GetSessionResponse`
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
  The first frame **must** be `prompt`; then zero or more `resume_approval` /
  `cancel` / `cancel_child` control frames. `ConverseRequest` is a `oneof kind
  { Prompt prompt=1; ResumeApproval resume_approval=10; Cancel cancel=11;
  CancelChild cancel_child=12 }`. The server starts the
  run, reads control frames on a side goroutine (`readControl`), and relays
  `Event`s on the main goroutine until the channel closes.
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
| `GET /v1/models` | `ListModels` | JSON selectable-model inventory (available providers only, secret-free) |
| `POST /v1/sessions/{id}/prompt` | start a run | `text/event-stream`; each event is `data: <proto Event as JSON>` |
| `POST /v1/sessions/{id}/approve` | `Run.Approve` | resolves the paused ask (verdict or legacy `allow`) |
| `POST /v1/sessions/{id}/cancel` | `Run.Cancel` | cancels the in-flight run |
| `POST /v1/sessions/{id}/cancel-child` | `Run.CancelChild` | cancels ONE child of the in-flight run |
| `POST /v1/sessions/{id}/adoption:preflight` | `PreflightSessionAdoption` | caller-owned eligibility and explicit-binding preflight; no source mutation |
| `POST /v1/sessions/{id}/adopt` | `AdoptSession` | idempotent atomic new-main copy; source remains inspect-only |
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
