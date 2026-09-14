# Contextual investigative guardrails — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes guardrail authority, dispatch ordering, public engine/wire surfaces, operator configuration, and main/worker invariants.
**Decision record:** [ADR 0334](../adr/0334-contextual-investigative-guardrails.md)
**Phase:** live contextual action and inbound-content review; restart-complete/cloud-native security state is deferred
**Status:** draft, 2026-09-14. The requested direction is recorded; every unchecked choice below requires human approval.
**Delivery:** Split. Merge of a Plan / Interface PR is the implementation authorization.
**Expected tasks:** deferred to orchestration

This is a plan/interface deliverable only. It does not claim the behavior is shipped and does not authorize production edits, live model calls, commits, or publication.

## Human decisions

- [ ] **Live approvals:** contextual guardrail asks support only Allow once and Deny. Reject an injected `allow_always` before any learning, execution, or `EvApproval` allow-always record. A contextual ask restored after process loss cannot reconstruct its review identity and must fail closed; this slice does not promise gRPC/restart approval recovery. If the live binding and origin marker are rejected, contextual interactive approval is blocked on a separate approval-provenance design rather than falling back to generic hook direct-execute.
- [ ] **Effective-mutation ordering:** after the existing permission decision, run trusted argument mutation once, then re-evaluate the ordinary permission policy and the existing system-scope check on the exact effective call before contextual review. Deny/Ask/plan semantics still bind; reviewer output can never override policy. Do not run mutation again after a live approval.
- [ ] **Contextual mode and sanitize migration:** add explicit operator-tier `guardrails.review: legacy|contextual`; absence remains `legacy`. Contextual rules reject `mode: sanitize` at startup with a migration message to use `legacy` for the existing checker rewrite or choose `block`/`advisory`; contextual reviewer-proposed commands are never adopted.
- [ ] **Failure/default mapping:** newly explicitly enabled contextual `block` rules map unavailable, incomplete, malformed, timed-out, exhausted, unauthorized, or stale review to interactive Allow-once/Deny before execution, and to deny/withhold headless. The named compatibility opt-outs are limited to the machine reasons `checker_down` and `compatibility_unresolved` (defined below); they never pass a `prohibited` finding or structural authority/identity/evidence failure. Existing legacy defaults do not change.
- [ ] **Default coverage:** approve the complete legacy and contextual default matrix below. In particular, `FetchMcpResource` is post-use and `CallMcpWithQuery` is both pre- and post-use; contextual action review evaluates read-only Shell using policy and read-only metadata rather than broadly skipping it. The legacy read-only-Shell pre skip remains legacy-only.
- [ ] **Checker route and disclosure:** a complete checker `(provider, model)` pair is an operator decision authorizing ordinary admitted task/tool/evidence content to that route. It does not grant access to evidence denied by its source authority or known-secret metadata and is not general DLP. Approve the exact precedence matrix below and prominently document this disclosure.
- [ ] **Budgets:** one fixed 90-second reviewer deadline includes a 30-second tool-less fast phase, provider time, at most four sequential evidence reads, and terminal submission; it explicitly excludes time waiting for a human verdict. After a human answer, a fresh short existing-operation context (proposed fixed 5 seconds) performs one binding revalidation without a model call or any new reviewer/LLM budget. These are initial product limits, not empirically optimized efficacy claims.
- [ ] **Run-local trajectory:** approve a delegation-root-owned, metadata-only trajectory ledger capped at 512 facts for the current root run. Cap exhaustion marks trajectory evidence incomplete and makes a review that requires it unresolved. No cross-run/restart fidelity is promised.
- [ ] **Public status and coverage:** approve the exact additive Go/protobuf surfaces below, including machine-only durable review status and owner-authorized `ListGuardrailCoverage`.
- [ ] **Human-readable checker rationale (deferred initial UX):** choose whether a later separately designed ephemeral owner-authorized detail channel is wanted. v1 exposes only a safe machine reason plus concern refs, not checker rationale or evidence explanation, because checker prose/evidence would be durable through `EvHook`; the machine status does not pretend to provide the prior desired rationale UX.
- [ ] **Inbound post behavior:** a post-use block/unresolved result is withheld; because the action already ran, it never offers approval for that execution. Interactive UI offers inspect metadata and retry as a new call; headless text states the result was withheld and never says to wait for a human.

## Interface contract

- **gRPC / protobuf:** Add the machine-only `GuardrailReview` and `GuardrailApprovalScope` field-5 projections and owner-authorized `ListGuardrailCoverage` RPC exactly specified below; existing fields/numbers remain unchanged.
- **Exported Go APIs / interfaces:** Add the exact `engine/agent` review types, `ToolReviewer`, `ReviewEvidenceSource`, and `Deps.ToolReviewer` below; do not widen `port.LLMRequest`, `port.HookRunner`, or `port.PermissionPolicy`.
- **Tool schemas:** Add only the review-run-local `ReadReviewEvidence` and `SubmitReviewAssessment` schemas below; neither enters a main or child catalog.
- **CLI / config:** Add only `guardrails.review`, `guardrails.checker.{provider,model}`, `--guardrails-review`, and `--guardrails-provider`; retain the current model flag and legacy paths under the exact precedence matrix.
- **Events / persistence:** Extend `HookPayload` with machine status and `PendingAsk` with the minimal serialized origin gate. This is only the fail-closed safety dependency for a live contextual ask: persist no review prose/content, add no event-log consumer, cloud-native implementation, replay repair, or restart recovery.
- **Security / authority:** Bind finite evidence handles and live Allow-once state to owner/session/environment/review/route/caller; permission denial wins, stale/unknown evidence is unresolved, and restored contextual approval fails closed.
- **Compatibility / migration:** Existing unconfigured behavior remains legacy, old clients ignore additive fields and cannot force contextual Allow always, contextual sanitize is a startup error, and no removal date or automatic rewrite is proposed.

These names and values are proposed, not existing. Implementation must amend this draft rather than substitute alternatives.

### Engine values

Add `engine/agent/review.go` with the following additive API. All strings received from a model/provider are repaired to valid UTF-8; public text is control/framing-neutralized and rune-bounded.

```go
type ReviewJob string
const (
    ReviewJobAction  ReviewJob = "action"
    ReviewJobInbound ReviewJob = "inbound"
)

type ReviewAssessment string
const (
    ReviewAcceptable ReviewAssessment = "acceptable"
    ReviewProhibited ReviewAssessment = "prohibited"
    ReviewUnresolved ReviewAssessment = "unresolved"
)

type ReviewEvidenceKind string
const (
    ReviewEvidenceScript     ReviewEvidenceKind = "script"
    ReviewEvidenceFile       ReviewEvidenceKind = "file"
    ReviewEvidencePatch      ReviewEvidenceKind = "patch"
    ReviewEvidenceUpload     ReviewEvidenceKind = "upload"
    ReviewEvidenceTranscript ReviewEvidenceKind = "transcript"
)

type ReviewPrincipalKind string
const (
    ReviewPrincipalUserTurn       ReviewPrincipalKind = "user_turn"
    ReviewPrincipalPlanVerdict    ReviewPrincipalKind = "plan_verdict"
    ReviewPrincipalApprovalVerdict ReviewPrincipalKind = "approval_verdict"
)

type ReviewPrincipalFact struct {
    Kind ReviewPrincipalKind
    Ref string
    Statement string
    PositiveVerdict bool
}

type ReviewCaller struct { Role string; Isolated bool; Capabilities []string }
type ReviewTarget struct { Kind string; Display string; Destination string }
type ReviewEvidenceMeta struct { Handle string; Kind ReviewEvidenceKind; Display string; Version string; Complete bool }
type ReviewTrajectoryFact struct { Call session.ToolCallID; Ref string; Direction string; DataClass string; TargetID string; Decision string }
type ReviewConcern struct { Ref string; Category string }
type ReviewEvidenceUse struct { Handle string; Version string; Supports []string }
type ReviewMissingEvidence struct { Ref string; Kind ReviewEvidenceKind; Handle string; Reason string }

type ToolReviewRequest struct {
    ReviewID string
    Job ReviewJob
    Event governance.HookEvent
    EffectiveCall session.ToolCall
    PrincipalFacts []ReviewPrincipalFact
    // False means at least one relevant fact was omitted; omission never authorizes.
    PrincipalFactsComplete bool
    Caller ReviewCaller
    Environment session.EnvironmentRef
    Target ReviewTarget
    Evidence []ReviewEvidenceMeta
    // False means the advertised evidence inventory is incomplete; omission never authorizes.
    EvidenceComplete bool
    Trajectory []ReviewTrajectoryFact
    TrajectoryComplete bool
}

type ToolReviewResult struct {
    Assessment ReviewAssessment
    Concerns []ReviewConcern
    Evidence []ReviewEvidenceUse
    Missing []ReviewMissingEvidence
}

type ReviewEvidenceRequest struct { ReviewID string; Handle string; Version string }
type ReviewEvidence struct { Handle string; Kind ReviewEvidenceKind; Version string; Complete bool; Content string }
type ReviewEvidenceSource interface { ReadReviewEvidence(context.Context, ReviewEvidenceRequest) (ReviewEvidence, error) }
type ToolReviewer interface { Review(context.Context, ToolReviewRequest, ReviewEvidenceSource) (ToolReviewResult, error) }
```

Add `Deps.ToolReviewer ToolReviewer`. Do not change `port.LLMRequest`, `port.HookRunner`, or `port.PermissionPolicy`.

`ReviewPrincipalFact` provenance is harness-created and immutable for one review. Its `Statement` remains untrusted task/plan text; only the fact that it came from a genuine user turn or that a specific `PresentPlan`/approval callback received a positive verdict is principal metadata. Never parse words such as “approved” from model/task/plan prose. Construct the snapshot from:

1. the most recent genuine user task turn relevant to the current call;
2. the exact `PresentPlan` call body paired by call ID with an actual positive plan verdict, if one exists;
3. actual human approval callbacks paired to their call IDs; and
4. targeted evidence selected by handle below.

Missing pairing/provenance becomes unknown, never approval. A child receives bounded, explicitly passed principal facts and never an arbitrary parent transcript. Selection omits superseded user turns unless explicitly handle-targeted. `PrincipalFactsComplete`, `EvidenceComplete`, and `TrajectoryComplete` are the concrete omission markers for their respective lists: `false` means relevant input was omitted by a bound, and an omitted fact/evidence item can never manufacture authorization. There is no whole-input byte cap.

### Evidence source and review protocol

At invocation, composition closes an evidence source over the authenticated owner, session, current environment, review ID, checker route, and caller capabilities. No private environment reference or path is sent to the reviewer. It mints a finite review-local map of opaque random/local IDs for only evidence already implicated by the effective call or trusted session state: decoded script, touched file, proposed patch, upload destination/body metadata, or selected child-local transcript excerpt. It performs no broad discovery.

The initial request includes the exact handle inventory and metadata (`handle`, kind, bounded display label, observed version, completeness). The model may request only an advertised handle; it cannot supply a path, URL, session ID, call ID, or guessed object ID. The source rejects unknown kind, wrong review, wrong version, wrong owner/session/environment/route, and expired handles before reading. Metadata precedes bodies. Existing source authority and known-secret classification can deny disclosure; configuring a checker provider is not a secret-reading grant. Reviewer access is never wider than the worker whose call is reviewed.

The fast phase is tool-less and its entire output must be one JSON object (or one lone `json` fence):

```json
{"assessment":"acceptable|prohibited|unresolved","concerns":[{"ref":"C1","category":"..."}],"request_evidence":[{"handle":"opaque-advertised-handle","version":"observed-version"}]}
```

A terminal acceptable/prohibited fast result must request no evidence. An unresolved result may request only advertised handles. The investigative phase exposes two run-scoped synthetic tools, never shared-catalog tools:

- `ReadReviewEvidence`: `{"review_id":"...","handle":"...","version":"..."}` → `{"handle":"...","kind":"...","version":"...","complete":true|false,"content":"..."}`.
- `SubmitReviewAssessment`: `{"assessment":"acceptable|prohibited|unresolved","concerns":[{"ref":"C1","category":"..."}],"evidence":[{"handle":"...","version":"...","supports":["C1"]}],"missing_evidence":[{"ref":"M1","kind":"...","handle":"...","reason":"..."}]}`.

Only a valid `SubmitReviewAssessment` terminates investigation. A wrong tool, malformed/wrong-whole-output JSON, duplicate/unknown ref, unsupported kind, missing submit, empty turn, timeout, or exhausted read/turn budget is terminal `unresolved`. No independent per-stage deadline may multiply the fixed 90-second reviewer budget; human waiting is outside that budget and receives only the one post-answer revalidation. Native reads report truncation/completeness; `acceptable` is invalid when required evidence is incomplete.

### Default coverage matrix

Explicit rules replace the entire relevant default set. The following is the proposed complete matrix; `block` is enforcing and `advisory` records/returns the result without withholding it.

| Review mode | Tool/category | Phase/job | Default mode |
|---|---|---|---|
| legacy | Shell | pre/action | existing default; skip only a read-only Shell pre-check in legacy mode |
| legacy | WebSearch, WebFetch, `mcp__*` | existing configured/default phases | existing legacy behavior |
| legacy | `FetchMcpResource` | post/inbound | existing legacy behavior |
| legacy | `CallMcpWithQuery` | pre/action and post/inbound | existing legacy behavior |
| contextual | Shell, including read-only Shell | pre/action | block; the contextual rubric receives the ordinary permission decision and Shell read-only metadata and decides on that bounded context—there is no broad read-only bypass |
| contextual | WebSearch, WebFetch, `mcp__*`, `CallMcpWithQuery` | pre/action | block |
| contextual | direct local mutations: Edit, Write, Copy, Move, Remove | pre/action | block |
| contextual | delegation: Subagent, Parallel, Team | pre/action | block |
| contextual | Shell, WebSearch, WebFetch, `mcp__*`, `FetchMcpResource`, `CallMcpWithQuery` | post/inbound | block |
| contextual | local reads: Read, ListDir, Grep, Glob | post/inbound | advisory |

The contextual action rubric must treat policy denial, configured Ask, plan mode, system-scope failure, or non-read-only Shell metadata as non-authorizing; its read-only metadata can reduce evidence needs but cannot override the ordinary policy. No local-read action pre-review is implied.

### Dispatch and approval lifecycle

Recommended pre-use order:

1. evaluate existing permission and plan gates on the requested call;
2. invoke trusted inner PreToolUse mutation exactly once;
3. if arguments changed, evaluate the ordinary permission policy again on the exact effective call, preserving deny dominance, configured Ask, plan mode, and the existing Shell system-scope check;
4. construct immutable principal/evidence/trajectory snapshots and run contextual review;
5. validate result and evidence bindings;
6. immediately before a live human ask, re-read every required evidence version and compare the exact effective-call binding within the review's remaining 90-second deadline; after a human Allow-once, repeat that revalidation once using a fresh fixed 5-second existing-operation context, with no reviewer call and no new LLM budget;
7. execute that exact call without rerunning mutation.

A policy deny at either evaluation wins. An Ask is surfaced normally and does not become allowed because a reviewer said acceptable. Stale/missing/version-unsupported or unverifiable evidence is `unresolved`; after a human answer it fails the old approval and prevents execution, rather than starting another review loop. A later human/user-directed attempt is a new review. Remote tools assert no atomicity. Filesystem revalidation narrows the race but does not claim bytes are snapshotted: an arbitrary POSIX writer can still change a file after revalidation unless that tool's existing mutation CAS covers it; this true residual remains.

Add serialized, additive `ReviewOrigin string` (`json:"review_origin,omitempty"`) to `session.PendingAsk`, with constants `contextual_guardrail` and `effective_mutation`. The field is only an origin/fail-closed gate, not persisted review identity. The live `Run` holds the exact review ID, effective-call digest, required evidence handles+versions, and tool pointer. `resolvePendingCall` checks `ReviewOrigin` before the current generic `HookOriginated` branch: after process loss, Deny remains Deny; Allow once/Allow always produce a fixed unresolved tool error and do not execute. Same-process handlers accept Allow once/Deny only; injected Allow always is rejected before `Policy.Learn`, `HookApprovalLearner`, execution, and approval-log emission. Existing legacy waiver normalization is corrected independently, but this plan does not make legacy approval replay safe.

### Structured status, disposition, and safe text

Keep separate dimensions:

- mode: `block|advisory|legacy_sanitize`;
- assessment: `acceptable|prohibited|unresolved`;
- inspection: `complete|skipped|unavailable`;
- disposition: `execute|ask|deny|withhold|pass_advisory`;
- reason: `none|rule_skip|checker_down|compatibility_unresolved|timeout|bad_output|evidence_exhausted|turn_exhausted|stale_binding|unauthorized_evidence|incomplete_evidence|unsupported_version|invalid_identity|human_denied|human_unavailable|allow_always_unsupported`.

`compatibility_unresolved` is the only unresolved machine reason eligible for an explicit compatibility opt-out: it means a legacy-compatible checker operational failure that an operator explicitly configured with `failClosed:false` or `onCheckerDown:warn`, and is not a prohibited assessment, denied disclosure, invalid evidence, incomplete identity/authority, or other structural failure.

| Phase/mode | Assessment/inspection | Interactive disposition | Headless disposition |
|---|---|---|---|
| pre/block | acceptable + complete | execute | execute |
| pre/block | prohibited or unresolved | ask Allow-once/Deny | deny (`human_unavailable` when applicable) |
| pre/advisory | any | pass_advisory | pass_advisory |
| post/block | acceptable + complete | execute result | execute result |
| post/block | prohibited or unresolved | withhold; offer inspect/retry as a new call | withhold |
| post/advisory | any | pass_advisory | pass_advisory |
| any explicit `failClosed:false` or checker-down `warn` compatibility opt-out | `checker_down` or `compatibility_unresolved` only; never `prohibited`, denied disclosure, invalid/incomplete evidence, or invalid/incomplete authority/identity | pass_advisory with opt-out reason | pass_advisory with opt-out reason |
| legacy sanitize | existing legacy verdict | existing legacy rewrite rules | existing legacy rewrite rules |

A fail-open compatibility opt-out is not a bypass for a genuine prohibited finding or a structural failure. Source-denied and known-secret evidence is never released to the checker, including on either opt-out.

The main model receives fixed, status-specific text only. Pre deny: `Guardrail review REVIEW-ID did not authorize this action (REASON; concerns REFS). Choose a materially different action.` Post withhold: `Guardrail review REVIEW-ID withheld this result (REASON; concerns REFS). Do not use or relay the withheld result; issue a new call only after changing the cause.` Human denial adds `The operator denied this call; do not retry it through another tool or worker.` These messages use the fixed machine `REASON`; the model has no status-inspection tool and is never directed to perform an opaque reference lookup. A user-facing Mecatui/RPC surface may direct an operator to `/guardrails`. Headless wording never says to wait for approval. Checker prose, evidence summaries, intermediate assistant text, and suggested commands never enter model text, events, snapshots, logs, diagnostics, or metrics.

### Protobuf and coverage

Add these exact protobuf enum declarations (names and numeric values are part of this proposed interface):

```proto
enum GuardrailReviewMode {
  GUARDRAIL_REVIEW_MODE_UNSPECIFIED = 0;
  GUARDRAIL_REVIEW_MODE_BLOCK = 1;
  GUARDRAIL_REVIEW_MODE_ADVISORY = 2;
  GUARDRAIL_REVIEW_MODE_LEGACY_SANITIZE = 3;
}
enum GuardrailAssessment {
  GUARDRAIL_ASSESSMENT_UNSPECIFIED = 0;
  GUARDRAIL_ASSESSMENT_ACCEPTABLE = 1;
  GUARDRAIL_ASSESSMENT_PROHIBITED = 2;
  GUARDRAIL_ASSESSMENT_UNRESOLVED = 3;
}
enum GuardrailInspectionState {
  GUARDRAIL_INSPECTION_STATE_UNSPECIFIED = 0;
  GUARDRAIL_INSPECTION_STATE_COMPLETE = 1;
  GUARDRAIL_INSPECTION_STATE_SKIPPED = 2;
  GUARDRAIL_INSPECTION_STATE_UNAVAILABLE = 3;
}
enum GuardrailDisposition {
  GUARDRAIL_DISPOSITION_UNSPECIFIED = 0;
  GUARDRAIL_DISPOSITION_EXECUTE = 1;
  GUARDRAIL_DISPOSITION_ASK = 2;
  GUARDRAIL_DISPOSITION_DENY = 3;
  GUARDRAIL_DISPOSITION_WITHHOLD = 4;
  GUARDRAIL_DISPOSITION_PASS_ADVISORY = 5;
}
enum GuardrailReason {
  GUARDRAIL_REASON_UNSPECIFIED = 0;
  GUARDRAIL_REASON_NONE = 1;
  GUARDRAIL_REASON_RULE_SKIP = 2;
  GUARDRAIL_REASON_CHECKER_DOWN = 3;
  GUARDRAIL_REASON_COMPATIBILITY_UNRESOLVED = 4;
  GUARDRAIL_REASON_TIMEOUT = 5;
  GUARDRAIL_REASON_BAD_OUTPUT = 6;
  GUARDRAIL_REASON_EVIDENCE_EXHAUSTED = 7;
  GUARDRAIL_REASON_TURN_EXHAUSTED = 8;
  GUARDRAIL_REASON_STALE_BINDING = 9;
  GUARDRAIL_REASON_UNAUTHORIZED_EVIDENCE = 10;
  GUARDRAIL_REASON_INCOMPLETE_EVIDENCE = 11;
  GUARDRAIL_REASON_UNSUPPORTED_VERSION = 12;
  GUARDRAIL_REASON_INVALID_IDENTITY = 13;
  GUARDRAIL_REASON_HUMAN_DENIED = 14;
  GUARDRAIL_REASON_HUMAN_UNAVAILABLE = 15;
  GUARDRAIL_REASON_ALLOW_ALWAYS_UNSUPPORTED = 16;
}
```

Add:

```proto
message GuardrailRef { string ref = 1; string category = 2; }
message GuardrailReview {
  string review_id = 1;
  string job = 2;
  GuardrailReviewMode mode = 3;
  GuardrailAssessment assessment = 4;
  GuardrailInspectionState inspection = 5;
  GuardrailDisposition disposition = 6;
  GuardrailReason reason = 7;
  string rule_id = 8;
  string rule_origin = 9;
  string scope = 10;
  string checker_provider_id = 11;
  string checker_model_id = 12;
  repeated GuardrailRef concerns = 13;
  repeated GuardrailRef evidence = 14;
  repeated GuardrailRef missing_evidence = 15;
}
message GuardrailApprovalScope {
  string review_id = 1;
  bool allow_once_only = 2;
  string caller_role = 3;
  string environment_display = 4;
  string destination_display = 5;
  string rule_id = 6;
}
```

Add `GuardrailReview guardrail = 5` to `Hook` and `GuardrailApprovalScope guardrail = 5` to `PermissionAsk`. In `engine/session`, define a named `GuardrailReviewPayload` with the same machine fields as `GuardrailReview` (including the ref slices) and add `Guardrail *GuardrailReviewPayload` to `HookPayload`; define a named `GuardrailApprovalScopePayload` with the matching approval-scope fields. `engine/session` owns these projection values and must not import `engine/agent`. Do not put either on `ApprovalPayload`. Every dynamic string is valid UTF-8, control/framing-sanitized, refs are grammar-validated, and display fields use the existing permission-reason bound of 240 runes. Since `EvHook` is durably appended by relays, no rationale, summary, next-action text, args, transcript, or evidence body is permitted in `GuardrailReview` or `Event.Text`. Existing clients ignore field 5 and retain `Hook.decision` plus fixed `Event.text`; old clients may send Allow always, but the server-side origin gate rejects it.

Add to `HarnessService`:

```proto
rpc ListGuardrailCoverage(ListGuardrailCoverageRequest) returns (ListGuardrailCoverageResponse);
message ListGuardrailCoverageRequest { string session_id = 1; }
message GuardrailCoverageEntry {
  string tool = 1;
  string phase = 2;
  string job = 3;
  string mode = 4;
  string rule_id = 5;
  string rule_origin = 6;
  string inspection = 7;
  string reason = 8;
}
message ListGuardrailCoverageResponse {
  bool enabled = 1;
  string review = 2;
  string checker_provider_id = 3;
  string checker_model_id = 4;
  repeated GuardrailCoverageEntry entries = 5;
}
```

The service owner-authorizes `session_id`, resolves that session's actual assembled catalog/rules, and returns bounded names/status only—no args, paths, exact environment refs, or evidence handles. Mecatui `/guardrails` renders it; proposed non-interactive client command is `mecatui guardrails --session SESSION_ID`; all headless composition roots emit one build-time diagnostics summary with mode, route, default-vs-explicit rules, and included/skipped counts. A stable model-visible system-prompt note lists covered tool/phase categories, says denial cannot be bypassed through another tool/worker, and tells the model to choose a materially different action. Real factory-path tests assert that owning prompt layer.

### Configuration and precedence

Smallest new YAML surface:

```yaml
guardrails:
  review: contextual # or legacy
  checker:
    provider: openrouter
    model: openai/gpt-5.1
```

CLI adds `--guardrails-review`, `--guardrails-provider`, and retains `--guardrails-model` as the checker model half. No removal date or automatic rewrite is proposed.

| Priority | Condition | Result |
|---|---|---|
| 1 | effective operator kill switch: `--guardrails=off` or the existing operator-tier YAML `guardrails.disabled: true` after normal operator folding | off, regardless of every lower source |
| 2 | `--guardrails-provider` present | `--guardrails-model` is required and the complete CLI pair wins; provider-only is a startup error and does not fill from YAML/legacy |
| 3 | complete CLI model/provider pair absent, but both YAML `guardrails.checker` members present | the complete YAML pair wins and is the exact checker route; a partial YAML pair is a startup error and does not fill from legacy |
| 4 | `--guardrails-model` alone, or no new pair; current legacy `guardrails.model` / `models.slots.guardrail` | preserve the established legacy model precedence and routing. A lone CLI model participates only as the existing legacy model override; it is not an implicit provider pair. Without an explicit new pair, legacy retains its established session-inherited route behavior and emits a visible warning that this known routing/disclosure risk remains. A complete new pair is an explicit route-fix opt-in even when `review: legacy`. |
| 5 | any configured legacy model/slot alias cannot resolve | startup error; never silently turn guardrails off |
| 6 | truly no model is configured | guardrails off |

`--guardrails-review` overrides YAML `review`; absent means `legacy`. Selecting `contextual` without a complete exact route is an error. `guardrails.checker` and `review` remain operator-tier, strict, and project blocks are ignored with the existing warning posture. The exact route opts into provider disclosure as described in the Human decision; it does not inherit a session provider. The explicit-pair route behavior is a compatibility change for a legacy deployment that elects it; unpaired legacy configuration is intentionally unchanged. No speculative “one cycle” removal is proposed.

### Run-local trajectory ledger

One ledger is owned by the delegation root `Run`; workers report through a harness callback, not parent-model events. Each of at most 512 facts contains only call/ref, direction (`read|write|send|delegate`), known data class or `unknown`, bounded target ID, and decision category. It stores no content, args, path, transcript, rationale, or evidence body. Reviewer handle metadata may address authorized facts; evidence bodies remain local to the reviewed child. Deduplicate exact tuples, clear at root-run termination after child drain, and never use session/tool IDs as metric labels. On cap exhaustion set `TrajectoryComplete=false`; an assessment requiring omitted trajectory is invalid and resolves unresolved. Implementation must add the ledger and one-review evidence map to List 1 in `docs/adr/0027-cloud-native.md`, and the trajectory ledger to List 2 as `reset-by-design`; that prospective inventory update documents lifetime and cleanup without adding cloud persistence.

## In scope — 7 scenarios

### Scenario 1 — effective action and genuine authority

This scenario changes the live ordering documented by [ADR 0334](../adr/0334-contextual-investigative-guardrails.md); permission remains authoritative.

- AC1.1: immutable context includes only provenance-tagged genuine user turns, actually approved plan-call bodies, actual approval callbacks, and targeted evidence; missing origin is unknown and child context is explicitly bounded.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_PrincipalFacts`
- AC1.2: trusted mutation runs once; ordinary permission and system-scope checks re-evaluate exact effective args; deny/ask/plan wins and execution receives byte-identical reviewed args.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_EffectivePermissionOrder`
- AC1.3: legacy Shell waiver normalization preserves quoted whitespace and non-Shell canonicalization rejects duplicate keys, without claiming replay correctness.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_LegacyWaiverIdentity`
- AC1.4: contextual sanitize is rejected and reviewer-proposed actions are never adopted.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario1_ContextualSanitizeRejected`

### Scenario 2 — finite evidence and TOCTOU

Evidence remains inside the quarantine and source authority defined by [ADR 0334](../adr/0334-contextual-investigative-guardrails.md).

- AC2.1: initial review sees exact handle inventory/metadata and only advertised review-local handles can be read; wrong-kind/version/review/owner/session/environment/route access fails before read.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_EvidenceHandleAuthority`
- AC2.2: the source mints only call/session-implicated script/file/patch/upload/transcript handles, never arbitrary discovery, Shell, network, secrets, or parent transcript for a child.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_NoAuthorityExpansion`
- AC2.3: required refs+versions are revalidated immediately before ask within the review deadline, then once after a human Allow-once with a fresh fixed 5-second existing-operation context and no new reviewer budget; stale, unsupported, truncated, missing, changed, or unverifiable evidence invalidates the old approval and is unresolved with no automatic retry or false CAS claim.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_Revalidation`
- AC2.4: exact 90s reviewer/30s/four-read/six-turn limits, exclusion of human waiting, and every malformed/exhausted path terminate unresolved; the post-answer revalidation is fixed once and does not call the reviewer. No content-byte/session-check caps exist.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario2_TotalBudgetAndFailures`

### Scenario 3 — live approval safety

The origin gate narrows, rather than silently widening, the current approval contract in [ADR 0062](../adr/0062-guardrails-approve-once.md).

- AC3.1: same-process contextual ask binds review ID, effective-call digest, evidence versions, caller and destination; Allow once revalidates and executes once without rerunning mutation, Deny does not execute.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_LiveAllowOnceBinding`
- AC3.2: contextual Allow always is rejected before policy/hook learning, execution, and durable allow-always emission, including an old/injected client verdict.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_AllowAlwaysRejected`
- AC3.3: restored contextual/effective-mutation pending origin fails closed before generic hook direct-execute; no restart recovery is claimed.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_RestartOriginGate`
- AC3.4: existing permission replay remains semantically separate and no contextual event can be consumed as a permission allow.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario3_ReplayIsolation`

### Scenario 4 — main/worker trajectory and disclosure

Worker isolation and inward dependencies remain those of the [agent-loop architecture](../architecture/agent-loop.md).

- AC4.1: rules cover main, read-only/writable Subagent, Parallel branches, Team members and lead; checker engines are inert; delegation calls are reviewed actions.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_MainWorkerCoverage`
- AC4.2: the 512-fact root ledger correlates sensitive-read/send and denied alternate-tool/child retries without moving content across worker boundaries; incomplete required trajectory is unresolved.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_BoundedTrajectory`
- AC4.3: composition constructs the exact configured checker route; partial/mismatched routes fail startup and no session-provider inheritance or `LLMRequest` widening occurs.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_ExactRoute`
- AC4.4: route configuration permits ordinary admitted disclosure only; denied/known-secret evidence never reaches provider invocation and reviewer authority never exceeds worker authority.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario4_DisclosureAuthority`

### Scenario 5 — separate status, durable minimization, and coverage

Projection extends the generic advisory visibility of [ADR 0051](../adr/0051-guardrails-advisory-tui-visibility.md) without persisting checker prose.

- AC5.1: every mode/assessment/inspection/disposition/reason mapping, including explicit legacy fail-open opt-outs and post withholding, is table-tested for interactive and headless runs.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_StatusMapping`
- AC5.2: durable events/proto contain machine categories and validated refs only; fixed text, UTF-8 repair, control/framing sanitation and 240-rune display bounds hold; no checker prose survives.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_DurableProjectionNoContent`
- AC5.3: `ListGuardrailCoverage` owner-authorizes and reflects the actual assembled session catalog/rules; `/guardrails`, client CLI, build summary, and old-client fallback expose no private refs.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_RealCoverageSurface`
- AC5.4: metrics have bounded mode/outcome/job/phase/route labels and counts/latency/reads/tokens, never IDs, args, paths, text, rationale, transcript or evidence.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario5_MetricsNoContent`

### Scenario 6 — inbound review remains separate

Post-use behavior preserves the effective-payload invariant in the [hooks and guardrails architecture](../architecture/hooks-and-guardrails.md).

- AC6.1: action approval cannot authorize inbound content; jobs use distinct prompts and assessments.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_SeparateJobs`
- AC6.2: enforcing prohibited/unresolved post content is withheld identically in recorded history, stream and model view; post never asks approval for the action already run.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_WithholdEffectivePayload`
- AC6.3: explicit/default rule replacement and local-read advisory coverage show skipped/unavailable distinctly from acceptable.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_DefaultAndExplicitCoverage`
- AC6.4: model-visible pre/post/denial text is fixed, headless-accurate, and denial includes anti-circumvention guidance.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario6_ModelText`

### Scenario 7 — offline protocol regression and prompt discoverability

Ordinary verification follows the offline-test and model-visible-affordance requirements in [AGENTS.md](../../AGENTS.md).

- AC7.1: a versioned offline scenario corpus covers authorization provenance, quoted whitespace, mutation, scripts/uploads, injection, stale/unauthorized evidence, alternate-worker bypass and read-to-send trajectory. It proves protocol/regression behavior, not model discrimination or efficacy.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_ProtocolCorpus`
- AC7.2: fake reviewer/mock provider tests cover all output/tool/timeout/exhaustion failures without network or paid inference; any real quality benchmark is optional and separately authorized.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_OfflineFailures`
- AC7.3: real factory-path tests assert action and inbound system prompts contain evidence-handle discovery, terminal submission, safe stop, untrusted-data, no-shell/network, coverage and anti-bypass clauses in their owning layer; an offline model-facing adversarial e2e proves forged reviewer framing/prose cannot enter the worker prompt or authorize execution.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_FactoryPromptContracts`
- AC7.4: configuration precedence tests cover the CLI and operator-YAML kill switches, complete/partial CLI and YAML pairs, lone CLI model legacy precedence, invalid-alias startup failure, retained session-inherited legacy routing plus its warning, explicit-pair route opt-in, disclosure warning, legacy default, contextual opt-in, sanitize rejection and every failure default.
  - verify: `TestADR_0334_ContextualGuardrails_Scenario7_ConfigMatrix`

## Out of scope

| Item | Defer-to | Boundary |
|---|---|---|
| Approval provenance/replay redesign | Separate approval-security plan | No contextual Allow always, no contextual permission replay, no generic hook direct-execute after restart. |
| Restart-complete reviewer/evidence/trajectory state and gRPC approval recovery | Cloud-native phase | Run-local state resets; restored contextual asks fail closed. |
| Replica coordination, durable policy generation, leases/drivers/background recovery | Cloud-native phase | No new service, driver, event-log consumer, or persistence protocol. |
| General reviewer Shell/network, DLP, secret discovery, repository exploration | Separate security design | Finite authority-bound handles only; known-secret denial is not general secret detection. |
| Path-scoped guardrail rule language | Separate rule-language design | Tool-name matching and explicit replacement remain. |
| Live-model efficacy evaluation | Optional, separately authorized | Offline fixtures are protocol/regression and dataset-readiness only. |

## Definition of done

1. Every Human decision is resolved by human review; the ADR remains Proposed and this plan remains draft until then.
2. Implementation updates the prospective CLOUD-NATIVE inventory rows, generated protobuf, engine API snapshots and `engine/CHANGELOG.md`, configuration reference, owning user docs, and model-visible prompt tests.
3. `task lint`, `task test`, `task docs`, `task api:check`, `task ac-trace-strict`, `task site:build`, and `go run ./cmd/mecademo` pass; tests remain offline.
4. The implementation PR links the approved Plan / Interface commit and stops on contract drift.

## Primary-source inspiration

These describe comparable bounded review/guardrail patterns; they are design input, not authority for Mecatl's trust decisions:

- [Codex auto-review](https://developers.openai.com/codex/sandboxing/auto-review)
- [OpenAI Agents SDK guardrails](https://openai.github.io/openai-agents-js/guides/guardrails/)
