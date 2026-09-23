# ADR 0031 — The operator-gated semantic subagent model router

- Status: Accepted
- Date: 2026-06-17
- Scope: composition (`internal/app`) + the engine `Subagent` delegation path (`engine/agent`); a new operator-tier `models.router:` config subtree and a `--subagent-model-router` enable flag. No `port.LLMRequest`, proto, or wire-contract change.
- Supersedes: none (it REALISES the "Layer 3b" deferred item of [ADR 0030](./0030-model-selection-heuristics.md))
- Superseded by: [ADR 0042](./0042-taxonomy-gated-model-router.md) (the ENABLE-MODEL decision ONLY — the "Operator-tier only" paragraph's flag-to-enable gate is replaced by taxonomy-presence-enables + a `disabled:`/`--subagent-model-router=false` kill-switch, the guardrails-parity model; everything else in this ADR — the engine half, the breaker, precedence, fail-soft, observability, and the cost-amplification analysis — is REUSED unchanged); AND [ADR 0066](./0066-route-unpinned-and-writable-delegations.md) (the AGENT-GATING rule ONLY — a named `agent` that expressed NO model intent (no `def.Model`) is now ROUTABLE, explicit `model: inherit` being the pin; the engine half, breaker, precedence spine, fail-soft, and observability are otherwise unchanged)

## Context

[ADR 0030](./0030-model-selection-heuristics.md) laid out a layered model-selection
scheme and deferred its headline dynamic piece — "Layer 3b, the operator-gated subagent
router" — to a later slice. This ADR is that slice (Phase 5).

The problem: a `Subagent` delegation inherits the parent's model (or the global
`--subagent-model` default). But delegations are heterogeneous — "rename a variable
across three files" and "redesign the storage layer for concurrency" want very different
models. The model author already CAN pin a per-call `model`, but it rarely knows the
deployment's model catalogue or cost trade-offs, and a fixed default cannot adapt
per-task. We want the harness to pick the right tier of model PER delegation, from an
operator-defined menu, without the model author choosing and without a new architecture.

ADR 0030's sketch had the classifier emit a **slot label**. Building it, a **named
category taxonomy** proved cleaner: the operator writes `models.router.categories` —
each a `name`, a human `description` the classifier reads, and a `model` selector — so
the operator's intent ("small / large", "fast / deep", whatever vocabulary fits their
fleet) is explicit and self-documenting, and the classifier's only job is to pick a
category by its description. This is the one deviation from 0030's sketch.

Constraints that shaped it (inherited from the sibling features it mirrors):

- **Provider is FIXED per session** — the router may pick a MODEL within the session's
  provider, never switch providers.
- **No `port.LLMRequest` widening** — the classifier is a composition-built engine; the
  chosen model rides the existing per-call engine-factory path, not a request field.
- **The loop emits exactly THREE diagnostic lines** — router diagnostics are Build-once
  + the dispatch-time `routeTask` closure (like the policy-deny INFO), never a fourth
  loop line.
- **gauntlet #7** — no child content (the task prompt, the classifier's reasoning) may
  cross into observability; only bare metadata (a category label, a model id).

## Decision

Build the router as a **sibling of `ChildAskReviewer` and the guardrail `modelhook`** —
the proven composition pattern — not as new architecture.

**Engine half (`engine/agent/modelrouter.go`).** A free function `RunModelRouter` drives
a dedicated, composition-built, tool-less ONE-TURN classifier `Engine` (role
`"model-router"`, no-progress nudge disabled, bounded by a 30s timeout) over a prompt
that renders the category names+descriptions in the clear and the (model-authored,
UNTRUSTED) task prompt inside the shared `UntrustedFence` (framing neutralised). It
parses a single-JSON verdict `{"category":"<name>"}` with the **whole-output-single-
object** rule (the issue-#31 hardened parse, reusing `StripLoneCodeFence`) and VALIDATES
the category against the offered list — a hallucinated category is a miss. The engine
layer stays **model-string-only**: `RunModelRouter` returns a CATEGORY NAME; composition
owns the category→model mapping.

**Composition half (`internal/app`).** `buildModelRouterTask` returns the
`agent.Deps.SubagentModelRouter` closure: it builds the classifier engine on the `router`
slot (default `cheap` tier; an operator `classifier-slot` overrides) via the
byte-for-byte `askAdjudicatorDeps` recipe (`childEngineDepsForProvider`,
`MaxNoProgressNudges=-1`, forced-nil nested caps), calls `RunModelRouter`, and maps the
chosen category's `Model` selector through `lookupModelAlias` to a concrete id (operator
taxonomy targets are UNCAPPED — the operator is authoritative). It is wired at BOTH
main-engine sites (`buildEngine` + `sessionEngineFactory`), re-derived per session like
`attachAskAdjudicator`.

**The run() hook + precedence.** The `Subagent` tool's `run()` consults
`parentCaps.routeTask` (bound from `Deps.SubagentModelRouter` in `Engine.parentCaps`)
BETWEEN fork-validation and engine selection, ONLY for a plain default delegation. The
chosen model threads into the EXISTING per-call `model` factory path
(`t.engineFactory(model)`) — **decide-once, commit-for-child-lifetime, same-provider**.
Precedence is enforced by GATING (`maybeRouteModel` returns empty unless none of these
are set): **explicit per-call `model` > agent-def `Model` > fork/resume > router >
inherited default**. The router fills the gap; it never overrides pinned intent. Both the
foreground and background paths route (the decision is made in `run()` before the
background branch and threaded into `backgroundChild`).

**Fail-soft + breaker.** The router is NEVER load-bearing. Any classifier failure,
cancellation, unparseable verdict, hallucinated category, or unresolvable target → the
delegation inherits the default explorer model. A per-run circuit breaker
(`modelRouterBreaker`, default 3 consecutive misses, mirroring `askReviewBreaker`) opens
after repeated misses and skips the classifier for the rest of the run; a success resets
it. Its mutex serialises classifications within a run, so a Subagent fan-out cannot
multiply classifier spend in parallel.

**No nesting.** A child engine has no `Subagent` tool, so structurally no router;
`childEngineDepsForProvider` additionally forces `Deps.SubagentModelRouter` nil (the
classifier engine is built through that path — inheriting it would recurse at
construction). The gRPC `RunTeam` direct path stays zero-caps.

**Operator-tier only.** The taxonomy lives in the user-global `settings.yaml`
`models.router:` subtree; a PROJECT-tier `router:` is stripped with a WARN
(`captureProjectModels`). The ENABLE gate is a FLAG (`--subagent-model-router`),
deliberately NOT a permconfig key — autonomous per-delegation model selection is a
spend/capability decision the operator owns, the same posture as `--subagent-ask-reviewer`.

**Interactive + headless.** The router runs in BOTH — it is orthogonal to the ask-review
path (it picks a model before the child runs; it does not adjudicate a permission ask).

**Observability.** `session.SubagentPayload` gains `RoutedCategory`/`RoutedModel`, set on
`EvSubagentStart` only when routed — bare metadata, gauntlet-#7 safe. This slice scopes
them to the session struct + a per-classification INFO emitted from the **dispatch-path
`routeTask` closure** (like the policy-deny INFO, NOT the `resolveChildAsk` child
chokepoint) + a Build-once "router ACTIVE" fact; the **proto/client wire** for the two
fields is a deliberate follow-up (no proto field is added — `buf` is not part of this
slice, and the engine-layer routing + diagnostics carry the feature without it).

## Consequences

**Easier:** per-task model selection from an operator menu, with zero model-author
burden and zero new architecture (a third instance of the composition-built one-turn
engine pattern). Byte-identical when OFF (no flag / no taxonomy ⇒ no classifier call,
proven by a test). Reuses the hardened fence + single-JSON parse, the slot/alias spine,
and the per-call engine factory.

**Harder / costs:** an enabled router adds one cheap classifier call per plain
delegation (mitigated by the `cheap`/`router` slot and the per-run breaker). It is an
outlives-a-call resource — the classifier engine (session-scoped, reconstructible) and
the per-run breaker (lost-by-design) take rows in [ADR 0027](./0027-cloud-native.md) List 1.
The `RoutedCategory`/`RoutedModel` observability is session-struct + diagnostics only
until the proto/client wire follow-up lands. One deviation from ADR 0030's sketch (a
category taxonomy rather than a bare slot label) — recorded here so the two read together.

**Cost-amplification residual (CWE-770, bounded by #92).** A malicious or peer-injected
`Subagent` task prompt can STEER the classifier toward the operator's most-expensive
category — the per-run breaker only counts MISSES (it cannot tell a "steered but valid"
classification from an honest one, and a steered success resets it), so it is not a
defense against steering. This is **bounded**: the router can only ever pick from the
operator's OWN taxonomy (the operator chose every category's model), the provider is
fixed per session, and the REAL spend ceiling is the token budget — `--max-run-tokens`
(per-run), `--max-team-tokens` (per-team), and the per-call `max_run_tokens`
tighten-only override. As of #92, `--max-run-tokens` bounds BOTH the routed child AND
the cumulative classifier spend: the classifier's `session.Usage` is folded into the
parent `sess.Usage` by the dispatch-path `routeTask` closure
(`Engine.parentCaps`/`dispatch.go`) UNCONDITIONALLY on every classification (hit OR
miss), so `budgetExhausted` (which reads `sess.Usage.TotalTokens()`) covers the
classifier. The per-run breaker bounds the CALL COUNT; the parent budget now bounds the
TOKEN spend of both the classifier and the routed child. Like the main loop's
`StopBudget`, the brake is a TURN-BOUNDARY terminal: a single in-flight classifier turn
(itself capped at one turn / 30s) may complete and push cumulative spend past the ceiling
before the budget trips on the next boundary — the same bounded-overshoot model as the
main loop, not a leak. Operator guidance: rely on
`--max-run-tokens` as the hard ceiling; a taxonomy spanning a WIDE cost range is
steerable by untrusted task prompts, so size the category models to the deployment's
acceptable per-delegation cost. No steering-detection mitigation is added (a steering
detector would be a heuristic with its own failure modes, and the token budget already
bounds the worst case).

## See also

- [ADR 0030](./0030-model-selection-heuristics.md) — the parent scheme; this realises its Layer 3b.
- [ADR 0021](./0021-guardrails.md) and the issue-#31 ask reviewer — the sibling composition-built one-turn-engine patterns this mirrors.
- [ADR 0027](./0027-cloud-native.md) — List 1 (the classifier engine + the per-run breaker resource rows).
- Living docs: [`docs/architecture/providers.md`](../architecture/providers.md) (the router section), [`docs/usage.md`](../usage.md) (`--subagent-model-router` + the `models.router:` YAML block), [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) (router mechanics, precedence, the breaker).
