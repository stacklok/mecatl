# Deployment & server hardening

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** server hardening (auth/mTLS, rate limiting, health, graceful shutdown), multi-replica single-writer enforcement (session leasing), permission & bash governance (deny-dominant resolution, posture ladder, workspace trust), the project authority set, and supply-chain scanning.

**Prerequisites:** [the API surface](api-surface.md) — the servers being hardened.

**Follow-on:** [observability](observability.md) — the persistence seams the server depends on.

The server (`internal/adapter/server`) is hardened for off-loopback operation,
and `cmd/mecated` wires the knobs:

- **Authentication** — optional bearer token (`--auth-token` / `MECATL_AUTH_TOKEN`,
  constant-time compared) enforced by a gRPC interceptor + HTTP middleware
  (`internal/adapter/server/authn.go`); optional **TLS / mTLS** (`--tls-cert` / `--tls-key` /
  `--client-ca`). The server still **warns loudly** if it binds a non-loopback
  address with no auth configured. `mecak8s` watches the parent directories of
  its server cert/key paths so Kubernetes projected-Secret `..data` swaps are
  observed. It publishes only a fully parsed, matching pair through
  `tls.Config.GetCertificate`; a bad rotation retains the last valid pair, while
  the client CA remains restart-required ([ADR 0240](../adr/0240-mecak8s-credential-reload-and-chart-security.md)).
- **Redis credential reload** — when mecak8s receives any Redis CA, username, or
  password file, it watches the lexical parent directories and transactionally re-reads
  the complete configured set. A bounded single-flight worker constructs and probes a
  candidate through the normal verified toolhive-core Redis path, then atomically publishes
  it. Every store/schedule/migration operation leases one client generation, so displaced
  clients close only after in-flight work and migration locks release them. Invalid
  candidates retain the last valid generation; no configured files means no watcher or
  reload goroutine ([ADR 0240](../adr/0240-mecak8s-credential-reload-and-chart-security.md)).
- **Rate limiting** — per-client + global token-bucket (`--rate-limit` /
  `--rate-burst`), bounded and idle-evicting. With OIDC enabled, a separate
  pre-validation rejected-token bucket protects JWT/JWKS validation. It is keyed
  only by the direct transport peer IP (`RemoteAddr` / gRPC peer), never by
  `Forwarded` or `X-Forwarded-For`; valid tokens do not consume it and proceed to
  the unchanged post-validation `(issuer, subject)` limiter.
- **Health** — HTTP `/healthz` (liveness) + `/readyz` (readiness) mounted outside
  auth/rate-limit, plus standard `grpc_health_v1` `SERVING` (`internal/adapter/server/health.go`).
- **mecak8s drain isolation** — a separate plaintext `--drain-addr` listener
  defaults to `0.0.0.0:8082` and serves only kubelet's `GET /drain`; the normal
  HTTP/SSE API listener has no drain route. The chart omits this port from the
  Service, protecting normal Service/gateway traffic, but direct Pod-IP access
  remains an operator-enforced NetworkPolicy or mesh-isolation residual ([ADR 0290](../adr/0290-mecak8s-drain-listener.md)).
- **mecak8s secure real-provider transport** — three postures: in-pod TLS + OIDC,
  edge-terminated TLS + OIDC (`security.tlsTerminatedUpstream=true`, ClusterIP-only h2c),
  and the explicit unsafe bypass. The upstream value is an operator attestation the chart
  cannot verify, and edge mode puts caller bearer tokens on the pod network in cleartext:
  restricting backend reachability to the gateway or mesh is the load-bearing control,
  and the chart ships no NetworkPolicy to do it. Full operator contract in
  [ADR 0278](../adr/0278-mecak8s-edge-terminated-tls.md).
- **Graceful shutdown** — gRPC `GracefulStop` + HTTP `Shutdown`.
- **Daemon config file (`daemon.yaml`, ADR 0088)** — the serve-time topology
  slice (gRPC/HTTP/metrics listen addresses, TLS cert/key/CA paths,
  rate-limit/burst) is optionally carried by a small, strict, versioned YAML
  file loaded ONLY when `mecated serve --config PATH` is supplied explicitly
  (`internal/adapter/daemonconfig`). There is NO conventional auto-load.
  `mecated config daemon init` scaffolds the v1 skeleton at
  `<XDG_CONFIG_HOME>/mecatl/daemon.yaml`; `mecated config daemon validate`
  strictly parses + semantically validates a file offline. It is a DISTINCT file
  from `settings.yaml` (POLICY/trust) and carries NO auth token value (the
  bearer token stays `MECATL_AUTH_TOKEN`/`--auth-token`); a non-loopback bind
  still requires auth/TLS. Precedence: defaults < file < explicit CLI. It folds
  into the cmd-mecated serve-time fields (no `app.Config` widening).

Deployment artifacts: a hardened **GitHub Actions** CI plus a **ko**-based release
that signs images with **cosign** and emits an **SBOM** and **SLSA provenance**
(`.github/workflows`, `.ko.yaml`); `deploy/` carries PSS-restricted manifests
(health probes can switch TCP→httpGet against the endpoints above). A **live
BDD e2e suite** (`e2e/`, `task e2e`, the `e2e-live.yml` workflow) exercises the
harness against a real model; it is opt-in (real money) and deliberately not
part of `task test`. The toolchain is **go 1.26.5** (both modules).

**Supply-chain scanning** (#118) closes the loop on dependency hygiene:
**`govulncheck`** runs per-module — the `engine` module is held STRICT-CLEAN,
while the root module runs through a fail-closed **reachable-vuln gate**
(`.github/scripts/govulncheck-gate.go`: it parses `govulncheck -format json` and
fails on any reachable finding whose OSV id is not on a dated accepted-risk
allowlist — govulncheck has no native ignore mechanism, and the wrapper runs
under `pipefail` so a broken scan cannot pass vacuously). **`dependabot`**
(`.github/dependabot.yml`) tracks both Go modules independently plus the
SHA-pinned GitHub Actions (grouping minor+patch, isolating majors); every action
is **SHA-pinned** with a `# vX.Y.Z` comment that dependabot preserves.

### Server-owned session placement

Placement is a trusted composition decision on every listener topology. Public clients
never submit workspace/cwd paths, placement IDs, or exact EnvironmentRefs. Create binds
the configured deployment default or explicit no-FS attenuation. Local `--workspace`
exists only as private operator configuration; embedded and loopback modes do not create
a path-authority exception.

Every persisted session carries exactly one private `EnvironmentRef{Kind,ID,Revision}`.
Run entry reauthorizes and exactly reattaches that ref; missing providers, authorization or
revision drift, nil Workspace, and identity mismatch fail closed without following a
current default. Trusted snapshots and driver storage may retain physical locator data,
while public session/event/error projections remain path-free. ACP cwd is a local assertion
against trusted configuration, not authority.

Alternate worktrees require an owned source session. Discovery emits display-safe metadata
and an opaque caller/source-scoped HMAC selector accepted only by ClearSession/ForkSession.
The Build-owned key and selectors are not persisted and no registry/map exists; restart
requires relisting. Schedules persist an already-resolved exact ref plus owner/scope;
delegation derives or server-forks the parent Environment and artifact handles cannot be
replayed as selectors. Mecak8s binds its storage-free default to no-FS; a future remote
placement provider uses the same private Bind/Reattach contract. See
[ADR 0290](../adr/0290-server-owned-session-placement.md).

### Multi-replica deployment & single-writer enforcement

By default mecatl assumes **session affinity** — route each session to exactly
one process. The in-process run registry enforces single-writer *within* a
process, but two replicas over one shared store have no cross-process exclusion
(the v1 posture). For a deployment that cannot guarantee affinity (e.g. a load
balancer that may reroute a session), wire an optional **session lease**
(cloud-native Phase 4) so the harness enforces single-writer itself: a
per-session lease is acquired at the run-entry funnel, held for the session's
life, renewed by a `Service`-owned goroutine, and released on session end /
shutdown. A second replica's run-start (or approve-resume) for a held session is
refused with **HTTP 409 Conflict / gRPC `FAILED_PRECONDITION`**, and a crashed
holder's lease lapses (after `--session-lease-ttl`, or immediately on process
death for the flock backend) so a survivor takes over. Backends:
`--session-lease-dir` (single-host flock), `--session-lease-k8s-namespace`
(`coordination.k8s.io` Lease, in-cluster — needs a least-privilege `leases`
Role), or `--session-lease-url` (a gRPC lease driver, independent of the store).
Empty = no leasing, the **byte-identical** affinity default. The flags + the
RBAC manifest are in the [usage guide](../usage.md) ("Cross-process session
leasing"); the seam and adapters are in [observability &
persistence](observability.md); the rationale and the resource/fidelity
re-audit are in `docs/adr/0027-cloud-native.md`.

### Permission & bash governance details (`engine/governance`)

The `permpolicy` adapter wraps `governance.Evaluator`. Resolution
(`evaluator.go`): a **Deny in any scope beats Ask beats Allow**; among rules of
the same effect the highest-precedence `Scope` wins (`Managed > CLI >
LocalProject > SharedProject > User > BuiltinDefault`); **no matching rule
defaults to Ask** (the
harness never silently allows an unconfigured call). Plan mode (`ModePlan`)
denies mutating tools (`Edit`, `Write`) and non-read-only `Bash` up front.
`ScopeBuiltinDefault` is the harness's built-in floor (read-allow /
mutate-ask), with ONE narrow exception to the same-effect tie-break: a
higher-scope configured Allow may loosen **only** a built-in-default Ask —
never a *configured* Ask. The floor also carries pre-approved (but
config-overridable) Allows for the memory tool family, the synthetic
`soul:apply` action, and the three read-only child-observability tools
(`InspectSubagent`/`InspectMember`/`SubagentStatus`, issue #37) — they loosen
no other tool's Ask. A client's allow-**always** verdict feeds
`PermissionPolicy.Learn` (over `engine/adapter/permstore`): a per-session
allow consulted at the lowest scope only, never overriding a deny or plan
mode.

**The posture ladder** (`internal/app/posture.go`) is the single operator knob for
how much the harness self-authorises. Four ordered tiers — **`strict < trusted < auto
< yolo`** (`--posture`, default `strict`) — fold to the maximum operator-tier value
(`resolvePosture`), then derive the underlying knobs (`applyPosture`, run before
`resolveTrust`). The two older flags are now **aliases**: `--trust-project` ≡ the
`trusted` tier (admit the project authority set, below), `--yolo` ≡ the `yolo` tier.
A project-file `posture:` is ignored with a WARN — posture is an operator-tier
decision (fail-closed core).

The allow-all postures (`auto` and `yolo`, `app.Config.AllowAllTools`) are **not** an
evaluator bypass: they inject a single `ScopeCLI`/`AudienceMain` allow-all rule into
the **main** engine's static ruleset (`mainRules` in `internal/app/build.go`) plus a
mirrored `AudienceSubagent` rule that binds children (`childRules`), loosening only the
`ScopeBuiltinDefault` mutate-ask floor. Deny-dominance and any deliberately configured
`Ask` are preserved exactly at **every** tier including `yolo`. The one extra step at
`yolo`: child command-substitution auto-runs too (`WithLooseSubstitution` extended to
children) — at `auto`/`trusted`/`strict` a child's `$(...)` still resolves through the
[subagents & teams](subagents-and-teams.md) child-ask model. See `docs/adr/0022-allow-all-posture.md` and the CLAUDE.md "CONFIG
axis" / "Posture ladder" notes.

For Bash, `bash.go` splits compound lines (`SplitCommands`, honouring quotes and
splitting on `&&`, `||`, `;`, `|`, a bare `&`, and newlines) and evaluates
**every** sub-command, taking the worst outcome — so a deny on `rm` blocks
`git status && rm -rf /`. `Canonicalize` strips a **closed, audited** set of
transparent wrappers (`timeout`, `time`, `nice`, `env`, `stdbuf`, `ionice`) but
deliberately **never** strips re-entrant launchers (`docker exec`, `npx`,
`sudo`, `devbox run`). `HasSubstitutionOrGrouping` flags `$(...)`, backticks,
`<(...)`, and `(`/`{` grouping and floors such segments at Ask (fail-safe) —
unless `SubstitutionReadOnly` clears it: a segment whose every
recursively-extracted inner command AND whose blanked outer are all read-only
resolves by the ordinary rule fold instead (global, main + children). For
ISOLATED children (worktree/force-copy forks), `IsolationApprovable`
additionally auto-approves read-only plus a minimal worktree-safe verb set —
the [subagents & teams](subagents-and-teams.md) 4-step child-ask model.
`ReadOnlyBash` classifies a command line as read-only for plan-mode gating and
is deliberately a SEPARATE, unchanged classifier.

### Workspace trust (`internal/app/trust.go`, `internal/adapter/workspacetrust`)

Whether a *project's* contributions are admitted — the **project authority set** —
is a **composition** decision, not a governance scope
(`governance`/`session`/`prompt`/`tool` stay trust-unaware). The project authority
set is: the project permission **ALLOW** rules, the project **soul**, and (Phase 2a)
the **project tier** of agent definitions, slash commands, and skills
(`<workspace>/.mecatl/*`, `<workspace>/.claude/*`). The decision is
produced once per process by `resolveTrust(cfg) TrustDecision` (MUST-FIX 2 of the
workspace-trust design), which folds, highest first:

1. `--trust-project` — the per-invocation operator flag (`TrustFlag`);
2. a **declarative** `trustedWorkspaces:` match (`TrustDeclared`) — Phase 1: the
   `internal/adapter/workspacetrust` leaf reads an operator-authored, **read-only**
   list of absolute workspace paths from the user-global `settings.yaml` (via the
   shared `xdgconfig` env seam) and answers "is this workspace declared-trusted?";
3. a **remembered** `trust.yaml` entry (`TrustRemembered`) — Phase 2b: a
   machine-written registry entry whose stored **identity-anchor hash** still
   matches the workspace's live identity surface. A present entry whose anchor
   **mismatches** ⇒ `Drifted` (and `Trusted=false` — fail-safe);
4. otherwise `TrustNone`.

`Build` collapses `decision.Trusted` back onto `cfg.TrustProject` before the
downstream build, so the existing consumers — `permconfig.Options.TrustProject`
and the soul provenance gate (`soulselect.go`) — honour declared trust through the
**exact same admission path** as the flag, with no adapter signature churn and no
bypass. The composition narrates the decision (a `workspace trust` INFO fact via
the injected `port.Diagnostics`: `trusted=… source=… drifted=…`), mirroring the
soul-selection narration.

**Phase 2a — the project-tier authority gate.** When the folded decision is
**untrusted**, composition also withholds the **PROJECT TIER ONLY** of
agents/commands/skills, mirroring how the project ALLOWs and project soul are gated:
the `agents`/`skills` adapters gained an additive `ResolveOptions.IncludeProjectTier`
(set to `cfg.TrustProject`; the three `internal/app` skills callers —
`resolveSkills`, the agent-def skill-preload `resolveSkillIndex`, and the
draft-overlap `activeSkillDirs` — all pass it), and `buildDirCommandExpander` drops
the default project-tier command dirs (`.mecatl/commands`, `.claude/commands`) when
`cfg.Workspace != "" && !cfg.TrustProject`, degrading to the `NoopExpander` so raw
text still passes through. The **user tier** (`$XDG_CONFIG_HOME/mecatl/*`,
`~/.claude/*`), the built-in tools, the base prompt, every Deny/Ask, the permission
prompt, and any **explicit** `--commands-dir`/`--agents-dir`/`--skills-dir`
(operator-supplied, not repo-injected) stay active — an untrusted repo degrades to
**"ask the human"**, never **"do nothing"**. The project soul carries a **double
gate** (trust provenance AND the `soul:apply` policy — a logical AND, reconciled in
`selectSoulSource`'s doc comment).

**Settings-vs-state split.** `trustedWorkspaces:` is config **DATA**, not a
governance `Rule`, and lives in the **human-authored** `settings.yaml` that mecatl
only ever *reads*. The **machine-written** trust registry (Phase 2b) is a
**separate** file — `<xdg>/mecatl/trust.yaml`, a sibling of but never inside
`settings.yaml`. `workspacetrust` reads it (`Remembered`) and writes it
(`Remember`, the only write path); `mecated` reads it declaratively and **never**
writes or prompts. Each entry is keyed by `realpath` and stores the
**identity-anchor hash** captured at trust time plus a `trustedAt` timestamp (the
timestamp is injected by composition — the adapter never calls `time.Now()`, so the
write is deterministic in tests). The **identity anchor** (`anchor.go`) is a
deterministic fold of the project **soul** ⊕ project-tier **agent** ⊕ **command** ⊕
**skill** definitions (sorted file set, per-file content hashes), and **explicitly
excludes `settings.yaml`** — permissions change every commit, so anchoring drift on
them would nag-fatigue the operator (they re-resolve live via permconfig's mtime
cache instead). A drift (entry present, anchor mismatched) re-gates to untrusted +
a WARN; the interactive re-prompt is the `mecatui` first-encounter prompt
(shipped — `cmd/mecatui/trust.go`), which prompts on first encounter or drift
and remembers via `workspacetrust.Remember`. The registry **write** uses `O_NOFOLLOW` + `0o600` +
temp-then-rename (mirroring `soulguard`'s sidecar write, CWE-59), and a corrupt /
oversized / wrong-version registry fails safe to untrusted. The SHA-256 primitive is
shared with `soulguard` via the `internal/adapter/hashutil` leaf (`SHA256Hex`) —
**only** the primitive is shared; the soul drift baseline (a soul-only `.sha256`
sidecar, re-blessed by `--approve-soul`) and the trust identity anchor (the
registry-stored fold, re-blessed by re-answering the prompt) stay **parallel**
mechanisms. **Path keying** is cleaned + absolute +
symlink-resolved (`filepath.Abs` then `EvalSymlinks`) on both sides, so a
moved/symlinked path cannot forge or inherit trust; an unresolvable entry is
skipped. Trust is **monotonic-positive**: it only ever *grants* admission of a
project's ALLOWs/soul — it never overrides a Deny or a configured Ask (those remain
deny-dominant in the evaluator). A missing/malformed/unparseable `settings.yaml`
**or** `trust.yaml` fails safe to untrusted (a corrupt config never grants trust).
See `docs/adr/0023-workspace-trust.md`.

## Prerequisites

- [The API surface being hardened](api-surface.md)

## Follow-on reading

- [Observability & persistence](observability.md)

## Related

- [Hooks & guardrails — operator-tier guardrails](hooks-and-guardrails.md)

---

[← Architecture guide](../architecture.md)
