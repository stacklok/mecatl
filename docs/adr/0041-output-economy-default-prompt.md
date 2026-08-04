# ADR 0041 — Output-economy default prompt

- Status: Superseded
- Date: 2026-06-21
- Scope: the `defaultTone` constant in `engine/prompt/builder.go` (the default system-prompt tone block, part of the cache-stable `StablePrefix`)
- Supersedes: none (extends the prompt-content work landed in [ADR 0024](./0024-system-prompt-research.md))
- Superseded by: [ADR 0086](./0086-remove-output-economy-control.md) — removes the optional `terse` output-economy control surface while keeping the load-bearing `defaultTone` guidance byte-for-byte. The dedicated `output_economy` perf scenario cited below was removed by that ADR.

## Context

Output tokens are billed at 3–5× input tokens across every major provider (GPT-4o 3×,
Claude Sonnet 5×, Gemini Flash 4×), because output generation is N sequential forward
passes while input is one. mecatl already pays input cheaply (the `StablePrefix` is
byte-stable across turns for prompt-caching, gauntlet #6), so the lever with the best
cost/quality ratio is shrinking the model's own prose + code-output footprint without
harming task correctness.

A survey of prior art surfaced three findings that shape this decision:

1. **The biggest lever is "write less code", not "talk tersely".** DietrichGebert/ponytail's
   agentic benchmark on real Claude Code sessions (Haiku 4.5, n=4, 12 feature tasks)
   measured −54% LOC and −22% tokens for a "lazy-code ladder" (does this need to exist?
   → stdlib → native/installed dep → one line → minimum that works), while a
   terse-prose-only control ("caveman") landed −20% LOC but **+7% tokens** — terse output
   with the same deliberation is not cheaper. The win comes from writing less code, which
   also shrinks the next turn's input.
2. **The safety carveout is load-bearing, not boilerplate.** In ponytail's Axis-2 safety
   tasks, an unscoped "prefer one-liners" prompt dropped a path-traversal guard 1/20; the
   scoped "never cut trust-boundary validation / data-loss error handling / security /
   accessibility" variant stayed 20/20. The ~3 lines it kept *were* the path-traversal
   check. An unscoped minimize directive *will* cut a security guard some non-zero
   fraction of the time.
3. **Economy directives must be scoped to prose, not cognition.** Claude Code's system
   "Output efficiency" block ("Go straight to the point. Try the simplest approach first.
   Do not overdo it. Be extra concise.") caused documented quality degradation
   (anthropics/claude-code#32508): the model self-reported interpreting "be concise" as
   "skip investigation," producing compile→fail→fix loops instead of reading first. The
   `IMPORTANT:` prefix also overrode user thoroughness preferences after compaction
   (#39583). Economy wording must explicitly separate brevity from investigation depth,
   carry no `IMPORTANT:` prefix, and defer to operator/project instructions on conflict.

mecatl's `defaultTone` already had the *downward* anti-over-engineering half ("smallest
change that satisfies the request, no premature abstractions"). It lacked the *upward*
ladder (prefer existing/stdlib/dep/one-line over writing new), the prose-economy scope
clause, the safety carveout, and the Edit-over-Write structural nudge.

## Decision

Rewrite `defaultTone` to carry five blocks, all in the cache-stable `StablePrefix`:

1. **Prose economy, scoped to PROSE ONLY.** Kill preamble, postamble, sycophantic
   openers, hollow closings, restating the request, restating tool results in prose.
   Explicitly state this does NOT mean read less / investigate less / check fewer types.
   No `IMPORTANT:` prefix. (The §2.1 / #32508 / #39583 separation principle.)
2. **Silence is acceptable; prefer structured output over prose.** A turn may be only
   tool calls. Do not announce intent ("let me…") — act. Reinforces the existing
   `agencyDelta` anti-announcement contract from the economy side.
3. **The minimum-code ladder.** Stop at the first rung that holds: does this need to
   exist → stdlib → already-imported dep → one line → minimum that works. Plus the
   mecatl-native rung: prefer `Edit` (emit only the change) over `Write` (emit the whole
   file) for partial modifications — cheaper in output and safer against concurrent
   changes. (The ponytail ladder + the Aider edit-format lesson.)
4. **The existing convention-following / anti-over-engineering block** (read-before-edit,
   smallest change, no unrequested features, three similar lines beat the wrong
   abstraction, comment only when non-self-evident, push back when wrong) — carried
   forward unchanged.
5. **The safety carveout.** Never cut these to hit a smaller line count: trust-boundary
   input validation, data-loss-preventing error handling, security, accessibility,
   anything explicitly requested. (The ponytail Axis-2 measured clause.)

No `port.LLMRequest` field is added — economy is a prompt concern, never a per-request
field (the repo's provider-neutrality discipline). The wording is model-neutral; per-model
tuning stays in the composition-layer `agencyDelta`. The soul / user-model / AGENTS.md
override these defaults on conflict (they already ride separate, higher-provenance
messages).

## Consequences

- The default prompt grows by ~400 bytes of *cached input* (cheap) to save a multiplier
  on *output* — the trade the economics favors. Gauntlet #6 (cache stability) is
  preserved: the new wording is in `StablePrefix`, byte-identical across turns.
- `TestStablePrefixByteStableAcrossEnv` and `TestStablePrefixContainsToolsAndNoVolatile`
  continue to pass (verified). A new `TestDefaultToneOutputEconomy` pins the four
  load-bearing clauses so a future silent weakening fails CI.
- Expected magnitude for mecatl's Go-systems workload is *smaller* than ponytail's
  −54% (which concentrated in frontend native-input over-build; Go systems code has less
  "native platform feature" room and more irreducible backend logic where ponytail's win
  was ~0%). The honest claim is: big where there's bloat to cut, near-zero on minimal
  code, never at the cost of a safety guard. The `output_economy` perf scenario
  (`perf/scenarios/output_economy_test.go` <!-- lint:not-a-citation: removed by ADR 0086; historical reference in a frozen ADR -->) was the measurement harness for before/after
  comparison.
- An operator-tier `--output-economy terse` posture knob (an answer-length default for
  explanatory turns) is **shipped**: the `--output-economy` flag (mecated, mecatui,
  mecatequi) + the operator-global `settings.yaml` `output-economy:` key select
  `normal` (default, no-op — the tone already carries the economy contract) or
  `terse` (appends the answer-length clause for explanatory turns). Operator-tier
  only (a project-tier key is WARN-ignored, mirroring posture/guardrails); CLI
  out-ranks YAML. The "terse" answer-length clause is the most over-steer-prone rule,
  so it is opt-in, not the always-on default.

## See also

- [ADR 0024](./0024-system-prompt-research.md) — the prior system-prompt enhancement
  (role/tone/safety rewrite, `toolDisciplineHints`, `agencyDelta`).
- `docs/architecture.md` — the two-layer prompt assembly (living doc).
- `engine/prompt/builder.go` (`defaultTone`) — the constant this decision shapes.
- `perf/scenarios/output_economy_test.go` <!-- lint:not-a-citation: removed by ADR 0086; historical reference in a frozen ADR --> — the measurement harness.
