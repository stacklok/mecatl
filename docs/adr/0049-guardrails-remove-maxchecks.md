# ADR 0049 — Remove the guardrails per-session checker call-count cap

- Status: Accepted
- Date: 2026-06-24
- Scope: the guardrails (`modelhook`) cost model — removal of the per-session checker call-count cap (`maxChecks` / the `checkBudget`). Touches `breaker.go`, `internal/adapter/modelhook/modelhook.go`, `internal/app/guardrails.go`, `internal/app/build.go`, `internal/adapter/permconfig/schema.go`, and the operator-tier `guardrails:` YAML subtree. NO engine (`engine/agent`) change; no `port.LLMRequest`, proto, or wire-contract change.
- Supersedes: the COST-MODEL / per-session call-count cap decision in [ADR 0021](./0021-guardrails.md) ONLY (the `maxChecks` / `checkBudget` call cap and the auto-200 default that applied to the default advisory rule set).
- Superseded by: none

## Context

[ADR 0021](./0021-guardrails.md) shipped guardrails as an opt-in, operator-tier-only content checker whose only cost is one extra LLM call per matched tool boundary. To bound that spend, 0021 added a per-session **call-count cap** — the `checkBudget` (`breaker.go`) — exposed as the operator-tier `guardrails.maxChecks` YAML key and the `Config.GuardrailsMaxChecks` composition field. When the operator enabled guardrails with only a model (taking the default advisory rule set) and did not pin `maxChecks`, the composition (`internal/app/guardrails.go`) auto-applied a default cap of 200 (`defaultGuardrailsMaxChecks`).

The cap was a security footgun for a control the operator explicitly opted into. Configuring a checker model is the **opt-in to spend** (ADR 0021's load-bearing decision): the operator has already decided their tool boundaries should be inspected. Once the cap was hit the runner **silently failed open** — every further matched call in the session skipped the checker and passed byte-unchanged, exactly as if the checker were offline under the default fail-open posture. So a guardrail the operator turned on to catch exfiltration and prompt injection quietly went inert after N calls in a long, tool-heavy session — the precise long-running, high-tool-fan-out workload where an unguarded boundary is most dangerous, and the one the operator most expected to be covered. The cap traded a bounded, operator-controllable cost (LLM spend, already gated by configuring a model) for an unbounded, silent correctness regression in a security control. That is the wrong trade for a security feature.

The auto-200 default compounded this: an operator who set only a model — the documented minimal config — got a cap they never asked for and were not told about at runtime, which could silently disable their guardrails mid-session.

## Decision

**Remove the per-session checker call-count cap (`maxChecks` / `checkBudget`) entirely. The checker runs once per matched tool boundary, unbounded, for the life of the session. Cost control lives in the operator's provider/billing layer, not in the harness.** Concretely:

- The `checkBudget` type and its `admit` method are removed from `breaker.go`. The `failureStreak` (the consecutive-checker-FAILURE escalation to the one-time "checker DOWN" sticky WARN) is KEPT unchanged — it bounds a correctness concern (a persistently-broken checker), not a cost concern.
- `modelhook.Runner` no longer carries a `budget` field; `modelhook.Options` no longer has a `MaxChecks` field; `modelhook.New` no longer constructs a budget.
- `internal/app/guardrails.go` drops `defaultGuardrailsMaxChecks` (the auto-200) and the `maxChecks := cfg.GuardrailsMaxChecks` / default-cap-injection logic; `buildGuardrailsHooks` no longer passes `MaxChecks` to `modelhook.New`. `effectiveGuardrailSpecs`'s `usedDefaults` return is now used ONLY to annotate the build-time posture line ("default set"), never to inject a cap.
- `Config.GuardrailsMaxChecks` (`internal/app/build.go`) and `GuardrailsSection.MaxChecks` / its strict-parse entry (`internal/adapter/permconfig/schema.go`) are removed. The operator-tier `guardrails:` YAML subtree is parsed STRICTLY as before; `maxChecks` is now an UNKNOWN key, so a config carrying it fails loud (strict parse) rather than silently ignoring it — an operator who copied an old config is told, not surprised.
- The build-time `guardrails ACTIVE` posture line (`logGuardrailsPosture`) no longer appends a `maxChecks=<n>` suffix.

The later contextual reviewer removes content-length skips entirely; see [ADR 0342](./0342-contextual-investigative-guardrails.md).

## Consequences

**Easier / better:**

- A guardrail the operator turned on stays on for the whole session. The silent fail-open-after-N-calls footgun is gone — the security control no longer self-disables mid-session on the workload where it matters most. This aligns guardrails with the feature's own thesis: configuring a model is the opt-in to spend, and the operator's provider/billing layer is the right place to bound that spend.
- One fewer operator-config knob and one fewer composition const (`defaultGuardrailsMaxChecks`), and the `usedDefaults` return is no longer load-bearing for a cost cap.
- The `maxChecks` YAML key is rejected loudly by the strict parser, so a stale config cannot silently re-introduce a cap the harness no longer honours.

**Behaviour change (the honest cost):** checker calls are now unbounded per session — deliberately. A long, tool-heavy session with the default advisory rule set (which matches `mcp__*` on both directions) will make one checker call per matched tool boundary, which is exactly the coverage the operator asked for when they configured a checker model. The auto-200 default is gone; an operator relying on it to cap spend must now bound spend at the provider/billing layer (where cost control belongs). The failure mode is NOT unbounded: a sustained checker outage still escalates to the one-time sticky "checker DOWN" WARN (the `failureStreak`), and each checker call is bounded by the 30s-ish per-attempt timeout + the fail-open/closed policy, so a broken checker degrades loudly rather than running away.

**Unchanged at the time:** the engine-layer routing, verdict parse, recursion guard, fail-open/closed posture, operator-tier-only enforcement, and kill switch. Later contextual-review changes are recorded by [ADR 0342](./0342-contextual-investigative-guardrails.md).

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature whose cost-model / call-cap clause this supersedes (everything else carries over).
- [ADR 0046](./0046-guardrails-slot-enable.md) — the guardrails enable-model ADR (configure = enable); this ADR does not touch enable, only cost bounding.
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
- Living docs: [`docs/usage.md`](../usage.md) (the guardrails section + the `--guardrails-model` / `--guardrails` flag rows), [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) (guardrails cost model).
