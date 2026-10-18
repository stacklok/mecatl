# Session-history contract reference

**Status:** Design draft, not finalized interfaces or shipped behavior.
**Scope:** Exhaustive lifecycle phases, commit gates, cancellation transitions, reload/failure matrices, extension evidence, and acceptance cases.

Start with the [overview](overview.md), then choose one [review packet](README.md).
This is lookup material, not the recommended end-to-end reading path.
The [architecture reference](session-commit-history.md) owns specialized contracts; the unchanged [history/execution model](session-commit-history.modelith.md) and [catalog/command model](session-command-protocol.modelith.md) express domain semantics.
Packets summarize those requirements and own the relocated explanations/worked sequences, not a second competing specification.
Candidate names do not prescribe interfaces or one record per phase.

| Review packet | Detailed reference here |
| --- | --- |
| [Execution and recovery](execution-and-recovery.md) | [Phases and gates](#tool-call-lifecycle-and-commit-gates), [failure evidence](#failure-evidence-and-dependent-work-gates) |
| [Cancellation and results](cancellation-and-results.md) | [Cancellation transitions](#coordinated-state-machine-with-cancellation) |
| [Authorization and hooks](authorization-and-hooks.md) | [Ask, hook-anchor, and intent gates](#required-commit-gates-by-phase) |
| [Model continuation](model-continuation.md) | [Input gates](#required-commit-gates-by-phase), [cache conformance](#acceptance-scenarios) |
| [Commands and lifecycle](commands-and-lifecycle.md) | [Owning scopes](#owning-scopes), [control/retry acceptance](#acceptance-scenarios) |
| [Storage and observation](storage-and-observation.md) | [Reload matrix](#observable-reload-matrix), [extension evidence](#built-in-extension-reconstruction-boundary) |

## Execution and recovery model

The execution/recovery explanation and ordinary model-tool-model path now live in [execution and recovery](execution-and-recovery.md).
This section retains incoming anchors; the tables below remain the exhaustive contract reference.

### One model turn and its tool call

See the [ordinary path](execution-and-recovery.md#ordinary-path-one-model-turn-and-its-tool-call).

### Recovery preserves knowledge, not the outside world

See [failure evidence explained](execution-and-recovery.md#failure-knowledge-is-not-the-outside-world) and the [exhaustive matrix](#failure-evidence-and-dependent-work-gates).

#### Persistence failure without generic rollback

See [settled execution rules](execution-and-recovery.md#settled-rules-and-why).

### Cancellation coordinates separate lifecycles

See [cancellation and results](cancellation-and-results.md), including the still-open predecessor-closure/admission bundling question.

## Contract vocabulary and ownership

Use the [shared vocabulary](vocabulary.md) for commit, phase, gate, admission, and provisional observation.
Specialized ownership scopes and responsibility boundaries remain below.

### Owning scopes

Durable command decisions have two kinds of owning scope:

| Scope | What it owns |
| --- | --- |
| **Session catalog** | Session membership and lifecycle decisions, including creation, publication, deletion, and fork admission. Catalog authority remains available when the affected session does not yet exist or is no longer accessible. |
| **Session** | Conversation and execution history, runs, tool calls/results, asks, steers, hooks, and session-owned extension operations. |

Each catalog and each session has its own bounded request-receipt scope; concurrent clients share that scope rather than owning separate retry caches.
A run or extension operation is a resource within its session scope, not a third peer kind of owning scope.
These are domain ownership scopes, distinct from the replica currently authorized to drive execution or append history.

### Responsibility boundaries

| Boundary | Responsibility | Must not imply |
| --- | --- | --- |
| API command admission | Authorize the caller, validate optional retry identity and targeting, and return the committed decision or an explicit uncertain/rejected outcome. | Request identity is permission, work identity, or proof of completion. |
| Engine/domain execution | Preserve original/effective values, authorization and call/result pairing; prepare semantic transitions and enforce acceptance gates. | Publishing live output makes it durable, or a tool error proves no external effects. |
| Session coordinator | Order transitions and controls, coordinate bounded batching and recovery, and distinguish prompt cancellation signaling from committed acceptance. | A process-local mutex fences another replica. |
| Storage acceptance | Atomically enforce append preconditions, accept immutable facts, assign positions, and support bounded reads and ambiguity resolution. | Storage executes or authorizes tools, interprets provider replay, or independently decides batching policy. |
| Projection and observation | Project authorized committed facts and explicitly provisional observations; expose common work-tracking conventions over existing run, extension-operation, and catalog-creation lifecycles. | Display completeness gives access to private continuation or proves external resources remain available; common work observation adds a universal operations collection. |
| Execution environment | Own workspace/artifact availability, preservation, and cleanup under its lifecycle contract. | Retaining a history reference extends the environment's lifetime. |

## Tool-call lifecycle and commit gates

The tool-call lifecycle covers the model's requested call, execution or nonexecution, and canonical result settlement.
On the ordinary continuation path, that result then enters a later model request; cancellation can end execution without any later model consumption.
A **phase** names work or waiting in that lifecycle; entering a phase in memory does not itself commit anything.
Labels such as **request accepted**, **intent accepted**, **result accepted**, and **input accepted** mean the corresponding facts have been committed to authoritative history.
They name what a commit established, not a separate kind of lifecycle object or an additional write.
One commit can establish several ready facts when no commit gate separates them; a phase can also require intermediate commits, such as an execution ask and its resolution.
Live boundaries such as execution admission and tool return do not require separate commits unless a specific gate says otherwise.
The terms below are **candidate terminology** for shared use in contracts, code, diagnostics, and tests; they do not prescribe Go types, enum encodings, or one record per phase.

### Named phases

```text
Request capture
  -> Action preparation [can await execution approval]
  -> Tool execution
  -> Result processing
  -> Result release [can await release approval]
  -> Result acceptance
  -> Model-input assembly
  -> Model continuation
```

This is the ordinary successful path, not a requirement that denied, canceled, or interrupted calls execute every phase.
Result release is the disclosure decision after required processing; it requires a human answer only when existing policy requires an ask.

| Phase | Work and exit condition |
| --- | --- |
| **Request capture** | Accept the completed model response, requested calls, and required replay/bindings together. Committing the request establishes call identity and original call position; streamed fragments alone do not. |
| **Action preparation** | Validate the requested arguments, authorize, run applicable pre-tool hooks, authorize rewrites, and resolve required execution asks. Committing the intent retains the exact effective authorized action and permitted preparation evidence. |
| **Tool execution** | Check dispatch eligibility against that accepted intent, then admit and invoke the tool under existing dispatch barriers. The live **execution admitted** boundary is distinct from intent acceptance. A return establishes available execution evidence and output, not disclosure permission or durable result acceptance. |
| **Result processing** | Hold raw output privately in memory and apply normal post-tool hooks, UTF-8 repair, and inbound review under existing failure semantics. Preserve established execution evidence independently of processing success. |
| **Result release** | Determine whether processed content can be disclosed, must be withheld, or awaits an exact release decision. Unreleased payloads remain memory-only. Denial or lost output yields an explicit synthetic result without claiming the tool did not execute. |
| **Result acceptance** | Commit the permitted canonical result or explicit recovery outcome under the original call identity, with safe evidence and any corresponding ask resolution. Committing the canonical result completes that call's canonical pairing. |
| **Model-input assembly** | Assemble all required accepted results in canonical/provider order, with effective instructions, eligible input, transformations, and replay. Commit the complete logical input before dependent model execution. |
| **Model continuation** | Recheck eligibility to advance the run and submit the accepted logical input to the model. Submission is distinct from input acceptance and does not by itself prove provider consumption or a completed response. |

Execution approval suspends action preparation at its recorded continuation point; result-release approval suspends result release after the tool has executed.
They are different asks with different recovery consequences.
Model-input assembly and continuation belong to the model turn and can cover several calls; they are not repeated model requests for each call.
A result accepted after cancellation can enter a later explicitly started run, but cannot cause continuation of the canceled run.

### Required commit gates by phase

Phases describe work or waiting; the rows below identify the required commit gates within or at the end of that work.
A phase can contain several gates, and not every phase transition requires a commit.
Hook-anchor and approval gates apply only when the corresponding hook-input capture or ask is needed.
Each gate waits for authoritative storage acceptance before allowing the specified dependent step; it does not require a separate physical write, synchronous I/O API, or identical backend flush mechanism.
An append submitted or buffered optimistically remains unresolved until the adapter establishes acceptance under its declared durability contract.
A **provisional observation** can describe permitted live progress before that confirmation; it grants no execution authority and can disappear on reload.
Only disclosure-permitted content can appear in a provisional preview.

| Phase | Commit gate | What must be accepted | What waits for confirmation | Recovery if confirmation or process is lost |
| --- | --- | --- | --- | --- |
| Request capture | Request accepted | Completed response, requested calls, required replay and occurrence bindings. | Dependent call preparation/execution. | Resolve ambiguous append identity first. Unaccepted response fragments grant no call authority. |
| Action preparation | Hook-input anchor accepted, if needed | Otherwise-unavailable reconstructible hook definition/input anchor. | Recoverably retryable hook invocation. | Reuse committed hook completion; retry an incomplete hook only with exact reconstructible inputs and authorized recovery. |
| Action preparation | Execution ask accepted, if needed | Exact ask and permitted continuation. | Publication of an actionable execution ask. | Preserve a reconstructible execution ask rather than classifying it as ordinary not-started work. |
| Action preparation | Execution verdict accepted, if needed | Exact authorized ask resolution and its recorded continuation point. | Resumption from that point, including hooks or dispatch preparation dependent on approval. | Reuse the accepted verdict without repeating committed hook work; cancellation and remaining eligibility checks still apply. Bundle with intent only when both are ready. |
| Action preparation | Intent accepted | Exact effective authorized intent and completed required preparation evidence. | Tool dispatch, followed by live dispatch-eligibility checks. | With no accepted intent, the tool was not dispatched by the protocol. With intent but no established outcome, execution is uncertain; never automatically rerun the tool. Pre-intent hook effects remain possible. |
| Result release | Release ask accepted, if needed | Exact release ask and safe correlation/processing evidence, excluding unreleased payloads. | Publication of an actionable release ask. | A lost memory-only hold closes unavailable under the original call identity; retained ask metadata cannot restore the bytes. |
| Result acceptance | Result accepted | Permitted canonical result or explicit recovery outcome, safe evidence, and applicable ask resolution. | Confirmed result publication and use in dependent model input. | Resolve an ambiguous append before retrying acceptance. An accepted result survives notification loss and does not require redispatch. |
| Model-input assembly | Input accepted | Complete effective logical input and required compatible replay. | Dependent model request. | Accepted input alone does not establish whether the provider received it. Preserve explicit interruption/recovery semantics rather than inferring safe automatic continuation. |

Tool return, entry into processing, and dispatch admission do not each require an additional hard write.
Safe evidence can share the next eligible commit; a crash before that commit can leave less knowledge than the live owner had.
In particular, a tool return lost before result acceptance can leave an unknown execution outcome or unavailable output, depending on the evidence actually retained.
Storage ambiguity pauses dependent work while already-started work can still return into bounded process-local buffers.
Readiness-driven batching can group independent ready results without waiting indefinitely for an earlier call.

### Coordinated state machine with cancellation

The **candidate state-machine structure** separates the run's execution lifecycle from each call's execution evidence, result disposition, and committed facts.
Cancellation is an event affecting those dimensions, not a single terminal tool-call state that erases them.
For cancellation, the run moves from **open** to **stopping** to **terminal**; normal completion, failure, and owner-loss recovery are additional run transitions, not synonyms for cancellation.
A live stopping transition can precede its durable cancellation acceptance because prompt local interruption must not wait for storage.

| Event and guard | Run transition | Call/result transition and required acceptance |
| --- | --- | --- |
| Authorized cancellation targets an open run. | Close local admission and enter stopping; promptly signal active work, descendants, and extensions. Commit the resolved cancellation target and all pending-source cancellations before confirmed acknowledgement. | Calls not yet admitted cannot cross execution admission. Preserve accepted results and established execution evidence; signal admitted work without asserting rollback. |
| Cancellation precedes execution admission, including during an execution ask or preparation. | Remain stopping. | Fence the continuation and settle not-started under the original call identity once non-admission is established. Close the outstanding execution ask; preserve any hook-effect uncertainty. |
| Execution admission precedes cancellation. | Remain stopping while bounded shutdown/settlement proceeds. | Tool effects can exist. A return follows normal bounded result processing; missing established outcome is recorded as unknown rather than failed or safe to retry. |
| Produced output reaches a release ask while stopping. | No return to open. | Accept the exact ask and safe evidence; keep output memory-only. No further model inference or dependent dispatch is permitted. |
| Bounded shutdown and active result processing finish, or their settlement deadline is reached. | Accept terminal run disposition with honest unresolved obligations. | An optional release ask can remain pending. Do not fabricate a canonical withheld result solely to mark the run terminal. A deadline does not establish external cessation or permit bypassing disclosure checks. |
| Exact release or denial wins while its hold is live, including after run termination. | Run remains stopping or terminal; never reopens. | Accept canonical released/withheld result with the ask resolution. Complete pairing once; do not replace an already canonical result. |
| Hold is lost through process loss. | Recovery uses accepted run evidence; loss is not a release decision. | Close the ask and accept an unavailable/withheld result under the original call identity. Do not reconstruct prohibited bytes or rerun the tool. |
| A successor run is admitted after bounded predecessor settlement. | Predecessor remains terminal; successor has a distinct identity. | At admission, invalidate outstanding holds, expire their asks, discard bytes, and fence late answers/processing, ordered against release decisions. Accept corresponding withheld results before successor model input. Atomic bundling of those result records with successor admission remains an open representation choice. |
| A late release answer or processing completion arrives after invalidation or canonical result acceptance. | No reopening or retargeting. | Reject stale/conflicting advancement; retain the accepted disposition. A protected retry can return its original decision without applying it again. |

Dispatch admission and cancellation must be ordered so exactly one wins the admission race; accepting intent alone does not win it.
The same ordering requirement applies to model-request admission: if cancellation wins, no new request is admitted; if request admission wins, cancellation signals that admitted request rather than retroactively preventing it.
Accepted model input alone does not win model-request admission.
A terminal run with a pending release ask is a valid state, as is a terminal run whose call result is accepted later.
A successor model request with an unresolved required predecessor call/result pairing is invalid.
Accepted history cannot establish two canonical results for the same call or two winning dispositions for the same ask.
These guards constrain the concrete transition API; they do not mandate a generic tentative aggregate or rollback layer.

## Observable reload matrix

These requirements concern a new client process reading authorized committed state, not reconstructing a view from its own cached events.
They apply through compaction and checkpoint-plus-suffix replay.
Completed committed facts are distinct from streamed observations that never committed.
The exact field budgets and several display representations remain open below.

| Item | Required observable behavior | Honest absence or limitation | Acceptance case |
| --- | --- | --- | --- |
| Conversation | Restore complete authorized retained conversation through pinned C, including material outside the current compacted model context; follow changes after C without a gap. | Uncommitted streamed response fragments can disappear after failure. | A01 |
| Human-visible reasoning | Restore completed committed display summaries associated with the producing response. | Never substitute provider-private reasoning/replay; representation of absent/interrupted summaries remains open. | A02 |
| Tool calls and outcomes | Restore admitted calls, effective dispatch facts, and permitted canonical or explicit recovery outcomes under stable call identities. | Intent alone means the tool might have started; unknown is not failure. | A03 |
| Tool detail | Make admitted arguments and permitted result content available through bounded reads, with explicit previews/truncation. | A preview is not the complete canonical result; unreleased bytes are unavailable to history reads. | A04 |
| File-change history | Preserve tool-reported change evidence and distinguish unconfirmed attempts. | An Edit/Write target alone proves intent, not success; this is neither exhaustive Shell/external change detection nor the current workspace/Git diff. | A05 |
| Usage and confirmed metadata | Restore committed reported usage and confirmed metadata without double-counting retries; preserve attribution. | Unknown usage is not zero, and inherited facts are not new spend. Exact inherited/new presentation remains open. | A06 |
| Subagent | Restore parent-owned inventory, status, model, reported usage, approved bounded activity, and canonical parent request/result without fetching the child. | Child transcript access is separately authorized; missing detail does not erase the parent summary. | A07 |
| Parallel | Restore branch inventory, outcomes, actual winner decision, approved activity, and canonical joined result. | Do not infer a winner from branch index or result prose; artifact availability follows the execution environment. | A08 |
| Team | Restore session-owned roster, member links/outcomes, approved activity, task/finding views, and canonical deliverable. | Parent display does not gain private mailbox bodies or full member transcripts. | A09 |
| Controls | Restore pending source slots/revisions and exact asks with committed dispositions across clients. | A persisted release ask cannot restore lost memory-only output. Unsent drafts remain local. | A10 |
| Notices/deliveries | Give retained meaningful notice/delivery categories stable identity and an explicit restore/refetch/unavailable classification. | Exact category inventory remains open; focus, scroll, spinners, and token animation are not durable facts. | A11 |
| Extension projection gaps | Retain approved parent items already committed and explicitly surface unresolved child-to-parent gaps. | Do not backfill private child content or present a partial view as complete. | A12 |

The source pass distinguishes current implementation from these requirements.
For example, message-based reload currently rebuilds conversation and top-level calls/results, while much delegation display is event-derived; the changed-file appendix records Edit/Write targets before successful completion.
See `internal/adapter/server/transcript.go:61` and `cmd/mecatui/ui/sessions_surface.go:1195` for message-based reload, `cmd/mecatui/ui/update.go:1929` and `cmd/mecatui/ui/update.go:1947` for delegation event projections, and `cmd/mecatui/ui/update.go:1331` for target-path tracking.
The product requirements are tracked in [#2114](https://github.com/stacklok/mecatl/issues/2114) and [#2079](https://github.com/stacklok/mecatl/issues/2079).

## Worked execution sequences

The sequences now belong to the bounded packets; these headings preserve incoming links.

### Ordinary tool call and trusted rewrite

See [authorization sequence](authorization-and-hooks.md#ordinary-tool-call-and-trusted-rewrite).

### Exact effective arguments across policy, execution, and history

See [exact argument semantics](authorization-and-hooks.md#settled-rules-and-why).

### Cache-stable history reconstruction

See [cache-stable reconstruction check](model-continuation.md#cache-stable-reconstruction-check).

### Dispatch eligibility and authorization freshness

See [authority/admission race](authorization-and-hooks.md#race-authority-changes-after-intent-acceptance).

### Execution approval at its actual phase

See [execution approval sequence](authorization-and-hooks.md#execution-approval-at-its-actual-phase).

### Result-release approval and process loss

See [release and process loss](cancellation-and-results.md#release-and-process-loss).

### Cancellation with produced output

See [ordinary cancellation settlement](cancellation-and-results.md#ordinary-settlement-with-produced-output) and [successor race](cancellation-and-results.md#race-release-versus-a-successor-run).

### Concurrent read results

See [concurrent-result race](execution-and-recovery.md#race-concurrent-read-results).

### One steer operation with an optional run constraint

See the [shared-input sequence](commands-and-lifecycle.md#ordinary-shared-input-sequence) for explanation.
This table retains the full targeting/disposition comparison; [source slots](session-commit-history.md#source-scoped-steers) and [durable recovery](session-commit-history.md#durable-steer-recovery) specify its detailed rules.

| Situation | Unconstrained session input | Input constrained to run R |
| --- | --- | --- |
| Admission | Request eligible execution under common admission rules. | Reject stale if R is not an eligible target; never retarget another run. |
| Eligible model-input boundary | Consume the exact pending revision with all other eligible slots. | Consume only for R; use the same revision/provenance machinery. |
| Normal target completion before consumption | Continue eligible execution or admit one follow-up. | Commit `not delivered: target ended`, retain accepted text as history, and clear runnable pending input. |
| Explicit user cancellation | Cancel pending revisions from every source and signal active extensions/descendants. | Same cancellation rule; no promotion. |
| Failure, shutdown, or owner loss | Preserve accepted unconsumed input for explicit recovery. | Preserve evidence for an explicit recovery decision without silently relaxing its run constraint. |

Explicitly carrying input into a new run is a new authorized decision, not pretending the old run consumed it.
Exact wire fields and recovery commands remain open; this adds no independent cross-run execution.

## Failure evidence and dependent-work gates

Execution, processing, disclosure, and durable acceptance are independent evidence dimensions.
The labels below describe facts, not final enum names.

| Last established boundary | Safe conclusion | Required response |
| --- | --- | --- |
| Model response acceptance unresolved | No accepted dispatch authority for its calls. | Resolve append identity or stop; no tool dispatch. |
| Requested call, no intent, no actionable execution ask | Tool not dispatched by the protocol; pre-intent hooks may have effects. | After fencing, settle not-started under the original identity. |
| Durable execution ask | Exact execution decision is pending. | Preserve/rehydrate reconstructible continuation; do not close as ordinary not-started. |
| Intent accepted, no established outcome | Tool might have started or still be running. | After fencing and evidence checks, settle unknown-outcome; never automatically rerun. |
| Output returned, processing incomplete | Execution and disclosure can have different certainty. | Continue bounded normal processing while live; preserve safe execution/processing evidence without storing prohibited payloads. |
| Post-tool hook failure | Tool may already have established effects. | Preserve current hook failure semantics; do not turn processing failure into tool nonexecution. |
| Enforcing inbound check incomplete/failed | Applicable existing failure policy determines release. | Do not bypass required disclosure checks because of cancellation or timeout. |
| Release ask, live hold lost | Produced output cannot be released or reconstructed. | Close ask and settle withheld/unavailable, without tool retry. |
| Result append ambiguous | Tool effects are not undone; result acceptance is uncertain. | Resolve transition identity before storage retry or dependent work. |
| Result accepted, notification lost | Committed outcome survives. | Catch up from authoritative history; no redispatch or duplicate usage. |
| Cancellation deadline reached | Local settlement bound expired; external cessation may remain unknown. | Record unresolved obligations honestly and do not report proven termination. |

The ordinary baseline remains one completed-response gate plus intent/result gates per call, or `1 + 2N` before eligible grouping.
Approval and otherwise-unavailable hook-input anchors can add gates; receipts share their protected mutation's commit.
This is not a physical-write estimate.

## Built-in extension reconstruction boundary

Every Team has a session owner, including a Team started through the existing direct API rather than a tool call.
The direct entry point must identify that owner without pretending an absent parent tool call exists.
Teams independent of sessions are a different feature.

Only trusted registered reducers interpret continuation-critical family state.
The common envelope carries identities, correlation, type/version, criticality, bounded child links, and active obligations; it does not make every family lifecycle a core record type.
No checkpoint persists engines, mutexes, channels, cancellation functions, environments, or cleanup closures.

| Family | Continuation/recovery evidence | Approved display and canonical result | Lifecycle distinction |
| --- | --- | --- | --- |
| Subagent | Effective admitted request and execution choices, child/materialization identity, drive/validation decisions, terminal evidence, and background collection/disposition. | Parent-owned full admitted request/result plus existing approved inventory, status, model, reported usage, and bounded activity. | A started-result is not completion; child text alone may not reproduce validated/salvaged parent output. |
| Parallel | Branch inventory and identities, actual completion/selection decisions, join/judge evidence, and merge/resource dispositions where relevant. | Branch outcomes and winner facts plus exact canonical joined result; no winner reconstruction from prose. | Child success does not prove auto-merge success; execution environment owns retained winner artifacts and cleanup. |
| Team | Session owner, coordination mutations, scheduling/round decisions, task/message/finding state, synthesis choices, and terminal/member dispositions required to explain execution. | Existing approved roster/activity/tasks/findings and canonical deliverable; private mailboxes are not new display fields. | Summary payloads are not continuation checkpoints; direct API and tool entry share the session-owned contract without requiring identical call correlation. |

This inventory identifies required classes of evidence, not an instruction to serialize every runtime field or duplicate canonical parent payloads.
Concrete record granularity and critical/display-only classification still need review against the behavior each fact enables.
First-cutover crash recovery settles interrupted work rather than automatically replaying children, judging, merging, scheduling rounds, or synthesizing another answer.
Missing critical interpreters fail closed for dependent execution while safe core inspection/deletion remain available.
An unavailable environment or artifact is an explicit availability result, not proof that retained history is missing or permission to recreate external work.

## Acceptance scenarios

The contract will be checked through real engine/host/client composition with offline fixtures, not source-text assertions or a single fake reducer agreeing with itself.
No implementation tests are added by this document.

| Cases | Required experiment and negative control |
| --- | --- |
| A01, A02 | Read with a fresh client at pinned C after compaction; follow commits after C. Disable the optional activity log. Verify complete committed conversation and display summaries, exclude private replay, and lose only deliberately uncommitted fragments. |
| A03, A04 | Fail response, intent, and result acceptance separately; lose accepted acknowledgements. Assert no premature dispatch/continuation, no repeated tool effects, stable identities, and explicit unknown/withheld dispositions. Plant sensitive output to detect leakage through previews and traces. |
| A05 | Contrast confirmed tool-reported changes, requested-but-rejected edits, uncertain effects, and a later unrelated workspace change. Historical display must neither invent success nor become a current diff. |
| A06 | Retry accepted transitions and reload inherited history. Distinguish reported/unknown usage and origin; no retry double counting or attribution of inherited spend as new work. |
| A07, A08, A09 | Replay full history and checkpoint-plus-suffix through the same C, then reload without child reads. Compare approved inventory/activity and canonical outcomes. Deny deeper access; remove a critical handler; expire an environment/artifact independently. |
| A09 | Start a Team through both entry points. Require explicit session ownership and equivalent durable lifecycle/recovery guarantees without inventing a parent tool call for the direct API. |
| A10 | Race clients on prompt admission, slot revision, exact approval, cancellation, and release versus new-run admission. Test both steer constraints through the same semantics and preserve committed dispositions. |
| A10 | Cancel while a tool has returned and its result hook is blocked. Verify prompt interruption of further execution, bounded normal processing, no resumed inference, no forced release UI, and invalidation of unreleased holds on a new run or process loss. |
| A11, A12 | Lose notifications, overflow bounded catch-up, and create a child-to-parent projection gap. Require authoritative catch-up or an explicit reload/gap outcome, never silent loss or unauthorized backfill. |
| Cache-stable reload | Restore real persisted history and construct requests through newly built real provider adapters using offline HTTP fixtures. Compare full replay and checkpoint-plus-suffix with stored and absent replay bindings, repeated provider keys across turns, nested nonlexicographic argument keys, string whitespace, and escaping. Preserve historical canonical IDs and deterministic fallback pairing. Deliberately reorder an argument object or change string content as negative controls; decoded-map equality must not hide the difference. Allow formatting/escaping equivalences only under the comparison's stated provider contract. |
| Cache-prefix configuration | With history fixed, rebuild the effective system prompt, tool definitions/order, model settings, cache-key configuration, and breakpoint placement before/after reload. Assert equivalent reconstruction under unchanged effective configuration; deliberately change each dimension to prove the fixture detects drift. Do not interpret unchanged requests as a guarantee about TTL, eviction, routing, or live provider hits. |
| Lifecycle gates | Interrupt before and after each hard gate in the named lifecycle, including accepted appends with lost acknowledgements. Compare live observations with fresh replay; provisional phase progress must not invent durable execution evidence or permit dependent work. Lose a tool return before its evidence commits as a negative control for claims of proven completion. |
| Cancellation transitions | Race cancellation separately against execution approval, execution admission after intent acceptance, result acceptance, and next-model-request admission. Exercise terminal-run/pending-release, release versus successor admission, and a settlement deadline with unresolved external effects. Assert one winning ask/result disposition and complete required pairing before successor model input. Approval never overrides cancellation; cancellation winning admission prevents new execution/model requests, while admission winning first permits interruption of already-admitted work. |
| API receipt bounds | Exercise 255, 256, and 257 distinct protected submissions in each scope; after admission at capacity, assert at most 256 retained receipts and a committed cutoff that retires every timestamp tie. Test identities just inside, on, and outside the one-hour age boundary under the reviewed precision/skew rules; none older than the allowed age can execute. Pressure one session/catalog without changing another scope's budget or cutoff. |
| API retry recovery | Use equal timestamps and out-of-order clients; lose a response, change owner, and roll the clock back. Reject expired/unseen old requests, preserve original cancellation targets, and reject changed-command reuse. Unprotected ambiguous requests are not blindly replayed. |

## Open contract mechanics

Open decisions are grouped by review topic rather than maintained as a second backlog:

- [Execution representation, assembly, and batching](execution-and-recovery.md#open-decisions-and-detail).
- [Independent result lifetimes, deadlines, and predecessor/successor atomic bundling](cancellation-and-results.md#open-decisions-and-detail).
- [Authorization binding and safe hook evidence](authorization-and-hooks.md#open-decisions-and-detail).
- [Logical-input and provider replay interfaces](model-continuation.md#open-decisions-and-detail).
- [Command, control, and catalog interfaces](commands-and-lifecycle.md#open-decisions-and-detail).
- [Storage, projection, extension, and compatibility interfaces](storage-and-observation.md#open-decisions-and-detail).

Close these with worked sequences and conformance cases before finalizing interfaces or an implementation breakdown.

## Related information

- [Design proposal](session-commit-history.md)
- [History/execution model](session-commit-history.modelith.md)
- [Catalog/command model](session-command-protocol.modelith.md)
- [Durable-execution and recovery foundations](session-commit-history-durable-execution-prior-art.md)
- [Harness prior-art comparisons](session-commit-history-prior-art.md)
