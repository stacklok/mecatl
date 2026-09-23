# ADR 0007 — Twelve agentic-harness patterns: pluggability audit

- Status: Historical
- Date: 2026-05-29
- Scope: twelve recurring agent-harness patterns — pluggability gaps and DDD-correct seams to close them

## Context

After the v1 core shipped, mecatl was audited against twelve recurring agent-harness patterns to identify which were fully pluggable, which were partial, and which were missing. The audit was conducted by a software-architect agent, verified against source on 2026-05-29. Four patterns were fully pluggable with no gaps; eight had seam or coverage issues requiring additional work packages.

## Decision

Prioritize five work packages to close the gaps: fire the three missing hook phases (P1), add a layer-2 permission classifier decorator (P2), introduce a scoped instruction-assembly seam (P3), add progressive tool disclosure (P4, gated on MCP tool count), and add fork-join parallelism (P5, gated on a forcing function). Patterns already complete (1, 6, 7, 11) and seams that are correct but need only additional adapters (5, 10-layer-1) are explicitly not gold-plated.

## Consequences

All five work packages shipped; all gaps in the audit are now closed. The audit is superseded as a live status tracker — it is preserved here as the rationale record for why each seam exists in its current form. Current behaviour is in `docs/architecture.md`; shipped and deferred items are in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

Audits mecatl against 12 recurring agent-harness patterns. For each pattern: what it is, its
status in the code, whether it sits behind a DDD seam (a port/interface a new
adapter can implement) or is hardcoded, and the smallest DDD-correct seam to
close any gap.

Author: software-architect agent. Verified against source on 2026-05-29.
Layering rules assumed (from `ARCHITECTURE.md` and verified by import audit):
`agent` imports only `session`/`port`/`tool`/`governance`/`prompt`+stdlib;
domain owns the interfaces the loop consumes; adapters are wired at
`cmd/mecated`. `governance` imports nothing from `internal` (session-free).
`FileSystem`/`Workspace` live in `engine/tool` to break a `port↔tool` cycle.

## Summary table

| # | Pattern | Status | Behind a seam? | Seam / location |
|---|---------|--------|----------------|-----------------|
| 1 | Persistent instruction file | Implemented | Yes | `prompt.DiscoverInstructions` over `tool.Workspace` FS port. **System-prompt CONTENT enhancement shipped (#19):** rewritten role/tone/safety, generated `toolDisciplineHints`, `<env>` `shell`/`<git-status>`, plan-mode reminder, per-model `agencyDelta` in composition. |
| 2 | Scoped context assembly | Partial (root-only) | Partial | `prompt.DiscoverInstructions` — single file, no scope ladder; `governance.Scope` ladder exists but unused for instructions |
| 3 | Tiered memory | Missing (v1 non-goal) | No seam | none |
| 4 | Dream / sleep consolidation | Missing (v1 non-goal) | No seam | none |
| 5 | Progressive compaction | Partial (single-stage) | Yes | `agent.Compactor` interface; `HeuristicCompactor` default |
| 6 | Explore-plan-act | Implemented | Yes | `session.PermissionMode` (`ModePlan`) + `Catalog.Available(mode)` + `governance` plan-mode gate |
| 7 | Context-isolated subagents | Implemented | Yes | `agent.TaskTool` over an injected child `*Engine`; `tool.Tool` seam |
| 8 | Fork-join parallelism | Missing | No seam (substrate exists) | read-parallel dispatch exists; no fan-out-of-subagents seam, no worktree port |
| 9 | Progressive tool expansion | Partial (eager) | Partial | `tool.Catalog` is the registration seam; all tools eager-registered, no metadata/lazy-hydration seam |
| 10 | Command risk classification | Partial (layer 1 only) | Yes (for L1) | `port.PermissionPolicy` (deny→ask→allow, plan gate, compound-Bash); no layer-2 classifier |
| 11 | Single-purpose tool design | Implemented | Yes | `tool.Tool` + `tool.Catalog`; typed Read/Edit/Write/Grep/Glob/WebFetch, Bash last-resort |
| 12 | Deterministic lifecycle hooks | Partial (under-fired) | Yes | `port.HookRunner` + `governance.HookEvent`/`HookPhase`; only Pre/PostToolUse + SubagentStop fire |

**Fully pluggable, no work needed:** 1, 6, 7, 11. The compaction (5) and
permission (10) *seams* are also correct and need no structural change — only
additional adapters/decorators to realise more of the pattern. Do not gold-plate
these.

---

## 1. Persistent Instruction File

**What it is:** A known-path file (`CLAUDE.md`/`AGENTS.md`) loaded as ground
truth at session start so conventions aren't relitigated each turn.

**State:** Implemented. `prompt.DiscoverInstructions`
(`engine/prompt/builder.go:153`) reads `AGENTS.md` (winning) then `CLAUDE.md`
(fallback) and returns them as **user-role** messages with a provenance marker.
The loop calls it once on the first turn (`engine/agent/loop.go` `recordPrompt`,
gated on `sess.Counters.Turns == 0`).

**Pluggable?** Yes. It reads through `tool.Workspace` (the FS port), never `os`,
so discovery is adapter-agnostic (osfs/memfs). The role decision — instructions
ride in a user message, never the elevated system role — is a deliberate
injection-resistance choice and correct.

**Gap + recommendation:** None structural. The discovery *policy* (which
filenames, precedence, role) is a free function, not an interface. That is
correct — there is one implementation and no second caller asking to vary it. Do
not extract an interface here (premature abstraction). If pattern 2 is built, the
discovery logic moves behind the assembler seam proposed there.

---

## 2. Scoped Context Assembly

**What it is:** Walk CWD→repo-root (plus user/managed scopes) collecting every
instruction file, concatenate with a precedence order; subdir files load lazily.

**State:** Partial. `DiscoverInstructions` reads only **one** file at the
**workspace root** — no parent-directory walk, no user (`~/.claude`) scope, no
managed (`/etc`) scope, no per-subdir lazy load, no `@import`. Notably,
`governance` already models the exact precedence ladder this pattern needs —
`Scope` (`Managed > CLI > LocalProject > SharedProject > User`,
`engine/governance/permission.go`) with `HasHigherPrecedenceThan` — but it is used
only for *permission* rules, not instruction assembly.

**Pluggable?** Partial. The FS seam (`tool.Workspace`) is right, but there is no
*assembler* interface; the single-file behaviour is baked into the function.

**Gap + recommendation:** Introduce an instruction-assembly seam consumed by the
loop, owned by the `prompt` context (the loop already imports `prompt`):

```go
// engine/prompt/instructions.go
type InstructionSource struct {
    Scope   Scope   // prompt-local precedence enum (see note)
    Origin  string  // e.g. "AGENTS.md", "~/.claude/CLAUDE.md"
    Content string
}

// InstructionAssembler resolves the ordered set of instruction fragments for a
// workspace. The default walks the workspace root only; richer adapters add the
// parent walk, user, and managed scopes.
type InstructionAssembler interface {
    Assemble(ctx context.Context, ws tool.Workspace) ([]InstructionSource, error)
}
```

- **Package:** `engine/prompt` (already imported by the loop). Use a
  `prompt`-local `Scope`/precedence rather than reusing `governance.Scope`: the
  permission ladder and the instruction ladder are different bounded concerns
  that merely share an ordering; coupling `prompt` to the permission context's
  vocabulary would leak language across contexts.
- **Default impl:** `RootAssembler` = today's single-file root behaviour, so v1
  behaviour is unchanged. A `LayeredAssembler` adds the parent walk + user scope
  later, behind the same interface.
- **Loop change:** `recordPrompt` calls `assembler.Assemble` instead of the free
  function. **This touches the loop** — serialize against other loop work.
- **Tests:** assembler returns fragments in precedence order from a memfs tree;
  loop records them as user messages on turn 0 only.
- **DoD:** loop depends on `InstructionAssembler`; default reproduces current
  behaviour byte-for-byte; a memfs-backed layered adapter resolves two scopes in
  the right order.

Defer the lazy per-subdir load (a `CwdChanged`-triggered reload) — it couples to
a hook phase that isn't fired yet (pattern 12).

---

## 3. Tiered Memory

**What it is:** Cross-session knowledge split into a small always-loaded index
(`MEMORY.md`), on-demand topic files, and searchable raw transcripts.

**State:** Missing. Documented v1 non-goal. The `memory`/`tier` grep hits in the
tree are `memstore`/`memfs` (in-memory *adapters*), unrelated. There is no
cross-session knowledge store, no index, no consolidation.

**Pluggable?** No seam exists. `port.SessionStore` persists *sessions*, not
distilled cross-session memory — a different concept; do not overload it.

**Gap + recommendation:** Correctly deferred. The smallest future seam, when
wanted, is a retrieval port the loop consults during context assembly — built
**together with** pattern 2's assembler, since tiered memory is "scoped context
assembly extended across sessions." Natural shape:

```go
// engine/port/memory.go (future)
type MemoryStore interface {
    Index(ctx context.Context, sessionRoot string) (string, error)  // tier 0
    Load(ctx context.Context, ref string) (string, error)           // tier 1
    Search(ctx context.Context, query string) ([]MemoryHit, error)  // tier 2
}
```

Owned by `port` (consumed by the loop), implemented by a future
`adapter/filememory`. **Do not build the seam yet** — no second consumer, no
concrete tier-1/tier-2 requirement. Writing the interface now is speculative.
Note it as the planned extension point and move on.

---

## 4. Dream / Sleep Consolidation

**What it is:** A background idle-time process that dedupes/prunes/rewrites the
memory store.

**State:** Missing. Documented v1 non-goal, and the source article rates this
the least load-bearing of the twelve.

**Pluggable?** No seam, correctly so — it presupposes pattern 3, which doesn't
exist.

**Gap + recommendation:** No action. When pattern 3 lands, consolidation is a
`Consolidator` over the same `MemoryStore`, run off the hot path (cron/idle, not
the agent loop). Building any seam now is pure speculation. Explicitly out of
scope.

---

## 5. Progressive Context Compaction

**What it is:** A cascade of compression stages of increasing aggressiveness so
recent turns stay raw and old turns collapse.

**State:** Partial — single-stage, not a cascade. `HeuristicCompactor`
(`agent/compaction.go:42`) does one pass: keep system + first user goal,
synthesize a touched-paths summary, truncate oversized tool bodies, keep the last
N turns verbatim. The loop triggers it at `CompactionRatio` of the context window
(`loop.go:304` `maybeCompact`), best-effort (a failure never aborts the run).

**Pluggable?** **Yes — this is the model seam.** `agent.Compactor`
(`compaction.go:24`) is a clean single-method interface
(`Compact(ctx, *Conversation) ([]Message, summary, err)`), injected via
`Deps.Compactor` with a sensible default. An LLM-backed summariser or a
multi-stage `CascadeCompactor` slots in **without touching the loop**.

**Gap + recommendation:** The four-stage cascade (HISTORY_SNIP → microcompact →
context collapse → autocompact) is a v1 non-goal, and the seam already supports
it: a `CascadeCompactor` composing several `Compactor`s behind the same interface
needs no domain or loop change. **No structural work.** If/when built, it is a
new adapter-level type in `agent` (or a package implementing `Compactor`);
default stays `HeuristicCompactor`.

Minor observation, not a finding: `estimateTokens` (`loop.go:329`) is a coarse
chars/4 heuristic. If a real tokenizer is ever needed it would become a small
`TokenEstimator` port — but with one implementation and no second caller, leave
it inline. Do not abstract.

---

## 6. Explore-Plan-Act Loop

**What it is:** Phased workflow with monotonically increasing tool permissions;
"plan mode" reads/reasons but cannot mutate until approved.

**State:** Implemented. `session.PermissionMode` (`session/session.go:49`) with
`ModePlan`; the catalog hides non-read-only tools in plan mode
(`Catalog.Available`/`Specs`, `tool/catalog.go:70`); and the governance evaluator
*also* denies mutating actions in plan mode as defense-in-depth
(`governance/evaluator.go:66` `planModeDecision`, including per-command Bash
classification). Plan-mode read-only is enforced at two layers (catalog
visibility + permission deny).

**Pluggable?** Yes. Mode is a session attribute; the gate lives in `governance`
(session-free) behind `port.PermissionPolicy`; tool visibility is computed by the
`Catalog`. A new mode or different gating policy is a governance change plus a
mode constant — no loop surgery.

**Gap + recommendation:** None. The explore→plan→act transition (approve a plan,
flip the mode) is driven by the client over the bidi API via session mode, which
is the right place for it. No seam to add.

---

## 7. Context-Isolated Subagents

**What it is:** A separate agent instance with its own context window, system
prompt, tool allowlist, and permission mode; only its final summary returns to
the parent.

**State:** Implemented. `agent.TaskTool` (`agent/subagent.go:75`) is a
`tool.Tool` that runs a **child `*Engine`** with a fresh `session.New`, its own
(tighter) `Limits`, a scoped read-only catalog, and an allow-all child policy.
`drainChild` (`subagent.go:257`) consumes the child's *entire* event stream
internally — only the terminal summary folds back, which is the isolation
guarantee. The child catalog excludes `Task` itself (no recursion) and is
read-only (so `TaskTool.ReadOnly()==true` is sound for read-parallel dispatch).

**Pluggable?** Yes — exemplary. The subagent is constructed at the composition
root (`cmd/mecated:buildTaskTool`) by injecting a child `*Engine`; the parent
`Engine` is never mutated. Options (`WithChildLimits`, `WithChildMode`,
`WithChildSessionPrefix`, `WithSubagentStopHook`) make the child configurable
without changing the type. A forked-context subagent (inheriting the parent
transcript) would be a different child-`Engine` wiring, same `tool.Tool` seam.

**Gap + recommendation:** None. The one latent coupling — `ReadOnly()` is sound
only while the child catalog stays read-only — is documented as an invariant on
the method (`subagent.go:181`). That is the correct place for it.

---

## 8. Fork-Join Parallelism

**What it is:** Fork N subagents into isolated git worktrees, run them in parallel
against the same task, then merge/select the best.

**State:** Missing. The read-only **substrate** exists — the dispatcher runs
maximal batches of read-only tools concurrently (`agent/dispatch.go:84`
`runReadBatch`), and `TaskTool.ReadOnly()==true` means **multiple `Task` calls in
one turn already fan out concurrently**. But there is no first-class
"fork N variants of the same task, isolate each in a worktree, join/select"
construct, and no worktree/isolation port.

**Pluggable?** No dedicated seam. Two missing pieces: (a) an isolation port so a
forked child runs against an independent working tree, and (b) a join/select
construct.

**Gap + recommendation:** The largest genuine gap, partly a v1 non-goal
(multi-agent). Smallest DDD-correct decomposition:

1. **Workspace isolation port** — lives in `engine/tool` next to `Workspace`
   (same reason `Workspace` lives there: avoids the `port↔tool` cycle):

   ```go
   // engine/tool/isolation.go
   type WorkspaceForker interface {
       // Fork returns N isolated workspaces derived from base (e.g. git
       // worktrees), plus a release func to tear them down.
       Fork(ctx context.Context, base Workspace, n int) ([]Workspace, func() error, error)
   }
   ```
   Default impl: a `git worktree`-backed adapter (`adapter/worktree`); a memfs
   copy-on-fork adapter for tests. A no-op single-workspace impl preserves
   current behaviour.

2. **Fan-out tool** — a `ForkTool` (mirrors `TaskTool`) that forks the workspace,
   runs the child `*Engine` once per fork concurrently, and returns the joined
   result. Reuses the child-`Engine` injection pattern from pattern 7; **no loop
   change** (it's just another `tool.Tool`). Selection/merge is a `ForkOption`
   (return all summaries vs an injected scorer).

- **Parallelism:** disjoint from the loop, but shares the `tool` package with
  pattern 9's seam — coordinate `engine/tool` edits.
- **Frozen-type risk:** none — `Workspace` is an interface; `WorkspaceForker` is
  additive; no frozen domain struct changes.
- **DoD:** `ForkTool` registered optionally at the composition root; a memfs
  forker drives a test running 3 forks concurrently and joining; the git adapter
  forks/cleans worktrees.

**Judgement:** build the worktree adapter only with a forcing function — git
worktree lifecycle is a non-trivial adapter. A fan-out *tool* without true
isolation (all forks share one read-only workspace) is a cheaper interim step the
existing read-parallel dispatch already half-supports.

---

## 9. Progressive Tool Expansion

**What it is:** Start with <20 default tools; load extra capabilities (MCP,
skills) on demand, surfacing only metadata until invoked, to protect attention.

**State:** Partial. The default kit is small and correct (Read/Edit/Write/Grep/
Glob/WebFetch/Bash/Task). But **all** tools — including MCP tools — are
**eager-registered** into one `Catalog` at startup (`cmd/mecated:buildCatalog`; MCP
via `registerMCP`), and the **full** spec of every available tool is rendered
into the (cache-stable) system prompt every turn (`prompt.toolInventory` plus the
`prompt.toolDisciplineHints` usage-guidance block generated off the same live
catalog — #19, so a Bash-disabled build omits the "reserve Bash" steer),
`buildRequest` → `Catalog.Specs(mode)`). There is no metadata-only tier, no lazy
hydration, no `ToolSearch`/`list-skills` meta-tool, no skill unit.

**Pluggable?** Partial. `tool.Catalog` is the *registration* seam (and the
mode-filter seam for plan mode), which is the right place to host progressive
disclosure. But the catalog exposes one flat `Specs(mode)` view; there is no
notion of "advertised-but-not-hydrated."

**Gap + recommendation:** The cheapest DDD-correct seam keeps the `Catalog` as
owner and adds a *disclosure* distinction, rather than a new top-level port:

```go
// engine/tool/tool.go — extend the contract additively
type DisclosableTool interface {
    Tool
    // Metadata returns the cheap always-present header (name + one line).
    // Spec() remains the full, hydrate-on-demand specification.
    Metadata() ToolMeta
}
```

plus a catalog method `AdvertisedSpecs(mode)` that returns full `Spec`s for the
core kit but only `ToolMeta` for disclosable tools, and a built-in
`ToolSearchTool` (a `tool.Tool`) that, given a query, returns the full `Spec`s of
matching disclosable tools so the model can pull them into context.

- **Package:** `engine/tool` (additive interface; tools that don't implement
  `DisclosableTool` are treated as always-advertised — no breakage).
- **Loop change:** `buildRequest` calls `AdvertisedSpecs` instead of `Specs`.
  **Touches the loop** (small) — serialize.
- **Default impl:** MCP tools (eager-registered, often numerous) implement
  `DisclosableTool` so they advertise metadata only; the core 8 stay full.
- **Tests:** prompt renders metadata for disclosable tools, full spec for core;
  `ToolSearch` returns the full spec of a named MCP tool.
- **DoD:** with 50 MCP tools registered, per-turn tool-inventory tokens are
  bounded by core + metadata, and the model can hydrate a specific MCP tool via
  `ToolSearch`.

**Judgement:** worth doing **only once MCP tool counts grow**. With a handful of
MCP tools the flat catalog is fine and the disclosure machinery is overhead. The
seam is cheap and additive, so it can wait without painting us into a corner.

---

## 10. Command Risk Classification

**What it is:** Two stacked layers — (1) deterministic allow/ask/deny pre-parser,
(2) an auto-mode *model* classifier for the residual cases (scope escalation,
injection, untrusted infra).

**State:** Partial — layer 1 only, but layer 1 is strong. The governance
`Evaluator` (`governance/evaluator.go`) does deny→ask→allow across `Scope`
precedence, plan-mode gating, **compound-Bash splitting** (every sub-command must
pass; substitution/grouping floors at Ask), and glob matching. The default
posture (`cmd/mecated:defaultRules`) allows read-only tools and asks on
Edit/Write/Bash, with unmatched calls defaulting to Ask (safe). Layer 2 (the
model classifier) does not exist.

**Pluggable?** Yes for layer 1 — `port.PermissionPolicy` is the seam the loop
consumes (`authorize`, `dispatch.go:176`); `permpolicy.Policy` is the
anti-corruption adapter translating `session.ToolCall`→governance. A second
policy slots in at the composition root.

**Gap + recommendation:** Add layer 2 as a **decorator over
`port.PermissionPolicy`** — no new port, no loop change:

```go
// internal/adapter/permclassify (new)
// ClassifyingPolicy wraps a base PermissionPolicy: returns the base decision
// unless the base says Allow on a sensitive action, in which case it consults an
// injected Classifier (a model call) and may downgrade to Ask/Deny.
func Wrap(base port.PermissionPolicy, c Classifier) port.PermissionPolicy
```

- **Package:** `internal/adapter/permclassify`, implementing the existing port —
  mirrors the house decorator idiom already used by `llmresilience.Wrap`.
- **The model call** reuses `port.LLMProvider` (the classifier is just another
  LLM consumer), so no new infra port. The two-stage (fast-filter →
  chain-of-thought) detail lives inside the adapter.
- **Default:** unwrapped policy (current behaviour), opt-in via a flag, exactly
  like resilience.
- **Loop change:** none — a decorator over an existing port. **Fully
  parallelizable.**
- **Tests:** a fake classifier downgrades an Allow to Ask on a crafted
  scope-escalation; passes Deny/Ask through untouched.
- **DoD:** with the wrapper enabled and a stub classifier, an otherwise-allowed
  sensitive Bash call is escalated to Ask; deny/ask decisions are unchanged.

**Security note (from the source doc):** treat compaction summaries as untrusted
when classifying — a payload can survive compaction. The classifier reads the
*raw* pending `session.ToolCall`, so this is naturally satisfied.

---

## 11. Single-Purpose Tool Design

**What it is:** Narrow, typed, individually-permissioned tools instead of one
generic shell; Bash as last resort.

**State:** Implemented. Each tool is a typed `tool.Tool` with its own `Spec`
(name, doc, JSON schema) and `ReadOnly` classification (`adapter/tools/*.go`):
Read/Edit/Write/Grep/Glob/WebFetch, plus Bash gated on a configured shell
(`buildCommandRunner`), and Task. Bash is explicitly optional
(`--no-bash`/empty `--shell` → shell-less mode) and last-resort.

**Pluggable?** Yes. `tool.Tool` is the per-tool contract and `tool.Catalog` the
registry seam; a new tool is one struct + one `MustRegister`. Permissions are
per-tool via the rule set keyed on tool name (and per-arg pattern).
`CommandRunner` is a further seam under Bash so command execution itself is
swappable (local/remote/none) without touching the tool.

**Gap + recommendation:** None. The catalog already prevents the "generic shell"
failure mode by making each operation a typed tool with its own permission rule.
The combinatorial-growth risk the doc warns of is mitigated by MCP + (future)
pattern 9 disclosure.

---

## 12. Deterministic Lifecycle Hooks

**What it is:** Named lifecycle events fire at fixed loop points, passing JSON to
user shell commands; exit 2 blocks (PreToolUse only).

**State:** Partial — the seam is complete, the *coverage* is not. The contract is
fully modelled: `governance.HookEvent`/`HookOutcome`/`HookPhase`
(`governance/hookevent.go`) with six phases declared (SessionStart,
UserPromptSubmit, PreToolUse, PostToolUse, Stop, SubagentStop), the
`port.HookRunner` seam, and the `hookexec` shell adapter (exit 0/2 mapping).
**But only three phases actually fire:** `PreToolUse` and `PostToolUse`
(`dispatch.go:233,308`) and `SubagentStop` (`subagent.go:292`). `SessionStart`,
`UserPromptSubmit`, and `Stop` are declared constants that nothing ever fires.
(The article also lists Resume/CwdChanged/Notification/PreCompact, not even
modelled — acceptable as future phases.)

**Pluggable?** Yes — `port.HookRunner` is the seam, injected via `Deps.Hooks`,
with the `hookexec` adapter at the composition root. Adding a firing site does
**not** change the seam; it changes the loop.

**Gap + recommendation:** Fire the three already-modelled phases at their natural
loop points. **No new interface — pure loop wiring against the existing port:**

- `SessionStart` — fire once in `drive` before the first turn (or in
  `recordPrompt` on `Turns==0`), best-effort, non-blocking.
- `UserPromptSubmit` — fire in `recordPrompt` after recording the user message;
  it *may* block (reject a prompt), so honour an exit-2 outcome by terminating
  the run with a clear reason. This is the one with semantics to get right.
- `Stop` — fire in `terminate`/`terminateComplete` before emitting the terminal
  result, best-effort (cannot veto a completed run, mirroring `SubagentStop`).

- **Package:** `engine/agent` (loop). **Touches the loop — serialize** against
  patterns 2 and 9, which also edit the loop.
- **Tests:** a recording `HookRunner` fake asserts SessionStart fires once before
  turn 1; UserPromptSubmit exit-2 aborts the run; Stop fires exactly once on
  every terminal path (complete/stop/cancel/fail).
- **DoD:** all declared phases with a loop site actually fire; the only blocking
  phase besides PreToolUse is UserPromptSubmit, and its block is honored.

Defer Resume/CwdChanged/Notification/PreCompact — they need loop concepts that
don't exist yet (resume-from-store hook point, cwd tracking, a pre-compaction
callback). `PreCompact` is the most defensible near-term add (one site, in
`maybeCompact`) if a consumer asks for it.

---

## Prioritized work list

Sized as work packages. **[PARALLEL]** = disjoint package, no loop edit.
**[LOOP]** = edits `engine/agent` loop — serialize these against each other.
No work package below requires changing a frozen domain *struct*; all are
additive interfaces, decorators, or new firing sites.

### P1 — Fire the missing hook phases (pattern 12) — [LOOP]
- **Interface:** none (reuse `port.HookRunner`, existing `HookPhase` constants).
- **Package:** `engine/agent` (loop.go).
- **Default impl:** existing `hookexec` adapter; default no-op map.
- **Tests:** recording fake asserts SessionStart once, UserPromptSubmit blocks on
  exit 2, Stop fires on all four terminal paths.
- **DoD:** SessionStart/UserPromptSubmit/Stop fire at their sites; only
  UserPromptSubmit (besides PreToolUse) can block.
- **Why first:** highest value/cost ratio — the seam is done, this is wiring; it
  completes a pattern that's "Partial" with declared-but-dead constants.

### P2 — Layer-2 permission classifier (pattern 10) — [PARALLEL]
- **Interface:** `permclassify.Wrap(base port.PermissionPolicy, c Classifier)
  port.PermissionPolicy` (decorator over the existing port; `Classifier` is an
  adapter-local type using `port.LLMProvider`).
- **Package:** `internal/adapter/permclassify`.
- **Default impl:** unwrapped policy (opt-in flag), mirroring `llmresilience.Wrap`.
- **Tests:** fake classifier downgrades Allow→Ask on scope escalation; Deny/Ask
  pass through.
- **DoD:** wrapper escalates an otherwise-allowed sensitive call; off by default.
- **Why:** fully disjoint from the loop; reuses the house decorator idiom.

### P3 — Scoped instruction assembly (pattern 2) — [LOOP]
- **Interface:** `prompt.InstructionAssembler` with a `prompt`-local scope/
  precedence enum (do **not** reuse `governance.Scope`).
- **Package:** `engine/prompt`; loop consumes it in `recordPrompt`.
- **Default impl:** `RootAssembler` reproducing today's single-root behaviour;
  `LayeredAssembler` (parent walk + user scope) as the richer adapter.
- **Tests:** layered adapter orders fragments by precedence over a memfs tree;
  loop records them on turn 0 only.
- **DoD:** loop depends on the interface; default is byte-identical to current.
- **Note:** edits the loop — serialize after P1.

### P4 — Progressive tool disclosure (pattern 9) — [LOOP] (small) + [PARALLEL] (tool pkg)
- **Interface:** `tool.DisclosableTool` (additive over `tool.Tool`) +
  `Catalog.AdvertisedSpecs(mode)` + a built-in `ToolSearchTool`.
- **Package:** `engine/tool` (additive); loop edit in `buildRequest`.
- **Default impl:** MCP tools advertise metadata only; core 8 stay full.
- **Tests:** prompt renders metadata for disclosable tools; ToolSearch hydrates a
  named MCP tool's full spec.
- **DoD:** per-turn tool-inventory tokens bounded by core+metadata with N MCP
  tools registered.
- **Gate:** build only once MCP tool counts justify it. Shares `engine/tool`
  with P5 — coordinate those edits.

### P5 — Fork-join parallelism (pattern 8) — [PARALLEL]
- **Interface:** `tool.WorkspaceForker` (in `engine/tool`, next to `Workspace`)
  + a `ForkTool` (`tool.Tool`, mirrors `TaskTool`, no loop change).
- **Package:** `engine/tool` (port) + `internal/adapter/worktree` (git impl) +
  `engine/agent` (the ForkTool, reusing the child-`Engine` pattern).
- **Default impl:** memfs copy-on-fork for tests; git-worktree adapter for prod;
  optional registration at the composition root.
- **Tests:** memfs forker runs 3 forks concurrently and joins; git adapter
  forks/cleans worktrees.
- **DoD:** `ForkTool` fans out N isolated children and returns a joined result.
- **Gate:** largest effort; build only with a forcing function. Shares
  `engine/tool` with P4.

### Deferred (no seam yet — do not pre-build)
- **Tiered memory (3)** and **dream consolidation (4)** — no consumer; building
  the `MemoryStore`/`Consolidator` interfaces now would be speculative. Note the
  planned shape (a `port.MemoryStore` consulted during context assembly, built
  *with* P3) and revisit when a cross-session-memory requirement is stated.
- **Additional hook phases** (Resume/CwdChanged/Notification/PreCompact) — need
  loop concepts that don't exist; `PreCompact` is the most defensible single add
  (one site in `maybeCompact`) if a consumer asks.

### Already fully pluggable — no work, do not gold-plate
- **1 (persistent instructions)**, **6 (explore-plan-act)**,
  **7 (context-isolated subagents)**, **11 (single-purpose tools)** are complete
  and correctly seamed. **5 (compaction)** and **10-layer-1 (permission)** have
  correct seams that need no structural change — only the *additional adapters*
  in P2/(future cascade), not interface churn.

## Parallelization plan

```
P1 (hooks) ──► P3 (instructions) ──► P4-loop-edit (tool disclosure)
   [LOOP, serialize: all edit engine/agent loop, in this order]

P2 (perm classifier)  ── independent decorator ── no loop edit
P5 (fork-join)        ── engine/tool + adapter/worktree + new ForkTool
P4-tool-pkg           ── engine/tool additive interface

[PARALLEL track]: P2 anytime. P4-tool-pkg and P5 both touch engine/tool —
coordinate (or sequence P4-tool-pkg before P5). The loop edits of P1/P3/P4 must
be serialized; the non-loop work of every package can proceed concurrently.
```


---

*Part of the [design docs](../design/README.md). Related: [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md), [System-Prompt Research & Enhancement (issue #19)](0024-system-prompt-research.md).*
