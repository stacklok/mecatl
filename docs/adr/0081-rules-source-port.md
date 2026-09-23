# ADR 0081 — `RulesSource` port for `.claude/rules` discovery

- Status: Accepted
- Date: 2026-08-02
- Scope: `engine/prompt` (the `RulesSource` port + `Rule` value object + `RulesAssembler`), `engine/adapter/rulesfs` (the FS adapter), the `internal/app` composition wiring, and the conventional discovery contract.

## Context

AGENTS.md/CLAUDE.md are the project-instruction surface mecatl already assembles into the
turn-0 prefix. Claude Code and the broader ecosystem add a second, finer-grained project
surface: `.claude/rules/<name>.md` — per-file markdown rules, optionally scoped by a `paths:`
frontmatter glob. mecatl had no discovery for them, so a rule an operator placed was
silently dropped: the model never saw it, and no diagnostic recorded the loss. A dropped
rule is invisible to events (it never enters the conversation), so a regression in
discovery is also invisible — the worst class of failure for an injection surface.

Four design forces shaped the decision:

1. **The port-home question.** The four sibling turn-0-context ports (`SoulSource`,
   `MemoryIndexSource`, `UserModelSource`, `CommandSource`) all live in `engine/prompt`,
   next to the assembler/expander that consumes them, satisfied *structurally* by adapters
   bound at the composition root. `SkillSource`/`AgentDefSource` live in `engine/tool`
   because skills become catalog-registered `Tool`s and agent defs build child engines —
   both consumed from `engine/agent` via the catalog, which cannot import `engine/prompt`.
   Rules never become tools; they are pure turn-0 context, exactly the shape of the four
   prompt-local ports.

2. **Eager vs. lazy activation.** A `paths:` glob invites lazy, path-triggered activation
   (inject a rule only when the turn touches a matching file). Lazy is cheaper per turn
   but breaks the byte-stable prompt-cache prefix (the prefix would vary by touched file),
   and it requires a file-match pass before the run that prompt assembly has no seam for.
   Eager (all rules at turn 0) keeps the prefix stable and the assembler simple, at the
   cost of context proportional to total rule bytes.

3. **Trust-class parity.** A rule body steers the model the same way AGENTS.md/CLAUDE.md
   do. The project tier (`<workspace>/.mecatl/rules`, `<workspace>/.claude/rules`) must
   therefore carry the SAME trust gate the other project-tier surfaces carry: a cloned
   repo's project rules cannot steer the model before the operator trusts the workspace.

4. **The operator's flag decision.** The issue and the original plan proposed a
   `--rules-dir` / `--rules-conventional` flag pair (strict opt-in, like skills). The
   operator overrode that: AGENTS.md/CLAUDE.md themselves are discovered always-on (inert
   when no file exists), and rules are the same trust class. The operator's call is that
   the trust gate — not a separate opt-in flag — is the real boundary: the conventional
   lanes are inert without directories, and `TrustProject` is the one gate that matters.

## Decision

1. **`RulesSource` port in `engine/prompt`.** The port + the `Rule` value object + the
   `RulesAssembler` live in `engine/prompt`, the consumer-local precedent set by
   `SoulSource`/`MemoryIndexSource`/`UserModelSource`/`CommandSource`. The FS adapter
   (`engine/adapter/rulesfs`) satisfies the port *structurally* — no adapter ever imports
   `engine/prompt` to satisfy it. Putting the port in `engine/tool` would force `prompt`
   to reach into `tool` for a type it is the sole consumer of, backwards on the dependency
   arrow (prompt already imports tool, but the concept belongs with the assembler).

2. **Eager v1, with `Paths` on the port from day one.** All discovered rules are injected
   at turn 0 as one user-role message, each rule fenced in a `<rule name="…">` block with
   an `Applies when:` condition rendered from `Paths`. The `paths:` glob is stated to the
   model as a condition the model applies itself ("when working in matching files, follow
   the rule; otherwise it does not apply") — eager v1. The `Paths` field rides the port
   from day one so a future lazy, path-triggered activation is additive (a new source
   variant that filters by path), not a breaking change to the port or the value object.

3. **Byte + count caps, fail-soft.** A per-rule body cap (`MaxRuleBytes`, 20 KiB — the
   soul-body precedent), a combined cap (40 KiB), and a count cap (32) bound the turn-0
   prefix tax. Over-cap rules are truncated/dropped with a footer and a composition-time
   WARN (the agentdefs "dropped" vs. "adjusted" word discipline); the assembler itself
   renders silently (domain never logs). A nil source, a source error, or an empty set
   yields no message and no error — fail-soft, the soul/memory discipline: a rule fault
   must never abort a run.

4. **Project tier trust-gated.** `ResolveOptions.IncludeProjectTier` is set from the folded
   `cfg.TrustProject` at composition; an untrusted workspace's project tier is withheld
   with a WARN mirroring the agentdefs/skills gate, and the user-tier lanes stay active
   regardless. Resolved ONCE at `Build` and threaded into the shared engine AND the
   per-session factory — never re-resolved (the issue-#42 drift class; the same
   `instructions` assembler flows both paths).

5. **NO FLAGS — conventional discovery always-on, like AGENTS.md.** The conventional lanes
   (`<workspace>/.mecatl/rules`, `<workspace>/.claude/rules`, `$XDG_CONFIG_HOME/mecatl/rules`,
   `~/.claude/rules`) are ALWAYS discovered in production (`Conventional: true` in
   `ResolveOptions`), inert when no directory exists — the same posture as AGENTS.md/
   CLAUDE.md themselves. There is no `--rules-dir`/`--rules-conventional` flag and no
   `Config.RulesDirs`/`Config.RulesConventional` field. The trust gate
   (`--trust-project`/`trustedWorkspaces`) is the ONE boundary: it gates the project tier,
   and the conventional user lanes are inert without dirs. This overrides the issue's
   flag-pair suggestion — the operator's call, grounded in the parity argument: the
   conventional lanes are inert without directories, and the real boundary is the trust
   gate, not a discovery opt-in.

## Consequences

Eager injection costs context proportional to total rule bytes; the combined cap bounds
it (40 KiB worst case across all rules, the 2× soul cap reflecting that rules can be
multiple). Lazy path-triggered activation is deferred — file a separate ADR if a profile
that touches many path-scoped rules makes the prefix tax material; the `Paths` field is
already forward-compatible. A `.claude/rules` dir in an untrusted repo does not steer the
model until `--trust-project` admits the project tier; the user-tier lanes are never gated
(an operator's own `~/.claude/rules` always applies).

The cap is a one-time prompt-cache-prefix invalidation when changed: the truncation point
moves, so a run after the change has a different prefix. Treat changing `MaxRuleBytes` or
the combined cap as a deliberate prefix invalidation, not a silent knob.

`IsInjectedTurn0Fragment` recognises the rules header (the single source of truth in
`turn0.go`), so the compaction first-user pin and the resume re-injection guard treat the
rules fragment as an injected turn-0 fragment — it is never mistaken for a genuine user
instruction and is never persisted into the conversation (ADR 0043 ephemeral fragments).

## See also

- [ADR 0043](./0043-ephemeral-turn0-instruction-fragments.md) — turn-0 fragments are ephemeral;
  rules ride the same path.
- [ADR 0011](./0011-soul-and-user-model.md) — the fail-soft byte-cap precedent (soul).
- The #328 agentfs/skillfs graduation — the consumer-local-port + structural-adapter +
  per-package carry + root-alias-shim pattern this follows.
- `docs/architecture.md` (prompt package), [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
  (pattern 2 — scoped context assembly).
