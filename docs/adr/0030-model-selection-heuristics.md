# ADR 0030 — Layered model-selection heuristics

- Status: Accepted
- Date: 2026-06-17
- Scope: composition (`internal/app`) model resolution; the run-entry/rehydration seam; an operator-gated subagent router; operator-vs-project config layering. No engine-core, port, or wire-contract change beyond a `resolved_model` echo-semantics clarification.

## Context

A model is chosen today on several axes that do not compose into a coherent policy: an operator default (`cfg.Model`, `Config.DefaultProvider`/`DefaultModel`), a per-session selector (`CreateSessionRequest{provider_id, model_id}` → `sessionEngineFactory` → `engineDepsForProvider`), a per-def pin (`AgentDef.Model`/`.Provider` via the one resolver `resolveProviderModel`), a per-call subagent override (`buildSubagentEngineFactory`), and semantic aliases (`Config.ModelAliases`/`resolveAlias`). One per-function knob already exists — `SubagentAskReviewerModel` — proving the "this internal call runs on a different model" pattern.

Three gaps remain (issue #86):

1. **A model per mode**, where mode is `PermissionMode` (plan vs execute) — a strong reasoning model while planning, a faster one while executing (the `opusplan` pattern).
2. **Per-mode/per-role bindings settable globally OR per project**, with the operator able to cap the allowable set.
3. **Auto-detecting a subagent's task and picking a model dynamically**, with the *harness* doing the classification.

Two facts shape the design. First, `Session.SetMode` (`engine/session/session.go`) is a real mid-session transition (ACP `session/set_mode`; deferred to the next prompt while running) — so "model follows mode" cannot be create-time-only. Second, the binding invariant is **provider** fixed per session, not model: `engineDepsForProvider` already re-derives everything model-dependent (Compactor, TokenCounter, ContextWindow, PromptConfig, Model) keyed on `(provider, model)`, and `Service.rehydrateSession` already rebuilds the per-session engine at the run-entry seam (`StartRunContent`). A same-provider model swap between turns is mechanically available on a seam that already exists and already survives restart.

Rejected alternatives: a mid-stream model swap (breaks no-replay-after-first-chunk); a FrugalGPT-style cascade router (re-running the cheap child re-executes its tools — fights the tool invariants); an embedding-similarity router (no embeddings port exists; a new dependency); putting the model binding inside the agent-def file (Roo Code's lesson is the opposite — keep the binding out of the consumer so the same agent runs under different models per deployment; aliases give that indirection for free); project-overridable *without* an operator cap (a project could point at an unvetted model).

## Decision

Adopt **aliases as the spine** and layer three mechanisms on top, all in composition.

**Layer 1 — aliases load-bearing.** Every consumer (slots, modes, router, agent defs) references a semantic alias (`reasoning`/`fast`/`cheap`); layered config binds aliases → concrete ids, capped by an operator allowlist. "Global or per-project" is then one binding question, not one per feature.

**Layer 2 — per-slot models.** A `models.slots` map binds named pipeline functions to aliases. The internal lightweight LLM calls (compaction summary, ask-reviewer, guardrail checker, team synthesis) resolve their model from a slot (default `cheap`) instead of riding the session model. Threads through `engineDepsForProvider`; no new domain concept. Plan-vs-execute lands here as the `plan` slot.

**Layer 3 — mode→model, re-resolved between turns.** The model is **fixed per turn, re-resolved at the run-entry seam** when `Session.Mode` has changed since the engine was built. The `rehydrateSession` rebuild trigger widens to include a mode change; the `plan`/execution slot is re-resolved; `resolved_model` is re-emitted. The in-flight turn always finishes on the old model (boundary check, never mid-stream). Because this rides the run-entry/rehydration seam keyed off persisted `Session.Mode`, it is surface-agnostic: headless multi-turn, single-shot (`mecatequi`, create-time only), TUI/ACP, and cloud-native restart all use the one path. The engine core stays model-agnostic.

**Layer 3b — operator-gated subagent router.** A `model-router` capability built as a sibling of `ChildAskReviewer`/guardrail `modelhook`: a composition-built, operator-gated (a new flag, empty = off — an autonomous spend decision, not a permconfig key), tool-less one-turn engine on the `cheap` slot, role `"model-router"`, with a per-run breaker and the no-progress nudge disabled. It classifies a subagent's task prompt — carried inside the `untrustedFence`, parsed by the whole-output-single-JSON-object rule — into a **slot label** (not a raw model id), decide-once at delegation, committed for the child's lifetime. Precedence: explicit per-call `model` > agent-def `Model` > router > inherited default (the router fills the gap; it does not override pinned intent).

**Config — project-overridable, operator caps.** A new `OperatorModelPolicy()` accessor mirrors `OperatorPosture()`/`OperatorGuardrails()`, except project-**merge-within-cap** rather than project-ignored: the `allowlist` is operator-tier and non-wideable; a trust-gated project `.mecatl/settings.yaml` may re-bind `default`/`slots`/`aliases` only to allowlisted entries (anything else → WARN + ignored, fail-closed).

Resolution for agent-def and session model stays in `resolveProviderModel`
(`internal/app/agentdefs.go`), unchanged. Slot/router resolution uses the sibling
`resolveSlotModel` (`internal/app/slots.go`); both share `lookupModelAlias`
(`internal/app/agentdefs.go`) as the alias spine. No `port.LLMRequest` field is added.

### Implementation status

- **Phase 1+2 (Layer 1 + Layer 2) — IMPLEMENTED.** Aliases as the spine (reusing
  `lookupModelAlias`) plus a `models.slots` map (`internal/app/slots.go`,
  `resolveSlotModel`) routing exactly THREE internal lightweight calls to a slot:
  **compaction** (the cascade tier-4 summary call), **ask-reviewer**, and **guardrail**.
  Each routed slot defaults to the `cheap` tier; the default is byte-identical when no
  slot is configured; resolution is fail-soft. Config is **operator-tier only** this
  slice (`--model-slot` / the user-global `models:` YAML subtree; a project-tier block
  is ignored with a WARN) — the project-merge-within-cap (`OperatorModelPolicy()` with
  an allowlist) is **not yet built**.
- **Phase 3 (Layer 3 — mode→model) — IMPLEMENTED.** A fourth slot, **`plan`**, reuses
  `resolveSlotModel` UNCHANGED but is wired on the **mode axis**: the per-session engine
  factory (`sessionEngineFactory`) re-resolves the session model to the plan model when
  the session's `PermissionMode` is plan, within the SAME provider (the provider stays
  fixed per session). Its default tier is **`reasoning`**, not `cheap` (a plan model is a
  strong-reasoning model). The model is **fixed per turn, re-resolved between turns** at
  the run-entry seam: the server (`engineAndWorkspaceFor`) compares a session's `Mode`
  against the engine's `builtForMode` and rebuilds a stale per-session engine (CASE 1) or
  promotes a default-FS session (CASE 2, gated by the composition predicate
  `server.Config.ModeNeedsEngine`, nil unless a plan slot is active — the byte-identical
  guard), both through the ONE shared `buildAndRegisterSessionEngine` helper (factored out
  of `rehydrateSession`). `resolved_model` re-emits on the next `GetSession`/turn after the
  rebuild (a `SetMode` response still carries the pre-rebuild model). Restart-into-plan
  rehydrates on the plan model (the persisted `Mode` is read back). Config remains
  **operator-tier only** this slice.
- **Phase 4 (project-overridable model config within an operator cap) — IMPLEMENTED.** A
  TRUSTED project's `.mecatl/settings.yaml` may re-bind `models.default`/`models.slots`/
  `models.aliases`, but ONLY within a non-wideable operator **allowlist**
  (`OperatorModelPolicy()` mirrors `OperatorGuardrails()`/`OperatorPosture()`, reading the
  SAME operator `models:` block). The cap is **opt-in**: with no operator allowlist a
  project `models:` block stays WARN-ignored (byte-identical to Phase 1+2). A project
  `allowlist:` key is ignored with a WARN (a project cannot widen its own cap), and the
  honoured bindings are trust-gated (the same `--trust-project` gate as project allow
  rules). The membership check is **resolve-then-check**: each project binding's value is
  resolved to a concrete id via the alias machinery and tested against the (alias-resolved)
  allowlist set — accept (merge) or drop with a build-once WARN, the operator/default value
  kept. The cap applies to ALL config-file binding consumers — the session `default`, every
  slot (incl. the Phase-3 `plan` slot), and aliases. The precedence is
  **`CLI(operator flags) > project-YAML(capped) > operator-YAML(settings.yaml) > built-in`**
  (overriding the original "operator-YAML > project" recommendation above — the feature's
  whole point is per-project override; the allowlist is the operator's boundary, and within
  it the operator delegates the choice to the project, while an explicit operator CLI flag
  still wins as a deliberate per-run override). The split is `permconfig`
  (`OperatorModelPolicy()`/`ProjectModelBindings()`, capture-time trust+opt-in gate) +
  composition (`foldProjectModelBindings`, the allowlist canonicalization + resolve-then-check
  + the CLI-key-survival precedence, all build-once). The session `default` follows the same
  chain: an operator `models.default:` (UNCAPPED — the operator's own binding) re-binds the
  session default over the registry default (`foldOperatorModelDefault`), a capped project
  `default:` can override it, and a CLI `--model` beats both. The allowlist is a **flat set,
  not per-slot**: an allowlisted model may be bound by a trusted project to ANY slot,
  **including the `guardrail`/`ask-reviewer` safety checkers** — within the operator's
  declared trust (the operator approved the model), but coarser than "approved models" might
  imply, so an operator must not allowlist a model they would be unwilling to see used as a
  safety checker. (Per-slot allowlist scoping is a deliberate future follow-up, not part of
  this slice.) OUT OF SCOPE this slice (capped only when added): an `AgentDef.Model` literal
  and the per-session API `model_id` selector.
- **DEFERRED (not this slice):** team **synthesis** routing (it lacks a clean seam — the
  lead synthesis runs on the lead member's whole engine, not a one-shot call). **Layer 3b
  (the operator-gated subagent router) was SUBSEQUENTLY IMPLEMENTED in Phase 5** — see
  [ADR 0031](./0031-subagent-model-router.md), which records the one deviation from the
  sketch above (a named CATEGORY taxonomy rather than a bare slot label).

## Consequences

**Easier:** a coherent, layered model policy; cheap models for housekeeping calls (immediate token savings); the `opusplan` UX through machinery that already survives restart; per-project model choice without editing agent defs; a dynamic subagent router that reuses a proven, hardened pattern instead of new architecture.

**Harder / costs:** `resolved_model` shifts from "fixed per session, set once" to "re-emitted on mode change" — a proto-comment change plus a client re-read on mode transitions (mecatui, ACP); no wire change. A mode flip now costs a per-session-engine rebuild (the same cost `rehydrateSession` already pays, only on transitions). The router adds a per-delegation classifier call when enabled (mitigated by a free keyword prefilter and the `cheap` slot); it is an outlives-a-call resource (engine + breaker) requiring `0027` List 1 + List 2 rows. More config surface to document and test.

**Committed to:** the alias indirection as the single binding mechanism; slot-label (not raw-id) classification so the vocabulary stays unified; the operator allowlist as a non-wideable cap; defaults byte-identical when nothing is configured.

## See also

- Issue #86 (this work); related #77 (in-TUI mode switching), #78 (modelith `PermissionMode`), #20 (cross-provider switch — the same provider-fixed wall).
- [ADR 0016](./0016-multi-provider.md) (provider/model neutrality), [ADR 0021](./0021-guardrails.md) and the `ChildAskReviewer` 4-step model in AGENTS.md (the gated-checker pattern mirrored here), [ADR 0027](./0027-cloud-native.md) (rehydration seam + List 1/List 2 inventory), [ADR 0031](./0031-subagent-model-router.md) (the Phase 5 realisation of Layer 3b).
- Living docs (Phases 1–4): the alias spine + `models.slots` + the routed calls + the `plan` slot + the project-overridable-within-an-operator-allowlist config layering are documented in [`docs/architecture/providers.md`](../architecture/providers.md) (model resolution + the allowlist precedence chain), [`docs/usage.md`](../usage.md) (`--model-slot`/`--model-alias` + the operator `models.allowlist:`/`default:` + the trust requirement), and [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) (per-slot mechanics, the O5 finding, `foldProjectModelBindings` + the CLI-key-survival precedence). The remaining deferred layer (the subagent router) adds the router mechanics and the `0027` List 1 + List 2 inventory rows when it lands.
- Lifecycle convention: [ADR 0002](./0002-documentation-lifecycle.md).
