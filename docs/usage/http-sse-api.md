## 5. The HTTP / SSE API

The HTTP adapter wraps the same service. Every event is emitted as one SSE
`data:` line carrying the proto `Event` marshalled to JSON — so HTTP and gRPC
share one event shape.

**Sessions & runs:**

| Method & path | Body | Response |
| --- | --- | --- |
| `POST /v1/sessions` | `{workspace, mode?, limits?, provider_id?, model_id?, profile?}` | `201` `{session_id}` |
| `GET /v1/sessions/{id}` | — | `200` session snapshot |
| `DELETE /v1/sessions/{id}` | — | `204` — close the session, releasing its per-session resources |
| `POST /v1/sessions/{id}/prompt` | `{text}` | `200` `text/event-stream` of events |
| `POST /v1/sessions/{id}/approve` | `{ask_id, allow}` | `204` |
| `POST /v1/sessions/{id}/cancel` | — | `204` |
| `POST /v1/sessions/{id}/cancel-child` | `{child_id}` | `204`; `404` for an unknown / already-finished child |

**Inventory & introspection** (the HTTP mirrors of the gRPC inventory RPCs in §4):

| Method & path | Response |
| --- | --- |
| `GET /v1/models` | the selectable provider/model inventory (`ListModels`) |
| `GET /v1/agents` | the agent-definition inventory |
| `GET /v1/skills` | the skills inventory |
| `GET /v1/commands` | the slash-command palette for a workspace |
| `GET /v1/soul` | the resolved soul snapshot (provenance, trust, drift) |
| `GET /v1/usermodel` | the live user-model index |
| `GET /v1/mcp/resources` | MCP resource snapshots |
| `GET /v1/mcp/resources/read` | read one MCP resource by URI |
| `GET /v1/mcp/prompts` | the MCP prompt inventory |
| `POST /v1/mcp/prompts/get` | expand one MCP prompt (rendered messages) |
| `GET /v1/mcp/sources` | the resolved MCP source inventory |
| `GET /v1/mcp/toolhive/groups` | the ToolHive groups in the resolved inventory |

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

All examples below were captured against a live `mecated --mock`.

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
substituted (see §6). `workspace` is required for the default profile —
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
- The no-FS session has **no** Read/Edit/Write/Grep/Glob/Bash, no Parallel, and
  no SkillDraft. It keeps MCP tools (server-global + resource meta-tools +
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

### Cancel a run

```console
$ curl -s -X POST http://127.0.0.1:8081/v1/sessions/<id>/cancel
# 204 No Content
```

The run terminates with a `result` whose `stop` is `cancelled`. No in-flight run
→ `404` `{"error":"no in-flight run for session"}`.

### ACP over stdio (`--acp`)

`mecated --acp` serves the **Agent Client Protocol** — JSON-RPC 2.0 over
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

