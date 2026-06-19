# mecatl — Usage & Operator Guide

`mecatl` is a headless, agentic coding harness. It owns its own context
window, tool loop, permission policy and lifecycle hooks, and talks to OpenAI
(or any OpenAI-compatible `/v1/responses` endpoint), OpenRouter, or Anthropic
(the native Messages API). The server, `mecated`, exposes one agent run over
**gRPC** and **HTTP/SSE** concurrently — or, opt-in, over **ACP on stdio** for
an editor that spawned it (see §5).

> Security, up front: **the `mecated` API is UNAUTHENTICATED by default.** It
> exposes command and file execution against the configured workspace, so out of
> the box it is intended for **localhost, single-user** use — the default listen
> addresses bind the loopback interface (`127.0.0.1`). Binding a non-loopback
> address without protection exposes unauthenticated command/file execution to
> the network. Before any non-loopback bind, enable the built-in protections:
> bearer auth (`--auth-token` / `MECATL_AUTH_TOKEN`), TLS (`--tls-cert` /
> `--tls-key`), mutual TLS (`--client-ca`), and rate limiting (`--rate-limit` /
> `--rate-burst`) — see the security & transport flags in §3.

---

## 1. Prerequisites & install

| Tool | Version | Needed for |
| --- | --- | --- |
| Go | >= 1.26.3 (toolchain auto-resolves from `go.mod`) | building & running everything |
| [go-task](https://taskfile.dev) | v3 | the `task` build targets |
| [golangci-lint](https://golangci-lint.run/) | v2.x | `task lint` (config: `.golangci.yml`) |
| `goimports` | — | `task fmt` only |
| [buf](https://buf.build/docs/installation) | latest | `task generate` only — regenerating the proto |

Build the binaries into `bin/`:

```console
$ task build
go build -o bin/mecated ./cmd/mecated
go build -o bin/mecademo ./cmd/mecademo
go build -o bin/mecatequi ./cmd/mecatequi
go build -ldflags "-X main.version=..." -o bin/mecatui ./cmd/mecatui
```

This produces `bin/mecated` (the server), `bin/mecatui` (the terminal UI),
`bin/mecademo` (the offline demo), and `bin/mecatequi` (the single-shot headless
CI/batch runner — see §10). To install the operator-facing binaries into
`GOBIN` / `GOPATH/bin`:

```console
$ task install
```

`task install` installs `mecated` and `mecatui`; it intentionally skips
`mecademo`, which is only a demo binary.

Other handy targets (`task --list` for the full set):

| Task | What it does |
| --- | --- |
| `task build` | compile `bin/mecated`, `bin/mecademo`, `bin/mecatequi`, `bin/mecatui` |
| `task install` | install `mecated` and `mecatui` into `GOBIN` / `GOPATH/bin` |
| `task test` | `go test -race ./...` |
| `task test:cover` | tests + `coverage/coverage.{out,html}` |
| `task test:golden` | refresh the `mecatui` View/teatest golden files (`-update`), then re-run them |
| `task e2e` | the **LIVE** e2e suite against OpenRouter — real money + network, needs `OPENROUTER_API_KEY` (see §10) |
| `task fuzz` | bounded coverage-guided fuzzing of the security-critical parsers (`FUZZTIME=2m task fuzz`); not part of `task test` |
| `task lint` | `golangci-lint run` + `go vet` |
| `task fmt` | `gofmt` + `goimports` |
| `task tidy` | `go mod tidy` |
| `task generate` | `buf generate` (no-op unless `buf` + `contracts/proto` present) |
| `task ci` | tidy → fmt → lint → test → build |

You do **not** need a network or an API key for `task build`, `task test`, or
the offline demo.

The default `mecated` build is CGO-free and statically linkable (the ko image
builds it with `CGO_ENABLED=0`).

---

## 2. The 60-second demo

`mecademo` drives a **real `agent.Engine`** through a scripted session against a
canned offline provider (`mockllm`) — no network, no key. It proves the full
shape of the loop: an auto-allowed tool call, a tool call that requires approval
(and is approved), and a final assistant message with usage accounting.

```console
$ go run ./cmd/mecademo
=== mecatl demo (offline / mockllm) ===
Driving a real agent.Engine: auto-allowed tool call -> permission ask + approval -> final result.

[001] turn=0 turn.start
[002] turn=0 message.delta  text="I'll read the greeting file first."
[003] turn=0 tool.call      tool=Read args={"path":"greeting.txt"}
[004] turn=0 tool.result    error=false result="     1\thello from the mecatl demo workspace"
[005] turn=1 turn.start
[006] turn=1 message.delta  text="Now I'll save a short note, which needs your approval."
[007] turn=1 permission.ask ASK tool=Write reason="approval required by rule for Write (note.txt)"  -> client auto-approves
[008] turn=1 tool.call      tool=Write args={"path":"note.txt","content":"reviewed the greeting\n"}
[009] turn=1 tool.result    error=false result="wrote \"note.txt\" (22 bytes)"
[010] turn=2 turn.start
[011] turn=2 message.delta  text="Done: I read greeting.txt and saved note.txt."
[012] turn=0 result         stop=end_turn text="Done: I read greeting.txt and saved note.txt."
      usage: in=4100 out=125 cacheRead=3600 cacheWrite=0 cacheHitRate=0.88
```

What each line means:

- **`turn.start`** — a new model call begins (`turn=N`).
- **`message.delta`** — streamed assistant text for the turn.
- **`tool.call`** — the model requested a tool, with raw JSON `args`.
- **`tool.result`** — the tool's output (`error=false/true`), token-shaped by the tool.
- **`permission.ask`** — the loop paused for client approval; carries the tool,
  the proposed args, and a human `reason`. In the demo a simulated client clicks
  "allow" (`run.Approve(askID, true)`), so the loop resumes. Over HTTP, `POST
  /v1/sessions/{id}/approve` returns **`204 No Content`** when an existing stream
  (the prompt's `text/event-stream` response) carries the verdict's effects; if the
  process that parked the ask had died and a restarted process resumes the session at
  the ask, the same endpoint instead returns a **`text/event-stream`** body (the
  resumed run) — consume it exactly like the prompt stream.
- **`result`** — the terminal event: `stop` reason (`end_turn`, `max_turns`,
  `cancelled`, …), final text, and cumulative `usage`. `cacheHitRate` is
  `cacheRead / inputTokens`.

### Running the demo live

Drive the same scenario against a real model:

```console
$ export OPENAI_API_KEY=sk-...
$ go run ./cmd/mecademo --openai --model gpt-5
```

Demo flags (`cmd/mecademo`):

| Flag | Default | Meaning |
| --- | --- | --- |
| `--openai` | `false` | run against the live OpenAI Responses API (key from `OPENAI_API_KEY`) |
| `--model` | `mock-model` | model identifier when `--openai` is set |
| `--openai-base-url` | `""` | override the OpenAI API base URL |

Without `--openai` the demo is fully offline. With `--openai` and no
`OPENAI_API_KEY`, it exits with `--openai requires OPENAI_API_KEY to be set`.

---

## 3. Running the server (`mecated`)

`mecated` is a composition root: it parses flags/env, then delegates the
assembly — an LLM provider, the seven-tool catalog plus a read-only-explorer
`Subagent` delegation tool (which gets a full shell inside an isolated git worktree when Bash
is configured), the permission policy, lifecycle hooks, the session store, and the
two-layer system prompt — to the shared `internal/app` package (`app.Build`),
and serves the resulting `HarnessService` over gRPC and HTTP/SSE concurrently.
(The TUI reuses that same `app.Build` to host an embedded server — see below.)

```console
$ export OPENAI_API_KEY=sk-...
$ go run ./cmd/mecated --openai --workspace "$PWD"
```

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--grpc-addr` | `127.0.0.1:8080` | gRPC listen address (loopback; **unauthenticated unless** the security & transport flags below are set) |
| `--http-addr` | `127.0.0.1:8081` | HTTP/SSE listen address (loopback; **unauthenticated unless** the security & transport flags below are set) |
| `--workspace` | current working dir | default session workspace root |
| `--model` | `""` | model identifier sent to the provider. Empty → the server-configured default (`--default-model`, when set), else the selected provider's built-in default: `gpt-5` (OpenAI), `openai/gpt-5` (OpenRouter), `claude-sonnet-4-6` (Anthropic). |
| `--default-provider` | `""` | server-configured **deployment-wide default provider** id shared by every client (also on `mecatui`'s embedded server); overrides the built-in provider preference for zero-selector sessions, while a client-side selector still wins. **Fail-fast:** an unknown or unavailable provider refuses startup. |
| `--default-model` | `""` | server-configured **deployment-wide default model** for the default provider (also on `mecatui`'s embedded server); sits below client-side defaults and above the per-provider built-in. **Fail-fast:** a model not catalogued for the default provider refuses startup (stricter than per-session selectors, which allow passthrough). |
| `--openai` | `false` | use the OpenAI Responses provider (key from `OPENAI_API_KEY`) |
| `--openai-base-url` | `""` | override the OpenAI API base URL (compatible endpoints) |
| `--openrouter-base-url` | `""` | override the OpenRouter API base URL (default `https://openrouter.ai/api/v1`; key from `OPENROUTER_API_KEY`) |
| `--anthropic-base-url` | `""` | override the native Anthropic API base URL (compatible/proxy endpoints; key from `ANTHROPIC_API_KEY`) |
| `--mock` | `false` | use a canned offline mock provider (no network; smoke tests only) |
| `--shell` | `/bin/sh` | shell used to execute `Bash`-tool commands; empty disables Bash (shell-less mode). |
| `--no-bash` | `false` | disable the `Bash` tool entirely (shell-less mode); overrides `--shell`. |
| `--compaction` | `heuristic` | compaction strategy: `heuristic` (single-summary) or `cascade` (tiered snip→strip→collapse→summarize). |
| `--tokenizer` | `heuristic` | token counter for the compaction trigger: `heuristic` (dependency-free) or `tiktoken` (offline tiktoken vocab). |
| `--context-window-override` | `0` | override the model's context window (in tokens) used by the compaction trigger — **compaction fires at 80% of it** — AND the footer context-meter denominator echoed to clients (both move together). The operator use is a **workaround**: set it to the model's **actual** window when the model under-reports its window or sits behind a proxy that does (e.g. `--context-window-override 128000` to pin a proxied 128k model). `0` (the default, **disabled**) keeps the live / catalogued / 128k resolution unchanged. ⚠️ A **small** value (below a few thousand tokens) makes the agent compact on **nearly every turn** — that is a churning, degraded mode useful only for **stress-testing compaction** (and the live e2e, `e2e/compaction_test.go`). This moves the trigger **threshold** (and the echoed denominator) only — **orthogonal** to `--compaction` (strategy) and `--tokenizer` (counter); it works identically with either. |
| `--store-dir` | `""` | directory for the JSONL session store (empty → in-memory) |
| `--session-store-url` | `""` | `host:port` of a remote **session-store gRPC driver** (`mecatl.driver.v1.SessionStoreService`); replaces the local store — mutually exclusive with `--store-dir`. **See the store-driver note below.** |
| `--memory-dir` | `""` | per-project **memory store** directory; setting it enables the `Remember`/`Recall`/`SearchMemory` tools (empty disables them). |
| `--memory-consolidate-interval` | `0` | interval for background consolidation ("dream") of the per-project memory store; `0` disables. Only meaningful with `--memory-dir`. |
| `--memory-store-url` | `""` | `host:port` of a remote **memory-store gRPC driver** (`mecatl.driver.v1.MemoryStoreService`); replaces the local flock store — mutually exclusive with `--memory-dir`, enables the memory tools like `--memory-dir` does. |
| `--event-log-url` | `""` | `host:port` of a remote **event-log gRPC driver** (`mecatl.driver.v1.EventLogService`) for the durable per-session event timeline (reasoning, ask/verdict pairs, delegation lifecycle); **INDEPENDENT of the session store** (not mutually exclusive with `--store-dir`). Empty keeps the local default (the `--store-dir` JSONL log, or in-memory). Append happens at the relay (a fault WARNs, never aborts the run); Read is server-streaming. Same auth/TLS posture as `--session-store-url` (equal URLs share one connection). **See the store-driver note below.** |
| `--child-retention` | `168h` | how long persisted **child** session snapshots (`subagent-*`/`parallel-*`/`team-*` ids — the `InspectSubagent`/`resume:` handles) are retained before the GC sweep deletes them. **Main sessions are governed by `--main-retention` instead** (default off). Durable-store-only in effect (`--store-dir` or a prunable `--session-store-url` driver; the in-memory default never accumulates across restarts). `0` disables the age pass. |
| `--child-retention-max-per-family` | `500` | max persisted child snapshots kept **per delegation family** (subagent/parallel/team); the oldest beyond the cap are deleted, skipping in-flight runs. `0` disables the cap. |
| `--main-retention` | `0` | how long persisted **main** (top-level operator/service) session snapshots are retained before the GC sweep deletes them; child sessions use `--child-retention` instead. Durable-store-only. `0` (default) **disables** the main age pass entirely, so main sessions are never touched — `mecated`'s behaviour is unchanged unless you opt in (`mecatui` defaults it on for its durable per-workspace store). |
| `--main-retention-max-total` | `0` | max persisted **main** session snapshots kept **store-wide** (a single global cap, not per-family); the oldest beyond the cap are deleted, skipping in-flight runs. Durable-store-only. `0` (default) disables the cap. |
| `--child-gc-interval` | `1h` | how often the session retention GC re-sweeps after the startup sweep; `0` = sweep at startup only. Only meaningful when a child or main retention/cap knob is active. |
| `--session-lease-url` | `""` | `host:port` of a remote **session-lease gRPC driver** (`mecatl.driver.v1.SessionLeaseService`) for **cross-process single-writer enforcement** (cloud-native Phase 4, multi-replica). Empty = **NO leasing** (the byte-identical single-writer-by-affinity default). Mutually exclusive with `--session-lease-dir` / `--session-lease-k8s-namespace`. Same auth/TLS posture as `--session-store-url`. **See the session-leasing note below.** |
| `--session-lease-dir` | `""` | directory for a **single-host flock** session lease (cross-process single-writer among processes on ONE machine; flock auto-releases on crash). **NOT safe across hosts** — use `--session-lease-k8s-namespace` or `--session-lease-url` for multi-host/multi-replica. Empty = no leasing. |
| `--session-lease-k8s-namespace` | `""` | Kubernetes namespace for `coordination.k8s.io` Lease-backed session leasing (the in-cluster multi-replica path). Uses in-cluster config (or the default kubeconfig out-of-cluster). The ServiceAccount needs RBAC on `leases` in `coordination.k8s.io` for this namespace (**see the session-leasing note below**). Empty = no leasing. |
| `--session-lease-ttl` | `30s` | session-lease lifetime: a crashed/killed holder's lease becomes claimable after this long. Only meaningful when a lease backend is selected. |
| `--session-lease-renew-interval` | `0` | how often the per-session renewer refreshes a held lease; `0` = `--session-lease-ttl` / 3. Keep it well below the TTL so a slow store does not lose the lease and cancel the run. Only meaningful when a lease backend is selected. |
| `--driver-auth-token` | `""` | bearer token sent on every store-driver RPC (or `MECATL_DRIVER_AUTH_TOKEN`; empty disables driver auth). Refused over cleartext to a non-loopback driver — pair with `--driver-tls`. |
| `--driver-tls` | `false` | enable transport TLS on the store-driver connections. |
| `--driver-tls-ca` | `""` | PEM CA bundle to verify the store driver's certificate (with `--driver-tls`; empty uses system roots). |
| `--driver-tls-cert` / `--driver-tls-key` | `""` | PEM client certificate/key pair for **mutual TLS** to the store driver. |
| `--skill-source-url` | `""` | `host:port` of a remote **skill-source gRPC driver** (`mecatl.driver.v1.SkillSourceService`); replaces local skills discovery — mutually exclusive with `--skills-dir`/`--skills-conventional`. **See the source-driver note below.** |
| `--soul-source-url` | `""` | `host:port` of a remote **soul-source gRPC driver** (`mecatl.driver.v1.SoulSourceService`); occupies the USER slot of the soul selection — mutually exclusive with `--soul-file` (`--no-soul` still wins). **See the source-driver note below.** |
| `--agent-source-url` | `""` | `host:port` of a remote **agent-definition gRPC driver** (`mecatl.driver.v1.AgentSourceService`); the definition set is **snapshotted at startup** (fatal if unreachable). Mutually exclusive with `--agents-dir`; the default-on conventional discovery is **superseded** (not an error). **See the source-driver note below.** |
| `--command-source-url` | `""` | `host:port` of a remote **slash-command gRPC driver** (`mecatl.driver.v1.CommandSourceService`); **composes** with file-backed commands (a local command file shadows a same-named driver command) and is consulted **live** per expansion/listing. Probed at startup (fatal if unreachable); runtime faults fail soft. **See the source-driver note below.** |
| `--commands-dir` | `""` | directory of **slash-command templates** (`<name>.md`); setting it enables server-side command expansion. Empty + `--enable-commands` uses the conventional dirs (`.mecatl/commands`, `.claude/commands`). |
| `--enable-commands` | `false` | enable slash-command expansion from the conventional directories (`.mecatl/commands`, `.claude/commands`) when `--commands-dir` is empty. |
| `--skills-dir` | `""` | directory to discover progressive-disclosure skills from, laid out as `<name>/SKILL.md`. **Repeatable** (highest precedence, in the order given); empty disables the `Skill` tool unless `--skills-conventional` is set. **See the skills trust note below.** |
| `--skills-conventional` | `false` | also discover skills from the conventional known paths: `<workspace>/.mecatl/skills`, `<workspace>/.claude/skills`, `$XDG_CONFIG_HOME/mecatl/skills` (or `~/.config/mecatl/skills`), and `~/.claude/skills` — lower precedence than `--skills-dir`. **OFF by default** (strict opt-in); only enable for trusted locations. **See the skills trust note below.** |
| `--skills-draft-dir` | `""` | enable the writable `SkillDraft` tool and set the **quarantine** directory for model-authored candidate skills. Empty disables the tool. Must be **outside the workspace root** (so the model's `Write`/`Edit` cannot reach it) and **disjoint** from every `--skills-dir` / conventional location — both fatal startup errors. **See the self-improving-skill loop note below.** |
| `--skills-draft-similarity-threshold` | `0.5` | 2-gram Jaccard similarity above which `SkillDraft` warns of a near-duplicate existing skill (warn-only; it never blocks the draft). |
| `--soul-file` | `""` | path to a user-scoped, **agent-read-only** persona/"soul" file (empty → the conventional `$XDG_CONFIG_HOME/mecatl/soul.md`, fallback `~/.config/mecatl/soul.md`). Injected as turn-0 context, **fail-soft** (missing/empty/oversized/injection-flagged → no fragment). No tool can write it. **See the persona/soul note below.** |
| `--no-soul` | `false` | disable the user-scoped persona/soul fragment entirely (otherwise it is read from the conventional location, fail-soft if absent). |
| `--approve-soul` | `false` | (re)write the soul **drift baseline** to the current soul's content hash, accepting the file as-is. The baseline is a harness-owned sidecar next to the soul (`<soul-path>.sha256`); a later run whose hash differs logs a drift `WARN`. Use once after intentionally editing your soul. **See the persona/soul note below.** |
| `--soul-strict` | `false` | refuse a **drifted** soul: if its content hash differs from the recorded baseline, contribute **no** soul fragment this run (instead of the default warn-and-load). Pair with `--approve-soul` to accept an edit. |
| `--user-model-dir` | `""` | directory for the user-scoped, **cross-project** user-model store of durable FACTS about the operator (empty → the conventional `$XDG_CONFIG_HOME/mecatl/usermodel`, fallback `~/.config/mecatl/usermodel`). Exposes **RememberUser/RecallUser/SearchUserModel** + a turn-0 `<user-model>` block. **See the user-model note below.** |
| `--no-user-model` | `false` | disable the user model entirely (the RememberUser/RecallUser/SearchUserModel tools and the `<user-model>` block). |
| `--user-model-review` | `false` | enable the **opt-in** background user-model reviewer: after a session stops, a fresh single-shot child extracts durable operator FACTS from the transcript via RememberUser. OFF by default. It **never reopens** the user session; the write path is injection-scanned. |
| `--user-model-review-interval` | `1` | session-count debounce for `--user-model-review` (review every Nth session that stops; 1 = every session). |
| `--user-model-consolidate-interval` | `0` | interval for background consolidation (dream) of the user-model store, scoped to the `user/` namespace; 0 disables. |
| `--permissions-conventional` | `true` | auto-discover the per-project permission config (`<workspace>/.mecatl/settings.yaml`, and with `--import-claude-permissions` also `<workspace>/.claude/settings.json`) plus the user-global file. **Re-resolved per session** against each session's workspace root. ON and inert until such a file exists. **See the permission-config note below.** |
| `--import-claude-permissions` | `false` | also import Claude-Code `settings.json` permissions (project + user). **Lossy** (fail-safe): see the table below. |
| `--trust-project` | `false` | honour the discovered **project authority set**: the project's ALLOW rules (its deny/ask are always honoured regardless), its project persona/soul at `<workspace>/.mecatl/soul.md`, AND the **project tier** of agent definitions, slash commands, and skills (`<workspace>/.mecatl/*`, `<workspace>/.claude/*`). It also gates the **read-only subagent/team-member shell** (issue #40): on an untrusted workspace, Subagent children and read-only members run Bash-less (Read/Grep/Glob only — creating their worktree runs a `git` checkout over the repo's `.git`, where a tracked `.gitattributes` can name filter drivers that execute code with nobody having run anything); mutating members and Parallel branches keep their hardened shells (force-copy forks are created by a pure file copy with no git invocation, and their git afterwards runs over the copied repo — the same exposure as the operator's own session). OFF by default (the safe stance) — an untrusted repo's grants, persona, agents, commands, skills, and subagent shell are withheld; the agent still runs in "ask the human" mode (see the workspace-trust note below). **See the permission-config, persona/soul, and workspace-trust notes below.** |
| `--permission-config` | `""` | path to a YAML permission-config file loaded at the **user (fully-trusted) scope** (**repeatable**). Always loaded regardless of `--permissions-conventional`. |
| `--posture` | `strict` | **OPERATOR POSTURE LADDER.** One ordered tier governs the whole prompt/trust posture: `strict` (default, fail-closed: prompt for the mutate-ask floor, no project trust) → `trusted` (honour the project authority set; still prompts) → `auto` (allow-all main + children, but the **child prompt-injection defence stays ON** — the recommended **unattended** default) → `yolo` (everything `auto` does **plus** the child substitution floor loosened — defence OFF). `--yolo` and `--trust-project` are **aliases** (for `yolo` and `trusted`); when both a `--posture` value and an alias are given the **higher tier wins** (with a `WARN`). An unknown `--posture` value fails closed to `strict` with a `WARN`. CLI out-ranks the user-global `posture:` setting. **See the allow-all/posture note below.** |
| `--print-posture` | `false` | (mecated) print the resolved posture tier and the per-defence breakdown (allow-all, main/child substitution loosening, project-trust floor) to stdout and exit, without starting the server. Useful for confirming what a given flag/env/settings combination resolves to. |
| `--yolo` | `false` | **Alias for `--posture yolo`** (the top tier). **OPERATOR POSTURE (dangerous).** Suppress permission prompts for the built-in mutate-ask floor (`Bash`/`Edit`/`Write`/`Team`/`SkillDraft`) **server-wide**, for the main agent **and** its children (subagents/team members/parallel branches) — for ephemeral, isolated, single-tenant deployments only. **Behaviour change (see the posture note):** `--yolo` now **also waives the child substitution floor** — a subagent/team-member/parallel-branch `$(...)`/backtick/heredoc command **auto-runs** (the child prompt-injection defence is **OFF**). For allow-all with the child defence kept **ON**, use `--posture auto` instead. A `Deny` in **any** scope and any **deliberately configured** `Ask` (managed/project/user) still apply at every tier. **Refused when running as root** (euid 0) unless `MECATL_SANDBOX=1` (or `IS_SANDBOX=1`) is set. **See the allow-all/posture note below.** |
| `--metrics-addr` | `127.0.0.1:9090` | loopback **admin/observability** listener (empty disables). Serves `/metrics` and the runtime-introspection endpoints — **see the observability note below**. |
| `--otlp-endpoint` | `""` | OTLP collector endpoint for trace export (empty → tracing is a no-op; metrics are always on via `/metrics`). |
| `--otlp-protocol` | `grpc` | OTLP transport: `grpc` or `http`. |
| `--otlp-insecure` | `false` | skip TLS for the OTLP exporter (for a local collector). |
| `--flight-recorder` | `true` | arm a bounded in-memory execution-trace **flight recorder** (8 MiB / 5s window) so a trace of the recent past can be snapshotted on demand. Low overhead; `=false` disables. |
| `--mutex-profile-fraction` | `0` | `runtime.SetMutexProfileFraction` rate (0 = off). Populates `/debug/pprof/mutex`; has runtime overhead — enable only while investigating lock contention. |
| `--block-profile-rate` | `0` | `runtime.SetBlockProfileRate` rate in ns (0 = off). Populates `/debug/pprof/block`; has runtime overhead — enable only while investigating blocking. |
| `--goroutine-warn-threshold` | `0` | live goroutine-leak alarm: log a `WARN` whenever `runtime.NumGoroutine()` exceeds this count. `0` disables the alarm (the goroutine-count `/metrics` series is exported regardless); pick a high ceiling (e.g. `10000`) so it fires only on a genuine leak. |
| `--goroutine-warn-interval` | `30s` | how often the goroutine-leak watchdog samples `runtime.NumGoroutine()`. Only consulted when `--goroutine-warn-threshold` > 0. |
| `--perf-mcp` | `false` | mount the **read-only perf MCP server** at `/mcp` on the admin listener (see the observability note). Requires `--metrics-addr`, and that address **must be loopback** — a non-loopback `--metrics-addr` with `--perf-mcp` is **refused** (fail-closed). |
| `--acp` | `false` | serve the **Agent Client Protocol** over stdio (JSON-RPC 2.0 on stdin/stdout) for an editor that spawned `mecated` as a subprocess; the TCP/HTTP listeners are skipped. **See the ACP subsection in §5.** |

#### Delegation & sub-agents

mecatl ships three delegation tools that each spin up an **isolated read-only
child loop** (full shell inside a per-child git worktree when Bash is configured —
see the architecture doc): **Subagent** (one isolated child, returns its result),
**Parallel** (N isolated branches fanned out, joined `all`/`first`/`judge`), and
**Team** (a coordinating crew over a shared task list, findings ledger, and
mailbox). See the delegation-capabilities note below.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--max-run-tokens` | `0` (**unlimited**) | **Default: unlimited** (`0` disables the brake). Maximum **cumulative input + output tokens per agent run**. A run that crosses it ends cleanly with `stop=budget` (terminal `StopBudget`). The budget is **inherited by every Subagent / Parallel branch / team member**, so a delegation fan-out cannot blow past it. Opt in by passing a positive value. |
| `--max-team-tokens` | `0` (**unlimited**) | **Default: unlimited** (`0` disables the brake). Maximum **cumulative input + output tokens per team run**, summed across **all members and rounds**. When crossed the team stops scheduling new rounds — the **in-flight round and the lead's synthesis still complete**, and the report states the budget stop. Applies to the `Team` tool and gRPC `CreateTeam`; a per-call Team `max_team_tokens` may only **tighten** it, and so may the wire `CreateTeamRequest.max_team_tokens` (HTTP: `"max_team_tokens"` in the create body). The outcome (incl. `budget_exhausted` and the `"budget"` stop) rides the **terminal `TeamEvent.outcome` frame** both `RunTeam` surfaces (gRPC stream + HTTP SSE) end with. **Orthogonal** to `--max-run-tokens` (per-run; both compose). |
| `--enable-parallel` | `true` | register the **Parallel** fan-out tool (N parallel isolated child branches). On by default; `=false` disables it. *(Renamed from the former `--enable-fork`.)* |
| `--websearch` | `""` (on) | **WebSearch** (issue #26) **master switch**: web search is **ON by default** (the Exa anonymous tier — no key, no config). Pass `--websearch=off` to **disable** it entirely (the kill switch — no outbound search calls; the tool reports it is disabled). Any value other than `off` (or unset) leaves web search enabled. Mirrors `--guardrails`. |
| `--websearch-url` | `""` | **WebSearch explicit override**: base URL of a vendor-neutral HTTP JSON search endpoint (e.g. a [SearXNG](https://docs.searxng.org/) `/search` URL or any generic JSON search API). When set it **wins over** the `SEARXNG_URL`/`BRAVE_API_KEY` env tiers **and** the Exa default. The API key is read from the **`WEBSEARCH_API_KEY`** env var (a secret, never a flag value). The adapter carries its **own** per-call timeout (10s) and concurrency limit (4), so the read-parallel dispatcher cannot launch unbounded egress. **Backend ladder + walkthrough: see [Enabling web search](#enabling-web-search) below.** |
| `--websearch-auth-header` | `"Authorization"` | HTTP header the `WEBSEARCH_API_KEY` is sent in (for `--websearch-url`) — default `Authorization` as a `Bearer` token; set e.g. `X-API-Key` to send the raw key. The secret rides the **header only, never the query string**. Ignored when no key is set. |
| `--websearch-query-param` | `"q"` | URL query parameter the search string is placed in (for `--websearch-url`). Tune for a generic JSON search endpoint that expects a different parameter name. |
| `SEARXNG_URL` *(env)* | `""` | **WebSearch SearXNG tier**: point at a **self-hosted** [SearXNG](https://docs.searxng.org/) `/search` URL to switch the backend to SearXNG (no API key). Wins over `BRAVE_API_KEY` and the Exa default; loses to `--websearch-url`. |
| `BRAVE_API_KEY` *(env)* | `""` | **WebSearch Brave tier**: a [Brave Search API](https://brave.com/search/api/) key switches the backend to Brave (sent in the `X-Subscription-Token` header against the Brave Web Search endpoint). The key is **never logged**. Wins over the Exa default; loses to `SEARXNG_URL`/`--websearch-url`. |
| `EXA_API_KEY` *(env)* | `""` | **WebSearch Exa paid tier**: an [Exa](https://exa.ai/) key upgrades the **default** Exa backend from the anonymous tier to the paid tier (appended as `?exaApiKey=` to the Exa endpoint, escaped). The key is **never logged**. Without it the Exa default runs anonymously. |
| `--fork-preserved-cap` | `agent.DefaultPreservedForkCap` | max **PRESERVED** winner forks (for `join=first`/`judge`) kept on disk at once — the oldest beyond this is LRU-reaped. Preserved fork workspaces stay inspectable (their paths ride the Parallel result) until reaped. |
| `--enable-teams` | `true` | register the experimental **agent-teams** capability (`CreateTeam`/`SpawnTeammate`/`RunTeam` + the in-loop `Team` tool). On by default and **inert** until a client drives a team; `=false` disables it. |
| `--subagent-model` | `""` | global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model (via an agent definition `model:` or a per-call override) — the analogue of `CLAUDE_CODE_SUBAGENT_MODEL`. The Parallel judge stays on the session model. A concrete id or a `--model-alias`; same provider as the session. Empty inherits the parent `--model`; a non-empty value that does not resolve to a usable model id (unknown alias, or an alias meaning *inherit* — the built-in `sonnet`/`opus`/`haiku` unless overridden) **fails startup**. `mecatui` accepts the same flag for its embedded server. |
| `--headless` | `false` | run **non-interactive**: declare that clients drive sessions but never answer permission prompts (autonomous / CI). A **child** (subagent/member/branch) unresolved permission ask is then **not surfaced** to the client (nobody would answer it — it would park until run-end) but resolved by the auto-deny path / the opt-in `--subagent-ask-reviewer`. **Caveat — this gates only CHILD asks: a MAIN-session ask still surfaces and, headless, parks unanswered forever.** Pair `--headless` with permission `allow` rules (or `--yolo`) covering the main agent's tool use, or those asks will hang. Default off: a normal mecated serving an interactive client (mecatui, an IDE) surfaces asks for a human. **`--subagent-ask-reviewer` only engages under `--headless`** — setting it on an interactive server is inert (a startup WARNING says so). |
| `--subagent-ask-reviewer` | `""` | **OPT-IN headless ask reviewer**: model id or `--model-alias` of a tool-less ONE-TURN reviewer that adjudicates a **headless** subagent/member/branch permission ask the 4-step model would otherwise blanket auto-deny. **Requires `--headless`** (on an interactive server — including the `mecatui` embedded server — it is inert: asks surface to the client/modal instead). An allow approves **this call only** (never learned); a deny — or any reviewer error/timeout/ambiguity — keeps the call denied (**fail-safe**); each adjudication is **one extra LLM call** on the reviewer model. Configured `deny`/`ask` rules always win. The gRPC `RunTeam`-direct path is **excluded** (it runs zero-caps — no reviewer). Resolved on the **session's provider** (same-provider only). Empty (default) disables it; an unusable model id **fails startup** (validated even when inert). Deliberately a **server flag, not a permission-config key** — see the permissions section. A configured `ask-reviewer` **model slot** (`--model-slot ask-reviewer=…`) **supersedes** this flag's model, but the flag stays the on/off gate. `mecatui` accepts the same flag for its embedded server but it is inert there (the embedded server is interactive). |
| `--subagent-ask-reviewer-max-denies` | `3` | circuit breaker for the reviewer: after this many **consecutive** non-allow reviewer outcomes (denies/failures/timeouts) within one run, further asks skip the reviewer and fall through to the plain auto-deny; an allow resets the count. |
| `--subagent-ask-reviewer-policy` | `""` | path to a **TRUSTED** policy rubric file; its content replaces the built-in rubric the reviewer applies. The built-in rubric (allow only clearly read-only or standard build/vet/test commands; deny anything that mutates shared state, touches the network/credentials, or whose effect is unclear) lives in `defaultAskReviewPolicy` (`engine/agent/askadjudicator.go`); a custom file is **plain prose** in the same style. Read once at startup; an unreadable file **fails startup**. |
| `--subagent-model-router` | `false` | **OPT-IN semantic model router** ([ADR 0031](adr/0031-subagent-model-router.md), extended to team members + Parallel branches by [ADR 0034](adr/0034-team-parallel-model-routing.md)): when set, a tiny one-turn classifier (on the `router` model slot) reads a delegation's task prompt and the operator's category taxonomy and picks which model the child runs on — for a **plain** `Subagent` delegation, for each **plain undefined agent-team member** (classified once at enrolment off its role briefing; a member with an agent def pins its own model), and for each **Parallel branch**. The taxonomy (categories + per-category model + a default) lives in the **operator-tier** `settings.yaml` `models.router:` subtree (a project-tier `router:` is stripped with a WARN); this flag is **only the enable gate**. It fires **before** the child is minted (decide-once, commit-for-lifetime, same-provider) and **only** to fill the gap — an explicit per-call `model`/`agent`, a `fork`, a `resume`, or a member's agent def already pins the engine (precedence: per-call `model` > agent-def `Model` > fork/resume > router > inherited default). **Fail-soft**: any classifier failure, an unknown category, an unresolvable target, or a per-run circuit breaker (3 consecutive misses, **shared** across all three families) → the inherited default model. Runs in **both** interactive and headless deployments; the gRPC `RunTeam` direct path is zero-caps and never routes. Empty/false (default) = **OFF, byte-identical** to no router. Deliberately a **server flag, not a permission-config key** — autonomous per-delegation model selection is an operator deployment decision. `mecatui` accepts the same flag for its embedded server. |
| `--agents-dir` | `""` | directory of named **agent definitions** (`<name>.md` + YAML frontmatter — `name`/`description`/`tools`/`model`/`provider`/`permissionMode`/`maxTurns`/`maxToolCalls`/`color`/`skills`/`mcpServers`/`hooks`/`memory`; full reference in `docs/adr/0013-agent-definitions.md`), reusable as a `Subagent(agent=<name>)` delegate and as a team-member role (repeatable; highest precedence). **TRUST BOUNDARY:** a def body steers the model like `AGENTS.md`/`CLAUDE.md`. A `memory: user\|project` field (issue #33) injects a per-agent `MEMORY.md` head into the def's prompt at startup (READ-ONLY in v1); the **project** tier is **`--trust-project`-gated** (it points into the attacker-controllable workspace). |
| `--agents-conventional` | `true` | also discover agent defs from the conventional locations (`<workspace>/.mecatl/agents`, `<workspace>/.claude/agents`, `$XDG_CONFIG_HOME/mecatl/agents`, `~/.claude/agents`; lower precedence than `--agents-dir`). ON and **inert** until such a dir exists. Project-tier defs are **trust-gated** (`--trust-project`). |
| `--model-alias` | `""` | model alias mapping `name=model-id` (repeatable), resolved only in composition — an agent def's `model: <alias>`, a `--model-slot` selector, and `--subagent-model` all resolve through this map (then the built-in sonnet/opus/haiku aliases). |
| `--model-slot` | `""` | **PER-SLOT MODELS** ([ADR 0030](adr/0030-model-selection-heuristics.md)): bind an internal lightweight LLM call to its own model as `slot=selector` (**repeatable**), e.g. `--model-slot compaction=cheap --model-alias cheap=gpt-4o-mini`. The routed slots are `compaction` (the compaction summary call), `ask-reviewer` (the headless child-ask reviewer), `guardrail` (the content checker), `plan` (plan-mode → model re-resolution, the opusplan pattern, [ADR 0030](adr/0030-model-selection-heuristics.md) Layer 3), and `router` (the subagent model-router classifier, [ADR 0031](adr/0031-subagent-model-router.md)); a **tier** key (`cheap`/`fast`/`reasoning`) gives a default a slot falls through to (the four internal-call slots — including `router` — default to `cheap`, but **`plan` defaults to `reasoning`**). The selector is a `--model-alias` or a concrete id, resolved on the **session's provider**. Empty (no `--model-slot`) keeps every call on the **session model** (**byte-identical default**). **Fail-soft**: a typo'd slot or an alias meaning *inherit* WARNs and keeps the session model — it never wedges the call. For `ask-reviewer`/`guardrail` the slot **supersedes the model** of `--subagent-ask-reviewer`/`--guardrails-model` but does **not** enable them (those flags stay the on/off gate). The YAML twin is the `settings.yaml` `models.slots:` subtree: operator-tier by default, and project-overridable **within an operator `models.allowlist`** on a trusted repo (ADR 0030 Phase 4 — see the per-slot models section); with no allowlist a project `models:` block is ignored with a WARN. `mecatui` accepts the same flag (and `--model-alias`) for its embedded server. |
| `--guardrails-model` | `""` | **GUARDRAILS** (issue #27): model id / `--model-alias` of a tool-less checker that inspects **outbound** tool-call args (`PreToolUse`, exfil) and **inbound** tool results (`PostToolUse`, prompt injection) and enforces a verdict. Empty (default) **disables** guardrails; an unusable model id **fails startup**. Configuring a model is the **opt-in to spend** — with **no rule list** it takes the **default advisory rule set** (WebSearch/WebFetch/mcp__\*, observe-only). The optional **rule list** + cost knobs live in the **user-global** `settings.yaml` `guardrails:` subtree (operator-tier **only** — a project repo cannot configure or weaken a checker; a project-tier block is ignored with a WARN); an explicit rule list replaces the defaults. `--guardrails-model` overrides the YAML model; a configured `guardrail` **model slot** (`--model-slot guardrail=…`) **supersedes** the checker model (this flag stays the on/off gate). Fires on the main loop regardless of `--headless`. **See the guardrails section below + `docs/adr/0021-guardrails.md`.** |
| `--guardrails` | `""` | guardrails master switch: pass `--guardrails=off` to force the checker **off** regardless of `--guardrails-model` / the `guardrails:` YAML (the kill-switch). Leave it unset to keep guardrails governed by the model + rule config. **Only `off` is accepted** — any other value (e.g. `--guardrails=on`, which does NOT enable: set `--guardrails-model` for that) **fails startup** rather than silently doing nothing. |

> **Delegation capabilities (Subagent / Parallel / Team).** Beyond the shared
> `--max-run-tokens` budget (**default: unlimited**), every delegation supports: an explicit **child-concurrency
> cap** (default 4) bounding how many children run at once; **per-call limits**
> (`max_turns` / `max_tool_calls` / `timeout`, **tighten-only** — a call can never
> loosen the inherited bounds) plus a **per-call model override** and a **per-call
> token budget** (`max_run_tokens`, the preferred arg; `max_tokens` is the deprecated
> alias for the same cumulative input+output budget — setting both to different values
> is rejected; **default: inherited/unlimited**, tighten-only); **opt-in structured output** (a synthetic `SubmitResult` tool with
> bounded validation-retry when the caller supplies a result schema); and, on a
> Subagent, an **agentId trailer** on the returned result plus a `References:`
> convention the explorer uses to cite the files it read. A Subagent call may also
> **fork** (`fork: true` — seed the child from a copy of the parent's full
> conversation instead of an empty context, for a focused continuation; runs on the
> parent's model, mutually exclusive with `model`/`agent`/`resume`). A Subagent call may also run
> **in the background** (`background: true` — returns immediately with the agentId; the
> child keeps working, RUN-scoped, and the model collects the result via the
> **`SubagentStatus`** tool, prompted by a turn-boundary notice; still running at run
> end ⇒ cancelled but persisted + resumable), and every child — subagent, parallel
> branch, team member — is **individually cancellable** by its id (gRPC `cancel_child`
> frame / HTTP `POST /v1/sessions/{id}/cancel-child` / the `x` key in mecatui's
> overlay) without touching the run; see `docs/adr/0015-background-subagents.md`.
> **Parallel** is observable
> over a dedicated `parallel.*` event family, and its result carries the preserved
> fork-workspace paths. A **Team** additionally honours a **team-wide token budget**
> (`--max-team-tokens`, **default: unlimited**, tightenable per call) checked at the round boundary — orthogonal
> to the per-run `--max-run-tokens`, which still bounds each member drive. Team stream
> events are intentionally watchable but bounded: member previews/tasks/findings are
> capped, permission asks are never forwarded, and `team.end` includes closed-enum
> member dispositions. (The `mecatui` `ctrl+a` overlay surfaces all three under
> **Subagents | Parallel | Teams** tabs with a fleet-status footer — see `docs/tui.md`.)

#### Enabling web search

The **WebSearch** tool is **always present** and, as of issue #26, **ON by default**:
out of the box it runs against the **Exa anonymous tier** — no key, no account, no
config. (The keyless general-web search option has been disappearing across the
industry — Jina closed its keyless tier, Google CSE is shutting down — and Exa's
anonymous endpoint is the last one standing; mecatl uses it as the zero-config
default while it lasts, and degrades gracefully when it doesn't.) You only need the
rest of this section if you want to **switch backends** or **turn search off**.

**The backend ladder (first match wins):**

1. `--websearch=off` — the **kill switch**. No outbound search; the tool reports it
   is disabled. For operators who don't want any default egress.
2. `--websearch-url` — an **explicit** HTTP-JSON endpoint (below). Wins over the env
   tiers and the Exa default.
3. `SEARXNG_URL` *(env)* — a self-hosted SearXNG `/search` URL (below).
4. `BRAVE_API_KEY` *(env)* — a Brave Search API key (below).
5. **default** — **Exa anonymous** (or the **Exa paid tier** if `EXA_API_KEY` is set).

**The default — Exa (zero-config):** nothing to do. mecated speaks a minimal
streamable-HTTP JSON-RPC handshake to Exa's public MCP endpoint
(`https://mcp.exa.ai/mcp` → `web_search_exa`) — anonymously. It makes **exactly the
three POSTs** the protocol needs and does **no OAuth discovery** (Exa publishes OAuth
metadata it does not enforce; mecatl never probes `/.well-known/`). Set `EXA_API_KEY`
to upgrade to the paid tier (the key is appended to the endpoint, escaped, and
**never logged**). If Exa is unreachable or rate-limited, WebSearch returns a
model-visible *"temporarily unavailable"* message naming the upgrade path — never a
silent empty result or a hang.

**Switching to Brave (a key, an independent index):** export `BRAVE_API_KEY` — that's
it. mecated targets the Brave Web Search endpoint with the key in the
`X-Subscription-Token` header and parses Brave's `{"web":{"results":[…]}}` shape.
The key is **never logged**.

```sh
export BRAVE_API_KEY=…      # the secret; sent in a header, never the query string
mecated                     # …plus your usual flags — Brave is now the backend
```

**Switching to SearXNG (no API key):** [SearXNG](https://docs.searxng.org/)
is a self-hostable metasearch engine. SearXNG ships with the JSON output format
**disabled**, and mecatl requests `format=json` — so you must enable it. Write a
minimal config that layers JSON onto SearXNG's defaults, then run it and point
mecated at its `/search` endpoint — no key, no account:

```sh
# 1. enable the JSON format (SearXNG layers this over its defaults)
mkdir -p searxng
cat > searxng/settings.yml <<'YAML'
use_default_settings: true
server:
  secret_key: "change-me-to-any-random-string"   # SearXNG refuses to start without one
search:
  formats: [html, json]                           # mecatl needs json; html is SearXNG's default UI
YAML

# 2. run it with that config mounted
docker run --rm -d -p 8080:8080 -v "$PWD/searxng:/etc/searxng" searxng/searxng

# 3. point mecated at it (env tier — wins over the Exa default)
export SEARXNG_URL=http://localhost:8080/search
mecated                                                # …plus your usual flags
```

**A generic / commercial search API (explicit override):** `--websearch-url` speaks a
**GET (or POST) with form-encoded query parameters** and parses the JSON shape below;
it **wins over** the env tiers and the Exa default. APIs that fit that shape work
directly. Supply the key via the **`WEBSEARCH_API_KEY`** environment variable (never a
flag — it's a secret); tune the header and query parameter for the endpoint:

```sh
export WEBSEARCH_API_KEY=…          # the secret; sent in a header, never in the URL/query
mecated --websearch-url https://api.search.brave.com/res/v1/web/search \
        --websearch-auth-header X-Subscription-Token \   # default "Authorization" (Bearer); set this for a raw-key header
        --websearch-query-param q                        # default "q"
```

> The `BRAVE_API_KEY` env tier above is the shortcut for exactly this Brave endpoint
> (it sets the URL, header, and query param for you); `--websearch-url` is the general
> escape hatch for any other JSON endpoint.

> **Not every API fits.** The adapter sends the query as **form/URL parameters**, not
> a JSON request body — so a service that requires a **JSON POST body** (e.g. Tavily)
> won't work directly. Front such an API with a small adapter/proxy that exposes the
> GET-params + `{"results":[…]}` shape, or use SearXNG.

**The JSON response contract** (what mecatl parses — the SearXNG shape, with field
aliases so most APIs map cleanly):

```json
{ "results": [
  { "title": "…", "url": "https://…",
    "snippet": "…",            // or "content" / "description" (first non-empty wins)
    "date": "2026-06-01",      // or "publishedDate" (optional)
    "source": "example.com" }  // or "engine" (optional)
] }
```

**Security posture:** web search is **ON by default**, so there **is** default
outbound egress (to Exa) — the kill switch (`--websearch=off`) is the operator escape
for deployments that don't want it. The query is sent **verbatim** (mecatl adds
nothing — secret-scanning of the query is the guardrails layer's job, issue #27,
which observes `WebSearch` for the exfiltration residual); any API key rides an
**HTTP header** (HTTP-JSON tier) or the escaped `?exaApiKey=` param (Exa paid tier),
**never logged**; the adapters do **not follow redirects** (an SSRF guard — a backend
cannot 302 the request to an internal address); and each carries its **own** 10s
per-call timeout + concurrency limit (4) so the read-parallel dispatcher can't launch
unbounded egress. Results are treated as **untrusted** and fenced before they reach
the model. Permission default is a config-overridable floor **Allow** (like
`WebFetch`); arg-pattern permission rules can target the `query` (e.g. an
`ask`/`deny` rule on a query glob).

**The JSON response contract** for the HTTP-JSON tiers (`--websearch-url`/`SEARXNG_URL`/
`BRAVE_API_KEY`) is shown above; the Exa default needs no schema knowledge.

**Verify it's working:** start a session and ask the agent to *"use the WebSearch
tool to search for `golang slices`"*. The default (Exa) returns a fenced, numbered
list of `Title — url` results; an unreachable backend returns the *"temporarily
unavailable"* message; and `--websearch=off` returns the *"disabled on this
deployment"* message (no network call).

#### LLM resilience knobs

These tune the resilience decorator wrapped around every provider (see
`internal/adapter/llmresilience`). They split cleanly into the **establishment**
window (retryable) and the **post-first-chunk** stream (terminal).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--llm-max-attempts` | `3` | max stream-establish attempts (initial call plus retries). Retries apply ONLY before the first committing chunk (text, tool calls, usage, or done) — non-committing reasoning chunks are buffered and discarded on retry. |
| `--llm-per-attempt-timeout` | `300s` | per-attempt timeout for **establishing** an LLM stream (connect + first committing chunk (text, tool calls, usage, or done) only; **never cuts an actively-streaming turn** — enforced by a separate timer stopped at the first committing chunk, not an absolute deadline that lingers through the stream). The timer stays live through any leading reasoning prefix — so increase this value if your model has a long thinking phase before its first output token. A timeout here is **retryable**. 0 disables. Generous by default so a slow large-context reasoning model has time to first token. `mecatui` accepts the same flag for its embedded server. |

> **Note — the per-attempt-timeout default was raised from 60s to 300s.** It now bounds
> establishment (time-to-first-token) ONLY, so the old 60s value starved slow reasoning
> models that legitimately take longer than a minute before their first chunk. If you
> prefer faster failover (e.g. quick retry/breaker engagement against a flaky provider),
> lower `--llm-per-attempt-timeout` — it no longer risks cutting a long actively-streaming
> turn, which the `--llm-stream-idle-timeout` watchdog now governs instead.
| `--llm-stream-idle-timeout` | `180s` | max idle gap between LLM stream chunks **after the first chunk**. A longer stall **ends the turn as an error** (it is terminal, never retried — replay is unsafe mid-stream). Guards against an upstream SSE connection that stalls mid-stream and would otherwise hang the turn forever. 0 disables. `mecatui` accepts the same flag for its embedded server. |
| `--llm-breaker-threshold` | `5` | consecutive **transient** establishment failures that open the per-provider circuit breaker (0 disables). |
| `--llm-breaker-cooldown` | `30s` | how long the breaker stays open before admitting a half-open trial. |

#### MCP & ToolHive

| Flag | Default | Meaning |
| --- | --- | --- |
| `--mcp-server` | `""` | remote MCP server as `name=URL` (**repeatable**); the auth token is read from `MCP_<NAME>_TOKEN`. **TRUST BOUNDARY:** a connected server's tools enter the model context. |
| `--mcp-resource-tools` | `true` | register the `ListMcpResources`/`ReadMcpResource` meta-tools when a connected MCP server exposes resources (no-op when none do). A remote resource's contents enter model context like any other MCP output. |
| `--mcp-prompts` | `true` | expand `/mcp__<server>__<prompt> key=value` inputs into the server-rendered prompt (static snapshot taken at connect). An MCP prompt steers the model like a slash command — enable only for servers you trust. |
| `--toolhive` | `true` | discover MCP servers from the **running ToolHive workloads** (the embedded ToolHive library lists already-running workloads and reads their HTTP proxy URLs; mecatl **never** starts or spawns a workload). Fails soft to zero servers when no container runtime is reachable. Same trust class as `--mcp-server`. |
| `--toolhive-group` | `""` | ToolHive group to discover workloads from (empty → the `default` group). Only consulted with `--toolhive`. |

#### Security & transport (auth, TLS, rate limiting)

All off by default (the loopback single-user posture); set them **before** any
non-loopback bind — see the trust note below.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--auth-token` | `""` | bearer token required on **every** gRPC/HTTP request (or `MECATL_AUTH_TOKEN`; empty disables auth). |
| `--tls-cert` / `--tls-key` | `""` | PEM server certificate/key pair; together they enable TLS on the gRPC + HTTP servers. |
| `--client-ca` | `""` | PEM client-CA bundle; enables **mutual TLS** (require + verify client certs). |
| `--rate-limit` | `0` | sustained per-client request rate in req/s (`0` disables rate limiting). |
| `--rate-burst` | `0` | rate-limit token-bucket burst size (`0` derives a sane default from `--rate-limit`). |

### Observability (the loopback admin listener)

`--metrics-addr` (default `127.0.0.1:9090`, empty disables) serves, **loopback-only and
unauthenticated** by design — these endpoints can expose prompt text and internal
state, so they must never be bound off-localhost:

| Endpoint | What |
| --- | --- |
| `/metrics` | Prometheus scrape — domain metrics (`mecatl_*`, incl. the turn/TTFT/inter-token/tool latency exponential histograms) plus Go runtime + process-RSS series, via the OTel prometheus exporter. |
| `/debug/pprof/` | the standard pprof profiles (`heap`, `goroutine`, `allocs`, `profile` (CPU), `trace`, and — when the rate flags are set — `mutex`, `block`). Capture with `go tool pprof http://127.0.0.1:9090/debug/pprof/heap`. |
| `/debug/vars` | a curated `mecatl_runtime` JSON snapshot (goroutines, heap, GC pauses, RSS, uptime) from `runtime/metrics` — cheap, structured, no STW. |
| `/debug/flightrecorder` | a snapshot of the in-memory flight-recorder ring (an execution trace of the recent past); view with `go tool trace`. Absent when `--flight-recorder=false`. |
| `/mcp` | the **perf MCP server** (read-only). Mounted only with `--perf-mcp`. Lets an agent introspect this process's runtime/latency/profile state over MCP — `list_slow_turns`, runtime/heap/CPU profile rankings, FlightRecorder summaries — returning **reduced numeric summaries** (never raw blobs). Absent (404) when `--perf-mcp` is off. |

> These are the **in-process** profiling sources — no external profiling backend
> is required. The `/mcp` endpoint (opt-in via `--perf-mcp`) exposes reduced,
> agent-readable summaries of the same data. See `docs/adr/0018-perf-observability.md`.

#### The perf MCP server (`--perf-mcp`)

`--perf-mcp` mounts a read-only MCP server at `/mcp` on the admin listener so an
agent can scrape this process's own performance state through MCP tools instead
of a human reading raw `/metrics` / `/debug/pprof`. It is **unauthenticated**
(decision 6: loopback + the SDK's DNS-rebinding protection only) and its output
can embed goroutine-derived function names and timing, so it is **fail-closed**:
`--perf-mcp` on a **non-loopback** `--metrics-addr` is **refused at startup**
(`bind loopback or add auth (future work)`). Any future off-loopback exposure
**MUST** add auth.

Generate a paste-ready client config with:

```sh
mecated perf-mcp print-config --metrics-addr 127.0.0.1:9090
```

It prints (note: **no `Authorization` header** — the surface is loopback/no-auth):

```json
{
  "mcpServers": {
    "mecatl-perf": {
      "type": "http",
      "url": "http://127.0.0.1:9090/mcp"
    }
  }
}
```

> **Embedded `mecatui` server:** when `mecatui` hosts its own server (`--perf
> --perf-mcp`), the admin surface defaults to a **fixed `127.0.0.1:9099`** (whereas
> `mecated` defaults to `9090`). A `mecatui` user can generate the matching client
> snippet by overriding the address — `mecated perf-mcp print-config --metrics-addr
> 127.0.0.1:9099` — or simply hardcode the `http://127.0.0.1:9099/mcp` URL, since
> the port is now predictable across restarts.

The perf server's `query_metric` tool and `perf://metrics/summary` resource expose
a **curated** counter/gauge/histogram set (not the full `/metrics` scrape). Beside
the per-run `runs_total{stop}`, the turn-semantics counters (issue #81)
`turns_total` (one per COMPLETED turn — the per-turn denominator, unlabelled by
stop) and `turn_empty_total` (the EMPTY SUBSET of those turns — no tool call, no
text) are curated too, each with the bounded per-role breakdown in the summary.
The two are not disjoint: an empty turn bumps **both** (`turns_total` via its
`EvTurnEnd`, then `turn_empty_total` via `EvNoProgress`), so
`turn_empty_total / turns_total` is the empty-turn **share** (empty turns ⊂ all
completed turns). `turn_empty_total` counts `EvNoProgress` emissions (one per
advisory nudge plus the give-up, up to `MaxNoProgressNudges+1` per stuck
sequence), not distinct sequences. A high share is the signature of a model going
silent mid-run.

A companion **interpretation skill** ships at
`.claude/skills/perf-mcp-interpretation/` — it teaches an agent to read this
server's reduced output (tool routing/cost, pprof rankings, the leak/contention/GC
signatures, the upper-bound caveat). Because it lives under `.claude/skills/`, an
agent working in this repo (e.g. Claude Code) discovers it automatically; an
external MCP client can copy it in alongside the perf-server config so the
connected agent knows how to act on the numbers.

### Environment

| Var | Effect |
| --- | --- |
| `OPENAI_API_KEY` | the OpenAI API key. **If set, it implies `--openai`** — the real provider is selected automatically. |
| `OPENROUTER_API_KEY` | the OpenRouter API key. **If set, the `openrouter` provider is auto-detected** — it rides the same stateless Responses adapter against `https://openrouter.ai/api/v1` (override with `--openrouter-base-url`). OpenRouter also accepts an `OPENAI_API_KEY` by convention. |
| `ANTHROPIC_API_KEY` | the Anthropic API key. **If set, the native `anthropic` provider is auto-detected** — the native Messages-API adapter (NOT the Responses adapter), stateless full-replay, with extended thinking **on** (model-aware: adaptive for Opus 4.8/4.7/4.6 + Sonnet 4.6, manual budget for older families). Default model `claude-sonnet-4-6`; override the host with `--anthropic-base-url`. |
| `MECATL_AUTH_TOKEN` | bearer token for the API (`--auth-token`) when the flag is unset — keeps the secret off the process argv. |
| `MECATL_DRIVER_AUTH_TOKEN` | bearer token for the store/source drivers (`--driver-auth-token`) when the flag is unset. |
| `MECATL_SANDBOX` / `IS_SANDBOX` | set either to `1` to affirm an isolated, disposable environment so the **allow-all postures** (`auto` and `yolo` — both waive the built-in mutate-ask floor) are permitted while running as root. Root + no prompts is refused otherwise (generalised from the old `--yolo`-only refusal). |

### Provider selection

A provider is **required** — the server has nothing to do without one. The server
builds an N-provider registry and AUTO-DETECTS availability from the environment:

- `OPENAI_API_KEY` set (or `--openai`) → the `openai` Responses provider.
- `OPENROUTER_API_KEY` set → the `openrouter` provider (same adapter, OpenRouter
  base URL; falls back to `OPENAI_API_KEY` by convention).
- `ANTHROPIC_API_KEY` set → the native `anthropic` Messages provider (extended
  thinking on, model-aware; default model `claude-sonnet-4-6`).
- `--mock` → canned offline provider (single text turn; smoke tests only).
- none of the above → startup error (the daemon refuses to start; mecatui fails the
  same check client-side before hosting an embedded server). The message enumerates
  the accepted keys per adapter, the compatible/proxy base-URL overrides
  (`--openai-base-url` / `--anthropic-base-url` / `--openrouter-base-url` for an
  endpoint without a public key), the offline `--mock` escape hatch, and points here:
  `no LLM provider available: set one of ANTHROPIC_API_KEY (Claude), OPENAI_API_KEY (OpenAI), or OPENROUTER_API_KEY (one key, many models — a good first choice) in the environment; for an OpenAI- or Anthropic-compatible/proxy endpoint pass the matching key plus --openai-base-url / --anthropic-base-url / --openrouter-base-url; to try mecatl offline with no key run with --mock; see docs/usage.md for provider setup`.

When more than one provider is available, `openai` is the default (single-provider
back-compat) — a `CreateSession` with no selector uses it.

#### Per-session provider/model selection (wire)

`CreateSession` accepts an OPTIONAL **`provider_id`** + **`model_id`** selector
(gRPC fields; JSON `provider_id`/`model_id` on `POST /v1/sessions`), so one server
can drive different providers/models per session. The provider is **fixed for the
session lifetime** — "switch provider" means a new session. The two fields are
distinct (never slash-joined). Resolution:

| `provider_id` | `model_id` | Result |
|---|---|---|
| empty | empty | the server **default** provider (today's behaviour) |
| empty | set | **`InvalidArgument`** — a bare model on the default provider is ambiguous; name the provider |
| available | empty | that provider's default model |
| available | catalogued | bound to (provider, model) |
| available | not catalogued | **passthrough** — the model string is sent verbatim (escape hatch for a model the catalog doesn't yet know) |
| unknown / unavailable | any | **`InvalidArgument`** — `unknown or unavailable provider`, never a silent fallback |

Keys are NEVER on the wire — only the provider id. A server caps the number of live
per-session engines (selector OR client-MCP sessions): exceeding it returns
`ResourceExhausted` (gRPC) / HTTP `429` — close sessions you finish with
`CloseSession` / `DELETE /v1/sessions/{id}` to free slots.

#### Discovering models — `ListModels` / `GET /v1/models`

`ListModels` returns the selectable-model inventory: every **available** provider's
models projected to public metadata only — `id`, `provider_id`,
`display_name`, `image`, `reasoning`, `context_limit` — **no keys, env-var names, or
base URLs**, and an unavailable provider is omitted entirely. The list is
`(provider_id, id)`-sorted; it is empty when no provider is available (or under
`--mock`). `ServerCapabilities.model_selection` is `true` iff the list is non-empty,
so a client can hide its picker against an older/empty server. The advertised
`context_limit` is the SAME catalog value the per-session engine uses for its
compaction trigger, so a large-context model is not compacted at the 128k default.
This holds for the configured **default** model too (issue #63): it resolves its real
window and compacts at that window, not a hardcoded 128k — the 128k floor now applies
only to a genuinely uncatalogued model. The window is resolved **live-first at the point
of use** (one resolver shared by the engine trigger and the `resolved_model` echo), so a
live-only model whose curated catalog lacks a window self-corrects to its live window
once the background catalog refresh lands — no engine rebuild. `--context-window-override`
still forces a fixed window when you need it, and now moves BOTH the compaction trigger
and the client footer denominator together.

For a provider with **live model listing** (currently **OpenRouter**), the picker
reflects the provider's **real, live catalog** (336 models) rather than the curated
embedded subset, when that provider is keyed. The live list is fetched once in the
background just after startup and swapped in — `/models` shows the embedded subset at
first and the full live set a moment later (and offline, or on an upstream blip, it
keeps showing the curated subset — the embedded catalog is the fallback floor). The
fetch is keyless and never blocks startup. Providers without a live lister (OpenAI)
show their curated embedded catalog as before.

> Network note: a keyed OpenRouter `Build` makes **one best-effort background
> outbound request** to `https://openrouter.ai/api/v1/models` to populate the live
> picker. It is unauthenticated (no key on the wire) and fail-safe — offline, blocked,
> or any error simply leaves the embedded curated subset in place.

#### The `mecatui` model picker + client-side last-used persistence

`mecatui` surfaces the wire selection as the **`/models`** palette command (gated on
`caps.model_selection`): a flat, **type-to-filter** picker — each row tagged with
its `provider_id` (`provider · name`), filtered live by a case-insensitive
substring over `provider_id`/`id`/display name, scrolled in a window clipped to the
terminal height that follows the cursor (`↑`/`↓` to move, `pgup`/`pgdown` to page,
`home`/`end` to jump, `enter` to select; `esc` clears a non-empty filter, then
closes) that **persists** your choice and applies it to the **next**
session — the provider is fixed per session, so a pick takes effect on the next
`CreateSession`, not the live run. The selection is stored **client-side** in
`$XDG_STATE_HOME/mecatui/models.yaml` (fallback `~/.local/state/mecatui/models.yaml`)
as a per-workspace map (realpath-keyed). A pick is scoped to its own workspace; an
unseen/new repo falls back to the server default rather than inheriting another repo's
pick. This is machine-written **state** under `XDG_STATE_HOME`
(a sibling of the human config, mirroring `trust.yaml`'s settings-vs-state split). On
launch the persisted selection is **reconciled** against `ListModels` BEFORE the
first `CreateSession`: if its provider is no longer available (a removed key), it
falls back to the server default for that run with a loud notice — the state file is
left intact (the preference returns next launch), and connect never hard-fails with
`InvalidArgument`. The reconcile is **provider-level only** (issue #41): a saved
model missing from the (possibly embedded-floor, pre-live-refresh) snapshot is sent
anyway — the server validates the model string verbatim. A server-**rejected** saved
selection retries once on the server default and surfaces a **loud warning** naming
the rejected model, never a silent downgrade; the state file is not rewritten.

> **`--model` vs the picker.** For the embedded `mecatui` server, `--model` is the
> server's **default** model (what it resolves when the client sends no `model_id`);
> the `/models` picker is the **client's** per-session selector layered on top. For an
> external `--server`, the server owns its provider config — `--model` is only a header
> display hint, and the picker's selection rides the wire as `provider_id`/`model_id`.

### The loopback / unauthenticated trust note

On startup `mecated` logs the trust posture for each listen address (the
`authenticated` field reflects whether a bearer token and/or TLS is configured):

```
level=INFO msg="API bound to loopback (single-user localhost trust model)" flag=grpc-addr addr=127.0.0.1:8080 authenticated=false
level=INFO msg="API bound to loopback (single-user localhost trust model)" flag=http-addr addr=127.0.0.1:8081 authenticated=false
```

A **non-loopback** address bound **with** authentication (`--auth-token` and/or
TLS) logs at INFO (`API bound to a non-loopback address WITH authentication
(bearer token and/or TLS)`). Binding one with **no** authentication gets a
prominent warning instead — it never hard-fails, since an operator may
legitimately front the server with a service mesh:

```
level=WARN msg="API bound to a NON-loopback address with NO authentication: it exposes UNAUTHENTICATED command/file execution to the network — set --auth-token / --tls-cert (or front it with a trusted mesh) before doing this" flag=http-addr addr=0.0.0.0:8081
```

### The skills directory trust note

Skills are an **operator-trust boundary**, the same trust class as the
`AGENTS.md` / `CLAUDE.md` instruction files. Every discovered skill's one-line
description is injected into the model's context on **every** request (it lives in
the `Skill` tool spec), and a skill's full body flows into context the moment the
model **activates** it. Both are model-steering instructions, not sandboxed data:
a malicious or careless skill can redirect the agent just as a tampered
`CLAUDE.md` could.

Where skills are loaded from, in **precedence order** (highest first):

1. **`--skills-dir`** (repeatable) — explicit, operator-configured directories.
2. **Project-level** (only with `--skills-conventional`): `<workspace>/.mecatl/skills`
   then `<workspace>/.claude/skills`.
3. **User-level** (only with `--skills-conventional`): `$XDG_CONFIG_HOME/mecatl/skills`
   (or `~/.config/mecatl/skills`) then `~/.claude/skills`.

A higher-precedence skill **shadows** a same-named lower-precedence one (the drop
is logged). The `.claude/skills` and user-home paths exist for Claude Code
compatibility — they are convenient, but they **widen the surface** through which
an untrusted `SKILL.md` body can enter model context. They are therefore the same
trust class as `AGENTS.md`/`CLAUDE.md`: only enable `--skills-conventional` when
**every** one of those locations is yours to trust, and treat third-party skills as
code to review — read the `SKILL.md` before adding it, exactly as you would a CI
script.

Skills are **strict opt-in**: with no `--skills-dir` and `--skills-conventional`
unset, the `Skill` tool is never registered and nothing is read — that is why the
conventional set defaults OFF rather than auto-discovering. The always-in-context
description cap and the on-activation body truncation apply to **every** source,
including the conventional ones. (An OS-level sandbox around tool execution remains
future work — see the deferral note in the architecture doc.)

A **discovered** skill's own directory (the one holding its `SKILL.md`) also
becomes **read-visible** to the model — `Read` accepts that directory's absolute
path even when it lies outside the workspace (e.g. `~/.claude/skills/<name>`), so
an activated skill's bundled `references/`, `scripts/`, and `assets/` files are
actually reachable (the `Skill` tool's result names the base directory). This is
**read-only and per-skill**: `Write`/`Edit` still refuse those paths, `Glob`/`Grep`
never enumerate them, a shadowed skill's directory or a sibling under a skills
source never becomes readable, an untrusted workspace's project-tier skills are
never discovered and therefore never readable, and the `SkillDraft` quarantine dir
is never a source so it can never enter the read allowlist. Bundled scripts run
via Bash by absolute path, under the same permission gates Bash always has.

### File-based permission config (`.mecatl/settings.yaml`, issue #13)

The built-in permission policy (read-only tools allowed; `Bash`/`Edit`/`Write`/
`Team`/`SkillDraft` ask) can be tuned per project and per user with config files,
**re-resolved per session** against each session's workspace root by the
`internal/adapter/permconfig` resolver. So two sessions running in different repos
under the same `mecated` get **different** decisions for the same tool call.

**Schema** — `.mecatl/settings.yaml` (the checked-in, shared file),
`.mecatl/settings.local.yaml` (a gitignored personal override at a higher scope),
and the user-global file all share the same shape, mirroring Claude-Code's
permissions:

```yaml
permissions:
  allow:
    - "Bash(go test:*)"   # Claude "prefix:*" form, normalised to the glob "go test*"
    - "Bash(go build*)"   # native mecatl glob
    - "Read"              # bare tool name = tool-wide
  ask:
    - "Bash(git push:*)"
  deny:
    - "Bash(rm:*)"        # deny wins absolutely, in any scope
  subagent:               # child-scoped rules (issue #32): bind ONLY subagent/member/branch engines
    allow:
      - "Bash(go generate:*)"   # clears a child's substitution-floored ask (see "Compound-Bash & substitution safety" below) ONLY when the $(...) inners are read-only
    ask:
      - "Bash(go test:*)"       # a configured child Ask is NEVER auto-approved by isolation
    deny:
      - "Bash(curl:*)"
```

Each entry is a rule spec `Tool(pattern)` or bare `Tool`. Patterns use the
evaluator's glob grammar; the Claude `prefix:*` / `prefix:` form is normalised to a
`prefix*` glob. Config rules use **glob** semantics (`Exact:false`) — only LEARNED
"allow always" rules are exact. **The `permissions:` subtree parses STRICTLY**: an
unknown key inside it (`alow:`, `subagnet:`, …) is a loud parse error and the file
is skipped (logged), never silently-ignored config; the file's top level stays
lenient (`trustedWorkspaces:` etc. keep parsing).

**Audience × effect** — which engine class each bucket binds.

**Baseline first:** child engines (Subagent children, team members, Parallel
branches) default to **allow-all** — everything runs except substitution-floored
commands (see *Compound-Bash & substitution safety* below) and anything a
configured deny/ask gates. So `subagent: allow` is NOT "let children run X" —
children already run X; it matters ONLY for *clearing a child's
substitution-floored ask*. `subagent: deny` / `subagent: ask` *tighten* (block or
gate a child command the floor would otherwise allow).

| Bucket | Main engine | Subagents (Subagent children / team members / Parallel branches) |
| --- | --- | --- |
| top-level `deny` | yes | **yes** (a deny only tightens — it binds everywhere) |
| top-level `allow` / `ask` | yes | no (children are already allow-all; see baseline above) |
| `subagent: allow` | no | yes — clears a child's **substitution-floored** ask (see below) when the hidden inners are read-only |
| `subagent: ask` | no | yes — gates a child command; a configured child Ask is never auto-cleared (it surfaces to the human, or auto-denies headless) |
| `subagent: deny` | no | yes |

The `subagent: allow` clearing is **bounded** (it relaxes the substitution floor
without trusting what a substitution hides): the allow vouches **only for the
OUTER command** — every command hidden inside `$(...)`/backticks must
independently classify **positively read-only** (`go test $(git rev-parse HEAD)`
clears; `go test $(anything-else)` surfaces/denies), and the outer must pass the
worktree-escape rejections (no `git push/config/remote/fetch/pull/clone/worktree/submodule`,
no path-bearing `git -C`/`--git-dir`/`--work-tree`, no `go … -exec/-toolexec/-overlay/-o`)
as defense-in-depth.

Child engines resolve **project** rules against their **session's workspace
root** (per-session engines re-pin at session-engine assembly; the shared
default engine pins the server root it was built for) — never against their
forked worktree/copy roots (a worktree lacks the gitignored
`settings.local.yaml`, and per-fork resolution would defeat the cache). User/CLI
rules apply to children as usual. A typo inside the `permissions:` subtree skips
the whole file (deny/ask included) — the WARN names the lost per-effect rule
counts. Under `--yolo` the allow-all **rule** binds children too (so the main and
child rulesets stay in lock-step), but the substitution-floor **loosening**
remains **main-only**: it never loosens a child's substitution floor — a child's
`$(...)`/backtick/heredoc command still resolves through the child-ask model.

**Headless ask review (`--headless` + `--subagent-ask-reviewer`).** On a
**headless** run (no human approver — mecated started with `--headless`), a child
ask that nothing above resolved is normally **blanket auto-denied**. The opt-in
reviewer inserts an automated step before that deny: a tool-less, one-turn LLM
reviewer (one extra LLM call on the reviewer model) examines the command — fenced
as untrusted data, with claims of prior approval inside it declared void, and the
verdict accepted only when the reviewer's *whole* reply is a single
`{"allow":…}` object so a forged verdict echoed inside the command can't be lifted
out — against the built-in read-only/verification rubric (or your
`--subagent-ask-reviewer-policy` file) and either approves **this call only** or
keeps it denied; any reviewer error/timeout/ambiguity also keeps it denied
(**fail-safe** — the reviewer is never load-bearing for safety), and a per-run
circuit breaker (`--subagent-ask-reviewer-max-denies`) bounds reviewer spend.

**Reachability:** the reviewer fires **only** when there is no human to ask — i.e.
under `--headless`. A normal interactive mecated (the default) **and the `mecatui`
embedded server** (which is always interactive — a human sits at its approval
modal) surface every unresolved child ask to the client/modal for a person to
answer, so a reviewer configured there is inert and the server logs a startup
WARNING to that effect. To actually use the reviewer, run `mecated --headless
--subagent-ask-reviewer …` (and point `mecatui --server` at it if you want the
TUI). The flag's model is still validated at startup even when inert, so a typo
is caught immediately rather than the day `--headless` is added.

Two hard bounds keep it subordinate to this section's rules: a **configured
`subagent: ask` is never delegated to the reviewer** (a deliberately-configured
Ask demands a *human* approver — it surfaces interactively or auto-denies
headless, exactly as the table above says), and a configured `deny` resolves
before any ask exists. It is deliberately a **server flag, NOT a `permissions:`
config key**: it grants an autonomous approval capability, which must be an
operator *deployment* decision — a (project-tier, possibly checked-in) settings
file must never be able to switch on a mechanism that approves commands by itself.

**Scope → location** (highest precedence first; see `engine/governance` Scope):

| Scope | Location | Trust |
| --- | --- | --- |
| `ScopeCLI` | each `--permission-config <file>` | fully trusted |
| `ScopeLocalProject` | `<workspace>/.mecatl/settings.local.yaml` (gitignored, personal); `<workspace>/.claude/settings.local.json` with `--import-claude-permissions` | **trust-gated** |
| `ScopeSharedProject` | `<workspace>/.mecatl/settings.yaml` (checked-in, shared); `<workspace>/.claude/settings.json` with `--import-claude-permissions` | **trust-gated** |
| `ScopeUser` | `$XDG_CONFIG_HOME/mecatl/settings.yaml` (or `~/.config/...`); `~/.claude/settings.json` with `--import-claude-permissions` | fully trusted |
| `ScopeBuiltinDefault` | the built-in floor (read-allow / mutate-ask) | n/a — lowest precedence |

The resolver re-reads project files **per session** against the session's workspace
root, and **revalidates** its per-root cache on the config files' mtime/size — so a
`deny` added mid-process takes effect on the next call, not at restart. Among
ask-vs-allow the configured **higher scope wins**, with ONE narrow exception: a
higher-scope config **Allow loosens ONLY the built-in `ScopeBuiltinDefault` Ask**
floor (e.g. allowing `Bash(go test:*)` relaxes the built-in Bash ask). It can never
suppress a **configured** Ask, **deny/ask in any scope still beats an allow**, and a
learned allow can never out-rank a configured deny/ask. Plan mode still hard-denies
mutations first. Config files are size- and rule-count-capped (defense-in-depth).

**The trust gate** — a project's config is part of the repo the model is editing.
Its **DENY and ASK** rules are **always** honoured (they only tighten). Its
**ALLOW** rules (shared AND local, **including `subagent:` allows**) are honoured
**only with `--trust-project`**; otherwise they are dropped (and logged) so a
checked-in `settings.yaml` cannot auto-approve tool calls in an untrusted repo —
for the main engine or its children. User-global and `--permission-config`
(CLI) files are the operator's own and are always fully trusted.

**Memory + soul are pre-approved at the floor** (issue #14) — the six memory tools
(`Remember`/`Recall`/`SearchMemory` and the cross-project `RememberUser`/`RecallUser`/
`SearchUserModel`) and the synthetic `soul:apply` action are explicit
`ScopeBuiltinDefault` Allows in the built-in ruleset, so by default they **do not
prompt**: an agent recalling and saving its own facts, and applying the operator's
soul, is part of "having a memory/identity", not a workspace mutation. They are
explicit (auditable in source + the `... ENABLED ...; permission: allow (built-in
default, overridable ...)` startup logs) and **overridable** — being the lowest scope,
any higher-scope config Ask/Deny wins. To require approval (or block) one, add it to a
`settings.yaml`:

```yaml
permissions:
  ask:
    - "Remember"     # require approval before the agent writes project memory
  deny:
    - "soul:apply"   # withhold the soul entirely this deployment
```

`soul:apply` is consulted at **soul-load (build time)**, not per tool call: `allow`
applies the soul, `deny` withholds it, and `ask` also **withholds** it (with a warning)
because there is no interactive gate at build time — set it back to `allow` to apply.

**Claude import is lossy** (`--import-claude-permissions`) — every lossy outcome is
logged:

| Claude spec | Outcome |
| --- | --- |
| `WebFetch(domain:x)` in an **allow** list | **demoted to `ask`** (domain/substring match is too risky to auto-allow) |
| bare `WebSearch` in an **allow** list | imported **verbatim** as `allow` — **no demotion** (its outbound payload is a query string, lower-risk than `WebFetch`'s arbitrary-URL fetch; egress is already provider-gated by `--websearch-url`) |
| `Read(~/...)` (leading `~`) | kept but **inert** — the `~` is left unexpanded, so it never matches the absolute path a tool resolves |
| unparseable spec | **dropped** |

The import never widens: a demotion only ever moves `allow → ask`, and the
`deny`/`ask` buckets import verbatim.

> **`mecatui` defaults match `mecated`.** The embedded TUI server sets
> `--permissions-conventional` and `--import-claude-permissions` ON, but
> `--trust-project` is **OFF by default** (WORKSPACE-TRUST Phase 0) — unified with
> `mecated`. So a project's ALLOW rules and its project soul are honoured only when
> you pass `--trust-project` to `mecatui`; deny/ask are always honoured regardless.
> (Earlier builds hardcoded trust ON for the TUI; that blanket-trust regression is
> gone.) The `mecated` daemon likewise defaults `--permissions-conventional` ON but
> `--trust-project` OFF (the safe stance); it defaults `--import-claude-permissions`
> OFF (the safe network stance).

### Guardrails — LLM-backed tool-content inspection (`guardrails:`, issue #27)

Guardrails inspect the data crossing the agent's tool boundary with a **separate,
tool-less checker model** and enforce a verdict — the *dual-LLM quarantine*. They
catch **outbound exfiltration** (a secret in `PreToolUse` args) and **inbound prompt
injection** (instruction-like content in a `PostToolUse` result). **OFF until a
checker model is configured** — configuring a model is the opt-in to spend. Full
rationale + threat model: `docs/adr/0021-guardrails.md`.

**The minimal config is just a model.** With `--guardrails-model X` (and no rule
list) guardrails are ON with the **default advisory rule set** — observe-only for the
network/MCP surfaces, off for local tools:

| Tool matcher | Phases | Mode |
| --- | --- | --- |
| `WebSearch` | pre + post | advisory |
| `WebFetch` | post | advisory |
| `mcp__*` | pre + post | advisory |

Advisory = observe-only: a finding is an **operator-log diagnostic** (carrying the
session id + tool-call id + a `guardrail-finding` marker so you can correlate it back
to the conversation); the call/result is byte-unchanged and the client/model see
nothing. Measure the false-positive rate, then promote a rule to `block`/`sanitize`.

**Operator-tier ONLY.** The `guardrails:` config is read from the **user-global**
`settings.yaml` + the CLI — **never** the project-tier file. This inverts the usual
tighten-only project gate: a project repo disabling or weakening a security checker
is a *downgrade*, so a project-tier `guardrails:` block is **ignored with a WARN**.
The subtree is parsed **strictly** (an unknown sub-key is an error, like
`permissions:`) so a typo cannot silently disable a guardrail. Set the checker model
with `--guardrails-model` (overrides the YAML `model:`); force off with
`--guardrails=off`.

```yaml
# ~/.config/mecatl/settings.yaml  (user-global only — NOT a checked-in project file)
guardrails:
  model: gpt-5-mini          # the checker model (or a --model-alias). With NO rules below,
                             # the default advisory set applies (the model is the opt-in).
  maxChecks: 50              # per-session checker-call cap — bounded SEPARATELY from
                             # --max-run-tokens so infra spend can't starve the agent.
                             # OMITTING maxChecks = NO cap (but the DEFAULT rule set, used when
                             # you set only a model, gets a default cap of 200 so it can't surprise-bill).
  minContentBytes: 16        # skip a short INBOUND (post) result (cost guard; omit = check every post).
                             # Outbound (pre) args are ALWAYS inspected — a short exfil arg is the point.
  rules:                     # an explicit list REPLACES the default advisory set
    - match: "WebFetch"      # inbound injection on fetched pages
      phases: ["post"]       # "pre" = outbound args, "post" = inbound result; omit = BOTH
      mode: block            # block | sanitize | advisory
    - match: "mcp__*"        # all MCP tools, both directions
      mode: advisory         # observe-only first; tune to block/sanitize later
    - match: "Bash"          # outbound exfil in shell args
      phases: ["pre"]
      mode: sanitize         # rewrite the args to the checker's sanitized form
      failClosed: true       # a checker outage treats the content as UNSAFE (default is fail-OPEN)
```

- **Matcher** keys on the tool **name** only (exact > `prefix*` > `*`, most-specific
  wins; a tie favours the earlier rule). A tool with no matching rule is unchecked.
- **`block`** vetoes a `PreToolUse` call; on `PostToolUse` — where a Block is **inert**
  (the tool already ran) — it **rewrites the result to a model-visible error**, so the
  model and client both see the block and the raw injected result never reaches either.
- **`sanitize`** rewrites the args (`pre`) / result (`post`) to the checker's
  `sanitized_content` — a Post rewrite carries a `[guardrail: redacted unsafe content]`
  marker so the model knows it was edited. **Sanitize trusts the checker's output**
  (a compromised checker could rewrite content): use it only with a trusted checker
  model; an unsafe verdict with no/oversized/invalid rewrite falls back to a block.
- **`advisory`** only logs an operator diagnostic (correlatable; client/model see nothing).
- **Fail-open by default** (a checker error/oversized content degrades to "no checker"
  with a WARN; a sustained outage escalates to a one-time **"checker DOWN"** sticky WARN);
  **`failClosed: true`** treats a checker error as unsafe. A checker **saying safe always passes**.
- Guardrails fire on the **main loop** regardless of `--headless` (unlike the
  `--subagent-ask-reviewer`, which is headless-only). The checker engine runs
  tool-less with inert hooks and no nested reviewer — it can never re-trigger a
  guardrail or call a tool.

#### The operator-global `posture:` setting

Like `guardrails:`, the posture ladder (above) can be set once in the **user-global**
`settings.yaml` instead of on every invocation, via an optional top-level `posture:`
string:

```yaml
# ~/.config/mecatl/settings.yaml  (user-global only)
posture: auto          # strict | trusted | auto | yolo
```

**Operator-tier ONLY** — read from the user-global `settings.yaml` + the CLI,
**never** the project-tier file (the same inversion as `guardrails:`). A malicious
repo dropping `.mecatl/settings.yaml` with `posture: yolo` must never be honoured, so
a **project-tier `posture:` is ignored with a WARN** (security-critical fail-closed).
A `--posture` flag (or its `--yolo`/`--trust-project` aliases) **out-ranks** the YAML
value; an unknown value fails closed to `strict` with a WARN.

### Per-slot models (`models:`, ADR 0030)

#### Quickstart: pick a model per job

Model selection is a stack of independent mechanisms. Pick the one(s) you need:

| You want… | Use |
|---|---|
| Short names for models you reference often | `models.aliases:` |
| Cheaper compaction / guardrail / ask-reviewer calls | `models.slots:` (`compaction`/`guardrail`/`ask-reviewer`) |
| Plan on a strong model, execute on a cheaper one | `models.slots: plan:` (the opusplan pattern) |
| Pick a subagent's model per task automatically | `--subagent-model-router` + `models.router:` |
| Let a trusted repo re-bind models within your cap | `models.allowlist:` + a project `.mecatl/settings.yaml` |

A complete tiered setup on one provider (here, OpenRouter — model selection only
swaps the model within a session's provider, never the provider itself):

```yaml
# ~/.config/mecatl/settings.yaml
models:
  aliases:                 # short names → concrete ids (the spine everything else references)
    heavy: z-ai/glm-5.2
    coder: deepseek/deepseek-v4-flash
    quick: google/gemini-3.5-flash
  default: z-ai/glm-5.2    # the session model
  slots:                   # route housekeeping + plan-mode to a cheaper/stronger model
    compaction: quick
    guardrail: quick
    ask-reviewer: quick
    plan: heavy            # plan-mode turns swap to the heavy model
    router: quick          # the classifier itself
  router:                  # pick a subagent's model per task (needs --subagent-model-router)
    default-category: medium
    categories:
      - name: large
        description: deep multi-step reasoning, architecture, subtle bugs
        model: heavy
      - name: medium
        description: standard implementation, bug fixes, writing and reviewing tests
        model: coder
      - name: small
        description: trivial mechanical tasks, single-file edits, quick lookups
        model: quick
```

Launch with `mecated --subagent-model-router` (the flag is the router's enable gate).
With nothing configured, every call keeps the session model — the default is
byte-identical.

**Verifying it's wired.** On startup mecated logs one build-once fact per active slot
(`model slot ACTIVE`) and, when the router is enabled, `subagent model router ACTIVE`.
Check the mecated log (stderr, or `$XDG_STATE_HOME/mecatl/mecatui.log` under mecatui)
for those lines. A slot that failed to resolve WARNs and degrades to the session model,
so a missing `ACTIVE` line is the signal something didn't bind.

#### How it works

The internal **lightweight** LLM calls — the compaction summary, the headless
ask-reviewer, and the guardrail checker — can run on a **cheaper model** than the
session via a **model slot** (the `--model-slot` flag, above, or the user-global
`settings.yaml` `models:` subtree). A slot binds a named call to a model **selector**
(an alias or a concrete id), resolved through the alias map. The byte-identical
default holds: with no slot configured every call keeps the session model.

```yaml
# ~/.config/mecatl/settings.yaml  (user-global only — NOT a checked-in project file)
models:
  aliases:                   # the alias spine (same map as --model-alias; CLI wins per key)
    cheap: gpt-4o-mini
    reasoning: gpt-5
  slots:                     # bind a slot (or a tier) to a selector
    compaction: cheap        # the compaction tier-4 summary call
    ask-reviewer: cheap      # the headless child-ask reviewer (issue #31)
    guardrail: cheap         # the LLM content checker (issue #27)
    plan: reasoning          # plan-mode turns run on the reasoning model (opusplan)
    router: cheap            # the subagent model-router classifier (ADR 0031)
    # cheap: gpt-4o-mini     # a TIER key gives a default a slot falls through to
```

- **Slots** route the internal-call slots `compaction`, `ask-reviewer`, `guardrail`,
  `router`, **plus** `plan` (the mode axis, below). A **tier** key
  (`cheap`/`fast`/`reasoning`) is the default a slot with no explicit binding falls
  through to — the internal-call slots default to `cheap`, while **`plan` defaults
  to `reasoning`** (a plan model is a strong-reasoning model, not a cheap one).
- **The `plan` slot (the opusplan workflow).** Bind `plan` to a strong-reasoning model
  and a session **automatically swaps to it while in plan mode** and back to the session
  model when executing — re-resolved **between turns** at the run-entry seam (never
  mid-turn; the model is fixed per turn), within the **same provider**. E.g.
  `--model-slot plan=reasoning --model-alias reasoning=anthropic/claude-opus-4.5` runs
  planning on Opus and execution on the session model. With no `plan` slot a mode flip
  changes nothing (**byte-identical**). The `resolved_model` echo re-emits the new model
  on the next `GetSession`/turn after the switch.
- **Resolution choke point** is `resolveSlotModel`: explicit slot binding > the slot's
  default tier > the session model. The selector is resolved through the **same alias
  machinery** as `--model-alias` / an agent def's `model:`.
- For the **compaction** slot, ONLY the summary LLM call's model changes — the
  session's own model, token counter, prompt, and context window stay put. For
  **ask-reviewer** / **guardrail** the slot **supersedes the model** of
  `--subagent-ask-reviewer` / `--guardrails-model`, but those flags stay the **on/off
  gate** (a slot alone never enables them).
- **Fail-soft**: a typo'd slot key or an alias that means *inherit* WARNs and degrades
  to the session model — a broken housekeeping slot never wedges the call.
- **Operator-tier by default, project-overridable within an allowlist (Phase 4).** The
  `models:` mapping is parsed **strictly** (an unknown top key like `slotz:` errors).
  `--model-slot`/`--model-alias` out-rank the YAML per key. By default a project-tier
  `models:` block is **ignored with a WARN** — UNLESS the operator opts in with an
  allowlist (next subsection). Team synthesis is a later ADR-0030 layer, not yet wired;
  the subagent **router** shipped in Phase 5 (below).

#### The subagent model router (`models.router:`, ADR 0031)

The **OPT-IN semantic model router** picks which model a `Subagent` delegation runs on,
**per task**, from a category menu you define. Turn it on with the
`--subagent-model-router` flag (the enable gate); define the taxonomy in the
**operator-tier** `models.router:` subtree:

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier ONLY — a project-tier router: is stripped with a WARN)
models:
  aliases:
    cheap: gpt-4o-mini
    big: anthropic/claude-opus-4.5
  slots:
    router: cheap            # the CLASSIFIER itself runs on this slot (default: cheap tier)
  router:
    classifier-slot: cheap   # optional; overrides the `router` slot for the classifier model
    default-category: small  # what the classifier picks when none clearly fits
    categories:
      - name: small
        description: trivial, mechanical, single-file edits; quick lookups; renames
        model: cheap
      - name: large
        description: deep multi-step reasoning, architecture, subtle concurrency bugs
        model: big
```

- **Give categories CLEAR, DISTINCT descriptions** — the description is the classifier's
  ONLY signal. Vague or overlapping descriptions make routing unreliable (and it
  fail-softs to the default model on a miss, so the win is simply lost).
- **The classifier** is a tiny one-turn call on the `router` slot (or `classifier-slot`),
  reusing the hardened single-JSON-verdict parse; the task prompt is fenced as untrusted.
- **Precedence** (the router fills the gap, never overrides): an explicit per-call
  `model`, a named `agent`, a `fork`, or a `resume` already pins the engine → the router
  does NOT fire. Otherwise: per-call `model` > agent-def `Model` > fork/resume > **router**
  > inherited default.
- **Per-category `model`** is an alias / slot / concrete id, resolved through the same
  alias map (operator targets are **uncapped** — the operator is authoritative).
- **Fail-soft + breaker**: any classifier failure, an unknown/hallucinated category, or
  an unresolvable target → the inherited default model; a per-run breaker (3 consecutive
  misses) skips the classifier for the rest of the run. **OFF (no flag / no taxonomy) is
  byte-identical** to no router.
- It runs in **both** interactive and headless deployments, and the gRPC `RunTeam`-direct
  path is excluded (zero-caps). See [ADR 0031](adr/0031-subagent-model-router.md).
- **Cost note (CWE-770):** an untrusted/peer-injected task prompt can **steer** the
  classifier toward your most-expensive category (the breaker only counts *misses*, not
  steered-but-valid classifications). It is **bounded** — the router can only pick from
  *your* taxonomy, the provider is fixed, and **`--max-run-tokens`** (plus
  `--max-team-tokens` and the per-call `max_run_tokens`) is the actual spend ceiling. The
  budget caps a routed child regardless of the chosen model **and** (since #92) folds each
  classifier call's own token spend into the parent run's cumulative `--max-run-tokens`, so
  repeated classifications cannot run up unbounded classifier cost either. Keep the category
  cost range modest and rely on the token budget as the hard ceiling.

#### Authoring skills & agent definitions for model selection

The router steers a delegation's model from the **operator's** config; a skill or
agent definition does not need to — and should not — name provider-specific model
ids. Two ways a delegation's model is chosen, and how to author for each:

- **Let the router pick (preferred).** Write the skill to describe the *capability*
  needed ("on a high-reasoning model") rather than a slug. A plain `Subagent`
  delegation with no `model`/`agent`/`fork`/`resume` is classified by the router
  into your category taxonomy, so the operator's `models.router:` config — not the
  skill text — decides the model. This keeps the skill provider-agnostic and
  portable across deployments.
- **Pin via an alias.** If a skill needs to force a tier, it can pass
  `Subagent(model="<alias>")`, where `<alias>` is a name the operator bound in
  `models.aliases:`. The alias is the only model vocabulary the model can usefully
  reference; mecatl does **not** inject the alias map into the prompt, so the skill
  must name the alias itself. Note this bypasses the router (an explicit per-call
  `model` wins by precedence), and the alias only resolves to a real model if the
  operator bound it — an unbound alias fail-softs to the inherited default.

The built-in aliases `sonnet`/`opus`/`haiku` default to "inherit the parent model"
until an operator overrides them, so a skill that names them is portable but inert
until configured. For a clean deployment-neutral posture, describe capabilities and
let the router own the mapping.

#### Project-overridable model config, capped by an operator allowlist (Phase 4)

A **trusted** project's `.mecatl/settings.yaml` may re-bind `models.default` /
`models.slots` / `models.aliases` — but only to entries the operator **allowlisted**. The
operator declares the cap in the **user-global** `settings.yaml`:

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier — the cap and the operator's own bindings)
models:
  allowlist:                 # the NON-WIDEABLE cap: alias names and/or concrete ids
    - reasoning
    - anthropic/claude-opus-4.5
  aliases:
    reasoning: gpt-5
  default: gpt-5             # the operator's session default (uncapped — operator is authoritative)
```

```yaml
# <repo>/.mecatl/settings.yaml  (project-tier — honoured ONLY within the allowlist, on a trusted repo)
models:
  default: anthropic/claude-opus-4.5   # accepted (allowlisted)
  slots:
    plan: reasoning                    # accepted (alias resolves to gpt-5, allowlisted)
    compaction: some-unvetted-model    # DROPPED with a WARN (not in the allowlist)
```

- **Opt-in by allowlist.** With **no** operator `models.allowlist`, a project `models:`
  block stays WARN-ignored — **byte-identical** to before Phase 4.
- **The allowlist is operator-tier and non-wideable.** A project-tier `models.allowlist:`
  key is always **ignored with a WARN** (a project cannot widen its own cap).
- **Trust-gated.** An **untrusted** workspace's project `models:` block is ignored (the
  same `--trust-project` / `trustedWorkspaces:` gate as a project's allow rules).
- **Resolve-then-check.** Each project binding's value is resolved to a concrete id and
  tested for membership in the (alias-resolved) allowlist set; an allowed binding is
  applied, an out-of-cap one is dropped with a build-once WARN (keeping the
  operator/default value). The cap applies to **every** config-file binding — the session
  `default`, all slots (including `plan`), and aliases.
- **Precedence:** `CLI (--model/--model-slot/--model-alias) > project-YAML (capped) >
  operator-YAML (settings.yaml) > built-in`. A project binding overrides the operator-YAML
  value for the same key, but an explicit operator **CLI flag** for a key still wins (a
  deliberate per-run override). This holds for the session `default` too: an operator
  `models.default:` re-binds the session default over the registry default (the operator's
  own default is **uncapped** — the allowlist caps project bindings only), a capped project
  `default:` can override it, and a CLI `--model` beats both.
- **The allowlist is a flat set, not per-slot.** A model you allowlist may be bound by a
  trusted project to **any** slot — including the `guardrail` and `ask-reviewer` **safety
  checkers**, not just a cheap session default. This stays within the trust you declared
  (the operator approved the model), but it is coarser than "approved models" might
  suggest: **do not allowlist a model you would be unwilling to see used as a safety
  checker.** Per-slot allowlist scoping is a deliberate future follow-up, not a current
  knob.
- **Out of scope (this slice):** the allowlist caps **config-file** bindings only — an
  agent-def `model:` literal and the per-session API `model_id` selector are not capped
  here.

### Declarative workspace trust (`trustedWorkspaces:`, WORKSPACE-TRUST Phase 1)

`--trust-project` is a **per-invocation** flag. For CI, a daemon, or a power-user
who works repeatedly in a known-good checkout, declaring the trust once is more
ergonomic than passing the flag every run. The **user-global** `settings.yaml`
(`$XDG_CONFIG_HOME/mecatl/settings.yaml`, or `~/.config/mecatl/settings.yaml`)
gains an optional top-level `trustedWorkspaces:` list of **absolute workspace
paths** to pre-trust:

```yaml
# ~/.config/mecatl/settings.yaml  (the operator's own, fully-trusted file)
trustedWorkspaces:
  - /home/me/src/my-project
  - /home/me/work/known-good-repo

permissions:            # the same file also carries user-scoped permission rules
  deny:
    - "Bash(curl:*)"
```

When the current workspace's path matches a declared entry, it is trusted exactly
as `--trust-project` would trust it — **the same admission gate, no separate
path**: the project's ALLOW rules and its project soul are honoured. This is
**read-only**: mecatl only ever *reads* `trustedWorkspaces:` from your
human-authored `settings.yaml`; it never writes it (the machine-written trust
registry is a **separate** file — see the next section). Both `mecated` and
`mecatui` honour it (it lives in the shared user config).

**Precedence and semantics:**

- The effective trust is `--trust-project` **OR** a `trustedWorkspaces` match.
  The flag wins as the *source label* when both apply; either way the workspace
  is trusted.
- **Path keying is symlink-safe.** Both the declared entries and the current
  workspace are compared on their **cleaned, absolute, symlink-resolved**
  (`realpath`) form, so a moved or symlinked path cannot forge or inherit another
  workspace's trust. A declared entry that does not resolve (typo / broken
  symlink) is ignored; the rest of the list is honoured.
- **Monotonic-positive.** `trustedWorkspaces` only ever **grants** trust. It can
  never override a **Deny** or a configured **Ask** anywhere — those tighten and
  are always honoured. Trust gates only whether a project's *ALLOW* rules (and
  its soul) are admitted, never the deny-dominant evaluation.
- **Fail-safe.** A missing key, a malformed entry, or an unparseable
  `settings.yaml` resolves to **untrusted** (a corrupt config never *grants*
  trust); it is logged, never an error that aborts startup. The composition logs
  the decision: `workspace trust trusted=… source=flag|declared|none`.

### Remembered trust + drift (`trust.yaml`, WORKSPACE-TRUST Phase 2b)

Beyond the human-authored `trustedWorkspaces:` list, mecatl keeps a
**machine-written** trust registry at
`$XDG_CONFIG_HOME/mecatl/trust.yaml` (fallback `~/.config/mecatl/trust.yaml`).
It is a **sibling of, but never inside,** the human `settings.yaml` (the
settings-vs-state split): you edit `settings.yaml`; only the harness writes
`trust.yaml`. Each entry remembers a trusted workspace (keyed by its
`realpath`) **and** the **identity-anchor hash** captured at the moment of
trust:

```yaml
# ~/.config/mecatl/trust.yaml   (machine-written; do not hand-edit)
version: 1
workspaces:
  /home/me/src/my-project:
    anchorSHA256: 9f2c…           # the project's identity surface at trust time
    trustedAt: 2026-06-04T12:00:00Z
```

A remembered entry is honoured as `source=remembered` (precedence **below**
`--trust-project` and a `trustedWorkspaces:` match) **only while its anchor still
matches** the workspace's live identity surface.

- **The identity anchor** is the high-signal, rarely-edited project authority
  surface: the project **soul** (`<ws>/.mecatl/soul.md`), and the **project-tier**
  **agent**, **slash-command**, and **skill** definitions under `<ws>/.mecatl/*`
  and `<ws>/.claude/*`. It is hashed deterministically (sorted file set, per-file
  content hashes folded).
- **`settings.yaml` is NOT in the anchor.** Editing your project's permission
  rules (which change on nearly every commit) does **not** trigger drift — that
  would nag-fatigue you into blind-clicking trust. Permission edits re-resolve
  live (permconfig's own mtime cache) without re-prompting. **Drift fires only
  when the project's persona / agents / commands / skills change.**
- **Drift fails safe.** If a remembered workspace's anchor no longer matches
  (the project's identity surface changed since you trusted it), the workspace is
  re-gated to **untrusted** for this run, logged at `WARN`
  (`workspace trust: identity anchor DRIFTED …`). `mecated` has no prompt, so a
  drifted entry never silently inherits the old grant; the interactive re-prompt
  that turns drift back into a fresh trust decision is the `mecatui` first-encounter
  prompt (see the next section). `--trust-project` always overrides (it
  short-circuits before the registry is even read).
- **`mecated` is read-only on the registry** — it *reads* `trust.yaml`
  declaratively (a remembered + undrifted workspace is trusted) but **never
  prompts and never writes** it. A repo trusted in `mecatui` for a given user is
  honoured by that same user's `mecated` (shared `$XDG_CONFIG_HOME`); cross-user
  is not shared (use that user's `trustedWorkspaces:`).
- **Security.** The registry is keyed by `realpath` (symlink-safe, symmetric on
  read and write); the write uses `O_NOFOLLOW` + `0o600` + temp-then-rename (a
  pre-planted symlink at the path is refused); and an unreadable / oversized /
  corrupt / wrong-version `trust.yaml` resolves to **untrusted** (a corrupt
  registry never *grants* trust). The path is derived solely from your user XDG
  config dir, never from a repo-controlled path — a repo cannot self-trust.

### The `mecatui` first-encounter trust prompt (WORKSPACE-TRUST Phase 2c)

`mecated` is purely declarative — it never asks. But when you launch **`mecatui`**
with its **embedded** server (the default: no `--server`, and no `mecated` already
running on the loopback default) in a workspace that is **not yet trusted** and
that carries a **project authority set** worth gating, `mecatui` prompts you once,
**before** the TUI takes over the screen:

```
mecatui: do you trust the project files in this workspace?
  /home/me/src/some-cloned-repo
Trusting honours this project's soul, agents, commands, skills, and ALLOW rules. Its deny/ask rules apply regardless.
[t]rust (persist) / [o]nce (this run only) / [n]o (default):
```

- **`t` (trust)** — trust this run **and remember it**: writes the workspace +
  its current identity-anchor hash to `trust.yaml`, so future launches (and your
  own `mecated`) trust it without asking, until the project's identity surface
  drifts.
- **`o` (once)** — trust **this run only**; nothing is persisted. Next launch asks
  again.
- **`n` / Enter / anything else** — **do not trust** (the safe default): the
  project's soul, agents, commands, skills, and ALLOW rules are withheld; the agent
  still runs with your user-tier config and the built-in tools.

**When the prompt fires.** Only when there is something a trust grant would
actually admit: a project soul (`<ws>/.mecatl/soul.md`), a project-tier
agent/command/skill definition, or a project `settings.yaml`/`settings.local.yaml`
carrying **ALLOW** rules. A repo with only deny/ask rules (which apply regardless)
or no project authority at all is **never** prompted — you are not nagged for a
workspace that has nothing to gate. A workspace already trusted (via
`--trust-project`, a `trustedWorkspaces:` match, or a remembered + undrifted
`trust.yaml` entry) is **not** prompted either.

**Drift is a re-prompt.** If you previously trusted a workspace and its identity
surface (soul / agents / commands / skills) has since **changed**, the prompt
re-fires with a "this workspace **CHANGED** since you trusted it" notice —
answering `t` re-persists the new anchor.

**Non-interactive = untrusted (fail-safe).** If `mecatui`'s stdin is **not a
terminal** (piped, redirected, headless), it **cannot** prompt — so it proceeds
**untrusted** for that run and prints a one-line note. It never blocks startup
waiting for input and never auto-trusts off a pipe. To trust non-interactively,
pass `--trust-project` or declare the workspace in `trustedWorkspaces:`.

**Security.** The echoed workspace path is **terminal-escape-sanitized** before
display, so a repo directory named with embedded ANSI/OSC escapes cannot corrupt
or spoof the prompt (CWE-150). The prompt and the registry write live in the
`mecatui` composition root, not the render layer.

### What an untrusted workspace withholds (WORKSPACE-TRUST Phase 2a)

Trust is **not** a kill-switch. An untrusted repo is still a fully usable coding
agent — it degrades to **"ask the human" mode**, never **"do nothing" mode**. The
line is drawn between the agent's **own capability** (never gated) and the repo's
**injected steering/authority** (gated).

**Always active on ANY repo — trusted or not (NEVER gated):**

- the built-in tools (Read, Edit, Bash, …) and the whole agent loop;
- the base system prompt;
- **your own user-tier config**: the user soul, and your user-level agent
  definitions, slash commands, and skills under `$XDG_CONFIG_HOME/mecatl/*` (or
  `~/.config/mecatl/*`) and `~/.claude/*`. A repo cannot touch these;
- **every Deny / Ask rule** from any scope (they only tighten);
- the permission prompt itself — on an untrusted repo the agent still runs; it
  just **asks** for the tool calls the repo would have auto-allowed.

**Withheld when the workspace is UNTRUSTED — the project-injected authority set:**

- the project's permission **ALLOW** rules (auto-approval the repo grants itself);
- the project **soul** (`<workspace>/.mecatl/soul.md` — a repo rewriting the
  agent's persona);
- the **project tier** of **agent definitions** (`<workspace>/.mecatl/agents`,
  `<workspace>/.claude/agents`), **slash commands** (`<workspace>/.mecatl/commands`,
  `<workspace>/.claude/commands`), and **skills** (`<workspace>/.mecatl/skills`,
  `<workspace>/.claude/skills`).

So a freshly-cloned, untrusted repo cannot silently steer the model with a
`.claude/agents/evil.md`, a malicious slash command, an injected skill, a
self-granted auto-approve, or a repo persona — but you can still read, edit, and
run-with-a-prompt in it from the first run. An explicit `--commands-dir`,
`--agents-dir`, or `--skills-dir` you pass is **operator-supplied** (not
repo-injected) and is honoured regardless of trust. Each withheld project-tier
source is logged at `WARN` so the degradation is visible. Trust the repo
(`--trust-project` or a `trustedWorkspaces:` entry) to admit its full project
authority set.

> The soul carries a **double gate**: it is admitted only if the workspace is
> trusted (provenance) **AND** the `soul:apply` permission resolves to Allow
> (policy) — a logical AND. An untrusted repo's soul is withheld irrespective of
> `soul:apply`; a trusted repo's soul still obeys an explicit `soul:apply: deny`.

### The operator posture ladder (`--posture`)

The whole prompt/trust posture is set by **one ordered operator tier** chosen at
process start by whoever owns the blast radius. Higher tiers grant more autonomy
and prompt less:

| posture | allow-all (no mutate-ask prompts) | main substitution floor | child substitution floor | project-trust floor | use it for |
|---|---|---|---|---|---|
| `strict` (**default**, fail-closed) | off | gated | gated | (your own `--trust-project`) | interactive / untrusted repos |
| `trusted` | off | gated | gated | **on** (honour the project authority set) | a repo you trust, still want prompts |
| `auto` | **on** (main + children) | loosened | **gated** (child prompt-injection defence **ON**) | on | the **recommended unattended default** |
| `yolo` | **on** (main + children) | loosened | **loosened** (child defence **OFF**) | on | a disposable, isolated, single-tenant sandbox |

Pick the tier with `--posture <strict|trusted|auto|yolo>` on `mecated` or the
embedded `mecatui` server (it is **ignored when `mecatui` dials an external
`--server`** — that server owns its own posture). `--yolo` is an **alias for
`--posture yolo`** and `--trust-project` is an **alias for `trusted`**; passing
both a `--posture` value and an alias resolves to the **higher tier** with a
`WARN`, an unknown `--posture` value fails closed to `strict` with a `WARN`, and a
CLI flag out-ranks the user-global `posture:` setting (below). Confirm what a given
combination resolves to with `mecated --print-posture` (prints the tier + the
per-defence breakdown and exits).

**`auto` is the recommended unattended default.** It is allow-all for the main
agent *and* its children, so a CI / container / VM run never parks on a mutate-ask
prompt — but the **child prompt-injection defence stays ON**: a subagent /
team-member / parallel-branch `$(...)`/backtick/heredoc command still resolves
through the child-ask model rather than auto-running. Only step up to `yolo` (which
loosens that child substitution floor too) where the harness genuinely cannot cause
durable harm.

> **Behaviour change — `--yolo` now also loosens the child substitution floor.**
> Previously the substitution-floor loosening was **main-only**; under `--posture
> yolo` (= `--yolo`) a child's `$(...)`/backtick/heredoc command **auto-runs** (the
> child injection defence is **OFF**). If you want allow-all but the child defence
> kept on, use `--posture auto`.

**The agent shell never sees the harness secrets.** Independently of the posture
tier, every agent-facing Bash shell runs with the harness's credentials scrubbed
out of its environment, so even under `auto`/`yolo` (allow-all) the model **cannot**
`echo $OPENROUTER_API_KEY` or `cat /proc/self/environ` to read a provider/auth key.
The scrub (`internal/adapter/envscrub`) is a precise denylist: it drops the exact
credential vars the harness reads (the provider keys, the websearch keys, the
`MECATL_*`/`GH_TOKEN`/`GITHUB_TOKEN` tokens) plus secret-shaped names (`*_API_KEY`,
`*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `AWS_*`, `AZURE_*`), while keeping the whole
toolchain (`PATH`, `HOME`, `GOPATH`, `GOCACHE`, `TMPDIR`, `LANG`, …) so `go
build`/`go test`/`git` still work. It applies to the main shell and to every
sandboxed subagent / team-member / parallel-branch shell.

#### How allow-all works (the mechanism)

For the allow-all tiers (`auto`/`yolo`) the harness suppresses the permission
prompts for the **built-in mutate-ask floor**.

It is **not** a `PermissionMode` and **not** an evaluator bypass. It injects a
single `ScopeCLI` allow-all **rule** into the engine's static ruleset, which
loosens **only** the built-in `Bash`/`Edit`/`Write`/`Team`/`SkillDraft` Ask floor.
The governance invariants are unchanged:

- A `Deny` in **any** scope (including `ScopeManaged`) still wins — deny-dominance is
  absolute. An admin can forbid specific tools/patterns even under allow-all.
- Any **deliberately configured** `Ask` (managed/project/user) still asks. Allow-all
  never suppresses a configured Ask, so a misconfigured Ask can still **block an
  unattended run** — the startup warning says so. (The common CI case configures no
  asks beyond the built-in floor, so allow-all is fully unattended there.)

**Main and children — and the child substitution floor is the `auto` vs `yolo`
line.** The allow-all **rule** is injected into both the main engine's ruleset
(`AudienceMain`) and the child/member ruleset (`AudienceSubagent`), so the mutate-ask
floor is loosened for subagents, team members, and parallel branches as well at both
allow-all tiers. The **child substitution-floor loosening** is what separates the two
tiers:

- Under **`auto`** the loosening stays **main-only** — a child's
  `$(...)`/backtick/heredoc command still resolves through the subagent child-ask
  model (see *Compound-Bash & substitution safety* below). The child
  prompt-injection defence is **ON**.
- Under **`yolo`** the loosening **also** applies to children — a child's
  substitution command **auto-runs**. The child injection defence is **OFF**. This
  is the deliberate behaviour change from the old main-only `--yolo`.

The allow-all rule also blankets the synthetic `soul:apply` floor (the soul is
applied without prompting under `auto`/`yolo`) — consistent and expected, since the
soul is already floor-Allow by default. A **configured** `Deny`/`Ask` on `soul:apply`
(or on any memory tool) still wins, exactly like every other tool.

**Sandbox-first.** The posture bypasses the *prompt*, never a *sandbox*. The real
boundary for unattended agentic execution is OS-level isolation (container/microVM,
network-off-by-default, ephemeral filesystem) — enable an allow-all posture **only
where the harness cannot cause durable harm**, and only on single-tenant daemons (the
posture makes *every* session on that daemon allow-all).

**Root refusal.** If an **allow-all posture** (`auto` or `yolo` — both waive the
mutate-ask floor) is requested **and** the process runs as root (`euid 0`) **and**
neither `MECATL_SANDBOX=1` nor `IS_SANDBOX=1` is set, the process **refuses to
start** with a clear error: root + no prompts can modify anything on the host, so the
operator must affirm an isolated, disposable environment via the env var. (This was
generalised from the old `--yolo`-only refusal; it now gates `auto` too.)

```sh
# CI / sandboxed container, offline mock, allow-all + child defence ON:
MECATL_SANDBOX=1 bin/mecated --mock --posture auto

# Disposable sandbox, child defence OFF too:
MECATL_SANDBOX=1 bin/mecated --mock --posture yolo   # == --yolo
```

### The self-improving-skill loop (`SkillDraft` + `mecated skills promote`)

`--skills-draft-dir <quarantine>` enables a **writable** `SkillDraft` tool so the
agent can author a reusable skill from a procedure it just performed. This is the
*only* tool that produces skills, and it is bounded by a hard trust boundary:

- A drafted skill is **never active in the session that wrote it.** `SkillDraft`
  validates and sanitizes the (untrusted) candidate — name regex, an
  injection-marker scan on both the description and the body, size caps, atomic
  write — and persists it to the **quarantine** directory. The quarantine is
  **never** registered as a `Skill` catalog source, so its contents add **zero**
  tokens to any context.
- To make it active, an **operator** runs the gate (flags precede the `<name>`):

  ```sh
  mecated skills promote \
    --skills-draft-dir <quarantine> \
    --skills-dir <active-skills-dir> \
    <name>            # add --yes to skip the interactive review (scripted/CI use)
  ```

  Promotion **prints the full candidate and asks for confirmation**, then verifies
  `origin: model` provenance, re-runs structural validation and the injection scan,
  refuses to overwrite an existing skill, and moves the candidate (provenance
  frontmatter intact: `origin: model`, `drafted_at`) into the active tree. It takes
  effect on the **next** server start — the convention is *author in session N →
  operator promotes → active in N+1.*
- **The trust boundary is structural.** The quarantine dir must live **outside the
  workspace root** (fatal startup error otherwise): the model's `Write`/`Edit` are
  confined to the workspace, so they cannot reach an out-of-workspace quarantine —
  the `Drafter` is the only way a candidate gets there. It must also be **disjoint**
  from every `--skills-dir` (fatal on overlap). `SkillDraft` itself defaults to
  **ask**, and being a mutating tool it is unavailable in plan mode.
- **Residual to know:** absent the (deferred) OS sandbox, the `Bash` tool can write
  to any path, so the structural boundary covers `Write`/`Edit` only — `mecated`
  warns when `SkillDraft` and `Bash` run together. For a fully structural boundary,
  run shell-less (`--no-bash`) or under an OS sandbox, and place active `--skills-dir`
  trees outside the workspace too (a startup warning flags an in-workspace one).

When you promote, **read the body** — it is agent-authored, untrusted,
instruction-like text that becomes trusted on promotion. The automated injection
scan is a backstop, not a substitute for reading it.

### Persona / soul (`~/.config/mecatl/soul.md`, issue #14)

A **user-scoped, agent-read-only** persona fragment — the operator's "soul": who
the agent is, its style, the posture it should take. It is read from
`$XDG_CONFIG_HOME/mecatl/soul.md` (fallback `~/.config/mecatl/soul.md`), or from an
explicit path via `--soul-file`, and injected as a **turn-0 user message** (after
the cache-stable system prefix, before the memory index — identity before saved
facts), fenced in a `<soul>…</soul>` data block so the model treats it as persona
data rather than a new instruction stream.

It is **on by default** and costs nothing when absent — a missing file is fail-soft.
The whole load is fail-soft: a missing, empty, whitespace-only, oversized (> 20 KiB),
unreadable, or **prompt-injection-flagged** file degrades to **no fragment**, never an
error that aborts a run. Disable it entirely with `--no-soul`.

It is **read-only to the agent by construction**: no tool can write the soul, and the
loader has no write path. This is deliberate — a writable identity anchor is a
prompt-injection trap (a single poisoned write would rewrite "who the agent is" across
*every* future session). Bootstrap and edit it by hand, with a text editor. (See
`docs/adr/0011-soul-and-user-model.md` for the threat model and the Phase-2 learning loop.)

**Drift detection (issue #14, Phase 3).** The harness fingerprints the soul's content
(sha256 of the clean body) and records it in a **harness-owned sidecar** next to the
soul file: `<soul-path>.sha256` (e.g. `~/.config/mecatl/soul.md.sha256`, or
`PATH.sha256` for `--soul-file PATH`). On the first load with no sidecar it records the
current hash as the baseline (trust-on-first-use) and logs `soul: baseline established`.
On a later load whose hash differs it logs a **`WARN` drift alert** with both hashes and
**still loads** the soul — a hand-edit on your own box is expected, so drift is surfaced,
not blocked (the soul is fenced DATA, never a permission gate). To manage drift:

- `--approve-soul` — (re)write the baseline to the current hash, accepting your edit.
  Run it once after you intentionally change your soul to silence the warning.
- `--soul-strict` — refuse a **drifted** soul: contribute no fragment this run until you
  `--approve-soul` the change. Useful on a shared/locked-down box.

The hash is computed by the read-only loader; the baseline **write** lives only in the
composition layer, so the agent still cannot touch the soul *or* its baseline. Note this
is **detection only** — there is no automatic restore-to-baseline (that would require a
harness-held copy of the approved bytes; deferred as a future opt-in). Delete the
`.sha256` sidecar to reset to trust-on-first-use.

**Project-sourced soul + trust gate (issue #14, Phase 3, Item 2).** Besides the
user-scoped soul above, the harness can also discover a **project soul** at
`<workspace>/.mecatl/soul.md` — a persona checked into the repo (parallel to
`.mecatl/settings.yaml`). Because it comes from a repo rather than your own config, it
is **untrusted by default**: it contributes **no fragment** unless you pass
`--trust-project` — the **same** flag that gates a project's permission ALLOW rules (no
separate soul-trust knob). An untrusted project soul is dropped with a `WARN` log, never
an error. Precedence is **USER-WINS** (a single identity anchor, not a merge):

- A **user-scoped** soul present (`<xdg>/mecatl/soul.md` or `--soul-file`) → it is used,
  and the project soul is **ignored** — even with `--trust-project`.
- **No** user soul **and** `--trust-project` set → the project soul loads (through the
  same byte-cap / injection-scan / fence / drift discipline as the user soul).
- **No** user soul and `--trust-project` **unset** → nothing (the project soul is
  dropped + logged).

Your **user-scoped soul is never trust-gated** — it always loads if present, regardless
of `--trust-project`. (Note: the embedded TUI server defaults `--trust-project` OFF,
unified with `mecated` (WORKSPACE-TRUST Phase 0), so a project `.mecatl/soul.md` is
honoured only when you pass `--trust-project` to `mecatui`.)

### User model (`~/.config/mecatl/usermodel`, issue #14 Phase 2)

A **user-scoped, cross-project** model of durable **FACTS about the operator** — who
they are and how they like to work. Unlike the soul (read-only) and per-project memory
(`--memory-dir`), the user model is **writable by the agent** and **shared across every
project**, backed by a SECOND `memory` store at `$XDG_CONFIG_HOME/mecatl/usermodel`
(fallback `~/.config/mecatl/usermodel`), overridable with `--user-model-dir`.

It surfaces two ways:

- **Tools (on by default):** `RememberUser`, `RecallUser`, `SearchUserModel` — the
  user-model siblings of the per-project memory tools. Keys are auto-namespaced under
  `user/`. The model sees a turn-0 `<user-model>` block summarising the saved facts
  (injected LAST: soul → memory index → user model).
- **Background reviewer (off by default, `--user-model-review`):** after a session
  stops, a fresh single-shot child reads the transcript and extracts operator facts via
  RememberUser. It is debounced by `--user-model-review-interval` and **never reopens or
  re-runs the user's session** — it spawns a brand-new child. A
  `--user-model-consolidate-interval` points a `dream` consolidator at the `user/`
  namespace.

**Rules vs facts — the operator boundary.** The user model holds **FACTS about the
operator** (stated preferences, communication style, domain background), **never rules
or behavioural instructions for the agent**. How the agent behaves comes from its soul
and the system rules; the `<user-model>` block is fenced **DATA** the model treats as
facts, not a new instruction stream, and the tool descriptions forbid storing rules or
anything the workspace already knows. The RememberUser write path injection-scans both
the value AND the effective description (reusing `skills.ScanForInjection`) — the
`<user-model>` block renders the key + description, so scanning only the value would
miss a payload hidden in `description` — and additionally rejects any field containing
the data-fence close-tag `</user-model>` (mirroring soul's reject-on-close-tag), so a
poisoned transcript cannot launder steering into the block or break its data fence. The user model is an instruction
**fragment**, not a governance scope — it can never loosen a configured permission Ask.
Over-eager memory is *steered* (by the descriptions), not *enforced* (there is no
rule/fact classifier); this is a deliberate, accepted residual risk. Single-operator
assumption: there is no per-user keying — "the operator" is implicitly singular, the
same trust-zone assumption the soul and `docs/adr/0009-tiered-memory.md` carry. Disable
it with `--no-user-model`.

### Graceful shutdown

`mecated` traps `SIGINT` / `SIGTERM`, stops accepting new work, drains the HTTP
server (bounded by a 10 s timeout) and `GracefulStop`s the gRPC server:

```
level=INFO msg="shutdown signal received; stopping servers"
```

---

## 4. The gRPC API

Service: `mecatl.v1.HarnessService` (`contracts/proto/mecatl/v1/harness.proto`).

**Sessions & runs:**

| RPC | Kind | Purpose |
| --- | --- | --- |
| `CreateSession(CreateSessionRequest) → CreateSessionResponse` | unary | allocate a server-side session, return its id |
| `GetSession(GetSessionRequest) → GetSessionResponse` | unary | snapshot of an existing session |
| `CloseSession(CloseSessionRequest) → CloseSessionResponse` | unary | end a session and release its server-side resources (learned rules, per-session engine/workspace); idempotent |
| `Converse(stream ConverseRequest) → stream ConverseResponse` | bidi | drive one agent run |

**Inventory & introspection** (read-only; most are snapshots taken at startup):

| RPC | Kind | Purpose |
| --- | --- | --- |
| `ListModels` | unary | the selectable provider/model inventory — public metadata only (powers the `/models` picker; see §3) |
| `ListAgents` | unary | the discovered agent-definition registry (name, description, resolved model, tool scope) |
| `ListCommands` | unary | the available slash commands for a workspace (discovery only — expansion happens on the run path) |
| `ListWorktrees` | unary | the git worktrees of a repo (discovery only — powers the mecatui `/worktrees` switch; nil-safe on a no-FS/cloud server; issue #102) |
| `ListSkills` | unary | the discovered skills inventory (name + one-line description) |
| `GetSoul` | unary | the resolved soul's build-time snapshot: content, size/hash, provenance, trust + drift state |
| `GetUserModel` | unary | the **live** user-model index (entry keys + descriptions; values omitted — `Recall` loads them) |
| `ListMcpResources` / `ReadMcpResource` | unary | static MCP resource snapshots; read one resource by URI |
| `ListMcpPrompts` / `GetMcpPrompt` | unary | MCP prompt snapshots; expand one prompt to its rendered messages |
| `ListMcpSources` | unary | the resolved MCP source inventory (static / ToolHive) + diagnostics |
| `ListToolHiveGroups` | unary | the distinct ToolHive groups in the resolved inventory (no live ToolHive call) |

**Agent teams** (experimental; registered with `--enable-teams`, the default):

| RPC | Kind | Purpose |
| --- | --- | --- |
| `CreateTeam(CreateTeamRequest) → CreateTeamResponse` | unary | allocate a team (optionally enrolling an initial roster); accepts the tighten-only `max_team_tokens` |
| `SpawnTeammate` | unary | enrol a member in an existing team (before `RunTeam`) |
| `SendTeammateMessage` | unary | post a message into a member's inbox, delivered at its next turn boundary |
| `CancelTeammate` | unary | cancel ONE member of a **running** team mid-round: it de-schedules with the `cancelled` stop reason and releases its claimed tasks; the team still delivers its report. Not-running team → `FailedPrecondition`; unknown member → `NotFound` |
| `RunTeam(RunTeamRequest) → stream TeamEvent` | server-stream | drive the team to quiescence; every member's events stream tagged with the member name, and the stream **ends with a single terminal frame carrying `TeamEvent.outcome`** (rounds, stop, `budget_exhausted`, usage, dispositions, findings) |
| `ListTeam` | unary | snapshot of the roster, shared task list, and quiescence |
| `CleanupTeam` | unary | tear down a finished team and release its resources |

### The `Converse` flow

The bidi stream drives exactly one run:

1. The client sends the **mandatory first frame**, a
   `Prompt{session_id, text, parts}` — `parts` optionally carries multimodal
   media (image/audio `Content` parts, capability-gated on the session's
   provider). (A first frame that is not a prompt → `InvalidArgument`.)
2. The server streams `ConverseResponse{Event}` envelopes in sequence order.
3. On a `permission.ask` event, the client sends a control frame
   `ResumeApproval{ask_id, verdict}` — `ask_id` echoes `Event.ask.ask_id`, and
   `verdict` is the **three-way** resolution: `DENY`, `ALLOW_ONCE`, or
   `ALLOW_ALWAYS` (which also **learns** a per-session allow rule for the same
   tool + exact canonical pattern; it never overrides a deny or plan-mode
   mutation denial). The legacy `allow` bool still works when `verdict` is
   unset: `true` → allow once, `false` → deny.
4. The client may send `Cancel{}` at any time to abort — the run terminates with
   a `result` whose `stop = "cancelled"` — or `CancelChild{child_id}` to cancel
   ONE delegated child (subagent / parallel branch / team member) by its id
   while the run itself keeps streaming.
5. The server emits a terminal `result` event and closes the stream.

`ConverseRequest` is a `oneof`:

| Field | When |
| --- | --- |
| `prompt` (`Prompt{session_id, text, parts}`) | mandatory first frame |
| `resume_approval` (`ResumeApproval{ask_id, verdict, allow}`) | resolve a paused ask (three-way `verdict`; the `allow` bool is the legacy fallback) |
| `cancel` (`Cancel{}`) | abort the in-flight run |
| `cancel_child` (`CancelChild{child_id}`) | cancel ONE child run by its id (the `agentId:` / `child_id` handle), leaving the run and sibling children untouched; unknown/finished ids are ignored on the stream |

A second `prompt`, or any unknown control frame, is ignored — a single
`Converse` stream drives a single run.

### Event envelope

Every event is the provider-neutral `Event` message (mirrors the domain
`session.Event` one-for-one — never an OpenAI type):

```protobuf
message Event {
  string type = 1;          // session.init | turn.start | turn.end |
                            // message.delta | reasoning.delta | tool.call |
                            // tool.result | tool.progress | permission.ask |
                            // permission.retract | hook | compaction |
                            // no_progress | result | subagent.* | team.* |
                            // parallel.*
  int64  seq  = 2;          // monotonic per run
  int32  turn = 3;
  string text = 4;          // streamed/final text where applicable
  ToolCall      tool_call   = 5;
  ToolResult    tool_result = 6;
  PermissionAsk ask         = 7;   // ask_id echoed in ResumeApproval
  Result        result      = 8;   // terminal event
  Usage         usage       = 9;   // cumulative on result; per-turn in turn_end
  TurnEnd       turn_end    = 10;  // turn.end: this turn's usage + elapsed time
  Hook          hook        = 11;  // structured hook phase/tool/decision/call_id
  Subagent      subagent    = 12;  // subagent.*: REDACTED metadata-only child projection
  Team          team        = 13;  // team.*: BOUNDED team projection (start/member/tasks/findings/end)
  Parallel      parallel    = 14;  // parallel.*: REDACTED fork-join projection (incl. winner + fork paths)
}
```

The three delegation families (`subagent.*`, `team.*`, `parallel.*`) project a
child loop's lifecycle without leaking its content: `subagent`/`parallel` carry
metadata only (ids, tool names/counts, usage, stop — never args, results, or
message text); `team` is fuller but every preview is capped and a member's
permission asks are never forwarded.

`Result.stop` is one of: `end_turn`, `max_turns`, `max_tool_calls`,
`max_consecutive_failures`, `budget` (the `--max-run-tokens` ceiling crossed —
a clean, reopen-able terminal), `no_progress`, `cancelled`, `error`. A child's
stop (on `subagent.end` / in a Subagent result) may additionally be
`structured_output` — a structured-output child that exhausted its
validation retries.

### Go client snippet

`mecated` does **not** register gRPC server reflection, so `grpcurl` must be
pointed at the proto (and its `buf.validate` import) explicitly. A generated Go
client is the simplest path:

```go
package main

import (
	"context"
	"io"
	"log"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

func main() {
	conn, err := grpc.NewClient("127.0.0.1:8080",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	client := mecatlv1.NewHarnessServiceClient(conn)

	// 1. Create a session.
	cs, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{
		Workspace: "/path/to/workspace",
		Mode:      mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
	})
	if err != nil {
		log.Fatal(err)
	}

	// 2. Open the Converse stream and send the mandatory first Prompt.
	stream, err := client.Converse(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{
			Prompt: &mecatlv1.Prompt{SessionId: cs.GetSessionId(), Text: "List the Go files."},
		},
	}); err != nil {
		log.Fatal(err)
	}

	// 3. Relay events; approve any permission.ask.
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return // terminal result delivered, stream closed
		}
		if err != nil {
			log.Fatal(err)
		}
		ev := resp.GetEvent()
		log.Printf("[%d] %s %s", ev.GetSeq(), ev.GetType(), ev.GetText())

		if ev.GetType() == "permission.ask" {
			_ = stream.Send(&mecatlv1.ConverseRequest{
				Kind: &mecatlv1.ConverseRequest_ResumeApproval{
					ResumeApproval: &mecatlv1.ResumeApproval{
						AskId: ev.GetAsk().GetAskId(),
						Allow: true,
					},
				},
			})
		}
		// To abort instead, send a Cancel{} frame:
		//   stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}}})
	}
}
```

`GetSession` returns a snapshot (`session_id`, `state`, `mode`, `workspace`,
`limits`, `turns`, `tool_calls`, `created_at_unix`).

### The terminal UI (`mecatui`)

`mecatui` is an optional, flashy terminal UI that drives a `mecated` over this
same gRPC `Converse` stream. After `task build` it lands at `bin/mecatui`. It
needs no separate server by default — with no `--server` it reuses a `mecated`
already running on `127.0.0.1:8080`, or else **hosts one in-process** over a UNIX
socket (built via `internal/app`, the same assembly `mecated` uses):

```sh
OPENAI_API_KEY=sk-... bin/mecatui --workspace "$PWD"   # embedded (default)
bin/mecatui --mock --workspace "$PWD"                  # embedded, offline mock

bin/mecated &                                          # …or an external server
bin/mecatui --server 127.0.0.1:8080 --workspace "$PWD"
```

The embedded server enables every **free + local** feature by default — memory
(per-project, under `$XDG_DATA_HOME/mecatui/memory`), server-side slash-command
expansion (`.mecatl/commands` / `.claude/commands`), skills (conventional
discovery), agent definitions, soul, user model, child-session retention GC,
ToolHive MCP discovery, and the MCP resource/prompt meta-tools. Only the
opt-ins that spend tokens or need explicit configuration stay off: static
`--mcp-server` registrations, the `SkillDraft` quarantine, the background
user-model reviewer, and memory consolidation — run a full `mecated` and use
`--server` for those. It also accepts **`--perf`** (off by default) to bring up the same
loopback observability surface `mecated` exposes — `/metrics`, `/debug/pprof/*`,
`/debug/vars`, `/debug/flightrecorder` — on a **fixed** `127.0.0.1:9099` port by
default (predictable, so an MCP-client config can hardcode the `/mcp` URL once;
distinct from `mecated`'s `:9090`). Pass `--perf-addr host:port` to move it, or
`--perf-addr 127.0.0.1:0` for an ephemeral port. On a port clash, startup **fails
with guidance** rather than silently falling back (`--perf-goroutine-warn-threshold`
arms the goroutine alarm). The chosen address is logged at startup (loopback, unauthenticated —
same posture as `mecated`'s admin listener; see the observability note in §3).
With `--perf` it also accepts **`--perf-mcp`** to mount the read-only perf MCP
server at `/mcp` on that admin surface (same fail-closed loopback enforcement: a
non-loopback `--perf-addr` with `--perf-mcp` is refused). This is the in-process
way to profile a freeze in the embedded server itself.
The embedded server also accepts `--yolo` (the
allow-all operator posture — same semantics, root refusal, and `MECATL_SANDBOX`/
`IS_SANDBOX` env as `mecated`; see the allow-all note in §7). It is **ignored when
dialling an external `--server`**. Note the TUI's **built-in slash commands**
(`/clear`, `/help`, and the caps-gated `/mcp`, `/agents`, `/team`, `/skills`,
`/soul`, `/usermodel`, `/models`, `/worktrees` — in that fixed palette order) still work
regardless — they act on the TUI itself, not the server, so typing `/` always
opens a useful palette even with workspace slash-command expansion off
(`/agents` browses the agent-definition inventory; `/team`, also `ctrl+a`,
opens the live agent-team overlay; `/skills` the skills inventory; `/soul` and
`/usermodel` the persona/user-model views; `/models` the model picker;
`/worktrees` the sibling-git-worktree switch — it lists the repo's worktrees and,
on select, starts a NEW session rooted at the chosen worktree so all local tools
bind there; gated on the server advertising `worktrees`, so it is honestly absent
against a no-FS/cloud server; issue #102). See
`docs/tui.md` for all flags.

It streams the conversation (glamour markdown for assistant text, themed cards
for tool I/O), shows a thinking spinner and a usage footer, and pops an inline
modal for permission asks that you approve/deny without leaving the stream. It is
themeable (Aztec default, plus `mono`/`solar`, plus drop-in JSON themes) and
respects the same trust model: it refuses to send `--auth-token` in cleartext to
a non-loopback server (use `--tls`). Full flag, key, and theming reference is in
**`docs/tui.md`**.

---

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

---

## 6. Configuration

### Workspace

`--workspace` (server-wide default) and the per-session `workspace` field set
the root all file/command tools operate against. The server builds an `osfs`
workspace rooted there. A root that cannot be opened yields a nil workspace;
tool calls then return readable errors the model can act on.

### Model

`--model` (empty by default — the selected provider's own default is used:
`gpt-5` for OpenAI, `openai/gpt-5` for OpenRouter, `claude-sonnet-4-6` for
Anthropic) is the identifier sent to the provider and stamped into the
system-prompt env. Pass **strings** for forward-compatibility and for
compatible endpoints.

### Session store

| `--store-dir` | Store | Behaviour |
| --- | --- | --- |
| empty (default) | in-memory (`memstore`) | nothing persists across restarts |
| set to a dir | JSONL replay (`jsonlstore`) | snapshots + tool-call log on disk |

The JSONL store writes three files per session under `--store-dir`:

```
<dir>/<id>.session.jsonl   # one snapshot per Save (latest line wins)
<dir>/<id>.tools.jsonl     # one record per tool call (call, result, duration)
<dir>/<id>.events.jsonl    # the relayed event timeline (reasoning, ask/verdict, delegation)
```

> **Privacy:** the durable store holds the **raw conversation** — prompts, model
> output, and tool arguments/results — in **plaintext** on disk. The store
> directory is created mode `0700` (owner-only). `mecated` keeps the store **off**
> by default (empty `--store-dir` → in-memory); `mecatui` defaults it **on** at a
> per-workspace directory under `$XDG_STATE_HOME/mecatui/sessions` (see
> [the TUI guide](tui.md)), so a session survives restart and can be inspected
> after the fact.

Persisted sessions are garbage-collected by a background sweep so the durable
store does not grow without bound. **Child** sessions (`subagent-*`/`parallel-*`/
`team-*` ids, written by the delegation paths so `InspectSubagent`/`InspectMember`/
`resume:` work) are bounded by `--child-retention` /
`--child-retention-max-per-family` (defaults 168h / 500). **Main** (top-level)
sessions are bounded by `--main-retention` / `--main-retention-max-total` — **both
off by default for `mecated`** (main sessions are then never swept), and on for
`mecatui` (30 days / 200 store-wide). The sweep re-runs every `--child-gc-interval`
(default 1h) and always skips an in-flight run; deleting a session removes all of
its files.

### Remote store drivers

A third option points the session store (and/or the memory store) at a
**remote driver process** speaking the `mecatl.driver.v1` gRPC protocol:

```sh
mecated --session-store-url 127.0.0.1:7443 --memory-store-url 127.0.0.1:7443
```

`--session-store-url` is mutually exclusive with `--store-dir` (and
`--memory-store-url` with `--memory-dir`) — a fatal startup error, never a
silent precedence. Equal URLs share one connection. The driver only ever sees
**opaque snapshots** (the `sessnap` encoding under a `"sessnap-json/1"` format
tag); it sits at the same trust tier as the on-disk store directory. A
conforming driver must accept snapshot payloads up to **64 MiB** (mount the
gRPC server with a matching receive limit; the harness client is already
configured for it).

Transport posture: **only LOCAL targets may ride plaintext** — loopback hosts
and unix sockets (the single-user default). Any other driver target
**requires `--driver-tls`, token or not**: the client refuses cleartext
pre-dial, because a driver delivers session payloads, memories,
model-steering skill bodies, and executable skill assets — an on-path
attacker over a cleartext remote link would gain driver-equivalent
capability regardless of auth. `--driver-auth-token` adds per-RPC bearer
auth on top; `--driver-tls-ca` pins a custom CA;
`--driver-tls-cert`/`--driver-tls-key` add a client certificate for mTLS.
Setting any `--driver-tls-*` file **without** `--driver-tls` is a fatal
startup error (it would otherwise be silently ignored). There are **no
retries and no default deadline** on driver RPCs — a driver failure surfaces
as the same unit failure a disk error would.

### Cross-process session leasing (multi-replica single-writer)

By default mecatl assumes **session affinity**: route every session to exactly
one mecated process and never run two processes against the same session id
concurrently. The in-process run registry enforces single-writer WITHIN a
process, but two replicas over one shared store have no cross-process exclusion —
last-write-wins on the JSONL store. For a deployment that cannot guarantee
affinity (e.g. a load balancer that may reroute a session), wire a **session
lease** so the harness enforces single-writer itself (cloud-native Phase 4):

```sh
# Single host, several mecated processes sharing one --store-dir:
mecated --store-dir /var/lib/mecatl/store --session-lease-dir /var/lib/mecatl/leases

# In-cluster multi-replica (coordination.k8s.io Lease per session):
mecated --session-store-url store-driver:7443 --session-lease-k8s-namespace mecatl

# Or a dedicated lease driver, independent of the store:
mecated --session-store-url store-driver:7443 --session-lease-url lease-driver:7443
```

When a lease is wired, the run-entry path acquires a per-session lease before
driving the engine. A second replica's run-start (or approve-resume) for a
session another replica holds is **refused with HTTP 409 Conflict** (gRPC
`FAILED_PRECONDITION`); the lease is held for the session's life, renewed in the
background (`--session-lease-renew-interval`, default `--session-lease-ttl`/3),
and released on session end / shutdown. A crashed holder's lease lapses after
`--session-lease-ttl` (or, for the flock backend, releases immediately on process
death), after which a survivor takes over. Losing the lease mid-run cancels the
run cleanly (recoverable). The three backends are **mutually exclusive**; empty =
no leasing (the byte-identical default).

- **`--session-lease-dir` (flock):** SINGLE-HOST only. Several mecated processes
  on ONE machine sharing the dir contend via `flock(2)`, with free crash recovery
  (the OS releases a dead process's lock). NOT safe across hosts (flock semantics
  over NFS/EFS are unreliable) — use k8s or the driver for multi-host.
- **`--session-lease-k8s-namespace` (Kubernetes):** the in-cluster multi-replica
  path. Each session is a `coordination.k8s.io/v1` Lease object named
  `mecatl-lease-<hash>` (the raw id is in the `mecatl.stacklok.com/session-id`
  annotation). Uses in-cluster config, or the default kubeconfig out-of-cluster.
  The pod's ServiceAccount needs this **namespace-scoped RBAC**:

  ```yaml
  apiVersion: rbac.authorization.k8s.io/v1
  kind: Role
  metadata:
    name: mecatl-session-lease
    namespace: mecatl
  rules:
    - apiGroups: ["coordination.k8s.io"]
      resources: ["leases"]
      verbs: ["get", "create", "update", "delete"]
  ---
  apiVersion: rbac.authorization.k8s.io/v1
  kind: RoleBinding
  metadata:
    name: mecatl-session-lease
    namespace: mecatl
  subjects:
    - kind: ServiceAccount
      name: mecatl            # the mecated pod's ServiceAccount
      namespace: mecatl
  roleRef:
    kind: Role
    name: mecatl-session-lease
    apiGroup: rbac.authorization.k8s.io
  ```

  A missing RBAC verb surfaces as a hard error (a Forbidden, never a silent
  no-lease run).
- **`--session-lease-url` (driver):** a remote `mecatl.driver.v1.SessionLeaseService`
  (multi-host, store-independent), sharing the same `--driver-tls`/auth posture and
  connection cache as the store drivers above.

Without an explicit backend, mecatl can also discover a lease from a session
store that happens to implement the lease seam (type-assertion, like the
retention seam); today's jsonlstore does not, so the no-flag default is no
leasing. See `docs/adr/0027-cloud-native.md` Phase 4 for the full design.

### Remote content-source drivers (skills + soul)

The same protocol carries two **content sources**:

```sh
mecated --skill-source-url 127.0.0.1:7443 --soul-source-url 127.0.0.1:7443
```

`--skill-source-url` replaces local skills discovery entirely (mutually
exclusive with `--skills-dir`/`--skills-conventional`). The driver's skill
set is **snapshotted once at startup** (fatal if the driver cannot answer —
an explicitly configured source that is down is a misconfiguration, never a
silent no-skills run). Skills cross the wire as **logical bundles** — name,
description, body, and payloads addressed by slash-relative logical names
(`references/api.md`, `scripts/run.sh`) — no paths. On a skill's **first
activation** its payloads materialize into a temporary, build-scoped **asset
cache** (the `Base directory` the activation header advertises); a
never-activated skill transfers zero bytes. Materialization is capped
(16 MiB per file, 64 MiB per bundle), name-validated and containment-checked
(an invalid bundle fails that activation with a model-addressable error,
never a partial bundle), honors the executable bit, and the whole cache is
removed on shutdown. **Trust:** a driver-served `SKILL.md` steers the model
like AGENTS.md/CLAUDE.md — point this only at a driver you trust (the same
tier as `--skills-dir`).

`--soul-source-url` serves the persona from the driver instead of the local
user soul file, occupying the **user slot** of the selection precedence (it
shadows a project soul exactly like a present user soul; `--no-soul` and the
`soul:apply` permission gate still apply). The driver is **probed at
startup** (fatal if unreachable); a fault at run time degrades fail-soft to
no fragment with a logged warning. The body is **re-validated locally**
(byte cap, injection scan, data-fence integrity — a driver is never trusted
to sanitize). The **drift baseline is skipped** for driver souls — the
baseline is sidecar-file machinery for a local file you edit, while a driver
sits behind the operator's own auth — so `--soul-strict` and
`--approve-soul` are no-ops for this provenance (one INFO line records the
skip).

Both share the `--driver-auth-token`/`--driver-tls*` posture, and equal URLs
share one connection with the store drivers.

### Remote content-source drivers (agent definitions + slash commands)

Phase C2 completes the family with two more sources on the same protocol:

```sh
mecated --agent-source-url 127.0.0.1:7443 --command-source-url 127.0.0.1:7443
```

`--agent-source-url` serves the **agent definitions** (the Subagent
specialists / team-member roles) from the driver. Like skills, the set is
**snapshotted once at startup** (fatal if the driver cannot answer — per-def
child engines are built once at build time, so there is no re-fetch). It is
mutually exclusive with explicit `--agents-dir`; the default-on
`--agents-conventional` discovery is simply **superseded** (an INFO line
narrates it — failing every default deployment over an ON-by-default,
usually-inert flag would be wrong; this asymmetry vs the opt-in skills
conventional discovery is deliberate). Defs cross the wire whole — tools,
limits, model/provider hints, skills, hooks, scoped MCP servers — with **no
path**: diagnostics identify a driver def as `driver: <target>`. (The
`memory:` field — per-agent persistent memory, issue #33 — is **not** carried
over the driver wire in v1; a driver-served def stays cold-start.) Inline MCP
server **headers are secret-shaped** (e.g. `Authorization`): the harness
never logs or projects them; they ride this wire only because driver dials
refuse all non-local cleartext. The driver's claimed origin tier is ignored —
every driver-served def is stamped `driver`. **Trust:** this is STRONGER than
model steering — a def's `hooks:` map executes as **ungated shell on the
harness host** (`hookexec`, every scoped lifecycle phase, no permission ask),
strictly more capability than the skill driver, whose payloads still ride the
permission-gated Bash path. **A compromised agent-source driver executes
arbitrary shell on the harness host via def hooks; treat it as
harness-equivalent infrastructure.** The build narrates every driver def that
carries hooks (`agent def carries lifecycle hooks (harness-side shell)` —
names only, never hook values) so the capability is visible at startup.

`--command-source-url` serves **slash-command templates**. Unlike every
other source driver it **composes instead of replacing**: the expansion
order is file-backed commands → driver commands → MCP prompts
(first-match-wins), so a local `<name>.md` shadows a same-named driver
command, and the palette merges all three. It is also **live**, not a
snapshot — the driver is consulted on every expansion and palette listing,
matching the file expander's reads-current-files behaviour, so the command
set may change while the server runs. The driver returns the RAW template
(frontmatter allowed); the harness strips frontmatter and substitutes
`$ARGUMENTS`/`$1`/`$2`… exactly as for a file command, so templates are
portable between the two backends byte-for-byte. The driver is probed once
at startup (fatal if unreachable); a fault at run time **fails soft** — the
raw input passes through unchanged and the palette omits the source (a
transient blip never aborts a run and never latches a command "missing").

All four content-source drivers share the `--driver-auth-token`/
`--driver-tls*` posture, and equal URLs share one connection.

### Permission modes

Set per session via `CreateSession` `mode` (HTTP `mode` string / proto
`PermissionMode`):

| Mode | Proto enum | Posture |
| --- | --- | --- |
| `default` | `PERMISSION_MODE_DEFAULT` | standard deny → ask → allow |
| `plan` | `PERMISSION_MODE_PLAN` | read-only toolset; mutations hard-denied |
| `acceptedits` | `PERMISSION_MODE_ACCEPT_EDITS` | auto-accept edits |

`PERMISSION_MODE_UNSPECIFIED` (and any unknown string) defaults to `default`.

### Default limits

A **zero** `Limits` value disables every stop condition, so the composition root
injects non-zero defaults for any session created without explicit limits, so a
default session is always bounded:

| Limit | Default | Disables when 0 |
| --- | --- | --- |
| `max_turns` | `2000` | yes |
| `max_tool_calls` | `8000` | yes |
| `max_consecutive_failures` | `5` | yes |

Supplying **any** non-zero limit field is taken as explicit and used as-is.

---

## 7. Permissions

### How a decision resolves

Each tool call is evaluated against a merged set of `Rule`s. A `Rule` is
`{Scope, Tool, Pattern, Effect}` where `Effect` is `deny`, `ask`, or `allow`,
`Tool` empty matches any tool, and `Pattern` empty matches any args (otherwise a
shell-style glob, with an exact-match fast path, over the canonicalized
command/argument string).

Resolution precedence:

1. **`deny` → `ask` → `allow`**: a `deny` in *any* scope beats an `ask` or
   `allow` anywhere; otherwise an `ask` beats an `allow`.
2. **Scope** breaks same-effect ties (highest precedence first):
   `Managed > CLI > LocalProject > SharedProject > User > BuiltinDefault`. (One
   narrow exception to ask-beats-allow: a higher-scope config **Allow** may
   loosen **only** the built-in `ScopeBuiltinDefault` Ask floor, never a
   *configured* Ask — see the permission-config note in §3.)
3. **No matching rule → `ask`** — the safe default. The harness never silently
   allows an unconfigured call.

A `deny`/`ask` carries a human `reason`, surfaced to the model (on deny, so it
can adapt) and to the client (on ask).

### The default ruleset `mecated` ships

| Tool | Default effect |
| --- | --- |
| `Read`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `Subagent` | `allow` |
| the six memory tools (`Remember`/`Recall`/`SearchMemory`, `RememberUser`/`RecallUser`/`SearchUserModel`) | `allow` (floor-scoped, config-overridable — see §3) |
| `InspectSubagent`, `InspectMember`, `SubagentStatus` (read-only child observability) | `allow` (floor-scoped, config-overridable) |
| `soul:apply` (the synthetic soul-load action) | `allow` (floor-scoped, config-overridable — see §3) |
| `Bash`, `Edit`, `Write`, `Team`, `SkillDraft` | `ask` |

Read-only exploration runs without interruption; anything that can mutate the
workspace pauses for approval (`Team` asks because it can spawn **mutating**
members, unlike the read-only `Subagent` explorer).

### Plan mode hard-denies mutations

When a session is in `plan` mode, the evaluator gates *before* the rule engine:

- `Edit` and `Write` are unconditionally **denied** (they always mutate).
- A `Bash` command that is **not** read-only is **denied**; read-only Bash and
  the read-only tools (`Read`/`Grep`/`Glob`) fall through to the rules.

The deny reason tells the model to present a plan and exit plan mode first.

### Compound-Bash & substitution safety

For `Bash`, the evaluator splits compound command lines and requires **every**
sub-command to pass; the **worst** outcome wins. So `git status && rm -rf /`
inherits the deny/ask from the `rm` segment even if `git status` would be
allowed. Any segment containing command/process substitution or subshell
grouping (which could smuggle a hidden inner command past the splitter) is
floored at **`ask`** — an allow rule for the outer literal can never silently
approve a concealed destructive command.

> Permission rules are configured in Go in the shared composition layer
> (`internal/app`, `defaultRules()`), via `permpolicy.NewPolicy([]governance.Rule{…})`.
> There is no rules config file in v1; to change the shipped policy, edit
> `defaultRules()` and rebuild. (It lives in `internal/app` so both `mecated` and
> the embedded `mecatui` server share one ruleset.)

---

## 8. Hooks

Lifecycle hooks let an external command observe or veto agent actions. A hook is
a phase → shell-command map; each command is run as `<shell> -c <command>`
(default shell `/bin/sh`), with the JSON `HookEvent` written to its **stdin**.

### Phases

| Phase | Fires |
| --- | --- |
| `SessionStart` | once when a session begins |
| `UserPromptSubmit` | when the user submits a prompt |
| `PreToolUse` | before a tool runs — a block aborts the call |
| `PostToolUse` | after a tool runs |
| `Stop` | when the main loop stops |
| `SubagentStop` | when a subagent loop stops |

> v1 fully implements `PreToolUse` and `PostToolUse`; the others are defined and
> wired as the injection seam. **`mecated` ships with no global hooks configured
> by default** (`hookexec.New(nil)`, in the shared composition layer
> `internal/app`), so every event is allowed. Global hooks are configured in Go
> by passing a populated `map[governance.HookPhase]string` to
> `hookexec.New(...)`. Separately, an **agent definition** may carry a per-def
> `hooks:` map executed through the same `hookexec` runner for that child's
> lifecycle phases — note this is **ungated shell on the harness host** (no
> permission ask), which is why agent-def sources are a trust boundary (see the
> `--agents-dir` / `--agent-source-url` notes in §3 and §6).

### The stdin contract

The hook receives a JSON `HookEvent` on stdin:

```json
{
  "Phase": "PreToolUse",
  "Tool": "Bash",
  "Input": { "command": "rm -rf build" },
  "SessionID": "8867bdea940108c1dd82d13d3fb7fc61"
}
```

### The exit-code contract

| Exit code | Outcome |
| --- | --- |
| `0` | **allow** — the message (if any) is read from stdout |
| `2` | **block** — the action is vetoed; the reason is read from stdout (preferred) or stderr |
| any other | **error** — the hook itself failed; surfaced to the caller |

A single invocation is bounded by a timeout (default 30 s).

### Example hook script

A `PreToolUse` hook that blocks any `Bash` command containing `rm -rf`:

```sh
#!/bin/sh
# pretooluse-guard.sh — exit 2 to block, 0 to allow.
event="$(cat)"                       # the HookEvent JSON arrives on stdin
if printf '%s' "$event" | grep -q 'rm -rf'; then
  echo "blocked: 'rm -rf' is not permitted by policy"   # reason -> client/model
  exit 2
fi
exit 0
```

Wire it (in `internal/app` — `build.go`, where `hookexec.New(nil)` is today):

```go
hooks := hookexec.New(map[governance.HookPhase]string{
    governance.PhasePreToolUse: "/path/to/pretooluse-guard.sh",
})
```

---

## 9. OpenAI & compatible endpoints

The OpenAI provider talks to the **Responses API** (`POST /v1/responses`) via
`github.com/openai/openai-go/v3`. The harness owns its own conversation state:
every request is stateless (`store:false`, no `previous_response_id`) and
resends the full input slice, carrying reasoning items forward.

| Setting | How |
| --- | --- |
| API key | `OPENAI_API_KEY` env var (selects `--openai` automatically) |
| Base URL | `--openai-base-url https://your-host/v1` (SDK appends `/responses`) |
| Model | `--model <id>` |

**OpenRouter** rides this same stateless Responses adapter: set `OPENROUTER_API_KEY`
and the `openrouter` provider is auto-detected against `https://openrouter.ai/api/v1`
(override with `--openrouter-base-url`). It accepts an `OPENAI_API_KEY` by convention
when no dedicated key is set.

```console
# OpenAI
$ OPENAI_API_KEY=sk-... go run ./cmd/mecated --openai --model gpt-5

# An OpenAI-compatible endpoint (vLLM / LiteLLM / local proxy)
$ OPENAI_API_KEY=token go run ./cmd/mecated --openai \
    --openai-base-url http://127.0.0.1:8000/v1 --model my-model
```

### What compatible servers may lack

`/v1/chat/completions` is broadly supported, but `/v1/responses` support is thin
and version-dependent (llama.cpp: none yet; vLLM: partial; LiteLLM proxies
translate). Per `docs/adr/0017-openai-responses-api.md` §8, features **commonly
missing** on compatible servers include:

- `previous_response_id` / `store` (the harness already avoids these by design —
  it keeps state itself, so it is portable),
- hosted tools,
- automatic caching / `prompt_cache_key` (expect a lower or zero `cacheHitRate`),
- encrypted reasoning content and reasoning summaries,
- strict mode and `parallel_tool_calls` (these vary).

If a compatible endpoint behaves oddly, suspect missing Responses-API support
before suspecting the harness.

---

## 10. Running mecatequi from GitHub Actions

`mecatequi` (the single-shot headless runner, `cmd/mecatequi`) runs one prompt against
an in-process engine and emits a working-tree git diff, a machine-readable summary JSON,
and an optional durable event log, then exits with a code derived from the run's terminal
state. Two adoption paths wire it into GitHub Actions safely: a **reusable `workflow_call`
workflow** (the recommended path — a ~15-line caller, no vendored scripts) and a hand-rolled
**example workflow** (the escape hatch — vendor it when you need to customise the job graph).
Both build on the same **composite actions**. The design rationale, the trust model, and the
(now fixed) secret-scrubbed agent shell live in `docs/adr/0028-mecatequi.md`; this section is
the operator walkthrough.

> The example workflow is a **template** — copy it into your own repo and review it. This
> repo does not run it against real issues (no `mecatequi` label, no configured secret).

### The composite action (`.github/actions/mecatequi`)

The action builds the binary from the action's **own** checkout (`go build -C
"$GITHUB_ACTION_PATH/../../.." ./cmd/mecatequi`, toolchain from the action's `go.mod`) — the
matlatl pattern. This works uniformly for self-use (`uses: ./.github/actions/mecatequi`) and
cross-repo (`uses: stacklok/mecatl/.github/actions/mecatequi@<tag>`, where GitHub checks the
tagged mecatl repo into `$GITHUB_ACTION_PATH`); see "Adopting mecatequi in another repo"
below. There is **no token and no `GOPRIVATE`** — the action source is the build input. It
then runs the binary with `--untrusted-prompt` by default, captures the exit code **without
failing the step**, and exposes the result as outputs. The LLM key is **not** an input: the
binary reads provider secrets from the environment, so the caller sets `OPENAI_API_KEY` (or
`OPENROUTER_API_KEY`, etc.) in the calling **job**'s `env` (job-level, not step-level — step
`env:` on a `uses:` step does not reach a composite action's internal steps; job `env:`
does).

**Inputs → flags:**

| Input | Flag | Default |
|---|---|---|
| `prompt-file` (required) | `--prompt-file` | — |
| `untrusted` | `--untrusted-prompt` (when `true`) | `true` |
| `instructions` | `--instructions` (omitted when empty) | baked-in PR-description + self-verify framing |
| `setup-script` | composite pre-run step (not a binary flag) — runs before the binary | `""` (no hook) |
| `workspace` | `--workspace` | `${{ github.workspace }}` |
| `posture` | `--posture` | `auto` |
| `timeout` | `--timeout` | `40m` |
| `max-run-tokens` | `--max-run-tokens` (omitted when empty) | `""` |
| `max-turns` | `--max-turns` (omitted when empty) | `""` |
| `model` | `--model` | `""` |
| `default-provider` | `--default-provider` | `""` |
| `default-model` | `--default-model` | `""` |

Prefer `model` for newer/passthrough models; `default-model` is catalog-validated and
rejects ids not in the embedded snapshot. `model` is the per-session passthrough path — it
accepts any model the provider serves.
| `openai` | `--openai` (when `true`) | `""` |
| `openai-base-url` | `--openai-base-url` | `""` |
| `guardrails-model` | `--guardrails-model` | `""` |
| `subagent-ask-reviewer` | `--subagent-ask-reviewer` (omitted when empty) | `""` |
| `subagent-ask-reviewer-max-denies` | `--subagent-ask-reviewer-max-denies` (omitted when empty) | `""` |
| `pr-body-template` | `publish.sh` PR-body template (via `MQ_PR_BODY_TEMPLATE`) | `""` |
| `pr-title-template` | `publish.sh` PR-title template (via `MQ_PR_TITLE_TEMPLATE`) | `""` |
| `out-diff` | `--out-diff` | `$RUNNER_TEMP/mecatequi.patch` |
| `out-summary` | `--out-summary` | `$RUNNER_TEMP/mecatequi.summary.json` |
| `out-events` | `--out-events` | `$RUNNER_TEMP/mecatequi.events.jsonl` |

The `subagent-ask-reviewer` / `subagent-ask-reviewer-max-denies` pair is **escape-hatch-only**
— it is exposed by the composite action but **not** surfaced by the reusable workflow, so reach
for it only when you hand-roll a workflow against the action directly.

**Outputs** (kebab-case): `patch-path`, `summary-path`, `events-path`, `summary-json`
(compacted JSON — best-effort and size-bounded by the `$GITHUB_OUTPUT` cap; read
`summary-path` for anything large), `stop-reason`, `non-empty-diff`, and `exit-class`
(`clean` / `run-failure` / `setup-failure`, derived from the captured exit code). Branch on
`exit-class`, not the raw code — and remember exit 0 is **not** "task accomplished": read
`stop-reason` and `non-empty-diff` to judge whether real work landed.

The action bakes in a default `instructions` value (TRUSTED framing, emitted **outside** the
prompt fence): write the final message as a PR description, and self-verify (run the repo's
build/lint/test until green) before finishing. For that self-verification to run, the agent
needs the repo's build/lint/test tools on PATH — so the action itself now **always provisions
`task` (go-task) + golangci-lint** in the same job, before the binary (Go is provisioned for
the binary build). That makes the **80% Go case work out of the box** on both adoption paths
(the reusable workflow and a direct action reference). For a project toolchain beyond that
(buf, protoc plugins, node, system packages), pass the **`setup-script`** input — operator
shell the action runs in the same job, before the binary. `setup-script` is **trusted operator
code** (a maintainer sets it), distinct from the untrusted prompt, and runs at `contents: read`
with no write token, so it cannot push or open a PR. Override the `instructions` input to
replace the framing wholesale; an empty value omits it.

### Adopting via the reusable workflow (recommended)

The reusable workflow (`.github/workflows/mecatequi-reusable.yml`, `on: workflow_call`) lets
a consumer adopt mecatequi with a **thin caller** instead of vendoring the whole
split-privilege job graph plus the glue scripts. It is the same three jobs
(`acknowledge` / `implement` / `publish`) with the same per-job permissions, so the token
boundary is preserved — the agent job holds only the LLM key(s) and no write token; the
publish job holds the write token and runs no agent code.

A minimal caller in the consuming repo:

```yaml
name: mecatequi
on:
  issues:
    types: [labeled]
  issue_comment:
    types: [created]
permissions:
  contents: read
jobs:
  mecatequi:
    uses: stacklok/mecatl/.github/workflows/mecatequi-reusable.yml@v0.0.4
    permissions:
      contents: write
      pull-requests: write
      issues: write
    secrets:
      openrouter-key: ${{ secrets.OPENROUTER_CI_TOKEN }}
      # Publish token — wire ONE form (see the table). The JIT App-token form is strongest:
      publish-app-id: ${{ secrets.RELEASE_APP_ID }}
      publish-app-private-key: ${{ secrets.RELEASE_APP_PRIVATE_KEY }}
    with:
      label: ready-for-agent
      model: anthropic/claude-sonnet-4.6
      default-provider: openrouter
```

The caller grants the workflow the permissions its `publish` job needs (a called workflow's
token can only be **downgraded** from the caller's grant, never escalated), passes the
trigger label/mention as **inputs** (a reusable workflow can't reliably read the caller's
`vars.*`), and wires the secrets it has.

**The caller owns the `on:` triggers.** The reusable workflow declares only
`on: workflow_call` — it has **no** `issues` / `issue_comment` triggers of its own. The
caller's own `on:` block (the `issues: [labeled]` + `issue_comment: [created]` in the
example above) is what actually fires the run; the workflow's `if:` gates then match the
`label` / `mention` inputs. **Omit or mis-set those triggers and you get total silence** —
no run, no error — because nothing ever delivers an event to the reusable workflow.

**Pin the latest released tag, not `@v0.0.4`.** The `@v0.0.4` in every example here is
**illustrative**. Pin the **latest released tag** from the
[Releases page](https://github.com/stacklok/mecatl/releases) — a `uses:` ref that points at a
tag which does not exist (e.g. a reader landing between releases) fails with GitHub's generic
*"workflow not found"* error, which looks like a config bug but is just a stale ref.

**Secrets (`secrets:`)** — all optional; wire only what you use:

| Secret | Purpose |
|---|---|
| `openrouter-key` | `OPENROUTER_API_KEY` for the agent job. |
| `openai-key` | `OPENAI_API_KEY` for the agent job. |
| `anthropic-key` | `ANTHROPIC_API_KEY` for the agent job. |
| `publish-app-id` + `publish-app-private-key` | **Form 1 (strongest):** a JIT GitHub App installation token is minted in the publish job — no standing grant. |
| `publish-token` | **Form 2:** a pre-minted token (e.g. a fine-grained PAT) you manage. |
| *(none of the three)* | **Form 3:** the publish job falls back to the standing `GITHUB_TOKEN` grant with a `::warning::`. Zero-config, but the weakest form. |

An undefined provider secret is the **empty string**, which the binary treats as **absent**
(it registers a provider only for a non-empty key), so a caller wires only the provider(s) it
uses; `default-provider` forces the choice.

**If you set `default-provider`, wire THAT provider's key.** `default-provider` only *selects*
which provider to use — it does not supply a key. Set `default-provider: openrouter` but wire
only `openai-key`, and the run fails at startup with *"no LLM provider available"* (the
selected provider has no key, and the binary will not silently fall back to a different one).
The pairing is: `default-provider: openrouter` ⇒ `openrouter-key`; `default-provider: openai`
⇒ `openai-key`; `default-provider: anthropic` ⇒ `anthropic-key`. If you wire exactly one
provider key and omit `default-provider`, the binary auto-detects it, which is the simplest
correct setup.

**Setting up publish-token Form 1 (the recommended JIT GitHub App).** Form 1 mints a
short-lived installation token scoped to exactly this repo — no standing PAT to manage or
leak. To set it up:

1. **Create a GitHub App** (org or personal): *Settings → Developer settings → GitHub Apps →
   New GitHub App*. Give it a name; you can leave the homepage/webhook fields blank and
   **disable the webhook** (the App is used only for token minting, not event delivery).
2. **Grant repository permissions** — under *Permissions → Repository*, set **Contents:
   Read and write**, **Pull requests: Read and write**, and **Issues: Read and write**
   (these are exactly what `publish.sh` needs to push the branch, open the PR, and comment).
3. **Create a private key** for the App (*General → Private keys → Generate a private key*) —
   download the `.pem`.
4. **Install the App** on the target repo (*Install App → choose the repo*). Installation is
   what scopes the minted token to that repo.
5. **Wire the two secrets in the consuming repo**: store the App's **App ID** as the
   `publish-app-id` secret and the **`.pem` contents** as `publish-app-private-key`. The
   publish job mints the installation token from them in-job.

The minting itself happens inside the reusable workflow's `publish` job (via
`actions/create-github-app-token`); you only supply the two secrets.

**Inputs (`with:`)** — all optional with sane defaults: `label` (default `mecatequi`),
`mention` (default `@mecatequi`), `model`, `default-provider`, `posture` (default `auto`),
`max-run-tokens`, `max-turns` (per-run turn cap; empty uses the deployment default),
`timeout` (default `40m`), `openai-base-url` (for an OpenAI-compatible
endpoint), `guardrails-model` (issue #27 checker model; empty disables), `setup-script`
(multi-line shell run before the binary to install a project toolchain beyond the always-on
`task` + golangci-lint — see the toolchain note above), `base-branch`, `pr-body-template`,
`pr-title-template`. The escape-hatch-only knobs (`default-model`,
`openai`, `subagent-ask-reviewer`) are deliberately **not** exposed by the reusable workflow —
a consumer that needs them vendors the example template instead.

**Org-access requirement.** Because `stacklok/mecatl` is private, the consuming org must
allow Actions to use its actions/workflows: **Settings → Actions → General → Access** on
`stacklok/mecatl` (or `gh api -X PUT
repos/stacklok/mecatl/actions/permissions/access -f access_level=organization`). No token or
PAT is configured in the consuming repo — the org setting is the only requirement.

**If that access is NOT enabled, the caller fails with a generic *"workflow not found"* /
access error** at the `uses:` resolution step — which reads like a typo in your YAML but is
actually the org setting. The fix is the **Access** setting above, not your caller workflow.
This is the same surface as the stale-tag failure mode (see "Pin the latest released tag"),
so check both when a `uses:` ref will not resolve.

**Create the trigger label first** (and set `label` / `mention` if you renamed them) — the
`labeled` trigger silently never fires for a label that does not exist.

If you need to customise the job graph itself — a custom permission-check gate job, an extra
approval stage, a different trigger — use the **vendor-the-directory escape hatch** below
instead.

### The example workflow (`.github/workflows/mecatequi-example.yml`)

The template is the **escape hatch** — the canonical **split-privilege** pattern you vendor
and edit when the reusable workflow's fixed job graph is not enough — *the step that can write to
GitHub never runs agent code; the step that runs agent code never holds a write token* —
plus an `acknowledge` job that guarantees the issue always carries a trace:

- **`acknowledge`** (`issues: write` only, no agent code, no LLM key): runs **first**,
  gated on the same trigger as `implement`, and posts an early *"🤖 mecatequi is working on
  this — see the run: …"* comment with the run URL. This guarantees a durable issue-side
  signal **even if everything downstream fails** — the failure mode where a skipped or
  failed publish left the issue author staring at silence.
- **`implement`** (`needs: acknowledge`; `contents: read`, no write, no id-token):
  checkout → `author-gate.sh` (defense-in-depth permission check, see the gate note below) →
  `extract-prompt.sh` writes the **untrusted** issue/comment text into a file via `jq`
  over `$GITHUB_EVENT_PATH` (never an inline `${{ }}`) → `uses: ./.github/actions/mecatequi`
  with `OPENAI_API_KEY` as the **only** secret, delivered at **job** level (step `env:` on a
  `uses:` step would not reach the composite's internal steps) → upload-artifact the
  patch/summary/events.
- **`publish`** (`needs: [acknowledge, implement]`; `contents: write` +
  `pull-requests: write` + `issues: write`): download-artifact (**non-fatal** — a missing
  artifact must not abort before `publish.sh` runs) → `publish.sh` applies the patch as
  **data** (`git apply`) → branch → commit → PR, or posts an honest failure comment.
  Its `if:` fires on implement **success or failure** (`!cancelled() &&
  needs.acknowledge.result == 'success'`) so a failed run still gets a terminal comment;
  `publish.sh` reads a missing/empty `EXIT_CLASS` as setup-failure, and a failed push /
  `gh pr create` comments before exiting non-zero (never a silent abort after deciding to
  open a PR). It runs no agent output as code.

Trigger: `issues` `labeled` with the `mecatequi` label, OR `issue_comment` `created`
mentioning `@mecatequi` on an issue (PR comments are excluded). Every external action is
SHA-pinned with a `# vX.Y.Z` comment, the workflow default is `permissions: contents:
read`, both job checkouts pin the immutable `github.sha` (so the patch applies onto the
tree it was diffed against), and a `concurrency` group keyed on the issue number prevents
overlapping runs.

**The author gate (read this — `author_association` is a trap).** The gate that keeps an
unauthorised author out is **trigger-based**, not `author_association`-based. GitHub's
webhook `author_association` is **unreliable for membership** — it reports an org MEMBER as
`CONTRIBUTOR`, so a gate that asserts `author_association ∈ {OWNER, MEMBER, COLLABORATOR}`
silently **skips legitimate runs**. The **live** workflow for this repo
(`.github/workflows/mecatequi.yml`) therefore removed that assertion entirely:

- For a **private repo**, the trigger **is** the gate: applying a label needs triage/write
  access and commenting is team-only, so GitHub's own permission model decides who can
  start a run. No `author_association` check is needed.
- For a **public repo**, do **not** trust `author_association`. Add a dedicated
  permission-check gate **job** that calls the `collaborators/{user}/permission` API and
  gates `implement`/`publish` on its result.

`author-gate.sh` (which asserts `author_association`) ships in the **example template
only** as a defense-in-depth illustration; it is deliberately absent from the live
workflow. See the [mecatequi design doc](adr/0028-mecatequi.md).

The action and its scripts are meant to be **vendored** — copied into your repo and
reviewed, not referenced by tag — so you control exactly what runs. To enable it:

1. Copy `.github/workflows/mecatequi-example.yml` and the whole
   `.github/actions/mecatequi/` directory into your repo.
2. Add the `OPENAI_API_KEY` repository secret (the agent job's only secret).
3. **Create the `mecatequi` label FIRST.** You cannot apply a label that does not exist,
   and the `labeled` trigger **silently never fires** without it — so the label must exist
   before anyone tries to apply it, or the workflow simply does nothing with no error.
4. Review the posture (`auto` is the documented CI default) and decide whether to keep the
   broad `GITHUB_TOKEN` in the `publish` job or upgrade to a JIT GitHub App token (the
   stronger option — see `docs/adr/0028-mecatequi.md`).
5. Apply the `mecatequi` label to a test issue and watch the run.

**Configurable trigger label / mention.** The trigger label and comment mention default to
`mecatequi` and `@mecatequi`, but both are overridable via **repo Actions variables** so you
can rename them without editing the workflow: set `MECATEQUI_LABEL` and `MECATEQUI_MENTION`
under **Settings → Secrets and variables → Actions → Variables**. Both job gates
(`acknowledge` and `implement`) read the same variables, so they cannot drift. If you change
`MECATEQUI_LABEL`, create the new label first (step 3 above — the `labeled` trigger silently
never fires for a label that does not exist).

### Adopting mecatequi in another repo (by tag, without the reusable workflow)

> Most consumers should use the **reusable workflow** ("Adopting via the reusable workflow
> (recommended)" above) — a thin caller, no vendored scripts. This subsection covers the
> lower-level path of referencing the **composite action** by tag directly, for a workflow
> you hand-roll yourself.

The example workflow vendors the action (copies `.github/actions/mecatequi/` into your
repo). To instead **reference mecatl's published action by tag** — no vendored copy — point
`uses:` at the subdirectory action and pin a tag. GitHub checks the mecatl repo out at that
tag into the action path and the action builds the binary from it (the matlatl pattern), so
there is **no token and no `GOPRIVATE`** to manage:

```yaml
- name: mecatequi
  id: mecatequi
  uses: stacklok/mecatl/.github/actions/mecatequi@v0.0.4   # pin the latest released tag
  with:
    prompt-file: ${{ runner.temp }}/prompt.txt
    posture: auto
```

`owner/repo/path@ref` is the GitHub syntax for an action that lives in a repository
subdirectory; pin it to an alpha `v0.0.x` tag (the first published tag is `v0.0.1`) or a
full commit SHA — the `@<ref>` is the version (there is no version input). What a consumer
needs:

- **Org access to mecatl's actions.** Because `stacklok/mecatl` is private, the org must
  allow Actions to use its actions: **Settings → Actions → General → Access** on
  `stacklok/mecatl` (or `gh api -X PUT
  repos/stacklok/mecatl/actions/permissions/access -f access_level=organization`). No token
  or PAT is configured in the consuming repo — the org setting is the only requirement.
- **`OPENROUTER_API_KEY`** (or `OPENAI_API_KEY`, etc.) — the LLM key, set as a repository
  secret and injected at the **agent job's** `env` level (not on the `uses:` step).
- **Create the trigger label first** (and, if you renamed it, set `MECATEQUI_LABEL` /
  `MECATEQUI_MENTION`) — see the enable steps above.

Honest cost: the action builds the binary from source on each run (no cached release asset
yet). A signed, checksummed binary / container is the later speed optimization — see
`docs/adr/0028-mecatequi.md` §5.

### Customising the PR description (templates)

By default `publish.sh` writes a rich built-in PR body (caveat + "What the agent did" +
"Files changed" + "Run" table + run link + `Closes #<n>`). To use your own style, supply a
template file of `{{placeholder}}` tokens. Resolution, in order:

1. The `MQ_PR_BODY_TEMPLATE` env on the `Publish` step (a path relative to the checkout).
2. Else `.github/mecatequi/pr-body.md` in the checkout — **the zero-config convention**.
3. Else the built-in body (absent template = today's behaviour, unchanged).

Easiest activation: copy the shipped `.github/mecatequi/pr-body.md.example`, edit it, and
**rename it to `.github/mecatequi/pr-body.md`** — no workflow change needed. An optional
`MQ_PR_TITLE_TEMPLATE` overrides the PR title (same placeholders). The **default** title is
`<issue title> (#<n>)` — the triggering issue's title, fetched READ-only via `gh issue view`
in the privileged publish job (the agent job holds no GitHub token), falling back to the prior
`mecatequi: changes for issue #<n>` literal when the title can't be fetched. In a title, use
the **short** placeholders (`{{issue_ref}}`, `{{issue_title}}`, `{{stop_reason}}`,
`{{branch}}`) — prose ones like `{{what_agent_did}}` or `{{summary_table}}` flatten to one
unwieldy line.

Placeholders: `{{what_agent_did}}`, `{{files_changed}}`, `{{summary_table}}`, `{{run_url}}`,
`{{issue}}`, `{{issue_ref}}`, `{{issue_title}}`, `{{stop_reason}}`, `{{non_empty_diff}}`, `{{diff_bytes}}`,
`{{total_tokens}}`, `{{branch}}`, `{{base}}`. An unknown `{{token}}` is left intact.

Two things to know: the `⚠️ Agent-authored — review carefully before merging.` caveat is
**always** force-prepended (a template cannot drop it — so do **not** add your own ⚠️ caveat
line in the template, or you get a duplicate), and **issue linkage is yours** in a custom
template — use `{{issue_ref}}` with `Closes`/`Refs` (the built-in default uses `Closes`).
Substitution is literal and single-pass over agent-authored (untrusted) values — see
`docs/adr/0028-mecatequi.md` for the mechanism and the full rationale.

### Injection safety (why it is built this way)

Untrusted issue text never appears in a `${{ }}` interpolation inside a `run:` body or as
an argv token. Extraction is `jq` over the event JSON file into a file; the file reaches
the binary via `--prompt-file`; the binary fences it. Every event-derived value
(author association, issue number, paths) is passed via `env:`. The produced patch is
applied as data, never executed. **Defense in depth:** every agent-facing Bash shell runs
with a secret-scrubbed environment (`internal/adapter/envscrub` — the harness's
provider/auth/forge credentials are dropped before the shell sees them), so a hijacked
agent cannot `echo $OPENROUTER_API_KEY` / `cat /proc/self/environ` to exfiltrate them; and
the workflow still bounds the blast radius by holding only the rotatable LLM key (no write
token) in the agent's job. See `docs/adr/0028-mecatequi.md` §6.

---

## 11. Live e2e suite

A live, ginkgo-driven end-to-end suite lives under `e2e/` (build tag `e2e` —
`task build`/`task test`/`task lint` never compile it). It spawns
`bin/mecated` against **OpenRouter** and drives real model runs over the gRPC
`Converse` stream, so it costs (a little) real money and needs the network.
Full detail — scenarios, environment knobs, the permission posture, the
prompt-filter caveats — is in `e2e/README.md`.

Run it locally:

```sh
# Export the key yourself, in-shell, from wherever you keep it — the Taskfile
# and the suite never read a key file, and the key must never appear on a
# logged command line.
export OPENROUTER_API_KEY=...
task e2e
```

What the scenarios prove (event-stream / side-effect assertions, never model
prose): a provider smoke canary, global + workspace skill activation, parallel
`Subagent` fan-out, the `Parallel` construct, a 3-member team with recorded
findings, `/metrics` counters, user + project memory writes, and the soul
composition fact.

Artifacts: every run writes JSONL transcripts per scenario plus the captured
`mecated.log` under `.scratch/e2e-<timestamp>-<rand>/artifacts/` (the scratch
root is kept on failure — the transcripts are the deliverable of a failed
run). A failing spec attaches a self-diagnosing report (network vs model vs
harness classification, transcript path, usage); the suite ends with a
cumulative token/cost estimate.

Remote target (the future-cloud env contract): point `MECATL_E2E_TARGET` at an
existing `host:port` to skip the local spawn; `MECATL_E2E_WORKSPACE` (required)
is the absolute workspace root on the server host, `MECATL_E2E_METRICS_URL`
enables the metrics spec, `MECATL_E2E_AUTH_TOKEN` supplies a bearer token.
Remote runs write artifacts to `.scratch/e2e-artifacts/`.

In CI the suite runs as the **non-blocking** `e2e-live` workflow (nightly +
manual dispatch + the `e2e-live` PR label) — see
`.github/workflows/README.md`.

---

## 12. Troubleshooting / FAQ

**`no LLM provider available: set one of ANTHROPIC_API_KEY (Claude), OPENAI_API_KEY (OpenAI), or OPENROUTER_API_KEY (one key, many models — a good first choice) …`**
You started `mecated` with no provider key in the environment. Set
`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or `OPENROUTER_API_KEY`; for a
compatible/proxy endpoint pass the matching key plus `--openai-base-url` /
`--anthropic-base-url` / `--openrouter-base-url`; or pass `--mock` for an
offline smoke test. (The full message is quoted in §3, "Provider selection".)

**`--openai requires OPENAI_API_KEY to be set`** (the `cmd/mecademo` demo only)
The demo's `--openai` flag was passed but no key is in the environment.
`export OPENAI_API_KEY=…`. (`mecated`/`mecatui` instead auto-detect the provider
from the environment and emit the `no LLM provider available: …` message above when
no key resolves.)

**`bind: address already in use`**
Another `mecated` (or process) holds the port. Pick free ports with
`--http-addr` / `--grpc-addr`, or stop the other process.

**WARN: "API bound to a NON-loopback address …"**
You bound something other than `127.0.0.1` / `localhost`. The API is
unauthenticated and exposes command/file execution. Bind loopback, or put a real
trust boundary (auth/mTLS proxy, network policy) in front of it.

**Edit fails: "you must have read the file with the Read tool this session …"**
`Edit` enforces read-before-edit: the file must have been read this session and
be unchanged since. Have the model `Read` the file (again) and retry the edit.

**Tool call hangs / never completes**
It is probably a `permission.ask` awaiting approval. Watch the SSE/event stream
for `permission.ask` and resolve it with `POST …/approve` (or a `ResumeApproval`
frame over gRPC). Denied calls return the reason to the model.

**The run "won't stop" / loops**
It can't run unbounded: default limits cap it (`max_turns=2000`,
`max_tool_calls=8000`, `max_consecutive_failures=5`). The terminal `result.stop`
tells you which limit fired (`max_turns`, `max_tool_calls`,
`max_consecutive_failures`). Tighten them per session via `limits`.

**`404 no in-flight run for session`** on approve/cancel
There is no active run for that session id — the run already finished, was never
started, or you used the wrong id. Approve/cancel only work while the prompt's
SSE stream is open.

**Reading the JSONL replay log**
With `--store-dir DIR` (for `mecatui`, the per-workspace default under
`$XDG_STATE_HOME/mecatui/sessions/<path-slug>/`), inspect a session after the fact
— note these files hold the **raw conversation in plaintext**:

For `mecatui` you do not pass `DIR` — find your per-workspace store under the
default base and pick the subdir matching your workspace (its name is the
workspace path with `/` replaced by `-`):

```console
$ ls ~/.local/state/mecatui/sessions/   # each subdir is one workspace
-var-home-ozz-dev-mecatl   -home-ozz-scratch
```

```console
$ ls DIR
8867….session.jsonl   8867….tools.jsonl   8867….events.jsonl

# Latest session snapshot (last line wins):
$ tail -n1 DIR/8867….session.jsonl | jq .

# Every tool call with its result and duration:
$ jq . DIR/8867….tools.jsonl

# The relayed event timeline (reasoning, ask/verdict pairs, delegation lifecycle):
$ jq . DIR/8867….events.jsonl
```

## See also

- [Architecture guide](architecture.md) — how the harness works under these flags: layers, the loop, ports, the API surface.
- [mecatui terminal UI](tui.md) — the terminal client for the server this guide runs.
- [ADR 0001 — the ACP adapter](adr/0001-acp-adapter.md) — the editor (`--acp`) wire surface in depth.
