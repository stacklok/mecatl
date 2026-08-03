# ADR 0011 — Soul and user-model: persistent identity and cross-session learning

- Status: Accepted
- Date: 2026
- Scope: `engine/prompt` (SoulSource port, SoulAssembler), `internal/adapter/soul`, `internal/app` (soul selection, drift detection, user-model review hook), `internal/adapter/memory` (user-model partition).
- Superseded by (injection mechanism only): [ADR 0043](./0043-ephemeral-turn0-instruction-fragments.md) — the soul fragment and the `<user-model>` block (like all turn-0 fragments) are no longer "injected as a persisted turn-0 user message"; they are assembled once per run and PREPENDED to the request EPHEMERALLY, never persisted into the conversation. The data-fenced, after-the-cache-breakpoint, agent-read-only semantics are unchanged — only the persistence is dropped.

## Context

mecatl had no persistent identity anchor: the agent's style and tone were baked into a single static `Config.Role` string in the cache-stable system prefix. There was no cross-session model of the operator's preferences. The Hermes reference implementation separates "who the agent is" (a user-authored, agent-read-only soul) from "what the agent knows" (agent-curated memory), and the community converged on a writable identity anchor being a security anti-pattern: a soul with a write path enables persistent prompt injection across all future sessions.

## Decision

Implement the soul as a user-authored, agent-read-only identity fragment (`~/.config/mecatl/soul.md`) injected at turn-0 as a fenced user-role message via the InstructionAssembler seam, with injection scanning and byte-capping at load. A project-sourced soul is trust-gated via `--trust-project` with user-wins precedence. Drift detection uses a harness-owned sha256 baseline sidecar; `--soul-strict` withholds a drifted soul. The user-model is a second, cross-project memory partition (`RememberUser`/`RecallUser`/`SearchUserModel` tools plus a turn-0 `<user-model>` block), with an optional Stop-hook background reviewer (`--user-model-review`) that never reopens the user's session. Both a `/soul` and `/usermodel` TUI inspection panel are provided. External dialectic modeling (Honcho-style) and per-user multi-tenant keying are explicit non-goals.

## Consequences

The agent has a stable, operator-controlled identity that survives compaction (re-read from disk each build) and cannot be overwritten by tool use. The user-model accumulates cross-session preferences without leaking into the governance scope. The background reviewer is off by default to avoid per-session LLM spend. Trust hierarchy and deny-dominance are unchanged; `soul:apply` and the memory tool names are pre-approved as lowest-scope floor Allows, overridable by higher-scope config. Current behaviour: docs/architecture.md. Shipped/deferred state: docs/design/PRODUCTION-READINESS.md.

---

> **Ground-truth corrections applied during implementation** (the as-built wins over
> the sketch below where they conflict):
> - **(A)** The soul is NOT read through the per-session `WorkspaceReader`. That handle
>   is rooted at the SESSION workspace; `~/.config/mecatl/soul.md` lives OUTSIDE any
>   session root. The adapter resolves it against the process environment via an
>   injectable `env` (getenv/userHomeDir/readFile), mirroring `permconfig`/`skills` —
>   exactly as `MemoryIndexAssembler` ignores its `ws` argument.
> - **(C)** No trust-gate in Phase 1. `--trust-project` gates project-sourced ALLOW
>   rules only; user-scoped config is always trusted, and `~/.config/mecatl/soul.md` is
>   user-authored on the user's own box. **UPDATE (Phase 3, Item 2, SHIPPED):**
>   trust-gating a *project-sourced* soul (a discovered `<workspace>/.mecatl/soul.md`)
>   is now wired — it is untrusted by default and honoured only with `--trust-project`
>   (the same issue-#13 gesture, no new flag), with USER-WINS precedence. The
>   **user-scoped soul stays ungated** (correction (C) preserved): it always loads if
>   present, regardless of `--trust-project`. See §8.2.
> - **(D)** ~~The content hash is DEFERRED to Phase 2~~ **SUPERSEDED (Phase 3, Item 1,
>   SHIPPED):** the hash now has a real consumer — **drift detection**. `soul.LoadWithMeta`
>   computes the sha256 of the clean body in the same read; the composition layer
>   (`internal/app/soulguard`) records it as a harness-owned baseline sidecar
>   (`<soulPath>.sha256`) trust-on-first-use, warns on a later mismatch, and (with
>   `--soul-strict`) refuses a drifted soul. The soul ADAPTER still computes the hash but
>   never writes the baseline — the agent-read-only invariant is intact.
> - The consumer-local port is named **`SoulSource`** (not `SoulReader`).
> Research basis: NousResearch/hermes-agent source read (`~/Development/hermes-dir`)
> and the `SOUL.md` community ecosystem. Sources at end.

## 1. The framing correction (read this first)

Issue #14 asks us to study how Hermes implements its "soul" and whether a
**self-modifying soul** fits mecatl. The first finding rewrites the question:

**Hermes's soul is _not_ self-modifying. The agent has no write path to it.**

Nous states the design intent directly: *"the separation of who the agent **is**
(`SOUL.md`) from what the agent **knows** (`MEMORY.md`) is the key architectural
insight."* There are two distinct subsystems, and conflating them is the central
trap:

| | **Who the agent IS** | **What the agent KNOWS** |
|---|---|---|
| Artifact | `SOUL.md` | `MEMORY.md`, `USER.md`, skills |
| Author | **User only** — no agent tool can write it | **Agent** (`memory()` / `skill_manage`) |
| Lifecycle | Static anchor, edited by hand | The "learning loop": nudges → background review → consolidation |
| Prompt slot | Stable tier, **slot #1**, verbatim | Volatile tier |
| Survives compaction by | being re-read from disk each build | being re-read from disk each build |

The "self-improving agent" / "deepening model of who you are across sessions"
marketing is the **right column**. The "soul" is the **left column** — a stable
identity anchor deliberately kept _outside_ the learning loop, precisely so the
loop can't corrupt it.

This matters for mecatl because the two halves have opposite security postures
(§4) and mecatl already owns most of the right column (§5).

## 2. How Hermes implements it (concretely)

### 2.1 The soul (`SOUL.md`) — identity, read-only to the agent

- Lives at `$HERMES_HOME/SOUL.md` (default `~/.hermes/SOUL.md`). Freeform Markdown,
  no enforced schema.
- Loaded by `agent/prompt_builder.py:load_soul_md()`: read → **prompt-injection
  scan** (`tools/threat_patterns.py`, scope `context`) → **truncate** (20k, head/tail)
  → return string or `None`.
- Injected at `stable_parts[0]` in `agent/system_prompt.py:build_system_prompt_parts()`
  — first fragment, verbatim, before tool guidance / memory / context files.
- Bootstrapped from `hermes_cli/default_soul.py:DEFAULT_SOUL_MD` on first run
  (`hermes_cli/config.py:_ensure_default_soul_md`). **Existing files are never
  overwritten.**
- **No tool writes it.** Only the user (text editor) or a profile-distribution
  `git` update can change it. The full system prompt is cached on
  `agent._cached_system_prompt` and rebuilt only after compaction.

### 2.2 The learning loop — user-model + skills, agent-writable

- **`MEMORY.md`** (env/project facts) and **`USER.md`** (model of the user), both
  under `$HERMES_HOME/memories/`, `§`-delimited, char-capped (2200 / 1375). Managed
  by `tools/memory_tool.py:MemoryStore`; injected into the **volatile** tier.
- **Nudge triggers** (`agent/conversation_loop.py`): a turn counter
  (`memory.nudge_interval`, default 10) and a tool-iteration counter
  (`skills.creation_nudge_interval`) set "review" flags.
- **Background review fork** (`agent/background_review.py`): on a nudge, Hermes
  forks an `AIAgent` in a daemon thread that **inherits the parent's cached system
  prompt verbatim** (for prefix-cache reuse), is handed the transcript, and is
  prompted to extract user persona/preferences/corrections → writes them back via
  the memory tool. Recursion disabled on the fork.
- **Session search** (`tools/session_search_tool.py`): FTS5 over `~/.hermes/state.db`
  for cross-session recall.
- **Optional Honcho plugin** (`agent/memory_provider.py`): external _dialectic_
  user modeling, recalled per-turn and injected into the volatile tier.

## 3. Convention status — `SOUL.md` is not a standard

Unlike `SKILL.md` (formal spec at agentskills.io, Linux-Foundation-governed,
adopted by Anthropic + OpenAI), `SOUL.md` is a **de facto community convention**
with no ratified schema. Two reference shapes exist: a slim Hermes-canonical one
(identity one-liner → style → avoid → posture) and a richer community template
(worldview, opinions-by-domain, tensions/contradictions, boundaries). Adopting it
buys ecosystem familiarity, not interoperability guarantees.

## 4. Security & governance — the part that matters most for mecatl

The community (hermes-soul-governance, prompt-security/clawsec `soul-guardian`,
relic, SoulTavern) independently converged on one lesson:

> **A _writable_ identity anchor is broken.**

Three threats, all directly in mecatl's `engine/governance` wheelhouse:

1. **Prompt injection _into_ identity.** Untrusted content (a fetched page, a
   cloned repo's file, an imported persona) rewrites who the agent is —
   _persistently, across all future sessions_. This is strictly worse than a
   single-turn injection. Hermes's mitigation: the soul has no write path + a
   load-time injection scan. The scan is necessary-not-sufficient.
2. **Drift via legitimate writes.** Behavioral _rules_ mistakenly stored in the
   writable memory degrade under compression. This is the entire motivation for
   the `hermes-soul-governance` project. Conclusion: rules belong in the
   read-only anchor, never in agent-curated memory.
3. **Portability / supply-chain.** `relic`'s "one soul, many agents, bidirectional
   sync" means write access to one agent's workspace compromises every connected
   agent's identity. `SoulTavern` treats _all imported persona content as untrusted_
   and wraps it in an operator-level identity directive + trust banner + sanitiser.

**How this maps onto mecatl (the encouraging part).** mecatl already has the exact
primitives this threat model demands:

- **Deny-dominant scope hierarchy** + `--trust-project` gate → a soul imported from
  a project/external source is _untrusted by default_, same as project permission
  allows.
- **Read-only `WorkspaceReader`** (issue #13) → the soul file is loaded through a
  handle that _cannot mutate_, by construction.
- **`ScopeUser`** already exists (`~/.config/mecatl/settings.yaml`) as the one
  user-global, non-project scope → the natural home for a user-scoped soul.

Design rule we adopt from this: **the soul is read-only to the agent loop, lives in
a user scope, is injection-scanned + content-hashed at load, and an externally
imported soul is trust-gated exactly like a project allow.**

## 5. What mecatl already has

mecatl already owns most of the **right column** (what-it-knows):

- **Two-layer prompt** (`engine/prompt/builder.go`): `StablePrefix` (cache-stable,
  `Config.Role`/`Tone`/`Safety`) + `VolatileSuffix` (`<env>`).
- **`InstructionAssembler` / `MultiAssembler`** (`engine/prompt/instructions.go`):
  the turn-0 context-injection chain. `RootAssembler` (AGENTS.md > CLAUDE.md) and
  `MemoryIndexAssembler` already ride it. **This is the seam.**
- **`MemoryIndexSource`** (`engine/prompt/memoryindex.go:19`): the existing
  _consumer-local port_ pattern — `prompt` declares a minimal interface the adapter
  satisfies structurally, no import cycle. The template for a soul source.
- **Memory store** (`internal/adapter/memory/store.go`): BM25 search, flock-safe,
  but **project-scoped**, not per-user.
- **Dream consolidation** (`internal/adapter/dream/dream.go`): Pattern 4 — conservative
  merge/forget on a ticker. The GC half of a learning loop already exists.
- **Hooks** (`port.HookRunner`): `Stop` / `SessionStart` attachment points.
- **Agent definitions** (`internal/adapter/agents/`): could define a soul-reflection
  subagent.

**Gaps** (what's missing for both halves):

- **G1** Memory is project-scoped; a soul needs **user scope** (`~/.config/mecatl/`).
- **G6/G8** No user-identity concept; `ScopeUser` holds only permission rules, not
  persona/preferences.
- No agent-read-only persona fragment in the prompt today (`Config.Role` is one
  static string, baked into the cache-stable prefix — wrong place for per-user
  identity).
- No transcript-as-corpus access for cross-session inference; no nudge/background
  review trigger that _generates_ new entries (dream only _compacts_ existing ones).

## 6. Proposal — two phases, persona first

### Phase 1 — the persona/soul (low-risk, high-fit). RECOMMENDED.

A user-scoped, **agent-read-only** identity fragment, injected as a turn-0 user
message (not the cache-stable prefix), with governance-grade load discipline.

```
engine/prompt/soul.go        SoulSource (consumer-local port, mirrors MemoryIndexSource)
                               SoulAssembler implements InstructionAssembler
internal/adapter/soul/         Store over ~/.config/mecatl/soul.md (env-injected,
                               NOT the WorkspaceReader — the file is outside any session root)
                                 - Load: resolve → read → trim → byte-cap → injection-scan
                                   (reuses skills.ScanForInjection); fail-soft to "" at each branch
                                 - no Write/Create/WriteFragment anywhere (no agent path)
internal/app/build.go          wire SoulAssembler into buildInstructionAssembler,
                               after RootAssembler and BEFORE MemoryIndexAssembler,
                               fenced in <soul>…</soul>
```

> As-built note: the hash step in the original sketch is DEFERRED to Phase 2 (no Phase-1
> consumer); there is no trust-gate (user-scoped config is always trusted). On by
> default reading the conventional path; `--soul-file` overrides it, `--no-soul` disables.

Properties, each tracing to §4:

- **Read-only to the agent** — no tool, no `WriteFragment`. (threat 1, 2)
- **User-scoped** — `~/.config/mecatl/soul.md`, applies across all projects. (G1)
- **Turn-0 user message, data-fenced** — never the stable prefix (keeps cache
  byte-stability; matches how `MemoryIndexAssembler` injects). Survives compaction
  by being re-read from disk each build.
- **Injection-scanned + byte-capped at load** (reusing `skills.ScanForInjection`); a
  hit/oversize degrades to no fragment. The content hash and trust-gating of an
  *imported* soul are deferred to Phase 2 (Phase 1 reads only the user-authored,
  always-trusted user-scoped path). (threats 1, 3)
- Fail-soft: a missing/empty/oversized/flagged soul degrades to no fragment, never
  an error.

Phase 1 is small, violates no layering rule, reuses an established seam, and lands
squarely inside mecatl's existing governance story. It is the spike's recommended
deliverable.

### Phase 2 — the user-model learning loop (2a + 2b SHIPPED)

The "what it knows about _you_" half: a user-scoped model the agent _does_ curate,
plus an optional background review pass. As-built:

- **User-scoped memory partition (2a, default-on)** — a SIBLING `memory.Store` (a
  SECOND `memory.New(dir)` instance) rooted at `<xdg>/mecatl/usermodel`, CROSS-PROJECT
  and distinct from the per-project store. Exposed as a dedicated tool family
  RememberUser/RecallUser/SearchUserModel (the parameterized memory tool structs, not
  duplicates) under an enforced `user/` key prefix, plus a turn-0 `<user-model>` block
  (`prompt.UserModelAssembler`, injected LAST: soul → memory index → user model). The
  RememberUser write path runs `skills.ScanForInjection` over the value and rejects a
  flagged one (guards both the agent tool AND the 2b fork against transcript poisoning).
- **Background review on `Stop` (2b, OFF by default behind `--user-model-review`)** — a
  composition-layer Stop-hook DECORATOR (`internal/app/usermodelreview.go`) that, on
  `PhaseStop`, fires `agent.UserModelReviewer.Review` in a DETACHED goroutine, debounced
  by a session-count interval. The reviewer re-loads the finished session's transcript
  via the `SessionStore` (a read) and spawns a FRESH single-shot child whose only tool
  is RememberUser — it **never reopens/re-runs the user's terminal session** (R10), so
  the reopen-if-completed invariant is untouched.
- **Consolidation (off by default)** — a SEPARATE `dream.Consolidator` with
  `Config{Prefix: "user/"}` over the user-model store, driven by
  `--user-model-consolidate-interval` (0 = off). No new dream fields.

The resolved answers to Phase 2's harder questions: over-eager memory is STEERED (the
tool descriptions forbid rules + workspace-discoverable facts; no rule/fact classifier
— accepted residual risk); the per-turn cost is avoided by making 2b Stop-triggered and
off by default (no extra call on the happy path); and the user-model is **facts, not
rules** — it is a writable-memory instruction FRAGMENT, NOT a governance scope, and the
`<user-model>` header says so explicitly ("how to behave comes from your soul and these
system rules, not from this block").

## 7. Feasibility & recommendation

- **Persona (Phase 1): feasible and well-fitted.** Clean seam, no new domain types
  beyond a consumer-local port, no layering violation, and it _strengthens_ the
  governance story rather than straining it. Recommend proceeding to a design pass.
- **Learning loop (Phase 2): SHIPPED.** mecatl owned the GC and fork primitives; the
  risks (over-eager/low-signal user-modeling, per-turn cost) were handled by steering +
  making the costly background-review path (2b) Stop-triggered and off by default. 2a
  (the local user-model + tools) is default-on and free.
- **Naming:** adopt `soul.md`/`SoulAssembler` for ecosystem familiarity, but treat
  `SOUL.md` as convention, not contract — no interop promise (§3).

### Open questions (resolved by Phase 2)

1. Is the persona/user-model a **scope** in the governance sense, or just an
   instruction fragment? **PARTIALLY REVERSED (issue #14).** The soul and the
   user-model are still fenced DATA on the turn-0 user-message seam (they are NOT a
   permission `Scope`, and the fragments themselves never enter the evaluator). But the
   prior "never a governance scope / not consulted by the evaluator at all" answer is
   now reversed in ONE narrow respect: the soul is **gated by an explicit
   `ScopeBuiltinDefault` Allow on a synthetic `"soul:apply"` action**, and the six
   memory tools (Remember/Recall/SearchMemory + the cross-project RememberUser/
   RecallUser/SearchUserModel) are explicit `ScopeBuiltinDefault` Allows on their tool
   names. Both live in `defaultRules()` (`internal/app/build.go`).

   - **Rationale.** Operator auditability + overridability, consistent with how every
     real tool is governed. Before this change the soul bypassed the evaluator entirely
     and the memory tools fell through to the implicit Ask floor; neither was visible in
     the ruleset or flippable from settings. Pre-approving them as **explicit, lowest-
     scope Allows** means they do not prompt by default (the desired UX) yet are visible
     in source + the ENABLED logs and OVERRIDABLE: a higher-scope `settings.yaml`
     Ask/Deny on `Remember` or `soul:apply` still wins.
   - **Safety argument (the invariants are structurally untouched).** `soul:apply` and
     the memory tool keys are at `ScopeBuiltinDefault` — the LOWEST scope — and are
     tool-name-exact. A floor Allow can only ever LOSE to a higher-scope Ask/Deny and
     can never loosen any OTHER tool's Ask, so the deny-dominant rule and the
     "config Allow loosens only the floor, never a configured Ask" rule (issue #13) hold
     unchanged. `soul:apply` is colon-namespaced so it can never collide with a real
     tool name.
   - **The soul gate.** The synthetic action is consulted at **soul-load (build time)**
     in `selectSoulSource`, through the SAME governance evaluator + `permconfig`
     resolver the real tool policy uses (project rules still gated by `--trust-project`,
     resolved against the workspace root). Allow ⇒ apply the soul (existing USER-wins /
     trust / drift selection unchanged); Deny ⇒ withhold (no fragment); **Ask ⇒ withhold
     with a warning**, because the soul is applied at build time with no interactive
     gate — an Ask cannot be satisfied, so the fail-safe is to not apply it (set
     `soul:apply` → allow to apply it).
2. Multi-user: the embedded/gateway surfaces are effectively single-user today.
   **RESOLVED (for now): "the operator" is implicitly singular.** The user-model store
   uses no per-user identity key — it is one cross-project store per host config dir,
   the same single-operator trust-zone assumption the soul and `MEMORY-TIERING.md`
   carry. Per-user/multi-tenant keying is explicitly out of scope.
3. Do we want a `/soul` or `/usermodel` TUI affordance (view current soul/user-model),
   mirroring `/agents` and `/skills`? **RESOLVED — SHIPPED (Phase 3, Item 3).** Both
   exist as caps-gated, read-only mecatui panels: `/soul` (a scrollable persona
   inspector showing content + provenance/trust/drift) and `/usermodel` (a
   key→description list with aggregate size/hash), backed by the `GetSoul` /
   `GetUserModel` unary RPCs and the `soul` / `user_model` capability bits. Trust and
   drift are COMPUTED in composition (`internal/app/soulsnapshot.go`) and projected
   into proto; the ui only displays the strings, never decides trust. See
   `docs/tui.md`.

## 8. Remaining scope — decisions, not deferrals

Everything beyond Phase 2 is a recorded decision here, not a silent TODO. Three items
**finish** the soul feature (Phase 3, since shipped — see the status header); two are **explicit non-goals**,
each because it contradicts an already-documented mecatl posture — so the answer is
derivable from the docs, not a judgement call left open.

### Phase 3 — completion work

1. **Drift detection + integrity. — SHIPPED (Item 1).** A content hash (sha256)
   baseline of the soul computed at load (`soul.LoadWithMeta`, over the clean body, in
   the same read), with tamper detection and an alert — the `clawsec` `soul-guardian`
   model from §4. This is what gives the Phase-1 hash (deferred in correction **(D)**) a
   real consumer at last. As built:
   - The baseline is a **harness-owned sidecar** next to the soul:
     `<soulPath>.sha256` (for `--soul-file PATH`, `PATH.sha256`). The WRITE lives ONLY in
     the composition layer (`internal/app/soulguard.go`); the soul adapter stays
     write-free.
   - **Trust-on-first-use:** no sidecar at load → the current hash is written as the
     baseline + `slog.Info("soul: baseline established")`.
   - **Warn-and-load (default):** a later run whose hash differs logs a `slog.Warn` drift
     alarm with both hashes and STILL loads (a hand-edit on the operator's own box is
     expected). Drift is NOT a governance gate — the soul is fenced DATA, never a
     permission scope.
   - `--approve-soul` (re)writes the baseline to the current hash (accept an edit);
     `--soul-strict` makes a DRIFTED soul contribute no fragment.
   - **Restore-to-baseline is DEFERRED (future opt-in).** A hash-only baseline gives
     detection + alert without a harness-owned COPY of the approved bytes — which would be
     both a content WRITE surface (contradicting the agent-read-only posture) and
     disproportionate for an MVP. If wanted later it is an explicit, operator-gated
     `--restore-soul` that copies an approved snapshot back; out of scope here.
   - User-model snapshot hashing is left to a later item — Item 1 is scoped to the SOUL.
2. **Trust-gating an imported / project-sourced soul. — SHIPPED (Item 2).** A soul that
   does NOT originate from the user's own `~/.config/mecatl/` — a soul file **discovered
   in a cloned repo** — is **untrusted by default** and gated exactly like a project
   ALLOW via `--trust-project` (the issue #13 mechanism — NOT a new trust concept or
   flag). This is the single context where the spike's "trust-gate" (§4) actually
   applies; the user-scoped soul stays ungated (correction **(C)**). As built:
   - **Two provenances.** USER: the conventional `<xdg>/mecatl/soul.md` (fallback
     `~/.config/mecatl/soul.md`) or an explicit `--soul-file PATH` — always trusted.
     PROJECT: a discovered `<workspace>/.mecatl/soul.md` (parallel to
     `.mecatl/settings.yaml`, resolved per the build-time workspace) — untrusted by
     default.
   - **USER-WINS precedence (single identity anchor; NOT a merge).** When a user-scoped
     soul is present it is used and the project soul is **ignored**. The project soul is
     used ONLY when (a) `--trust-project` is set AND (b) no user-scoped soul is present.
     Two simultaneous soul blocks are explicitly avoided.
   - **Untrusted = dropped silently-but-LOGGED** (`slog.Warn`, mirroring `applyTrustGate`'s
     report posture), never an error. The project soul then contributes no fragment.
   - **Same loader discipline.** A trusted project soul loads through the SAME
     `soul.Store` (byte cap, injection scan, fence reject) and the SAME Item-1 drift
     baseline (the drift check runs against WHICHEVER soul wins — never both).
   - **Provenance metadata.** The selected soul carries `Provenance` (User|Project) +
     `Trusted` + `Drifted` + hash/size in a composition-level `soulMeta`
     (`internal/app/soulselect.go`). Item 3 (SHIPPED) projects it — plus the loaded
     content — into the proto `SoulInfo` via `internal/app/soulsnapshot.go`.
   - **Layering.** The trust decision lives in `internal/app` (composition), reusing
     `Config.TrustProject`. `engine/prompt` stays trust-unaware (the `SoulSource`
     interface is unchanged); `engine/governance` is NOT involved (the soul is fenced
     DATA, not a permission scope); the `internal/adapter/soul` loader stays write-free
     (the new `soul.NewWithEnv` is an env-injectable READ constructor, no write path).
3. **`/soul` + `/usermodel` TUI inspection. — SHIPPED (Item 3).** Read-only browsers
   showing the current soul/user-model content, byte size, hash, and trust state —
   mirroring the `/agents` and `/skills` inventory views. As built:
   - **Two unary RPCs** mirroring `ListSkills`. `GetSoul` returns a build-time
     `SoulInfo` snapshot (content + size + sha256 + present + provenance + trusted +
     drifted); `GetUserModel` returns the **live** user-model index (key + description
     per entry, plus aggregate size + hash). Two new `ServerCapabilities` bits — `soul`
     (a soul source is wired) and `user_model` (the user-model store is wired) — gate
     the panels, exactly as `skills` gates `/skills`.
   - **Soul = a build-time SNAPSHOT** projected in `internal/app/soulsnapshot.go` from
     the Item-2 `soulMeta` + the loaded body. **User-model = a LIVE lister** wrapping
     the SAME user-model store's read-only `Index` (never a second store on the same
     dir — the one-Store-per-dir lock invariant holds).
   - **The `/soul` panel is SCROLLABLE** (the only divergence from the short-list
     `/skills` template): the persona body can be up to 20 KiB, so the panel shows a
     line-window with pgup/pgdn (and up/down, home/end) scroll plus a "lines X–Y of N"
     indicator, instead of dumping multi-KB into a centred card. `/usermodel` is a
     short key→description list like `/skills`.
   - **Trust/drift are COMPUTED in composition and PROJECTED** into proto; the ui only
     displays the strings (`soulTrustLabel`), never decides trust. The render/client
     packages import no `internal/...` and no proto directly, per the TUI layering rule.

### Explicit non-goals (decided against, with rationale)

4. **External dialectic modeling (Honcho-style) — NON-GOAL.** Adding an external
   memory/user-modeling provider port + a hosted-service adapter contradicts mecatl's
   **local-first / "no external by default" posture** (the embedded-server default
   enables only free+local features; external/networked stays off). The local user-model
   (2a) + consolidation (2b) already deliver cross-session user-modeling with no external
   dependency, no secrets, and no network egress. Revisit ONLY if a concrete need for
   portable / cross-device user-modeling arises — and then as its own RFC, behind an
   explicit opt-in, not as soul work.
5. **Per-user multi-tenant keying — NON-GOAL.** mecatl is single-operator today: the
   embedded TUI host and the gateway both serve one operator, and there is no
   user-identity concept in the `session` aggregate. Threading a per-user key through the
   aggregate + partitioning the soul/user-model stores per user would build machinery
   **for a surface that does not exist** — the wrong abstraction written on spec ("the
   cheapest abstraction is the one you don't write yet"). The single-operator trust-zone
   assumption (Open question #2, `MEMORY-TIERING.md`) holds until a genuine multi-tenant
   surface lands; this becomes in-scope the moment one does, and not before.

## Sources

- NousResearch/hermes-agent (source read): `agent/prompt_builder.py`,
  `agent/system_prompt.py`, `agent/background_review.py`, `agent/conversation_loop.py`,
  `tools/memory_tool.py`, `hermes_cli/default_soul.py`, `agent/memory_provider.py`.
- Hermes docs: `website/docs/user-guide/features/personality.md`;
  hermes-agent.nousresearch.com/docs.
- Ecosystem: jangyuxue/hermes-soul-governance, prompt-security/clawsec
  (`soul-guardian`), LucioLiu/relic, imphillip/SoulTavern, aaronjmars/soul.md.
- mecatl: `engine/prompt/{builder,instructions,memoryindex}.go`,
  `internal/adapter/{memory,dream}`, `engine/governance/permission.go`.


---

*Part of the [design docs](../design/README.md). Related: [Genuine tiered memory (closing the tier-0 gap)](0009-tiered-memory.md), [Tier-2 / semantic memory recall — assessment + buildable design](0010-semantic-memory-recall.md), [Conversation compaction](0012-compaction.md).*
