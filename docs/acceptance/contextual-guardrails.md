# Contextual investigative guardrails — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes guardrail authority, dispatch ordering, public engine/wire surfaces, operator configuration, and main/worker invariants.
**Decision record:** [ADR 0334](../adr/0334-contextual-investigative-guardrails.md)
**Phase:** live contextual action and inbound-content review; cross-process held-result recovery and wider cloud-native security state are deferred
**Status:** proposed, 2026-09-14. Product choices are approved. Implementation must build and validate the quality-measurement and capacity-calibration deliverables before any production-readiness claim; neither requires results before implementation.
**Delivery:** Split. Merge of a proposed Plan / Interface PR is the implementation authorization; that gate has not been waived.
**Expected tasks:** deferred to orchestration

This is a plan/interface deliverable only. It does not claim the behavior is shipped and does not authorize production edits, live model calls, commits, or publication.

## Human decisions

- [x] **Live approvals:** preserve Run once, Don't ask again for this action in this session, and Cancel; include the narrow approval-provenance/replay correctness fix without expanding into the wider cloud-native effort. — Decision: The operator approved retaining convenient repeat approvals and fixing their semantics underneath. A guardrail waiver must never become a broader permission grant, including after restart. Changes to the command, target, or relevant script invalidate the prior approval.
- [x] **Effective-mutation ordering:** check the exact final action against permissions and guardrails without redundant approval for unchanged calls. — Decision: The operator approved running trusted argument mutation once and, when arguments change, re-evaluating the ordinary permission policy and existing system-scope check on the exact effective call before contextual review. Deny/Ask/plan semantics remain authoritative; a changed call prompts only when its resulting action requires approval. Do not run mutation again after a live approval.
- [x] **Remove sanitization:** remove the sanitize implementation, configuration option, and current-behavior documentation outright; retain block and advisory. — Decision: The operator confirmed sanitize has no users and approved its removal as a breaking change, with no migration path, compatibility mode, or deprecation period. Ordinary strict configuration validation suffices for unsupported modes; do not silently map sanitize to another mode. Reviewer-proposed replacement actions are never automatically executed.
- [x] **Reviewer replacement:** replace the existing reviewer outright with the contextual investigative reviewer. — Decision: The operator explicitly rejected a legacy implementation or mode. Keep one reviewer, no `guardrails.review: legacy|contextual` selector, compatibility implementation, or deprecation machinery. Guardrails remain off unless enabled.
- [x] **Failure/default mapping:** separate checker-failure behavior from unsafe-finding behavior. — Decision: The operator accepted bounded recovery for operational checker failures, followed by human approval when available or blocking the current action unattended. Operators may explicitly select continue-with-warning on checker failure; advisory remains non-enforcing and reports inspection failure. Recovery stays within the approved per-review limits, never an unbounded retry loop. No fallback provider is implicitly authorized: an alternate checker must be explicitly configured and admitted for disclosure. These choices never override deterministic permission denials or authorize access to forbidden evidence, and operational failure is never presented as proof of an unsafe action.
- [x] **Default coverage:** enforce checks on commands, file changes, external requests and delegation before execution, and on web/MCP, Shell and local file/search results before delivery; apply the same applicable rules to workers. — Decision: The operator selected option 2 for local content: hold flagged local file, directory-listing and search output until a human approves release of that exact result once, matching the agreed web/MCP/Shell release flow. Do not use advisory-only defaults for local results. Guardrails remain opt-in; explicit advisory mode remains available. Ordinary issue requirements, admitted project instructions and quoted attack examples must pass without findings merely for containing instructions. All expanded enforcement is subject to the agreed false-positive and attack-detection quality gate.
- [x] **False-positive discipline:** source-aware and task-aware discrimination is a core requirement, not optional UX polish. — Decision: The operator identified excessive false positives on ordinary GitHub issues and legitimate CLAUDE.md instructions as a reason users would disable guardrails, and required the prior-art research to improve prompts and protections rather than merely expand a naive classifier. The contract must distinguish expected task requirements, authorized project instructions, quoted attack examples, and concrete attempts to cross an authority boundary. No blanket filename/domain exemption; provenance and project admission come from the harness. Require paired benign/adversarial cases and measurement of both false warnings and false blocks. The operator subsequently approved making demonstrated discrimination a shipping criterion: instructions, imperative language, assistant-directed prose, or quoted attack examples alone must not trigger findings; a finding identifies concrete attempted redirection and the authority boundary crossed. Evaluate the actual checker model for false warnings, false blocks, missed attacks, latency, and cost before approving broader enforcement; advisory spam is not an acceptable substitute. Offline mocks prove wiring only, not checker quality; real-model evaluation requires separate explicit authorization and an agreed checker route and quality gate.
- [x] **Checker selection:** reuse the existing `models.slots.guardrail` slot and shared model-selection machinery, rather than introducing a second checker configuration system. — Decision: The operator approved independent checker selection and explicitly requested leveraging the existing guardrail slot. Preserve that operator-facing selection; fix composition so the checker route is captured at Build and cannot change with the main/session provider. Do not add a `guardrails.checker` subtree or parallel provider flags. A scalar guardrail slot binds its selected model to the deployment default provider; the proposed optional `{provider, model}` slot form explicitly selects another configured provider. Reviewer evidence access remains no wider than the originating worker's authority.
- [x] **Budgets:** use the researched runtime recommendation as the initial design, not as a claim of measured performance. — Decision: The operator endorsed 90 seconds total per review, including initialization, evidence gathering and recoverable retries but excluding human approval wait; ordinary cases finish with one assessment, and investigation is justified only when missing evidence could change the decision. Remove the arbitrary four-read cutoff and derived six-turn limit. Permit at most three total attempts for recoverable operational failures inside the same deadline, never retries of a genuine rejection, and account for nested provider retries. Reuse engine cancellation/limits machinery. Measure normal-check latency, investigation frequency, missed attacks, and false positives on the selected checker before shipping. The pinned Codex implementation supplies the 90-second/three-attempt precedent; Claude hook limits are comparative ceilings, not Auto-classifier latency evidence. Post-approval evidence is revalidated once under the remaining normal operation/parent context, with no arbitrary child deadline.
- [x] **Current-task trajectory:** connect relevant actions across the current task and its workers without copying worker conversations into the main model's prompt. — Decision: The operator approved harness-owned evidence linking sensitive reads to external sends and identifying attempts to repeat a denied action through another worker or tool. Reassess materially safer alternatives and new genuine user authorization rather than treating prior denial as permanent prohibition. Scope is the current task/delegation-root run only, not cross-session history or restart-complete state. The initial arbitrary 512-fact implementation limit is not part of this approval; bounded retention and incomplete-evidence behavior must be specified without silently dropping evidence relevant to an authorization decision.
- [x] **Public status and coverage:** expose an understandable guardrail status view and precise intervention/approval details. — Decision: The operator approved visibility of the configured checker, effective check coverage, health and inspection outages, the action or result stopped, finding versus inability to inspect, and the exact scope of Run once and Don't ask again. Prefer existing client/API conventions.
- [x] **Human-readable checker rationale:** include meaningful explanations now, not a later UX phase. — Decision: The operator approved a specific concern and relevant source explanation instead of opaque concern codes alone. Explain why an action does not fit the user's authorization or why inspection could not complete, without exposing secrets. Checker-authored prose must not become trusted instructions to the working agent and reviewed content must not be dumped into logs. Use an owner-authorized safe human-display channel distinct from machine-only durable audit projection.
- [x] **Inbound post behavior:** withhold flagged output from the working agent while allowing an interactive human to release that exact result once. — Decision: The operator approved explaining the concern, retaining the already-produced result for same-run owner-authorized release, and delivering that existing result without re-executing the tool or repeating its side effects. A release authorizes reading the content, not obeying embedded instructions or granting permission for later actions. In unattended enforcing mode, withhold the output and report why; advisory delivers it unchanged with the finding visible. No sanitization is reintroduced. Cross-process held-result recovery remains outside the approved scope.

## Implementation measurements and calibration

Implementation selects private evidence-handle, evidence-byte, trajectory-fact, and trajectory-byte capacities as internal constants, using the native tool/provider context limits and measured stress workload. The existing `max_*` names describe those internal capacities only: they are not new public deployment configuration fields and need no unspecified public defaults. Before implementation review, an evidence report records the selected finite values, rationale, stress experiments, and the offline proofs that each read rejects before an oversized allocation and that aggregate exhaustion becomes unresolved rather than acceptable. Capacity calibration may change those private constants within this declared fail-closed behavioral boundary without another human plan round.

The implementation also builds the versioned paired quality corpus, measurement runner, baseline, and paired-regression reporting. Offline mocks prove protocol and safety wiring only; they do not establish checker efficacy. Any actual model comparison is a separate release-validation activity and requires operator authorization of its checker route and spend at that time. Its results must be recorded before claiming production readiness, but neither a live evaluation nor numerical results are prerequisites for proposing, merging, or implementing this plan. The approved 90-second total deadline and three-attempt maximum remain unchanged.

## Interface contract

- **gRPC / protobuf:** additive machine review/scope fields at current free field 5, `Approval.origin=6`, owner-authorized coverage, and a live-only detail RPC; exact declarations below.
- **Exported Go APIs / interfaces:** consumer-local `engine/agent` reviewer, evidence, and transient-detail interfaces plus session-owned machine projections; no inward cycle; exact declarations below.
- **Tool schemas:** two review-run-local evidence/submit tools, absent from every ordinary catalog; exact schemas below.
- **CLI / config:** no new reviewer/provider selector; reuse `models.slots.guardrail`, existing aliases, kill switch, rule modes, and checker-down choice; remove sanitize.
- **Events / persistence:** machine-only durable projections, narrow approval-origin replay repair, and run/service-local held result, evidence, trajectory, and detail resources.
- **Security / authority:** exact effective-call gates, harness provenance, finite evidence authority, separate action/inbound jobs, and secret-conscious transient rationale.
- **Compatibility / migration:** breaking reviewer/sanitize removal with additive old-reader-safe wire fields and fail-closed restored live approvals; no migration or compatibility mode.

### 1. gRPC / protobuf

Add machine-only review metadata at the next free field in each current message: `GuardrailReview guardrail = 5` on `Hook` (current fields 1–4) and `GuardrailApprovalScope guardrail = 5` on `PermissionAsk` (current fields 1–4). Add `string origin = 6` to the current `Approval` message (fields 1–5), with exact recognized values `permission`, `hook_guardrail`, and `plan`; absent or unrecognized values are unknown and cannot drive permission learning. Add owner-authorized unary RPCs `ListGuardrailCoverage` and `GetGuardrailReviewDetail`; the latter reads only the live registry, retains completed-review detail through the owning delegation-root Run so clients can fetch after the event, and returns not-found after root-run cleanup, cancellation/disconnect cleanup, shutdown, or restart. It never reads the durable event log.

Use these exact enums:

```proto
enum GuardrailJob { GUARDRAIL_JOB_UNSPECIFIED = 0; GUARDRAIL_JOB_ACTION = 1; GUARDRAIL_JOB_INBOUND = 2; }
enum GuardrailAssessment { GUARDRAIL_ASSESSMENT_UNSPECIFIED = 0; GUARDRAIL_ASSESSMENT_ACCEPTABLE = 1; GUARDRAIL_ASSESSMENT_PROHIBITED = 2; GUARDRAIL_ASSESSMENT_UNRESOLVED = 3; }
enum GuardrailInspection { GUARDRAIL_INSPECTION_UNSPECIFIED = 0; GUARDRAIL_INSPECTION_COMPLETE = 1; GUARDRAIL_INSPECTION_OPERATIONAL_FAILURE = 2; }
enum GuardrailDisposition { GUARDRAIL_DISPOSITION_UNSPECIFIED = 0; GUARDRAIL_DISPOSITION_EXECUTE = 1; GUARDRAIL_DISPOSITION_ASK_ACTION = 2; GUARDRAIL_DISPOSITION_WITHHOLD_RESULT = 3; GUARDRAIL_DISPOSITION_RELEASE_RESULT = 4; GUARDRAIL_DISPOSITION_DENY = 5; GUARDRAIL_DISPOSITION_PASS_ADVISORY = 6; GUARDRAIL_DISPOSITION_CONTINUE_WARNING = 7; }
enum GuardrailApprovalKind { GUARDRAIL_APPROVAL_KIND_UNSPECIFIED = 0; GUARDRAIL_APPROVAL_KIND_ACTION = 1; GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE = 2; }
```

`GuardrailReview` contains `review_id=1`, `job=2`, `assessment=3`, `inspection=4`, `disposition=5`, machine `reason_code=6`, `rule_id=7`, `rule_origin=8`, `checker_provider_id=9`, `checker_model_id=10`, and repeated validated concern/source refs at 11–12. `GuardrailApprovalScope` contains `review_id=1`, `kind=2`, opaque `grant_digest=3`, `session_only=4`, and `repeat_available=5`; field 6 is reserved. `Approval` retains fields 1–5 and adds exact string `origin=6`. This proto field exists for projection parity when `StreamSessionEvents` renders LOG_ONLY approvals and for mapper/wire consumers; it is not the durable replay source. The jsonl event log and grpcdriver store the raw JSON `session.Event`, whose JSON `ApprovalPayload.Origin` is what `replayApprovals` actually validates before learning. No new cloud transport is introduced. The durable messages contain no rationale, argument/result preview, raw args, evidence body, target path, transcript, or held result. Human display comes only from the transient detail API.

The status/detail RPC messages are exact:

```proto
message ListGuardrailCoverageRequest { string session_id = 1; }
message GuardrailCoverageEntry { string tool = 1; string phase = 2; GuardrailJob job = 3; string mode = 4; string rule_id = 5; string rule_origin = 6; GuardrailInspection inspection = 7; string reason = 8; }
message ListGuardrailCoverageResponse { bool enabled = 1; string checker_provider_id = 2; string checker_model_id = 3; repeated GuardrailCoverageEntry entries = 4; }
message GetGuardrailReviewDetailRequest { string session_id = 1; string review_id = 2; }
message GetGuardrailReviewDetailResponse { string review_id = 1; string concern = 2; string source_display = 3; string next_action = 4; }
```

`HarnessService` adds `rpc ListGuardrailCoverage(ListGuardrailCoverageRequest) returns (ListGuardrailCoverageResponse)` and `rpc GetGuardrailReviewDetail(GetGuardrailReviewDetailRequest) returns (GetGuardrailReviewDetailResponse)`.

`GetGuardrailReviewDetail(session_id, review_id)` returns bounded `concern`, `source_display`, and `next_action` strings. The Service authorizes the exact session owner; for a delegated review it also accepts only the already-authorized parent owner presenting the parent-visible child reference that the live root Run registered, never an arbitrary child/session ID. It returns no environment or filesystem reference. Detail for every completed assessment—acceptable, advisory, enforcing, or operational failure—remains available through the owning delegation-root Run lifetime so a UI can fetch it after receiving the card/event, then is deleted after child drain at root-run cleanup (or earlier on cancellation, disconnect cleanup, or shutdown). This uses the existing bounded root-Run lifetime rather than an arbitrary retention timer. This is not an `Event` field: relays append every observed `session.Event` before sending, so putting prose in `EvHook` would persist it. Mecatui requests detail for the current ask/status card and never treats it as working-agent input.

### 2. Exported Go APIs / interfaces

Add the consumer-local API in `engine/agent/review.go`; it may import `engine/governance`, `engine/session`, and stdlib. `engine/session` never imports `engine/agent`.

```go
type ReviewJob string
const ( ReviewJobAction ReviewJob = "action"; ReviewJobInbound ReviewJob = "inbound" )
type ReviewAssessment string
const ( ReviewAcceptable ReviewAssessment = "acceptable"; ReviewProhibited ReviewAssessment = "prohibited"; ReviewUnresolved ReviewAssessment = "unresolved" )

type ReviewPrincipalFact struct { Kind, Ref, Statement string; PositiveVerdict bool }
type ReviewCaller struct { Role string; Isolated bool; Capabilities []string }
type ReviewTarget struct { Kind, Display, DestinationID string }
type ReviewEvidenceMeta struct { Handle, Kind, Display, Version string; Complete bool }
type ReviewCapacity struct {
    MaxEvidenceHandles int       // count per review
    MaxEvidenceBytes int64       // bytes per review
    MaxTrajectoryFacts int       // count per delegation-root Run
    MaxTrajectoryBytes int64     // bytes per delegation-root Run
}
type ReviewTrajectoryFact struct { Call session.ToolCallID; Ref, Direction, DataClass, TargetID, Decision string }
type ReviewConcern struct { Ref, Category, Rationale, SourceRef string }
type ReviewEvidenceUse struct { Handle, Version string; Supports []string }
type ReviewMissingEvidence struct { Ref, Kind, Handle, Reason string }

type ToolReviewRequest struct {
    ReviewID string
    Job ReviewJob
    Event governance.HookEvent
    EffectiveCall session.ToolCall
    PrincipalFacts []ReviewPrincipalFact
    PrincipalFactsComplete bool
    Caller ReviewCaller
    Environment session.EnvironmentRef
    Target ReviewTarget
    Evidence []ReviewEvidenceMeta
    EvidenceComplete bool
    Trajectory []ReviewTrajectoryFact
    TrajectoryComplete bool
    Capacity ReviewCapacity
}
type ToolReviewResult struct { Assessment ReviewAssessment; Concerns []ReviewConcern; Evidence []ReviewEvidenceUse; Missing []ReviewMissingEvidence }
type ReviewEvidenceRequest struct { ReviewID, Handle, Version string }
type ReviewEvidence struct { Handle, Kind, Version string; Complete bool; Content string }
type ReviewEvidenceSource interface { ReadReviewEvidence(context.Context, ReviewEvidenceRequest) (ReviewEvidence, error) }
type ToolReviewer interface { Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error) }
type ReviewDetail struct { SessionID session.SessionID; ReviewID, Concern, SourceDisplay, NextAction string }
type ReviewDetailSink interface { PublishReviewDetail(context.Context, ReviewDetail) }
```

`ReviewDetails` is a callback bound to the owning root Run; publishing associates the entry with that root's existing lifecycle and registered child-reference relation, and root cleanup removes the whole registry. Individual assessment completion does not delete an entry.

Add `ToolReviewer ToolReviewer` and `ReviewDetails ReviewDetailSink` to `agent.Deps`. These are engine API additions and require the API snapshot and changelog update during implementation. Do not widen `port.LLMRequest`, `port.HookRunner`, or `port.PermissionPolicy`. Composition builds the reviewer through the normal child-engine factory with the checker provider/model pair, context window, prompt config, cancellation, and token accounting re-derived for that pair.

`engine/session` owns only transport/persistence-neutral machine projections; exact additions are:

```go
type GuardrailApprovalKind string
const ( GuardrailApprovalAction GuardrailApprovalKind = "action"; GuardrailApprovalResultRelease GuardrailApprovalKind = "result_release" )
type GuardrailReviewPayload struct {
    ReviewID, Job, Assessment, Inspection, Disposition, ReasonCode string
    RuleID, RuleOrigin, CheckerProviderID, CheckerModelID string
    Concerns, Sources []GuardrailRef
}
type GuardrailRef struct { Ref, Category string }
type GuardrailPendingScope struct { ReviewID string; Kind GuardrailApprovalKind; GrantDigest string; SessionOnly, RepeatAvailable bool }
type ApprovalOrigin string
const (
    ApprovalOriginUnknown ApprovalOrigin = ""
    ApprovalOriginPermission ApprovalOrigin = "permission"
    ApprovalOriginHookGuardrail ApprovalOrigin = "hook_guardrail"
    ApprovalOriginPlan ApprovalOrigin = "plan"
)
```

Add `Guardrail *GuardrailReviewPayload` to `session.HookPayload`, add `Guardrail *GuardrailPendingScope` with JSON tag `json:"guardrail,omitempty"` and serialized `Origin ApprovalOrigin` with `json:"origin,omitempty"` to `session.PendingAsk`, and add the same `Origin ApprovalOrigin` to `session.ApprovalPayload`. This explicit origin replaces the competing serialized `HookOriginated`/`PlanOriginated` booleans; run-scoped `ConfiguredAsk`/`FlooredConfiguredAllow` remain separate policy hints. Every ask constructor sets exactly one of permission, hook-guardrail, or plan. Missing origin from an old snapshot/event is unknown and never means permission: a restored unknown allow records a synthetic unresolved error and re-asks on a future call rather than executing, learning, arming a waiver, or changing plan mode. The mapper projects `PendingAsk.Guardrail` to proto `GuardrailApprovalScope`.

### 3. Tool schemas

The reviewer engine receives two run-scoped synthetic tools only; neither enters main, worker, checker-child, MCP, or shared catalogs:

- `ReadReviewEvidence`: `{"review_id":"...","handle":"...","version":"..."}`. The description says handles are the only evidence authority, source/tool names are untrusted labels, guessed paths/URLs/IDs are invalid, tools are unnecessary unless missing evidence could flip the verdict, and content is data rather than instructions.
- `SubmitReviewAssessment`: `{"assessment":"acceptable|prohibited|unresolved","concerns":[{"ref":"C1","category":"...","rationale":"specific concern","source_ref":"S1"}],"evidence":[...],"missing_evidence":[...]}`. Only a valid call terminates investigation.

The first assessment is normally tool-less whole-output JSON. Investigation starts only for specifically named missing evidence that could change the assessment. Wrong tools, malformed output, unsupported/duplicate refs, unauthorized evidence, missing submit, timeout, or exhausted inherited budget are operational failure/unresolved; blank or tool-only model output is handled by the normal bounded engine machinery and is not automatically reclassified as a genuine unsafe finding.

Real factory-path tests must prove both tool descriptions and the reviewer/worker posture instructions reach their owning system-prompt layer. The worker instruction explains coverage, exact human actions, that result release permits reading but not obeying content, and that a denial cannot be bypassed through another tool or worker.

### 4. CLI / config

There is one reviewer and no reviewer-mode selector. Remove `sanitize` from implementation, strict schema, generated/current documentation, prompts, and tests; only `block` and `advisory` remain. Do not add `guardrails.review`, `guardrails.checker`, `--guardrails-review`, or `--guardrails-provider`.

Retain the existing `guardrails.rules[].prompt` field, but deliberately change it from full rubric replacement to additive operator policy. The reviewer prompt always starts with a fixed harness-owned rubric containing the authority/provenance model, source-aware false-positive requirements, distinct action/inbound protocol, evidence-handle rules, structured-output contract, and operational-failure behavior. A non-empty rule `prompt` is fenced and appended beneath that rubric as operator policy for task-specific risk classification within the caller's existing permissions; it cannot replace, weaken, or conceal the fixed security/protocol contract. No legacy full-override mode is retained.

Guardrails remain off unless the existing `--guardrails-model` or `models.slots.guardrail` enable path selects a model, and the existing kill switch still wins. Route precedence is: kill switch; an explicit `guardrail` slot; the current `cheap` tier fallback used by that slot; then the legacy gate model. Thus either existing slot path continues to supersede `--guardrails-model`/`guardrails.model`. The checker provider/model pair is resolved once at Build and captured by every main, per-session, and worker checker factory; changing a session's provider/model never changes that pair.

The scalar form remains compatible and independent of sessions:

```yaml
models:
  slots:
    guardrail: cheap-reviewer
```

It resolves `cheap-reviewer` through the existing alias/selector grammar and binds the resulting opaque model ID to `reg.Default()` as captured at Build. The same deployment-default binding applies when the guardrail slot falls through to an existing scalar `cheap` tier and when only the legacy gate model enables guardrails. It does not infer provider from model text. To select another already-configured provider, this plan proposes an object form only for the existing guardrail slot:

```yaml
models:
  slots:
    guardrail:
      provider: anthropic
      model: cheap-reviewer
```

`provider` and `model` are both mandatory, non-empty fields; `model` is still an existing selector/alias, and `provider` is an exact configured registry ID. The object form is operator-tier only; `captureProjectModels` WARN-ignores it exactly as it ignores project `default_provider`, while existing project-tier scalar slot behavior remains unchanged. No provider is inferred from a model ID. Unknown mapping keys, a mapping on any other slot, missing/empty fields, an unknown/unavailable configured provider, or an unresolvable configured guardrail selector are startup errors—never silent disablement, session fallback, or checker-down runtime posture. Provider validation is the local registry lookup only: Build does not call a provider's live model-list endpoint or reject an opaque model merely because live inventory cannot prove it exists.

The proposed parser/composition delta is deliberately narrow. In `permconfig`, replace only the `ModelsSection.Slots map[string]string` decode surface with:

```go
type ModelSlotValue struct {
    Model string
    Provider string
    ExplicitProvider bool
}
type ModelSlots map[string]ModelSlotValue
func (s *ModelSlots) UnmarshalYAML(ast.Node) error
```

`ModelSlots.UnmarshalYAML` decodes every existing scalar as `{Model: scalar}` byte-compatibly; only key `guardrail` may decode a strict `{provider, model}` mapping, setting `ExplicitProvider`. In `app.Config`, keep `ModelSlots map[string]string` for all shared scalar selection and add only:

```go
type ModelTargetSelector struct { ProviderID, Model string }
GuardrailSlot *ModelTargetSelector
```

`foldOperatorModelSlots` continues the current CLI-per-key precedence: a CLI `--model-slot guardrail=...` scalar suppresses an operator-YAML guardrail object; otherwise scalar values populate the existing map and the object populates `GuardrailSlot`. The single fail-fast resolver is:

```go
func resolveGuardrailBinding(cfg Config, reg *providerRegistry) (providerID, model string, src guardrailSource, configured bool, err error)
```

It implements the precedence above, resolves the selected model through `lookupModelAlias`, looks up an explicit mapping provider or the captured `reg.Default()`, and returns one concrete pair consumed by status, posture logging, escape checking, and all checker-engine factories. Other slots retain their current scalar types, fail-soft semantics, and session-provider behavior. This optional object is a proposed API detail implementing the already-approved independent existing-slot route, not a claim that provider-aware slots exist today and not a general slots migration. Do not add `guardrails.review`, `guardrails.checker`, `--guardrails-review`, or `--guardrails-provider`.

Keep the existing operator-tier `guardrails.onCheckerDown` choice: enforcing default `fail` means bounded recovery then interactive human action approval or unattended block; explicit `warn` permits continue-with-warning only for operational checker failure. Per-rule `failClosed` may tighten or opt into that operational warning behavior, but cannot pass a genuine prohibited finding, deterministic deny, forbidden evidence, invalid authority, or stale binding. Advisory always delivers unchanged while visibly distinguishing finding from inspection failure.

Budget is 90 seconds total from reviewer setup through evidence and recovery, sharing the parent cancellation deadline and excluding human wait. At most three complete attempts fit inside that same deadline, only for classified recoverable operational failures; provider-internal retries count against it. A completed assessment is never retried. There is no four-read, six-turn, five-second revalidation, or new limiter framework. Post-human revalidation uses the remaining normal operation/parent context, runs once, does not reset model budget, and does not call the reviewer. The deadline limits elapsed work, not memory: implementation selects and validates private finite evidence-handle/byte and trajectory-fact/byte capacities before allocation; they are internal constants, not public configuration.

### 5. Events / persistence

`EvHook` and its field-5 proto projection carry machine dimensions and validated refs only. `EvPermissionAsk` carries machine approval scope, never rationale, argument/result display, or a held result. The transient detail registry is Service-owned and process-local, but each entry is attached to the owning delegation-root Run and retained until root cleanup so a client can fetch acceptable/advisory detail after observing its event. Exact-owner access and the registered parent-to-delegated-reference relation are the only authorization paths. Cancellation, disconnect cleanup, shutdown, and restart remove it earlier.

The implementation must add these exact resources to both cloud-native inventory lists because each outlives one internal tool call and loses state on restart:

| Resource | Owner / scope | Cleanup | Restart decision |
|---|---|---|---|
| transient review-detail registry | Service entries indexed under one delegation-root Run | root child-drain/termination; earlier cancellation, disconnect cleanup, or shutdown | reset-by-design; detail becomes not-found |
| held-result map | live root Run, keyed by exact review/call binding | atomic release/deny consumption; otherwise cancellation, disconnect, root termination, or shutdown destroys bytes | reset-by-design; synthesize withholding error, never rerun |
| review evidence handles/previews | one review, bounded by owner/session/environment/route/caller and implementation-calibrated private count/byte capacities | assessment completion, cancellation, timeout, or root termination | reset-by-design; stale/unavailable handle is unresolved |
| trajectory ledger | one delegation-root Run shared by registered workers | after child drain/root termination or earlier cancellation/shutdown | reset-by-design; no cross-run reconstruction |
| contextual repeat grants | Build-owned process map, scoped to one session and exact version-bound digest | session clear, process shutdown, or invalidation/miss; no durable replay | reset-by-design; re-review after restart |

No wider cloud persistence is added.

A delegation-root `Run` owns a trajectory ledger until normal child drain/termination. Workers submit harness facts through a callback, not parent-model prompts: sensitive read, external send, delegation, deny, release, materially safer replacement, and new genuine approval. Retention is bounded by the existing root-run lifetime and cleared after child drain. Implementation selects private finite `max_trajectory_facts` and `max_trajectory_bytes` capacities from native limits and measured stress workload, then enforces them before retaining a new fact. Do not impose the rejected 512-item cap or silently omit relevant facts. Exact duplicates and superseded non-authorizing status may be compacted only when the resulting fact set is authorization-equivalent; otherwise mark `TrajectoryComplete=false`, stop retaining bodies, preserve only bounded metadata that identifies the forbidden/incomplete source class, and fail affected reviews unresolved. A metadata-only representation is forbidden when it could erase the data class, direction, target identity, denial/release status, or other fact needed for the authorization decision. Add this resource to List 1 and List 2 as run-local/reset-by-design, without wider cloud persistence.

Narrow approval correctness is in scope. Every new `PendingAsk` and `ApprovalPayload` carries the explicit origin `permission`, `hook_guardrail`, or `plan`; absent is unknown. `surfaceAsk` copies the pending origin into the verdict event. In `resolvePendingCall`, validate approval kind and switch on origin before any `Policy.Learn`, waiver operation, plan transition, execution, or held-result release: permission may follow the ordinary learning/execution path; hook-guardrail may execute an approved action or release the one bound held result and may arm only the exact process/session waiver; plan performs only the existing plan transition and never executes or learns; unknown fails closed with a paired synthetic error. `replayApprovals` consumes only `AllowAlways` events whose origin is exactly permission. Old events/snapshots with no origin are unknown and conservatively re-ask on a later call; call/tool names and old booleans are never used to guess permission origin. The `EvApproval` remains a verdict mirror and adds only this small machine origin field—no args, result, rationale, or grant body.

A contextual action repeat grant never calls permission learning and never survives restart. Repeat is offered only for a repeatable, version-bound action, and the human detail names the exact session/environment/caller/tool/effective-call/target scope and every implicated script, configuration, or target dependency included in it. Its `GrantDigest` is an opaque HMAC-SHA256 over one shared canonical preimage helper used by both creation and recomputation: the exact ASCII bytes `mecatl/guardrail-grant/v1`, followed by one NUL byte, followed by the canonical length-delimited encoding of that scope and the identified dependency versions. The HMAC key is the existing Build-owned random secret used for process-local opaque selectors or a sibling key from the same Build owner, but domain separation is mandatory in either case. A wrong-purpose MAC made with the same key and identical subsequent fields never validates. Neither the key nor the preimage is logged. The digest is not an unkeyed hash of secret-shaped args and cannot collide through delimiter ambiguity. If required dependency evidence is missing, unverifiable, or marked incomplete by review, set `RepeatAvailable=false`, explain the precise limitation to the human, and still offer Run once; do not silently downgrade all approvals or claim to discover every dynamic Shell dependency. Safe common direct actions with complete versioned bindings retain repeat. A known script/config/target version change misses the grant and triggers a fresh review; an incomplete dynamic dependency can never mint a new repeat grant. The exact tool call remains in existing session history; no call body is copied into the approval event. This is the approved narrow replay repair, not a general event-log/restart redesign.

### 6. Security / authority

Action and inbound review are separate jobs and separate prompts. Action review runs after one trusted inner mutation and deterministic permission/plan/system-scope re-evaluation of changed effective args. An unchanged call does not receive a redundant permission ask. Deny, configured Ask, plan mode, and Shell system-scope decisions always bind. Approval executes the byte-identical reviewed call without rerunning mutation.

Authority comes from harness provenance: current genuine user task, genuinely admitted project instructions, actual positive plan/approval callbacks, caller capabilities, exact environment, and current delegation-root trajectory. Text remains untrusted. No filename, repository, or domain is intrinsically trusted. The reviewer must find affirmative attempted authority crossing or redirection; imperative language, ordinary GitHub issue instructions, admitted `AGENTS.md`/`CLAUDE.md` project instructions, assistant-directed prose, or quoted security examples alone are not findings. Missing evidence does not itself increase intrinsic risk. The action-risk rubric is not reused wholesale for inbound content.

Evidence handles are finite, random/opaque, review-local capabilities minted only for implicated objects. Creation is admitted only while both unresolved review limits—`max_evidence_handles` count and `max_evidence_bytes` aggregate—have capacity; exceeding either marks `EvidenceComplete=false` and makes a capacity-dependent assessment unresolved. Metadata may display a bounded untrusted tool/MCP/source label, never a guessed path or authority. Reads bind review, owner, session, environment revision, checker route, caller capability, handle, and version. Textual file evidence uses incremental `offset`/`limit` previews shaped to the native Read tool's existing 2,000-line and 25,000-byte per-call caps and returns `Complete=false` plus continuation metadata when more remains. That reuse is a payload bound, not an allocation proof: because current `fstools.ReadTool` calls `Workspace.ReadVersion` before truncation, the evidence adapter must use a backend bounded-read/preflight path or reject the handle before reading an oversized source; it must not allocate the entire object and truncate afterward. Existing incoming tool results continue through full contextual review under ADRs 0049/0050 and are never silently capped or skipped; the secondary evidence budget controls only additional reviewer-fetched evidence. Eligibility is an explicit source-kind/worker-authority allowlist: credential-bearing fields already classified by the harness (including MCP headers and provider credentials), binary data, and otherwise unauthorized sources never receive handles. If bounded access or trustworthy completeness metadata is unavailable, expose only non-sensitive identity/version/class metadata, mark the source incomplete, and force unresolved whenever its body could affect authorization; metadata-only is forbidden for a class whose body is required to decide. There is no vague content-based “secret detector” that can grant access or claim complete DLP. Rationale and source display are UTF-8 repaired, control/framing neutralized, rune-bounded, and treated as untrusted human display. Raw inputs, evidence, held results, and rationale are absent from diagnostics, metrics, and durable audit.

### 7. Compatibility / migration

This is an approved breaking replacement: no legacy reviewer, dual mode, sanitize compatibility, migration, deprecation, or automatic rewrite. Strict configuration rejects `sanitize` and unknown fields. Unconfigured deployments remain guardrails-off; explicit advisory remains available.

Additive protobuf fields are ignored by old readers. The server validates approval kind before any state mutation. Action asks support Run once, Don't ask again for this action in this session, and Cancel. `result_release` supports Release once and Cancel only: AllowAlways or any other verdict is rejected before deletion, release, history/event recording, policy learning, waiver arming, or audit claim, leaving the held item available for a valid live answer unless the run is otherwise ending. A valid Release once consumes exactly one bound repaired `ToolResult`, records/emits/delivers it once, and performs no tool/action execution, PreToolUse/PostToolUse hook, reviewer call, `Policy.Learn`, or waiver operation.

If cancellation, decline, disconnect/run cleanup, or restart makes a held result unavailable, the original bytes are destroyed/lost and the outstanding tool call is closed with one recorded/emitted synthetic withholding error so history stays paired; the original tool is never rerun. Cross-process restoration of a contextual action ask likewise cannot prove its live binding: Deny remains Deny and either allow records an accurate unresolved synthetic error without executing. More complete cross-process recovery is deferred.

## Runtime behavior and defaults

### Completed-review disposition matrix

`inspection=complete` means the checker completed its protocol; it does not imply that the checker had enough evidence to decide. In particular, `assessment=unresolved` caused by exhausted capacity, incomplete trajectory, or specifically missing decision-relevant evidence is a completed assessment, not an operational checker outage.

| Rule mode | Job | Assessment | Inspection | Runtime disposition |
|---|---|---|---|---|
| enforcing (`block`) | action | acceptable | complete | execute after binding revalidation |
| enforcing (`block`) | action | prohibited | complete | ask for action approval interactively; deny headless |
| enforcing (`block`) | action | unresolved | complete | ask for action approval interactively; deny headless, naming actual missing-evidence source refs |
| enforcing (`block`) | inbound | acceptable | complete | release the result |
| enforcing (`block`) | inbound | prohibited | complete | hold for `result_release` interactively; withhold headless |
| enforcing (`block`) | inbound | unresolved | complete | hold for `result_release` interactively; withhold headless, naming actual missing-evidence source refs |
| advisory | action or inbound | acceptable | complete | original action/result unchanged, with acceptable status |
| advisory | action or inbound | prohibited | complete | original action/result unchanged, with an explicit finding |
| advisory | action or inbound | unresolved | complete | original action/result unchanged, with explicit unresolved/incomplete status; never label it an unsafe finding |
| enforcing (`block`, `onCheckerDown: fail`) | action or inbound | no completed assessment | operational_failure | bounded recovery, then the job-appropriate interactive approval or unattended deny/withhold |
| enforcing (`block`, `onCheckerDown: warn`) or advisory | action or inbound | no completed assessment | operational_failure | original action/result unchanged with an explicit checker-failure warning |

`onCheckerDown` and its warning/fail streak apply only to operational checker failure. A completed unresolved assessment does not increment the checker-DOWN streak. Invalid authority, stale binding, forbidden evidence, and deterministic permission/plan/system-scope denial fail closed in their owning gate and never become checker-DOWN. Missing-evidence reporting references only actual unavailable/incomplete sources; it must not fabricate an attack or prohibited finding.

### Coverage

When guardrails are enabled, the default enforcing rules are:

| Boundary | Tools/categories | Job |
|---|---|---|
| before execution | Shell; Edit, Write, Copy, Move, Remove; WebSearch, WebFetch; `mcp__*`; `CallMcpWithQuery`; Subagent, Parallel, Team | action |
| before result delivery | Shell; Read, ListDir, Grep, Glob; WebSearch, WebFetch; `mcp__*`; `FetchMcpResource`; `CallMcpWithQuery` | inbound |

The same applicable checks run for main, read-only/writable Subagent, Parallel branches, Team members, and lead. Checker engines are inert and cannot recursively invoke guardrails. Explicit operator rules replace matching defaults using existing precedence; `advisory` remains explicit. No local-read pre-check is implied.

### Pre-action read-batch assessment and execution

The dispatcher serially performs existing tool lookup, requested-call permission/plan/system-scope checks, trusted mutation exactly once, effective-call policy/system-scope checks when mutation changed arguments, immutable review-request preparation, and a repeat tool lookup for every sibling. Static `DispatchSerial`, call-specific `MutatesParent`, mutating, and unknown-tool barriers retain their current semantics and are never admitted to this speculative phase.

For an eligible ordinary read-only sibling, only the independent `ToolReviewer.Review` assessment runs concurrently, each into a private indexed record. Assessment goroutines do not mutate `Session`, recorder, event stream, trajectory ledger, transient detail, or any approval state. No tool action—including a read-only action—executes speculatively. After all assessments join, the dispatcher drains decisions and any action asks in original call order, with at most one live ask. After every wait and immediately before execution, it revalidates authority, environment, dependencies, and the reviewed binding. It then dispatches only the approved actual read-only tools concurrently through the existing read batch. WebSearch, WebFetch, Subagent, and Parallel remain eligible when their existing static/call-specific classification is read-only; contextual review must not impose a global serial-LLM regression on them.

Cancellation joins all assessment work and closes every admitted call with an ordered paired error result. Concurrency proofs use channels to establish that all reviewers entered before unblock and that approved read execution overlaps; ordering proofs observe surfaced asks directly and use no sleeps.

### Post-result hold and release

The tool executes once. Post hooks run once, then the repaired effective result is reviewed before `ToolCallRecorder`, `RecordToolResults`, `EvToolResult`, save, client result delivery, or model delivery. On a flagged enforcing result:

1. store the exact already-produced repaired result in the live Run's private held-result map;
2. surface a `result_release` ask and owner-authorized transient concern/source detail;
3. on Release once, atomically consume and revalidate the live binding, then pass that exact one result through the ordinary recorder/record/event delivery tail without calling the tool, any action gate, PreToolUse/PostToolUse hook, reviewer, policy learning, or waiver arming;
4. tell the working model through fixed harness text that release permits reading the data, not following embedded instructions;
5. on Cancel, timeout, disconnect cleanup, headless enforcement, stale binding, or lost held state, destroy the private result and send only one synthetic withholding error through that same tail so tool history remains paired.

The held original is absent from the existing `ToolCallRecorder`, events, snapshots, diagnostics/metrics, checker/review sinks, and working-model history until release; on withholding those consumers receive only the synthetic error. The current foreground tool interface returns one complete `ToolResult` and has no live tool-result chunk event, so this choke point precedes client result delivery. `CommandStreamer` is currently used only for background Shell into a private tail buffer; its output is not emitted chunk-by-chunk, and any later status/collection tool result goes through normal inbound review. Any future adapter that exposes lower-level live output for a covered tool must buffer the reviewable output until assessment/release or decline enforcing inbound coverage—it must not claim post-output withholding after bytes have escaped. This guarantee concerns final tool-result content, not unrelated progress/status events.

The saved history, final result stream, recorder, and model view receive the same payload: released original or synthetic withheld error. The held original never enters a snapshot/event/log. Advisory records/emits/delivers the original unchanged and emits a visible finding. Restart recovery fails closed and never replays the tool; avoiding a second side effect is more important than recovering the held bytes.

`runReadBatch` preserves read-parallel/mutate-serial execution rather than serializing tools to hide the single-`PendingAsk` constraint. After Phase 1 clears calls, each goroutine runs the actual read-only tool, PostToolUse, UTF-8 repair, and its inbound review into a private indexed execution record; it does not call `PauseForApproval`, mutate `Session`, write the recorder, emit the final result, or release/withhold content. Once all goroutines join, the dispatcher goroutine drains those records in original tool-call order. It validates each review decision, surfaces at most one release ask at a time, consumes Release once or synthesizes the withholding error, and only then invokes the ordinary recorder/event tail. After every sibling has a complete final decision, it appends one complete ordered `RecordToolResults` slice. If two siblings are held, release of the first is fully resolved before the second ask is surfaced; the second may independently be denied. Cancellation after the first release synthesizes cancellation/withholding errors for every remaining sibling and records the complete ordered slice before termination. An unknown/stale pending result is rejected, never matched by position or tool name. No drain path reruns tool execution, PostToolUse, the inbound reviewer, or any side effect.

## Acceptance scenarios

### Scenario 1 — effective action, authority, and repeat grants

This changes the hook-approval contract in [ADR 0062](../adr/0062-guardrails-approve-once.md) while preserving permission authority.

- AC1.1: trusted mutation runs once; changed effective args are re-evaluated by permission, plan, and system-scope gates; unchanged calls do not receive a duplicate ask; execution receives byte-identical reviewed args.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_EffectiveCallOrder`
- AC1.2: Run once executes once; Don't ask again is offered only when implicated script/config/target dependencies are identified and version-bound, matches only the exact displayed session/environment/caller/tool/effective-call/target/dependency digest, and never reaches permission learning or permission replay. Grant creation and recomputation share one HMAC preimage helper that prepends exact ASCII `mecatl/guardrail-grant/v1` plus NUL before the canonical length-delimited scope; the prefix is mandatory even with a sibling key, a wrong-purpose MAC made with the same key and fields never validates, and neither key nor preimage is logged. A known script change misses; an incomplete dynamic dependency sets `repeat_available=false` with a precise human limitation while Run once remains available; complete safe direct actions retain repeat.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_ExactRepeatGrant`, `TestADR_0334_ContextualGuardrails_Scenario1_GrantDomainSeparation`
- AC1.3: permission, hook-guardrail, and plan approvals carry distinct explicit origins through live and awaiting paths; JSON event replay accepts only explicit permission origin, the `Approval.origin=6` proto projection preserves exact parity through mapper and `StreamSessionEvents`, and absent/unrecognized origins fail closed before learning/action.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_ApprovalOriginReplay`, `TestADR_0334_ContextualGuardrails_Scenario1_ApprovalOriginProtoProjection`
- AC1.4: sanitize configuration and implementation paths are absent/rejected, with no legacy reviewer selector or replacement-action execution.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_ReplacementRemoval`
- AC1.5: requested/effective deterministic gates, trusted mutation, immutable request preparation, and repeat lookup remain serial on the dispatcher; independent action reviews for eligible read-only siblings enter concurrently into mutation-free private records, then decisions/action asks drain in original order with at most one live ask. After each wait authority/environment/dependencies are revalidated before approved read-only tools execute concurrently. Static `DispatchSerial`, call-specific `MutatesParent`, mutating, and unknown-tool barriers remain serial; cancellation joins reviews and produces complete ordered paired errors; no tool action is speculative and WebSearch/WebFetch/Subagent/Parallel incur no blanket serial-review regression.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_ConcurrentActionReviews`

### Scenario 2 — evidence, budget, and failure behavior

Evidence remains narrower than the checker quarantine established by [ADR 0021](../adr/0021-guardrails.md).

- AC2.1: only advertised bound handles can be read; wrong handle/version/review/owner/session/environment/route/capability fails before access, and tool/MCP/source labels grant no authority.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_EvidenceAuthority`
- AC2.2: one 90-second deadline covers setup, evidence, provider recovery, and at most three complete recoverable attempts; genuine assessments are not retried; human wait is excluded and revalidation neither resets budget nor uses a fixed five-second context.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_TotalBudget`
- AC2.3: operational checker failure maps to job-appropriate interactive approval or unattended deny/withhold under `fail`, and only explicit `warn` continues with warning. A completed unresolved assessment caused by capacity exhaustion, incomplete trajectory, or named missing evidence follows the explicit mode/job matrix: enforcing asks action approval or holds result release interactively and denies/withholds headless; advisory leaves the original unchanged with unresolved/incomplete status, never an unsafe finding. Completed unresolved does not increment checker-DOWN state. Findings, deterministic denial, stale/forbidden evidence, and invalid authority fail closed in their owning path and never use the operational opt-out; missing-evidence refs are real rather than fabricated attacks.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_FailureMatrix`
- AC2.4: revalidation narrows races using existing versions/context without claiming an exact universal filesystem snapshot or remote atomicity.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_RevalidationResidual`
- AC2.5: evidence uses finite handles and cumulative byte capacity; textual source previews reuse native Read's existing per-call line/byte shaping but reject before whole-source allocation when bounded backend access is unavailable. Existing incoming results are not capped/skipped, incomplete capacity cannot produce an acceptable decision, and the configured bound fields report their units.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_CapacityBeforeAllocation`

### Scenario 3 — inbound hold/release

Paired history and one effective payload preserve the [hooks and guardrails architecture](../architecture/hooks-and-guardrails.md).

- AC3.1: every default post tool, including local Read/ListDir/Grep/Glob, Shell, Web/MCP, `FetchMcpResource`, and both phases of `CallMcpWithQuery`, follows enforcing hold semantics in main and workers.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_DefaultCoverage`
- AC3.2: flagged output remains absent from recorder/history/save/event/client/model and review sinks until release; Release once consumes and delivers the exact produced result once; action execution, policy/waiver mutation, reviewer, and pre/post hooks are not repeated.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_ExactResultRelease`
- AC3.3: cancel, timeout, client drop, headless enforcement, and restart destroy/unavailable held state, pair history with the same synthetic error sent to stream/model, and never re-execute.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_HeldResultCleanup`
- AC3.4: result release cannot arm an action/permission grant; action approval cannot approve inbound content.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_ApprovalClassIsolation`
- AC3.5: two read-batch siblings execute and review concurrently into private records, but their held-result decisions drain in original call order with only one live release ask. Release first/deny second yields one ordered complete result slice; cancellation after releasing the first synthesizes errors for every remainder; unknown pending IDs reject; no tool, hook, reviewer, or side effect reruns.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_ConcurrentInboundReleaseOrdering`

### Scenario 4 — trajectory, workers, and routing

Worker isolation and routing remain composition-owned as described by the [agent-loop architecture](../architecture/agent-loop.md).

- AC4.1: harness facts correlate read-to-send and alternate-tool/worker retry across one delegation-root run without copying worker transcripts into parent prompts; safer actions/new genuine approval are reassessed.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_RootTrajectory`
- AC4.2: ledger lifetime/cleanup follows root Run conventions; implementation-calibrated private aggregate count/byte capacities are enforced before allocation, relevant facts are never silently dropped, any compaction is authorization-equivalent, and incomplete evidence fails unresolved rather than blindly acceptable.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_Retention`
- AC4.3: the Build-captured guardrail route is stable across main/session/worker provider changes: a scalar slot (including current `cheap` fallback) uses `reg.Default()`, the proposed strict guardrail mapping uses its exact configured provider, both resolve aliases without model-ID provider inference/live-list startup calls, and slot precedence still wins the legacy gate.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_ExactRoute`
- AC4.4: worker reviewer evidence never exceeds that worker's authority; checker recursion is disabled.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_WorkerAuthority`

### Scenario 5 — status, transient detail, and safe audit

Transient detail extends, rather than misuses, the durable advisory projection from [ADR 0051](../adr/0051-guardrails-advisory-tui-visibility.md).

- AC5.1: coverage/status reports configured provider/model, effective tool-phase-job-mode coverage, health/outage, finding versus inspection failure, disposition, and exact approval scope from the actual assembled session catalog/rules.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_CoverageTruth`
- AC5.2: durable events/proto carry machine metadata only; transient detail is exact-owner/delegated-parent-reference authorized, live-only, bounded/neutralized, retained through root-run cleanup for post-event fetch, deleted on every cleanup path, and absent after restart.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_TransientDetail`
- AC5.3: checker prose never becomes trusted worker instructions; raw reviewed input/result/rationale is absent from logs, metrics, snapshots, and event-log replay.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_NoContentLeak`
- AC5.4: old clients ignore additive fields; invalid approval kinds fail before execution, release, learning, or misleading audit.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_OldClientSafety`

### Scenario 6 — false-positive and attack discrimination

The corpus tests the replacement decision recorded by [ADR 0334](../adr/0334-contextual-investigative-guardrails.md), not empirical efficacy through mocks.

- AC6.1: a versioned paired corpus covers ordinary GitHub issue tasks, genuinely admitted project instructions, quoted attack examples, assistant-directed prose, and matched adversarial authority-crossing variants.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_PairedCorpus`
- AC6.2: fixed prompts require concrete attempted redirection plus named authority boundary/source; missing context alone and imperative words alone are insufficient; action and inbound jobs use distinct rubrics. A configured `rules[].prompt` is additive operator task-risk policy and cannot hide or replace authority, provenance, false-positive, evidence, protocol, or output contracts.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_RubricContract`, `TestADR_0334_ContextualGuardrails_Scenario6_AdditiveRulePrompt`
- AC6.3: implementation builds a versioned measurement runner and report schema for false warnings, false blocks, misses, latency, investigation frequency, and spend; offline mocks prove protocol only and make no efficacy claim. Actual checker-model comparisons require separate operator route/spend authorization at release-validation time.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_QualityReportSchema`
- AC6.4: implementation-selected private evidence/trajectory count-and-byte capacities are finite, enforced before allocation, and aggregate exhaustion produces complete unresolved/incomplete behavior rather than acceptable.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_ImplementationCalibration`
- AC6.5: implementation review inspects an evidence report that records the selected finite capacity constants and units, rationale from native limits, measured offline stress experiments and results, and the corresponding pre-allocation/exhaustion proof artifacts. Neither this report nor separately authorized real-model quality results blocks plan proposal, Plan / Interface merge, or implementation review.
  - verify: inspection — implementation capacity-calibration report contains selected constants, rationale, stress experiments/results, and proof artifact references

### Scenario 7 — prompt discoverability and lifecycle

Factory tests and inventory follow the model-visible-affordance and resource rules in [AGENTS.md](../../AGENTS.md).

- AC7.1: real factory-path tests assert evidence-tool use, terminal submission, no-unnecessary-tools, untrusted-data, release semantics, coverage, and anti-bypass instructions in their owning prompt layers for reviewer and worker.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_FactoryPrompts`
- AC7.2: malformed/blank/tool-only/timeout/cancellation paths terminate within inherited limits without automatically labeling content unsafe or permitting it.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_TerminalSafety`
- AC7.3: cloud inventory rows document transient detail, held-result map, evidence handles/previews, trajectory ledger, and contextual repeat grants in both List 1 and List 2 with ownership, lifetime, cleanup, and reset-by-design behavior.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_ResourceInventory`
- AC7.4: concrete protobuf review/approval fields preserve exact action-versus-inbound job, assessment (including unresolved), inspection state (complete or operational failure), disposition, approval kind, repeat availability, and explicit approval origin through mapper and stream projection; invalid approval-kind/origin combinations fail before execution, release, learning, waiver, or misleading audit.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_InterfaceProjectionSafety`

## Out of scope

| Item | Boundary |
|---|---|
| Cross-process held-result recovery or tool-result escrow | Restart fails closed and never re-executes the tool. |
| Wider approval/event-log/cloud-native redesign | Only hook-versus-permission origin/replay correctness and required resource inventory rows are included. |
| Cross-session/restart trajectory | Current delegation-root Run only. |
| General reviewer Shell/network/repository exploration, DLP, or secret discovery | Finite source-authorized evidence handles only. |
| Guardrail slot object on non-guardrail slots or implicit provider fallback | Only `models.slots.guardrail` proposes strict `{provider, model}`; other slots remain scalar and the scalar guardrail route uses the Build-captured deployment default. |
| Live quality execution in ordinary tests | Requires separate route/spend authorization and readiness thresholds. |
| Path/domain/filename trust exemptions | Authority comes from harness provenance and admission. |

## Definition of done

1. Implementation review includes the selected private evidence/trajectory count-and-byte capacities, rationale, stress experiments, and offline proofs that reads fail before oversized allocation and aggregate exhaustion resolves safely; these values are internal constants, not new public configuration.
2. The implementation builds the paired corpus, measurement runner, baseline, and paired-regression report. Any real-model comparison is separately operator-authorized for route and spend at release validation; do not claim empirical efficacy or production readiness before its results exist.
3. Implementation updates and human inspection reconcile living architecture/implementation notes, owning user docs, generated configuration reference, generated proto, API snapshots/changelog, and Mecatui approval/status UI without marking behavior shipped before it lands; no single code test is treated as proof of prose, generated-artifact freshness, or UI semantics.
4. Every named executable acceptance proof exists in the actual harness path; inspection-only AC6.5 has its report artifact; paired quality fixtures distinguish protocol tests from empirical evaluation.
5. `task lint`, `task test`, `task docs`, `task api:check`, `task ac-trace-strict`, `task site:build`, and `go run ./cmd/mecademo` pass; `task docs`, API compatibility, and site build are the explicit generated-doc/API/site gates, while UI behavior also receives owning golden/manual inspection. Ordinary tests remain offline.
6. The implementation PR links the merged Plan / Interface commit and stops on contract drift.

## Primary-source inspiration

These describe comparable bounded review/guardrail patterns; they are design input, not authority for Mecatl's trust decisions:

- [Codex auto-review](https://developers.openai.com/codex/sandboxing/auto-review)
- [OpenAI Agents SDK guardrails](https://openai.github.io/openai-agents-js/guides/guardrails/)

### Runtime and prompt evidence checked during contract review

These are published/source defaults, not latency measurements. Codex source is pinned to `5b1d6560181680f95cde95c14ed042acc02248ed`; Claude documentation was retrieved on 2026-09-14.

- [Codex synchronous reviewer constants](https://github.com/openai/codex/blob/5b1d6560181680f95cde95c14ed042acc02248ed/codex-rs/ext/guardian-reviewer/src/lib.rs): `REVIEW_TIMEOUT = 90s`, `MAX_REVIEW_ATTEMPTS = 3`.
- [Codex retry semantics](https://github.com/openai/codex/blob/5b1d6560181680f95cde95c14ed042acc02248ed/codex-rs/ext/guardian-reviewer/src/retry.rs): recoverable failures retry inside one shared deadline; completed verdicts, cancellation, timeout, and nonrecoverable errors do not retry. These are review attempts, not a three-tool or three-model-turn limit.
- [Codex reviewer settings](https://github.com/openai/codex/blob/5b1d6560181680f95cde95c14ed042acc02248ed/codex-rs/ext/guardian-reviewer/src/settings.rs): provider request/stream retry limits are each one; reviewer permissions are intersected with read-only, MCP/skills/memories and recursive review are disabled. Its parent token budget is not inherited; this is evidence of another product's policy, not a proposal to remove Mecatl's budget guarantees.
- [Claude hook configuration](https://code.claude.com/docs/en/hooks#hook-handler-fields): prompt hooks default to 30 seconds; agent hooks default to 60 seconds. [Agent hooks](https://code.claude.com/docs/en/hooks#agent-based-hooks) support read-only investigation and up to 50 turns; they are documented as experimental. These are programmable hooks, not published latency/defaults for Claude's built-in Auto classifier.
- [Codex investigation and authorization rubric](https://github.com/openai/codex/blob/5b1d6560181680f95cde95c14ed042acc02248ed/codex-rs/core/assets/guardian/policy_template.md): prefer existing transcript evidence; call tools only when local state could change the decision. Untrusted material can supply implementation details for an authorized task, authorization is judged semantically rather than by exact syntax, and missing context does not itself increase intrinsic action risk. This supports targeted investigation and affirmative evidence of unauthorized redirection, not flagging every issue or instruction file. Mecatl must retain its own harness-established project admission and authority rules, not import filename-based trust from another harness.

No cited source establishes a four-read investigation limit, a normal-path latency percentile, or a false-positive rate transferable to Mecatl. Model-specific quality and latency still require separately authorized evaluation. Do not copy Codex's action-approval rubric verbatim as an inbound-content filter.
