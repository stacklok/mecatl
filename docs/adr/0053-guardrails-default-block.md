# ADR 0053 — Flip guardrails default mode from advisory to block

- Status: Accepted
- Date: 2026-06-24
- Scope: the built-in default guardrail rule set — `internal/app/guardrails.go` (`defaultGuardrailSpecs`). Adds a new `guardrails.defaultMode` YAML key. No engine/port/proto/wire change.
- Supersedes: the default-mode clause of [ADR 0021](./0021-guardrails.md) ONLY — the line "a default advisory rule set applies when a model is configured but no explicit rules are authored." 0021's threat model, modes, matcher, enforcement, recursion guard, fail-open/closed, and cost guards carry over.
- Superseded by: none

## Context

[ADR 0021](./0021-guardrails.md) shipped the default guardrail rule set in **advisory** (observe-only) mode, with the rationale "the operator measures the false-positive rate before promoting a rule to block/sanitize." Combined with [ADR 0051](./0051-guardrails-advisory-tui-visibility.md) (advisory findings now surface to the TUI), advisory mode is a legitimate "observe before enforcing" posture — but it should be the opt-in, not the default. An operator who enables a security control expects enforcement, not an observe-only mode that requires a config change to actually block.

## Decision

**Flip `defaultGuardrailSpecs` from `ModeAdvisory` to `ModeBlock`.** The built-in default rules (WebSearch/WebFetch/mcp__*) now ship in block mode — enabling guardrails = real enforcement out of the box. Add a `guardrails.defaultMode` YAML key (`block` default / `advisory` / `sanitize`) so the operator can downgrade the built-in defaults to observe-only without authoring a full rule list. An explicit `rules:` list still replaces the defaults entirely (the `defaultMode` key is ignored when explicit rules are configured).

## Consequences

**Easier / better:**

- Enabling guardrails now means enforcement — the operator gets the protection they expect.
- Advisory is still available via `defaultMode: advisory` for operators who want observe-only first.

**Costs:**

- A stricter default — an operator who relied on the advisory default (no blocking, just logging) now gets blocking. The fix is `defaultMode: advisory` (one line). This is the intended trade-off: the default for a security control is enforcement.

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature whose default-mode clause this supersedes.
- [ADR 0051](./0051-guardrails-advisory-tui-visibility.md) — advisory findings now surface to the TUI (a prerequisite for advisory being a useful opt-in).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
