# ADR 0069 — Plan-approval gate

- Status: Accepted
- Date: 2026-07-21
- Scope: the plan-approval seam — the `engine/agent` PresentPlan signalling tool + the dispatcher's plan-ask surface, `engine/session` `PendingAsk.PlanOriginated`/`AskOrigin`/`Origin()`/`StopPlanApproved`, `engine/tool.PlanOnly` catalog gate, the `engine/agent.Run.planApprovedTarget` run-scoped flip, and the composition/adapter wiring (`internal/adapter/server.Service.ApprovePlan` + the `ApprovePlan` streaming RPC + `POST /v1/sessions/{id}/plan:approve` + the opt-in `--plan-mode-auto-approve` observer + the mecatui plan-approval modal). The `PermissionAsk` proto is UNCHANGED (no provenance field added; the tool name `PresentPlan` is the discriminator).
- Supersedes: none
- Superseded by: none
- Amended: 2026-07-23 — the deny/iterate path described in §3 ("On Deny it synthesizes a
  deny result and the loop CONTINUES in plan mode (the model iterates on the plan)") was
  superseded DURING implementation by a CLEAN TERMINAL. An operator deny of a plan ask now
  ends the run with `session.StopPlanIterate` (a non-error terminal, routed through the
  completed path, Reopen-recoverable): the run STOPS, the session stays `ModePlan`, and the
  operator's NEXT typed prompt drives the revision — the model does NOT keep iterating
  in-turn with no operator input. The living docs (`docs/architecture/agent-loop.md`,
  `docs/design/IMPLEMENTATION-NOTES.md`) and `engine/CHANGELOG.md`'s `StopPlanIterate` entry
  describe the shipped behaviour; this note corrects the frozen ADR rather than rewriting it.

## Context

Plan mode (`session.ModePlan`, ADR 0007 pattern 6 — explore-plan-act) has existed
since v1: a read-only toolset enforced via `governance` + `tool.Catalog.Available(mode)`,
with mutations hard-denied. What it LACKED was a structured approval gate. A model in
plan mode could present a plan in its assistant text, but there was no seam for the
operator to APPROVE that plan and hand control back for execution. The only exits from
plan mode were a client-driven `session/set_mode` (ACP) or a `SetMode` call with no
correlation to what the model had actually presented — the operator had to eyeball the
transcript and flip the mode manually, with no audit trail tying the flip to a specific
presented plan.

The harness already had the exact primitive a plan-approval gate wants: the
permission-ask machinery (`PauseForApproval` → `StateAwaiting` → `EvPermissionAsk` →
`Approve` → `ResumeWith`), plus the cross-process resume seam
(`Engine.ResumeApproval` → `driveFromAwaiting`, ADR 0027 Phase 2). ADR 0062 had JUST
proven the pattern — a PreToolUse hook BLOCK refined into an askable block reusing that
machinery, with a serialized `HookOriginated` marker so a fresh process resumes correctly.
A plan-approval gate is the same shape: a PresentPlan call refined into an askable ask,
with a serialized `PlanOriginated` marker. Reusing one mechanism for policy asks,
guardrail blocks, AND plan approvals is the obvious shape.

Two further forces shape the design. First, the `opusplan` model swap (ADR 0030 Layer 3)
already re-resolves the session model between turns when `Session.Mode` changes, via the
run-entry `rehydrateSession` seam (CASE 1 rebuild). So a plan→execute mode flip at the
terminal boundary ALREADY swaps the plan model for the execute model — the approval gate
only needs to flip the mode; the model swap rides the existing seam for free. Second, the
`SetMode` invariant (`engine/session/session.go` (`SetMode`)) rejects a mid-run
mode flip from `StateRunning`/`StateAwaiting` — it is legal only from `StateIdle` or
`StateCompleted`. Flipping at the terminal boundary (after `Stop`, when the session is
`StateCompleted`) preserves the invariant; flipping mid-turn would break it.

## Decision

**A PresentPlan signalling tool → a `PlanOriginated` awaiting ask → an atomic
`ApprovePlan` RPC → a `StopPlanApproved` clean terminal → a `SetMode` flip at the
terminal boundary. Plus an opt-in auto-approve observer in composition, NOT in the loop.**

**1. The PresentPlan tool (the signalling affordance).** `agent.NewPresentPlanTool()`
(`engine/agent/presentplan.go`) is a read-only, signaling-only `tool.Tool` whose
`Spec().Name == "PresentPlan"`. Its `Execute` is VESTIGIAL — it returns an
`"awaiting operator approval"` result and exists only for honesty on a misroute, because
the dispatcher intercepts a `PresentPlan` call by name in plan mode BEFORE execution. The
plan CONTENT is the model's preceding assistant text; the tool's args carry only an
optional one-line note (kept minimal so the model is not tempted to re-state the plan in
the args instead of in its assistant message). It implements `tool.PlanOnly` (see §4) so
the catalog hides it outside plan mode.

**2. The `PlanOnly` catalog gate.** `engine/tool/tool.go` (`PlanOnly`) is a new OPTIONAL
marker interface a `Tool` MAY implement to declare itself plan-mode-only. The catalog's
mode projection (`engine/tool/catalog.go` (`Available`) / `Specs` /
`AdvertisedSpecs`) EXCLUDES a `PlanOnly` tool from every non-plan mode, so `PresentPlan`
is ADVERTISED plan-mode-only. It is registered into EVERY catalog (shared + per-session)
so the name-set stays equal (guarded by `TestPerSessionCatalogMatchesSharedCatalog`); the
projection gate hides it outside plan mode. The dispatcher's name+mode check
(`sess.Mode == ModePlan && c.Name == "PresentPlan"` in `engine/agent/dispatch.go`
(`runReadBatch`/`runOne`)) is defense-in-depth ON TOP of the projection gate, not the sole
gate.

**3. The dispatcher's plan-ask surface.** `engine/agent/dispatch.go` (`surfacePlanAsk`) is
a sibling of `askHookApproval` (ADR 0062) that reuses the shared `surfaceAsk` spine:
mint an askID, build a `session.PendingAsk{PlanOriginated: true}`, `PauseForApproval` →
`StateAwaiting` → emit `EvPermissionAsk` → block on the verdict. It is sequenced
one-at-a-time in dispatch Phase 1 (never the parallel fan-out), exactly like a
policy/hook ask. The headless guard mirrors `preHook`'s askable-block degrade:
`!e.deps.Interactive && !e.deps.PlanModeAutoApprove` synthesizes a deny result teaching
the model the plan was not approved (fail-safe — no silent mode flip); the
`PlanModeAutoApprove` exception (§6) SURFACES the ask even when headless so the
composition observer can resolve it. On Allow it does NOT execute anything (PresentPlan is
signalling-only): it sets `r.planApprovedTarget` (AllowOnce → `ModeDefault`,
AllowAlways → `ModeAccept`) and synthesizes an allow result. On Deny it synthesizes a
deny result and the loop CONTINUES in plan mode (the model iterates on the plan).

**4. The serialized `PlanOriginated` marker + `AskOrigin`.**
`engine/session/session.go` (`PlanOriginated`) is a new serialized `bool`
(`json:"plan_originated,omitempty"`), the plan-approval sibling of `HookOriginated`. It
is CROSS-PROCESS LOAD-BEARING: the awaiting-resume path (`Engine.ResumeApproval` →
`resolvePendingCall` in `engine/agent/dispatch.go`) runs in a FRESH process and keys the
plan-flip branch on it — an Allow must NOT re-present the plan or run any tool; it
synthesizes the allow result and sets `r.planApprovedTarget`, exactly like the live path.
The `session.AskOrigin` enum (`AskOriginNone`/`AskOriginHook`/`AskOriginPlan`) +
`engine/session/session.go` (`Origin`) is a read-time convenience accessor
DERIVED from the two serialized bools (`HookOriginated`, `PlanOriginated`); it is NOT a
stored field. This RETIRES the `session.go` caveat that warned against folding a third
provenance signal into an enum with the run-scoped `ConfiguredAsk`/`FlooredConfiguredAllow`:
those two stay run-scoped and never-serialized, while the two serialized bools are the
on-disk contract and `Origin()` derives the single provenance from them without conflation
(Hook takes precedence if both were incorrectly set — a construction invariant violation,
since only one site ever sets a given ask).

**5. The `StopPlanApproved` clean terminal + the terminal-boundary mode flip.**
`engine/session/session.go` (`StopPlanApproved`) is a new `StopReason = "plan_approved"`,
a CLEAN non-error terminal (like `StopNoProgress`/`StopBudget`/`StopStructuredOutput`):
the session ends `COMPLETED` and stays Reopen-recoverable. `engine/agent/loop.go`
(`runLoop`) checks `r.planApprovedTarget` at TWO sites: an EARLY check at the top of the
loop (load-bearing for the awaiting-resume path, where `resolvePendingCall` already set it
and recorded the pending call's result) and a post-dispatch check (the live path). Either
fires `terminateComplete(…, session.StopPlanApproved, …)`. `engine/agent/loop.go`
(`terminateComplete`) flips the mode AT the terminal boundary: AFTER `sess.Stop(reason)`
moves the session to `StateCompleted`, `sess.SetMode(r.planApprovedTarget)` is legal
(`SetMode` rejects it only from `StateRunning`/`StateAwaiting` — the
`engine/session/session.go` invariant preserved, pinned by
`TestPlanApprovalDoesNotFlipMidTurn`). The error path (`terminate`) does NOT flip: an
errored plan run stays in plan mode, honestly. The flipped mode is saved so a
Reopen/restart continues in the approved posture. `r.planApprovedTarget` is RUN-SCOPED and
deliberately NOT serialized: a parked plan-ask resumes via `resolvePendingCall`, which
re-sets it on the resumed run before `runLoop` sees it (the serialized `PlanOriginated`
marker is the cross-process contract).

**6. The atomic `ApprovePlan` RPC (composition, NOT the loop).**
`internal/adapter/server/service.go` (`ApprovePlan`) is the atomic plan-approval
RPC. It composes EXISTING seams and adds NO new engine machinery: (1) a live run is
rejected (`ErrNotAwaitingPlan` → 409 — an approve mid-run must use the `Converse`
`ResumeApproval` frame); (2) the session is loaded and must be `StateAwaiting` on a
`PlanOriginated` ask (`sess.PendingAsk().Origin() == AskOriginPlan`); (3) `target_mode →
verdict`: `ModeDefault` → `VerdictAllowOnce`, `ModeAccept` → `VerdictAllowAlways`,
`ModePlan`/zero → `VerdictDeny` (iterate, no flip, no continuation); (4)
`resumeFromAwaiting` re-enters the loop AT the ask, the run terminates `StopPlanApproved`,
`terminateComplete` flips the mode; (5) ATOMIC CONTINUATION (allow paths only): after the
resumed run drains, a FRESH run starts via the SAME `StartRunContent` path
(`loadAndReopen` → `engineAndWorkspaceFor` CASE 1 rebuild picks up the FLIPPED mode →
execute model) carrying `agent.PlanApprovedProceedText` + an optional operator note. Both
runs' events are relayed on the one returned channel. On deny, NO continuation runs (the
session stays in plan mode; the model re-plans on the next prompt). The wire surfaces:
`contracts/proto/mecatl/v1/harness.proto` (`ApprovePlanRequest`) +
`rpc ApprovePlan(...) returns (stream Event)` (generated via `task generate`),
`internal/adapter/server/grpc_approveplan.go` (the streaming handler, mirroring the
`Converse` relay discipline), and `POST /v1/sessions/{id}/plan:approve` in
`internal/adapter/server/http.go`. The `PermissionAsk` proto is UNCHANGED — the tool name
`PresentPlan` is the discriminator (no provenance field added).

**7. The mode flip rides ADR 0030 Layer 3 (CASE 1 rebuild).** The plan→execute model swap
is NOT new: ADR 0030 Layer 3 already re-resolves the session model between turns when
`Session.Mode` has changed, via the run-entry `rehydrateSession` seam. The
`ApprovePlan` continuation run's `StartRunContent` → `loadAndReopen` →
`engineAndWorkspaceFor` compares the session's `Mode` against the engine's `builtForMode`
and rebuilds the per-session engine (CASE 1) on the flipped mode, re-resolving the `plan`
slot to the execute model. The approval gate flips the mode; the model swap rides the
existing seam for free.

**8. Auto-approve as an opt-in COMPOSITION observer, NOT in the loop.**
`internal/adapter/server/service.go` (`MaybeAutoApprovePlan`) is the auto-approve
observer: called by the gRPC/HTTP relay loops alongside `appendEvent`+`Persist` for every
`EvPermissionAsk`, it auto-resolves a parked plan-ask via the EXISTING `ApprovePlan` path
(`ModeDefault` + a loud note) when ALL of `cfg.PlanModeAutoApprove` (the OPT-IN operator
flag, DEFAULT OFF) + `ev.Ask.Origin() == AskOriginPlan` + headless
(`!cfg.Interactive`) hold. It NEVER fires for a non-plan ask, NEVER fires interactively,
and is NEVER load-bearing for safety (the engine still gates the PresentPlan — this merely
resolves the parked ask). The engine's `agent.Deps.PlanModeAutoApprove` is the seam that
loosens the `surfacePlanAsk` headless guard so the ask is SURFACED even when headless; the
engine NEVER auto-approves on its own. The flag is OPERATOR-TIER ONLY
(`internal/adapter/permconfig/resolve.go` (`OperatorPlanModeAutoApprove`) — a project-tier
`plan_mode_auto_approve:` is ignored with a WARN). Wired via
`internal/app/build.go` (`Config.PlanModeAutoApprove`,
`foldOperatorPlanModeAutoApprove`) + `cmd/mecated/main.go` (`--plan-mode-auto-approve`)
+ `internal/adapter/permconfig/schema.go` (`PlanModeAutoApprove` YAML key). A LOUD startup
diagnostic (`plan_mode_auto_approve: ON (NO HUMAN REVIEW)`) is emitted when on.

## Consequences

**Easier / better:**

- One approval mechanism for policy asks, guardrail blocks, AND plan approvals. Every
  client that already renders an `EvPermissionAsk` modal (mecatui, ACP, custom gRPC) gets
  the plan-approval gate for free — Approve & run / auto-accept edits / iterate, the same
  buttons (the mecatui modal keys off `tool == "PresentPlan"`, no proto field needed).
- The operator acts AT the presented plan, with an audit trail: the `EvApproval` verdict
  (tool NAME + verdict string + askID + the opaque call id — gauntlet #7) is recorded on
  the durable event log at BOTH verdict sites (`dispatch.go` `surfaceAsk` live path +
  `resolvePendingCall` resume path), so a restart-replay reconstructs the approval.
- The `opusplan` model swap (plan→execute) rides the existing ADR 0030 Layer 3 seam — the
  gate flips the mode, the model swap happens for free at the run-entry rebuild.
- Cross-process resume is correct: the serialized `PlanOriginated` marker prevents a
  re-present, and the run-scoped `planApprovedTarget` is re-set by `resolvePendingCall`
  before `runLoop` sees it. The `AskOrigin`/`Origin()` accessor retires the `session.go`
  caveat cleanly — the two serialized bools are the on-disk contract; the read-time enum
  derives the single provenance without conflating the run-scoped hints.

**Costs:**

- A new serialized `bool` on `PendingAsk` (`PlanOriginated`) — additive, `omitempty` keeps
  a pre-#206 snapshot decoding to `false` (covered by a snapshot round-trip test).
- A new streaming RPC (`ApprovePlan`) + HTTP route + proto message. The `PermissionAsk`
  proto is UNCHANGED (the tool name is the discriminator), so no `task generate` is needed
  for the ask surface — only for the `ApprovePlanRequest`/`ApprovePlan` rpc.
- A mode flip now costs a per-session-engine rebuild on the continuation run (the same
  cost `rehydrateSession` already pays on any mode transition; only on approvals, not
  every turn).
- The `PlanOnly` marker interface is one more catalog gate to keep honest (the dispatcher's
  name+mode check is defense-in-depth on top of it).

**Cloud-native inventory (ADR 0027): NO new List 1 / List 2 row is needed.**
`engine/agent/loop.go` (`planApprovedTarget`) is RUN-SCOPED (a within-run transient,
deliberately NOT serialized — the serialized `PlanOriginated` marker is the cross-process
contract, and `resolvePendingCall` re-sets `planApprovedTarget` on the resumed run before
`runLoop` sees it). `Service.ApprovePlan` reuses `resumeFromAwaiting` + `StartRunContent`,
BOTH already inventoried (the awaiting-resume seam, ADR 0027 Phase 2; the run-entry
funnel, Phase 1). `PendingAsk.PlanOriginated` is a serialized `PendingAsk` field in the
SAME class as `HookOriginated` (ADR 0062), already covered by the snapshot round-trip
discipline — no new outlives-a-call resource, no new rehydrate-fidelity ledger row.

## See also

- [ADR 0062](./0062-guardrails-approve-once.md) — the guardrail approve-once pattern this
  gate mirrors (the `HookOriginated` serialized-marker precedent, the shared `surfaceAsk`
  spine, the headless degrade inside the surface).
- [ADR 0030](./0030-model-selection-heuristics.md) Layer 3 — the mode→model re-resolution
  the plan→execute flip rides (the run-entry `rehydrateSession` CASE 1 rebuild).
- [ADR 0027](./0027-cloud-native.md) — the cloud-native arc; the awaiting-resume seam
  (Phase 2) the `PlanOriginated` marker rides, and the inventory discipline (no new row —
  see Consequences).
- [ADR 0007](./0007-twelve-patterns-audit.md) pattern 6 — explore-plan-act, the plan-mode
  feature this gate completes.
- [`docs/architecture/agent-loop.md`](../architecture/agent-loop.md) — the plan-approval
  approve→execute loop (living docs).
- [`docs/design/IMPLEMENTATION-NOTES.md`](../design/IMPLEMENTATION-NOTES.md) — the plan
  approval subsection (the seam mechanics).
- [`docs/usage.md`](../usage.md) — the `ApprovePlan` RPC, the `--plan-mode-auto-approve`
  flag, the mecatui plan-approval UX.
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
