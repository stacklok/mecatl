---
id: 02-guardrail-routing-decision
title: Resolve guardrail-routed escape checking (ADR + mechanism)
blocked_by: []
status: done
branch: "plan-path-escape-posture-w2/02-guardrail-routing-decision"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/path-escape-posture-w2
---

# Task brief

Resolve the plan's deferred "Guardrail-routed escape checking" decision. The
design settled that `auto` should gate out-of-root escapes on the guardrails
decision WHEN the operator configures it — but the modelhook matcher keys on
tool NAME only (`internal/adapter/modelhook/matcher.go` `ruleMatches`), never on
call args, so a path-scoped guardrail RULE cannot express it. The deferred
decision: is the path-aware route (a) a new checker input field, or (b) a
composition-level pre-check that calls `agent.RunGuardrailCheck` directly?

Read `.claude/agents/tdd-worker.md` first. This is a DESIGN + minimal-mechanism
task, not a full feature:

1. Decide (a) vs (b) with grounding in the existing seams
   (`internal/adapter/modelhook/modelhook.go`, `agent.RunGuardrailCheck`,
   `internal/app/escapepolicy.go`). Prefer the smaller, layering-clean option;
   justify briefly.
2. Capture it as a NEW ADR (copy `docs/adr/template.md`, next number — check
   `ls docs/adr/` for the highest). The ADR pins the mechanism, the
   operator-config surface (how the escape guardrail knob is configured), and
   the fail-open/closed posture. ADRs are frozen; status Accepted, dated today.
3. Implement the MINIMAL mechanism the ADR chooses, wired so `auto`+configured
   escape knob routes the escape through the checker, with a guard test. Keep it
   tight — full guardrail sophistication is not the goal; the routed decision is.

If the honest conclusion is that guardrail routing is NOT worth building in Wave
2 (the mechanism is disproportionate to the value), say so, write the ADR
deferring it with reasons, and mark this task's AC as satisfied-by-ADR instead.
Do not force a build.

## Acceptance criteria

- AC-W2-G1: the guardrail-routing decision is captured in a new ADR
  (`docs/adr/NNNN-*.md`) naming the chosen mechanism (or the deferral, with
  reasons) and the operator-config surface.
  - verify: inspection — the ADR resolves and is linked per the matlatl gate.
- AC-W2-G2: if the ADR builds the mechanism, an `auto`+configured escape routes
  through the checker and a blocked escape is denied; if the ADR defers, this AC
  is satisfied by the deferral rationale instead.
  - verify: `TestPathEscapePosture_GuardrailRoutedEscape` (or `none — deferred
    by ADR-NNNN` if deferred)
