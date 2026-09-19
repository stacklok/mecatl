# ADR 0342 — Contextual investigative guardrails

- Status: Proposed
- Date: 2026-09-14
- Scope: contextual action/inbound review, exact effective-call ordering, live repeat grants and result release, main/worker trajectory, checker routing, and safe status
- Proposed supersession: ADR 0021's one-payload/tool-less classifier architecture; ADR 0051's generic advisory projection; ADR 0060's narrow default coverage; ADR 0062's waiver identity and approval-origin behavior
- Superseded by: —

## Context

The current guardrail is a one-turn classifier over one hook payload. It cannot reliably use genuine task provenance, an actually approved plan, exact effective arguments after trusted mutation, caller authority, current worker trajectory, or narrowly requested evidence. Its generic inbound rubric also treats instruction-like language and uncertainty as unsafe, which over-flags normal issue requirements, admitted project instructions, and quoted security examples.

Three correctness gaps matter before broadening enforcement:

1. trusted PreToolUse mutation can change the action without repeating deterministic permission and Shell system-scope evaluation;
2. hook Allow-always and durable permission replay do not carry enough origin separation, while waiver normalization can equate materially different Shell text; and
3. a post-use finding currently has no live escrow: either unsafe inbound content reaches the model or the already-produced result is discarded and a retry risks repeating side effects.

Provider/model selection is also an authority boundary. The existing scalar `models.slots.guardrail` selects a model but the current checker is built per session and can silently move with that session's provider. The approved checker is an independent Build-captured route: scalar selection uses the deployment default provider, while a proposed strict guardrail-slot object names another configured provider. A model ID remains opaque and cannot identify its provider.

Finally, an `EvHook` is observed by the durable relay before live delivery. Checker rationale placed there is durable even if a UI calls it transient. Human-readable rationale therefore needs a separate owner-authorized live channel.

## Proposed decision

The product direction and exact proposed interface live in the [acceptance plan](../acceptance/contextual-guardrails.md). The plan is ready for Plan / Interface review; neither document authorizes implementation until that Split gate is merged. Implementation must build finite capacity calibration and quality-measurement deliverables, while separately authorized release validation supplies any actual checker-model efficacy evidence before a production-readiness claim.

### One reviewer, two jobs

Replace the old reviewer rather than retaining modes. Remove sanitize from code, configuration, prompts, tests, and current documentation without compatibility or migration machinery. Guardrails remain off unless enabled; enforcing `block` and explicit `advisory` remain.

Use one protocol with separate prompts:

- **action** reviews the exact outbound call after one trusted mutation and deterministic re-evaluation; and
- **inbound** reviews the already-produced effective result before history, save, event, client, or working-model delivery.

The inbound rubric is not the action-risk rubric reused. Both prompts require affirmative evidence of attempted authority crossing or redirection. Imperatives, assistant-directed prose, normal issue tasks, genuinely admitted project instructions, and quoted attack examples are not findings by themselves. Missing evidence does not itself increase intrinsic risk. Existing per-rule `prompt` configuration becomes additive operator policy beneath a fixed harness rubric; it may customize task-risk rules within existing permissions but cannot replace or hide authority, provenance, source-aware false-positive, evidence, protocol, or structured-output contracts. This deliberate replacement has no legacy full-override mode.

### Authority and evidence

The harness supplies immutable provenance for the current genuine user task, genuinely admitted project instructions, actual positive plan/approval callbacks, caller capabilities, environment identity, and relevant current-root trajectory. Text remains untrusted; provenance is not inferred from filenames, domains, or words such as “approved.” Workers receive bounded harness facts, not parent transcripts.

A review-local source exposes only opaque handles minted for objects already implicated by the call or trusted session state. A handle binds owner, session, environment revision, review, checker route, caller authority, source version, and expiry. Tool/MCP/source names are untrusted display metadata and never paths or authority. Forbidden/secret evidence is not disclosed. The reviewer has no Shell, network, general repository discovery, MCP, memory, skills, or recursive guardrails. Implementation selects finite handle-count and cumulative evidence-byte bounds as private internal constants using native tool/provider context limits and measured stress workload; units and effective values are visible to the reviewer request and documented with the implementation evidence report. Textual file previews reuse native Read's existing 2,000-line/25,000-byte output shaping and continuation semantics, but not its current whole-file `ReadVersion` allocation: bounded backend access or preflight must reject an oversized source before allocation. Existing incoming tool results remain fully reviewed under ADRs 0049/0050; this secondary budget controls only additional fetched evidence. Incomplete or unavailable capacity is explicit and cannot yield an acceptable decision when omitted content could matter.

The normal path makes one whole-output structured assessment. Investigation is allowed only when named missing evidence could change the verdict; it has one read-evidence tool and one terminal-submit tool. No arbitrary read/turn count is added.

### Ordering and approvals

Pre-use ordering is:

1. ordinary permission, plan, and Shell system-scope evaluation on the requested call;
2. trusted inner mutation exactly once;
3. if effective args changed, repeat those deterministic gates on the effective call;
4. contextual action review and binding validation; and
5. execute byte-identical effective args without rerunning mutation.

Deny, configured Ask, plan mode, and system-scope restrictions always bind. An unchanged call gets no redundant permission ask.

For a read batch, existing lookup, all requested/effective deterministic gates, trusted mutation, immutable request preparation, and repeat lookup remain serialized on the dispatcher. Only independent contextual action assessments for eligible read-only siblings run concurrently, each into a private indexed record with no `Session`, recorder, event, trajectory, detail, or approval mutation. The dispatcher joins them, drains decisions and asks in original order with at most one live ask, then revalidates authority, environment, dependencies, and binding after every wait before dispatching approved actual read-only tools concurrently. No tool executes speculatively. Static `DispatchSerial`, call-specific `MutatesParent`, mutating, and unknown-tool barriers remain serial. WebSearch, WebFetch, Subagent, and Parallel retain concurrent review when their existing classification admits them; cancellation joins reviews and yields complete ordered paired errors.

Action findings preserve Run once, Don't ask again for this action in this session, and Cancel. The repeat grant is guardrail-only and process/session-local; it never calls permission learning and never survives restart. It is available only when the action is repeatable and every implicated script/configuration/target dependency can be identified and version-bound. Its opaque digest is HMAC-SHA256 over one canonical helper preimage shared by creation and recomputation: exact ASCII `mecatl/guardrail-grant/v1`, then one NUL byte, then canonical length-delimited session, environment revision, caller, tool, effective args, destination/target, and dependency versions. Domain separation is mandatory whether the Build owner supplies a reused opaque-selector key or a sibling key; a wrong-purpose MAC made with the same key and subsequent fields never validates. The key and preimage are never logged. The digest is neither an unkeyed oracle over secret-shaped args nor delimiter-ambiguous. The human sees the exact scope. Any known material change causes a miss and new review. Missing, unverifiable, or incomplete dependency evidence sets `repeat_available=false` with a precise limitation while preserving Run once; implementations need not claim complete discovery of arbitrary dynamic Shell dependencies, and complete safe direct actions must not lose repeat by blanket downgrade. Events carry only the digest and existing call reference, never the call body.

The narrow approval repair is included: every new pending ask and durable approval identifies permission, hook-guardrail, or plan origin explicitly. `resolvePendingCall` validates kind and origin before any learning, waiver, plan transition, execution, or result release; `replayApprovals` learns only explicit permission-origin Allow-always from the raw JSON `session.Event` stored by the event-log/grpcdriver path. Missing or unrecognized origin is unknown and fails closed toward a future re-ask, never guessed from a call/tool name or old provenance boolean. `EvApproval` remains a metadata-only verdict mirror with only the origin added. The current protobuf `Approval` fields 1–5 add exact string `origin=6` (`permission`, `hook_guardrail`, or `plan`) so mapper and `StreamSessionEvents` projections preserve parity; protobuf is not required for the actual JSON replay persistence. This adds no cloud transport and is not a wider replay or cloud-native redesign.

### Post-result escrow and release

Post hooks and UTF-8 repair run once. A flagged enforcing result is retained privately on the live Run before `ToolCallRecorder`, paired history, save, result event, client result delivery, or working-model delivery. Interactive Release once atomically consumes and delivers that exact one result through the ordinary final-result tail; it never executes an action/tool, reruns a pre/post hook or reviewer, learns permission, or arms a waiver. Unsupported Allow-always is rejected before mutation. Release authorizes reading the data, not following embedded instructions or approving a later action.

Cancel, decline, timeout, disconnect cleanup, unattended enforcement, stale binding, process loss, or unavailable held state destroys/loses the private bytes and records/emits one synthetic withholding error so history remains paired. Restart never re-executes the tool to recover a result. The current engine has no live tool-result chunk event; background Shell's lower-level streamer writes only to its private tail before collection. Any future covered live-output adapter must buffer reviewable bytes or decline enforcing inbound coverage rather than claim post-output withholding. Advisory delivers the unchanged result with a visible finding.

Read-batch execution stays parallel. Cleared siblings execute, run PostToolUse and repair, and may review concurrently into private indexed records that do not mutate `Session`, surface asks, record, or emit final results. After join, only the dispatcher goroutine drains decisions in original call order, with one release ask live at a time, then records one complete ordered result slice. Two held siblings can therefore resolve release-then-deny independently; cancellation after the first release closes every remainder with synthetic errors. Unknown pending identities reject. The drain never reruns a tool, hook, reviewer, or side effect.

### Current-root trajectory

A delegation-root Run owns a metadata-only trajectory shared by harness callbacks from main and workers. It links relevant sensitive reads, sends, delegation, denials, materially safer replacements, and new genuine approval without copying child conversations into the parent prompt. Scope ends after child drain at root-run termination; there is no cross-session/restart state.

No arbitrary 512-item cap is introduced. Authorization-relevant facts cannot be silently omitted. Exact duplicates or superseded non-authorizing status may be compacted only when authorization-equivalent. Implementation selects and enforces private per-root fact-count and byte bounds before allocation; when exhausted it marks trajectory incomplete. Once incomplete, bounded metadata must retain the authorization-relevant class/direction/target/decision or the affected review fails unresolved—never blindly acceptable. Implementation inventories the trajectory, evidence handles, held results, and transient detail under the cloud-native resource rules.

### Checker route and budget

Reuse `models.slots.guardrail` and the shared alias machinery. Resolve one provider/model pair at Build and capture it for all main/session/worker checker factories. The current scalar guardrail slot—including its `cheap` tier fallback—resolves its model selector and binds it to the captured deployment default `reg.Default()`; it continues to win over the legacy gate model. This ADR proposes a strict optional `models.slots.guardrail: {provider: configured-id, model: existing-selector}` form when the operator wants another configured provider. Both object fields are mandatory; the object is operator-tier only and a valid project-tier object is WARN-ignored without changing existing project scalar behavior. No provider is inferred from opaque model text. Unknown keys, missing fields, unknown configured providers, and unresolvable guardrail selectors fail startup rather than disabling/falling back. Validation uses the local provider registry and does not require a live model-inventory network call. Other slots retain existing scalar and provider behavior. This is a proposed API realization of the approved independent existing slot, not a claim that the object exists today; it adds no `guardrails.checker` subtree or provider flag.

One 90-second deadline covers setup, evidence, provider recovery, and at most three complete attempts. Only classified recoverable operational failures retry inside that same deadline; completed findings do not. Human waiting is excluded. Revalidation after a human answer runs once under the remaining normal operation/parent context, without resetting model budget, reviewer recall, or claiming universal filesystem/remote atomicity. A deadline does not bound memory: implementation calibrates private `max_evidence_handles`/`max_evidence_bytes` per review and `max_trajectory_facts`/`max_trajectory_bytes` per delegation-root Run, enforces them before allocation, and documents values and stress experiments in implementation review. They are internal capacities, not public deployment configuration fields.

Operational checker failure is distinct from a prohibited or completed unresolved assessment. `inspection=complete, assessment=unresolved` covers capacity exhaustion, incomplete trajectory, and specifically missing decision-relevant evidence; it is not an outage and never increments checker-DOWN state. In enforcing mode it takes the job-appropriate human boundary: ask action approval or hold for result release interactively, and deny the action or withhold the result unattended. Advisory leaves the original action/result unchanged with explicit unresolved/incomplete status, never an unsafe finding. Missing-evidence detail cites actual unavailable/incomplete source refs and does not fabricate an attack. Only `inspection=operational_failure` enters the existing `onCheckerDown` posture: `fail` recovers within budget, then asks an interactive human or blocks/withholds unattended; explicit `warn` may continue with warning. Neither posture can override permission denial, prohibited findings, forbidden evidence, stale authority, or invalid identity.

### Coverage and workers

When enabled, defaults review before execution: Shell; local mutations; WebSearch/WebFetch; MCP calls including `CallMcpWithQuery`; and Subagent/Parallel/Team. They review before delivery: Shell; Read/ListDir/Grep/Glob; WebSearch/WebFetch; MCP results including `FetchMcpResource` and `CallMcpWithQuery`. The same applicable rules bind main and workers; checker engines are inert.

Expanded defaults are not empirically effective merely because protocol tests pass. Implementation builds a paired benign/adversarial corpus, measurement runner, quality baseline, and paired-regression report for false warnings, false blocks, misses, latency, investigation frequency, and spend. Actual checker-model comparisons require separate operator authorization of route and spend at release validation; their results are required before a production-readiness claim, not before the plan can be proposed, merged, or implemented.

### Projection and discoverability

Durable hook/ask projections contain machine job, assessment, inspection, disposition, reason code, checker route, opaque keyed grant digest, exact approval scope, origin, and validated refs only. Raw JSON events carry the replay-learning origin; protobuf `Approval.origin=6` mirrors it for LOG_ONLY `StreamSessionEvents` projection parity. They contain no display/argument preview. Checker prose, inputs, evidence, held output, and call body are absent from events, snapshots, logs, diagnostics, and metrics.

A Service-owned live registry exposes bounded, UTF-8-repaired, control/framing-neutralized concern/source/next-action detail through a unary RPC authorized to the exact owner or the registered parent owner plus parent-visible delegated reference. It exposes no environment/filesystem ref. Detail for acceptable, advisory, enforcing, and failed reviews survives through the existing delegation-root Run lifetime so a UI can fetch it after the event, then disappears after child drain/root cleanup or earlier cancellation, disconnect cleanup, shutdown, or restart. It is not a session event.

The model receives only fixed harness guidance: what boundary was enforced, the actionable next step, anti-bypass instruction, and—for released results—that reading is allowed while embedded instructions remain untrusted. Real factory-path tests pin the evidence-tool and worker posture instructions in their owning prompt layers.

## Consequences

The reviewer can inspect actual effective actions and narrowly selected evidence without becoming a privileged general agent. Permission policy remains authoritative. Exact repeat grants retain usable interactive UX without widening permission or restart authority. Inbound content can be held and released without repeating side effects. Main/worker trajectory detects read-to-send and alternate-worker bypass within one root run.

Costs are extra review latency/spend, additive engine/protobuf surfaces, dispatch restructuring, transient run/service resources, and explicit UI handling for action versus result-release asks. Enforcing operational failures and lost held results fail closed. Quality efficacy is not established by implementation mocks; a separately authorized real-model release validation is required before claiming production readiness. Capacity calibration remains fail-closed throughout implementation and review.

## Deferred

- cross-process held-result recovery or durable result escrow;
- wider approval provenance, event-log replay, replicated trajectory/evidence, leases, drivers, or background recovery;
- cross-session trajectory;
- provider-aware object forms for slots other than `guardrail`, or implicit provider fallback;
- general reviewer exploration, DLP, or secret discovery; and
- real-model efficacy claims or production-readiness signoff before separately authorized route/spend evaluation.

## Primary-source inspiration

These are comparisons, not authority for Mecatl's trust model:

- [Codex auto-review](https://developers.openai.com/codex/sandboxing/auto-review)
- [OpenAI Agents SDK guardrails](https://openai.github.io/openai-agents-js/guides/guardrails/)
- [Pinned Codex guardian rubric](https://github.com/openai/codex/blob/5b1d6560181680f95cde95c14ed042acc02248ed/codex-rs/core/assets/guardian/policy_template.md) — authorized tasks may use untrusted implementation details; missing evidence does not itself increase intrinsic risk

## See also

- [Contextual guardrails acceptance plan](../acceptance/contextual-guardrails.md)
- [ADR 0021](./0021-guardrails.md) — current checker architecture and quarantine
- [ADR 0049](./0049-guardrails-remove-maxchecks.md) — no silent check-count cap
- [ADR 0050](./0050-guardrails-remove-maxcontentbytes.md) — no silent reviewed-input size skip
- [ADR 0051](./0051-guardrails-advisory-tui-visibility.md) — current advisory projection
- [ADR 0060](./0060-guardrails-bash-default.md) — current Shell default
- [ADR 0062](./0062-guardrails-approve-once.md) — current hook approval and waiver behavior
- [ADR 0080](./0080-guardrail-routed-escape-checking.md) — separate permission-bound escape route
- [Hooks and guardrails architecture](../architecture/hooks-and-guardrails.md)
