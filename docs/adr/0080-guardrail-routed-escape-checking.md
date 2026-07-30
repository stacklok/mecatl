# ADR 0080 — Guardrail-routed path-escape checking (composition pre-check, auto-only)

- Status: Accepted
- Date: 2026-07-30
- Scope: composition (`internal/app`) — the path-escape posture decision + the guardrails (modelhook) wiring; no engine, port, or proto change.
- Supersedes: —
- Superseded by: —

## Context

The path-escape posture plan
([`docs/acceptance/path-escape-posture.md`](../acceptance/path-escape-posture.md))
settled that posture `auto` should gate an out-of-root FS escape on the
guardrails decision **when the operator configures it**, and deferred the
"guardrail-routed escape checking" row to wave 2. The deferred question was
the route's shape:

- **(a) a new checker input field / arg-aware guardrail rule** — teach the
  modelhook matcher to key on call args (a path), so a guardrail *rule*
  expresses the escape gate.
- **(b) a composition-level pre-check** that calls the checker directly where
  the escape decision already lives.

Two constraints frame the choice. First, the modelhook matcher keys on the
tool **name** only (`internal/adapter/modelhook/matcher.go` `ruleMatches`),
never on call args — the plan already ruled out path-scoped rule syntax this
wave ("rules stay tool-name-scoped"). Option (a) therefore means widening the
matcher beyond tool names and inventing a path-matching rule syntax: new
config surface, new specificity/tiebreak semantics, and a hook-path decision
for something that is fundamentally a **permission** decision. Second, the
escape decision already lives in the composition-layer wrapping
`port.PermissionPolicy` (`internal/app/escapepolicy.go`), where it composes
with the inner fold (deny-dominance, the configured-Ask floor, plan-mode
precedence) *before* dispatch. An escape gate expressed as a guardrail hook
would run on a different seam (the HookRunner), after authorization, and could
not honour the configured-Ask floor or plan-mode precedence the policy wrapper
already guarantees.

## Decision

Choose **(b): a composition-level pre-check inside the escape policy**, not a
new checker input field and not a path-scoped rule.

Concretely:

- **The mechanism.** At posture `auto`, when the escape knob is configured,
  the escape policy routes a non-denied out-of-root escape through the
  **same** engine-backed `modelhook.VerdictChecker` the hook-path Runner uses
  (`agent.RunGuardrailCheck` over a tool-less one-turn checker engine +
  `modelhook.ParseVerdict`). The escape's raw args JSON is fenced with
  `agent.WriteUntrustedBlock` under a short escape-specific rubric — the
  identical dual-LLM quarantine (whole-output-single-object verdict parse) as
  the ask-review and guardrail prompts. Verdict mapping:
  - `safe` → fall through to the ordinary `auto` posture row (read **Allow** /
    write **Ask**);
  - `unsafe` → **Deny** the escape (a checker block is a veto, mirroring the
    hook-path PreToolUse block);
  - checker **error/timeout/unparseable** → **fail CLOSED** to the
    write-escape **Ask** (the already-safe posture that surfaces to a human and
    is deny-safe headless) — never a silent allow, never a plain pass-through
    of the read-allow row.
- **The operator-config surface.** A single operator-tier key
  `guardrails.escape: true` in the user-global `settings.yaml` `guardrails:`
  subtree (no CLI flag, YAML-only like the other guardrails scalars), folded
  onto `Config.GuardrailsEscape`. It is **operator-tier only** — a project-tier
  `guardrails:` block is already ignored wholesale by `permconfig.Resolver`
  (a project repo weakening/disabling a security checker is a downgrade), and
  the escape knob inherits that gate. The knob implies nothing without a
  checker model: it requires `guardrailsConfigured` (a checker model
  configured, kill-switch off). Default `false` = the un-routed posture table,
  byte-identical to before.
- **The posture scope.** The route is **auto-only**. `yolo` demotes all
  guardrail modes to advisory (ADR 0062) and never spends a checker call on a
  decision the posture already made; `strict`/`trusted` keep their own
  Scenario-4 escape Ask. `withEscapeGuardrailRoute` refuses to arm the route at
  any non-auto posture.
- **The engine scope.** The route rides **only the main session's** escape
  policy. A child engine never relaxes escapes at any posture (Scenario 5), so
  there is no child route, and the checker engine is built through the
  recursion-guarded `childEngineDepsForProvider` path (inert hooks, tool-less),
  exactly like the hook-path checker.
- **Deny-dominance is preserved.** The route runs **after** the inner policy
  fold and only on a call the inner did not deny and did not configured-Ask —
  a configured Deny or a configured Ask never reaches the checker.

Rejected alternative (a) is rejected on cost and layering: it opens a new
arg-aware rule syntax and a hook-path decision for a permission problem,
against the plan's explicit "no path-scoped rules this plan" cut, for no
expressiveness the pre-check lacks.

## Consequences

- Easier: the escape gate needs no new rule language, no matcher change, no
  port/engine/proto change, and reuses the already-hardened checker + verdict
  parse. The routed decision lands where the posture decision already lives, so
  the whole permission fold (deny-dominance, configured-Ask floor, plan mode)
  applies to it unchanged.
- Harder / costs: an `auto`+knob escape costs one checker LLM call per escape
  (bounded by `agent.RunGuardrailCheck`'s timeout; token spend is the
  operator's opt-in, as with all guardrails). The route is a second consumer of
  the checker; both share the recursion guard and the "checker DOWN" posture is
  the hook-path Runner's, not the route's (the route fails closed to an Ask,
  which is itself safe).
- Committed: the escape knob is operator-tier and auto-only; widening it to
  other postures or to a path-scoped rule language is a new ADR.

## See also

- [`docs/acceptance/path-escape-posture.md`](../acceptance/path-escape-posture.md)
  — the posture table and the deferred "guardrail-routed escape checking" row
  this resolves.
- [ADR 0021](./0021-guardrails.md) — the modelhook guardrails architecture
  (the dual-LLM quarantine, the checker engine, the verdict parse).
- [ADR 0060](./0060-guardrails-bash-default.md) — adding Bash to the default
  rule set with the read-only pre-filter (the cost-guard precedent the route's
  auto-only scope follows).
- [ADR 0062](./0062-guardrails-approve-once.md) — approve-once + the posture
  demotion (yolo → advisory) that scopes the route to `auto`.
- [ADR 0047](./0047-absolute-path-resolution.md) — the canonicalize-then-reject
  containment the escape relax never strips.
