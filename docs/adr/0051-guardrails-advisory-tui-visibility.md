# ADR 0051 — Surface advisory guardrail findings to the TUI

- Status: Accepted
- Date: 2026-06-24
- Scope: the advisory-mode guardrail finding path — `internal/adapter/modelhook/modelhook.go` (`enforce`, ModeAdvisory arm), `engine/agent/dispatch.go` (hook-event emission), `engine/session/event.go` (`HookAdvisory`), the TUI render (`cmd/mecatui/ui/render.go`), and the wire proto (`contracts/proto/mecatl/v1/harness.proto`, `HOOK_DECISION_ADVISORY`). Adds a new exported `session.HookAdvisory` const + a new proto enum value.
- Supersedes: the advisory-visibility clause of [ADR 0021](./0021-guardrails.md) ONLY — the line "the client and model see nothing on an advisory finding." 0021's threat model, modes, matcher, enforcement, recursion guard, fail-open/closed, and cost guards carry over.
- Superseded by: none

## Context

[ADR 0021](./0021-guardrails.md) designed advisory mode as observe-only: "a finding is an operator diagnostic, the call/result is byte-unchanged, so the operator measures the false-positive rate before promoting a rule to block/sanitize." The stated goal was measurement before enforcement — but the implementation made measurement impossible: an advisory finding went ONLY to the operator log (`r.diag.Log`) and returned an empty `governance.HookOutcome{}`. The loop's dispatch only emits an `EvHook` when a hook outcome does something (`HookBlocked` or `HookModified`); an empty outcome means "hook did nothing" → no event → the TUI never sees the finding.

An operator who has to leave the TUI mid-session and `grep guardrail-finding` a log file to discover the checker flagged three `mcp__*` calls will not do it. Advisory mode became a black hole: findings fired, vanished into the operator log, and never got promoted because the signal never surfaced in the operator's primary interface.

## Decision

**Advisory findings now emit a client-visible `EvHook` (with `HookAdvisory` decision, warning-coloured) in addition to the operator diag log. The model sees nothing — the tool result is byte-unchanged.** Concretely:

- `session.HookAdvisory` (`HookDecision = "advisory"`) — a new decision value, client-visible, model-invisible. Added to `engine/session/event.go` + the proto `HOOK_DECISION_ADVISORY` enum value.
- The advisory arm of `modelhook.enforce` returns `governance.HookOutcome{Message: "guardrail advisory: <reason>"}` (instead of an empty outcome). The `r.diag.Log` call stays for headless deployments.
- Dispatch recognises the advisory shape (`Message != "" && !Block && len(Mutated) == 0` — a previously-unreachable state) and emits an `EvHook` with `HookAdvisory`. The call/result is not altered.
- The TUI renders an advisory notice with the "⚠" glyph in warning colour, distinct from blocked (error "✗") and modified (info "✎").
- `governance.HookOutcome` is NOT widened — `Message` (already on the type) is the signal; the dispatch discriminator is a previously-unreachable state, so no collision with existing paths.

## Consequences

**Easier / better:**

- Advisory mode now functions as designed: the operator sees findings in the TUI where they work, can measure the false-positive rate, and can promote to `block`/`sanitize` with confidence.
- The diag log stays for headless deployments (`mecated --headless`, `mecatequi`) — the `guardrail-finding` grep marker remains the correlation channel for post-hoc log analysis.
- Model-invisibility is preserved: the tool result is byte-unchanged, no annotation reaches the model. A malicious tool result that trips a spurious advisory finding cannot steer the model's behaviour.

**Costs:**

- An MCP-heavy session under the default advisory set fires a finding per `mcp__*` call — the TUI scrollback gets noisier. This is a rendering concern (count badge, collapsible notices) that this ADR does not address; the visibility is the correct behaviour, the rendering can be improved later.
- The advisory `EvHook` adds one event per finding to the client stream. In headless deployments (no TUI) the event is still emitted but has no consumer beyond the log — the relay is unaffected.

**Unchanged at the time:** the threat model, quarantine, framing discipline, verdict parse, enforcement modes, checker-down posture, and recursion prevention. Later contextual-review changes are recorded by [ADR 0342](./0342-contextual-investigative-guardrails.md).

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature whose advisory-visibility clause this supersedes (everything else carries over).
- [ADR 0049](./0049-guardrails-remove-maxchecks.md) — the precedent (#168) that removed the per-session call-count cap on the same thesis (a security control should not silently degrade).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
- Living docs: [`docs/usage.md`](../usage.md) (guardrails advisory mode), [`docs/architecture/hooks-and-guardrails.md`](../architecture/hooks-and-guardrails.md).
