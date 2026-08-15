## 3. Running the server (`mecated`)

`mecated` is a composition root: it parses flags/env, then delegates the
assembly — an LLM provider, the seven-tool catalog plus a read-only-explorer
`Subagent` delegation tool (which gets a full shell inside an isolated git worktree when Bash
is configured), the permission policy, lifecycle hooks, the session store, and the
two-layer system prompt — to the shared composition package (`app.Build`),
and serves the resulting `HarnessService` over gRPC and HTTP/SSE concurrently.
(The TUI reuses that same `app.Build` to host an embedded server — see below.)

```console
$ export OPENAI_API_KEY=sk-...
$ go run ./cmd/mecated serve --openai --workspace "$PWD"
```

### Getting help

`mecated` provides progressive, mode-specific help:

- Bare `mecated` with no command word prints the top-level help to stderr and
  exits 2 (usage error); a leading `--help`/`--help-all` prints help and
  exits 0.
- `mecated serve --help` — task-oriented common flags (~20 most-used flags)
  grouped by user task (workspace & session, provider, permissions, tools,
  MCP, etc.).  Includes a pointer to `--help-all` for the full reference.
- `mecated serve --help-all` — exhaustive reference listing every registered
  public flag with its registered name, default, and description.  Exits 0; does
  not start a listener.
- `mecated acp --help` — ACP-specific common flags only (no server-boundary
  listener/TLS/rate-limit/metrics/OTLP/driver flags).
- `mecated acp --help-all` — full ACP flag reference (excludes server-boundary
  flags, which the `acp` command does not serve).
- `mecated --help` — concise command entry page listing available subcommands.
- `mecated --help-all` — the exhaustive serve-compatible flag reference (every
  public flag a `mecated serve` invocation accepts), plus a note pointing to
  `mecated acp --help-all` for the ACP-scoped subset.
- `mecated mcp login SERVER [--no-browser] [--permission-config PATH ...]` — authorize one operator-configured
  OAuth server backed by a mutable local credential store. Login uses the same
  conventional operator settings and explicit-file precedence as `serve`; the
  repeatable `--permission-config` selects trusted config files and never carries an
  OAuth value. This is the only command
  that installs an OAuth presenter; `--help` performs no settings, environment,
  browser, listener, or network work.
- `mecated import --help` — offline Codex/Claude Code session, skill, and
  workspace-file import flags. The import does not start a listener or provider.

### Importing local agent work

`mecated import` converts a Codex or Claude Code JSONL transcript into an idle,
provider-neutral Mecatl session in the local JSONL store:

```console
$ go run ./cmd/mecated import \
    --from claude-code \
    --session ~/.claude/projects/<project>/<session-id>.jsonl \
    --store-dir ~/.local/share/mecatl/sessions \
    --workspace ~/work/imported-project \
    --copy-files \
    --skills
```

Only user and assistant text is seeded into the conversation. Provider-private
reasoning and tool-call records are omitted so the history remains valid when a
different Mecatl provider resumes it. `--copy-files` never overwrites existing
paths and skips `.git`, symlinks, and special files. `--skills` copies
conventional Agent Skills bundles to `<workspace>/.mecatl/skills`; repeat
`--skills-dir` to add explicit sources. TRUST BOUNDARY: imported `SKILL.md`
files steer the model like `AGENTS.md`/`CLAUDE.md` — only import skills from a
trusted source, the same way you would point `mecated serve --skills-dir` at a
trusted directory. Start `mecated serve` with the same `--store-dir`, and
explicitly enable the imported skills directory.

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
| `--auth-file` | `""` | path to a YAML credentials file (`providers.<name>.api_key` for `anthropic`/`openai`/`openrouter`/`opencode`, or file-only `providers.openai-codex.oauth`); overrides the conventional default `$XDG_CONFIG_HOME/mecatl/auth.yaml` (usually `~/.config/mecatl/auth.yaml`, a `settings.yaml` sibling). See [Credentials file](#credentials-file-authyaml) below — an environment variable wins over the file only for the API-key providers; `openai-codex` has no environment alias. |
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
| `--schedule-store-url` | `""` | `host:port` of a remote **schedule-store gRPC driver** (`mecatl.driver.v1.ScheduleStoreService` + `ScheduleOneShotReArmerService`) for the durable schedule registry (scheduled tasks); **INDEPENDENT of the session store** — when set, replaces the `ScheduleStore()` discovery from the configured store. Empty keeps the byte-identical default (the configured store's own `ScheduleStore()` accessor, or no scheduling). The driver's `Claim`/`ClaimNow`/`ReArmOneShot` run the atomic advance server-side. Same auth/TLS posture as `--session-store-url` (equal URLs share one connection). |
| `--child-retention` | `168h` | how long persisted **child** session snapshots (`subagent-*`/`parallel-*`/`team-*` ids — the `InspectSubagent`/`resume:` handles) are retained before the GC sweep deletes them. **Main sessions are governed by `--main-retention` instead** (default off). Durable-store-only in effect (`--store-dir` or a prunable `--session-store-url` driver; the in-memory default never accumulates across restarts). `0` disables the age pass. |
| `--child-retention-max-per-family` | `500` | max persisted child snapshots kept **per delegation family** (subagent/parallel/team); the oldest beyond the cap are deleted, skipping in-flight runs. `0` disables the cap. |
| `--main-retention` | `0` | how long persisted **main** (top-level operator/service) session snapshots are retained before the GC sweep deletes them; child sessions use `--child-retention` instead. Durable-store-only. `0` (default) **disables** the main age pass entirely, so main sessions are never touched — `mecated`'s behaviour is unchanged unless you opt in (`mecatui` defaults it on for its durable per-workspace store). |
| `--main-retention-max-total` | `0` | max persisted **main** session snapshots kept **store-wide** (a single global cap, not per-family); the oldest beyond the cap are deleted, skipping in-flight runs. Durable-store-only. `0` (default) disables the cap. |
| `--child-gc-interval` | `1h` | how often the session retention GC re-sweeps after the startup sweep; `0` = sweep at startup only. Only meaningful when a child or main retention/cap knob is active. |
| `--no-scheduler` | `false` | **Scheduled tasks:** disable the in-process scheduler tick loop. The scheduler is **ON by default** on `mecated` when its `--store-dir` JSONL store is configured; a store with no `ScheduleStore` (the in-memory default) never ticks. With `--no-scheduler` the create/list/fire API and the in-chat `Schedule` tool still work — manual management is independent of the tick loop. See [ADR 0059](../adr/0059-scheduled-tasks.md) + [ADR 0073](../adr/0073-schedule-tool.md). |
| `--scheduler-tick-interval` | `30s` | how often the tick loop polls `ScheduleStore.Due`; `0` = the 30s default. Inert under `--no-scheduler` or a store with no `ScheduleStore`. |
| `--scheduler-min-interval` | `1m` | the frequency floor the create-seam enforces (a schedule whose cadence is tighter than this is rejected, fail-closed — by BOTH the in-chat `Schedule` tool and the REST/gRPC create). Defaults to `1m` so an on-by-default scheduler + the floor-Allow `Schedule` tool cannot mint an unbounded tight-cadence recurring fire out of the box; set it explicitly to tighten, or to `0` to disable the floor. |
| `--scheduler-max-concurrent-fires` | `4` | max schedules fired in parallel per tick. Inert under `--no-scheduler` or a store with no `ScheduleStore`. |
| `--schedule-fire-retention` | `7d` (168h) | how long persisted `sched--`-prefixed fire-session snapshots are retained before the GC sweep deletes them (a distinct family from `--child-retention`/`--main-retention`); a LIVE fire (one mid-run) is never deleted. The **conditional default**: the flag reads `0`, but when unset the harness applies `7d` so a durable store does not grow without bound; an explicit `0` disables the pass — fire sessions are never swept. Only meaningful with a durable store (`--store-dir` or a prunable `--session-store-url` driver). |
| `--schedule-fire-retention-max-total` | `0` (off) | max persisted `sched--`-prefixed fire-session snapshots kept store-wide; the oldest beyond the cap are deleted, skipping in-flight fires. The symmetric peer of `--main-retention-max-total` for the schedule-fire family: the age horizon (`--schedule-fire-retention`) bounds the tail, this cap bounds the head. Durable-store-only. `0` (default) disables the cap. |

> **In-flight fires + the per-fire wall-clock deadline (issue #386, [ADR 0097](../adr/0097-scheduled-fire-inflight-state.md)).** A fire is observable while it runs: `ScheduleQuery inspect` / the `/schedule` overlay / the wire surface show a claimed fire as `in-flight: claimed (session pending)` then `in-flight` with start time, last-progress time, and deadline — never an ambiguous "no fires". Each fire has a wall-clock deadline set by the schedule's `fire_timeout` spec field (a 30-minute default when unset); a fire that runs past it terminates with a `timeout` stop reason (distinguishable from a manual cancel) and its session stays recoverable. A crashed fire is reconciled to a terminal record after restart instead of sitting `pending` forever. |
| `--session-lease-url` | `""` | `host:port` of a remote **session-lease gRPC driver** (`mecatl.driver.v1.SessionLeaseService`) for **cross-process single-writer enforcement** (multi-replica). Empty = **NO leasing** (the byte-identical single-writer-by-affinity default). Mutually exclusive with `--session-lease-dir` / `--session-lease-k8s-namespace`. Same auth/TLS posture as `--session-store-url`. **See the session-leasing note below.** |
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
| `--user-model-dir` | `""` | directory for the user-scoped, **cross-project** user-model store of durable FACTS about the operator (empty → the conventional `$XDG_CONFIG_HOME/mecatl/usermodel`, fallback `~/.config/mecatl/usermodel`). Exposes the user memory tools and the live bounded operator profile in the volatile system suffix. **See the user-model note below.** |
| `--no-user-model` | `false` | disable the user model entirely (explicit tools and live operator profile). |
| `--user-model-review` | `false` | deprecated compatibility alias for operator `learning.mode: auto`; runs the synchronous completed-trajectory user-model reviewer after eligible clean completions and never reopens the user session. |
| `--user-model-review-interval` | `1` | process-wide completed-session debounce for `learning.mode: auto` and the compatibility alias (1 = every eligible completion). |
| `--user-model-consolidate-interval` | `0` | independently authorize process-wide background consolidation (dream) of the cross-project user-model store's `user/` namespace; 0 disables. A project `learning.mode: off` cannot suppress a positive operator schedule. |
| `--permissions-conventional` | `true` | auto-discover the per-project permission config (`<workspace>/.mecatl/settings.yaml`, and with `--import-claude-permissions` also `<workspace>/.claude/settings.json`) plus the user-global file. **Re-resolved per session** against each session's workspace root. ON and inert until such a file exists. **See the permission-config note below.** |
| `--import-claude-permissions` | `false` | also import Claude-Code `settings.json` permissions (project + user). **Lossy** (fail-safe): see the table below. |
| `--trust-project` | `false` | honour the discovered **project authority set**: the project's ALLOW rules (its deny/ask are always honoured regardless), its project persona/soul at `<workspace>/.mecatl/soul.md`, AND the **project tier** of agent definitions, slash commands, skills, and project rules (`.claude/rules`, issue #329) (`<workspace>/.mecatl/*`, `<workspace>/.claude/*`). It also gates the **read-only subagent/team-member shell**: on an untrusted workspace, Subagent children and read-only members run Bash-less (Read/Grep/Glob only — creating their worktree runs a `git` checkout over the repo's `.git`, where a tracked `.gitattributes` can name filter drivers that execute code with nobody having run anything); mutating members and Parallel branches keep their hardened shells (force-copy forks are created by a pure file copy with no git invocation, and their git afterwards runs over the copied repo — the same exposure as the operator's own session). OFF by default (the safe stance) — an untrusted repo's grants, persona, agents, commands, skills, rules, and subagent shell are withheld; the agent still runs in "ask the human" mode (see the workspace-trust note below). **See the permission-config, persona/soul, and workspace-trust notes below.** |
| `--permission-config` | `""` | path to a YAML permission-config file loaded at the **user (fully-trusted) scope** (**repeatable**). Always loaded regardless of `--permissions-conventional`. |
| `--posture` | `strict` | **OPERATOR POSTURE LADDER.** One ordered tier governs the whole prompt/trust posture: `strict` (default, fail-closed: prompt for the mutate-ask floor, no posture-derived trust) → `trusted` (interactive roots trust the project; still prompts) → `auto` (allow-all main + children, child prompt-injection defence ON) → `yolo` (also loosens child substitution; defence OFF). `--yolo` and `--trust-project` are aliases; the higher tier wins. On a **headless** root posture never raises `TrustProject`: explicit `--trust-project`, `trustedWorkspaces:`, or undrifted remembered trust admits BOTH repo steering and the read-only child shell; without a trust source, auto/yolo gets neither. On an interactive root, trusted/auto/yolo retain the historical trust floor. The resolved tier and the root-aware `trust_project`/`project_ingestion` decision are emitted as the structured `operator posture` startup diagnostic by `mecated serve` (once, before serving). See [ADR 0095](../adr/0095-root-aware-project-trust.md) and [ADR 0096](../adr/0096-diagnostic-only-posture-reporting.md). |
| `--reasoning-effort` | `auto` | **OPERATOR REASONING-EFFORT TIER** ([ADR 0055](../adr/0055-reasoning-effort.md)). The default reasoning depth for every session: `auto` (default — unset; do **not** send a reasoning-effort field, so the provider's own default applies) or one of `low`/`medium`/`high`/`xhigh`/`max`. **OpenAI** supports `low`/`medium`/`high` only, so `xhigh`/`max` are **clamped down to `high`** (with a `WARN` naming the requested and clamped-to values); **Anthropic** maps all five. Empty = unset (honours the operator-global `reasoning-effort:` setting if present). A per-session `CreateSession.reasoning_effort` **out-ranks** this default. A model the catalog/live source says has **no** reasoning support drops the effort (with a `WARN`); an unknown model fails open (sends it). Operator-tier only: a project-tier `reasoning-effort:` key is ignored with a `WARN` (a project cannot raise the model's reasoning spend). CLI out-ranks the user-global setting. An unknown value fail-softs to unset with a `WARN`. It binds the agent and its subagents, never the harness's internal classifier calls. **Mid-conversation change:** a running session's effort is changed by *forking* it — `ForkSession` with a `reasoning_effort` override (ADR 0068) creates a peer session on the new tier that **keeps the transcript** (the mecatui `/effort` picker does this; provider/model always inherit). |
| `--no-prompt-cache` | `false` | **PROVIDER-SIDE PROMPT CACHING** ([ADR 0100](../adr/0100-provider-prompt-caching.md)) is **ON by default**: Anthropic gets a 4-slot `cache_control` breakpoint budget over the growing conversation (not just the system prompt), and OpenAI/OpenRouter get `prompt_cache_key` (+ OpenAI's model-gated `prompt_cache_retention`, or OpenRouter's `cache_control` field). Pass `--no-prompt-cache` to disable it entirely — every adapter's cache dialect degrades to `None` and Anthropic drops its three new breakpoints, reproducing the pre-ADR-0100 wire exactly (only the pre-existing StablePrefix breakpoint survives). The dialect is gated on `(provider id, resolved base URL)`, never the id alone, so pointing `--openai-base-url` at a non-canonical compatible endpoint (vLLM/LiteLLM) already gets `None` — see [openai-compatible.md](./openai-compatible.md). |
| `--anthropic-cache-ttl` | `""` (API default, `5m`) | TTL stamped on **every** Anthropic ephemeral `cache_control` breakpoint (uniform across all 4 slots — the rule that makes the documented TTL-ordering 400s unreachable). Accepts `5m` or `1h`; empty (default) omits the `ttl` field entirely, so the API's own 5-minute default applies. Any other value is ignored with a `WARN` (fires at most once per process). No effect on OpenAI/OpenRouter/openaichat (Anthropic-only). |
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

#### Delegation & sub-agents

mecatl ships three delegation tools that each spin up an **isolated read-only
child loop** (full shell inside a per-child git worktree when Bash is configured —
see the architecture doc): **Subagent** (one isolated child, returns its result),
**Parallel** (N isolated branches fanned out, joined `all`/`first`/`judge`), and
**Team** (a coordinating crew over a shared task list, findings ledger, and
mailbox). See the delegation-capabilities note below.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--max-run-tokens` | `0` (**unlimited**) | **Default: unlimited** (`0` disables the brake). Maximum **cumulative input + output tokens per agent run**. A run that crosses it ends cleanly with `stop=budget` (terminal `StopBudget`). The budget is **inherited by every Subagent / Parallel branch / team member**, so a delegation fan-out cannot blow past it. Resuming a Subagent resets its turn and tool-call counters but preserves its cumulative token usage, so `resume` does not replenish this budget. Opt in by passing a positive value. |
| `--max-team-tokens` | `0` (**unlimited**) | **Default: unlimited** (`0` disables the brake). Maximum **cumulative input + output tokens per team run**, summed across **all members and rounds**. When crossed the team stops scheduling new rounds — the **in-flight round and the lead's synthesis still complete**, and the report states the budget stop. Applies to the `Team` tool and gRPC `CreateTeam`; a per-call Team `max_team_tokens` may only **tighten** it, and so may the wire `CreateTeamRequest.max_team_tokens` (HTTP: `"max_team_tokens"` in the create body). The outcome (incl. `budget_exhausted` and the `"budget"` stop) rides the **terminal `TeamEvent.outcome` frame** both `RunTeam` surfaces (gRPC stream + HTTP SSE) end with. **Orthogonal** to `--max-run-tokens` (per-run; both compose). |
| `--enable-parallel` | `true` | register the **Parallel** fan-out tool (N parallel isolated child branches). On by default; `=false` disables it. *(Renamed from the former `--enable-fork`.)* |
| `--websearch` | `""` (on) | **WebSearch** **master switch**: web search is **ON by default** (the Exa anonymous tier — no key, no config). Pass `--websearch=off` to **disable** it entirely (the kill switch — no outbound search calls; the tool reports it is disabled). Any value other than `off` (or unset) leaves web search enabled. Mirrors `--guardrails`. |
| `--websearch-url` | `""` | **WebSearch explicit override**: base URL of a vendor-neutral HTTP JSON search endpoint (e.g. a [SearXNG](https://docs.searxng.org/) `/search` URL or any generic JSON search API). When set it **wins over** the `SEARXNG_URL`/`BRAVE_API_KEY` env tiers **and** the Exa default. The API key is read from the **`WEBSEARCH_API_KEY`** env var (a secret, never a flag value). The adapter carries its **own** per-call timeout (10s) and concurrency limit (4), so the read-parallel dispatcher cannot launch unbounded egress. **Backend ladder + walkthrough: see [Enabling web search](#enabling-web-search) below.** |
| `--websearch-auth-header` | `"Authorization"` | HTTP header the `WEBSEARCH_API_KEY` is sent in (for `--websearch-url`) — default `Authorization` as a `Bearer` token; set e.g. `X-API-Key` to send the raw key. The secret rides the **header only, never the query string**. Ignored when no key is set. |
| `--websearch-query-param` | `"q"` | URL query parameter the search string is placed in (for `--websearch-url`). Tune for a generic JSON search endpoint that expects a different parameter name. |
| `SEARXNG_URL` *(env)* | `""` | **WebSearch SearXNG tier**: point at a **self-hosted** [SearXNG](https://docs.searxng.org/) `/search` URL to switch the backend to SearXNG (no API key). Wins over `BRAVE_API_KEY` and the Exa default; loses to `--websearch-url`. |
| `BRAVE_API_KEY` *(env)* | `""` | **WebSearch Brave tier**: a [Brave Search API](https://brave.com/search/api/) key switches the backend to Brave (sent in the `X-Subscription-Token` header against the Brave Web Search endpoint). The key is **never logged**. Wins over the Exa default; loses to `SEARXNG_URL`/`--websearch-url`. |
| `EXA_API_KEY` *(env)* | `""` | **WebSearch Exa paid tier**: an [Exa](https://exa.ai/) key upgrades the **default** Exa backend from the anonymous tier to the paid tier (appended as `?exaApiKey=` to the Exa endpoint, escaped). The key is **never logged**. Without it the Exa default runs anonymously. |
| `--fork-preserved-cap` | `agent.DefaultPreservedForkCap` | max **PRESERVED** winner forks (for `join=first`/`judge`) kept on disk at once — the oldest beyond this is LRU-reaped. Preserved fork workspaces stay inspectable (their paths ride the Parallel result) until reaped. A single-branch winner's fork is preserved AND its diff auto-merged into this workspace (see [ADR 0039](../adr/0039-parallel-auto-merge.md)); it still counts toward this cap and is reaped like any other. |
| `--enable-teams` | `true` | register the experimental **agent-teams** capability (`CreateTeam`/`SpawnTeammate`/`RunTeam` + the in-loop `Team` tool). On by default and **inert** until a client drives a team; `=false` disables it. |
| `--subagent-model` | `""` | global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model (via an agent definition `model:` or a per-call override) — the analogue of `CLAUDE_CODE_SUBAGENT_MODEL`. The Parallel judge stays on the session model. A concrete id or a `--model-alias`; same provider as the session. Empty inherits the parent `--model`; a non-empty value that does not resolve to a usable model id (unknown alias, or an alias meaning *inherit* — the built-in `sonnet`/`opus`/`haiku` unless overridden) **fails startup**. `mecatui` accepts the same flag for its embedded server. |
| `--headless` | `false` | run **non-interactive**: declare that clients drive sessions but never answer permission prompts (autonomous / CI). A **child** (subagent/member/branch) unresolved permission ask is then **not surfaced** to the client (nobody would answer it — it would park until run-end) but resolved by the auto-deny path / the opt-in `--subagent-ask-reviewer`. **Caveat — this gates only CHILD asks: a MAIN-session ask still surfaces and, headless, parks unanswered forever.** Pair `--headless` with permission `allow` rules (or `--yolo`) covering the main agent's tool use, or those asks will hang. Default off: a normal mecated serving an interactive client (mecatui, an IDE) surfaces asks for a human. **`--subagent-ask-reviewer` only engages under `--headless`** — setting it on an interactive server is inert (a startup WARNING says so). |
| `--subagent-ask-reviewer` | `""` | **OPT-IN headless ask reviewer**: model id or `--model-alias` of a tool-less ONE-TURN reviewer that adjudicates a **headless** subagent/member/branch permission ask the 4-step model would otherwise blanket auto-deny. **Requires `--headless`** (on an interactive server — including the `mecatui` embedded server — it is inert: asks surface to the client/modal instead). An allow approves **this call only** (never learned); a deny — or any reviewer error/timeout/ambiguity — keeps the call denied (**fail-safe**); each adjudication is **one extra LLM call** on the reviewer model. Configured `deny`/`ask` rules always win. The gRPC `RunTeam`-direct path is **excluded** (it runs zero-caps — no reviewer). Resolved on the **session's provider** (same-provider only). Empty (default) disables it; an unusable model id **fails startup** (validated even when inert). Deliberately a **server flag, not a permission-config key** — see the permissions section. A configured `ask-reviewer` **model slot** (`--model-slot ask-reviewer=…`) **supersedes** this flag's model, but the flag stays the on/off gate. `mecatui` does NOT accept this flag — it was inert there (mecatui runs interactive, so a child ask surfaces to the modal, not the reviewer) and has been removed (ADR 0089); run a headless `mecated serve --headless --subagent-ask-reviewer …` and point `mecatui connect` at it instead. |
| `--plan-mode-auto-approve` | `false` | **OPT-IN autonomous plan approval** ([ADR 0069](../adr/0069-plan-approval-gate.md), issue #206): when a plan-mode run ends HEADLESS (no human to review a presented plan), auto-approve the plan via `ApprovePlan(ModeDefault)` instead of leaving it parked. This is a deliberate **autonomous-approval capability** — an operator deployment decision, **NEVER load-bearing for safety** (the engine still gates the `PresentPlan` call; this only resolves the parked ask). It does **NOT** fire when interactive (a human can approve), NOT in non-plan modes, NOT for non-plan asks (a policy/hook ask is still the human's/auto-deny's responsibility). **DEFAULT OFF**: a headless plan ask is auto-denied (the model iterates). Requires `--headless` to engage (an interactive deployment surfaces the plan to the human). **OPERATOR-TIER ONLY** — the YAML twin is the user-global `settings.yaml` `plan-mode-auto-approve:` key; a project-tier block is ignored with a WARN (an autonomous-approval grant is an operator decision, not delegable to a project repo). A **LOUD** startup diagnostic (`plan_mode_auto_approve: ON (NO HUMAN REVIEW)`) is emitted when on, and a per-approval `WARN` names the session + ask id. `mecatui` does NOT accept this flag (mecatui runs interactive, so a plan ask surfaces to the human); run a headless `mecated serve --headless --plan-mode-auto-approve …` and point `mecatui connect` at it instead. |
| `--subagent-ask-reviewer-max-denies` | `3` | circuit breaker for the reviewer: after this many **consecutive** non-allow reviewer outcomes (denies/failures/timeouts) within one run, further asks skip the reviewer and fall through to the plain auto-deny; an allow resets the count. |
| `--subagent-ask-reviewer-policy` | `""` | path to a **TRUSTED** policy rubric file; its content replaces the built-in rubric the reviewer applies. The built-in rubric (allow only clearly read-only or standard build/vet/test commands; deny anything that mutates shared state, touches the network/credentials, or whose effect is unclear) lives in `defaultAskReviewPolicy` (`engine/agent/askadjudicator.go`); a custom file is **plain prose** in the same style. Read once at startup; an unreadable file **fails startup**. |
| `--subagent-model-router` | _(kill-switch)_ | **Semantic model router KILL-SWITCH** ([ADR 0042](../adr/0042-taxonomy-gated-model-router.md), superseding [ADR 0031](../adr/0031-subagent-model-router.md)'s enable model; extended to team members + Parallel branches by [ADR 0034](../adr/0034-team-parallel-model-routing.md)). The router is **enabled by configuring** a `models.router:` category taxonomy in the **operator-tier** `settings.yaml` (the guardrails-parity model — configure = enable), **not** by this flag. Pass **`--subagent-model-router=false`** to force the router OFF despite a taxonomy (the kill-switch; equivalently `models.router.disabled: true` in YAML — the two combine). A **bare `--subagent-model-router` / `=true`** is a harmless no-op: it still parses but neither enables nor disables (the router stays governed by the taxonomy). When enabled, a tiny one-turn classifier (on the `router` model slot) reads a delegation's task prompt and the operator's category taxonomy and picks which model the child runs on — for a **plain** `Subagent` delegation, for each **plain undefined agent-team member** (classified once at enrolment off its role briefing; a member with an agent def pins its own model), and for each **Parallel branch**. It fires **before** the child is minted (decide-once, commit-for-lifetime, same-provider) and **only** to fill the gap — an explicit per-call `model`/`agent`, a `fork`, a `resume`, or a member's agent def already pins the engine (precedence: per-call `model` > agent-def `Model` > fork/resume > router > inherited default). **Fail-soft**: any classifier failure, an unknown category, an unresolvable target, or a per-run circuit breaker (3 consecutive misses, **shared** across all three families) → the inherited default model. Runs in **both** interactive and headless deployments; the gRPC `RunTeam` direct path is zero-caps and never routes. No taxonomy (default) = **OFF, byte-identical** to no router. `mecatui` accepts the same flag for its embedded server (a taxonomy in the operator-global `settings.yaml` enables it for every binary, no per-binary flag needed), and honours `--subagent-model-router=false` for the embedded server too, for parity. |
| `--agents-dir` | `""` | directory of named **agent definitions** (`<name>.md` + YAML frontmatter — `name`/`description`/`tools`/`model`/`provider`/`permissionMode`/`maxTurns`/`maxToolCalls`/`color`/`skills`/`mcpServers`/`hooks`/`memory`; full reference in `docs/adr/0013-agent-definitions.md`), reusable as a `Subagent(agent=<name>)` delegate and as a team-member role (repeatable; highest precedence). **TRUST BOUNDARY:** a def body steers the model like `AGENTS.md`/`CLAUDE.md`. A `memory: user\|project` field injects a per-agent `MEMORY.md` head into the def's prompt at startup (READ-ONLY in v1); the **project** tier is **`--trust-project`-gated** (it points into the attacker-controllable workspace). |
| `--agents-conventional` | `true` | also discover agent defs from the conventional locations (`<workspace>/.mecatl/agents`, `<workspace>/.claude/agents`, `$XDG_CONFIG_HOME/mecatl/agents`, `~/.claude/agents`; lower precedence than `--agents-dir`). ON and **inert** until such a dir exists. Project-tier defs are **trust-gated** (`--trust-project`). |
| `--model-alias` | `""` | model alias mapping `name=model-id` (repeatable), resolved only in composition — an agent def's `model: <alias>`, a `--model-slot` selector, and `--subagent-model` all resolve through this map (then the built-in sonnet/opus/haiku aliases). |
| `--model-slot` | `""` | **PER-SLOT MODELS** ([ADR 0030](../adr/0030-model-selection-heuristics.md)): bind an internal lightweight LLM call to its own model as `slot=selector` (**repeatable**), e.g. `--model-slot compaction=cheap --model-alias cheap=gpt-4o-mini`. The routed slots are `compaction` (the compaction summary call), `ask-reviewer` (the headless child-ask reviewer), `guardrail` (the content checker), `plan` (plan-mode → model re-resolution, the opusplan pattern, [ADR 0030](../adr/0030-model-selection-heuristics.md) Layer 3), and `router` (the subagent model-router classifier, [ADR 0031](../adr/0031-subagent-model-router.md)); a **tier** key (`cheap`/`fast`/`reasoning`) gives a default a slot falls through to (the four internal-call slots — including `router` — default to `cheap`, but **`plan` defaults to `reasoning`**). The selector is a `--model-alias` or a concrete id, resolved on the **session's provider**. Empty (no `--model-slot`) keeps every call on the **session model** (**byte-identical default**). **Fail-soft**: a typo'd slot or an alias meaning *inherit* WARNs and keeps the session model — it never wedges the call. For `ask-reviewer` the slot **supersedes the model** of `--subagent-ask-reviewer` but does **not** enable it (that flag stays the on/off gate); for `guardrail` the slot **supersedes the model** of `--guardrails-model` AND **enables** guardrails (ADR 0046 — configure = enable). The YAML twin is the `settings.yaml` `models.slots:` subtree: operator-tier by default, and project-overridable **within an operator `models.allowlist`** on a trusted repo (ADR 0030 — see the per-slot models section); with no allowlist a project `models:` block is ignored with a WARN. `mecatui` accepts the same flag (and `--model-alias`) for its embedded server. |
| `--guardrails-model` | `""` | **GUARDRAILS**: model id / `--model-alias` of a tool-less checker that inspects **outbound** tool-call args (`PreToolUse`, exfil) and **inbound** tool results (`PostToolUse`, prompt injection) and enforces a verdict. Configuring a model here OR via a bound **`guardrail` model slot** (`--model-slot guardrail=…` / `models.slots.guardrail`) **ENABLES** guardrails (configure = enable, [ADR 0046](../adr/0046-guardrails-slot-enable.md) — the [ADR 0042](../adr/0042-taxonomy-gated-model-router.md) router-parity model); empty + no slot **disables** them. An unusable model id **fails startup**. Configuring a model is the **opt-in to spend** — with **no rule list** it takes the **default block rule set** (WebSearch/WebFetch/mcp__\*/Bash, enforcing — [ADR 0060](../adr/0060-guardrails-bash-default.md); downgrade via `defaultMode: advisory`). A bound `guardrail` **model slot supersedes** the checker model (this flag then supplies only the enable gate). The optional **rule list** + cost knobs live in the **user-global** `settings.yaml` `guardrails:` subtree (operator-tier **only** — a project repo cannot configure or weaken a checker; a project-tier block is ignored with a WARN); an explicit rule list replaces the defaults. `--guardrails-model` overrides the YAML model. Fires on the main loop regardless of `--headless`. Build prints one `guardrails: ON\|OFF …` posture line (resolved model + provenance). **See the guardrails section below + `docs/adr/0021-guardrails.md` + `docs/adr/0046-guardrails-slot-enable.md`.** |
| `--guardrails` | `""` | guardrails **kill-switch only**: pass `--guardrails=off` to force the checker **off** regardless of `--guardrails-model` / the `guardrail` slot / the `guardrails:` YAML. The **positive enable path** is configuring a checker model (`--guardrails-model` OR the `guardrail` slot), NOT this flag. **Only `off` is accepted** — any other value (e.g. `--guardrails=on`, which does NOT enable) **fails startup** rather than silently doing nothing. |

> **Delegation capabilities (Subagent / Parallel / Team).** Beyond the shared
> `--max-run-tokens` budget (**default: unlimited**), every delegation supports: an explicit **child-concurrency
> cap** (default 8) bounding how many children run at once; **per-call limits**
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
>
> **A member that fails a round is retried once.** A team member whose round ends in an
> error — **any** error; the harness cannot tell a transient stall from a permanent failure
> at this seam, so a poisoned history or a permanent 4xx is retried too and re-fails — has
> its session recovered and is
> given **one** more scheduled round before it is benched; a second failed round benches it
> with the same `stopped` / `error` disposition as before. The counter is per-member and
> never reset, so the exposure is **one extra round per member over the whole team run** —
> not one per failure. `team.end` reports the count (`error_rounds`), and the lead is told
> in its synthesis prompt, so a retried member never reads as a clean one; `mecatui` shows
> such a lane as `done (retried)`. There is **no flag or config key** for this in any
> binary — it is a behavioural default like the per-member turn budget, and an embedder can
> change it only with the library option `agent.WithMemberErrorRetries`. The operator brakes
> on the extra spend are the existing token ceilings — **`--max-team-tokens`** team-wide
> (checked at the round boundary, so it genuinely prevents the retry round) and
> `--max-run-tokens` as each member's CUMULATIVE ceiling (it lives on the member's session
> and accumulates across rounds, including the retry round) — on top of the built-in round
> cap and per-member turn budget. Mechanism: `docs/adr/0077-resume-a-failed-subagent.md`.

#### Enabling web search

The **WebSearch** tool is **always present** and **ON by default**:
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
mecated serve               # …plus your usual flags — Brave is now the backend
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
mecated serve                                          # …plus your usual flags
```

**A generic / commercial search API (explicit override):** `--websearch-url` speaks a
**GET (or POST) with form-encoded query parameters** and parses the JSON shape below;
it **wins over** the env tiers and the Exa default. APIs that fit that shape work
directly. Supply the key via the **`WEBSEARCH_API_KEY`** environment variable (never a
flag — it's a secret); tune the header and query parameter for the endpoint:

```sh
export WEBSEARCH_API_KEY=…          # the secret; sent in a header, never in the URL/query
mecated serve --websearch-url https://api.search.brave.com/res/v1/web/search \
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
nothing — secret-scanning of the query is the guardrails layer's job,
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

These tune the resilience decorator wrapped around every provider (the
LLM resilience layer). They split cleanly into the **establishment**
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
| `--mcp-server` | `""` | remote MCP server as `name=URL` (**repeatable**); the auth token is read from `MCP_<NAME>_TOKEN` (a token-bearing URL must be `https`, or `http` to loopback). **TRUST BOUNDARY:** a connected server's tools enter the model context. |
| `--mcp-server-insecure-http` | `""` | EXPLICIT per-server opt-in (**repeatable**): name of a `--mcp-server` entry whose `MCP_<NAME>_TOKEN` bearer may ride plain `http` to a non-loopback host. Acknowledges the token travels **cleartext on the network path** to that server — the mitigations are network-layer controls (NetworkPolicy / namespace trust) plus short-lived tokens. Relaxes ONLY the http scheme gate, ONLY for that name, order-independently of where its `--mcp-server` appears; naming an unregistered server, or one whose URL is already `https`/loopback/non-http, fails startup loudly ([ADR 0090](../adr/0090-mcp-insecure-http-optin.md)). |
| `--mcp-resource-tools` | `true` | register the `ListMcpResources`/`ReadMcpResource` meta-tools when a connected MCP server exposes resources (no-op when none do). A remote resource's contents enter model context like any other MCP output. A `FetchMcpResource` tool is also registered: it fetches the contents of an **`https://`** `resource_link` URI an MCP tool result surfaced as a typed block (the client-side dereference for an https link). **SSRF constraint:** only `https://` URIs are client-fetched, re-validated for SSRF on every redirect via `ValidateMediaURL` (no internal/private/metadata IPs, no cross-origin credentials); non-`https` schemes (`perf://`, `file://`, custom) stay **server-readonly** and are read via `ReadMcpResource` with the owning MCP server. A `CallMcpWithQuery` tool is **always registered when a connected MCP server exposes ≥1 tool** (independent of this flag — it is about tools, not resources): it calls a remote MCP tool and filters the result through a **jq expression in memory (no disk)** before it enters context, so a large JSON response is narrowed to a subset instead of being truncated into an unparseable blob. **Args:** `server` (MCP server name), `tool` (the remote tool name, NOT the `mcp__`-prefixed namespaced name), `args` (the remote tool's input arguments, verbatim), `jq_filter` (a jq expression, e.g. `.items[] | {id, name}`). **When to use it:** when a tool returns a big JSON structure and you only need a subset of fields, AND the remote tool offers no pagination/filter parameters of its own (prefer narrowing the remote call directly when it does — `CallMcpWithQuery` saves the context budget, not the remote-hop bandwidth). Fails loud if the remote result isn't JSON or the jq filter is invalid. **Fail-closed behaviour:** a direct `mcp__*` call whose **structured (JSON)** result exceeds the output cap is NOT truncated — truncating JSON mid-token would make it unparseable — so the tool returns an actionable error naming the two escape hatches (narrow/paginate the remote call, or re-run it through `CallMcpWithQuery`). Unstructured text results still truncate with a marker. See `docs/adr/0063-mcp-structured-failclosed-callmcpwithquery.md`. |
| `--mcp-prompts` | `true` | expand `/mcp__<server>__<prompt> key=value` inputs into the server-rendered prompt (static snapshot taken at connect). An MCP prompt steers the model like a slash command — enable only for servers you trust. |
| `--toolhive` | `true` | discover MCP servers from the **running ToolHive workloads** (the embedded ToolHive library lists already-running workloads and reads their HTTP proxy URLs; mecatl **never** starts or spawns a workload). Fails soft to zero servers when no container runtime is reachable. Same trust class as `--mcp-server`. |
| `--toolhive-group` | `""` | ToolHive group to discover workloads from (empty → the `default` group). Only consulted with `--toolhive`. |

The host library now contains an opt-in, random-loopback OAuth login runtime and a one-shot
composition operation for an **already-resolved** OAuth server profile. It is not wired to
`mecated`: there is currently **no `mecated mcp login` command**, OAuth profile loader, or
credential-store key acquisition. Those arrive only after issue #523 provides one canonical
profile path. Do not put OAuth endpoints or secrets on argv, and do not infer browser
permission from ACP or daemon stdio. See [ADR 0112](../adr/0112-mcp-oauth-loopback-runtime.md).

#### Security & transport (auth, TLS, rate limiting)

All off by default (the loopback single-user posture); set them **before** any
non-loopback bind — see the trust note below.

| Flag | Default | Meaning |
| --- | --- | --- |
| `--auth-token` | `""` | bearer token required on **every** gRPC/HTTP request (or `MECATL_AUTH_TOKEN`; empty disables auth). |
| `--tls-cert` / `--tls-key` | `""` | PEM server certificate/key pair; together they enable TLS on the gRPC + HTTP servers. |
| `--client-ca` | `""` | PEM client-CA bundle; enables **mutual TLS** (require + verify client certs). |
| `--rate-limit` | `0` | sustained per-client request rate in req/s (`0` disables rate limiting). With OIDC, this also bounds rejected bearer validation per direct transport peer IP. |
| `--rate-burst` | `0` | rate-limit token-bucket burst size (`0` derives a sane default from `--rate-limit`), including the separate OIDC rejected-token bucket. |
| `--oidc-issuer` | `""` | OIDC issuer URL (the `iss` claim, byte-exact) whose tokens identify callers. Setting it turns **caller identity** on: every request must present a bearer the IdP vouches for, and the verified `(iss, sub)` is recorded as the session owner. Empty processes requests unauthenticated exactly as before. |
| `--oidc-jwks-uri` | `""` | static JWKS endpoint for `--oidc-issuer`, short-circuiting OIDC discovery (the air-gapped / pinned-key deployment). Empty derives it from the issuer's discovery document. |
| `--oidc-audience` | `""` | audience (`aud`) this deployment accepts. **Required** with `--oidc-issuer` — an audience-less verifier would accept tokens minted for a different service. |
| `--oidc-max-jwks-staleness` | `1h` | maximum age of a last-good JWKS when refresh cannot reach the IdP. `0` deliberately disables the bound; a negative duration is rejected. Once stale, a failed refresh returns 503 rather than accepting with stale keys. |

The four OIDC flags are identical on `mecak8s`. Two things to be clear about
([ADR 0100](../adr/0100-caller-identity-threading.md),
[ADR 0101](../adr/0101-bounded-jwks-staleness.md)):

- **Attribution, not isolation.** Sessions, schedules and durable event-log
  records gain an owner; **nothing is refused** on identity grounds. Any
  authenticated caller can still act on any session. Per-caller access control is
  separate, later work — do not deploy these as a tenancy boundary.
- **Validator and revocation boundary.** The production validator is a
  delegated, actively-maintained OIDC/JWT library. Its last-good JWKS cache tolerates short IdP
  outages, but the default one-hour staleness bound fails closed with **503** if
  a refresh still cannot obtain current keys. A malformed, expired, wrong-issuer,
  or wrong-audience token is **401**. When OIDC and rate limiting are both on,
  repeated rejected bearers are limited before further validation by the direct
  transport peer IP; `Forwarded` and `X-Forwarded-For` are never trusted. An
  admitted valid token does not consume this rejected-token bucket and is charged
  once by the existing verified-principal limiter. An exhausted rejected-token
  bucket returns **429** (gRPC `RESOURCE_EXHAUSTED`). The bound limits
  signing-key revocation exposure during an outage; it does **not** revoke an otherwise valid individual
  token before its expiry. The cache is process-local and unpersisted, so a restart
  fetches keys again. `--oidc-max-jwks-staleness=0` is the explicit, risk-accepting
  unbounded-cache escape hatch.

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
the per-run `runs_total{stop}`, the turn-semantics counters
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

### Credentials file (`auth.yaml`)

If you'd rather not export a provider key into the process environment (any
other process a shell spawns can read its own environment, and a key exported
via `export` in a shell profile is inherited by everything that shell starts),
`mecated`/`mecatui`/`mecatequi` can instead read it from a small YAML file — a
`settings.yaml` sibling, kept out of that file specifically so `settings.yaml`
stays safe to share or check into a dotfiles repo:

```yaml
# ~/.config/mecatl/auth.yaml  (mode 0600 recommended — this file holds secrets)
providers:
  anthropic:
    api_key: sk-ant-...
  openai:
    api_key: sk-...
  openai-codex:
    oauth:
      access_token: eyJ...manual-access-token
      account_id: acct_...             # optional when present in the token
      expires_at: 2026-08-06T12:00:00Z # optional when present in the token
  openrouter:
    api_key: sk-or-...
  opencode:
    api_key: sk-...
```

Only the providers you use need an entry. For the four **API-key providers**,
resolution order is:

1. The matching environment variable (`ANTHROPIC_API_KEY`, etc.), if set —
   unchanged existing behaviour, so a deployment that only ever used env vars
   is byte-identical whether or not an `auth.yaml` happens to exist.
2. The `providers.<name>.api_key` entry in the file at `--auth-file` (if
   passed) or the conventional default `$XDG_CONFIG_HOME/mecatl/auth.yaml`
   (usually `~/.config/mecatl/auth.yaml`).
3. Empty — the existing "no credential" behaviour.

`openai-codex` is deliberately file-only: it reads
`providers.openai-codex.oauth` and has no environment-variable alias or
environment-over-file precedence path.

The file is parsed **strictly**: an unrecognized field (e.g. `apikey` instead
of `api_key`) or an unrecognized provider name (e.g. `anthropik`) is reported
as a startup warning rather than silently ignored — a credentials file is
exactly the place a quiet typo shouldn't degrade into a confusing "provider
not available" error later. A missing file at the **conventional** default
path is not an error (most deployments still use env vars, or haven't created
one yet); a missing file at an **explicit** `--auth-file` path always is,
since you named that exact path. Neither case is ever fatal to startup — a
bad or missing file just means that provider's credential falls back to
whatever the environment already resolved (frequently empty). A decode
failure never echoes the file's content back into the warning (only the path
and a generic shape complaint) — the file exists to hold secrets, so its own
error messages don't get to leak them.

`mecated` also warns (non-fatally, on Unix) when the file's permissions grant
group or other access — this file holds plaintext secrets, so a `chmod 600`
is more than a suggestion on a shared host.

There is no write path yet (no `mecated auth set` command) — create the file
yourself. The `openai-codex.oauth` block above is the only accepted OAuth shape;
OAuth under another provider and `api_key` under `openai-codex` are ignored with
a value-free warning.

#### OpenAI Codex subscription: manual token (experimental)

`openai-codex` uses a ChatGPT Codex subscription against OpenAI's undocumented
private backend. It is **not** the public OpenAI API and a ChatGPT subscription
does not provide `OPENAI_API_KEY` credit. The two billing identities are
independent: `openai` requires its API key; `openai-codex` requires the OAuth
snapshot above. Configuring one never enables or replaces the other.

Create `auth.yaml` with mode `0600`, then select the distinct provider explicitly:

```console
$ chmod 600 ~/.config/mecatl/auth.yaml
$ mecated serve --default-provider openai-codex

# Or name provider + model on a single HTTP session. Keep the fields separate.
$ curl -sS -X POST http://127.0.0.1:8081/v1/sessions \
    -H 'content-type: application/json' \
    -d '{"workspace":"/absolute/repo","provider_id":"openai-codex","model_id":"gpt-5"}'
```

The process reads and validates one immutable snapshot at startup. `account_id`
may be omitted only when the token carries a usable account claim. A supplied
account ID must itself be valid and must match the token claim when both are
present; an invalid explicit ID, a mismatch, or no usable resolved account is
rejected. `expires_at` may be
omitted when the token carries a usable expiry; an explicit value must be
RFC3339, and when both expiries exist the earlier one wins. Every models/inference
request checks the captured expiry again before network I/O,
but this release has no login, refresh token, automatic rotation, import from
`~/.codex/auth.json`, or auth-file writer. Replace an expired/rejected access
token in `auth.yaml` and **restart** `mecated`, embedded `mecatui`, or
`mecatequi`; editing the file cannot update a running process.

Treat the backend as an experimental compatibility dependency, not a supported
third-party API contract. mecatl identifies itself honestly as `mecatl` and does
not impersonate the Codex CLI. Model inventory is live and entitlement-authoritative:
the public OpenAI catalog cannot add subscription models. A successful list may
remain as process-local last-known-good display data after a later refresh error,
but current inference still fails honestly.

The file is plaintext. Mode `0600` blocks other users, not another process running
as the same UID. Agent-facing Bash does not receive the token in its environment,
arguments, prompts, logs, diagnostics, events, or session snapshots, but Bash with
that UID can still read a known `auth.yaml` path. Use a dedicated OS identity or
stronger sandbox boundary when that residual risk is unacceptable. There is no
keyring or privilege-separated secret broker in this release.

### Daemon config file (`daemon.yaml`)

The serve-time LISTENER TOPOLOGY — gRPC/HTTP/metrics listen addresses, TLS
cert/key/CA paths, and rate-limit/burst — is a small, versioned slice you can
put in a file instead of repeating on every invocation. The file is a DISTINCT
file from `settings.yaml` (which is POLICY/TRUST — permissions, posture,
guardrails, model taxonomy) and is loaded ONLY when you start the server with an
explicit `--config PATH`. There is **no conventional auto-load**: a `daemon.yaml`
at the conventional path is inert until `--config` names it.

**Scaffold and validate offline** (never starts the server):

```sh
# Write a minimal, commented v1 skeleton at the conventional path
# $XDG_CONFIG_HOME/mecatl/daemon.yaml (default ~/.config/mecatl/daemon.yaml)
mecated config daemon init

# Print the skeleton to stdout without writing (paste-ready reference)
mecated config daemon init --print

# Strictly validate a file (default: the conventional path)
mecated config daemon validate
mecated config daemon validate --file /etc/mecatl/daemon.yaml
```

`config init` still owns the operator `settings.yaml` (POLICY); `config daemon`
owns `daemon.yaml` (TOPOLOGY). The help distinguishes the two surfaces.

**Start with it:**

```sh
mecated serve --config ~/.config/mecatl/daemon.yaml
```

#### v1 fields

| Key | Default | Meaning |
| --- | --- | --- |
| `version` | _(required)_ | the schema version; must be exactly `v1`. Any other value or a missing key is a parse error. |
| `grpc_addr` | `127.0.0.1:8080` | gRPC listen address (host:port). |
| `http_addr` | `127.0.0.1:8081` | HTTP/SSE listen address (host:port). |
| `metrics_addr` | `127.0.0.1:9090` | metrics/admin listen address (host:port). An explicit empty string (`""`) DISABLES the metrics endpoint. |
| `tls_cert` | `""` | PEM server certificate; with `tls_key` enables TLS on gRPC + HTTP. |
| `tls_key` | `""` | PEM server private key (paired with `tls_cert`). |
| `client_ca` | `""` | PEM client-CA bundle; enables mutual TLS (require + verify client certs). Requires `tls_cert`/`tls_key`. |
| `rate_limit` | `0` | sustained per-client request rate (req/s); `0` disables rate limiting. |
| `rate_burst` | `0` | token-bucket burst size; `0` derives a sane default from `rate_limit`. |

The schema is **strict**: an unknown top-level key is a parse error (not a
silently-ignored typo), a multi-document file is refused, and a missing or
unsupported `version` is rejected. `config daemon validate` runs the strict
parse plus the effective semantic validation possible without
starting/binding (rate-limit/burst bounds).

#### Precedence: defaults < file < explicit CLI

- A field **absent** from the file keeps the built-in default (the loopback
  addresses above).
- A field **present** in the file overrides the built-in default.
- An **explicit CLI flag** overrides the file value, including an explicit
  empty/zero — so `mecated serve --config daemon.yaml --metrics-addr ""` disables
  metrics even if the file sets `metrics_addr`, and `--grpc-addr 0.0.0.0:8080`
  overrides a file `grpc_addr`.

#### Security

- The API bearer **token is NOT accepted in `daemon.yaml`**. Set it via
  `export MECATL_AUTH_TOKEN=...` or `--auth-token` — the same env/CLI sources as
  without a config file. There is no `token:`/`password:` key in the v1 schema.
- A **non-loopback** bind without authentication/TLS still exposes
  UNAUTHENTICATED command/file execution to the network and logs a prominent
  WARNING. `daemon.yaml` changes topology, not the trust model — enable auth
  (`MECATL_AUTH_TOKEN`/`--auth-token`) and/or TLS (`tls_cert`/`tls_key`) before
  binding a non-loopback address.
- `config daemon validate` never prints secrets or raw file content; the success
  line carries only the file path and version.

See [ADR 0088](../adr/0088-daemon-config-file.md) for the rationale.

### Provider selection

A provider is **required** — the server has nothing to do without one. The server
builds an N-provider registry and AUTO-DETECTS availability from the environment:

- `OPENAI_API_KEY` set (or `--openai`) → the `openai` Responses provider.
- `OPENROUTER_API_KEY` set → the `openrouter` provider (same adapter, OpenRouter
  base URL; falls back to `OPENAI_API_KEY` by convention).
- `ANTHROPIC_API_KEY` set → the native `anthropic` Messages provider (extended
  thinking on, model-aware; default model `claude-sonnet-4-6`).
- a valid `providers.openai-codex.oauth` snapshot in
  [`auth.yaml`](#openai-codex-subscription-manual-token-experimental) → the
  distinct experimental `openai-codex` provider; it has no environment alias.
- Each API-key provider above also counts as "set" when its key comes from
  [`auth.yaml`](#credentials-file-authyaml) instead of the environment — the
  file is just a second source for the same credential, checked only when the
  matching API-key environment variable is empty. `openai-codex` instead uses
  only its distinct `providers.openai-codex.oauth` file entry.
- `--mock` → canned offline provider (single text turn; smoke tests only).
- none of the above → startup error (the daemon refuses to start; mecatui fails the
  same check client-side before hosting an embedded server). The error names all
  four API-key environment routes (Anthropic, OpenAI, OpenRouter, and OpenCode),
  their compatible/proxy base-URL flags, the offline `--mock` escape hatch, and
  the operator guide. If you intended subscription access, provide a valid
  `providers.openai-codex.oauth` entry through the conventional or explicit
  `auth.yaml` path instead.

An explicit session selector wins, then an operator `--default-provider` /
`models.default_provider`, then automatic preference. Automatic preference keeps
every pre-existing credential-driven provider ahead of `openai-codex`, and keeps
`openai-codex` ahead of intent-only gateways such as ToolHive. Thus merely adding
the manual token never redirects an existing API-key deployment. Codex becomes the
automatic default when no pre-existing credential-driven provider is available,
even if an intent-only gateway such as ToolHive is also available. A zero-selector
session continues to float with the deployment default after restart, while an
explicit Codex selector is persisted and must rehydrate through Codex rather than
`openai`.

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
This holds for the configured **default** model too: it resolves its real
window and compacts at that window, not a hardcoded 128k — the 128k floor now applies
only to a genuinely uncatalogued model. The window is resolved **live-first at the point
of use** (one resolver shared by the engine trigger and the `resolved_model` echo), so a
live-only model whose curated catalog lacks a window self-corrects to its live window
once the background catalog refresh lands — no engine rebuild. `--context-window-override`
still forces a fixed window when you need it, and now moves BOTH the compaction trigger
and the client footer denominator together. Operators can instead set exact stable
mappings in user-global/explicit `settings.yaml` under `models.context_windows`:
`provider-id → final-model-id → tokens`. This operator-tier map is consulted after
alias/slot routing resolves the final ID, before live metadata; project values are
ignored with a warning. Its exact entries also replace `context_limit` in `ListModels`.

Providers may opt into live model listing, but fallback semantics belong to each
credential boundary. Providers without a live lister (including public OpenAI)
show their curated embedded catalog.

For **OpenRouter**, the picker reflects its real live catalog (336 models) rather
than the curated subset when keyed. One unauthenticated best-effort background
fetch swaps it in after startup; offline/error keeps the embedded floor.

> Network note: a keyed OpenRouter `Build` makes **one best-effort background
> outbound request** to `https://openrouter.ai/api/v1/models` to populate the live
> picker. It is unauthenticated (no key on the wire) and fail-safe — offline, blocked,
> or any error simply leaves the embedded curated subset in place.

For experimental **openai-codex**, `/backend-api/codex/models` is authenticated and defines the
account's entitlements. There is no public-catalog fallback. Before its first
success, an authorization/service failure contributes no Codex models and a
successful empty response stays empty. After success, process-local last-known-good
rows may remain visible during a later refresh failure; this never makes a failing
inference request succeed. `/models` surfaces `unauthorized`, `unreachable`, and
`empty` with provider-specific remediation.

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
`InvalidArgument`. The reconcile is **provider-level only**: a saved
model missing from the (possibly embedded-floor, pre-live-refresh) snapshot is sent
anyway — the server validates the model string verbatim. A server-**rejected** saved
selection retries once on the server default and surfaces a **loud warning** naming
the rejected model, never a silent downgrade; the state file is not rewritten.

> **`--model` vs the picker.** For the embedded `mecatui` server, `--model` is the
> server's **default** model (what it resolves when the client sends no `model_id`);
> the `/models` picker is the **client's** per-session selector layered on top. For an
> external server (`mecatui connect`), the server owns its provider config — `--model` is only a header
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

A discovered skill's bundled assets remain in the **logical SkillSource namespace**;
they do not become workspace files. Activating the skill lists bounded logical names.
To read a textual reference, the model calls `Skill` again with the exact `name` and
`asset`; that one payload is returned after name, size, UTF-8, and NUL validation.
This works for local and remote skills, including `no-fs` sessions. `Read`, `Stat`,
`Glob`, `Grep`, and Bash receive no skill-derived path or access grant, and executable
metadata does not cause implicit materialization or execution. Skill authors whose
workflow truly needs a script on disk must provide an explicit step to create or obtain
it inside the workspace; the resulting Write/Bash calls follow normal permissions.

#### Path forms

Every file tool (`Read`/`Write`/`Edit`/`Stat`, and the `Glob`/`Grep` path glob)
accepts a path in one of two forms:

- **session-relative** (the usual form), interpreted relative to the workspace
  root; any `..` that climbs out of the root is rejected; and
- **absolute**, accepted **iff it canonicalizes inside the workspace root** — it
  is the same physical file a relative path would reach, addressed by its
  absolute alias, and reduced to its root-relative form before any operation. An
  absolute path that resolves OUTSIDE the workspace root is rejected with
  `ErrPathEscape`.

A symlink inside the workspace whose target resolves OUTSIDE the workspace is
rejected at resolution time, whether addressed relatively or absolutely —
defense-in-depth on top of the `os.Root` confinement that still guards every
in-root operation. `Glob`/`Grep` patterns are not paths: a leading `/` in a
pattern is stripped (patterns are root-relative), and patterns are never routed
through absolute resolution. Rationale + threat model:
[ADR 0047](../adr/0047-absolute-path-resolution.md).
