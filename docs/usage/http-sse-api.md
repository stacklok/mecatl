## 10. The HTTP / SSE API

This is the detailed operator and wire reference. For the client-integration
entry point, shared event lifecycle, and gRPC comparison, start with [Drive via
gRPC / HTTP](https://github.com/stacklok/mecatl/blob/main/user-docs/building/deployment/grpc-http.md).

The HTTP adapter wraps the same service. Every event is emitted as one SSE
`data:` line carrying the proto `Event` marshalled to JSON — so HTTP and gRPC
share one event shape.

**Sessions & runs:**

| Method & path | Body | Response |
| --- | --- | --- |
| `POST /v1/sessions` | `{workspace, mode?, limits?, provider_id?, model_id?, profile?}` | `201` `{session_id}` |
| `GET /v1/sessions` | — | `200` `{sessions: [...]}` — the stored-session inventory (picker rows: id, timestamps, state, turns, model id; no conversation content), most-recently-active first |
| `GET /v1/sessions/{id}` | — | `200` session snapshot |
| `GET /v1/sessions/{id}/events` | — | `200` `text/event-stream` — replay a session's durable event log (full timeline incl. the log-only `approval`/`compaction_archive`/`user_prompt` a live prompt stream skips); empty for an unknown id, `501` when no durable `EventLog` is wired |
| `POST /v1/sessions/{id}/rename` | `{title}` | `200` updated session snapshot with operator title provenance; `412` when kind/state/liveness gates reject the stale action, `409` when another replica holds the session lease |
| `POST /v1/sessions/{id}/delete` | — | `204` after permanently removing the snapshot and store-managed sidecars; `412` when the target is active, awaiting, or not a main chat, `409` when another replica holds the session lease, `501` when the configured store cannot physically delete |
| `DELETE /v1/sessions/{id}` | — | `204` — close the session, releasing its per-session resources (not physical stored-session deletion) |
| `POST /v1/sessions/{id}/prompt` | `{text}` | `200` `text/event-stream` of events |
| `POST /v1/sessions/{id}/approve` | `{ask_id, allow}` | `204` |
| `POST /v1/sessions/{id}/plan:approve` | `{"target_mode": "default" \| "accept_edits" \| "plan", "note": "..."}` | `200` `text/event-stream` — atomically resolve a parked **plan-approval** ask ([ADR 0069](../adr/0069-plan-approval-gate.md)): on `default`/`accept_edits` resume the parked run AND start the continuation run (both streamed); on `plan`/`""` iterate (no continuation). `409` on a precondition failure (live run / not awaiting / not a plan ask), `404` on an unknown session |
| `POST /v1/sessions/{id}/cancel` | — | `204` |
| `POST /v1/sessions/{id}/cancel-child` | `{child_id}` | `204`; `404` for an unknown / already-finished child |
| `POST /v1/sessions/{id}/fork` | `{"title": "...", "reasoning_effort": "..."}` (both optional; empty/absent inherits the source's) | `201` `{session_id}` — create a peer session from `{id}`'s conversation history snapshot (ADR 0065); same provider/model only, with the ONE optional selector delta a reasoning-effort override (ADR 0068); `412` if `{id}` is not an idle/terminal main chat or is live in this process, `409` when another replica holds its lease |
| `POST /v1/sessions/{id}/adoption:preflight` | `{workspace, environment_kind, environment_id, provider_id, model_id, profile?}` | `200` `{eligible, reason_code, bindings}`. Requires authenticated caller ownership; absent and foreign IDs are both `404`. Every binding is explicit and unresolved bindings return `binding_unresolved` rather than selecting a default |
| `POST /v1/sessions/{id}/adopt` | the same explicit bindings plus `idempotency_key` | `201` `{session_id, source_session_id, capabilities, resolved_model}`. Revalidates under the source mutation lease; a retry returns the same complete target. The legacy source is unchanged |

Adoption is available only when authenticated caller ownership and a per-session engine
factory are wired (`ServerCapabilities.legacy_adoption`). It accepts no message array or
transcript upload: transcript authority comes only from the server's `SessionStore.Load`.
There is no automatic or bulk endpoint. A cross-provider target strips provider-private
reasoning/phase/item identifiers through the same carryover rule used by model switches.

**Inventory & introspection** (the HTTP mirrors of the gRPC inventory RPCs in §9):

| Method & path | Response |
| --- | --- |
| `GET /v1/models` | the selectable provider/model inventory (`ListModels`) |
| `GET /v1/agents` | the agent-definition inventory |
| `GET /v1/skills` | the skills inventory |
| `GET /v1/commands` | the slash-command palette for a workspace |
| `GET /v1/soul` | the resolved soul snapshot (provenance, trust, drift) |
| `GET /v1/usermodel` | the live bounded user-model index; `?key=<exact-key>` also returns read-only value/version/provenance/proposal linkage/timestamps/bounded history when available |
| `POST /v1/sessions/{id}/reflect` | synchronously reflect a caller-owned completed session on its persisted provider (optional empty/`{}` body); returns bounded abstained/staged/promoted/conflicted counts |
| `POST /v1/dream/plans` | body `{"target":"project_memory"}` or `{"target":"user_model"}`; spends one planner call and returns the bounded-lifetime exact/synthesized review plan plus opaque process-local id |
| `POST /v1/dream/plans/{plan_id}/decision` | body `{"decision":"apply"}` or `{"decision":"dismiss"}`; decides the authoritative retained whole plan and returns planned/applied/conflicted/skipped/failed source counts |
| `GET /v1/learning/proposals` | bounded caller-partitioned proposal page (`status`, `cursor`, `limit`, optional reviewable `project`; promotion remains launch-root/trust-gated) |
| `GET /v1/learning/proposals/{id}` | bounded proposal detail with digest availability, never raw evidence text |
| `POST /v1/learning/proposals/{id}/decision` | approve/reject with `expected_version`; stale versions return `409` |
| `POST /v1/learning/proposals/{id}/undo` | compensating undo with `expected_version`; stale/current-memory conflicts return `409` |
| `GET /v1/mcp/resources` | MCP resource snapshots |
| `GET /v1/mcp/resources/read` | read one MCP resource by URI |
| `GET /v1/mcp/prompts` | the MCP prompt inventory |
| `POST /v1/mcp/prompts/get` | expand one MCP prompt (rendered messages) |
| `GET /v1/mcp/sources` | the resolved MCP source inventory |
| `GET /v1/mcp/toolhive/groups` | the ToolHive groups in the resolved inventory |

Manual dream generation sends the selected bounded memory values/descriptions to the configured
planner and spends tokens; regeneration is explicit and spends again. Apply/dismiss is whole-plan,
with no client-supplied operations or per-source toggles. Exact duplicates keep the survivor;
approved synthesis atomically rewrites the displayed survivor and tombstones the displayed sources
per operation, while independent operations can yield a partial receipt. Plans expire after ten
minutes and exist only on the generating process: expiry, restart, or a wrong replica returns `404`
and makes the old decision non-retryable while allowing explicit fresh generation. A same-decision
request while apply is running returns `409` and remains explicitly retryable; an opposite request
returns `412` while apply is active and `410` after the opposite terminal decision. Only the terminal
case offers fresh generation. Genuinely indeterminate transport failures preserve the exact plan ID and
decision for same-decision retry because the first request may already have applied. No error path offers
the opposite decision. Capacity pressure returns `429`, and an unavailable deployment/target `501`. Ownership enforcement
disables manual dream review. This does not change the separate, off-by-default schedule flags or
`learning.mode`, and no recall counters or provider/model identity are returned.

**Agent teams** (with `--enable-teams`, the default):

| Method & path | Body | Response |
| --- | --- | --- |
| `POST /v1/teams` | team spec (incl. the tighten-only `max_team_tokens?`) | create a team |
| `POST /v1/teams/{id}/members` | member spec | spawn a teammate |
| `POST /v1/teams/{id}/messages` | message | post into a member's inbox |
| `POST /v1/teams/{id}/members/cancel` | `{"member": "..."}` | cancel one member of a running team (404 unknown team/member, 412 not running) |
| `POST /v1/teams/{id}/run` | — | `text/event-stream` of `TeamEvent`s, ending with the terminal `outcome` frame |
| `GET /v1/teams/{id}` | — | team snapshot (roster, tasks, quiescence) |
| `DELETE /v1/teams/{id}` | — | clean up the team |

All examples below were captured against a live `mecated serve --mock`.

### Create a session

```console
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions \
       -d '{"workspace":"/tmp/mecatlws"}'
{"session_id":"8867bdea940108c1dd82d13d3fb7fc61"}
```

Optional fields:

```json
{
  "workspace": "/tmp/mecatlws",
  "mode": "plan",
  "limits": { "max_turns": 20, "max_tool_calls": 80, "max_consecutive_failures": 3 }
}
```

`mode` accepts `default`, `plan`, `acceptedits` (also `accept_edits` / `accept`);
unknown/empty falls back to the server default (`default`). A `limits` object
with all-zero (or omitted) fields gets the server's non-zero defaults
substituted (see §11). `workspace` is required for the default profile —
omitting it returns `400` `{"error":"workspace is required"}`.

### Create a no-filesystem session (`profile: "no-fs"`)

A session can opt out of the filesystem entirely — useful for pure
research/coordination agents (MCP tools + memory + web fetch) that should never
touch a disk:

```console
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions \
       -d '{"profile":"no-fs"}'
{"session_id":"..."}
```

The same `profile` field exists on the gRPC `CreateSessionRequest` (enum-as-
string: `""` = default, `"no-fs"`). Rules, all enforced server-side:

- `"no-fs"` REQUIRES an **empty** `workspace` (the combination is contradictory
  and returns `400`/`InvalidArgument`); the default profile still requires one.
- Any other profile value is rejected loudly — never a silent fallback.
- The no-FS session has **no** Read/Edit/Write/Grep/Glob/Bash/BashStatus, no
  Parallel, and no SkillDraft. It keeps MCP tools (server-global + resource meta-tools +
  client MCP), the six memory tools, WebFetch, WebSearch, Skill (bodies are text
  injection; out-of-workspace skill assets are unreadable), and delegation —
  Subagent and Team children run the same file-less surface with **no**
  worktree/fork isolation (there is nothing to isolate) and no shell.
- The model is told up front (a system-prompt posture note plus an honest
  Subagent tool description), so it plans around MCP/memory/web search+fetch
  instead of burning turns on unknown-tool errors.
- The profile composes with `provider_id`/`model_id` and is FIXED for the
  session lifetime.

### Inspect a session

```console
$ curl -s http://127.0.0.1:8081/v1/sessions/8867bdea940108c1dd82d13d3fb7fc61
{"session_id":"8867bdea940108c1dd82d13d3fb7fc61","state":"idle","mode":"default","workspace":"/tmp/mecatlws","turns":0,"tool_calls":0}
```

A missing id returns `404` `{"error":"not found: \"...\""}`.

### Start a run (SSE stream)

```console
$ curl -s -N -X POST http://127.0.0.1:8081/v1/sessions/8867bdea940108c1dd82d13d3fb7fc61/prompt \
       -d '{"text":"hello"}'
data: {"type":"turn.start","seq":1}

data: {"type":"message.delta","seq":2,"text":"Mock provider: no real model is configured. Set --openai/OPENAI_API_KEY for live use."}

data: {"type":"result","seq":3,"result":{"stop":"end_turn","text":"Mock provider: no real model is configured. Set --openai/OPENAI_API_KEY for live use.","usage":{}},"usage":{}}
```

> `-N` disables curl's buffering so you see events as they stream. The example
> above is the `--mock` provider (one text turn). Against a real model you also
> see `tool.call`, `tool.result`, and — for tools that need approval —
> `permission.ask`. The JSON field names follow the proto JSON shape:
> `tool_call`, `tool_result`, `ask` (`{ask_id, tool, args, reason}`),
> `is_error`, `call_id`.

Disconnecting the client (closing the curl connection) cancels the run.
`text` is required — omitting it returns `400` `{"error":"text is required"}`.

### Approve / deny a pending ask

When the stream emits a `permission.ask` with an `ask.ask_id`, resolve it on a
**second** connection while the SSE stream is still open:

```console
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<id>/approve \
       -d '{"ask_id":"<ask_id-from-the-event>","allow":true}'
# 204 No Content
```

Set `"allow":false` to deny (the model receives the denial reason and adapts).
If there is no in-flight run for the session you get `404`
`{"error":"no in-flight run for session"}`.

### Approve a presented plan (`plan:approve`)

In plan mode, once the model has presented a complete plan it calls the
`PresentPlan` signalling tool, which parks the run `awaiting` on a
**plan-approval** ask (`ask.tool == "PresentPlan"` — the tool name is the
discriminator; no provenance field on the proto). Resolve it atomically with
`POST /v1/sessions/{id}/plan:approve` ([ADR 0069](../adr/0069-plan-approval-gate.md)):

```console
$ curl -s -N -X POST http://127.0.0.1:8081/v1/sessions/<id>/plan:approve \
       -d '{"target_mode":"default","note":"looks good, proceed"}'
# 200 text/event-stream — the resumed run's events, then (on allow) the
# continuation run's events carrying the proceed message, ending with a
# terminal result {stop: "plan_approved"} then the continuation's result.
```

`target_mode` selects the verdict and the resulting posture:

- `"default"` → allow-once: flip to `default` mode (deny→ask→allow) and execute.
- `"accept_edits"` → allow-always: flip to `acceptEdits` mode (auto-accept
  edits for the execution phase).
- `"plan"` or `""` → deny/iterate: stay in plan mode, NO continuation run
  starts (the model re-plans on the next prompt).

A `409` means a precondition failed: the session has a live run (use the
`Converse` `resume_approval` frame for an in-flight run), is not `awaiting`,
or is awaiting a non-plan ask. A `404` means an unknown session. An
in-flight `Converse` run that parked on the plan ask may ALSO be resolved by
the `resume_approval` frame on its own stream; the `plan:approve` RPC is the
headless/cross-process composition of resume + continuation into one stream.

### Cancel a run

```console
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<id>/cancel
# 204 No Content
```

The run terminates with a `result` whose `stop` is `cancelled`. No in-flight run
→ `404` `{"error":"no in-flight run for session"}`.

### Mid-run steer is gRPC-only (v1)

The **steer** capability (steer-while-running, issue #512 — inject an operator
instruction into an *in-flight* run, drained at the next turn boundary) rides the
bidi gRPC `Converse` stream as a `steer` / `steer_cancel` request arm. The HTTP/SSE
run path has **no mid-run client→server channel** — `POST /v1/sessions/{id}/runs`
streams server→client only — so an HTTP/SSE client **cannot steer** in v1. Read the
`steer` bit off the `CreateSession` capabilities echo: when present/true a gRPC
client may send `steer` frames; when absent/false the server reports `too_late`
(and, over gRPC, auto-promotes the text to a fresh follow-up run). A unary
`POST /v1/sessions/{id}/steer` endpoint is a possible cheap follow-up (mirroring
`approve`/`cancel`), deferred.

### ACP over stdio (`mecated acp`)

`mecated acp` serves the **Agent Client Protocol** — JSON-RPC 2.0 over
stdin/stdout — for an editor that spawned `mecated` as a subprocess. It is the
stdio alternative to the gRPC/HTTP listeners (which are skipped); everything
else is the **same wiring**: the engine, tools, permission policy, session
store, MCP, and skills come from the same `app.Build` assembly, and the session
workspace is the editor-provided cwd. Logs go to **stderr**, so stdout carries
only JSON-RPC frames.

- There is **no TLS / auth / rate limiting** on this surface — stdio to the
  parent process is itself the trust boundary.
- Prompt content is **multimodal and capability-gated**: every content block
  becomes text, a media part, or a loud invalid-params error (image/audio is
  gated on the session provider's capabilities) — never a silent drop.
- ACP session resume (`session/load`) is advertised **only** when a durable
  session store is configured (`--store-dir`); the in-memory default would lose
  the session across a restart, so the capability is withheld.

---

See also: the [gRPC API](grpc-api.md) (the wire sibling), or the
[operator guide index](../usage.md).

