# ADR 0052 — Global guardrails checker-down posture toggle

- Status: Accepted
- Date: 2026-06-24
- Scope: the guardrails checker-error path — `internal/app/guardrails.go` (`GuardrailReviewPolicy`), `internal/adapter/modelhook/matcher.go` (`CompiledRule`), `internal/adapter/permconfig/schema.go` (`GuardrailsSection.OnCheckerDown`, `GuardrailRuleSpec.FailClosedPresent`), `internal/app/guardrails.go` + `internal/app/build.go` (Config wiring). Adds a new operator-tier YAML key `guardrails.onCheckerDown`; no engine/`port`/proto/wire change.
- Supersedes: none (additive — extends the fail-open/closed decision in [ADR 0021](./0021-guardrails.md) without replacing it)
- Superseded by: none

## Context

[ADR 0021](./0021-guardrails.md) specified that on a checker error/timeout, the default posture is **fail-open** (WARN + degrade to "no checker"), with an optional **per-rule** `failClosed: true` that treats the content as unsafe (block). The operator had no way to set a global "halt rather than run unguarded" posture without authoring a full rule list with `failClosed: true` on every rule — and the default advisory rule set (authored in code, not YAML) carried no `failClosed` at all.

Some operators are fine with the checker being down and issuing a warning (the agent keeps working). Others don't want to risk it and would rather all calls fail when the checker is unavailable. The per-rule `failClosed` knob is too granular for this — the operator needs a global toggle, with `warn` as the default (preserving current behavior) and `fail` as the opt-in.

## Decision

**Add a global `guardrails.onCheckerDown` posture toggle (warn default / fail opt-in).** When `fail`, ALL rules treat a checker error as unsafe (block), unless a rule explicitly sets `failClosed: false`. When `warn` (the default), the current behavior holds: per-rule `failClosed` decides, else fail-open.

Resolution in `onCheckerError` (`internal/adapter/modelhook/modelhook.go`):
1. If the rule explicitly set `failClosed` (`failClosedSet == true`) → the per-rule value wins (true → block, false → warn).
2. Else → the global `failOnCheckerDown` fills in (true → block, false → warn).
3. Advisory rules always fail-open regardless — an advisory finding is observe-only by definition.

The YAML key `guardrails.onCheckerDown` accepts `warn` (default, empty) or `fail`. A `*bool` field was avoided in favor of tracking presence (`FailClosedPresent` on `GuardrailRuleSpec`, set by the custom `UnmarshalYAML`) so the per-rule override can distinguish "not set" from "explicitly false" — a YAML `bool` can't do this on its own.

## Consequences

**Easier / better:**

- Operators who want "halt rather than run unguarded" can set `onCheckerDown: fail` once instead of `failClosed: true` on every rule.
- Operators who want per-rule control still have it — an explicit `failClosed` on a rule wins over the global.
- The default (`warn`) is byte-identical to the pre-feature behavior — no existing deployment breaks.

**Costs:**

- One new YAML key + one new `Config` field + one new `Runner` field + a presence-tracking field on `GuardrailRuleSpec`/`RuleSpec`/`CompiledRule`. The presence tracking is the cost of distinguishing "unset" from "explicitly false" in a YAML `bool`.
- The `failureStreak` "checker DOWN" escalation still fires under `fail` — the sticky WARN is emitted regardless of the posture (it signals the checker is down, not that the content was blocked).

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature whose fail-open/closed decision this extends (additively, not a supersession).
- [ADR 0049](./0049-guardrails-remove-maxchecks.md) — the precedent (#168) that removed the per-session call-count cap on the same thesis.
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
- Living docs: [`docs/usage.md`](../usage.md) (guardrails fail-open/closed section).
