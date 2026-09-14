# ADR 0334 — Contextual investigative guardrails

- Status: Proposed
- Date: 2026-09-14
- Scope: minimal live fail-closed origin-gate safety dependency for guardrail authority, effective-action ordering, evidence access, main/worker trajectory, checker routing, approval limits, and structured observability; not a cloud-native implementation or replay repair
- Proposed supersession: ADR 0021's one-payload/tool-less classifier architecture; ADR 0051's generic advisory-only UI projection; ADR 0060's Shell payload-classifier default; ADR 0062's waiver-key normalization and checker-reason-in-tool-result behavior
- Superseded by: —

## Context

The live guardrail path is a quarantined, tool-less, one-turn classifier over one tool-boundary payload. It lacks genuine user-turn provenance, actual plan/approval callbacks, exact target and caller authority, worker context, and prior sensitive-read or denial facts. It cannot reliably distinguish an authorized release from an unauthorized one, connect a read to a later send, or identify a denied action retried through another tool or child.

The live path also has two trust defects. `normalizeWaiverKey` collapses whitespace inside quoted Shell literals, allowing different decoded commands to share identity. Checker-authored block rationale is copied into a tool error and reaches the parent model as trusted-looking text. In addition, trusted PreToolUse mutation currently bypasses ordinary permission re-evaluation by design; contextual review of the effective action requires a deliberate change to that ordering, not an accidental reinterpretation of the current contract.

Some reviews need narrowly scoped evidence—the exact script implicated by a call, a proposed patch/upload, one addressed file, or selected local transcript evidence. Giving a reviewer broad filesystem, transcript, Shell, network, or secret access would make it a second privileged agent. Provider selection is also disclosure authority: choosing a checker route does not itself grant access to evidence that the caller or source cannot disclose.

Human approval is constrained by an existing restart hazard. A live hook approval may learn through `HookApprovalLearner`; `resolvePendingCall` learns permission policy before branching on `HookOriginated`, and its serialized hook marker directly executes on resume. `replayApprovals` later consumes metadata-only Allow-always records without guardrail provenance. A wider contextual waiver cannot safely ride those paths while approval-provenance/replay repair is deferred.

Guardrail `EvHook` events are durably appended by relays. Putting rationale, summaries, or reviewed content in their payload is therefore durable content storage, even if a UI calls it “human-facing.” The initial contextual surface must use machine categories and opaque refs only unless a separate ephemeral, owner-authorized detail API is designed and inventoried.

ADRs 0049 and 0050 removed silent per-session check-count and reviewed-input caps. Explicit total review time/step limits are compatible only if exhaustion is visible and never grants. Cloud-native persistence, replica coordination, and restart-complete reviewer state remain deferred.

## Proposed decision

This ADR records the exact recommended design for Plan / Interface review. Because material choices remain unchecked in the [acceptance plan](../acceptance/contextual-guardrails.md), none of the following is implementation authority yet.

### Separate jobs and immutable authority context

Use two jobs over shared protocol machinery:

1. **Action review** evaluates the exact outbound call after trusted argument mutation and deterministic permission re-evaluation, before execution.
2. **Inbound review** evaluates a result before it reaches recorded history, client stream, or parent model. It never inherits action approval and cannot approve an action that already ran.

Each review gets an immutable, bounded snapshot. Harness-created provenance says that text came from the recent genuine user task turn, from a `PresentPlan` call body paired by call ID with a positive human plan verdict, or from an actual approval callback. The text remains untrusted content; provenance does not make model/task/plan prose a principal assertion. Never infer approval by parsing words such as “approved.” Missing origin or pairing is unknown. Children receive explicitly passed principal facts and child-local evidence, never arbitrary parent transcript.

Relevance is deterministic: recent genuine task turn, actual approved-plan reference, actual approval references, evidence implicated by the effective call, and targeted prior trajectory facts. `PrincipalFactsComplete`, `EvidenceComplete`, and `TrajectoryComplete` report omission for the corresponding lists; `false` means a relevant item was bounded out and can never create authorization. There is no guardrail-wide input-byte cap.

### Effective-call ordering and sanitize

Recommended pre-use order is: original permission/plan gate; one trusted inner mutation; ordinary permission plus the existing Shell system-scope check on exact effective args; contextual review; evidence/binding revalidation; execute exact args without rerunning mutation. Deny dominance, configured Ask, plan mode, and every existing permission restriction continue to bind. A reviewer verdict never authorizes a policy-denied action.

This intentionally changes the current durable trusted-hook exception and therefore requires the acceptance-plan choice. Legacy sanitize keeps its current checker rewrite only under `review: legacy`. `review: contextual` rejects `mode: sanitize` at startup; reviewer-proposed commands are display-invalid and never adopted. Operators migrate that rule to contextual `block`/`advisory` or retain legacy mode.

### Finite evidence authority and protocol

Composition binds a per-review evidence source to authenticated owner, session, current environment, caller authority, review ID, and exact checker route. It sends no private environment ref/path to the model. Invocation mints a finite map of opaque review-local handles only for scripts, files, patches, uploads, or transcript excerpts already implicated by the effective call or trusted session evidence. It performs no arbitrary discovery.

The initial request contains the complete allowed handle inventory and bounded metadata. The model can select only an advertised handle, never guess a path, URL, ID, or ref. Reads reject wrong review, kind, version, owner, session, environment, route, authority, or expiry before access. Reviewer authority is no wider than the worker being reviewed. Known-secret/source-denied evidence is not read or sent; this is not a claim of general DLP or secret detection.

The fast phase is tool-less and whole-output JSON parsed. An unresolved result may enter investigation. Investigation exposes only one evidence-read tool and one structured terminal-submit tool; only the submit tool terminates. Malformed output, wrong tool, invalid/duplicate refs, unauthorized/stale evidence, timeout, empty turn, missing submit, or limit exhaustion is `unresolved`.

One fixed 90-second reviewer deadline includes the 30-second fast phase, all evidence reads, provider responses, and final submission, but excludes waiting for a human verdict. At most four evidence reads are sequential and at most six model turns occur: fast, up to four one-read turns, final submit. After a human Allow-once, one fresh fixed 5-second existing-operation revalidation checks the binding without calling the reviewer or granting new LLM budget. These are an initial product proposal, not an empirically optimized quality claim. Native read truncation/completeness is retained; required incomplete evidence invalidates `acceptable`.

The assessment names required evidence handles and observed versions. The dispatcher revalidates them immediately before a human ask within the reviewer deadline, and once after a human answer with that fixed 5-second context. Staleness or unverifiability invalidates the old approval and ends the current attempt unresolved; it does not silently restart a fresh-budget loop. A future user/human attempt creates a new review. This narrows but does not eliminate TOCTOU: arbitrary POSIX writers may race after revalidation unless an existing tool CAS applies, and remote actions make no atomicity claim.

### Approval restriction and deferred recovery

Contextual asks support Allow once and Deny only. The server rejects injected Allow always before learning, execution, or an allow-always audit event, including requests from old clients. A live Run binds review ID, effective-call digest, evidence handles/versions, caller, and destination and revalidates on Allow once. Mutation does not rerun.

Add a minimal serialized pending-ask origin marker for `contextual_guardrail` and `effective_mutation`. It stores no review identity or evidence. `resolvePendingCall` checks it before the generic `HookOriginated` direct-execute branch: Deny remains safe, while any restored Allow fails closed as unresolved. No gRPC/restart approval recovery is promised. If this origin gate is not approved, interactive contextual approval is blocked on the deferred approval-provenance design.

Correct legacy waiver normalization independently, but do not claim that legacy hook waiver/replay is safe. Contextual events never enter permission `Policy.Learn` or `replayApprovals`.

### Run-local trajectory

A delegation-root Run owns a metadata-only ledger capped at 512 deduplicated facts. Workers export facts through a harness callback, not parent-model events. A fact contains call/ref, direction, known data class or unknown, bounded target ID, and decision category—no content, args, paths, transcript, or rationale. Child evidence remains local; authorized reviewer metadata may refer to it. Cleanup follows child drain at root-run termination. Cap exhaustion marks trajectory incomplete, making any assessment that requires omitted trajectory unresolved. IDs are not metric labels.

This ledger and the one-review evidence map must be added prospectively to the cloud-native resource inventory during implementation; the ledger's restart decision is reset-by-design. This is lifecycle documentation, not cloud persistence or a new owner service.

### Structured outcome and safe projection

Represent mode, assessment, inspection state, disposition, and machine reason independently. Do not collapse them into one status list. The acceptance plan defines their exact enums and mapping table, including explicit fail-open compatibility opt-outs. Enforcing pre-use prohibited/unresolved asks only when an interactive human exists; headless denies. Enforcing post-use prohibited/unresolved withholds because execution already happened; it offers inspect/retry as a new call, never approval for that execution. Limit exhaustion never grants.

Model text is fixed and status-specific. It contains a review ID, validated concern refs, machine reason, corrective guidance, and denial anti-circumvention language where applicable. It contains no checker prose. Headless text never tells the model to wait for a human. Checker assistant/intermediate text, rationale, evidence summaries, suggested actions, and bodies stay private to review execution and are discarded.

The durable `Hook` projection carries only machine dimensions, bounded rule/route identifiers, and validated concern/evidence/missing refs. All public strings receive valid-UTF-8 repair, control/framing neutralization, and existing permission-reason bounds. **Unapproved UX choice:** v1 intentionally exposes only safe machine reasons and refs, not the previously requested human-readable checker rationale/evidence explanation. Checker prose would become durable through `EvHook`; an ephemeral owner-authorized detail channel requires a separate design and is not invented here. Approval reason is synthesized from machine metadata. Existing clients retain `Hook.decision` and fixed `Event.text`.

Add owner-authorized `ListGuardrailCoverage(session_id)` over the session's actual assembled catalog and rules. It returns bounded tool/phase/job/mode/rule/inspection metadata without args, paths, private refs, or evidence handles. Mecatui `/guardrails`, a non-interactive Mecatui client invocation, and one build-time headless diagnostics summary expose real coverage. A system-prompt note tells the model which categories are covered and that denial must not be bypassed through another tool/worker; real factory-path tests pin the owning prompt layer.

### Configuration and route disclosure

Add only `guardrails.review: legacy|contextual` and exact `guardrails.checker.{provider,model}`, with matching CLI review/provider flags and the current model flag as the model half. Absence of `review` remains legacy, so existing behavior does not silently change; contextual use is explicit.

Precedence is fail-closed: the effective operator kill switch (CLI or existing operator-tier YAML `guardrails.disabled` folding); complete CLI provider+model pair (a provider flag without the model is an error); complete YAML pair (a partial pair is an error); then the established legacy model precedence. A lone `--guardrails-model` remains only the legacy model override, never an implicit pair. Without an explicit new pair, legacy retains its established session-inherited route behavior and emits a visible warning that its known routing/disclosure risk remains; a complete new pair explicitly opts into exact routing, even under `review: legacy`. An invalid configured legacy alias is a startup error, not silent off; only truly absent model configuration is off. No removal date or automatic rewrite is decided.

The exact checker route is an operator disclosure choice for otherwise-authorized ordinary tool/task/evidence content. It does not widen source authority, admit known-secret evidence, inherit per-session provider authority, or add provider-private fields to `port.LLMRequest`.

### Defaults and evaluation claims

The proposed contextual default coverage and failure behavior remain explicit unchecked choices in the plan. The complete proposed contextual set is action-pre `block` for Shell (including read-only Shell), WebSearch, WebFetch, `mcp__*`, `CallMcpWithQuery`, direct local mutations (Edit/Write/Copy/Move/Remove), and delegation (Subagent/Parallel/Team); inbound-post `block` for Shell, WebSearch, WebFetch, `mcp__*`, `FetchMcpResource`, and `CallMcpWithQuery`; and inbound-post `advisory` for Read/ListDir/Grep/Glob. In contextual mode, the Shell rubric consumes the ordinary policy decision and read-only metadata; it does not broadly skip read-only Shell. Explicit rules replace the default set. The legacy read-only-Shell pre skip and all other legacy defaults remain compatibility behavior only in legacy mode. Explicit `failClosed:false`/checker-down warning opt-outs apply only to `checker_down` or defined `compatibility_unresolved` operational failures; they never pass a prohibited finding or denied disclosure/structural authority, identity, or evidence failure, and denied/known-secret evidence is never released.

Offline fake-reviewer/mock-provider scenarios prove protocol behavior, regressions, prompt wiring, and dataset readiness. They do not prove model discrimination or security efficacy. Any real quality benchmark requires separate authorization and is not an ordinary test gate.

## Consequences

The reviewer can evaluate actual effective actions and narrowly gather relevant evidence without becoming a general agent. Deterministic permission policy remains authoritative. Main/worker trajectory can expose read-to-send and alternate-worker bypasses within one root run. Operators can distinguish assessment, inspection, disposition, and failure cause and inspect actual coverage without persisting checker prose.

Costs include additive engine/protobuf/API work, dispatch ordering changes, worker callbacks, run-local bounded state, exact route migration, and additional latency/spend. Contextual interactive approval is intentionally one-call and live-only. Restart loses evidence and trajectory and cannot approve a restored contextual ask. Unresolved enforcing behavior is intentionally disruptive rather than silently permissive.

## Deferred

- approval provenance/replay redesign, repeat waivers, and restart/gRPC approval recovery;
- durable/replicated evidence, trajectory, policy generation, leases, drivers, event-log consumers, and background recovery;
- general reviewer Shell/network/repository exploration, DLP, or secret discovery;
- path-scoped rule language; and
- live-model efficacy evaluation.

## Primary-source inspiration

These are comparisons, not authority for Mecatl's trust model:

- [Codex auto-review](https://developers.openai.com/codex/sandboxing/auto-review)
- [OpenAI Agents SDK guardrails](https://openai.github.io/openai-agents-js/guides/guardrails/)

## See also

- [Contextual guardrails acceptance plan](../acceptance/contextual-guardrails.md)
- [ADR 0021](./0021-guardrails.md) — current checker architecture and quarantine
- [ADR 0049](./0049-guardrails-remove-maxchecks.md) — no silent per-session check cap
- [ADR 0050](./0050-guardrails-remove-maxcontentbytes.md) — no silent reviewed-input size skip
- [ADR 0051](./0051-guardrails-advisory-tui-visibility.md) — current generic advisory projection
- [ADR 0060](./0060-guardrails-bash-default.md) — current Shell default
- [ADR 0062](./0062-guardrails-approve-once.md) — current live hook approval and waiver behavior
- [ADR 0080](./0080-guardrail-routed-escape-checking.md) — separate permission-bound escape route
- [Hooks and guardrails architecture](../architecture/hooks-and-guardrails.md)
