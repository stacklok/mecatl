# ADR 0050 — Remove the guardrails oversized-content inspection skip

- Status: Accepted
- Date: 2026-06-24
- Scope: the guardrails (`modelhook`) content-inspection path — removal of the `maxContentBytes` (256 KiB) skip-inspection behavior. Touches `internal/adapter/modelhook/modelhook.go` and its tests ONLY. NO config, schema, composition, CLI, proto, or wire-contract change (the bound was a package const, never an operator-config knob).
- Supersedes: the COST-MODEL / `maxContentBytes` "induced-fail-open" clause in [ADR 0021](./0021-guardrails.md) ONLY (the built-in 256 KiB content bound and the `onContentTooLarge` skip-then-route path).
- Superseded by: none

## Context

[ADR 0021](./0021-guardrails.md) added a built-in `maxContentBytes = 256 * 1024` bound on the tool content the checker inspects (`internal/adapter/modelhook/modelhook.go`). Content over the bound was NOT silently passed: in an enforcing mode it routed through the same fail-open/closed policy as a checker error (the "induced-fail-open defense"), and in advisory mode it WARNed-but-passed. The stated rationale was that an attacker could emit a huge tool result to time out the checker and slip through unchecked, so the runner bounded the input BEFORE the checker call.

The bound is a gap on both sides:

- **Attacker side:** a "too big to check" cutoff is itself the gap an attacker exploits — pad a payload past 256 KiB and the checker is skipped (routed to fail-open/closed, but never INSPECTED). The induced-fail-open defense only routes the failure; it does not inspect the content, so a genuine injection padded past the bound is never judged by the checker.
- **Benign side:** a legitimate large tool result (a big file Read, a verbose MCP response) trips the bound on entirely benign content, degrading the checker to a WARN/block on content that was safe.

The failure mode the bound was guarding against — a checker call that times out or errors on huge input — is ALREADY bounded by the 30s per-check timeout (`engine/agent/guardrailcheck.go` (`guardrailCheckTimeout`)) and the existing fail-open/closed policy (`onCheckerError`): a checker error/timeout on huge input WARNs (fail-open) or blocks (fail-closed), the same outcome the bound induced, but reached honestly through the real checker path rather than by skipping inspection. So the bound adds a skip gap without adding a bound the timeout does not already provide.

This is the same class of decision as [ADR 0049](./0049-guardrails-remove-maxchecks.md): a cost/abuse guard whose skip behavior is a security downgrade for a control the operator explicitly opted into, superseded by trusting the operator's provider/billing layer for cost control and the existing timeout + fail-open/closed posture for the failure mode.

## Decision

**Remove the `maxContentBytes` const and the `onContentTooLarge` method. The checker inspects content regardless of size; a checker error or timeout on huge input flows through the existing `onCheckerError` fail-open/closed path.** Concretely:

- The `maxContentBytes = 256 * 1024` const and its doc comment are removed from `internal/adapter/modelhook/modelhook.go`.
- The oversized-content early-return block in `check()` is removed; matched content reaches the checker and errors/timeouts follow the configured posture.
- The `onContentTooLarge` method is removed entirely. Its advisory-WARN / enforcing-route-to-`onCheckerError` behavior is no longer reachable, because oversized content now reaches the real checker; the enforcing failure mode is reached honestly via a checker timeout/error instead.
- The `fmt` import is KEPT (`buildCheckPrompt` still uses `fmt.Fprintf`).
- `maxSanitizedBytes` was a distinct checker-output defense at the time. The later contextual reviewer removes sanitize and the remaining input-length skip; see [ADR 0363](./0363-contextual-investigative-guardrails.md).

**Option A — deterministic compaction of huge content before inspection** (truncate/summarize to a bounded size, then inspect the compacted form) is deferred as a future enhancement. It preserves inspection of the whole payload in spirit but adds a compaction step whose fidelity (does the compacted form still carry the injection?) is its own design question; it is not needed to close the current skip gap, which the removal alone closes.

## Consequences

**Easier / better:**

- The "too big to check" skip gap is gone. A matched tool result is not skipped based on size.
- One fewer package const and one fewer method (`onContentTooLarge`); the control flow is simpler.
- The fail-open/closed posture on a checker failure is now reached honestly through the real checker path on huge input, not by a size-based pre-skip — the same outcome the bound induced, without the inspection gap.

**Behaviour change (the honest cost):** a huge tool result now drives one checker call bounded by the 30s `guardrailCheckTimeout` (`engine/agent/guardrailcheck.go` (`guardrailCheckTimeout`)) rather than being pre-skipped. On huge input the checker will typically time out, which routes through `onCheckerError`: a fail-closed rule blocks (the safe outcome for a rule that opted into fail-closed), a fail-open rule WARNs-but-passes (degraded to "no checker", as before). This is one checker call per huge result rather than zero — cost control lives in the operator's provider/billing layer, the same place [ADR 0049](./0049-guardrails-remove-maxchecks.md) placed per-session call spend. The failure mode is bounded by the per-check timeout, not unbounded.

**Known amplification under concurrent read-batch fan-out:** the agent loop fans out N concurrent read-only tool calls per turn (`read-parallel`), each driving its own checker call. With the size bound removed, a turn with N large results drives N concurrent 30s checker calls. The `failureStreak` breaker does not cap this (it escalates to a one-time WARN after 3 consecutive failures but does not stop calling the checker). This is an accepted trade-off: the inspection guarantee (no evasion by padding) outweighs the amplification, which is bounded per-call by the 30s timeout and bounded overall by the operator's provider/billing layer. A future enhancement (Option A — truncate-with-marker into the checker prompt, or serializing guardrail checks) could bound the fan-out multiplier without reintroducing the skip gap. Separately, MCP tool results are not subject to the harness's `MaxOutputBytes` truncation that every built-in tool applies — a pre-existing ingress gap worth a separate follow-up.

**Unchanged at the time:** verdict parsing, recursion prevention, checker-down posture, operator-tier-only enforcement, and the kill switch. Later contextual-review changes are recorded by [ADR 0363](./0363-contextual-investigative-guardrails.md).

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature whose cost-model / `maxContentBytes` clause this supersedes (everything else carries over).
- [ADR 0049](./0049-guardrails-remove-maxchecks.md) — the precedent (#168) that removed the per-session call-count cap on the same thesis (a cost guard whose skip is a security downgrade; cost control lives at the provider/billing layer).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
- Living docs: [`docs/usage.md`](../usage.md) (the guardrails failure-mode note), [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) (guardrails cost/abuse model).
