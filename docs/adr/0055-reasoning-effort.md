# ADR 0055 — Reasoning-effort knob

- Status: Accepted
- Date: 2026-06-25
- Scope: The provider adapters (`provider/openai`, `provider/anthropic`), the composition layer (`internal/app`), the permission-config resolver (`internal/adapter/permconfig`), the gRPC/HTTP contract + server (`contracts/proto/mecatl/v1`, `internal/adapter/server`), the `Session` aggregate (`engine/session`), and the `mecatui` client + TUI.

## Context

Reasoning models expose an "effort" / "thinking depth" knob: OpenAI's Responses API
takes `reasoning.effort`, and Anthropic's Messages API takes `output_config.effort`.
Operators want to dial this per deployment (a cheap fast lane vs a deep lane) and per
session (a hard task gets `high`, a trivial one gets `low`), without forking the
prompt or the harness.

Two structural constraints shaped the design:

1. **`port.LLMRequest` is frozen and provider-neutral** (the `Model` field is a bare
   opaque string; provider-private knobs like reasoning-effort, thinking-budget, and
   `store`/`include` are adapter-CONSTRUCTION Options, never request fields — a guard
   test in `engine/port/llm_neutral_test.go` tripwires silent widening). So effort
   cannot be an `LLMRequest` field, and the agent loop must never branch on it.

2. **The provider adapter is built ONCE per registry entry and SHARED across
   sessions** (`internal/app/registry.go`). Per-MODEL variation is expressed with
   resolvers keyed on `req.Model` (`WithThinkingResolver`/`WithMaxTokensResolver`).
   But effort is a user/operator CHOICE, not derivable from `req.Model` — so it
   cannot be a model-keyed resolver either.

The two providers also disagree on the supported range. Anthropic supports the full
`{low, medium, high, xhigh, max}` ladder; OpenAI's contract here is `{low, medium,
high}` (its current SDK happens to expose `xhigh`/`none`/`minimal` too, but we hold
the conservative contract). A neutral vocabulary therefore needs a per-provider clamp.

## Decision

**A neutral composition-string vocabulary, NOT a port enum.** Effort is the closed
set `{auto, low, medium, high, xhigh, max}` where `auto`/`""` mean UNSET — do not send
a reasoning-effort field at all (the provider's own default applies). This vocabulary
lives in composition (`internal/app/reasoning_effort.go`); the loop never sees it, the
`engine/api` surface stays clean, and each adapter maps the neutral token to its own
SDK enum. `NormalizeReasoningEffort` is the single validator (fail-soft: an unknown
token normalises to unset).

**Per-provider mapping + clamp.** Composition clamps the neutral token per provider
(`clampEffortForProvider`): OpenAI/OpenRouter clamp `xhigh`/`max` DOWN to `high` with a
diagnostic; Anthropic identity-maps all five. The clamp lives in composition — not the
adapter — because composition holds the `port.Diagnostics` needed to narrate it (the
openai adapter has none). The adapters carry a `WithReasoningEffort(string)`
construction Option; they map a recognised token verbatim and OMIT the field on
anything else (fail-soft — a stray token never 400s a request). The openai adapter
stamps `reasoning.effort`; the anthropic adapter stamps `output_config.effort`,
INDEPENDENT of (and coexisting with) the existing extended-thinking config — the
legacy manual-thinking path (`WithThinkingBudget`) is untouched.

**Per-session re-mint via the engine FACTORY, never a clone-and-swap-LLM.** Each
registry entry carries a `remintEffort func(effort string) port.LLMProvider` closure
capturing its construction inputs (key/baseURL/resolvers/resilience config). The
shared `entry.provider` is built with the OPERATOR-DEFAULT effort. When a session's
resolved effort differs from that default, the per-session engine factory
(`sessionEngineFactory`) calls `entry.remintEffort(sessionEffort)` to mint a fresh
same-provider adapter carrying the session's effort — exactly the discipline the
per-call subagent `model` override uses ("the factory owns adapter construction"). The
DEFAULT PATH is BYTE-IDENTICAL: when the resolved effort equals the operator default
the entry was built with, the shared provider is reused with no re-mint.

**Effort binds the AGENT and its subagents, NOT the harness's internal calls.** The
re-minted (effort-bearing) provider feeds the main engine and the per-session catalog's
subagent parent (so a session's children inherit its effort), but the three utility
engines — the guardrail content checker, the child-ask reviewer, and the model-router
classifier — are built off the OPERATOR-DEFAULT provider (`utilityProvider`), so a
session that dials `reasoning_effort:max` never silently raises the reasoning spend of
those cost-sensitive internal classifier/one-turn calls.

**Capability-gate degrade, fail-open on unknown** (the Aider pattern). If effort is set
but the resolved model reports `Reasoning==false` (catalog or live source), composition
DEGRADES: it drops the effort and emits a WARN. An UNKNOWN model (uncatalogued, no live
entry) FAILS OPEN — the effort is sent and the provider 400s honestly if it really
cannot — matching the existing unknown=capable posture of the thinking path.

**Operator-tier + per-session precedence.** The operator default is read from the
user-global `settings.yaml` `reasoning-effort:` key (and `--reasoning-effort`), folded
by `foldOperatorReasoningEffort` (CLI out-ranks YAML, mirroring posture/output-economy).
A project-tier `reasoning-effort:` key is WARN-ignored by permconfig (operator-tier
only — a project cannot raise the model's reasoning spend). A per-session
`CreateSession.reasoning_effort` OUT-RANKS the operator default.

**Persisted on the `Session` aggregate for restart fidelity.** `Session.ReasoningEffort`
is a write-once opaque creation label (next to `Profile`/`ProviderID`/`ModelID`),
round-tripped by the `sessnap` snapshot and the `eventsource` fold. A restarted
session re-mints the same-effort engine via the rehydration seam — and
`needsRehydration` now triggers on a persisted effort. This is the one accepted
additive `engine/api` change (`Session.ReasoningEffort`, classified Added=minor per
`engine/COMPATIBILITY.md`).

**Enum-only v1.** Effort is the enum ladder, not a raw token budget; `WithThinkingBudget`
stays for the legacy manual-thinking models (a separate, coexisting axis).

The effective resolved+clamped value is echoed on `CreateSessionResponse.resolved_model`
(`reasoning_effort`), so a client (the mecatui footer) can show the active effort
verbatim — the runtime-discoverability axis.

## Consequences

- A new per-registry-entry closure (`remintEffort`) outlives a call; it captures
  construction inputs and is invoked only on the off-default per-session path. It holds
  no mutable state of its own, so it adds no rehydrate-fidelity row beyond the session
  label itself.
- The default path stays byte-identical (no re-mint, shared provider), pinned by a test
  that asserts no re-mint occurs for an unset/equal effort.
- A re-mint constructs a fresh resilience-wrapped adapter per off-default session: a new
  breaker/retry wrapper instance, scoped to that session's engine. This is the same cost
  the per-call `model` override already pays.
- Provider neutrality holds: `port.LLMRequest` is unchanged (the
  `engine/port/llm_neutral_test.go` guard still passes); the loop never branches on
  effort.
- The OpenAI clamp is conservative (`xhigh`/`max`→`high`) even though the current
  openai-go SDK exposes an `xhigh` tier — a deliberate contract choice, revisitable by a
  superseding ADR if OpenAI's support stabilises.

## See also

- [ADR 0016 — Multi-provider](./0016-multi-provider.md) — the registry + per-session
  factory this re-mint rides on.
- [ADR 0037 — Engine stability contract](./0037-engine-stability-contract.md) — the
  `engine/api` gate the `Session.ReasoningEffort` addition is classified under.
- The provider-neutrality discipline and the per-session-factory re-mint pattern are
  the same ones recorded for the subagent `model` override and the "Provider is FIXED
  per session" rule in `docs/design/IMPLEMENTATION-NOTES.md`.
