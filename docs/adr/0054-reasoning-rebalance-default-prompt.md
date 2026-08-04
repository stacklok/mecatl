# ADR 0054 — Reasoning rebalance of the default-tone prompt

- Status: Accepted
- Date: 2026-06-25
- Scope: the `defaultTone` constant in `engine/prompt/builder.go`
- Amends: [ADR 0041](./0041-output-economy-default-prompt.md) — the minimum-code ladder, the safety carveout, and the anti-over-engineering decisions of 0041 STAND; this rebalances only the prose-economy half of that tone.

## Context

[ADR 0041](./0041-output-economy-default-prompt.md) (commit 636e8683, PR #136)
reworked the default tone toward output economy: it added several brevity
directives — a prose-economy block, an announcement-ban ("Do not announce upcoming
actions … either emit the tool calls or say nothing and act"), and a
"manufacture no narration" line — and zero thoroughness directives. The brevity
guidance was correct for the *delivered answer* but it leaked into the *cognition*.

For interleaved-reasoning models — OpenRouter passthroughs, deepseek, GLM in its
non-thinking mode — the reasoning happens in the *visible output channel*, not a
separate hidden thinking stream. For those models a blanket "say nothing and act"
and "do not manufacture narration" is read as "do not reason out loud", which
suppresses the very chain-of-thought the model needs. The symptom is shallower
work: less reading before editing, fewer edge cases considered, errors patched
without being understood.

The Claude Code project has hit the same failure mode and documented it:

- [anthropics/claude-code#32508](https://github.com/anthropics/claude-code/issues/32508)
  — the model self-reported confusing "don't talk much" with "don't think much".
- [anthropics/claude-code#39583](https://github.com/anthropics/claude-code/issues/39583)
  — an `IMPORTANT:` thoroughness directive had to be re-asserted after compaction
  because brevity guidance kept biasing the model away from it.
- [anthropics/claude-code#42796](https://github.com/anthropics/claude-code/issues/42796)
  — a measured Read:Edit ratio collapse from 6.6 to 2.0 and edits-without-a-prior-read
  rising from 6.2% to 33.7% under brevity-heavy prompting.

Anthropic's published guidance is that adaptive thinking is *promptable*: a
brevity-heavy prompt biases the thinking budget down, and the literal token "think"
can over-suppress reasoning when extended thinking is turned off. The remedy is to
(a) scope brevity explicitly to the delivered message, (b) name reasoning as a
protected channel, and (c) phrase thoroughness with "reason through" / "work
through" / "understand" rather than the word "think".

## Decision

Rewrite the `defaultTone` constant's prose-economy half (its first two paragraphs),
keeping the rest of ADR 0041 verbatim:

1. **Scope brevity structurally to the final message.** The opening clause now reads
   "Be concise and direct in your final answer to the client — the message you write
   for the reader, not the work that produces it", and every brevity instruction is
   nested under "In that final message". Brevity no longer reads as a property of the
   whole turn.

2. **Name reasoning as a protected channel and give an explicit exemption list.**
   "Brevity applies to what you write for the reader — never to how carefully you
   reason or work. It does NOT mean: read less, investigate less, check fewer types,
   or skip understanding an error before fixing it."

3. **Add explicit thoroughness directives.** "Reason through the problem before you
   change anything: understand the cause before fixing it, work through edge cases
   and failure modes, and resolve an unexpected result before moving past it." The
   word "think" and its variants are deliberately avoided.

4. **Remove the announcement-ban** ("Do not announce upcoming actions … say nothing
   and act"). Same-turn action discipline is already owned by the per-model
   `agencyDelta` in `internal/app/build.go` (`agencyDelta`); duplicating it in the
   neutral default tone is what suppressed the visible-channel reasoning. The
   "manufacture no filler narration to pad a turn" line is kept (it targets filler,
   not reasoning), and "a turn may be only tool calls with no prose" still permits a
   genuinely prose-free turn.

5. **Keep the ADR 0041 wins verbatim.** The minimum-code ladder, the
   Edit-over-Write nudge, the convention-following / anti-over-engineering block, and
   the safety carveout (input validation at trust boundaries, error handling,
   reversibility/commit discipline) are byte-for-byte unchanged.

## Consequences

- **Cache stability is preserved.** The constant stays in the cache-stable
  `StablePrefix` and references nothing volatile (no date, cwd, model, shell, git, or
  mode), so the gauntlet #6 byte-stability invariant
  (`TestStablePrefixByteStableAcrossEnv`,
  `TestStablePrefixContainsToolsAndNoVolatile`) still holds.
- **The pinning test moves with the wording.** `TestDefaultToneOutputEconomy` is
  re-pinned to the new load-bearing clauses (final-message scope, reasoning
  protection, the exemption list, and the two thoroughness directives) plus the
  unchanged ladder / safety / Edit-over-Write clauses.
- **Composition is unaffected.** The `outputEconomyToneDelta` "terse" posture still
  composes onto `DefaultTone()`; the `engine/api` surface is unchanged (the
  `DefaultTone` signature is identical), so no `task api:update` is needed.
- **Cost trade.** The rebalanced block is slightly longer, a small one-time increase
  in cached input tokens, traded for restored reasoning depth on
  interleaved-reasoning models. This is the deliberate cost of the fix.

## See also

- [ADR 0041](./0041-output-economy-default-prompt.md) — the output-economy tone this amends.
- [ADR 0024](./0024-system-prompt-research.md) — the system-prompt research this builds on.
- `engine/prompt/builder.go` — the `defaultTone` constant.
- `internal/app/build.go` — `agencyDelta`, the per-model task-persistence/same-turn-action contract.
- `perf/scenarios/output_economy_test.go` <!-- lint:not-a-citation: removed by ADR 0086; historical reference in a frozen ADR --> — the offline output-economy scenario benchmark.
