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

