# mecatl — Usage & Operator Guide

`mecatl` is a headless, agentic coding harness. It owns its own context
window, tool loop, permission policy and lifecycle hooks, and talks to OpenAI
(or any OpenAI-compatible `/v1/responses` endpoint), OpenRouter, or Anthropic
(the native Messages API). The server, `mecated`, exposes one agent run over
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
| 18. ToolHive LLM gateway | [§ below](#toolhive-llm-gateway) |

## Scheduled tasks

`mecated` and `mecak8s` can run scheduled agent fires autonomously (issue #189,
[ADR 0059](adr/0059-scheduled-tasks.md)) behind `--scheduler`:

```sh
mecated --store-dir ./state --scheduler --scheduler-tick-interval 30s
mecak8s --redis-url redis://... --scheduler   # multi-replica
```

Flags:

| Flag | Default | Description |
|---|---|---|
| `--scheduler` | false | Enable the in-process scheduler tick loop. Requires a store that exposes a `ScheduleStore` (jsonlstore via `--store-dir`, or redisstore via `--redis-url`). |
| `--scheduler-tick-interval` | 30s | How often the tick loop polls `ScheduleStore.Due`. |
| `--scheduler-min-interval` | 0 (off) | The frequency floor enforced at schedule-save time (a schedule tighter than this is rejected). |
| `--scheduler-max-concurrent-fires` | 4 | Bounds the per-tick fire fan-out. |
| `--schedule-fire-retention` | 7d (when `--scheduler` on) | How long persisted `sched--`-prefixed fire-session snapshots are retained before the GC sweep deletes them (a distinct family from `--child-retention`/`--main-retention`); a LIVE fire (one mid-run) is never deleted. 0 disables the pass — fire sessions are never swept. Only meaningful when `--scheduler` is enabled and a durable store is configured. |
| `--schedule-fire-retention-max-total` | 0 (off) | Max persisted `sched--`-prefixed fire-session snapshots kept store-wide; the oldest beyond the cap are deleted, skipping in-flight fires. The symmetric peer of `--main-retention-max-total`: the age horizon (`--schedule-fire-retention`) bounds the tail, this cap bounds the head (a per-minute cron accumulates ~10k sessions/week the horizon never trims). Durable-store-only. |

Schedules are managed via the **`ScheduleService`** gRPC + REST API (Phase 2a,
issue #232), the operator-tier **`settings.yaml` `schedules:` block** (Phase 2b,
issue #233 — declarative reconcile into the store), the **`mecated schedules`
CLI** (Phase 2b), and the **`mecatui /schedule` overlay** (Phase 3a, issue #234
— list/inspect/pause/resume/fire-now/delete from the TUI; see `docs/tui.md`).
An in-overlay Create form + NL→cron is planned for a later phase.

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

### Declarative schedules (`settings.yaml`, Phase 2b)

An operator can declare schedules in the **operator-tier** `settings.yaml` (the
user-global file or a CLI-supplied config) instead of creating them one-by-one over
the API. On startup `mecated` parses the `schedules:` block and **reconciles** it
into the durable `ScheduleStore` (idempotent upsert — create missing, update
differing, leave unchanged alone):

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier — NOT a project file)
schedules:
  - name: nightly-review
    cron: "0 9 * * *"          # 5-field cron or @-macro; mutually exclusive with oneShot
    timezone: "America/New_York" # IANA name; empty = UTC
    prompt: "Summarize today's commits and open a follow-up if any test broke."
    workspace: "/repo"
    mode: plan                  # a non-mutating schedule MUST run in plan mode
    # mutating: true           # opt into write tools (then mode may be default/acceptEdits)
    maxTurns: 20                # per-fire turn budget (0 = disabled)
    maxToolCalls: 40            # per-fire tool-call budget (0 = disabled)
    maxFires: 0                 # total fires for a cron (0 = forever); ignored for one-shot
    singleton: true             # skip the next fire if a prior one is still running (currently always effectively true — see notes)
    # misfire: skip             # "" (default = fire-once-now) or "skip"
    carryContext: true           # Phase 2c: render the prior fire's conversation as a fenced untrusted preamble (see notes)

  - name: one-shot-patch
    oneShot: "2026-07-04T10:00:00Z"  # RFC3339 instant; must be in the future
    prompt: "Apply the pending security patch."
    mutating: true
    provider: anthropic
    model: claude-sonnet-4-5
    oneShotRetry: true          # Phase 2c: re-arm on a mid-fire crash (cron triggers reject this)
    oneShotMaxRetries: 3        # Phase 2c: re-arm budget (default 3 when oneShotRetry=true and this is 0)
```

Notes:

- The `schedules:` key is a **YAML sequence** (no `items:` wrapper). Each element is
  decoded **strictly** — an unknown key inside one declaration is a parse error, so a
  typo can't silently disable a schedule.
- **Operator-tier only.** A **project-tier** `.mecatl/settings.yaml` `schedules:`
  block is **ignored with a WARN** — a project repo cannot register schedules (same
  security-downgrade fold as guardrails/posture). Set `schedules:` in your
  user-global `settings.yaml` or pass it via `--config`.
- **No destructive reconcile.** Removing a schedule from the YAML does NOT delete it
  from the store — an operator must delete it explicitly via the API/CLI. Re-running
  `mecated` only creates/updates; it never deletes.
- A declaration with neither `cron` nor `oneShot`, or with both, is rejected by the
  create-seam at reconcile time (WARN'd + skipped, not fatal — one bad schedule does
  not drop the rest).
- **`workspace` is required** for a default-profile schedule (a fire mints a real
  filesystem session), and must be OMITTED for a `no-fs`-profile schedule. The
  create-seam validates this up front, so an empty-workspace default schedule is
  rejected at create/reconcile time rather than failing later at fire time.
- **`singleton` currently always effectively resolves to `true`.** The create-seam
  coerces `singleton: false` to `true` (overlap suppression) — a `false` value is
  accepted but silently overridden, so a fold-time WARN names the schedule. Full
  opt-out support (allowing overlapping fires) needs an engine-port/proto change and
  is deferred.
- **Phase 2c fields (issue #236)** — two opt-in schedule-spec fields, both
  defaulting OFF (the pre-Phase-2 path is byte-identical when neither is set):
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
    become live instructions; the fence (`agent.FenceUntrusted` +
    `NeutraliseFraming`) quarantines it so a forged `<<<UNTRUSTED` marker or
    harness section header in the prior content cannot break out of its block.
    Allowed on either trigger. On prior-session-load failure (not found, decode
    error) the fire degrades to fresh-context (WARN, never fails the fire). A
    re-armed one-shot does NOT carry context on the retry (the gate short-circuits
    on the `pending` sentinel).

### `mecated schedules` CLI (Phase 2b)

`mecated schedules <verb>` is a thin HTTP client over the running server's
`/v1/schedules` REST surface — it dials `--server-addr` (default the loopback HTTP
listener the server itself binds) and never boots the daemon. Use it for ad-hoc
management against a running `mecated`:

```sh
# Create a cron schedule (flags mirror the REST body).
mecated schedules create --name nightly-review --cron "0 9 * * *" \
  --prompt "Summarize today's commits." --workspace /repo --mode plan

# List all schedules (text by default; --output json for machine consumption).
mecated schedules list

# Inspect one schedule (optionally its recent fires with --fires).
mecated schedules inspect nightly-review --fires

# Pause / resume / delete.
mecated schedules pause  nightly-review
mecated schedules resume nightly-review
mecated schedules delete nightly-review

# Force an immediate fire (synchronous-to-terminal, like FireNow).
mecated schedules fire nightly-review
```

A bare `mecated schedules` or an unknown verb prints the usage banner and exits 2.

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

### Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `--toolhive-llm` | `true` | Auto-detect a locally-running ToolHive LLM proxy by reading ToolHive's own config file and probing `127.0.0.1`; registers it as provider id `toolhive`. Pass `=false` on a shared host where you don't want this. |
| `--toolhive-llm-base-url` | `""` | An EXPLICIT proxy base URL (must resolve to loopback — `127.0.0.0/8`, `[::1]`, or `localhost`). Skips the config-file auto-detect entirely but keeps the startup probe. No environment-variable twin (this is a deliberate, visible flag). |

Registration happens on **detected intent alone** — the proxy does not need to be
running yet, and a later restart with the proxy temporarily down never bricks the
session: mecatl always knows about the "toolhive" provider once ToolHive's config
says it should exist, and a down proxy just means a request-time connection error
(exactly like any other transient provider outage) instead of a rejected session.

### Troubleshooting

`/models` renders the current state for the `toolhive` provider whenever it isn't
healthy:

| State | What you see | Remediation |
| --- | --- | --- |
| `unreachable` | `` toolhive: proxy not reachable — start it with `thv llm proxy start` `` | The proxy isn't running (or its port changed). Start it. |
| `unauthorized` | `` toolhive: gateway rejected the credential — re-auth with `thv llm setup` `` | Your gateway credential expired or was revoked. Re-run setup. |
| `empty` | "your gateway credential lists no models — ask your platform admin or re-run `thv llm setup`" (replaces the generic empty-picker note) | The proxy is reachable and your credential is valid, but it advertises zero models — this is an ORGANIZATIONAL problem (ask your platform admin), not a local one. |

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
