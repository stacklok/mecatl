# mecatl — Usage & Operator Guide

`mecatl` is a headless, agentic coding harness. It owns its own context
window, tool loop, permission policy and lifecycle hooks, and talks to OpenAI
(or any OpenAI-compatible `/v1/responses` endpoint), OpenRouter, Anthropic
(the native Messages API), or OpenCode Go (Chat Completions — and any
OpenAI-compatible `/v1/chat/completions` endpoint), plus the experimental
`openai-codex` provider for a manually supplied ChatGPT Codex subscription
token. The server, `mecated`, exposes one agent run over
**gRPC** and **HTTP/SSE** concurrently — or, opt-in, over **ACP on stdio** for
an editor that spawned it.

> Security, up front: **the `mecated` API is UNAUTHENTICATED by default.** It
> exposes command and file execution against the configured workspace, so out
> of the box it is intended for **localhost, single-user** use — the default
> listen addresses bind the loopback interface (`127.0.0.1`). Binding a
> non-loopback address without protection exposes unauthenticated command/file
> execution to the network. Before any non-loopback bind, enable the built-in
> protections: bearer auth (`--auth-token` / `MECATL_AUTH_TOKEN`), TLS
> (`--tls-cert` / `--tls-key`), mutual TLS (`--client-ca`), and rate limiting
> (`--rate-limit` / `--rate-burst`) — see [mecated](usage/mecated.md).
>

> **How to read this guide:** sections 1–4 (install → quickstart → mecated →
> guardrails) are a **linear new-operator path**; the remaining sections are
> **independent topic branches** — pick any that cover your area.
>
> For the underlying architecture, see the [architecture overview](architecture.md)
> and the [progressive reading map](READING.md).

---

## OAuth protected-resource discovery

`mecated` and `mecak8s` share the optional RFC 9728 profile flags
`--oidc-resource`, `--oidc-client-id`, and `--oidc-scopes`. The resource must be
an operator-supplied absolute HTTPS URL; Helm exposes the equivalent
`oidc.resource`, `oidc.clientID`, and `oidc.scopes` values. Metadata advertises
standard `resource`, `authorization_servers`, and `bearer_methods_supported: ["header"]`
fields separately from mecatl's audience/client-id extensions. Every protected API
route advertises the configured resource's metadata URL as its service-wide base;
the server never derives it from a request Host or path. Anonymous metadata
and OIDC discovery are bootstrap-only and remain separate from authenticated
gRPC transport. ToolHive is implementation provenance for the remote client
adapter, not a runtime engine dependency. Without the profile, explicit OIDC
login and existing issuer/audience behavior are unchanged.

## Managed temporary storage (Linux and macOS)

By default Bash commands use a private managed temporary lease. The harness removes
that lease after normal command completion and a bounded Build-owned maintenance
worker recovers validated abandoned command/job leases after the configured TTL.
The worker never scans arbitrary system temporary directories and does not delay a
command allocation. This lifecycle is available on Linux and macOS; other
platforms must use `mode: system`.

To use the inherited or configured system temporary directory instead, an operator
sets the user-global (not project) `settings.yaml` value below. System mode is the
rollback switch: it stops new managed leases and reaping, and it leaves existing
managed data untouched for manual inspection or removal.

```yaml
# ~/.config/mecatl/settings.yaml
temporary_storage:
  mode: system # managed is the Linux and macOS default
```

Managed mode accepts `managed_root`, `system_temp_dir`, `command_reap_after`,
`reap_interval`, `reap_timeout` (default five minutes), and
`shutdown_reap_timeout` (default one minute). Each workspace manifest also records
its canonical current path for owner-only debugging; it is refreshed when that managed
workspace key is opened from a new path. These are operator controls; project
settings cannot redirect or weaken cleanup. A Bash call may request `temp_scope:
system` only when ordinary Bash permission and the separate `BashSystemTemp`
capability are both allowed. See [ADR 0281](adr/0281-managed-temporary-command-leases.md).

---

## Build identity and safe diagnostics

Every shipped executable accepts exact top-level `--version` and prints its build id without starting normal configuration or services. Ordinary `task build`, `task install`, and Taskfile-driven ko builds resolve their source identity at build time with `git describe --tags --match 'v[0-9]*' --always --dirty`: the most recent root release tag, commits since it, abbreviated SHA, and an optional dirty suffix (for example, `v0.0.22-28-g40a6b3fc6-dirty`). `BUILD_ID=<value>` preserves that explicit linker stamp verbatim, including `dev`. Direct Go or ko builds without a stamp do not invoke git at runtime; they fall back to embedded VCS metadata as `dev+<12-char-vcs-revision>[.dirty]`, or `dev` if metadata is unavailable or invalid. Authenticated clients can read the server build identity and sanitized diagnostic display endpoint projections through gRPC `GetServerInfo` or HTTP `GET /v1/info`; these are not connection configuration or instructions. The detailed transport contracts are in [the gRPC API](usage/grpc-api.md) and [the HTTP/SSE API](usage/http-sse-api.md). Mecatui's `/diagnostics` behavior is documented in [the TUI guide](tui.md).

---

## Table of contents

| Section | File |
| --- | --- |
| 1. Prerequisites & install | [install.md](usage/install.md) |
| 2. The 60-second demo | [quickstart.md](usage/quickstart.md) |
| 3. Running the server (`mecated`) | [mecated.md](usage/mecated.md) |
| 4. Guardrails | [guardrails.md](usage/guardrails.md) |
| 5. Model routing (slots, aliases, router) | [model-routing.md](usage/model-routing.md) |
| 6. Workspace trust & posture | [workspace-trust.md](usage/workspace-trust.md) |
| 7. Skills, soul, user model | [skills-soul-usermodel.md](usage/skills-soul-usermodel.md) |
| 8. mecak8s (Kubernetes-native agent) | [mecak8s.md](usage/mecak8s.md) |
| 9. The gRPC API | [grpc-api.md](usage/grpc-api.md) |
| 10. The HTTP / SSE API | [http-sse-api.md](usage/http-sse-api.md) |
| 11. Configuration (settings, stores, leases) | [configuration.md](usage/configuration.md) |
| 12. Permissions | [permissions-config.md](usage/permissions-config.md) |
| 13. Hooks | [hooks.md](usage/hooks.md) |
| 14. OpenAI & compatible endpoints | [openai-compatible.md](usage/openai-compatible.md) |
| 15. Running mecatequi from GitHub Actions | [mecatequi-ci.md](usage/mecatequi-ci.md) |
| 16. Live e2e suite | [e2e.md](usage/e2e.md) |
| 17. Troubleshooting / FAQ | [troubleshooting.md](usage/troubleshooting.md) |
| 18. OpenAI Codex subscription | [OpenAI Codex subscription](#openai-codex-subscription-experimental) |
| 19. ToolHive LLM gateway | [ToolHive LLM gateway](#toolhive-llm-gateway) |

## mecatui command discovery

`mecatui --help`, `mecatui -h`, and `mecatui help` render the concise command index.
`mecatui help sessions`, `mecatui help connect`, `mecatui help debug`, and `mecatui help login` alias the
corresponding command-specific help; direct `sessions --help`, `connect --help`, `debug --help`, and
`login --help` remain available. Use bare `mecatui --help-flags` for common embedded-mode
flags, or `--help-all` with bare `mecatui`, `sessions`, or `connect` for every applicable
flag. `mecatui llm login` supports standard help and `--skip-browser`; it opens
the ToolHive LLM gateway OIDC flow only. `mecatui login ADDRESS` instead supports the
remote issuer/client/audience/optional-CA/callback options and enrolls that remote target.
It defaults to public issuer address admission with system roots; optional `--tls-ca`
replaces those roots, while `--private-issuer` requires `--tls-ca` and admits private
addresses only.

Inside the TUI, `/retry` manually repeats the last typed `retryable` failed model
step when it is still retry-pending. Mecatui automatically retries
`retryable + precommit` only once; a second precommit failure or any
`retryable + visible` failure requires `/retry`. The command sends no new prompt,
preserves the textarea and queued prompts, and reports a harmless status when no
eligible failure exists. Historical transcript replay never triggers automatic retry.

Inside the TUI, `/title <text>` renames the active session through the existing
server rename operation; bare `/title` displays its title and provenance without a
mutation. A manual title ends automatic generation for that session. Automatic titles
are opt-in: configure a compatible explicit `models.slots.title` binding (see
[model routing](usage/model-routing.md)); the server then schedules bounded work after
a completed exchange is durably persisted, without delaying or changing the chat. On
startup it recovers the bounded pre-submission gap for completed pending sessions with
source prompts but no attempt; a crash-unknown claimed attempt is never retried. Its
durable `session_title` token usage is separate from normal session/run usage.
Operators can diagnose the server-owned lifecycle through session-correlated
diagnostics: submission, admission/eligibility, claim, selected provider/model,
completion outcome, token counts, and conditional-commit loss. Failed calls report
only stable classifications (`deadline`, `cancelled`, `provider`, `invalid-output`, or
`protocol`) and a stage; diagnostics never include source prompts, provider error
text, credentials, or model output. Live title updates are best-effort, so reconnect
and session reopen re-fetch the authoritative stored title.

When a server advertises bundled protected workspace services, `/tools-connect`
starts or rechecks their enrollment and `/tools-cancel` cancels the pending bundle.
The prompt remains available while consent is pending; if the server rejects a
prompt until enrollment completes, mecatui keeps it and submits it once after a
connected control response. These commands are absent when the server does not
advertise enrollment support.

## mecatui remote TLS

`mecatui connect ADDRESS` resolves TLS after it has the target: omitted `--tls`
uses verified TLS for non-loopback or unparseable targets and plaintext for
loopback. `--tls`/`--tls=true`, `--tls-ca`, and `--insecure` select TLS;
`--tls=false` is the explicit plaintext downgrade. It conflicts with
`--tls-ca` and `--insecure`, which also conflict with each other. A bearer is
refused on both unsafe transports to a non-loopback target: explicit plaintext
and `--insecure`, whose unverified TLS hides an MITM rather than a listener. A saved OIDC
connection always verifies the gRPC server TLS, and its issuer CA is never used
as server trust; `connect --tls-ca` is the sole custom server-CA input.

Authentication resolution is: explicit static token, explicit `--anonymous`, saved OIDC
enrollment, then a credential-free attempt for any clean missing enrollment. A static token
wins even when `--anonymous` is present; otherwise `--anonymous` bypasses saved credentials
and has no environment equivalent. Only an actual `Unauthenticated` RPC offers OIDC/token
recovery; corrupt or unreadable registry, keyring, or credential state fails closed. Login
is persistent OIDC enrollment, not anonymous login.

### Tailscale shared-authority deployment

A credential-free Tailscale deployment makes tailnet ACLs the shared authority boundary; TLS
is not caller authentication. Bind `mecated` to one concrete Tailscale address, never a
wildcard or Funnel, use a dedicated low-privilege server workspace, and configure a
restrictive rate limit. Use `--posture strict` (or `trusted` only for trusted project inputs):
`auto` and `yolo` weaken the remaining approval boundary.

```sh
mecatui connect ozzllama:9080 --tls=false
```

Remote credential-free connections retain verified TLS by default; the separate plaintext
flag explicitly relies on Tailscale transport. Do not infer anonymous safety from an address
or hostname. For non-loopback connections the server owns the workspace.

## Server-owned session placement

Clients never send a filesystem path, cwd, exact environment reference, or general
placement ID when creating or resuming a session. `CreateSession` binds the trusted
deployment default when `profile` is omitted, or the filesystem-free environment when
`profile` is `no-fs`. Local `--workspace` configures the embedded/daemon server privately;
it is not a field sent by `mecatui connect`.

Alternate worktrees are discovered from an owned source session. `ListWorktrees(session_id)`
returns bounded display metadata and an opaque caller/source-scoped selector. The selector
is accepted only by `ClearSession` or `ForkSession`, is never a path, and expires on server
restart; relist before retrying. `/clear` is accepted while idle, running, or awaiting
approval. It cancels the source run or durable approval, waits for that exact lifecycle to
deregister, then creates a distinct empty-history successor that inherits the source's exact
placement. Cancellation is irreversible: if placement, engine setup, or successor persistence
then fails, no successor is published and Mecatui does not rebind, but the source may already
be terminal-cancelled. Retry `/clear` after the local stream settles. Failures rejected during
preflight, including an invalid explicit worktree selector, leave an awaiting source unchanged.
Existing workspace/tool mutations are never rolled back. Mecatui keeps the source binding and
transcript visible but blocks source input and approvals during the handoff; only a correlated
successful response binds the successor. `/worktrees`, `/effort`, and inventory fork use the
history-carrying successor operation. Failed relist and fork operations leave the currently
selected session unchanged. Schedules resolve and store an exact
private placement at creation, while delegation derives placement from its parent; neither
models nor delegation/artifact handles can select a host path. For a configured
local status command, the optional local session-context service may resolve an
already-bound eligible local root asynchronously. Mecatui supplies that privileged
value to its local direct command as `Workspace.Path` in raw command JSON and as the
process CWD, and to status templates through their StatusML-escaped projection. It
refreshes it whenever the active session is replaced or switched, discards stale
responses, and otherwise uses the configured helper executable's cleaned absolute
parent directory (then its launch working directory if that cannot be determined);
the root is never exposed in command arguments, environment, ordinary UI state, or
universal Harness/HTTP/event/placement projections.

## mecatui session identity

The TUI header shows a compact short handle for the active session rather than a long
opaque ID. For a non-empty valid-UTF-8 ID, it renders safe `[A-Za-z0-9._-]` bytes
literally except that a leading `-` is encoded as `%2D`; every other UTF-8 byte is
uppercase `%HH`. It takes the longest prefix of complete literal or `%HH` atoms that fits
12 ASCII columns. The displayed literal has no leading `#` and can be passed unchanged to
`mecatui debug`; type `/session` to inspect the safely quoted full ID, title, state,
bounded placement label, known timestamps, provider, and model, then press `c` in that
overlay to copy the exact ID.
Use `/sessions` separately to Continue a stored chat or Inspect scheduled, child, and
unknown/other runs without changing the active chat. Its selected-row hints come from
server capabilities: `y` copies the exact ID, `v` views without attaching, `f` forks an
eligible main chat, `r` edits its title, and `d` confirms permanent deletion. Rename,
fork, and delete are revalidated at execution; failure leaves the active prompt target
unchanged, and the currently attached chat must be switched away before deletion.
To make that same inventory the
first interactive view, run `mecatui sessions [flags]` for the embedded server or
`mecatui connect ADDRESS sessions [flags]` for a remote server. These forms create no
throwaway session: `enter` continues or inspects, inspection `esc` goes Back, `n` creates
one new chat after model defaults are reconciled, and picker `esc` quits with no final
session handoff. Seed-prompt and resume flags conflict with this explicit browser intent.
To continue directly at process startup, pass `--resume SESSION_ID` or
`--resume-latest` in either embedded or `connect` mode. mecatui adopts the complete
authoritative transcript without creating a throwaway session; latest excludes active,
awaiting, scheduled, child, unknown, and transcript-unavailable rows. When no eligible
chat exists, `--resume-latest` starts a fresh chat instead of failing (a genuine
inventory-list failure still surfaces). The first new
prompt still enters the normal atomic run funnel. If attachment fails, the transcript
stays visible and the preserved prompt can be retried with `r` or returned to with
`esc`; no fallback chat is created. A `--prompt`/`--prompt-file` seed is submitted only
after adoption. While the TUI is open, `/session` + `c` copies the exact ID. On an
ordinary clean exit, mecatui leaves the alternate screen and then writes one stderr line
of the stable form `mecatui: final-session-id=<JSON string>`; JSON-decoding the value
recovers the byte-exact final active ID after any rebind. Keep it to launch
`mecatui --resume SESSION_ID` later. No handoff is claimed when setup fails, no session
exists, the TUI fails, or a signal interrupts/forces exit; stdout is unchanged. See the
[full TUI reference](tui.md#continue-a-chat-at-startup).

## Debug a stored session

Use `mecatui debug TARGET` against the embedded store, or
`mecatui connect ADDRESS debug TARGET` against a running server. `TARGET` may be an exact full
opaque ID—including the exact final ID printed when mecatui exits—or the displayed 12-column short
handle. Safe `[A-Za-z0-9._-]` bytes are literal except that a leading `-` is encoded as `%2D`;
other bytes are uppercase `%HH` atoms. The handle has no leading `#`; pass that displayed literal
unchanged. A syntactically valid short target consults the complete caller-visible inventory.
Exact full-ID equality wins; otherwise one unique projected match resolves. On ambiguity, open
`/session`, copy the exact full ID, and pass it as `TARGET` through the same command. If inventory
fails or no projection matches, mecatui sends `TARGET` unchanged and reports the ordinary server
exact-ID authorization/not-found result.

The command creates a **separate durable
debug session** and submits one first user turn containing the sanitized current debugger
client/server diagnostics baseline plus a request to inspect the bound target's status and
authoritative transcript. A custom `--prompt` replaces that diagnosis request, not the
baseline. Remote baseline lookup failures are safely classified and do not block diagnosis.
The invocation itself is consent:
mecatui prints a privacy warning because stored prompts, outputs, tool arguments/results,
paths, and secrets may be sent to the selected model.

The debug engine has an empty workspace, the no-filesystem profile, and the
read-only `InspectSession` tool. Add repeatable `--debug-mcp NAME` flags to expose direct
tools from only those already-configured server-global streaming-HTTP MCP servers:

```sh
mecatui debug SESSION_ID --debug-mcp github
mecatui connect ADDRESS debug SESSION_ID --debug-mcp github --debug-mcp slack
```

Unknown, disconnected, duplicate, or tool-empty selections fail creation. No inline/client
MCP configuration, URL, header, stdio transport, resource tool, or query meta-tool is accepted.
The selected server names and exact initial tool-name ceiling are persisted; restart requires
every selected server/tool still to exist and never adds newly advertised tools. Read-only
annotations are honored, while absent/false means mutating. Every mutating call requires a
fresh interactive approval even under configured/yolo/learned Allow; Deny remains absolute,
headless use is denied, and Allow Always executes only the current call without learning.
The model must draft an outward action first and may call a mutating reporting tool only after
a later genuine current operator request explicitly asks to publish/send it.

InspectSession's status and transcript views come from the target
snapshot; activity and performance are optional EventLog projections. `related` returns only
opaque target-bound handles for inspectable same-owner retained descendants; those handles can
scope every ordinary view without accepting raw session IDs. `delegation` reports typed
subagent/parallel/team/schedule evidence and parent result linkage. `history` catalogs the
current snapshot, retained compaction archives, and retained-event reconstruction with separate
opaque handles, and `manifest` lists content-free request manifests. Each reports scan,
projection, and retention gaps explicitly; a pruned, inaccessible, absent, never-produced, or
not-retained child is labelled only when the available lineage/event evidence proves it.
Snapshot latest-run counters and cumulative usage are named separately from lifetime EventLog
aggregates. The `network` view
shows bounded failed/interesting resilience attempts with retry/terminal decisions, elapsed
and safe failure classes, validated provider statuses, and closed correlation kinds with fixed
SHA-256 digests. It never exposes raw provider codes, correlation IDs, errors, URLs, headers, bodies, prompts,
tool arguments, or credentials, and states that successful-attempt and DNS/TCP/TLS phase
timing are unavailable.
Evidence is bounded and fenced as hostile data. The target ID is fixed by the server,
not supplied by the model, and the debug run never resumes, mutates, approves, cancels,
steers, or leases the target. The normal padded header places amber/bold
`DEBUG target <handle>` immediately after `mecatui` and keeps that complete identity when
less important model/mode/server details are shed. `/session` shows the safely quoted exact
target ID and copies it with `t`; the target-derived terminal title uses the same handle.
Model/mode/session-changing affordances are disabled. See
[ADR 0254](adr/0254-session-debugger-admin-transport.md).

## Scheduled tasks

`mecated` and `mecak8s` run scheduled agent fires autonomously (issue #189,
[ADR 0059](adr/0059-scheduled-tasks.md)). The scheduler is **ON by default** whenever the configured store exposes a
`ScheduleStore` (jsonlstore via `--store-dir` on `mecated`; redisstore via
`--redis-url` on `mecak8s`) — no flag needed ([ADR
0073](adr/0073-schedule-tool.md)); a store with no `ScheduleStore` (the
in-memory default) never ticks:

```sh
mecated serve --store-dir ./state --scheduler-tick-interval 30s   # scheduler ticks by default
mecak8s --redis-url redis.example:6379 --redis-tls          # multi-replica, ticks by default
mecated serve --store-dir ./state --no-scheduler                  # opt out (manual management still works)
```

The mecak8s Helm chart offers three secure real-provider transport postures — in-pod
TLS, operator-attested edge-terminated TLS, and the explicit unsafe bypass. Picking one
is [the mecak8s guide's](usage/mecak8s.md) job, not this page's.

Flags:

| Flag | Default | Description |
|---|---|---|
| `--no-scheduler` | false | Disable the in-process scheduler tick loop (ON by default on any schedule-capable store). The create/list/fire API and the in-chat `Schedule` tool still work — manual management is independent of the tick loop. The removed `--scheduler` opt-in fails fast as an unknown flag (clean removal, no deprecated alias — see ADR 0073). |
| `--scheduler-tick-interval` | 30s | How often the tick loop polls `ScheduleStore.Due`. |
| `--scheduler-min-interval` | 1m | The frequency floor enforced at schedule-create time — by BOTH the in-chat `Schedule` tool and the REST/gRPC handler (a schedule whose cadence is tighter than this is rejected, fail-closed). Defaults to 1m so an on-by-default scheduler + the floor-Allow `Schedule` tool cannot mint an unbounded tight-cadence recurring fire out of the box; set it explicitly to tighten, or to 0 to disable the floor. |
| `--scheduler-max-concurrent-fires` | 4 | Bounds the per-tick fire fan-out. |
| `--schedule-fire-retention` | 7d (168h) | How long persisted `sched--`-prefixed fire-session snapshots are retained before the GC sweep deletes them (a distinct family from `--child-retention`/`--main-retention`); a LIVE fire (one mid-run) is never deleted. The 7d default applies whenever the flag is unset (the scheduler is on by default); an explicit 0 disables the pass — fire sessions are never swept. Only meaningful with a durable store configured. |
| `--schedule-fire-retention-max-total` | 0 (off) | Max persisted `sched--`-prefixed fire-session snapshots kept store-wide; the oldest beyond the cap are deleted, skipping in-flight fires. The symmetric peer of `--main-retention-max-total`: the age horizon (`--schedule-fire-retention`) bounds the tail, this cap bounds the head (a per-minute cron accumulates ~10k sessions/week the horizon never trims). Durable-store-only. |

Schedules are managed via three surviving surfaces: the in-chat **`Schedule`
tool** (the model-facing catalog affordance — create/list/inspect/pause/resume/
delete/fire, ADR 0073), the **`ScheduleService`** gRPC + REST API (Phase 2a,
issue #232), and the **`mecatui /schedule` overlay** (Phase 3a, issue #234 —
list/inspect/pause/resume/fire-now/delete from the TUI; see `docs/tui.md`). The
operator-tier `settings.yaml` `schedules:` block and the `mecated schedules`
CLI were removed by ADR 0073 (the in-chat tool + the retained API + the OS
scheduler cover the use cases). An in-overlay Create form + NL→cron is planned
for a later phase.

**gRPC** (`mecatl.v1.ScheduleService`): `CreateSchedule`, `GetSchedule`,
`ListSchedules`, `UpdateSchedule`, `DeleteSchedule` (idempotent), `FireNow`,
`PauseSchedule`, `ResumeSchedule`, `GetFire`, `ListFires`.

**REST** (under `/v1/schedules`):

| Method | Route | RPC |
|---|---|---|
| POST | `/v1/schedules` | CreateSchedule |
| GET | `/v1/schedules` | ListSchedules |
| GET | `/v1/schedules/{name}` | GetSchedule |
| PUT | `/v1/schedules/{name}` | UpdateSchedule |
| DELETE | `/v1/schedules/{name}` | DeleteSchedule |
| POST | `/v1/schedules/{name}/fire` | FireNow |
| POST | `/v1/schedules/{name}/pause` | PauseSchedule |
| POST | `/v1/schedules/{name}/resume` | ResumeSchedule |
| GET | `/v1/schedules/{name}/fires` | ListFires |
| GET | `/v1/schedules/{name}/fires/{id}` | GetFire |

Create a cron schedule + force an immediate fire (REST):

```sh
# Create a cron schedule (read-leaning → plan mode).
curl -X POST http://localhost:8080/v1/schedules \
  -H 'content-type: application/json' \
  -d '{"name":"nightly-report","prompt":"summarize commits from today",
       "workspace":"/repo","mode":"PERMISSION_MODE_PLAN",
       "trigger":{"cron":"0 9 * * *"}}'

# Fire it immediately. FireNow is SYNCHRONOUS-TO-TERMINAL: it blocks until
# the fire's run completes (bounded by the schedule's turn/token limits, default
# 50 turns), then returns the fire_id + session_id. A client/proxy timeout
# (gRPC deadline, HTTP proxy_read_timeout) shorter than the run will truncate
# the response — set a generous client deadline. The fire keeps running
# server-side after the client disconnects; poll GetFire for the persisted record.
curl -X POST http://localhost:8080/v1/schedules/nightly-report/fire
# -> {"fire_id":"sched--...","session_id":"sched--..."}

# Retrieve the persisted fire record (stop reason + session id) after the run.
curl http://localhost:8080/v1/schedules/nightly-report/fires/<fire_id>
```

A deployment whose store does NOT expose a `ScheduleStore` (memstore, or a store
without the accessor) honestly reports the schedule RPCs as `Unimplemented`
(gRPC) / 501 (HTTP). `FireNow` on a paused/done schedule is `FailedPrecondition`
/ 412.

A scheduled fire mints a fresh `sched--` top-level session per fire with
subagent-grade defaults (bounded turn/token budgets, read-leaning posture unless
`mutating: true` is set on the schedule, headless ask model). The fire id IS the
session id (a `sched--<name>-<ts>-<rand>` id, passed as a `WithSessionID`
override on session create), so a fire's persisted session carries the `sched--`
GC-retention family prefix swept by `--schedule-fire-retention`. The at-most-once
firing semantics mean a crash mid-fire skips the slot — a recurring schedule
self-heals via the fire-once-now misfire policy; a one-shot can be lost.

### Schedule-spec field notes

The schedule spec fields below apply however the schedule is created (the
in-chat `Schedule` tool, the REST/gRPC API):

- A declaration with neither `cron` nor `oneShot`, or with both, is rejected by
  the create-seam (fail-closed).
- **Placement is resolved at schedule creation.** The schedule inherits its source's
  exact private `EnvironmentRef` or composition resolves the deployment default/no-FS
  choice immediately. It persists that ref plus durable owner and trusted placement scope,
  never a worktree selector or “follow current default” instruction. Every fire
  reauthorizes and exactly reattaches before creating the fire session; drift records a
  failure without filesystem access.
- **`singleton` currently always effectively resolves to `true`.** The create-seam
  coerces `singleton: false` to `true` (overlap suppression) — a `false` value is
  accepted but silently overridden. Full opt-out support (allowing overlapping
  fires) needs an engine-port/proto change and is deferred.
- **Phase 2c opt-in fields (issue #236)**, both defaulting OFF (the pre-Phase-2
  path is byte-identical when neither is set):
  - **`oneShotRetry`** (bool, default `false`) — re-arm a one-shot that crashed
    mid-fire (prior fire ended in `StopError`, or `LastFireSessionID` is still the
    `pending` sentinel — Claim happened but RecordFire did not) up to
    `oneShotMaxRetries` times, via the optional `ScheduleOneShotReArmer` store
    interface. A store that does not implement it degrades to at-most-once (the
    one-shot stays lost). **One-shot-ONLY:** the create-seam rejects
    `oneShotRetry: true` on a cron trigger fail-closed (a cron self-heals via the
    misfire policy already). A re-armed one-shot starts FRESH — it does NOT carry
    context on the retry (the crashed fire's context is untrusted AND incomplete).
  - **`oneShotMaxRetries`** (int, default `0` = off; the create-seam applies a
    default of `3` when `oneShotRetry: true` and this is `0`) — bounds the re-arm
    budget. The durable `oneShotRetryCount` on the schedule state is incremented
    on each re-arm; once it reaches `oneShotMaxRetries` the one-shot stays
    disabled (permanently done — no crash-loop). Must be `>= 0`.
  - **`carryContext`** (bool, default `false`) — render the prior fire's
    conversation as a FENCED UNTRUSTED preamble prepended to the prompt (NOT as
    seeded history). Carried context is UNTRUSTED (model-authored +
    tool-result-laden; a prior fire may have been prompt-injected), so it must not
    become live instructions; the canonical governance fence
    (`governance.FenceUntrusted` + `NeutraliseFraming`) quarantines it so a forged `<<<UNTRUSTED` marker or
    harness section header in the prior content cannot break out of its block.
    Allowed on either trigger. On prior-session-load failure (not found, decode
    error) the fire degrades to fresh-context (WARN, never fails the fire). A
    re-armed one-shot does NOT carry context on the retry (the gate short-circuits
    on the `pending` sentinel).

## OpenAI Codex subscription (experimental)

`openai-codex` lets a local `mecated`, embedded `mecatui`, or `mecatequi`
use models entitled to a ChatGPT Codex subscription. This is a distinct billing
identity from public API-key OpenAI: a subscription is not API credit, the token
never enables provider `openai`, and `OPENAI_API_KEY` never enables
`openai-codex`.

The first release is deliberately manual. Put an access-token snapshot under
`providers.openai-codex.oauth` in owner-only `auth.yaml`, start with
`--default-provider openai-codex` or send an explicit
`provider_id: openai-codex` + `model_id` session selector, and restart after
replacing the token. There is no login, refresh, import, keyring, or write path.
The backend is undocumented and experimental, not a supported third-party API
contract. See the [exact schema, precedence, lifecycle, and plaintext same-UID
boundary](usage/mecated.md#openai-codex-subscription-manual-token-experimental)
and the [failure table](usage/troubleshooting.md#openai-codex-manual-token-and-entitlement-failures).

`mecak8s` accepts `auth.yaml` API-key entries but intentionally rejects the local
`providers.openai-codex.oauth` entry; Kubernetes delivery for subscription OAuth
requires a separate Secret or external-secret design.

## Standalone MCP broker on Kubernetes

The production remote broker is `cmd/mecabroker`, packaged separately from `mecak8s`:

```sh
task build                    # includes bin/mecabroker
task ko:build:broker          # local image, no push
helm template broker deploy/helm/mecabroker \
  -f deploy/helm/mecabroker/ci/production-values.yaml
```

Treat the fixture values as shape documentation and replace every `.invalid`, Secret,
and TEST-NET value. The chart intentionally deploys exactly one `Recreate` replica with
no PDB, autoscaling, or outer-broker Redis. A restart interrupts active attachments and
outer OAuth callback correlation; this is not an HA or exactly-once deployment.

If a tool reports that its outcome is unknown after a broker response was lost, treat the
operation as possibly completed. Do not automatically repeat a mutation. First inspect provider
state through a known-safe status/read operation when one exists. If that cannot establish the
outcome, report the uncertainty and obtain an explicit recovery decision. Mecatl supplies this
procedure in the broker-enabled model prompt, but does not enforce it as a reconciliation or
approval gate and cannot prevent duplicate external effects from a later new invocation.

The Service publishes only TLS gRPC and browser callback ports. Health, readiness, and
pre-stop drain use a loopback-only admin listener through fixed self-probe commands in the
shell-less image. Readiness validates the finite TLS/OIDC/profile/ToolHive/discovery/static-
route prerequisites without logging in a user or executing a tool. Drain rejects new gRPC
and callbacks before the 2s propagation wait, gives active work a finite 55s deadline, and
then cancels remaining work before teardown; the chart reserves 70s total.

Set `networkPolicy.publicFrom` to the single union of exact namespace, pod, and CIDR peers that may reach the multiplexed public listener. The same NetworkPolicy port carries gRPC and browser callbacks; vanilla NetworkPolicy cannot provide route-level separation, so `mecak8sFrom` and `browserCallbackFrom` are not valid settings.


## ToolHive LLM gateway

If you already run [ToolHive](https://github.com/stacklok/toolhive)'s LLM gateway
proxy, mecatl auto-detects it — **no API key, no flag, no config edit**. This is
UNRELATED to `--toolhive` (ToolHive MCP workload discovery, documented elsewhere) —
the two features share a vendor name and nothing else.

### Walkthrough

```sh
# 1. Set up the gateway credential once (per ToolHive's own docs).
thv llm setup

# 2. Start the local proxy (listens on 127.0.0.1:14000 by default).
thv llm proxy start

# 3. Run mecatui (or mecated) as usual — no --openai/--anthropic/--model needed.
go run ./cmd/mecatui

# 4. Open the model picker and confirm the gateway's models are listed.
#    (inside mecatui) /models
```

If step 4 shows your gateway's models with a `●` marker on the auto-selected
default, you're done — every session now talks to your organization's gateway
over the OpenAI-compatible protocol, with the header showing a persistent
"via ToolHive gateway" segment as a reminder.

### When you have an API key set (the disclosure surface)

If you also have an OpenAI/Anthropic/OpenRouter API key configured, the gateway
is detected but is NOT your default (the precedence ladder ranks a key-driven
provider above the intent-driven gateway). mecatl surfaces that the gateway is
*available* through five disclosure affordances — none of which reroute your
session:

- **Idle footer notice.** Once per process, when the gateway is detected-and-
  reachable but not your default, the idle footer-left shows a muted, dismissable
  notice:
  `<provider-id> gateway available (N models, no API key needed) — /models to use it, or --default-provider <provider-id>`
  It fires only at idle (never during a run/approval/connect). Dismiss it with any
  keypress, or by opening `/models`; either dismisses it for the rest of the
  process (it won't re-fire).
- **Header "available" segment.** While the gateway is detected-and-reachable but
  NOT your active default, the header carries a muted
  `<provider-id> gateway available` segment (the sibling of the `via ToolHive
  gateway` segment that shows when the gateway IS your default). The two are
  mutually exclusive — only one renders at a time — and the segment is
  vendor-neutral (it reads the provider id from the live status, not a hardcoded
  `toolhive`). Neither renders when no gateway is present, so a key-only setup is
  unchanged.
- **Welcome splash line.** On the first-run splash (before you've typed a prompt,
  once the gateway is known-reachable after connect), a muted
  `<provider-id> gateway detected (no API key needed) — /models` line surfaces the
  no-API-key alternative at the moment you're most attentive. Suppressed when the
  gateway is your default or absent.
- **Picker "org" tag.** In the `/models` picker, every model row served by the
  gateway carries an `org` segment beside the capability tokens (matching the
  existing `img`/`reason` glyph style) — so you can tell at a glance which rows
  are gateway-served (no key, no per-token spend) vs. key-driven.
- **Provenance hint.** The picker's `current:` provenance line appends, when the
  gateway is available-but-not-default and your session runs on a key-driven
  default:
  ` · toolhive gateway also available — outranked by your openai key`
  (the gateway id and your default-provider id are read from the live status, so
  the hint is vendor-neutral and accurate for any provider pair.)

### Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `--toolhive-llm` | `true` | Auto-detect a locally-running ToolHive LLM proxy by reading ToolHive's own config file and probing `127.0.0.1`; registers it as provider id `toolhive`. Pass `=false` on a shared host where you don't want this. |
| `--toolhive-llm-base-url` | `""` | An EXPLICIT proxy base URL (must resolve to loopback — `127.0.0.0/8`, `[::1]`, or `localhost`). Skips the config-file auto-detect entirely but keeps the startup probe. No environment-variable twin (this is a deliberate, visible flag). ALWAYS forces proxy mode — direct mode derives its base URL from the config's `gateway_url`, so this override and `--toolhive-llm-mode direct` are contradictory (Build rejects the combination). |
| `--toolhive-llm-mode` | `auto` | How the `toolhive` provider routes to the gateway. `auto` (default): talk to the real `gateway_url` DIRECTLY when the OIDC trio (`gateway_url` + `issuer` + `client_id`) is configured AND `gateway_url` is HTTPS, else fall back to the loopback proxy (today's behaviour). `proxy`: force the loopback reverse proxy. `direct`: force direct-to-gateway (Build-fails if OIDC is not configured). See [Direct mode](#direct-mode) below. |

Registration happens on **detected intent alone** — the proxy does not need to be
running yet, and a later restart with the proxy temporarily down never bricks the
session: mecatl always knows about the "toolhive" provider once ToolHive's config
says it should exist, and a down proxy just means a request-time connection error
(exactly like any other transient provider outage) instead of a rejected session.

### Direct mode

In addition to the loopback proxy path, the `toolhive` provider can talk DIRECTLY to
the real `gateway_url` with no local proxy hop. mecatl imports ToolHive as a Go
library and builds the OIDC token source in-process (the same `llm.NewTokenSource`
`thv llm token` uses), so the bearer is minted and refreshed without spawning a
subprocess and without `thv llm proxy` running. See
[ADR 0102](adr/0102-toolhive-direct-mode.md) for the full design.

`--toolhive-llm-mode` selects the routing:

| Mode | When it's used | Base URL |
| --- | --- | --- |
| `auto` (default) | Direct when the OIDC trio (`gateway_url` + `issuer` + `client_id`) is configured AND `gateway_url` is HTTPS; otherwise the loopback proxy (today's behaviour). | `gateway_url + "/v1"` when direct, `http://127.0.0.1:<port>/v1` when proxy. |
| `proxy` | Always the loopback reverse proxy. The escape hatch for a misconfigured OIDC block or a self-signed gateway cert. | `http://127.0.0.1:<port>/v1` (or an explicit `--toolhive-llm-base-url`). |
| `direct` | Always direct-to-gateway. Build fails fast if OIDC is not configured, naming the missing fields and the remediation — never a silent fallback to proxy. | `gateway_url + "/v1"`. |

The HTTPS gate is a security invariant, not a preference: a non-HTTPS `gateway_url`
would send the OIDC bearer token over cleartext (CWE-319). The `auto` and `direct`
arms fall back to proxy (or Build-fail, respectively) when `gateway_url` is not
`https://` — except the `http://localhost` / `http://127.0.0.1` dev carve-out.

#### Logging in (direct mode)

Direct mode needs a cached OIDC credential. Get one with either of:

```sh
# Option A: mecatui runs the interactive OIDC browser flow in-process (no separate thv binary).
mecatui llm login
# Headless / SSH / CI: print the authorization URL instead of opening a browser.
mecatui llm login --skip-browser

# Option B: the ToolHive CLI itself.
thv llm setup
```

Both write a refresh-token REFERENCE (never the token value) to ToolHive's own config,
so a subsequent non-interactive direct-mode session reuses the credential without
re-login. `mecatui llm login` is a CLI-only operation — it does NOT start a session or
connect to a server.

#### Headless remediation

`mecated` is headless by definition, so a direct-mode cache-miss (no cached
credential) surfaces a terminal error rather than launching a browser:

```
no cached ToolHive LLM gateway credential — run `thv llm setup` (or `mecatui llm login`) to log in, or use `--toolhive-llm-mode proxy`
```

The error names all three remediations. Pick whichever fits the deployment:
`thv llm setup` / `mecatui llm login` to obtain a credential, or
`--toolhive-llm-mode proxy` to fall back to the loopback proxy (which holds the
credential itself).

#### Self-signed gateway certificates (use proxy mode)

Direct mode does NOT honor `tls_skip_verify` from the ToolHive config — this is an
upstream ToolHive gap (the OIDC-discovery client and the token source have no
`InsecureSkipVerify` plumbing), not a mecatl choice. If your gateway uses a
self-signed certificate, use `--toolhive-llm-mode proxy`, which DOES honor
`tls_skip_verify`. The limitation surfaces as an opaque TLS error at the first
request, never a silent failure; a future ToolHive bump that closes the gap removes
it with a one-line code change.

### Making the gateway your default persistently

By default a key-driven provider (OpenAI/Anthropic/OpenRouter) outranks the
intent-driven gateway on the precedence ladder, so the gateway is available but
not your default. To make `toolhive` the default **without unsetting your API
key**, set `models.default_provider` in your operator-tier `settings.yaml`:

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier — NOT a project file)
models:
  default_provider: toolhive
```

This is the `--default-provider` flag's YAML twin: a CLI `--default-provider
toolhive` OUT-RANKS the YAML value (CLI-wins fold), and either feeds the UNCHANGED
precedence ladder as an explicit operator override — it does not lower the
precedence of key-driven providers, it declares "I want toolhive to be the default
despite my key." It is validated fail-fast at startup: an unknown or unavailable
provider refuses to boot. Operator-tier only (a project-tier `default_provider:`
is ignored with a WARN). See the
[configuration reference](configuration-reference.md#models) for the full entry.

### Troubleshooting

`/models` renders the current state for the `toolhive` provider whenever it isn't
healthy:

| State | What you see | Remediation |
| --- | --- | --- |
| `unreachable` | `` toolhive: proxy not reachable — start it with `thv llm proxy start` `` | The proxy isn't running (or its port changed). Start it. |
| `unauthorized` | `` toolhive: gateway rejected the credential — re-auth with `thv llm setup` `` | Your gateway credential expired or was revoked. Re-run setup. |
| `empty` | "your gateway credential lists no models — ask your platform admin or re-run `thv llm setup`" (replaces the generic empty-picker note) | The proxy is reachable and your credential is valid, but it advertises zero models — this is an ORGANIZATIONAL problem (ask your platform admin), not a local one. |
| available, not default | idle footer notice "`<provider-id>` gateway available (N models, no API key needed) — /models to use it, or `--default-provider <provider-id>`"; header carries a `<provider-id> gateway available` segment; welcome splash carries a `<provider-id> gateway detected (no API key needed) — /models` line; picker rows carry an `org` tag; provenance line names the outranking key | This is expected when a key-driven provider outranks the gateway. To make the gateway your default without unsetting the key, set `models.default_provider: toolhive` in settings.yaml (or pass `--default-provider toolhive`). |

**Gateway lists models, but requests fail (`provider_unresolved` / `cost_enforcement_failure`, HTTP 5xx).** The `/models` state above is driven by the gateway's model *listing* — so a gateway that lists fine reports healthy, yet an individual model may still be rejected at request time. This happens when a model alias isn't cost-routed on the gateway: some gateways (e.g. Envoy AI Gateway) expose "friendly" aliases (`claude-sonnet-5`, `gemini-2.5-flash`) that resolve to a billing pool with no concrete upstream provider, so the request 5xxs with a cost-enforcement error before it ever reaches a model. mecatl auto-selects the *first* model the gateway lists when you set no `--model`/`--default-model`, which may be one of these aliases. Fix it from your side — pick a fully-qualified or provider-namespaced slug (`us.anthropic.claude-sonnet-5`, `gpt-5`, `z-ai/glm-5.2`) via `/models`, `--model`, or the mecatui global default (`ctrl+g`) — or ask your platform admin to add a cost route for the alias. Because the error is a 5xx, mecatl retries it and (after enough consecutive failures) opens the circuit breaker; switching to a working slug and letting the ~30s cooldown lapse clears it.

The proxy comes back up? The very next `/models` open (or the background live
refresh) picks it up automatically, AND — if the gateway was the sole provider and
had no default model yet — the very next new session (or a resume after a restart)
also picks up the healed default automatically. No mecatl restart needed. A session
already created BEFORE the proxy came back up **keeps failing every turn**, even
after the proxy is up — its model never resolved to a real id, so every request
it sends is rejected by the gateway; it does not self-heal mid-session. Open a
fresh session (or restart mecated/mecatui) to get a session that actually works.

Diagnostics for this feature ride mecatui's usual log file
(`$XDG_STATE_HOME/mecatl/mecatui.log`, fallback `~/.local/state/mecatl/mecatui.log`);
mecated logs to stderr/journald as usual.

**Shared-host recommendation:** if you run `mecated`/`mecatui` on a host other
operators also use, pass `--toolhive-llm=false` — a per-user ToolHive config
detected by one operator's process should not surprise another.

## Plan approval

In **plan mode** (`--mode plan` / `session/set_mode PLAN`), once the model has
presented a complete plan in its assistant text it calls the `PresentPlan`
signalling tool. That parks the run `awaiting` on a **plan-approval** ask — a
distinct gate from an ordinary permission ask, though it reuses the same
permission-ask machinery ([ADR 0069](adr/0069-plan-approval-gate.md)). The
operator then approves (→ execute) or iterates (→ re-plan).

**mecatui:** a `PresentPlan` ask renders a distinct "Plan ready for review"
modal (the tool name is the discriminator — no proto provenance field). Three
buttons: **`[A]pprove & run`** (→ `default` mode, deny→ask→allow execution),
**`[W] auto-accept edits`** (→ `acceptEdits` mode, auto-accept every edit in the
execution phase), **`[D] iterate`** (stay in plan mode; the model revises and
re-presents). The footer labels a `plan_approved` stop as the approval terminal.

**gRPC:** the `ApprovePlan(ApprovePlanRequest) → stream Event` RPC
([§9](usage/grpc-api.md)) resolves a parked plan-ask atomically: on an ALLOW
verdict it resumes the parked run AND starts a FRESH continuation run carrying
the proceed message, streaming BOTH runs' events on the one response stream.
`target_mode` (`DEFAULT` / `ACCEPT_EDITS` / `PLAN`) selects the verdict and the
resulting posture (`PLAN`/`UNSPECIFIED` → deny/iterate, no continuation).

**HTTP:** `POST /v1/sessions/{id}/plan:approve` with a
`{"target_mode":"default"|"accept_edits"|"plan","note":"..."}` body — see
[§10](usage/http-sse-api.md#approve-a-presented-plan-planapprove). `409` on a
precondition failure (live run / not awaiting / not a plan ask).

**ACP:** the ACP adapter has NO bespoke `ApprovePlan` method — it composes the
flow from the two EXISTING primitives the editor speaks natively:
`session/set_mode` (the operator picks default / accept-edits / plan) +
`session/prompt` (the proceed message). A presented-plan `permission.ask` over
ACP is resolved by the editor's existing `session/request_permission` reply;
the subsequent mode flip + continuation prompt are ordinary `session/set_mode`
+ `session/prompt` calls.

**Headless / autonomous:** by DEFAULT a headless plan ask is auto-denied (the
model iterates; no silent mode flip). The opt-in **`--plan-mode-auto-approve`**
flag (DEFAULT OFF, OPERATOR-TIER ONLY) auto-approves a parked plan ask via
`ApprovePlan(ModeDefault)` when the deployment is headless — a deliberate
autonomous-approval capability, NEVER load-bearing for safety. It does NOT fire
when interactive, NOT in non-plan modes, NOT for non-plan asks. A LOUD startup
diagnostic (`plan_mode_auto_approve: ON (NO HUMAN REVIEW)`) + a per-approval
`WARN` name the session. The YAML twin is the user-global `settings.yaml`
`plan-mode-auto-approve:` key; a project-tier block is WARN-ignored.
