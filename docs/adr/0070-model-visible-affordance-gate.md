# ADR 0070 — Model-visible affordance gate

- Status: Accepted
- Date: 2026-07-21
- Scope: the convention that any model-behavior-dependent affordance (a tool, gate, or
  mode whose correct operation requires the model to DO something) ships with BOTH a
  model-visible prompt-layer instruction AND an executable system-prompt discoverability
  test. Applies repo-wide wherever an affordance is added; recorded here so a future
  implementer finds ONE named rule, not a scattered idiom.
- Supersedes: none
- Superseded by: none

## Context

Issue #206 shipped a plan-approval gate keyed on the model calling the `PresentPlan`
tool (ADR 0069): the dispatcher intercepts a `PresentPlan` call by name, surfaces it as
an askable `PlanOriginated` permission ask, and on approval flips the session mode from
plan to execute. The gate is correct in mechanics — but it is **load-bearing on model
behavior**. Nothing in the shipped system prompt told the model the tool/gate existed, or
that it MUST call `PresentPlan` to obtain approval, or that an inline "acceptable" /
"looks good" / "approved" in chat is NOT approval. So the model improvised: it treated an
affirmative user word as the green light and proceeded, and the gate never fired. The
fix added a `planModePostureNote` appended to the plan-mode session's Role (the
cache-stable `StablePrefix` layer) via an `applyPlanModePosture` helper wired into the
per-session factory, plus a per-turn reinforced reminder in `engine/prompt/builder.go`
(`Build`), plus a test asserting the contract lands in the built engine's system prompt.

This is a **class** of bug, not a one-off. A model-facing affordance whose correct
operation depends on the model doing something — calling a specific tool, stopping after
a signal, treating a channel as authoritative, NOT improvising a workflow — is incomplete
if it ships without (a) a model-visible instruction telling the model it exists + the
exact expected behavior, and (b) an executable test proving that instruction actually
lands in the system prompt. Without (a) the model cannot be expected to use the
affordance; without (b) a future refactor that silently drops the instruction
reintroduces the bug and CI stays green.

The repo ALREADY had the idiom: `internal/app/build.go` (`applyNoFSPosture`) — the
"MODEL-VISIBLE POSTURE (mandatory discoverability, the #40 pattern)" comment — rewrites
the prompt for a no-filesystem engine and appends a note to the Role telling the model
there is no filesystem, and `internal/app/nofs_profile_test.go` asserts that note is
present in the built engine's system prompt. Issue #206's fix replicated the same shape
(`planModePostureNote` + `applyPlanModePosture` + a system-prompt-content test). But the
idiom was never generalized into a named rule a future implementer would find before
repeating the mistake.

## Decision

**A model-behavior-dependent affordance ships with TWO things, or it is incomplete:**

1. **A model-visible prompt-layer instruction.** Tell the model the affordance exists and
   the exact expected behavior, via the layer that reaches the model at the right scope:
   a Role suffix (cache-stable `StablePrefix`, the `*PostureNote` constant + `applyXPosture`
   helper idiom in `internal/app/build.go`) for a posture the model must hold across the
   whole session; a `tool.Tool` `Spec().Description` for an affordance the model invokes by
   name; a per-turn reinforced reminder in `engine/prompt/builder.go` (`Build`)'s volatile
   suffix for a mode- or state-scoped nudge. Pick the layer that reaches the model at the
   right scope — a per-turn reminder cannot substitute for a cache-stable Role contract,
   and a Role suffix is the wrong place for a transient nudge.

2. **An executable system-prompt discoverability test.** Assert the instruction is
   present in the BUILT engine's system prompt via the REAL factory path
   (`sessionEngineFactory` → `RunContent` → captured `LLMRequest.System`), NOT the
   `applyXPosture` helper in isolation — so the mutation that deletes the factory wiring
   (the `deps.PromptConfig = applyXPosture(...)` call) FAILS the test. One concrete,
   well-named test per affordance is the pattern; the RULE is what generalizes. Do NOT
   build a registry/framework. Assert against the LAYER the instruction owns
   (e.g. `req.System.StablePrefix` for a Role-suffix note), not the combined
   `Layered.Render()` — a clause duplicated in another layer (the plan-approval contract
   is carried in BOTH the Role `planModePostureNote` and `builder.go`'s volatile suffix)
   makes a `Render()` oracle vacuous against a wiring removal.

Reviewers reject a model-behavior-dependent gate that lacks either half.

## Consequences

**Easier / better:**

- One named rule closes a class of bug, not a single instance. A future implementer
  adding a model-invoked gate (a "request human review", a "checkpoint and wait", a
  "treat this channel as authoritative") finds the rule before shipping a gate the model
  never learns to use.
- The executable test makes the affordance's discoverability a CI-checked invariant, not
  a convention relying on reviewer vigilance. Deleting or weakening the factory wiring
  fails the build.
- The idiom is already in place (`applyNoFSPosture` + `applyPlanModePosture`); this ADR
  only names it, so no code change is required for existing affordances — only the
  discipline going forward.

**Costs:**

- Every new model-behavior-dependent affordance now carries a two-part tax: a prompt
  instruction AND a system-prompt-content test. This is the correct cost — an affordance
  the model cannot discover is dead weight — but it is a small ongoing obligation.
- The test must be written against the REAL factory path, which is heavier than testing
  the helper in isolation. The plan-approval test (`TestPlanModeEngineSystemPromptContainsPlanApprovalContract`)
  is the reference shape: build via `sessionEngineFactory`, drive a one-turn `RunContent`,
  capture the `LLMRequest.System` via a `mockllm.WithRequestObserver`.
- Asserting against the owning layer (e.g. `StablePrefix`) rather than `Render()` requires
  the test author to know which layer the instruction lives on — a small amount of
  per-affordance care, the alternative being a vacuous oracle.

## See also

- [ADR 0069](./0069-plan-approval-gate.md) — the plan-approval gate this rule was learned
  from (issue #206: gate shipped without telling the model → never fired).
- [ADR 0024](./0024-system-prompt-research.md) — the system-prompt research; P9
  ("reinforced reminders" in the volatile suffix) is the per-turn layer of this rule, and
  the StablePrefix-vs-VolatileSuffix placement discipline the layer choice rests on.
- [ADR 0002](./0002-documentation-lifecycle.md) — the documentation lifecycle convention.
- `internal/app/build.go` (`applyNoFSPosture`, `applyPlanModePosture`) — the `*PostureNote`
  + `applyXPosture` helper idiom.
- `internal/app/slots_plan_test.go` (`TestPlanModeEngineSystemPromptContainsPlanApprovalContract`)
  and `internal/app/nofs_profile_test.go` — the reference system-prompt discoverability
  tests.
