# Extensibility — MCP, tools & progressive disclosure

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the `tool.Catalog` registration seam, MCP client (streaming-HTTP only), progressive tool disclosure (`Disclosable` + `ToolSearch`), skills (progressive-disclosure instruction units), the self-improving skill loop (`SkillDraft`), slash commands, the engine-as-library module contract, and the seam summary table.

**Prerequisites:** [the ports](ports.md) — the `tool.Workspace`/`tool.Catalog` seams and the tool contract.

**Follow-on:** [the API surface](api-surface.md) and [subagents & teams](subagents-and-teams.md) — the MCP/inventory endpoints and delegation families that consume the catalog.

The `tool.Catalog` is the single registration seam, so every tool — core, remote,
or generated — is one uniform `tool.Tool`.

**Portable memory capabilities.** `tool.MemoryStore` remains the six-method base
contract. `tool.MemoryLifecycleStore` is an optional additive capability for opaque
versions, inspection, tombstones, and compensating undo. Consumers register the
portable `engine/adapter/memorytools` base family for every store and its lifecycle
family only when that interface is implemented. Remote adapters may implement both;
an old driver still serves ordinary operations and operator-profile reads through the
original RPCs, while unsupported lifecycle mutations fail visibly instead of falling
back to unversioned deletion. `prompt.OperatorProfileSource` is a separate
consumer-defined read seam, so an embedding can supply live user facts without
adopting the file adapter or the lifecycle capability.

**MCP client** (`internal/adapter/mcp`) — remote tools register here. The
transport is **streaming-HTTP only** (the project's hard constraint): the
stdio/command transport is never used, so no MCP server is ever `os/exec`-spawned.
`mcp.Connect` / `mcp.NewManager` dial the configured servers, and the discovered
tools are registered into the catalog **namespaced** `mcp__<server>__<tool>` so a
remote tool can never collide with or shadow a built-in. Concrete session loss
(server restart, plain missing-session 404, closed transport, EOF, or refused
connection) is re-established transparently with a single bounded reconnect
attempt per call, serialized under a mutex. A structured JSON-RPC 400/404 or HTTP
429/502/503/504 is instead a one-call failure: the live session is retained and
the operation is never replayed automatically. See
[ADR 0056](../adr/0056-mcp-client-reconnect.md) and
[ADR 0223](../adr/0223-mcp-sdk-transport-error-semantics.md). The client also holds the
**standalone SSE GET stream** open per connected server, so server-initiated
`notifications/{tools,prompts,resources}/list_changed` invalidate the cached
snapshots (lazily re-listed on the next read); live catalog refresh is
deferred to a later phase — see [ADR 0057](../adr/0057-mcp-server-notifications.md).

The adapter optionally owns an authorization-code `OAuthController` when an embedding
supplies `ServerConfig.OAuth`. One official SDK handler, durable credential source,
authorization singleflight, and dedicated hardened HTTP client live for the whole
`Server` lifetime and survive MCP session reconnects. Preregistered confidential and CIMD
clients are supported; DCR is rejected by omission because the SDK exposes no durable
registration hook. A nil presenter fails protected-server login immediately. An explicitly
constructed stdlib-only `mcp/oauthlogin` runtime can instead own one serialized, random-path
IPv4-loopback callback interaction. `internal/app.LoginMCP` bridges that runtime to a copy
of one already-resolved OAuth `ServerConfig`, calls the real `mcp.Connect`, requires
initialize and initial tool listing to succeed, and immediately closes the temporary
server/controller while leaving the borrowed credential store open. The callback converts
only code/state/issuer; the controller and official SDK retain their issuer/state checks,
discovery, PKCE, exchange, and durable CAS. OAuth traffic is exact-origin allowlisted, DNS-resolved and pinned, and blocks
loopback, link-local, metadata, unspecified, multicast, mapped, and other special destinations unconditionally.
An exact `private_origins` opt-in admits only RFC1918 IPv4 or ULA IPv6 answers; every DNS answer must remain in that
class. The adapter ignores
proxies, and follows only bounded same-origin safe redirects. Discovery GETs may reach the
resource/additional origins, but the presenter and protocol transport permit codes, tokens,
client authentication, and token exchanges only at the canonical configured issuer origin;
preregistered confidential clients require Basic and `client_secret_post` is denied before
network send. The shipped roots resolve strict operator-tier `mcp.servers` profiles
through one loader: `none`, environment-referenced `static_bearer`, or OAuth backed by a
mutable encrypted local Store or read-only environment Reader. Normal serving and ACP
never install a presenter. Only `mecated mcp login SERVER [--no-browser]
[--permission-config PATH ...]` authorizes a local Store; the repeatable permission-config
option selects trusted operator settings only and never carries OAuth values. Environment
credentials are preprovisioned and picked up after restart. The
combined path is guarded offline through operator resolution → explicit login → encrypted
store close/reopen → `app.Build` global catalog → model tool call → lazy refresh rotation →
second process restart → real dropped-session reconnect. The same gate verifies manager-before-
profile-source teardown and scans diagnostics, errors, model-facing results, and generated
configuration projections for distinct secret canaries. Headless startup with a clean store
fails soft with a login remedy and no presenter; ACP consumes the already-built catalog and
cannot provide OAuth profiles or authorize. After operator authorization, ACP sessions may
invoke the shared global OAuth-backed tools under ordinary permissions.
OAuth remains unavailable to per-session/inline/discovered MCP, and DCR remains
unsupported. The ordinary MCP client has an OAuth-mode-only exact-resource capability and
cross-origin redirect gate so its audience-bound bearer cannot be reattached elsewhere.
Static `Authorization` and OAuth are mutually exclusive; OAuth-disabled static
headers retain their existing origin-scoped behavior. See [ADR 0219](../adr/0219-mcp-oauth-sdk-profile.md)
for the constrained dependency profile, [ADR 0220](../adr/0220-mcp-oauth-controller.md)
for controller ownership, [ADR 0112](../adr/0112-mcp-oauth-loopback-runtime.md) for the
opt-in host runtime, and [ADR 0113](../adr/0113-operator-mcp-auth-profiles.md) for profile
and command wiring.

**Progressive tool disclosure** (pattern 9) — a tool may optionally implement
`tool.Disclosable`; the built-in `tool.Search` tool (catalog name `ToolSearch`,
`tool.NewToolSearch`) hydrates hidden tools on demand by searching the catalog. A
tool that does not implement `Disclosable` is always listed, so this is opt-in and
backwards-compatible (gated by the `ProgressiveTools` flag on the Engine `Deps`).

**Skills** (`internal/adapter/skills`) — pattern 9 applied to *instructions*
instead of tool schemas. A skill is a progressive-disclosure instruction unit: a
`SKILL.md` file with YAML frontmatter (`name` + `description`) and a markdown
body, laid out as `<skills-dir>/<name>/SKILL.md` (matching the Agent Skills
ecosystem; see the format references under `docs/examples/skills/`).
`skills.Discover` scans the directory and parses each file into a pure
`skills.Skill` value object; discovery is forgiving — a malformed or
frontmatter-less file is **skipped and reported** (`skills.SkipError`), never
fatal, and a kept skill whose **always-in-context description** exceeds a cap
(`maxDescriptionBytes`) is rune-safe truncated with a warning (so one oversized
description cannot bloat every request and break the byte-stable prompt prefix);
an oversized body is flagged too (it is truncated on activation). A single
read-only `Skill` tool (`skills.NewTool`, catalog name `Skill`,
`ReadOnly()==true`) exposes them: its `Spec().Description` **enumerates every
discovered skill's name + one-line description** — the cheap, always-in-context,
cache-stable metadata layer. `Execute({name})` returns that skill's full **body** and
a bounded inventory of bundled assets by logical name. If the instructions need a
textual reference, the model calls the same tool again with
`Execute({name, asset})`; the tool validates the advertised logical name, fetches
only that payload through `tool.SkillSource`, enforces its size cap, and rejects
invalid UTF-8 or NUL-containing content. No base directory crosses the seam, no
asset is materialized or added to the workspace, and `Read`/`Bash` gain no implicit
access. A workflow that genuinely needs a file must create or obtain it explicitly
inside the workspace under ordinary permissions. Because the tool is read-only it
is also available in plan and no-filesystem sessions. The tool is registered **only
when at least one valid skill is discovered** — an empty inventory advertises
nothing.

The discovered set is *also* projected into a server-side inventory snapshot
(`internal/app.skillSnapshot`, name-sorted, name+description only — no body),
carried on `server.Config.Skills` and served read-only by the **`ListSkills`
RPC** (`HarnessService.ListSkills` / `GET /v1/skills`). It mirrors `ListAgents`
rather than `ListCommands`: skills are discovered once at build time and
immutable for the process lifetime, so the snapshot is a pure read, never a live
re-scan. The mecatui TUI consumes it for the `/skills` browser panel (gated on
`caps.Skills` plus a wired `client.SkillLister`); activation stays the model's
concern, so the panel is discovery only.

*Where skills come from* is itself a seam: `skills.Source`
(`Skills(ctx) ([]Skill, []SkipError, error)`) is the **pluggable extensibility
point**. `skills.DirSource{Dir, Label}` is the default local-filesystem
implementation (the `<dir>/<name>/SKILL.md` layout); `skills.MultiSource`
composes an **ordered** list of sources with a defined precedence — **earlier
source wins** on name collisions, the loser dropped with a "shadowed by a
higher-precedence source" `SkipError`. A future embedded-defaults or remote
registry source just implements `Source` and slots into the `MultiSource`; the
consumer (`skills.RegisterSource`) is unchanged. Two seams now exist at
different altitudes. **`tool.SkillSource`** (`engine/tool/skillsource.go`) is
the DOMAIN port skills cross as **logical bundles** — identity/metadata
(`SkillMeta`), body, and payloads addressed by logical name
(`SkillAsset`, `ListSkillAssets`/`ReadSkillAsset`) — never a path/dir/root;
both the FS adapter (`skills.FSSource`) and the remote driver
(`SkillSourceService`, [observability & persistence](observability.md)) implement it, and the conformance suite holds them
to the same semantics. `skills.Source` remains the **adapter-local**
discovery/composition seam underneath it (where a skill's files live is the FS
adapter's private business); nothing in the agent loop consumes skills directly
(they are packaged into a `tool.Tool` at composition time).

**The self-improving skill loop** (`skills.Drafter`, opt-in) closes the loop so
durable skills can *come into being from the agent's own experience*. A single
writable tool, **`SkillDraft`** (`skills.NewDraftTool`, catalog name `SkillDraft`,
`ReadOnly()==false`), lets the model PROPOSE a skill; its `skills.Drafter` write
seam (mirroring `Source`, in the adapter package — nothing in domain/agent consumes
or produces skills) validates and sanitizes the untrusted candidate and writes it
to a **quarantine directory that is NEVER registered as a catalog `Source`**. The
default `DirDrafter` is fully offline: it reuses `parseSkill`/`validateName`, an
exported injection scan (`ScanForInjection`, run on both the always-in-context
description and the body), a name regex (lowercase Agent-Skills style, blocking
traversal), the existing size caps, a path-containment assert, an atomic
temp+rename write, and an offline **2-gram Jaccard** novelty check
(`Jaccard2Gram`) that *warns* (never blocks) on near-duplicate descriptions. Every
quarantined `SKILL.md` is provenance-stamped (`origin: model`, `drafted_at`) for the
reviewer; `parseSkill` ignores those keys so they never reach context. **The trust
boundary** (stated in `internal/adapter/skills/promote.go`): the model can author a candidate but can
never activate its own proposal in any session. It rests on two invariants, both
enforced in `cmd/mecated` (`validateSkillDraftConfig`, fatal on a misconfig):
(1) the quarantine dir must live **outside the workspace root**, so the model's
workspace-confined `Write`/`Edit` structurally cannot reach it — a candidate only
ever enters quarantine via the `Drafter`; and (2) the dir must be **disjoint from
every active skills dir**. Promotion from quarantine to an active `--skills-dir` is
an **operator** action (`skills.Promote`, the `mecated skills promote` subcommand),
which **shows the full candidate, requires confirmation** (`--yes` for scripted use),
**verifies `origin: model` provenance**, and re-runs structural validation + the
injection scan before moving it (refusing to overwrite an existing name). The
convention is **author in session N → operator promotes → active in N+1**: drafts
never enter the live catalog or perturb the byte-stable prompt prefix (it is built
once at startup from operator-trusted sources only). `SkillDraft` is opt-in via
`--skills-draft-dir` (empty ⇒ tool not registered, like `--memory-dir` gating
Remember). **Residual** (documented, not hidden): absent the deferred OS sandbox the
`Bash` tool can write to any path, so the structural boundary covers `Write`/`Edit`
only — `mecated` warns when `SkillDraft` and `Bash` are enabled together; the fully
structural deployment is shell-less or sandboxed. `SkillDraft` itself defaults to **ask** so a
human reviews authorship, and being mutating it is filtered out of plan mode.

It stays **opt-in**: `mecated` wires it via a repeatable `--skills-dir`
(highest precedence) and an opt-in `--skills-conventional` that adds Claude-Code-
style **known paths** (`skills.ResolveSources`): project-level
`<workspace>/.mecatl/skills` and `<workspace>/.claude/skills`, then user-level
`$XDG_CONFIG_HOME/mecatl/skills` (or `~/.config/mecatl/skills`) and `~/.claude/skills`,
with precedence **explicit > project > user**. With neither flag set, the resolver
yields no sources and nothing is read. Discovery (reading files, YAML parsing via
`github.com/goccy/go-yaml`) is an adapter concern; nothing in this package is imported
by a domain package — it merely implements the domain `tool.Tool` interface.

### The engine as an embeddable library

The extensibility story is not only "swap an adapter inside mecatl" — `engine/`
is **its own Go module** (`github.com/stacklok/mecatl/engine`), so an external
consumer can import the loop, the domain, and the ports directly without pulling
in mecatl's full dependency cone. The engine module's standalone closure is
deliberately tiny — `doublestar` + `robfig/cron` + `github.com/goccy/go-yaml` +
`x/sync` (+ test-only `goleak`) — versus the
toolhive/k8s/OTel/gRPC cone the root module carries; an embedding host brings its
own adapters. The exported identifiers of the **seven core packages** (`session`,
`governance`, `tool`, `prompt`, `port`, `team`, `agent`) are the engine's STABLE
public surface, governed by [`engine/COMPATIBILITY.md`](../../engine/COMPATIBILITY.md)
and the `api-compat` gate (`internal/apicheck`); the `engine/adapter/*` reference
adapters (`mockllm`, `memfs`, `nofs`, `memstore`, …) ship for offline tests and
sane defaults and carry **no** stability promise. See
[ADR 0036](../adr/0036-engine-module.md) (the module carve) and
[ADR 0037](../adr/0037-engine-stability-contract.md) (the contract).

### Seam summary

Every capability above is a default-on (or opt-in) interface; the core never
changes when one is swapped:

| Seam | Where | Default → swap-in |
|---|---|---|
| `port.LLMProvider` | `engine/port/llm.go` | `openai`/`mockllm`; decorated by `llmresilience`; other vendors slot in unchanged |
| `port.PermissionPolicy` | `engine/port/permission.go` | `permpolicy` (layer-1 rules), optionally decorated by `permclassify` (layer-2 model classifier) |
| `Compactor` | `engine/agent/compaction.go` | `HeuristicCompactor` → `CascadeCompactor` |
| `TokenCounter` | `engine/agent/tokencount.go` | `HeuristicTokenCounter` → `tokenizer.Counter` |
| `InstructionAssembler` | `engine/prompt/instructions.go` | `RootAssembler` (AGENTS.md/CLAUDE.md) → `MultiAssembler` composing `RootAssembler` → `SoulAssembler` (persona) → `MemoryIndexAssembler` (saved project facts) → `UserModelAssembler` (operator FACTS), all as turn-0 user messages |
| `prompt.SoulSource` | `engine/prompt/soul.go` (impl `internal/adapter/soul`) | nil (off) → `*soul.Store`; agent-READ-ONLY (no write path), env-injected (not the WorkspaceReader — the file is outside any session root), injection-scanned + byte-capped, fail-soft; on by default, `--soul-file`/`--no-soul`. **Two provenances + trust gate (issue #14, Phase 3, Item 2):** a USER soul (`<xdg>/mecatl/soul.md` or `--soul-file PATH`) is always trusted; a PROJECT soul (a discovered `<workspace>/.mecatl/soul.md`, parallel to `.mecatl/settings.yaml`) is **untrusted by default** and honoured only with `--trust-project` (the SAME issue-#13 gesture — not a new flag, not routed through governance: the soul is fenced DATA). **USER-WINS precedence** (single identity anchor, not a merge): a present user soul is used and the project soul is ignored; an untrusted project soul is dropped + WARN-narrated (via the injected `port.Diagnostics`). The selection (provenance/trusted/drift metadata) lives in `internal/app/soulselect.go`; `engine/prompt` stays trust-unaware. **Drift baseline (Item 1):** `soul.LoadWithMeta` computes the sha256 of the clean body in the same read; `internal/app/soulguard` records it as a harness-owned sidecar `<soulPath>.sha256` trust-on-first-use (against WHICHEVER soul wins), WARNs on a later mismatch, and (with `--soul-strict`) drops a drifted soul. `--approve-soul` re-baselines. The WRITE lives ONLY in the composition layer — the adapter stays write-free. |
| `prompt.UserModelSource` | `engine/prompt/usermodel.go` (impl `internal/adapter/memory`) | nil (off) → a SECOND, user-scoped, **cross-project** `*memory.Store` over `<xdg>/mecatl/usermodel`; durable operator FACTS exposed as RememberUser/RecallUser/SearchUserModel (enforced `user/` prefix; write-time injection scan) + the turn-0 `<user-model>` block; on by default, `--user-model-dir`/`--no-user-model`. Writable FACTS, not a governance scope. `learning.mode`: Off has no automatic observer; Review stages evidence-backed proposals without writes; Auto stages then conservatively promotes; `--user-model-review` is a deprecated Auto alias. |
| `CommandExpander` | `engine/prompt/command.go` | `NoopExpander` → `DirCommandExpander` (slash commands) |
| `tool.Disclosable` + `ToolSearch` | `engine/tool` | always-listed → progressive disclosure |
| `Skill` tool (skills) | `internal/adapter/skills` (impl) | off → opt-in `--skills-dir`; progressive disclosure of *instructions* (metadata always in context, body on activation) |
| `skills.Source` (adapter) / `tool.SkillSource` (domain port) | `engine/adapter/skillfs/source.go` / `engine/tool/skillsource.go` | `DirSource` (one dir) → `MultiSource` (ordered, earlier-wins); known-path resolver (`--skills-conventional`: project `.mecatl`/`.claude`, user XDG/`~/.claude`); the domain port carries skills as logical bundles (metadata/body/assets, no paths) — implemented by `skills.FSSource` and the remote `SkillSourceService` driver |
| `skills.Drafter` (self-improving loop) | `internal/adapter/skills/drafter.go` | off → opt-in `--skills-draft-dir`; default `DirDrafter` (offline: validate/sanitize/2-gram-Jaccard novelty → out-of-workspace quarantine, NEVER a catalog Source). WRITE side is pluggable (a future LLM-vetting decorator slots in); promotion is filesystem-only in the MVP — operator `mecated skills promote` is the gate (shows content, confirms, verifies provenance; author N → promote → active N+1) |
| `tool.CommandRunner` | `engine/tool/tool.go` (impl `osfs`) | the command-execution chokepoint; an OS sandbox wraps here |
| `tool.MemoryStore` | `engine/tool/tool.go` (impl `memory`; conformance `engine/adapter/memconformance`) | cross-session memory + `dream` consolidation |
| `tool.EnvironmentForker` | `engine/tool/isolation.go` (impl `forker`) | fork-join isolated branches (returns a complete child `Environment`) |
| `tool.Catalog` | `engine/tool/catalog.go` | core tools + MCP (streaming-HTTP) |
| `mcpperf.Deps` (perf MCP server) | `internal/adapter/mcpperf` | opt-in `--perf-mcp`; a read-only streaming-HTTP MCP `http.Handler` mounted at `/mcp`. `mecated` serves it on its loopback admin listener; embedded mecatui with no explicit `--perf-addr` chooses ephemeral loopback TCP because streaming HTTP needs a URL (plain `--perf` instead defaults to a private per-instance UNIX socket). Built by DI — `Snapshot`/`Gatherer`/`Profiler` from `telemetry`, a slow-turn ring buffer (`telemetry.SlowTurnBuffer`) bridged at the cmd boundary to the `mcpperf.SlowTurnSource` seam (telemetry never imports mcpperf — the dependency points inward). Explicit TCP is fail-closed to loopback (unauthenticated); no stdio transport |
| `SessionStore` + AGENTS.md/CLAUDE.md discovery | `port` + `engine/prompt/builder.go` | file-as-memory; AGENTS.md wins over CLAUDE.md, injected as a **user** message, never system |

**Remaining non-goals / deliberate deferrals**: an **OS-level sandbox**
(Landlock/seccomp/Seatbelt) is the one explicitly-deferred item — the
`CommandRunner` seam is the place it wraps, and shell-less deploys avoid the
surface entirely. **stdio MCP is never supported**. Embeddings remain unbuilt
(multi-provider routing shipped — [multi-provider](providers.md)); **skills**
exist as progressive-disclosure instruction units (see above), with bundled
*packaging* shipped as logical assets on the `tool.SkillSource` port
(`SkillAsset`, `ListSkillAssets`/`ReadSkillAsset` — never a path on the wire).
The `Skill` tool retrieves textual assets one at a time by logical name; it does
not materialize them or widen the workspace. The guiding restraint still holds: build the shape, instrument it,
and resist features before the loop, tools, permissions, hooks, and cache all work.

## Prerequisites

- [The ports](ports.md) — the `tool.Workspace`/`tool.Catalog` seams.

## Follow-on reading

- [The API surface](api-surface.md) — the MCP passthrough RPCs.
- [Subagents & teams](subagents-and-teams.md) — delegation families that consume the catalog.

## Related

- [The agent loop that runs the tools](agent-loop.md)

---

[← Architecture guide](../architecture.md)
