# Changelog — `github.com/stacklok/mecatl/engine`

All notable changes to the engine module's public API are recorded here. The
format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/); changes
are classified per [COMPATIBILITY.md](./COMPATIBILITY.md) (Added = minor;
Changed/Deprecated/Removed = breaking, pre-v1 a minor bump).

The covered surface is the seven core packages (`session`, `governance`, `tool`,
`prompt`, `port`, `team`, `agent`); their committed API snapshots live in
[`engine/api/`](./api/).

## [Unreleased]

### Added

- **`prompt.Rule`, `prompt.RulesSource`, `prompt.RuleOrigin`, `prompt.RulesAssembler`, and `prompt.MaxRuleBytes`** (issue #329, task 1) — the port, value-object, and turn-0 assembler for `.claude/rules` / project-rules discovery. `Rule` is a pure value object (name, body, path globs, origin tier label — no path/dir/root concept, matching the `SoulSource`/`CommandSource` discipline). `RulesSource` is a consumer-defined `ListRules` port satisfied structurally by the adapter at the composition root. `RuleOrigin` (`project`/`user`/`driver`) is a tier label never a location, the third parallel closed label set after `SkillOrigin`/`AgentOrigin`. `MaxRuleBytes` (20 KiB) is the one canonical per-rule body cap all sources share, mirroring the soul body cap. `RulesAssembler` renders the discovered rules as a single user-role message, fenced, with combined byte (40 KiB) and count (32) caps, and a dropped-footer when truncated — fail-soft throughout. `RulesHeader()` exposes the shared header string so tests can assert the exact emitted text. The assembler sits naturally after `RootAssembler` and before `SoulAssembler` in the turn-0 ordering (project context → persona → saved facts → operator model). `IsInjectedTurn0Fragment` recognises the rules header. See ADR 0081 (to be written in task 3). Classified Added per COMPATIBILITY.md (new types, const, func, and assembler are a minor bump).- **`agent.WithTeamSharedBaseWorkspace` + `agent.WithTeamToolSharedBaseWorkspace`**
  (path-escape-posture task 06, Scenario 5 AC5.1d) — the team-supervisor
  analogue of `WithSharedChildWorkspace`: a `SupervisorOption` (plus its
  `TeamOption` counterpart the in-loop Team tool threads through) injecting the
  NON-relaxed workspace view a BASE-SHARING (shell-less) read-only team member
  runs against. The composition root wires it whenever the team base may carry
  out-of-root relaxation (the auto/yolo main-session relax): the supervisor's
  base-share fallback otherwise hands the member the base VERBATIM, silently
  giving the shell-less member the main session's escape reach — the same
  child-never-relaxes leak task 05 closed on the Subagent nil-forker path. The
  closure receives the base workspace root and returns the member's workspace;
  a nil return falls back to the verbatim base. The two FORKED tiers (Mutating
  force-copy, read-only worktree) never consult it — their forks already land
  in a non-relaxed constructor. nil (the default) is byte-identical to the
  pre-option behaviour. Classified Added per COMPATIBILITY.md (new exported
  option funcs are a minor bump). (path-escape-posture plan, task 06)

- **`agent.WithSharedChildWorkspace`** (path-escape-posture task 05, Scenario 5
  AC5.1b) — a new `SubagentOption` injecting the NON-relaxed workspace view a
  BASE-SHARING child runs against. The composition root wires it whenever the
  parent workspace may carry out-of-root relaxation (the auto/yolo main-session
  relax): a nil-forker read-only child (no shell wired) and the
  `mode:"read-write"` direct-write child (ADR 0041) both run against the parent
  base, and must see it WITHOUT the relax — the relax is main-session-only. The
  closure receives the parent workspace root and returns the child's workspace;
  a nil return falls back to the parent ws unchanged. A FORKED child
  (`WithChildForker` wired) never consults it. nil (the default) is
  byte-identical to the pre-option behaviour. Classified Added per
  COMPATIBILITY.md (a new exported option func is a minor bump).
  (path-escape-posture plan, task 05)
- **`session.SubagentPayload.Text` / `.Detail` / `.InnerKind` and
  `session.ParallelPayload.Text` / `.Detail` / `.InnerKind`** (ADR 0079,
  delegation-observability-convergence) — the two delegation payloads gain the
  same bounded-preview fields `TeamPayload` already carried: `Text` (a clamped
  child message/result-text preview), `Detail` (a clamped tool-call-args or
  tool-result-body preview), and `InnerKind` (the inner event kind the preview
  was projected from). Every preview is fed only through `clampPreview`
  (control-byte scrub + rune cap) at the single `drainChildObserved`
  chokepoint, is client-only, and never enters the parent's `Conversation`
  (gauntlet #7 unchanged). Classified Added per COMPATIBILITY.md (new struct
  fields are a minor bump). (delegation-observability-convergence plan,
  tasks 01–02)
 (chore(engine): api snapshot + CHANGELOG for the ADR-0079 payload widening)

- **`agent.WithMemberErrorRetries` + `agent.MemberOutcome.ErrorRounds` +
  `session.TeamMemberDisposition.ErrorRounds`** (issue #318, ADR 0077) —
  the bounded MEMBER RETRY that closes #318's last acceptance bullet ("a team
  member that hits one transient stall still participates in later rounds").
  ADR 0077 made a failed member's session RECOVERABLE, which rescued the lead's
  synthesis turn but still descheduled the member for the rest of the run.
  `WithMemberErrorRetries(n)` is a new `SupervisorOption` (default **1**) capping
  how many `StopError` rounds a member is retried through before it is benched with
  the same disposition it received before this release (`stopped` +
  `StopReasonError`, tasks released). `WithMemberErrorRetries(0)` restores
  bench-on-the-first-errored-round — the SCHEDULING half only. It does NOT restore
  the pre-ADR-0077 behaviour of the same round: a member whose round ends in
  `StopError` still has its session RECOVERED (`session.Session.Recover`) rather than
  latched `nonResumable`, so a failed LEAD still runs its synthesis turn either way.
  That half has no knob — see the "A FAILED delegated child is now resumable" entry
  under **Changed** for the one lever (`agent.WithSubagentStore`) and its scope.
  `MemberOutcome.ErrorRounds`
  and its `session.TeamMemberDisposition.ErrorRounds` mirror are new `int` COUNTS of
  a member's run-level failed rounds — the disposition-honesty signal, since a
  retried-then-finished member is `DispositionDone` with no `Reason` and would
  otherwise be indistinguishable from one that never failed. No enum gained a value
  (`MemberDisposition` is closed and mirrored on the proto wire) and no exported
  signature changed. Classified Added per COMPATIBILITY.md (a new option constructor
  plus two struct fields are a minor bump).

  **BEHAVIOUR CHANGE for library consumers on the default configuration:** a team
  member whose round ends in `session.StopError` is no longer benched on the first
  failure. It stays schedulable, its in-progress task claim is RELEASED back to
  pending (so it or a peer can re-claim), it is force-scheduled for one retry turn
  carrying a supervisor-authored retry note, and it is benched only once its errored
  rounds EXCEED the cap. The counter is per-member and NEVER reset, so at the default
  cap of 1 the exposure is exactly **one extra scheduled round per member over the whole
  team run** — not one per failure: a permanently-failing member runs `cap+1` = 2 rounds
  in total instead of 1, which is what bounds it and the reason the default is 1.
  A failed RECOVERY (`nonResumable`), a CANCELLED member, and a turn-budget-exhausted
  member are never retried. Pin `WithMemberErrorRetries(0)` to restore the previous
  release's SCHEDULING behaviour (see the scope note above — the session recovery is
  not covered by it).

  **OPERATORS of `mecated`/`mecatui`/`mecak8s` cannot reach `WithMemberErrorRetries`, and
  that is deliberate.** There is no `--max-member-error-retries` flag and no
  `app.Config` field: this is a per-member BEHAVIOURAL bound, and the repo's line is that
  those stay engine-only defaults while team-wide RESOURCE ceilings get flags. The
  precedent is the sibling `WithMemberTurnBudget` (default 200 turns per member), which is
  likewise engine-only with no operator flag — not `WithTeamTokenBudget`, which is
  team-wide and is wired to `--max-team-tokens`. An operator's control over the extra spend
  is therefore the existing token ceilings: `--max-team-tokens` (team-wide, summed across
  all members and rounds) and `--max-run-tokens` (per run, inherited by every member
  drive), on top of the built-in round cap and per-member turn budget. A LIBRARY consumer
  that wants fail-fast passes `WithMemberErrorRetries(0)`. (issue #318)

- **`session.SubagentPayload.Cause`** (issue #319) — a new `string` field on the
  redacted `subagent.*` observability projection carrying the child run's FAILURE
  DETAIL (the loop's `session.ResultPayload.Error`) when `Stop` is `StopError`;
  empty on every other terminal, and set on `EvSubagentEnd` ONLY. It is
  harness/provider metadata (a transport or loop error string), never
  child-authored model output, so it is gauntlet-#7 safe on the same footing as
  `Stop`/`Usage`. It is LINE-ORIENTED by contract — normalised at the emit site
  (whitespace collapsed to single spaces, then rune-clamped), so a consumer renders
  it as-is rather than re-deriving the collapse for its own single-line surface. It
  rides the proto/client wire end-to-end (`Subagent.cause` = field 17). Classified
  Added per COMPATIBILITY.md (a new struct field on an existing payload is a minor
  bump). Behaviour note for library consumers: the model-facing `Subagent` tool result
  on a `StopError` terminal now LEADS with this cause and demotes the child's last
  assistant text to clamped "Last activity before the failure" context — the same
  change applies to a failed `Parallel` branch's reported reason. Both halves of that
  body are framing-NEUTRALISED (`agent.NeutraliseFraming`) before composition, since
  neither is harness-authored: a provider error string or a child's prose could
  otherwise forge the `agentId:` trailer or a bracketed harness note it is composed
  next to. No exported signature changed. (issue #319)

- **`agent.SessionOriginScheduleManager` + `agent.NewSessionOriginScheduleManager` +
  `agent.OriginBinder` + `agent.Deps.OriginBinder`** (ADR 0075,
  fire-result-delivery task 02) — a new `port.ScheduleManager` wrapper that
  holds the current origin session id in a `sync/atomic.Pointer[session.SessionID]`
  and stamps every `CreateSchedule` call's `OriginSessionID` from the bound id
  (empty if unbound) before delegating. The `OriginBinder` interface (single
  method `BindSessionOrigin(session.SessionID)`) is an OPTIONAL `Deps` field
  the engine calls in `startRun` so the executing session's id is captured at
  the run boundary. Composition wraps the real manager with
  `NewSessionOriginScheduleManager` and wires it as both the tool's manager and
  the engine's `OriginBinder`. The model-supplied `origin` arg is never in the
  tool schema and is ignored if present — the bound id always wins. Classified
  Added per COMPATIBILITY.md (a new exported type + constructor + interface +
  Deps field are a minor bump). (fire-result-delivery plan, task 02)

- **`port.ScheduleSpec.OriginSessionID`** (ADR 0075, fire-result-delivery Phase 1) —
  a new `session.SessionID` field on `ScheduleSpec` carrying the session whose
  terminal result delivery should receive the fire's outcome. Empty means no
  delivery (the v1 pre-delivery posture). Non-empty values are validated at the
  create-seam: a non-existent session is rejected fail-closed with
  `ErrInvalidArgument`. The field is METADATA-ONLY — it is never rendered into a
  prompt, never surfaced to the model, and never appears in any model-visible
  surface. It is an infrastructure-level routing key the fire path reads to route
  the outcome. An empty OriginSessionID in out-of-band creates (REST/gRPC, no
  conversation) persists empty — additive, no existing behavior changed.
  Classified Added per COMPATIBILITY.md (a new struct field is a minor bump).
  (fire-result-delivery plan, task 01)

- **`port.DeliveryQueue` + `port.DeliveryNote` + `port.NopDeliveryQueue`**
  (ADR 0075, fire-result-delivery Scenario 4 / ADR 0027 List 1+2) — a new
  DURABLE per-session pending-delivery queue port for scheduled-task fire
  results, keyed on the ORIGIN session id (not a per-Run registry). The queue
  holds the opaque rendered note text (from the delivery renderer) plus a
  monotonic per-session sequence that is the exactly-once ledger key: a note
  queued during one run drains on the origin's next run-entry if the current
  run ends first (the ledger is session-scoped). A durable backing (the same
  durability the session snapshot has) survives a process restart so a note
  queued before a restart drains after it; `NopDeliveryQueue` is the
  byte-identical no-delivery default (a deployment with delivery unwired sees
  nothing). The loop stays storage-agnostic: the fire path (composition) ENQUEUEs,
  the loop's turn-boundary drain + the run-entry funnel DEQUEUE via this port.
  Classified Added per COMPATIBILITY.md (a new interface + value type + no-op
  default are a minor bump). (fire-result-delivery plan, task 04)

- **`agent.ScheduleQueryTool` + `agent.NewScheduleQueryTool` +
  `agent.ScheduleQueryToolName` (ADR 0073, the AC1.4 read-parallel/mutate-serial
  partition)** — the scheduled-task surface is now TWO catalog entries over the
  ONE injected `port.ScheduleManager`: a read-only query tool (list/inspect,
  catalog name `"ScheduleQuery"`, `ReadOnly()==true` so list/inspect join the
  read-parallel batch) and the mutating `agent.ScheduleTool`
  (create/pause/resume/delete/fire, `ReadOnly()==false` so every mutating call
  serialises). The split realises the per-verb ReadOnly partition a single
  tool's no-arg `ReadOnly()` cannot express: a mutating verb can never fan out
  into the read-parallel batch. Classified Added per COMPATIBILITY.md (a new
  exported tool + constructor + name are a minor bump). (schedule-tool plan,
  task 06 repair)

### Changed

- **A text-bearing `StopError` turn now carries a terminal CAUSE** (issue #319
  review-audit follow-up) — BEHAVIOUR only; no exported signature moved and
  `engine/api/*.txt` is unaffected. `Engine.terminateComplete` gains an internal
  `errMsg` parameter (it is unexported), and the loop's `finishTurnNoTools`
  synthesises the cause for a text-bearing turn that ended on a terminal stop
  CHUNK (`max_tokens` / `refusal` / `incomplete` / `failed` → `StopError`,
  relayed by both adapters' `mapStop` on the `ChunkDone` stop, NOT as a Go
  error) via the new unexported `stopTerminalCause`. Before this, such a turn
  emitted an EMPTY `session.ResultPayload.Error`, so a delegation's
  `subagentErrorBody` rendered the child's truncated/refused text AS the failure
  — the exact #319 presentation on the `terminateComplete` path (the empty-turn
  shape already routed through `terminate` with a cause). The synthesised cause
  is harness-authored metadata (a stop label + a "TRUNCATED or refused" shape
  note), never model text, so it is gauntlet-#7 safe, and it is `StopError`-only
  (a clean limit / cancellation keeps an empty cause, honouring the
  `SubagentPayload.Cause` "empty on every other terminal" contract). Consumers
  asserting that a text-bearing `StopError` delegation result led with the
  child's last text must adjust. No new exported symbol.

- **Model-facing next-action wording on the Subagent failure terminals** (issue #319 /
  #318 review round) — BEHAVIOUR/COPY only; no exported signature moved and
  `engine/api/*.txt` is unaffected. Three strings a consumer might be matching on
  changed, all in the same direction (the harness must not assert something the next
  turn contradicts):
  1. The `StopError` resume hint no longer says "continue where it left off" — it now
     states that the CONVERSATION is preserved but the WORKSPACE is not (the failed
     child ran in a throwaway checkout, so files it wrote are gone), which is what
     `resumeStalenessNote` tells the resumed child.
  2. The PER-CALL TIME-BUDGET terminal (`timeout_ms`) now carries a next action — a
     timed-out child lands `StateCancelled` and has always been resumable, but the
     terminal named no recovery, and a `mode:"read-write"` timeout gets the single
     combined resume-or-discard decision instead of a bare partial-edits warning.
  3. The last-resort recovered-digest prefix no longer restates the stop-reason note's
     next action; it states provenance + partial-ness only (the two rendered
     back-to-back and duplicated the clause byte-for-byte).
  Consumers asserting on the old copy must adjust. No new exported symbol.

- **A FAILED delegated child is now resumable** (ADR 0077, issue #318) — a
  BEHAVIOUR change with NO exported signature change, so it is classified Changed
  (behaviour only; `engine/api/*.txt` is unaffected). The `Subagent` tool's
  `resume: <agentId>` used to hard-refuse a child persisted in
  `session.StateFailed` ("ended in a failed state and is not resumable"); it now
  recovers it through the existing exported `session.Session.Recover`, matching the
  service layer's `loadAndReopen` discipline (all three terminals recover). A
  genuinely non-resumable state — e.g. a snapshot still recorded `running` — is
  still a model-addressable tool error. Two model-facing strings changed with it: a
  `StopError` Subagent result now carries a resume hint after its `agentId:`
  trailer, and a resumed `mode:"read-write"` child is told its earlier edits SURVIVED
  (it never forked) instead of receiving the read-only fresh-checkout staleness note.
  In `agent.Supervisor`, a team member whose round left its session failed is
  likewise recovered rather than benched, so a failed LEAD still reaches its final
  synthesis. `MemberOutcome.Stopped`/`Reason` for a failed round changed too — see
  the `agent.WithMemberErrorRetries` entry under **Added**: on the default
  configuration a member whose round ends in `StopError` is now retried, so it
  finishes `Stopped == false` / `DispositionDone` / `Reason == ""` rather than
  `stopped`/`error`. Read the two entries together; the disposition a consumer sees
  is the retry entry's, not this one's. Consumers relying on a failed child being permanently
  unresumable — or matching on the old refusal copy — must adjust. There is no knob
  that restores the old refusal; the ONE lever is the precondition `resume` has always
  had — it requires a wired session store, so a consumer that must forbid resuming a
  failed child does not pass `agent.WithSubagentStore` (which disables `resume:`
  wholesale, not just for the failed state, and also stops `InspectSubagent` from
  loading persisted child transcripts). A `StopError` result built without a store
  correspondingly omits the resume hint, so the model is never told about a path that
  deployment cannot serve. (issue #318)

- **`agent.NewPlanAwareScheduleTool(base tool.Tool, mgr port.ScheduleManager)`**
  — the AC4.3 plan-mode gate now also denies the `fire` of a MUTATING schedule
  (the plan-mode hard-deny on mutations: a `mutating: true` schedule's fire
  writes the workspace). It reads the schedule's create-time `Mutating` posture
  from the injected `mgr` (the fire verb carries no mutating flag of its own),
  so the constructor takes the manager as a second argument. The mutating
  `ScheduleTool` now carries ONLY the mutating verbs (list/inspect moved to
  `agent.ScheduleQueryTool`); its schema and unknown-verb message reflect that.
  Classified Changed per COMPATIBILITY.md (a widened constructor signature +
  narrowed verb surface; pre-v1 a minor bump). (schedule-tool plan, task 06
  repair)

### Added

- **`agent.NewPlanAwareScheduleTool`** (ADR 0073, the AC4.3 plan-mode gate) —
  wraps the MUTATING Schedule tool for a PLAN-MODE session's catalog. The default
  mutating tool reports `ReadOnly()==false`, so the plan-mode catalog projection
  (`engine/tool/catalog.go` `Available(ModePlan)`) would hide the WHOLE tool —
  including the read-leaning create plan mode must keep (a schedule CREATE does
  not itself mutate the workspace; the FIRE's posture is pinned at create-time
  by the Mutating/Mode invariant). The plan-aware variant reports
  `ReadOnly()==true` (so the projection advertises it) and hard-denies the
  mutating shapes (a `mutating: true` create; the fire of a mutating schedule)
  per call with the plan-mode deny reason BEFORE the base tool runs; the
  read-leaning create (`mutating:false`) and the fire of a read-leaning
  schedule drive through unchanged. Composition registers it only for a
  plan-mode session's catalog; every non-plan engine keeps the DEFAULT tool
  (the read/mutate serialization contract is unchanged). Classified Added per
  COMPATIBILITY.md (a new exported constructor is a minor bump). (schedule-tool
  plan, task 05; the fire-gate extension + manager arg landed in task 06 repair)

- **`port.ScheduleManager` + `port.ErrFireNowOverlap` + `agent.ScheduleTool`
  (ADR 0073, the model-facing Schedule tool)** — a new consumer-local
  `port.ScheduleManager` interface (the create/inspect/list/update/pause/
  resume/delete/fire/list-fires verbs the Schedule tools need, satisfied by
  composition with the server Service's schedule methods — the SAME validated
  create-seam the REST/gRPC handlers ride, never a second path), the
  port-level `ErrFireNowOverlap` sentinel a `FireNow` overlap returns (so a
  layer that may not import the scheduler adapter — the tool — matches the
  singleton rejection via `errors.Is`), and the MUTATING `agent.ScheduleTool`
  itself (`agent.NewScheduleTool(port.ScheduleManager)`, catalog name
  `agent.ScheduleToolName` = `"Schedule"`). The tool carries the mutating verbs
  (create/pause/resume/delete/fire) and reports `ReadOnly()==false` (every
  mutating call serialises so it never overlaps a sibling read); the read-only
  list/inspect verbs live on the `agent.ScheduleQueryTool` (task 06 repair).
  Classified Added per COMPATIBILITY.md (a new interface + sentinel + tool are
  a minor bump). (schedule-tool plan, task 01)

- **`session.StripProviderState`** — provider-neutral history for cross-provider
  model-switch carryover (minor): a pure function returning a copy of a message
  slice with every provider-private replay blob cleared (`Message.Reasoning`,
  `Message.ProviderPhase`, each `ToolCall.ItemID`), preserving the
  provider-neutral fields (`Role`, `Text`, `ToolCall` ID/Name/Args,
  `ToolResult` incl. its `Parts`, `Message.Parts`) verbatim. Both provider
  adapters treat an empty blob as "no blob" and omit it on the wire, so a
  stripped history replays safely to ANY provider — the new request's
  reasoning config is derived from the NEW model. Neither the input slice nor
  any shared `ToolCalls` backing array is mutated; nil/empty input passes
  through. Classified Added per COMPATIBILITY.md (a new exported function is a
  minor bump).

- **`session.PendingAsk.PlanOriginated` + `session.AskOrigin` enum +
  `session.PendingAsk.Origin()`** (issue #206, Wave 1) — a new serialized `bool`
  field (`json:"plan_originated,omitempty"`, sibling of `HookOriginated`) marking an
  ask that arose from the plan-approval gate (an operator was asked to approve a
  presented plan), NOT from the permission policy or a hook. It is CROSS-PROCESS
  LOAD-BEARING: the awaiting-resume path (`Engine.ResumeApproval` →
  `resolvePendingCall`) runs in a FRESH process and keys the plan-flip branch on it
  (an Allow flips the session out of plan mode and drives the turn through the
  completed path with `StopPlanApproved`). The two serialized bools
  (`HookOriginated`, `PlanOriginated`) remain the on-disk contract; the new
  `AskOrigin` enum (`AskOriginNone`/`AskOriginHook`/`AskOriginPlan`) and the
  `Origin()` read accessor are a convenience derivation over those bools, NOT a
  stored field (Hook takes precedence if both were incorrectly set).
  `ConfiguredAsk`/`FlooredConfiguredAllow` stay untouched (run-scoped, never
  serialized). Classified Added per COMPATIBILITY.md (new struct field + enum type +
  consts + method are minor bumps; the serialized two-bool contract is additive — a
  pre-#206 snapshot deserializes to `PlanOriginated=false`, `omitempty` keeps the
  key absent when false). (#206)

- **`session.StopPlanApproved`** (issue #206, Wave 1) — a new `StopReason = "plan_approved"`
  const, the CLEAN non-error terminal emitted when an operator approves a presented
  plan. Like `StopNoProgress` / `StopBudget` / `StopStructuredOutput` it is routed
  through the completed path (session ends COMPLETED, Reopen-recoverable), NOT
  `StopError`. It maps to the proto stop string verbatim (no proto enum; the wire
  stop field is a string passthrough, exactly like its clean-terminal siblings).
  Classified Added per COMPATIBILITY.md (a new exported const is a minor bump). (#206)

- **`agent.NewPresentPlanTool() tool.Tool`** (issue #206, Wave 1) — the
  plan-approval gate's signalling affordance. In plan mode, once the model has
  presented a complete plan in its preceding assistant text, it calls `PresentPlan`
  to hand control to the operator. The tool is read-only / signaling-only
  (`ReadOnly() == true`, dispatches read-parallel); `Execute` is VESTIGIAL — it
  returns `session.NewToolResult(call.ID, "PresentPlan: awaiting operator approval.")`
  and exists only for honesty on a misroute, since the dispatcher (Wave 2) intercepts
  a `PresentPlan` call by name in plan mode before execution. No marker interface is
  introduced (single implementation; the dispatcher name-checks `c.Name ==
  "PresentPlan"`). Classified Added per COMPATIBILITY.md (a new exported constructor
  is a minor bump; the `presentPlanTool` struct is unexported). (#206)

- **`agent.PlanApprovedProceedText`** (issue #206, Wave 2) — an exported `const string`
  ("Plan approved by operator. Proceed with execution.") carrying the harness-framed
  proceed message the composition/service layer (Wave 4's ApprovePlan seam) injects as
  ordinary recorded history when an operator approves a presented plan, signalling the
  model to begin execution. It is event-silent (a recorded user message, NOT a
  diagnostics line — the loop's "exactly THREE lines" invariant holds). Exported so the
  service layer references the exact text without re-stringing it; the text is a stable
  contract the model reads as the proceed signal. Classified Added per COMPATIBILITY.md
  (a new exported const is a minor bump). Wave 2 also lands the agent-loop
  plan-approval seam itself (the run-scoped `Run.planApprovedTarget` field, the
  `surfacePlanAsk` dispatcher sibling of `askHookApproval`, the `runReadBatch`/`runOne`
  name+mode interception, the `runLoop` `StopPlanApproved` termination, the
  `terminateComplete` mode flip, and the `resolvePendingCall` `PlanOriginated`
  cross-process resume branch) — all unexported, so no further API surface changes. (#206)

- **`tool.PlanOnly` interface** (issue #206, Wave 3) — a new OPTIONAL capability
  interface a `Tool` MAY implement to declare itself a plan-mode signalling tool:
  registered everywhere (so the shared and per-session catalog name-sets stay equal —
  guarded by `TestPerSessionCatalogMatchesSharedCatalog`) but advertised/callable
  ONLY in `ModePlan`. The catalog's mode projection (`Catalog.Available` / `Specs` /
  `AdvertisedSpecs`) EXCLUDES a `PlanOnly` tool from every non-plan mode, so it is
  never offered to the model in default/acceptEdits. This is the projection gate for
  `PresentPlan` (Wave 1): the dispatcher's name+mode check (`sess.Mode == ModePlan &&
  c.Name == "PresentPlan"`) is defense-in-depth ON TOP of this gate, not the sole
  gate. A tool that does NOT implement `PlanOnly` is advertised in every mode it is
  otherwise eligible for, so the default catalog view is unchanged for every
  non-plan-signalling tool. Classified Added per COMPATIBILITY.md (a new exported
  interface with a marker method is a minor bump; additive — existing tools are
  unaffected). (#206)

- **`agent.Deps.PlanModeAutoApprove`** (issue #206, Wave 6a) — a new `bool` field
  on `Deps` that, when true, loosens the `surfacePlanAsk` headless guard so a
  plan-approval ask (PresentPlan) is SURFACED (EvPermissionAsk emitted, run parks)
  even when the engine is headless (`Interactive=false`). This enables the
  composition layer's auto-approve observer (`Service.MaybeAutoApprovePlan`) to
  resolve the parked ask without a human operator. It is an OPT-IN, OPERATOR-TIER-
  ONLY, DEFAULT-OFF flag: when false (the default) the existing headless auto-deny
  is byte-identical. The engine NEVER auto-approves on its own — it only surfaces
  the ask; the composition Service layer delivers the verdict. Classified Added per
  COMPATIBILITY.md (a new exported struct field with a false zero-value is a minor
  bump; additive — existing code is unaffected). (#206)

- **`session.StopPlanIterate`** (issue #206) — a new `StopReason = "plan_iterate"`
  const, the CLEAN non-error terminal emitted when an operator chooses to iterate
  on a presented plan (a deny verdict). It is the iterate sibling of
  `StopPlanApproved`: a deny of a plan ask TERMINATES the plan run CLEANLY instead
  of continuing in-turn (the old behaviour kept the model iterating with NO
  operator input), so the run ends and the operator's next typed prompt drives the
  revision. Like `StopPlanApproved` it is routed through the completed path (session
  ends COMPLETED, Reopen-recoverable), NOT `StopError`, and the session stays
  `ModePlan` (no mode flip). It maps to the proto stop string verbatim (no proto
  enum; the wire stop field is a string passthrough, exactly like `StopPlanApproved`).
  Classified Added per COMPATIBILITY.md (a new exported const is a minor bump). (#206)

### Changed

- **`agent.RunModelRouter` + `agent.Deps.SubagentModelRouter` gain a miss-reason
  return** (issue #287) — both now return an extra `missReason string` before the
  terminal `ok bool`: `RunModelRouter` returns `(category string, usage
  session.Usage, missReason string, ok bool)` and the `Deps.SubagentModelRouter`
  closure type is `func(ctx, taskPrompt string) (category, model string, usage
  session.Usage, missReason string, ok bool)`. `missReason` is `""` on a hit and one
  of the new `RouterMiss*` constants on a miss, so the dispatch chokepoint can log a
  per-miss INFO naming WHY a plain delegation fell through to the inherited default
  model (metadata only — never the task prompt or classifier output; gauntlet #7).
  Classified Changed (breaking; pre-v1 minor bump) per COMPATIBILITY.md — a widened
  return signature. A composition consumer adds one return value at the
  `buildModelRouterTask` closure and the `RunModelRouter` call site. (#287)

### Added

- **`agent.WithRoutableAgents(names []string) SubagentOption`** — injects the
  composition-computed SET of agent-def names eligible for the OPT-IN model router
  (issue #286): a def that expressed NO model intent (absent `model:`) is CLASSIFIED and
  its scoped engine rebuilt on the routed model (via the agent+model factory), while a def
  that pinned ANY model (incl. explicit `inherit`) is honoured verbatim. nil/empty (the
  default) means no def routes — byte-identical to pre-#286. Layering-clean (def NAME
  strings only). Classified Added per COMPATIBILITY.md (a new exported option constructor is
  a minor bump; the `SubagentTool` struct is unexported). (#286)

- **`agent.WithWritableEngineFactory(f func(model string) (*Engine, bool))
  SubagentOption`** — injects the composition factory that mints a WRITABLE explorer
  child engine on a per-call OVERRIDE model for a mode:"read-write" Subagent call with
  no `agent` (issue #285), so a writable subagent honours the per-call `model` and the
  OPT-IN router pick instead of always running on its default model. nil (the default,
  and always on the no-FS path) leaves the writable-explorer-per-model path unwired (a
  read-write+`model` call then errors from validateMode — never a silent inherit).
  Classified Added per COMPATIBILITY.md (a new exported option constructor is a minor
  bump; the `SubagentTool` struct is unexported). (#285)

- **`agent.RouterMiss*` constants** — `RouterMissDegenerateInput`,
  `RouterMissClassifierError`, `RouterMissCancelled`, `RouterMissBadVerdict`, and
  `RouterMissUnknownCategory`: the string reason a model-router classification did
  not yield a routed model, returned by `RunModelRouter`/`Deps.SubagentModelRouter`
  (see Changed above). Classified Added per COMPATIBILITY.md (new exported
  identifiers are a minor bump). (#287)

### Fixed

- **`port.RouteToolResultParts` now drops empty-render text-summarised blocks**
  (behavioral fix, no API change). A text-summarised block (`BlockText`,
  `BlockStructuredContent`, `BlockEmbeddedResource`, `BlockResourceLink`) whose
  `session.ToolBlockText(b)` renders to the empty string is now dropped from the
  projection (image/audio gating is unchanged). If every block drops, the existing
  `len(out)==0 → nil` return routes the caller to the single-string `Content`
  fallback. WHY: strict providers reject an empty text content block on the wire —
  Moonshot via OpenRouter (POST /responses) 400s the whole request with "Invalid
  request: text content is empty", and the Anthropic Messages API rejects "text
  content blocks must be non-empty". Because the LLM adapters replay full history
  statelessly, a single empty-text tool-result block (e.g. an MCP fetch past the
  end of a document returning one empty text block) poisons every subsequent
  request and permanently bricks the session. The fix is request-time
  (serialization), never a history rewrite, so an already-poisoned persisted
  session heals on replay. No exported signature change, so `engine/api/*.txt` is
  unchanged. Classified as a behavioral bug fix per COMPATIBILITY.md (no
  guarded-surface change). CALLER CONTRACT: when the projection returns nil and a
  caller degrades to `tr.Content`, and that string is itself empty, the caller
  must substitute a non-empty deterministic placeholder — an empty string on the
  wire reproduces the strict-provider rejection this fix prevents. The provider
  adapters (`internal/adapter/openai`, `internal/adapter/anthropic`) carry matching
  belt-and-suspenders guards that substitute a deterministic
  `"(tool returned no output)"` placeholder for an empty single-string tool
  output.

## [0.4.0] - 2026-06-30

### Added

- **`session.Usage.ReasoningTokens`** — a new `int` field (after
  `CacheWriteTokens`) carrying the provider's reasoning-token spend for a model
  call. OpenAI surfaces `output_tokens_details.reasoning_tokens`; Anthropic
  surfaces `output_tokens_details.thinking_tokens`. It is a SUBSET of
  `OutputTokens` (providers bill reasoning as part of the inclusive output
  total), so `TotalTokens()` is UNCHANGED (`InputTokens + OutputTokens`) — the
  budget brake is not affected, and the field is additive observability only.
  Classified Added per COMPATIBILITY.md (a new struct field is a minor bump).
  (#213)

## [0.5.0] - 2026-07-16

### Added

- **`port.ScheduleSpec.OneShotRetry` + `OneShotMaxRetries` +
  `port.ScheduleState.OneShotRetryCount` + `port.ScheduleOneShotReArmer`** —
  an OPT-IN at-least-once retry for one-shot schedules that cannot tolerate
  the at-most-once crash-loss trade-off (ADR 0059 consequence). `OneShotRetry`
  (default false) enables the retry; `OneShotMaxRetries` (default 0 = off;
  the create-seam applies 3 when `OneShotRetry=true` and the field is 0)
  bounds the retry count; `OneShotRetryCount` is the durable counter. The
  optional `ScheduleOneShotReArmer` interface (type-asserted on the store,
  the same pattern as `PrunableStore`) provides the atomic re-arm
  (`ReArmOneShot` re-enables + advances `NextFireAt` + increments the count).
  Rejected on cron triggers (a cron self-heals via misfire). Classified Added
  per COMPATIBILITY.md (new struct fields + a new optional interface are a
  minor bump; `ScheduleStore` itself is unchanged). (#236)

- **`port.ScheduleSpec.CarryContext`** — an OPT-IN carried-context toggle
  (default false = fresh per fire, unchanged). When true, the fire path loads
  the prior fire's session and renders its conversation as a FENCED untrusted
  preamble prepended to the fire's prompt (via `agent.FenceUntrusted` +
  `NeutraliseFraming`), NOT as seeded history — carried context is untrusted
  (a prior fire may have been prompt-injected) and must not become live
  instructions. A re-armed one-shot does NOT carry context on the retry.
  Classified Added per COMPATIBILITY.md (a new struct field is a minor bump).
  (#236)

- **`port.MetaLister` + `port.SessionMeta`** — a new OPTIONAL cheap-listing seam
  a `SessionStore` adapter may additionally implement, discovered by type
  assertion (the same pattern as `PrunableStore`). `MetaList(ctx) ([]SessionMeta, error)`
  returns every stored session's picker metadata (id/state/turns/model id/title/
  created_at/last-modified) by reading ONLY the last snapshot line of each and
  decoding into a small struct that SKIPS the `messages` array — so listing N
  sessions is O(N × last-line-read) rather than O(N × filesize) for large
  histories. `Service.ListSessions` prefers `MetaLister` when the store
  implements it (jsonlstore does, via a tail-read `readLastLine` helper) and
  falls back to the Load-per-row path otherwise (memstore/redisstore/grpcdriver).
  `SessionMeta` is a port-owned struct (the server adapter references the shape
  without importing any concrete store). Classified Added per COMPATIBILITY.md
  (a new interface + struct are a minor bump; `SessionStore` itself is unchanged).
  (#245)

- **`session.Session.Title`** — a new `string` field (after `ReasoningEffort`)
  carrying a human-readable session label seeded ONCE from the first genuine
  user prompt, clamped to 120 runes (`maxTitleRunes`). It is set via the new
  `session.Session.SetTitle(text string)` (set-once: only the first non-empty
  prompt sticks; subsequent prompts do not overwrite), called by the loop from
  `recordPrompt` (the genuine prompt site) only — never the synthetic
  continuation/nudge site — so a no-progress nudge never seeds or overwrites
  the title. It is persisted in the snapshot (an inert stored label, like
  `Profile`; `omitempty` keeps a pre-Title snapshot decoding to `""` — additive,
  no format-tag bump) and projected on the wire (`Session.title`,
  `SessionSummary.title`). Two read-time consumers derive it lazily without
  write-on-read when empty: `ListSessions`/`GetSession` (the server lazy
  fallback, `deriveTitle`) and the event-sourced `eventsource.Fold`. The new
  `session.IsGenuineUserPrompt(m Message) bool`,
  `session.IsSynthesisedSummary(text string) bool`, and
  `session.ClampTitle(s string) string` (plus the exported markers
  `session.CompactionSummaryMarker` / `session.Tier4SummaryMarker`, promoted
  from the unexported `engine/agent` constants) back those consumers — the
  domain leaf owns the genuine-vs-synthesised distinction it needs without
  importing `engine/agent`/`engine/prompt`. `IsGenuineUserPrompt` deliberately
  does NOT check `prompt.IsInjectedTurn0Fragment` (ADR 0043 makes turn-0
  fragments ephemeral, so they never appear in persisted history); the loop's
  own `isGenuineUserTurn` keeps that arm as defense-in-depth and delegates its
  synthesised-summary arm to the new domain export. Classified Added per
  COMPATIBILITY.md (new struct field + methods + consts are a minor bump). (#247)

- **scheduled-tasks Phase 2a: `port.ScheduleStore.ClaimNow`.** A new
  `ClaimNow(ctx, name, now, nextFire) (Schedule, error)` method — the FireNow
  primitive. It performs the SAME atomic advance as `Claim` (LastFireAt=now,
  NextFireAt=nextFire, FireCount++, LastFireSessionID=PendingFireSessionID,
  disable on zero nextFire) but does NOT enforce the `NextFireAt <= now`
  due-check — it claims the slot regardless of whether it is due (a manual
  trigger bypasses the cadence but still claims atomically for at-most-once).
  The `Enabled` + `MaxFires` checks STILL apply (a disabled or exhausted
  schedule cannot be force-fired). The at-most-once fence (no due-check) is
  `LastFireAt == now`: a second `ClaimNow` at the same `now` is rejected (the
  advance already happened), and a later `ClaimNow` at a new `now` succeeds
  (crash-recoverable — a hard crash between ClaimNow and RecordFire does not
  wedge the schedule, unlike a pending-sentinel fence). The tick loop's
  `fireOne` KEEPS using `Claim` (the due-check is correct for the poll loop);
  only `FireNow` uses `ClaimNow`. Classified Added per COMPATIBILITY.md (a new
  interface method is a minor bump). (#189)

- **scheduled-tasks Phase 2a: `port.ScheduleStore.SetEnabled`.** A new
  `SetEnabled(ctx, name, enabled) error` method — the pause/resume primitive.
  It atomically sets the schedule's `Enabled` flag WITHOUT touching any other
  State field, which `Save` CANNOT do: `Save` preserves the existing State half
  on a Spec overwrite (so a Load→set Enabled→Save is inert, the Enabled flip
  does not persist). `PauseSchedule` sets `Enabled=false`; `ResumeSchedule`
  sets `Enabled=true`. The not-found case wraps `ErrScheduleNotFound`.
  Classified Added per COMPATIBILITY.md (a new interface method is a minor
  bump). (#232)

- **scheduled-tasks Phase 2a: `port.ScheduleStore.ListFires`.** A new
  `ListFires(ctx, scheduleName) ([]ScheduleFire, error)` method — the list
  companion to `LoadFire`. It returns the fire records for a schedule, in no
  guaranteed order; the not-found case for the SCHEDULE wraps
  `ErrScheduleNotFound`, and an empty fire list for an existing schedule is a
  successful empty slice (not an error). Classified Added per COMPATIBILITY.md
  (a new interface method is a minor bump). (#232)

- **scheduled-tasks Phase 2a: `session.EvScheduleFired` / `EvScheduleSkipped` /
  `EvScheduleFailed` events + `session.SchedulePayload`.** Three new
  `EventType` string consts (`schedule.fired` / `schedule.skipped` /
  `schedule.failed`) and a new `SchedulePayload` value object
  (`ScheduleName`/`FireID`/`SessionID`/`Kind`/`Stop`/`Err`), plus a new
  `Event.Schedule *SchedulePayload` field. They are the CLIENT-VISIBLE
  scheduler-lifecycle projection (unlike the log-only `EvApproval` /
  `EvCompactionArchive` / `EvUserPrompt`), emitted from COMPOSITION (the
  scheduler) at fire time — NOT the agent loop (`engine/agent` never imports
  `port.ScheduleStore`). `Kind`/`Stop`/`Err` are STRING passthroughs (the
  `EvNoProgress`/`StopBudget` discipline — no proto enum). Classified Added per
  COMPATIBILITY.md (new exported consts + a new struct field are minor bumps).
  (#232)

- **`port.ScheduleStore` + value objects** (`Schedule`, `ScheduleSpec`, `TriggerSpec`,
  `ScheduleState`, `ScheduleFire`, `ScheduleProviderSelector`, `ErrScheduleNotFound`,
  `ErrScheduleUnsupported`, `SchedulerLeaderLeaseID`, `TriggerKind`/`TriggerCron`/
  `TriggerOneShot`/`TriggerNone`, `MisfirePolicy`/`MisfireFireOnceNow`/`MisfireSkip`,
  `PendingFireSessionID`).
  A new OPTIONAL durable schedule registry port — a peer of `port.SessionLease` /
  `port.EventLog` — defining the durable registry for the scheduled-tasks feature
  (issue #189, Phase 1a: contract + value objects, no implementation yet). The port
  carries the FULL at-most-once claim-before-fire contract in its doc-comments:
  `Claim` is the atomic advance (NextFireAt + LastFireAt + FireCount + a
  sentinel-pending LastFireSessionID) that gives exactly-once firing across
  replicas; the store is parser-free (the caller computes the next cron fire);
  `engine/agent` NEVER imports this port — the tick loop, cron parsing, misfire
  policy, and leader-lease acquisition all live in composition. `TriggerSpec`
  carries an XOR (Cron | OneShot) with a `Validate()` + `Kind()`; `MisfirePolicy`
  is the fire-once-now (default) / skip enum; `SchedulerLeaderLeaseID` is the
  well-known leader-lease id; `PendingFireSessionID` is the single-source sentinel
  string a `Claim` stamps onto `LastFireSessionID` and the scheduler reads back.
  No implementation ships yet — adapters are a later
  phase. Classified Added per COMPATIBILITY.md (new exported identifiers in
  `engine/port`). (#189)
- **`port.ScheduleSpec.Trigger` field added** (`port.TriggerSpec`). Phase 1a
  landed `TriggerSpec` (the Cron XOR OneShot sum type) but did not wire it onto
  `ScheduleSpec`; Phase 1b's conformance suite + reference adapter need the
  field to save/load schedules with a trigger. The field is the sole carrier of
  the firing trigger on a spec (there is no top-level Cron/OneShot). Classified
  Added per COMPATIBILITY.md (a new struct field in a pre-v1 port value object
  is a minor addition; the zero `TriggerSpec` is `TriggerNone`, which the
  create-seam's `Validate` rejects fail-closed — no schedule can be saved with
  an unset trigger). (#189)

- **`session.ToolResult.Parts`** — a new `[]Content` field (additive; zero-value
  = string-only, byte-identical to the pre-#223 shape) carrying typed tool-result
  blocks (text/image/audio/resource-link/embedded-resource/structured-content)
  alongside the legacy model-facing `Content` string. The 2-arg
  `NewToolResult`/`NewToolError` constructors are preserved (~200 call sites); the
  new path uses `NewToolResultWithParts`. Consumers prefer `Parts` when non-empty,
  falling back to `Content`. Typed content rides `EvToolResult.ToolResult`, so
  `engine/adapter/eventsource` (`Fold`) reconstructs it from the durable log with
  no relay sidecar. Classified Added per COMPATIBILITY.md (a new struct field is a
  minor bump). (#223, [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`session.Content` block-kind generalization** — the existing `Content` gains a
  `BlockKind` discriminator plus block variants (`BlockText`/`BlockImage`/
  `BlockAudio`/`BlockResourceLink`/`BlockEmbeddedResource`/`BlockStructuredContent`)
  and validating constructors (`NewContent`/`ValidateMediaParts`, the SINGLE choke
  point the ACP adapter already uses for prompt media — no second validation path).
  A legacy media part (`BlockKind == ""`, the user-message media shape) is distinct
  from a tool-result block. Classified Added per COMPATIBILITY.md (additive fields
  + consts + constructors). (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`port.RouteToolResultParts`** — a pure composition-driven projection that
  returns the capability-gated subset of a recorded `session.ToolResult.Parts`
  for the model-facing request: image blocks survive iff `caps.Image`, audio
  blocks iff `caps.Audio`, and text / resource-link / embedded-resource /
  structured-content blocks always survive. It lives in `engine/port` so the
  provider adapters (T7) can call it from their `RoleTool` case without importing
  composition, and so composition can pass the SINGLE computed
  `port.ProviderCapabilities` intersection (catalog ∩ adapter). It is a
  READ-ONLY PROJECTION: it builds a fresh slice and never mutates the recorded
  `*session.ToolResult`, preserving the recorded-history == client-stream ==
  model-view guarantee. `Content.Audience` (untrusted server self-attestation,
  CWE-345) is advisory display routing ONLY and is NOT consulted — a
  `["user"]`-audience block still passes to the model. Nil/empty `Parts` (or a
  projection that drops every block) returns nil so the caller degrades to the
  recorded model-facing `Content` string. Classified Added per COMPATIBILITY.md
  (a new exported function is a minor bump). (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`FetchMcpResource` tool** (registered in `internal/adapter/tools`, NOT an
  `engine/` exported API — recorded here for completeness) — the model-facing
  affordance to fetch the contents of an `https://` `resource_link` URI an MCP
  tool result surfaced as a typed block. Only `https://` is client-fetched,
  re-validated for SSRF on every redirect via `session.ValidateMediaURL` (no
  internal/private/metadata IPs, no cross-origin credentials); non-`https` schemes
  stay server-readonly via `ReadMcpResource`. Output is capped at
  `toolkit.MaxOutputBytes`; binary content is summarized. The model sees the
  `resource_link` REFERENCE, never auto-fetched raw bytes. No `engine/` exported
  surface change. (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **Wire: `ContentBlock` proto message + `ToolResult.blocks`/`structured_content`
  fields.** The gRPC `ToolResult` proto mirrors the additive domain `Parts` field
  as `blocks` (`repeated ContentBlock`) plus a `structured_content` scalar; the
  `ContentBlock` message carries the block-kind discriminator and variant fields.
  Additive — legacy clients/sessions round-trip with empty `blocks`. (This is a
  `contracts/gen` wire change, not an `engine/` exported-API change; recorded
  here as the wire half of #223.) (#223,
  [ADR 0078](../docs/adr/0078-mcp-typed-tool-results.md))

- **`session.ValidateResolvedIP`** — a new exported func
  (`func ValidateResolvedIP(ip net.IP) error`) re-exporting the dial-layer SSRF
  IP predicate (`isGlobalUnicast`, the same one `ValidateMediaURL` uses for
  literal-IP hosts) as the single screening definition shared between the
  URL-string path and the dial-IP path. It returns nil for a routable public IP
  and a non-nil error naming the rejection for internal IPs (loopback/private/
  link-local/CGNAT/metadata/unspecified/multicast). It is the dial-layer backstop
  a harness fetch tool (`FetchMcpResource`) installs as its `http.Transport`'s
  `DialContext` to close the DNS-rebinding window `ValidateMediaURL`'s hostname
  check cannot (an attacker-controlled resolver answers the hostname check with a
  public IP, then returns 169.254.169.254 at dial time). Classified Added per
  COMPATIBILITY.md (a new exported function is a minor bump). (#223)

- **`session.ToolBlockText`** — a new exported func
  (`func ToolBlockText(b Content) string`) extracted from the byte-identical
  `toolBlockText` helper duplicated in `internal/adapter/openai/request.go` and
  `internal/adapter/anthropic/request.go`. It renders a non-image tool-result
  block to its model-facing text form (resource-link URI+name/title, an
  embedded-resource pointer when no inline `Text`, else `Text` verbatim). Both
  adapters now call the shared domain projection instead of carrying their own
  copy, removing a drift risk. Classified Added per COMPATIBILITY.md (a new
  exported function is a minor bump). (PR #226 review)

### Notes

- **`robfig/cron/v3` added to the engine dep closure (parser-only).** The
  scheduled-tasks feature (issue #189, Phase 1c) needs a cron → next-fire
  helper at the composition layer: `TriggerSpec.Cron` (Phase 1a) is the raw
  expression the durable `ScheduleStore` stores verbatim and never interprets,
  and the caller computes the next fire to hand to `Claim`. Per decision #6 the
  durable store is ground truth and the in-memory timer is a derived lookahead,
  so mecatl uses `robfig/cron/v3`'s PARSER only (`cron.ParseStandard` +
  `Schedule.Next`) — NOT the `cron.New()` daemon. The dependency is a single
  module with a zero-dependency `go.mod`, mirroring how `doublestar` and
  `x/sync` already travel with the importable core (ADR 0036); the
  `task test:engine-standalone` hygiene proof confirms the standalone closure
  stays self-contained. The helper itself (`engine/adapter/cronparse.NextFire`)
  lives under `engine/adapter/*`, which COMPATIBILITY.md EXCLUDES from the
  stability surface (like `memlease.New` / `eventsource.Fold`), so it is NOT in
  `engine/api/*.txt` and is NOT an api-compat-gated addition. No new exported
  core symbol; no public-API change. (#189)

## [0.3.0] - 2026-06-30

### Added

- **Guardrail approve-once seam (ADR 0062).** Three additive surfaces let a
  PreToolUse hook BLOCK be refined into an interactive approval ask (reusing the
  existing permission-ask machinery) instead of a permanent dead-end:
  - **`governance.HookOutcome.AskApproval`** — a new `bool` field (after
    `Mutated`). Meaningful only on a PreToolUse outcome with `Block==true`: an
    interactive engine (`Deps.Interactive`) surfaces the block as a permission ask
    (PauseForApproval → StateAwaiting → EvPermissionAsk → Approve); a headless
    engine IGNORES it and the block stands (byte-identical fail-safe).
  - **`session.PendingAsk.HookOriginated`** — a new `bool` field
    (`json:"hook_originated,omitempty"`, SERIALIZED). Marks an ask that arose from
    a hook block; the cross-process awaiting-resume path keys on it to execute the
    approved call WITHOUT re-running the PreToolUse hook (which would re-block).
  - **`port.HookApprovalLearner`** — a new OPTIONAL interface
    (`LearnHookApproval(ctx, governance.HookEvent)`). The engine type-asserts it on
    `Deps.Hooks` and calls it ONLY on a `VerdictAllowAlways` verdict for a
    hook-originated ask, so a consumer (the guardrails adapter) can arm a
    session-scoped "Allow & don't ask again" waiver. No method was added to
    `HookRunner` (that would be breaking).

  All three are classified Added per COMPATIBILITY.md (a new struct field is a
  minor bump; a new optional interface is a minor bump). The engine stays generic —
  the new surfaces carry NO guardrail vocabulary. See
  [ADR 0062](../docs/adr/0062-guardrails-approve-once.md). (guardrail-approve-once)

## [0.2.0] - 2026-06-25

### Added

- **`session.Session.ReasoningEffort`.** A new write-once opaque creation label
  on the `Session` aggregate (a neutral reasoning-effort token, `""` = unset),
  sitting next to `Profile`/`ProviderID`/`ModelID` and carrying the same
  inert-label posture (the domain stores it but never interprets it — the neutral
  vocabulary, normalisation, per-provider clamp, and adapter re-mint all live in
  composition). It is round-tripped by the `sessnap` snapshot and the
  `eventsource` fold so a restarted process re-mints the same-effort per-session
  engine. Classified Added per COMPATIBILITY.md (a new exported struct field).
  See [ADR 0055](../docs/adr/0055-reasoning-effort.md). (reasoning-effort)

- **`session.HookAdvisory`.** A new `HookDecision` value (`"advisory"`) for an
  `EvHook` carrying an advisory guardrail finding — client-visible (rendered as
  a warning notice), model-invisible (the tool result is byte-unchanged). The
  advisory arm of `modelhook.enforce` now returns a `HookOutcome{Message:...}`
  (instead of an empty outcome), and dispatch recognises the
  `Message!="" && !Block && len(Mutated)==0` shape as an advisory outcome,
  emitting an `EvHook` with `HookAdvisory`. Classified Added per
  COMPATIBILITY.md (a new exported const + a new wire `HookDecision` enum
  value). (#170)

- **`agent.WithAgentModelEngineFactory`.** A new `SubagentOption` injecting a
  composition-supplied factory `func(agentName, model string) (*Engine, bool)`
  that rebuilds a named specialist's scoped engine on a per-call override model,
  so a Subagent call may now set BOTH `agent` and `model` (previously rejected).
  The override model runs on the def's resolved provider; the specialist's
  catalog/prompt/skills/hooks/memory are preserved (NOT the generic explorer
  set); the provider-closing Deps are re-derived for the override model via the
  contamination-safe per-provider path. A def with inline MCP servers is
  declined on the agent+model path (v1 scope limit); reference-only MCP is
  supported. Classified Added per COMPATIBILITY.md (a new exported Option).
- **`governance.BashCommandFromArgs`.** A new exported helper
  `func(args json.RawMessage) (string, bool)` extracting the Bash tool-call command
  string from its args JSON (the `command`/`cmd` field pair), with fail-safe
  semantics: a parse error or a missing/whitespace-only command returns `("", false)`
  so a caller gating a safety decision INSPECTS rather than skips. It consolidates
  three prior private copies (`governance.bashCommand`, `agent.bashCmdFromArgs`,
  `modelhook.bashCmdFromArgs`) into the single source of truth for the Bash args
  schema, so a schema change lands in one place. The two agent/modelhook wrappers
  now delegate to it. Classified Added per COMPATIBILITY.md (a new exported func).

- **`agent.WithAgentWritableEngineFactory`.** A new `SubagentOption` injecting a
  composition-supplied factory `func(agentName string) (*Engine, bool)` that rebuilds
  a named specialist's scoped engine WRITABLE (`allowMutating=true` — Edit/Write survive
  scoping) on the def's resolved provider/model, using the MAIN session's command runner
  (direct-write parity, ADR 0077/0058). A Subagent call may now set BOTH `mode:"read-write"`
  and `agent` (previously rejected); the specialist runs with its prompt/skills/catalog +
  Edit/Write against the real parent workspace, dispatch-serial via `MutatesParent`,
  `isolated:false` (A2 auto-approve does not apply). A def with inline MCP servers is
  declined on the writable-agent path (v1 scope limit); reference-only MCP is supported.
  `mode:"read-write"`+`agent`+`model` (all three) stays rejected (v1 scope limit — the
  writable specialist runs on its own resolved model). Classified Added per
  COMPATIBILITY.md (a new exported Option). (#204)

### Changed

- **`tool.MaxAgentDescriptionBytes` raised 800 → 2000; `tool.MaxAgentBodyBytes`
  raised 8192 → 32768 (32 KiB).** The two agent-def caps got more headroom, with an
  asymmetric rationale: the DESCRIPTION rides `Subagent`'s `Spec().Description` on
  every request, summed across all registered agents, and is part of the byte-stable
  prompt-cache prefix, so it stays conservative (2000 B); the BODY is in-context only
  for that one specialist engine's own turns, so it can afford 32 KiB. The constant
  identifiers and types are unchanged — only their VALUES move. Classified Changed
  per COMPATIBILITY.md (an exported const value is part of the API snapshot): an
  external consumer relying on the exact old numbers, or on the old truncation point,
  must re-baseline. Discovery stays deterministic; the only runtime effect is a
  one-time prompt-cache-prefix invalidation. (#156)

## [0.1.0] - 2026-06-22

### Changed

- **Behaviour (no API change): turn-0 instruction fragments are now EPHEMERAL.** The
  soul / project-instruction / memory-index / user-model fragments produced by the
  `prompt.InstructionAssembler` chain are no longer persisted into
  `session.Conversation.Messages` at turn 0. The agent loop now assembles them ONCE per
  run and PREPENDS them to the per-request `port.LLMRequest.Messages` on every turn
  (including resume), never writing them into the conversation, event-carrying them, or
  snapshotting them. The genuine user prompt is still recorded and event-carried
  unchanged. This fixes resume-time history bloat (resumed runs no longer re-append the
  fragments), keeps the persisted conversation clean (so compaction anchors on the
  genuine first instruction), and converges the snapshot + `eventsource.Fold`
  rehydration paths fragment-free. No exported surface changes:
  `RecordUserPromptWithParts` keeps its `instr` parameter (now called with nil) and
  `prompt.IsInjectedTurn0Fragment` is retained (defense-in-depth for legacy persisted
  history). See `docs/adr/0043-ephemeral-turn0-instruction-fragments.md`.

### Added

- `session.PendingAsk.Call` (`session.ToolCallID`) — the opaque gated tool-call id on
  the ask REQUEST half, mirroring `session.ApprovalPayload.Call` on the verdict half.
  It round-trips in the sessnap snapshot, giving a host durable, grammar-free
  correlation of a pending ask back to its tool call (no askID grammar parse). It is
  an opaque identifier, not secret content (it is already implicitly encoded inside the
  askID), so surfacing it opens no new leak surface. (#148)
- `agent.RunOptions.AskIDDiscriminator` (`string`) — an opt-in, host-supplied trailing
  askID component that REPLACES the process-global `"r<serial>"` suffix, making the
  askID `"<sessionID>:<n>:<callID>:<discriminator>"` reconstructable across processes
  from persisted state. The host contract requires it to be unique-per-attempt,
  stable-per-attempt-across-processes, and colon-free (a colon-containing value is
  ignored with a WARN and falls back to the serial). Empty (the zero value) preserves
  the `"r<serial>"` fallback with no behaviour change. See
  `docs/adr/0044-host-supplied-askid-discriminator.md`. (ADR-0044, #117)
- `prompt.IsInjectedTurn0Fragment(text string) bool` — reports whether a string is
  the body of a harness-injected turn-0 context fragment (project instructions /
  soul / memory index / user model) rather than a genuine user instruction. The four
  turn-0 `InstructionAssembler`s record their output as `RoleUser` messages, so a
  consumer that must anchor on "the user's genuine first instruction" (the compaction
  first-user pin; the resume re-injection guard) calls this to skip them. It
  recognises each fragment by the header its renderer prepends (the shared
  source-of-truth constants in `engine/prompt/turn0.go`), so a header reword is
  reflected automatically. Additive function in `engine/prompt`. See ADR 0012.
- `prompt.DefaultTone() string` — returns the built-in tone/style block `Build`
  uses when `Config.Tone` is empty. Mirrors `DefaultRole()`; lets the composition
  layer compose an output-economy tier delta (ADR 0041: the "terse" posture
  appends an answer-length clause) onto the SAME default tone the prompt package
  uses, rather than carrying a private verbatim copy that could silently diverge.
  Additive; no frozen domain type changes.

### Removed

- **BREAKING:** `agent.WithWritableChildForker(f tool.WorkspaceForker) SubagentOption`
  and `agent.WithSubagentAutoMerge(m tool.ForkMerger) SubagentOption` — removed. The
  writable Subagent (`mode:"read-write"`) no longer forks the workspace or merges a
  diff back: it now writes DIRECTLY to the real parent workspace, exactly as the main
  agent does, and git is the rollback layer (ADR 0077, which supersedes the
  writable-subagent decision in ADR 0040). `agent.WithWritableChildEngine` is RETAINED
  (a read-write call still selects a separate Edit/Write-bearing engine); it now runs
  against the parent workspace with the main session's command runner, no forker, no
  merger. The dispatcher still runs a read-write call mutate-serial — `SubagentTool.
  MutatesParent` is now decoupled from any merger (true whenever the writable engine is
  wired and the call is `mode:"read-write"`). Composition no longer wires a forker or
  merger into the Subagent tool; the shared `tool.ForkMerger` remains for Parallel's
  single-branch auto-merge only. See `docs/adr/0077-direct-write-subagent.md`.

## [0.0.4] - 2026-06-21

### Added

- `tool.ForkMerger` — an OPTIONAL port (`Merge(ctx, forkRoot, parentWS) error`)
  by which a preserved winning fork's changes are merged BACK into the parent
  workspace. Additive interface in `engine/tool` (next to `WorkspaceForker`); no
  frozen domain type changes. See `docs/adr/0039-parallel-auto-merge.md`.
- `agent.WithAutoMerge(m tool.ForkMerger) ParallelOption` — wires a merger into
  the Parallel tool. When set AND a `join=first` or `join=judge` run has exactly
  one branch with a successful winner, the winner's diff is auto-merged into the
  parent workspace after the run. nil (the default for the option itself) keeps
  the no-auto-merge behaviour for that tool instance; composition wires a merger
  unconditionally (default-on — see `docs/adr/0039-parallel-auto-merge.md`).
  Multi-branch runs never auto-merge. The merge is a POST-RUN step, so
  `ParallelTool.ReadOnly()` stays `true`. On a conflict the tool returns an error
  naming the conflict + the preserved fork path; it never forces. The `forker.Merger`
  adapter runs `git diff --no-textconv` and refuses `.gitattributes`-touching
  patches (closes attacker-named `diff.*.textconv`/`filter.*.smudge` RCE from an
  untrusted fork `.git`). See `docs/adr/0039-parallel-auto-merge.md`.
- `agent.WithWritableChildEngine(e *Engine) SubagentOption`,
  `agent.WithWritableChildForker(f tool.WorkspaceForker) SubagentOption`, and
  `agent.WithSubagentAutoMerge(m tool.ForkMerger) SubagentOption` — wire the
  WRITABLE-subagent path (the `mode:"read-write"` Subagent arg). A read-write call
  runs the writable child engine (an Edit/Write-bearing explorer) in a force-copy
  fork; its working-tree diff is merged back into the parent workspace after the
  run via the injected merger (the SAME composition-owned, process-wide serialized
  `tool.ForkMerger` Parallel's `WithAutoMerge` uses). The merge is a POST-RUN step,
  so `SubagentTool.ReadOnly()` stays `true`. On a conflict the tool returns an error
  naming the preserved fork and does not force; nil options leave the writable path
  unwired (a `mode:"read-write"` arg then surfaces a model-addressable "not
  supported" error). `mode:"read-write"` is rejected with `background`/`agent` and
  composes with `fork`/`model`/`resume`/`output_schema`.
- `agent.(*SubagentTool).MutatesParent(call session.ToolCall) bool` and
  `agent.(*ParallelTool).MutatesParent(call session.ToolCall) bool` — implement an
  internal optional `parentMutatingCaller` seam the dispatcher consults: a `ReadOnly()`
  tool stays read-only for fan-out, but a CALL that will merge a fork diff back into
  the parent workspace (a `mode:"read-write"` Subagent, or a single-branch
  `join=first`/`judge` auto-merging Parallel) reports `true` and is excluded from the
  concurrent read batch (dispatch-serial), so its post-run merge never overlaps a
  sibling parent read. Returns `false` for read-only fan-out and for malformed args.

## [0.0.3] - 2026-06-21

### Hygiene

- `go.mod`: the `go` directive is now the minor version `go 1.26`, not the patch
  `go 1.26.4`. A library's `go` directive sets the language version it requires,
  and Go raises a consumer's own directive to match the highest one in its module
  graph — so a patch-level directive forces every consumer to a patch directive
  too. The engine uses no Go 1.26.4-specific language feature, so `go 1.26` is the
  correct floor. This unblocks consumers (e.g. a downstream consumer) whose CI forbids a
  patch-level `go` directive. No public-API change. (A `toolchain` directive, if
  ever added, may stay patch-pinned — only the `go` language directive must be
  minor.)

## [0.0.2] - 2026-06-20

### Added

- `prompt`: `Builder` func type (`func(Config) Layered`) — the system-prompt
  assembly seam. `agent.Deps.PromptBuilder` (optional; nil → `prompt.Build`,
  byte-identical to v0.0.1) lets a host embedding the engine for a non-coding
  agent fully own the system prompt (role, tone, safety, tool inventory) with no
  coding-agent defaults and no `Available tools:` block. Only the MAIN loop's
  `buildRequest` routes through it; the compaction summarizer (`cascade.go`)
  builds its own `prompt.Layered` directly and is unaffected. (#127)

## [0.0.1] - 2026-06-19

This is the **initial baseline** (tagged `engine/v0.0.1`). The entries below record the establishment of
the module and its compatibility contract, not a change to a previously-published
surface.

### Added

- The engine became its own Go module, `github.com/stacklok/mecatl/engine`,
  importable independently of the host repo (monorepo via `go.work`). (#113,
  [ADR 0036](../docs/adr/0036-engine-module.md))
- A public API stability contract for the seven core packages: this CHANGELOG,
  [COMPATIBILITY.md](./COMPATIBILITY.md), the committed text snapshots under
  [`engine/api/`](./api/), and the `api-compat` freshness gate that fails CI on
  an unflagged change to the exported surface. (#114,
  [ADR 0037](../docs/adr/0037-engine-stability-contract.md))
- `engine/adapter/eventsource` reference fold (event-sourced `SessionStore.Load`
  rehydration): `Fold` reconstructs a `*session.Session` from a `port.EventLog`
  stream plus out-of-band creation metadata (`SessionMeta`), for hosts whose system
  of record is an append-only event log. It is an EXCLUDED reference adapter (no
  guarded-surface change) and ships with the documented reconstruction contract in
  [COMPATIBILITY.md](./COMPATIBILITY.md) (the only residual limitation is the
  provider-private `Reasoning`/`ProviderPhase`/`ItemID` replay fields). (#115,
  [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md))
- `session`: `EvUserPrompt` event (+ `UserPromptPayload` + `Event.UserPrompt`). The
  durable event log now records the user-role messages the loop adds — the genuine
  client prompt and the harness-authored synthetic continuations (nudges/notices) —
  so an event-sourced fold reconstructs user-role turns (closing the "the log can't
  show what the user asked" gap, ADR 0027 row 11). It is LOG-ONLY: the relay appends
  it and skips it on the live client wire (the EvApproval/EvCompactionArchive
  precedent; wire `type` string passthrough, no proto enum). (#115,
  [ADR 0038](../docs/adr/0038-event-sourced-rehydration.md))

### Hygiene

- CI now runs `govulncheck` on the engine module (a reachable-vulnerability scan
  of its own dependency closure, separate from the root module's), and
  `.github/dependabot.yml` keeps the engine `go.mod` current — supply-chain
  hygiene for the engine library's consumers. No public-API change. (#118)
