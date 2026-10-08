---
title: HTTP and SSE API reference
description:
  Look up Mecatl HTTP routes, SSE events, request bodies, and responses.
sidebar_position: 3
---

# HTTP and SSE API reference

This reference describes HTTP routes, SSE events, and response behavior. For
client integration and transport selection, see
[Connect with gRPC or HTTP](/building/grpc-http.md).

The HTTP adapter uses the same service as gRPC. Each SSE `data:` line contains
an event serialized from the generated protobuf Go value with `encoding/json`.
Field names match the gRPC event structure; protobuf enums are JSON numbers
rather than ProtoJSON enum names.

## Server identity

`GET /v1/info?provider_id=<PROVIDER_ID>` is a process-wide, state-free identity
probe. It takes no request body. `provider_id` is optional but must be the
caller's already-known active provider; it may appear exactly once. Absent,
repeated, or unknown selectors leave `llm_provider_display_endpoint`
unavailable. It returns `200 OK` with this JSON object:

```json
{
  "build_id": "dev",
  "server_implementation": "mecated",
  "llm_provider_display_endpoint": "https://api.example.com/v1"
}
```

`build_id` is the opaque linker-stamped build identity (`dev` in an unstamped
source build). `server_implementation` is the stable composition family only:
`mecated`, `mecak8s`, or embedded `mecatui`; a generic embedding uses `unknown`.
It is neither an instance identifier nor a deployment label.

The optional `llm_provider_display_endpoint` is a sanitized display value for
the supplied `provider_id`, or absent when unavailable. It retains only the
scheme, host, optional port, and escaped clean path; it omits userinfo, query,
fragment, invalid or control data, malformed values, and oversized values. It is
not connection configuration. The route does not infer a default or session
selection, discover providers, re-read configuration, inspect session state, or
list alternatives.

The route is inside the normal API authentication boundary. When `--auth-token`
(or `MECATL_AUTH_TOKEN`) is configured, send its bearer token exactly as for the
other HTTP API routes; configured TLS or mTLS requirements also apply. A caller
without required authentication receives the normal `401` response. The endpoint
never returns configuration beyond this sanitized endpoint projection, nor does
it return capabilities, topology, listener details, authentication or TLS
material, workspace paths, session or durable state, prompts, credentials, or
raw errors.

The route is additive. An older server returns `404`; clients should reduce that
to their own safe unsupported status rather than display the response body.
Clients must tolerate an absent or blank `server_implementation` in an otherwise
valid response as `unknown`, and preserve an unfamiliar non-empty family for
forward compatibility. `build_id` is not a semantic-version API.

## Sessions and runs

|Method & path|Body|Response|
|-|-|-|
|`POST /v1/sessions`|`{mode?, limits?, provider_id?, model_id?, profile?, mcp_servers?}`; `profile` omitted = server default, `"no-fs"` = explicit attenuation|`201` `{session_id, placement}` where placement is bounded display metadata; no path or exact private ref|
|`GET /v1/sessions`|No body|`200` `{sessions: [...]}` with path-free stored-session inventory|
|`GET /v1/sessions/{id}`|No body|`200` authoritative session snapshot, including title/provenance, title-generation lifecycle, and canonical durable token usage when present|
|`GET /v1/sessions/{id}/events`|No body|`200` SSE durable event replay; empty for an unknown ID, `501` without durable storage. See [Durable event replay](#durable-event-replay).|
|`GET /v1/sessions/{id}/watch?cursor=&run_id=`|No body|`200` SSE durable replay and follow, with `{event, cursor, phase}` frames. See [Durable replay and follow](#durable-replay-and-follow).|
|`POST /v1/sessions/{id}/rename`|`{title}`|`200` updated session snapshot with operator title provenance; `412` when kind/state/liveness gates reject the stale action, `409` when another replica holds the session lease|
|`POST /v1/sessions/{id}/delete`|No body|`204` after permanently removing the snapshot and store-managed sidecars; `412` when the target is active, awaiting, or not a main chat, `409` when another replica holds the session lease, `501` when the configured store cannot physically delete|
|`POST /v1/sessions/{id}/compact`|no body|`200` `{"compacted":true}` when one forced pass saved shorter model history, or `{"compacted":false}` for a successful no-op; `412` for an active/awaiting/non-main session, `409` when another replica holds its lease|
|`POST /v1/sessions/{id}/workspace-enrollment/connect`|no body|`200` safe `WorkspaceEnrollment` projection; begins an eligible pre-prompt workspace-service enrollment or observes its exact pending enrollment|
|`POST /v1/sessions/{id}/workspace-enrollment/{enrollment_id}/retry`|no body|`200` safe `WorkspaceEnrollment` projection; cancels the exact pending enrollment before beginning its replacement; stale IDs return `412`|
|`POST /v1/sessions/{id}/workspace-enrollment/{enrollment_id}/cancel`|no body|`200` safe `WorkspaceEnrollment` projection; cancels only the exact pending enrollment and clears its prompt gate; stale IDs return `412`|
|`DELETE /v1/sessions/{id}`|No body|`204`; close the session and release runtime resources while retaining stored state|
|`POST /v1/sessions/{id}/prompt`|`{text}`|`200` `text/event-stream` of events; rejected while failed-step retry intent is pending|
|`POST /v1/sessions/{id}/retry`|no body|`200` `text/event-stream` for a prompt-free failed-step retry; reuses conversation/tool state but re-resolves live instruction sources; `409` unless persisted state is eligible|
|`POST /v1/sessions/{id}/controls/resolve-ask`|`{expected_run_id, ask_id, verdict, review_id?, guardrail_kind?}`; `verdict` is `deny`, `allow_once`, or `allow_always`|`200` `{run_id, ask_id}`; `409 stale_run_control` when the run is no longer live|
|`POST /v1/sessions/{id}/plan:approve`|`{"target_mode": "default" \| "accept_edits" \| "plan", "note": "..."}`|`200` `text/event-stream` — atomically resolve a parked **plan-approval** ask: on `default`/`accept_edits` resume the parked run AND start the continuation run (both streamed); on `plan`/`""` iterate (no continuation). `409` on a precondition failure (live run / not awaiting / not a plan ask), `404` on an unknown session|
|`POST /v1/sessions/{id}/controls/cancel`|`{expected_run_id}`|`200` `{run_id}`; `409 stale_run_control` when the run is no longer live|
|`POST /v1/sessions/{id}/cancel-child`|`{child_id}`|`204`; `404` for an unknown / already-finished child|
|`POST /v1/sessions/{id}/controls/steer`|`{expected_run_id, text?, parts?, message_id?}`; text or at least one part is required|`200` `{outcome, run_id, message_id}`; `409 stale_run_control` when the run is no longer live|
|`POST /v1/sessions/{id}/controls/cancel-steer`|`{expected_run_id, message_id?}`|`200` `{outcome, run_id, message_id}`; `409 stale_run_control` when the run is no longer live|
|`POST /v1/sessions/{id}/clear`|`{"worktree_selector":"..."}` optional|`201` `{session_id, placement}` — distinct empty-history successor; omitted selector inherits exact source placement|
|`POST /v1/sessions/{id}/fork`|optional `{title, reasoning_effort, provider_id, model_id, worktree_selector}`|`201` `{session_id, placement}` — history-carrying successor; omitted selector inherits exact placement, supplied selector must be fresh and source-scoped; all overrides resolve atomically|

### Durable event replay

`GET /v1/sessions/{id}/events` replays the complete durable timeline, including
`session.title` changes and log-only `approval`, `compaction_archive`, and
`user_prompt` events omitted from a live prompt stream. A replayed
`user_prompt.synthetic=true` identifies a server-authored continuation. An
absent or false value means a genuine prompt or an event whose origin is
unknown; clients must not infer origin from text.

### Durable replay and follow

`GET /v1/sessions/{id}/watch` replays durable events, then stays open to follow
new appends. Each SSE `data:` frame contains `{event, cursor, phase}`, unlike
`/events`, which sends bare events and ends after replay. The open-string
`phase` values are `replay`, `live`, and `gap`; clients must tolerate unknown
values. Exactly one event-less `live` frame marks the replay-to-live boundary.
An event-less `gap` frame identifies a known failed durable append.

The cursor is opaque; an empty value starts at the beginning. Persist it only
after processing its frame and reconnect with the last processed cursor.
Optional `run_id` narrows delivery to one run. Resume with that same filter: a
filtered watch advances past records it omits, so changing or removing the
filter can skip them. Start from the beginning to change filters.

|HTTP status|Meaning|
|-|-|
|`501`|No durable event log or no cursor support.|
|`404`|The caller may not read the session.|
|`400`|The session ID identifies a delegation child.|
|`200`|Streaming response accepted; later failures use terminal SSE error frames.|

Cursor decoding occurs after the `200` response is committed. Malformed and
expired cursors therefore produce a terminal `event: error` frame, rather than
an HTTP error status. Its `data:` object contains `code` and `error`:

|Code|Meaning and recovery|
|-|-|
|`cursor_malformed`|The cursor is invalid.|
|`cursor_expired`|Restart from the beginning.|
|`watch_capacity`|Follower admission is full; reconnect with bounded backoff.|
|`watch_lagging`|The client did not consume the bounded buffer quickly enough; reconnect from the last processed cursor.|
|`activity_gap`|Recorded events are missing; retry cannot recover them.|

Both capacity and lagging errors are resumable with the same `run_id` filter.

### Workspace enrollment

`WorkspaceEnrollment` contains only `enrollment_id`, `status`,
`required_services`, and the ephemeral `presentation_url` when a new enrollment
needs browser presentation. These unary controls reject every request body,
validate the session and enrollment correlation from the path, retain the
request context, and do not expose callbacks, selectors, or broker state.

### Client-provided MCP servers

`mcp_servers` mounts client-provided streaming-HTTP MCP servers for the created
session's lifetime, via a per-session engine. Each entry is
`{name, url, type?, headers?}`, the HTTP equivalent of the gRPC
`CreateSessionRequest.mcp_servers` field, documented in full in
[the gRPC API guide](./grpc-api.md). The short version: client MCP is a separate
listener-scoped outbound-network/credential policy.
Only a `--grpc-unix-socket` daemon with `--http-addr ""` accepts it. This
excludes HTTP deployments, which use a TCP listener; every other deployment,
loopback included, returns `501` / `client_mcp_unsupported`. Check
`mcp_servers_on_create` in `GET /v1/compatibility` `features` first. Mounting is
all-or-nothing: a server that does not connect fails the create with `503` /
`client_mcp_unreachable` rather than returning a session quietly missing its
tools. A `stdio` or `sse` entry is `400` on every deployment (Mecatl never
spawns an MCP server process); each `name` must be 1-64 chars of
`[A-Za-z0-9._-]`, contain no `__`, and be unique in the request; a URL carrying
userinfo credentials is `400` (use `headers`); and header values are never
logged, evented, or echoed in an error.

### Strict request decoding

The create route rejects every unrecognized body field with `400` and names the
field. Use `mcp_servers`; the ProtoJSON spelling `mcpServers` is invalid.

### Server-owned placement

The create body has no workspace, cwd, placement ID, exact EnvironmentRef, or
worktree selector. Local/embedded `--workspace` is trusted server configuration
only. `GET /v1/commands?session_id=...` and `GET /v1/worktrees?session_id=...`
authorize and exactly reattach that source session; no-FS returns empty without
touching filesystem providers. Worktrees contain bounded display metadata and an
opaque caller/source-scoped selector accepted only by the clear and fork
successor routes. Selectors are not paths or durable IDs and expire on server
restart, so clients relist. Sessions/snapshots and trusted driver storage retain
the exact private `EnvironmentRef{kind,id,revision}`; HTTP projections never do.

## Inventory, introspection, and learning controls

These routes mirror the gRPC inventory, introspection, and learning methods.

|Method & path|Response|
|-|-|
|`GET /v1/models`|the selectable provider/model inventory (`ListModels`)|
|`GET /v1/agents`|the agent-definition inventory|
|`GET /v1/skills`|the skills inventory|
|`GET /v1/commands?session_id=...`|slash commands for an owned, exactly reattached source; no-FS returns empty|
|`GET /v1/worktrees?session_id=...`|display-safe worktrees plus ephemeral caller/source-scoped selectors; no paths/exact refs|
|`GET /v1/soul`|the resolved soul snapshot (provenance, trust, drift)|
|`GET /v1/usermodel`|the live bounded user-model index; `?key=<exact-key>` also returns read-only value/version/provenance/proposal linkage/timestamps/bounded history when available|
|`POST /v1/sessions/{id}/reflect`|synchronously reflect a caller-owned completed session on its persisted provider (optional empty/`{}` body); returns bounded abstained/staged/promoted/conflicted counts|
|`GET /v1/learning/attempts`|content-free caller-partitioned attempt page; accepts closed `state`, opaque `cursor`, and bounded `limit` (default 50, maximum 200)|
|`GET /v1/learning/attempts/{id}`|content-free attempt lifecycle detail; foreign and missing IDs both return `404`|
|`POST /v1/learning/attempts/{id}/retry`|body `{"expected_version":"<opaque>"}`; failed-to-queued attempt CAS only|
|`POST /v1/learning/attempts/{id}/abandon`|body `{"expected_version":"<opaque>"}`; non-compensating attempt CAS only, with no downstream rollback promise|
|`POST /v1/dream/plans`|body `{"target":"project_memory"}` or `{"target":"user_model"}`; spends one planner call and returns the bounded-lifetime exact/synthesized review plan plus opaque process-local id|
|`POST /v1/dream/plans/{plan_id}/decision`|body `{"decision":"apply"}` or `{"decision":"dismiss"}`; decides the authoritative retained whole plan and returns planned/applied/conflicted/skipped/failed source counts|
|`GET /v1/learning/proposals`|bounded caller-partitioned proposal page (`status`, `cursor`, `limit`, optional reviewable `project`; promotion remains launch-root/trust-gated)|
|`GET /v1/learning/proposals/{id}`|bounded proposal detail with digest availability, never raw evidence text|
|`POST /v1/learning/proposals/{id}/decision`|approve/reject with `expected_version`; stale versions return `409`|
|`POST /v1/learning/proposals/{id}/undo`|compensating undo with `expected_version`; stale/current-memory conflicts return `409`|
|`GET /v1/mcp/resources`|MCP resource snapshots|
|`GET /v1/mcp/resources/read`|read one MCP resource by URI|
|`GET /v1/mcp/prompts`|the MCP prompt inventory|
|`POST /v1/mcp/prompts/get`|expand one MCP prompt (rendered messages)|
|`GET /v1/mcp/sources`|cached published/pre-shadow MCP sources plus revision, stale, and reconciling status|
|`POST /v1/sessions/{id}/mcp-refresh`|bodyless direct MCP refresh for an eligible owned root; returns request-pinned revision and changed|
|`GET /v1/mcp/toolhive/groups`|the ToolHive groups in the resolved inventory|

### Manual dream review

Manual dream generation sends the selected bounded memory values/descriptions to
the configured planner and spends tokens; regeneration is explicit and spends
again. Apply/dismiss is whole-plan, with no client-supplied operations or
per-source toggles. Exact duplicates keep the survivor; approved synthesis
atomically rewrites the displayed survivor and tombstones the displayed sources
per operation. Independent operations can yield a partial receipt.

Plans expire after ten minutes and exist only on the generating process: expiry,
restart, or a wrong replica returns `404` and makes the old decision
non-retryable while allowing explicit fresh generation. A same-decision request
while apply is running returns `409` and remains explicitly retryable; an
opposite request returns `412` while apply is active and `410` after the
opposite terminal decision. Only the terminal case offers fresh generation.

Indeterminate transport failures preserve the exact plan ID and decision for
same-decision retry because the first request may already have applied. No error
path offers the opposite decision.

Capacity pressure returns `429`, and an unavailable deployment/target `501`.
Ownership enforcement disables manual dream review. This does not change the
separate, off-by-default schedule flags or `learning.mode`, and no recall
counters or provider/model identity are returned.

## Agent teams

Team routes are enabled by default through `--enable-teams`.

|Method & path|Body|Response|
|-|-|-|
|`POST /v1/teams`|team spec (incl. the tighten-only `max_team_tokens?`)|create a team|
|`POST /v1/teams/{id}/members`|member spec|spawn a teammate|
|`POST /v1/teams/{id}/messages`|message|post into a member's inbox|
|`POST /v1/teams/{id}/members/cancel`|`{"member": "..."}`|cancel one member of a running team (404 unknown team/member, 412 not running)|
|`POST /v1/teams/{id}/run`|No body|`text/event-stream` of `TeamEvent`s, ending with the terminal `outcome` frame|
|`GET /v1/teams/{id}`|No body|team snapshot (roster, tasks, quiescence)|
|`DELETE /v1/teams/{id}`|No body|clean up the team|

## Session request examples

The examples use a local `mecated serve --mock` HTTP listener at
`127.0.0.1:8081`. Session IDs in responses are illustrative; use the ID returned
by your own create request.

### Create a session

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions -d '{}'
{"session_id":"8867bdea940108c1dd82d13d3fb7fc61","placement":{"kind":"local"}}
```

Optional fields:

```json
{
  "mode": "plan",
  "limits": {
    "max_turns": 20,
    "max_tool_calls": 80,
    "max_consecutive_failures": 3
  }
}
```

`mode` accepts `default`, `plan`, `acceptedits` (also `accept_edits` /
`accept`); unknown/empty falls back to the server default (`default`). Each
omitted or zero `limits` field inherits its deployment default:

|Limit|Deployment default|
|-|-|
|`max_turns`|`2000`|
|`max_tool_calls`|`8000`|
|`max_consecutive_failures`|`5`|

A non-zero field overrides only that limit; other zero fields still inherit
their defaults.

A `workspace`, `cwd`, placement ID, or exact environment ref is an unknown field
and the strict decoder returns `400`; configure local `--workspace` on the
server.

### Create a no-filesystem session (`profile: "no-fs"`)

The `no-fs` profile creates a session without filesystem tools, suitable for
research or coordination through MCP, memory, and web tools:

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions \
       -d '{"profile":"no-fs"}'
{"session_id":"..."}
```

The gRPC `CreateSessionRequest` accepts the same profile strings: `""` selects
the deployment default and `"no-fs"` selects the filesystem-free profile. The
server enforces these rules:

- `"no-fs"` binds the server's filesystem-free placement; no workspace field
  exists. Omitted profile binds the server's deployment default.
- The server rejects every other profile value.
- The no-FS session excludes
  Read/ListDir/Edit/Write/Copy/Move/Remove/Grep/Glob/Shell/ShellStatus,
  Parallel, and SkillDraft. It keeps MCP tools (server-global + resource
  meta-tools + client MCP), the six memory tools, WebFetch, WebSearch, Skill
  (bodies are text injection; out-of-workspace skill assets are unreadable), and
  delegation. Subagent and Team children use the same file-free tools, with no
  shell or worktree/fork isolation.
- A system-prompt posture note and the Subagent tool description explain the
  available MCP, memory, and web tools to the model.
- The profile composes with `provider_id`/`model_id` and remains fixed for the
  session lifetime.

### Inspect a session

```sh
$ curl -s http://127.0.0.1:8081/v1/sessions/8867bdea940108c1dd82d13d3fb7fc61
{"session_id":"8867bdea940108c1dd82d13d3fb7fc61","state":"idle","mode":"default","placement":{"kind":"local"},"turns":0,"tool_calls":0}
```

A missing ID returns `404` `{"error":"not found: \"...\""}`.

### Compact model history without a turn

Use the bodyless manual operation when the next prompt may not fit or when you
want to reduce stored model history before continuing:

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/compact
{"compacted":true}
```

The server runs the configured compactor once regardless of the automatic 0.8
trigger. It adds no prompt and starts no model turn, although the cascade
strategy may make a compaction-slot summarization call. `false` is a successful
no-op and causes no save or event append. The legal states are idle, completed,
cancelled, and failed; state is preserved. Running/awaiting, scheduled, child,
or same-process live sessions return `412`. A lease held by another replica
returns `409`. Missing and foreign-owned IDs both return `404`; configured HTTP
authentication still applies before ownership checks.

On change, the compacted snapshot is saved before the existing compaction notice
and archive are appended. Save failure returns `500` without appending them. An
event-log append failure after save does not roll back the snapshot or change
the `200` response, so the log may lack that manual compaction record. Clients
can check `capabilities.manual_compaction` on session creation; an old server
leaves it false and returns `404` for the unknown route.

### Start a run (SSE stream)

```sh
$ curl -s -N -X POST http://127.0.0.1:8081/v1/sessions/8867bdea940108c1dd82d13d3fb7fc61/prompt \
       -d '{"text":"hello"}'
data: {"type":"turn.start","seq":1}

data: {"type":"message.delta","seq":2,"text":"Mock provider: no real model is configured. Set --openai/OPENAI_API_KEY for live use."}

data: {"type":"result","seq":3,"result":{"stop":"end_turn","text":"Mock provider: no real model is configured. Set --openai/OPENAI_API_KEY for live use.","usage":{}},"usage":{}}
```

`-N` disables curl buffering so events arrive as they stream. The mock provider
returns one text turn. A real model can also emit `tool.call`, `tool.result`,
and `permission.ask` for calls that need approval. JSON field names include
`tool_call`, `tool_result`, `ask` (`{ask_id, tool, args, reason}`), `is_error`,
and `call_id`.

Disconnecting the client (closing the curl connection) cancels the run. `text`
is required; omitting it returns `400` `{"error":"text is required"}`.

### Retry the failed model step

When a terminal result explicitly carries `retry_disposition: 2` (retryable) and
`stream_progress: 2` (precommit) or `3` (visible), repeat that exact model step
without submitting another prompt. The concrete SSE mappings are
`retry_disposition`: `0` unspecified, `1` unknown, `2` retryable, `3` permanent;
and `stream_progress`: `0` unspecified, `1` unknown, `2` precommit, `3` visible,
`4` complete. For example, an automatically safe retry result contains
`"retry_disposition":2,"stream_progress":2`.

```sh
$ curl -s -N -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/retry
data: {"type":"session.init","seq":1}

# model events follow, ending with a terminal result
```

The route has no request body. It persists failed-step retry intent before
launch and adds no user message. Persisted conversation/tool state is reused
while live turn-0 instructions, operator profile, and system-prompt sources are
re-resolved. Normal `/prompt` requests are rejected while intent is pending. A
clean pre-turn brake leaves it pending; cancellation clears it. A `409` means
the server rejected eligibility. Retrying after `VISIBLE` output is an explicit
operator choice; automated clients should use the narrower typed
`RETRYABLE + PRECOMMIT` case and a finite retry bound.

The terminal Result fields are optional for wire compatibility. Presence with
`UNKNOWN` is an explicit conservative answer from a new server. Absence means
the server predates typed semantic retry and is not evidence that replay is
safe.

### Approve / deny a pending ask

Run controls target a specific run. Each run event includes its `run_id`;
pass that value as the required `expected_run_id`. Check for the `prompt_free_controls`
feature before using the `controls/*` routes.

When the stream emits a `permission.ask` with an `ask.ask_id`, resolve it on a
**second** connection while the SSE stream is still open:

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/controls/resolve-ask \
       -H 'Content-Type: application/json' \
       -d '{"expected_run_id":"<RUN_ID>","ask_id":"<ASK_ID>","verdict":"allow_once"}'
{"run_id":"<RUN_ID>","ask_id":"<ASK_ID>"}
```

`verdict` is `allow_once`, `allow_always`, or `deny`. For a contextual
guardrail ask, also pass the `review_id` and `guardrail_kind` from the ask. If the named run has
finished or been replaced, the server returns `409` with the problem code
`stale_run_control`.

### Approve a presented plan (`plan:approve`)

In plan mode, once the model has presented a complete plan it calls the
`PresentPlan` signalling tool, which parks the run `awaiting` on a plan-approval
ask. `ask.tool == "PresentPlan"` identifies it; the protobuf ask has no
provenance field. Resolve it atomically with
`POST /v1/sessions/{id}/plan:approve`:

```sh
$ curl -s -N -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/plan:approve \
       -d '{"target_mode":"default","note":"looks good, proceed"}'
# 200 text/event-stream: resumed run events, then (on allow) the
# continuation run's events carrying the proceed message, ending with a
# terminal result {stop: "plan_approved"} then the continuation's result.
```

`target_mode` selects the verdict and the resulting posture:

- `"default"` → allow-once: flip to `default` mode (deny→ask→allow) and execute.
- `"accept_edits"` → allow-always: flip to `acceptEdits` mode (auto-accept edits
  for the execution phase).
- `"plan"` or `""` → deny/iterate: stay in plan mode, no continuation run starts
  (the model re-plans on the next prompt).

A `409` means a precondition failed: the session has a live run (use the
`Converse` `resume_approval` frame for an in-flight run), is not `awaiting`, or
is awaiting a non-plan ask. A `404` means an unknown session. An in-flight
`Converse` run that parked on the plan ask can also be resolved by the
`resume_approval` frame on its own stream; the `plan:approve` RPC is the
headless/cross-process composition of resume + continuation into one stream.

### Cancel a run

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/controls/cancel \
       -H 'Content-Type: application/json' \
       -d '{"expected_run_id":"<RUN_ID>"}'
{"run_id":"<RUN_ID>"}
```

The run terminates with a `result` whose `stop` is `cancelled`. If the named run
is no longer live, the server returns `409 stale_run_control`.

### Steer a running session

A steer injects operator input into an in-flight run. The run commits it at the
next turn boundary, after any current model response and tool batch settle. HTTP
clients use the unary `controls/steer` and `controls/cancel-steer` routes. gRPC
clients use the `steer` and `steer_cancel` arms on the bidirectional `Converse`
stream. ACP does not support steer.

Check for both the runtime `steer` capability and the `http_steer` compatibility
feature before using the HTTP routes. These advertised values identify whether
the server supports HTTP steering.

Send text, multimodal parts, or both, addressed to the run by its `run_id`:

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/controls/steer \
       -H 'Content-Type: application/json' \
       -d '{"expected_run_id":"<RUN_ID>","text":"Use the existing parser","message_id":"client-42"}'
{"outcome":"accepted","run_id":"<RUN_ID>","message_id":"client-42"}
```

The `parts` array uses the same `{kind, mime_type, data?, url?}` content blocks
as an HTTP prompt. A message can contain at most 16 parts, with at most 10 MiB per inline part
and 20 MiB of inline media in total. The selected provider must support every
supplied media kind.
`message_id` is an optional client correlation value. It must be no longer than
64 Unicode code points. A pending bundle can accept multiple steers: the first
returns `accepted`, and later fragments return `appended`. The eventual `steer`
event echoes the latest message ID as a watermark for the committed bundle.

A steer applies only to the named run. If that run is absent, has finished,
has been replaced, or ends before the steer can be committed, the server
returns `409` with the problem code `stale_run_control`. Send a new prompt
to continue.

Retract a pending bundle before it reaches a turn boundary:

```sh
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<SESSION_ID>/controls/cancel-steer \
       -H 'Content-Type: application/json' \
       -d '{"expected_run_id":"<RUN_ID>","message_id":"cancel-42"}'
{"outcome":"retracted","run_id":"<RUN_ID>","message_id":"cancel-42"}
```

When no bundle can be retracted, the outcome is `none_pending`. A cancel-steer
for a run that is absent or no longer active returns `409 stale_run_control`.

All `controls/*` routes decode JSON strictly. Unknown fields, trailing JSON, a
missing `expected_run_id`, a missing steer payload, invalid content, and an
overlong `message_id` return `400`. Bodies larger than 32 MiB return `413`.

## ACP over stdio (`mecated acp`)

`mecated acp` serves the Agent Client Protocol as JSON-RPC 2.0 over stdin/stdout
for an editor that owns the `mecated` subprocess. It disables the gRPC and HTTP
listeners while using the same engine, tools, permission policy, session store,
MCP, and skills composition.

ACP `session/new` binds the trusted configured placement and `session/load`
exactly reattaches the persisted private ref before access. The editor-provided
cwd is only a local consistency assertion; a mismatch is rejected and cwd cannot
select or construct authority. Other ACP session, discovery, error, and event
projections remain path-free. Logs go to **stderr**, so stdout carries only
JSON-RPC frames.

- The stdio connection to the parent process is the trust boundary. ACP supplies
  no TLS, authentication, or rate limiting.
- Prompt content is **multimodal and capability-gated**: every content block
  becomes text, a media part, or an invalid-params error (image/audio is gated
  on the session provider's capabilities). Unsupported content is rejected
  rather than silently dropped.
- ACP session resume (`session/load`) is advertised **only** when a durable
  session store is configured (`--store-dir`); the in-memory default would lose
  the session across a restart, so the capability is withheld.

---

## Related information

- [gRPC API reference](./grpc-api.md)
- [Connect with gRPC or HTTP](/building/grpc-http.md)
