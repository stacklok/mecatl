## 7. Skills, soul, user model

### Legacy self-improving-skill loop (`SkillDraft` + `mecated skills promote`)

> **Deprecated compatibility workflow:** `mecated skills promote` operates only on
> the old `origin:model` quarantine tree. It does not inspect or activate records in
> the evaluated skill lifecycle repository. Lifecycle integrations must explicitly
> import a legacy candidate as an unevidenced Draft. The standard evaluator abstains
> for unevidenced drafts; any later review/evaluation/stage/activation is an explicit host or
> operator action. There is no command or startup sweep that makes an unevaluated lifecycle
> skill live. Operator/manual skills already supplied through `--skills-dir` are unchanged.

`--skills-draft-dir <quarantine>` enables a **writable** `SkillDraft` tool so the
agent can author a reusable skill from a procedure it just performed. This legacy
quarantine tool is bounded by a hard trust boundary:

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

### Authoring bundled assets

Treat bundled files as **logical textual references**, not paths. In `SKILL.md`,
tell the model to call the `Skill` tool again with the skill name and exact logical
asset name, for example: “Call `Skill` with `{name: "deploy", asset:
"references/api.md"}` before choosing an endpoint.” Activation lists the available
logical names and sizes; the follow-up call returns one bounded UTF-8, NUL-free asset.
Do not tell the model to use `Read` on a skill directory or assume a base directory
will be advertised.

Bundled execution is not implicitly available. A `scripts/run.sh` asset is only a
logical payload; the harness does not materialize it, apply its executable bit, or
make it available to Bash. If a workflow genuinely requires a script or data file on
disk, author an explicit, permission-governed step that creates or obtains it inside
the session workspace, then invoke it normally. Prefer keeping reference material
textual and consuming it directly through `Skill`.

### Skills as slash commands (`/<skill-name>`)

Each discovered skill is also invocable as a **slash command** — a Claude-Code
skill-as-command semantics: `/<skill-name>` expands to the skill's **body**
directly in context (the body IS the command template; the model then has the
instructions). No new tool, no new dispatch concept — it reuses the existing
slash-command layer, so `$ARGUMENTS`/`$1`/`$2` placeholders substitute exactly
like a file-backed command.

This means the two ways to load a skill's instructions are equivalent:
- call the **`Skill`** tool with the skill's `name` (the progressive-disclosure
  path, which also surfaces the logical bundled-asset inventory), or
- type **`/<skill-name> <args>`** at the prompt (the inline path, which injects
  the body plus the same post-expansion logical inventory).

Neither path retrieves asset content automatically. When an asset is needed, the
model calls `Skill` with `{name, asset}`.

**Precedence** (first-that-expands-wins): a local command file (`<commands-dir>/<name>.md`)
**shadows** a same-named skill; a skill **shadows** a same-named slash-command
driver source; both shadow MCP prompts. So a repo's own `<name>.md` command
file wins over a skill of the same name, and a skill wins over a driver command.

The **project-tier trust gate is inherited by construction**: an untrusted
workspace's project-tier skills (under `<workspace>/.mecatl/skills`,
`<workspace>/.claude/skills`) never enter the seam, so they are **not** invocable
as `/<skill-name>` until you `--trust-project`. Operator-explicit `--skills-dir`
paths and your user-tier skills (`~/.claude/skills`) are never gated — they are
always invocable. With no skills discovered, the bridge is a no-op (an unknown
`/<name>` passes through unchanged, never a blank substitution).

### Persona / soul (`~/.config/mecatl/soul.md`)

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
`docs/adr/0011-soul-and-user-model.md` for the threat model and the learning loop.)

**Drift detection.** The harness fingerprints the soul's content
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

**Project-sourced soul + trust gate.** Besides the
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
unified with `mecated`, so a project `.mecatl/soul.md` is
honoured only when you pass `--trust-project` to `mecatui`.)

### Project rules (`.claude/rules`)

A **project/user rule** fragment — markdown rule files (`<name>.md`) discovered from
the conventional locations and injected as a **turn-0 user message** (after the
cache-stable system prefix, between the root instructions and the soul — project
context before persona). Each rule is fenced in a `<rule name="…">…</rule>` block
with an `Applies when:` condition, and the model is told to apply a rule's `paths:`
glob itself ("when working in matching files, follow the rule; otherwise it does
not apply"). This is the pattern-2 instance of scoped context assembly; see
[ADR 0081](../adr/0081-rules-source-port.md).

Discovery is **always-on**, like AGENTS.md/CLAUDE.md themselves — no flag, and inert
when no directory exists. The conventional lanes, in descending precedence, are:

- `<workspace>/.mecatl/rules` and `<workspace>/.claude/rules` — the **project** tier
  (a project rule overrides a personal one of the same name);
- `$XDG_CONFIG_HOME/mecatl/rules` (fallback `~/.config/mecatl/rules`) and
  `~/.claude/rules` — the **user** tier.

The **project tier is trust-gated**: a rule under `<workspace>/.claude/rules` in an
**untrusted** workspace is withheld (with a WARN) until you trust the repo
(`--trust-project` or `trustedWorkspaces`). The user-tier lanes are never gated —
your own `~/.claude/rules` always applies.

The whole load is **fail-soft**: a missing dir, an unreadable file, malformed
frontmatter, or a discovery fault degrades to no fragment, never an error that
aborts a run. A single rule body is capped at 20 KiB; the combined rule fragment is
capped at 40 KiB across 32 rules, and rules beyond the cap are dropped with a
footer and a WARN.

### User model (`~/.config/mecatl/usermodel`)

A **user-scoped, cross-project** model of durable **FACTS about the operator** — who
they are and how they like to work. Unlike the soul (read-only) and per-project memory
(`--memory-dir`), the user model is **writable by the agent** and **shared across every
project**, backed by a SECOND `memory` store at `$XDG_CONFIG_HOME/mecatl/usermodel`
(fallback `~/.config/mecatl/usermodel`), overridable with `--user-model-dir`.

It surfaces three ways:

- **Live operator profile (on by default):** full active user facts are reloaded for
  every provider request into a bounded, JSON-structured block in the volatile system
  suffix. It does not enter history and does not change the cache-stable prefix. When
  bounding omits facts, the block names `SearchUserModel` and `RecallUser` only if both
  tools are in that request's actual catalog; child/internal catalogs that lack either
  receive generic unavailable-in-this-context guidance instead. The
  old public `UserModelAssembler` remains available to engine embedders, but standard
  composition no longer injects a `<user-model>` turn-0 message.
- **Tools (on by default):** `RememberUser`, `RecallUser`, `SearchUserModel`, plus
  `InspectUserMemory`, `ForgetUserMemory`, and `UndoUserMemory` when the store supports
  lifecycle history. Keys are auto-namespaced under `user/`. Project memory gets the
  corresponding Inspect/Forget/Undo tools. A remote driver that positively advertises
  lifecycle support is held to that protocol: a missing/failing lifecycle RPC is an
  operation error and never falls back to an unconditional legacy write or read. Remember,
  Recall, Search, Inspect, and Undo
  are floor Allows; Forget is a floor Ask. Any configured Ask/Deny/Allow at a higher
  scope overrides these built-in floors.
- **Staged reflection (`learning.mode`):** `off` is the default and attaches no
  automatic completion observer; explicit reflection remains available through its lazy path.
  `review` and `auto` share the configurable threshold admission policy: balanced is the
  default (conservative/balanced/eager thresholds 6/4/3), and process-local cooldown/count/token
  limits are configured under `learning.automatic`. Genuine current principal remember/learn
  requests are hard but still budgeted; historical/tool/web/MCP/assistant/repository text cannot
  hard-trigger. Restart resets those process-local budgets and no shutdown catch-up runs.
  `review` reflects admitted main-session completions and stages bounded, evidence-backed proposals
  without writing memory. `auto` uses the same stage-first path and then promotes only
  conservative standard-policy-eligible, non-conflicting facts. Evidence-backed procedures use
  the versioned learned-skill lifecycle: `review` evaluates and stages PASS/ABSTAIN (FAIL rejects).
  Auto additionally uses `learning.skills.activation`, for example:

  ```yaml
  learning:
    mode: auto
    skills:
      activation: validated # validated | evaluated
  ```

  Learning remains globally Off by default. In the standard app, explicitly selecting Auto with
  `activation` omitted resolves to `validated`; set `evaluated` to retain the former PASS-only
  assurance. The importable engine pipeline's zero value remains `evaluated`. A trusted project may
  tighten validated to evaluated, never loosen it; an untrusted project setting is ignored. PASS
  uses the ordinary evaluated activation under either policy. Under validated, a missing evaluator
  or ABSTAIN may publish only a non-legacy, accepted/exact, evidence-backed version. Evaluated
  ABSTAIN, similar candidates, external collisions, alternate/untrusted projects, missing publisher,
  and a repository without validated activation stay staged. FAIL rejects; evaluator infrastructure
  failure records a generic durable ERROR/rejected record, warns without raw detail, and does not
  publish. A retry cannot reinterpret that marker as an activatable ABSTAIN.
  Activation, archive, and rollback
  return committed state plus publication status; temporary publication failure revokes the live learned entry
  and startup or the next `/skills` refresh reconciles it. Rollback targets must be versions durably
  proven previously active by evaluated, validated, or rollback transition. `SkillDraft` derives the verified caller, exact workspace, and main-agent owner and refuses
  identity-free calls. It creates body-only inactive content: learned assets/scripts are unsupported.
  `off` never materializes procedures automatically; explicit `SkillDraft` or legacy import creates
  only an inactive validated draft. Project proposals are eligible only when the session root is the
  exact trusted configured root. The gRPC/HTTP learned-skill API lists and inspects bounded bodies,
  diffs, evidence/evaluations, and receipts and applies activate/reject/archive/rollback with an
  expected revision. External/operator skills retain precedence and cannot be lifecycle-mutated.
  Proposal detail
  re-checks source ownership and evidence digests and exposes a bounded, redacted canonical
  preview before approval; changed, unavailable, and cross-owner evidence is not previewed or
  promotable. `--user-model-review` remains as a deprecated `auto` alias and
  `--user-model-review-interval` is now only a deprecated post-threshold weighted downsampler
  (`0`/`1` inert; hard triggers bypass). Reflection never reopens
  or re-runs the user's session.
- **Scheduled consolidation:** `--user-model-consolidate-interval > 0` independently
  authorizes a process-wide consolidator over the cross-project `user/` namespace. It only retires
  byte-identical active duplicates through lifecycle CAS; base-only and non-identical proposals are
  skipped. It runs when the user-model store and provider are available regardless of effective
  workspace `learning.mode`; a project `off` ceiling cannot suppress this operator schedule. The
  schedule is off by default.
- **Manual consolidation:** mecatui `/dream` is a separate immediate maintenance flow. Choose project
  memory or the user model, acknowledge one planner call/token spend, review exact-duplicate and
  synthesized-replacement operations, then apply or dismiss the whole process-local plan. Approved
  synthesis atomically rewrites its displayed survivor and tombstones its displayed sources per
  operation; independent operations can produce a partial receipt. Plans are process-local. Restart,
  expiry, or wrong-replica routing makes the old decision non-retryable and offers explicit fresh
  generation. A same decision still applying, or an indeterminate transport outcome, preserves the exact
  plan ID and decision for same-decision receipt retrieval. An opposite decision is never offered; an
  opposite applying decision enables no fresh generation, while a known terminal conflict permits an
  explicit fresh plan. The feature is unavailable under
  ownership enforcement or without a planner and both reviewed atomic target capabilities. It has
  no per-source toggles, durable plans, grouped undo, provider/model display, or recall counters.

**Rules vs facts — the operator boundary.** The user model holds **FACTS about the
operator** (stated preferences, communication style, domain background), **never rules
or behavioural instructions for the agent**. How the agent behaves comes from its soul
and the system rules. The live profile is fenced and JSON-encoded as DATA and explicitly
cannot change permissions, safety, tools, or policy; a current user instruction wins a
conflict. New lifecycle writes enforce a strict lowercase namespaced key grammar and
reject high-confidence secret shapes. Recall, Inspect, remote, imported, and migrated
data pass through the same structural/UTF-8/secret final-boundary projection, without
blanket prompt-injection keyword suppression of useful prose. The user model is data,
not a governance scope — it can never loosen a configured permission Ask.
Over-eager memory is *steered* (by the descriptions), not *enforced* (there is no
rule/fact classifier); this is a deliberate, accepted residual risk. Single-operator
assumption: there is no per-user keying — "the operator" is implicitly singular, the
same trust-zone assumption the soul and `docs/adr/0009-tiered-memory.md` carry. Disable
it with `--no-user-model`.

---

See also: [workspace trust & posture](workspace-trust.md), [example skills](../examples/README.md),
or the [operator guide index](../usage.md).

